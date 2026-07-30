package treearchive

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"testing"
	"testing/fstest"

	"github.com/joshuadavidthomas/ts-skills/internal/safetree"
)

func TestMaxBytesCoversPrototypeLimits(t *testing.T) {
	const payload = 128 << 20
	const entries = 2048
	const names = 1024
	want := int64(payload + entries*(256+2*names) + 22)
	if MaxBytes != want {
		t.Fatalf("MaxBytes = %d, want %d", MaxBytes, want)
	}
}

func TestEncodeDecodeRoundTrip(t *testing.T) {
	files := fstest.MapFS{
		"z.txt":           {Data: []byte("last")},
		"SKILL.md":        {Data: []byte("skill")},
		"assets/data.txt": {Data: []byte("asset")},
	}
	var encoded bytes.Buffer
	if err := Encode(context.Background(), &encoded, files); err != nil {
		t.Fatal(err)
	}
	archive, err := zip.NewReader(bytes.NewReader(encoded.Bytes()), int64(encoded.Len()))
	if err != nil {
		t.Fatal(err)
	}
	wantNames := []string{"SKILL.md", "assets/data.txt", "z.txt"}
	if len(archive.File) != len(wantNames) {
		t.Fatalf("archive entries = %d, want %d", len(archive.File), len(wantNames))
	}
	for index, entry := range archive.File {
		if entry.Name != wantNames[index] {
			t.Fatalf("archive entry %d = %q, want %q", index, entry.Name, wantNames[index])
		}
		if entry.Method != zip.Store {
			t.Fatalf("archive entry %q method = %d, want %d", entry.Name, entry.Method, zip.Store)
		}
		if entry.Mode().Perm() != 0o644 {
			t.Fatalf("archive entry %q mode = %o, want 644", entry.Name, entry.Mode().Perm())
		}
	}

	archivePath := filepath.Join(t.TempDir(), "tree.zip")
	if err := os.WriteFile(archivePath, encoded.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
	snapshot, err := Decode(context.Background(), archivePath, t.TempDir(), safetree.PrototypeLimits())
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := snapshot.Close(); err != nil {
			t.Error(err)
		}
	}()
	for name, file := range files {
		got, err := fs.ReadFile(snapshot.FS(), name)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(got, file.Data) {
			t.Fatalf("decoded %q = %q, want %q", name, got, file.Data)
		}
	}
}

func TestDecodeRejectsInvalidFormatsAndLimits(t *testing.T) {
	valid := encodeTestArchive(t, zip.Store)
	end := bytes.LastIndex(valid, []byte{'P', 'K', 5, 6})
	if end < 0 {
		t.Fatal("test ZIP has no end record")
	}
	underreported := bytes.Clone(valid)
	binary.LittleEndian.PutUint16(underreported[end+8:end+10], 1)
	binary.LittleEndian.PutUint16(underreported[end+10:end+12], 1)
	zip64 := bytes.Clone(valid)
	binary.LittleEndian.PutUint16(zip64[end+8:end+10], ^uint16(0))
	binary.LittleEndian.PutUint16(zip64[end+10:end+12], ^uint16(0))

	limited := safetree.PrototypeLimits()
	limited.MaxFiles = 1
	tests := map[string]struct {
		archive []byte
		limits  safetree.Limits
		want    error
	}{
		"too many entries":      {archive: valid, limits: limited, want: safetree.ErrLimitExceeded},
		"underreported entries": {archive: underreported, limits: safetree.PrototypeLimits(), want: ErrInvalid},
		"ZIP64":                 {archive: zip64, limits: safetree.PrototypeLimits(), want: ErrInvalid},
		"deflated entry":        {archive: encodeTestArchive(t, zip.Deflate), limits: safetree.PrototypeLimits(), want: ErrInvalid},
	}
	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			archivePath := filepath.Join(t.TempDir(), "tree.zip")
			if err := os.WriteFile(archivePath, test.archive, 0o600); err != nil {
				t.Fatal(err)
			}
			_, err := Decode(context.Background(), archivePath, t.TempDir(), test.limits)
			if !errors.Is(err, test.want) {
				t.Fatalf("Decode error = %v, want errors.Is %v", err, test.want)
			}
			if test.want == ErrInvalid && errors.Is(err, safetree.ErrLimitExceeded) {
				t.Fatalf("Decode error = %v, want format rejection", err)
			}
		})
	}
}

func TestDecodePreservesCancellation(t *testing.T) {
	archivePath := filepath.Join(t.TempDir(), "tree.zip")
	if err := os.WriteFile(archivePath, encodeTestArchive(t, zip.Store), 0o600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := Decode(ctx, archivePath, t.TempDir(), safetree.PrototypeLimits())
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Decode error = %v, want context.Canceled", err)
	}
	if errors.Is(err, ErrInvalid) {
		t.Fatalf("Decode error flattened into ErrInvalid: %v", err)
	}
}

type cancelAfterReadTree struct {
	files  fstest.MapFS
	cancel context.CancelFunc
}

func (t cancelAfterReadTree) Open(name string) (fs.File, error) {
	file, err := t.files.Open(name)
	if err != nil || name == "." {
		return file, err
	}
	return &cancelAfterReadTreeFile{File: file, cancel: t.cancel}, nil
}

func (t cancelAfterReadTree) ReadDir(name string) ([]fs.DirEntry, error) {
	return t.files.ReadDir(name)
}

type cancelAfterReadTreeFile struct {
	fs.File
	cancelled bool
	cancel    context.CancelFunc
}

func (t *cancelAfterReadTreeFile) Read(buffer []byte) (int, error) {
	read, err := t.File.Read(buffer)
	if read > 0 && !t.cancelled {
		t.cancelled = true
		t.cancel()
	}
	return read, err
}

func TestEncodePreservesCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	tree := cancelAfterReadTree{
		files:  fstest.MapFS{"large": {Data: bytes.Repeat([]byte("x"), 128<<10)}},
		cancel: cancel,
	}
	var encoded bytes.Buffer
	if err := Encode(ctx, &encoded, tree); !errors.Is(err, context.Canceled) {
		t.Fatalf("Encode error = %v, want context.Canceled", err)
	}
}

func encodeTestArchive(t *testing.T, method uint16) []byte {
	t.Helper()
	var archive bytes.Buffer
	writer := zip.NewWriter(&archive)
	for _, name := range []string{"SKILL.md", "assets/data.txt"} {
		entry, err := writer.CreateHeader(&zip.FileHeader{Name: name, Method: method})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := fmt.Fprint(entry, name); err != nil {
			t.Fatal(err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	return archive.Bytes()
}
