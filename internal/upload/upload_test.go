package upload

import (
	"archive/zip"
	"bytes"
	"context"
	"errors"
	"io"
	"io/fs"
	"mime/multipart"
	"net/textproto"
	"os"
	"strings"
	"testing"

	"github.com/joshuadavidthomas/ts-skill-registry/internal/agentskill"
	"github.com/joshuadavidthomas/ts-skill-registry/internal/safetree"
)

var testSkill = []byte("---\nname: sample\ndescription: Sample skill\n---\n# Instructions\nDo nothing.\n")

type zipEntry struct {
	name string
	body []byte
	mode fs.FileMode
}

func makeZIP(t *testing.T, entries ...zipEntry) []byte {
	t.Helper()
	var output bytes.Buffer
	writer := zip.NewWriter(&output)
	for _, entry := range entries {
		header := &zip.FileHeader{Name: entry.name, Method: zip.Deflate}
		if entry.mode != 0 {
			header.SetMode(entry.mode)
		}
		file, err := writer.CreateHeader(header)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := file.Write(entry.body); err != nil {
			t.Fatal(err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	return output.Bytes()
}

type directoryPart struct {
	name     string
	filename string
	body     string
}

func directoryReader(t *testing.T, parts ...directoryPart) *multipart.Reader {
	t.Helper()
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	boundary := writer.Boundary()
	for _, part := range parts {
		var destination interface{ Write([]byte) (int, error) }
		if part.filename == "" {
			created, err := writer.CreateFormField(part.name)
			if err != nil {
				t.Fatal(err)
			}
			destination = created
		} else {
			header := make(textproto.MIMEHeader)
			header.Set("Content-Disposition", `form-data; name="`+part.name+`"; filename="`+part.filename+`"`)
			header.Set("Content-Type", "application/octet-stream")
			created, err := writer.CreatePart(header)
			if err != nil {
				t.Fatal(err)
			}
			destination = created
		}
		if _, err := destination.Write([]byte(part.body)); err != nil {
			t.Fatal(err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	return multipart.NewReader(bytes.NewReader(body.Bytes()), boundary)
}

func TestEquivalentZIPAndBrowserDirectoryHaveSameDigest(t *testing.T) {
	limits := safetree.PrototypeLimits()
	zipSubmission, err := StageZIP(context.Background(), t.TempDir(), bytes.NewReader(makeZIP(t,
		zipEntry{name: "SKILL.md", body: testSkill},
		zipEntry{name: "assets/data.txt", body: []byte("asset")},
	)), `C:\fakepath\skill.zip`, limits)
	if err != nil {
		t.Fatal(err)
	}
	defer zipSubmission.Close()
	if zipSubmission.Root() != "sample" || zipSubmission.Label() != "skill.zip" {
		t.Fatalf("ZIP root/label = %q/%q", zipSubmission.Root(), zipSubmission.Label())
	}

	directorySubmission, err := StageBrowserDirectory(context.Background(), t.TempDir(), directoryReader(t,
		directoryPart{name: "manifest", body: `[{"index":0,"path":"sample/SKILL.md","size":74},{"index":1,"path":"sample/assets/data.txt","size":5}]`},
		directoryPart{name: "file-0", filename: "ignored.md", body: string(testSkill)},
		directoryPart{name: "file-1", filename: "also-ignored.txt", body: "asset"},
	), limits)
	if err != nil {
		t.Fatal(err)
	}
	defer directorySubmission.Close()

	zipDigest, err := agentskill.SumTree(zipSubmission.FS(), zipSubmission.Root())
	if err != nil {
		t.Fatal(err)
	}
	directoryDigest, err := agentskill.SumTree(directorySubmission.FS(), directorySubmission.Root())
	if err != nil {
		t.Fatal(err)
	}
	if zipDigest != directoryDigest {
		t.Fatalf("ZIP digest %s != directory digest %s", zipDigest, directoryDigest)
	}
	if got, err := fs.ReadFile(directorySubmission.FS(), "sample/assets/data.txt"); err != nil || string(got) != "asset" {
		t.Fatalf("directory asset = %q, %v", got, err)
	}
}

func TestZIPAcceptsOneMatchingWrapper(t *testing.T) {
	submission, err := StageZIP(context.Background(), t.TempDir(), bytes.NewReader(makeZIP(t,
		zipEntry{name: "sample/", mode: fs.ModeDir | 0o755},
		zipEntry{name: "sample/SKILL.md", body: testSkill},
	)), "", safetree.PrototypeLimits())
	if err != nil {
		t.Fatal(err)
	}
	defer submission.Close()
	if submission.Root() != "sample" || submission.Label() != "upload.zip" {
		t.Fatalf("root/label = %q/%q", submission.Root(), submission.Label())
	}
}

func TestZIPRejectsMalformedPathsLinksRootsAndCollisions(t *testing.T) {
	tests := map[string][]zipEntry{
		"traversal": {{name: "../SKILL.md", body: testSkill}},
		"absolute":  {{name: "/SKILL.md", body: testSkill}},
		"windows":   {{name: `sample\SKILL.md`, body: testSkill}},
		"drive":     {{name: "C:/SKILL.md", body: testSkill}},
		"link": {
			{name: "sample/SKILL.md", body: testSkill},
			{name: "sample/link", body: []byte("target"), mode: fs.ModeSymlink | 0o777},
		},
		"duplicate": {
			{name: "sample/SKILL.md", body: testSkill},
			{name: "sample/SKILL.md", body: testSkill},
		},
		"prefix": {
			{name: "sample/SKILL.md", body: testSkill},
			{name: "sample/file", body: []byte("file")},
			{name: "sample/file/child", body: []byte("child")},
		},
		"ambiguous": {
			{name: "first/SKILL.md", body: testSkill},
			{name: "second/SKILL.md", body: testSkill},
		},
		"outside-wrapper": {
			{name: "sample/SKILL.md", body: testSkill},
			{name: "outside.txt", body: []byte("outside")},
		},
	}
	for name, entries := range tests {
		t.Run(name, func(t *testing.T) {
			_, err := StageZIP(context.Background(), t.TempDir(), bytes.NewReader(makeZIP(t, entries...)), "skill.zip", safetree.PrototypeLimits())
			if !errors.Is(err, ErrMalformedUpload) {
				t.Fatalf("error = %v, want ErrMalformedUpload", err)
			}
		})
	}
}

func TestZIPRejectsEncryptedEntry(t *testing.T) {
	archive := makeZIP(t, zipEntry{name: "SKILL.md", body: testSkill})
	for index := 0; index+10 < len(archive); index++ {
		switch {
		case bytes.Equal(archive[index:index+4], []byte{'P', 'K', 3, 4}):
			archive[index+6] |= 1
		case bytes.Equal(archive[index:index+4], []byte{'P', 'K', 1, 2}):
			archive[index+8] |= 1
		}
	}
	_, err := StageZIP(context.Background(), t.TempDir(), bytes.NewReader(archive), "encrypted.zip", safetree.PrototypeLimits())
	if !errors.Is(err, ErrMalformedUpload) {
		t.Fatalf("error = %v, want ErrMalformedUpload", err)
	}
}

func TestZIPAndDirectoryEnforceLimits(t *testing.T) {
	limits := safetree.PrototypeLimits()
	limits.MaxFileBytes = 4
	limits.MaxExpandedBytes = 8
	_, err := StageZIP(context.Background(), t.TempDir(), bytes.NewReader(makeZIP(t,
		zipEntry{name: "SKILL.md", body: testSkill},
	)), "skill.zip", limits)
	if !errors.Is(err, safetree.ErrLimitExceeded) {
		t.Fatalf("ZIP error = %v, want limit", err)
	}

	limits = safetree.PrototypeLimits()
	limits.MaxPathBytes = 16
	_, err = StageBrowserDirectory(context.Background(), t.TempDir(), directoryReader(t,
		directoryPart{name: "manifest", body: `[{"index":0,"path":"sample/a-very-long-file","size":1}]`},
		directoryPart{name: "file-0", body: "x"},
	), limits)
	if !errors.Is(err, safetree.ErrLimitExceeded) {
		t.Fatalf("directory error = %v, want limit", err)
	}
}

func TestEveryConfiguredTreeLimitIsEnforced(t *testing.T) {
	t.Run("files", func(t *testing.T) {
		limits := safetree.PrototypeLimits()
		limits.MaxFiles = 1
		_, err := StageBrowserDirectory(context.Background(), t.TempDir(), directoryReader(t,
			directoryPart{name: "manifest", body: `[{"index":0,"path":"sample/one","size":1},{"index":1,"path":"sample/two","size":1}]`},
		), limits)
		if !errors.Is(err, safetree.ErrLimitExceeded) {
			t.Fatalf("error = %v, want file limit", err)
		}
	})
	t.Run("depth", func(t *testing.T) {
		limits := safetree.PrototypeLimits()
		limits.MaxDepth = 2
		_, err := StageBrowserDirectory(context.Background(), t.TempDir(), directoryReader(t,
			directoryPart{name: "manifest", body: `[{"index":0,"path":"sample/nested/file","size":1}]`},
		), limits)
		if !errors.Is(err, safetree.ErrLimitExceeded) {
			t.Fatalf("error = %v, want depth limit", err)
		}
	})
	t.Run("expanded bytes", func(t *testing.T) {
		limits := safetree.PrototypeLimits()
		limits.MaxFileBytes = 4
		limits.MaxExpandedBytes = 5
		_, err := StageBrowserDirectory(context.Background(), t.TempDir(), directoryReader(t,
			directoryPart{name: "manifest", body: `[{"index":0,"path":"sample/one","size":3},{"index":1,"path":"sample/two","size":3}]`},
			directoryPart{name: "file-0", filename: "one", body: "one"},
			directoryPart{name: "file-1", filename: "two", body: "two"},
		), limits)
		if !errors.Is(err, safetree.ErrLimitExceeded) {
			t.Fatalf("error = %v, want expanded-byte limit", err)
		}
	})
	t.Run("ZIP request bytes", func(t *testing.T) {
		_, err := StageZIP(context.Background(), t.TempDir(), io.LimitReader(repeatingZeroReader{}, maxZIPBytes+1), "huge.zip", safetree.PrototypeLimits())
		if !errors.Is(err, safetree.ErrLimitExceeded) {
			t.Fatalf("error = %v, want request-byte limit", err)
		}
	})
}

type repeatingZeroReader struct{}

func (repeatingZeroReader) Read(dst []byte) (int, error) {
	for index := range dst {
		dst[index] = 0
	}
	return len(dst), nil
}

func TestBrowserDirectoryRejectsMalformedManifestAndPartSequence(t *testing.T) {
	tests := map[string][]directoryPart{
		"manifest-not-first": {
			{name: "file-0", body: "x"},
		},
		"unordered-index": {
			{name: "manifest", body: `[{"index":1,"path":"sample/SKILL.md","size":1}]`},
			{name: "file-1", body: "x"},
		},
		"unknown-field": {
			{name: "manifest", body: `[{"index":0,"path":"sample/SKILL.md","size":1,"other":true}]`},
			{name: "file-0", body: "x"},
		},
		"multiple-roots": {
			{name: "manifest", body: `[{"index":0,"path":"sample/SKILL.md","size":1},{"index":1,"path":"other/file","size":1}]`},
			{name: "file-0", body: "x"}, {name: "file-1", body: "y"},
		},
		"traversal": {
			{name: "manifest", body: `[{"index":0,"path":"sample/../SKILL.md","size":1}]`},
			{name: "file-0", body: "x"},
		},
		"wrong-part": {
			{name: "manifest", body: `[{"index":0,"path":"sample/SKILL.md","size":1}]`},
			{name: "file-1", body: "x"},
		},
		"wrong-size": {
			{name: "manifest", body: `[{"index":0,"path":"sample/SKILL.md","size":2}]`},
			{name: "file-0", filename: "SKILL.md", body: "x"},
		},
		"collision": {
			{name: "manifest", body: `[{"index":0,"path":"sample/file","size":1},{"index":1,"path":"sample/file/child","size":1}]`},
			{name: "file-0", filename: "file", body: "x"}, {name: "file-1", filename: "child", body: "y"},
		},
		"extra-part": {
			{name: "manifest", body: `[{"index":0,"path":"sample/SKILL.md","size":1}]`},
			{name: "file-0", filename: "SKILL.md", body: "x"}, {name: "extra", body: "y"},
		},
	}
	for name, parts := range tests {
		t.Run(name, func(t *testing.T) {
			_, err := StageBrowserDirectory(context.Background(), t.TempDir(), directoryReader(t, parts...), safetree.PrototypeLimits())
			if !errors.Is(err, ErrMalformedUpload) {
				t.Fatalf("error = %v, want ErrMalformedUpload", err)
			}
		})
	}
}

func TestSubmissionOwnsAndRemovesSnapshot(t *testing.T) {
	parent := t.TempDir()
	submission, err := StageZIP(context.Background(), parent, bytes.NewReader(makeZIP(t,
		zipEntry{name: "SKILL.md", body: testSkill},
	)), "skill.zip", safetree.PrototypeLimits())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fs.ReadFile(submission.FS(), "sample/SKILL.md"); err != nil {
		t.Fatal(err)
	}
	if err := submission.Close(); err != nil {
		t.Fatal(err)
	}
	if err := submission.Close(); err != nil {
		t.Fatalf("second close: %v", err)
	}
	entries, err := os.ReadDir(parent)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), ".ts-skills-") {
			t.Fatalf("owned staging remains: %s", entry.Name())
		}
	}
}

func TestSubmissionCloseRetainsSnapshotAfterFailure(t *testing.T) {
	submission, err := StageZIP(context.Background(), t.TempDir(), bytes.NewReader(makeZIP(t,
		zipEntry{name: "SKILL.md", body: testSkill},
	)), "skill.zip", safetree.PrototypeLimits())
	if err != nil {
		t.Fatal(err)
	}
	injected := errors.New("injected snapshot close failure")
	submission.closeSnapshot = func(*safetree.Snapshot) error { return injected }
	if err := submission.Close(); !errors.Is(err, injected) {
		t.Fatalf("first Close error = %v, want injected failure", err)
	}
	if submission.snapshot == nil {
		t.Fatal("failed Close released submission snapshot ownership")
	}
	if _, err := fs.ReadFile(submission.FS(), "sample/SKILL.md"); err != nil {
		t.Fatalf("submission after failed Close: %v", err)
	}
	submission.closeSnapshot = nil
	if err := submission.Close(); err != nil {
		t.Fatal(err)
	}
	if submission.snapshot != nil {
		t.Fatal("successful Close retained submission snapshot ownership")
	}
}
