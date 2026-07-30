package tree

import (
	"bytes"
	"context"
	"errors"
	"testing"
	"testing/fstest"
)

func TestPortableTreeConsumersShareManifestValidation(t *testing.T) {
	files := fstest.MapFS{
		"A.txt": {Data: []byte("first")},
		"a.txt": {Data: []byte("second")},
	}
	if _, err := NewSource(context.Background(), files, ".", PrototypeLimits()); !errors.Is(err, ErrInvalidPath) {
		t.Fatalf("NewSource collision error = %v, want %v", err, ErrInvalidPath)
	}
	if _, err := Stage(context.Background(), t.TempDir(), ".stage-", files); !errors.Is(err, ErrInvalidPath) {
		t.Fatalf("Stage collision error = %v, want %v", err, ErrInvalidPath)
	}
	var archive bytes.Buffer
	if err := Encode(context.Background(), &archive, files); !errors.Is(err, ErrInvalidPath) {
		t.Fatalf("Encode collision error = %v, want %v", err, ErrInvalidPath)
	}
}

func TestManifestEnforcesAggregateLimits(t *testing.T) {
	limits := Limits{MaxFiles: 2, MaxPathBytes: 16, MaxDepth: 2, MaxFileBytes: 5, MaxExpandedBytes: 6}
	_, err := NewManifest([]File{
		{Path: "a", Size: 4},
		{Path: "b", Size: 3},
	}, limits)
	if !errors.Is(err, ErrLimitExceeded) {
		t.Fatalf("NewManifest aggregate error = %v, want %v", err, ErrLimitExceeded)
	}
}
