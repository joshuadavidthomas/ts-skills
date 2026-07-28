package upload

import (
	"bytes"
	"context"
	"errors"
	"io/fs"
	"mime/multipart"
	"net/textproto"
	"os"
	"strings"
	"testing"

	"github.com/joshuadavidthomas/ts-skills/internal/safetree"
)

var testSkill = []byte("---\nname: sample\ndescription: Sample skill\n---\n# Instructions\nDo nothing.\n")

type directoryPart struct {
	name     string
	filename string
	body     string
}

func stageDirectory(t *testing.T, parent string, limits safetree.Limits, parts ...directoryPart) (*Submission, error) {
	t.Helper()
	reader := directoryReader(t, parts...)
	first, err := reader.NextPart()
	if err != nil {
		t.Fatal(err)
	}
	return StageBrowserDirectory(context.Background(), parent, first, reader, limits)
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

func TestBrowserDirectoryStagesCompleteTree(t *testing.T) {
	submission, err := stageDirectory(t, t.TempDir(), safetree.PrototypeLimits(),
		directoryPart{name: "manifest", body: `[{"index":0,"path":"sample/SKILL.md","size":74},{"index":1,"path":"sample/assets/data.txt","size":5}]`},
		directoryPart{name: "file-0", filename: "ignored.md", body: string(testSkill)},
		directoryPart{name: "file-1", filename: "also-ignored.txt", body: "asset"},
	)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = submission.Close() }()
	if submission.Root() != "sample" || submission.Label() != "sample" {
		t.Fatalf("directory root/label = %q/%q", submission.Root(), submission.Label())
	}
	if got, err := fs.ReadFile(submission.FS(), "sample/assets/data.txt"); err != nil || string(got) != "asset" {
		t.Fatalf("directory asset = %q, %v", got, err)
	}
}

func TestDirectoryEnforcesLimits(t *testing.T) {
	limits := safetree.PrototypeLimits()
	limits.MaxPathBytes = 16
	_, err := stageDirectory(t, t.TempDir(), limits,
		directoryPart{name: "manifest", body: `[{"index":0,"path":"sample/a-very-long-file","size":1}]`},
		directoryPart{name: "file-0", body: "x"},
	)
	if !errors.Is(err, safetree.ErrLimitExceeded) {
		t.Fatalf("directory error = %v, want limit", err)
	}
}

func TestEveryConfiguredTreeLimitIsEnforced(t *testing.T) {
	t.Run("files", func(t *testing.T) {
		limits := safetree.PrototypeLimits()
		limits.MaxFiles = 1
		_, err := stageDirectory(t, t.TempDir(), limits,
			directoryPart{name: "manifest", body: `[{"index":0,"path":"sample/one","size":1},{"index":1,"path":"sample/two","size":1}]`},
		)
		if !errors.Is(err, safetree.ErrLimitExceeded) {
			t.Fatalf("error = %v, want file limit", err)
		}
	})
	t.Run("depth", func(t *testing.T) {
		limits := safetree.PrototypeLimits()
		limits.MaxDepth = 2
		_, err := stageDirectory(t, t.TempDir(), limits,
			directoryPart{name: "manifest", body: `[{"index":0,"path":"sample/nested/file","size":1}]`},
		)
		if !errors.Is(err, safetree.ErrLimitExceeded) {
			t.Fatalf("error = %v, want depth limit", err)
		}
	})
	t.Run("expanded bytes", func(t *testing.T) {
		limits := safetree.PrototypeLimits()
		limits.MaxFileBytes = 4
		limits.MaxExpandedBytes = 5
		_, err := stageDirectory(t, t.TempDir(), limits,
			directoryPart{name: "manifest", body: `[{"index":0,"path":"sample/one","size":3},{"index":1,"path":"sample/two","size":3}]`},
			directoryPart{name: "file-0", filename: "one", body: "one"},
			directoryPart{name: "file-1", filename: "two", body: "two"},
		)
		if !errors.Is(err, safetree.ErrLimitExceeded) {
			t.Fatalf("error = %v, want expanded-byte limit", err)
		}
	})
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
			_, err := stageDirectory(t, t.TempDir(), safetree.PrototypeLimits(), parts...)
			if !errors.Is(err, ErrMalformedUpload) {
				t.Fatalf("error = %v, want ErrMalformedUpload", err)
			}
		})
	}
}

func TestSubmissionOwnsAndRemovesSnapshot(t *testing.T) {
	parent := t.TempDir()
	submission, err := stageDirectory(t, parent, safetree.PrototypeLimits(),
		directoryPart{name: "manifest", body: `[{"index":0,"path":"sample/SKILL.md","size":74}]`},
		directoryPart{name: "file-0", filename: "SKILL.md", body: string(testSkill)},
	)
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
	submission, err := stageDirectory(t, t.TempDir(), safetree.PrototypeLimits(),
		directoryPart{name: "manifest", body: `[{"index":0,"path":"sample/SKILL.md","size":74}]`},
		directoryPart{name: "file-0", filename: "SKILL.md", body: string(testSkill)},
	)
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
