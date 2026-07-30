package tree

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"testing"
	"testing/fstest"
)

func TestStageCopiesDurableTreeAndTransfersOwnership(t *testing.T) {
	snapshot, err := Stage(context.Background(), t.TempDir(), ".stage-", fstest.MapFS{
		"SKILL.md":        {Data: []byte("skill")},
		"assets/data.txt": {Data: []byte("asset")},
	})
	if err != nil {
		t.Fatal(err)
	}
	if got, err := fs.ReadFile(snapshot.FS(), "assets/data.txt"); err != nil || string(got) != "asset" {
		t.Fatalf("staged asset = %q, %v", got, err)
	}
	path, err := snapshot.TakePath()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := snapshot.FS().Open("SKILL.md"); !errors.Is(err, fs.ErrClosed) {
		t.Fatalf("FS after TakePath error = %v, want %v", err, fs.ErrClosed)
	}
	if err := snapshot.Close(); err != nil {
		t.Fatal(err)
	}
	if got, err := os.ReadFile(filepath.Join(path, "SKILL.md")); err != nil || string(got) != "skill" {
		t.Fatalf("transferred file = %q, %v", got, err)
	}
}

func TestStageRejectsUnsupportedEntriesAndCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := Stage(ctx, t.TempDir(), ".stage-", fstest.MapFS{"file": {Data: []byte("data")}}); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled Stage error = %v, want %v", err, context.Canceled)
	}

	if _, err := Stage(context.Background(), t.TempDir(), ".stage-", fstest.MapFS{
		"link": {Mode: fs.ModeSymlink},
	}); err == nil {
		t.Fatal("Stage accepted a symbolic link")
	}
}
