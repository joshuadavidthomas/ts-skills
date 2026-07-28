package safetree

import (
	"bytes"
	"context"
	"errors"
	"io/fs"
	"os"
	"testing"
	"testing/fstest"
)

func TestBuilderRejectsPathsAndCollisionsBeforeWriting(t *testing.T) {
	builder, err := NewBuilder(t.TempDir(), PrototypeLimits())
	if err != nil {
		t.Fatal(err)
	}
	defer builder.Close()
	for _, name := range []string{"../escape", "/absolute", "a\\b", "a//b", "."} {
		if err := builder.AddFile(context.Background(), name, 1, bytes.NewReader([]byte("x"))); !errors.Is(err, ErrInvalidPath) {
			t.Errorf("AddFile(%q) error = %v, want ErrInvalidPath", name, err)
		}
	}
	if err := builder.AddFile(context.Background(), "a", 1, bytes.NewReader([]byte("x"))); err != nil {
		t.Fatal(err)
	}
	if err := builder.AddFile(context.Background(), "a/b", 1, bytes.NewReader([]byte("x"))); !errors.Is(err, ErrInvalidPath) {
		t.Fatalf("prefix collision error = %v", err)
	}
}

func TestBuilderUsesActualBytesForLimits(t *testing.T) {
	limits := PrototypeLimits()
	limits.MaxFileBytes = 3
	limits.MaxExpandedBytes = 4
	builder, err := NewBuilder(t.TempDir(), limits)
	if err != nil {
		t.Fatal(err)
	}
	defer builder.Close()
	if err := builder.AddFile(context.Background(), "large", 1, bytes.NewReader([]byte("four"))); !errors.Is(err, ErrLimitExceeded) {
		t.Fatalf("actual size error = %v, want ErrLimitExceeded", err)
	}
	if err := builder.AddFile(context.Background(), "ok", 3, bytes.NewReader([]byte("abc"))); err != nil {
		t.Fatal(err)
	}
	if err := builder.AddFile(context.Background(), "total", 1, bytes.NewReader([]byte("zz"))); !errors.Is(err, ErrLimitExceeded) {
		t.Fatalf("expanded size error = %v, want ErrLimitExceeded", err)
	}
}

func TestFinishTransfersOwnership(t *testing.T) {
	parent := t.TempDir()
	builder, err := NewBuilder(parent, PrototypeLimits())
	if err != nil {
		t.Fatal(err)
	}
	if err := builder.AddFile(context.Background(), "file", 4, bytes.NewReader([]byte("data"))); err != nil {
		t.Fatal(err)
	}
	snapshot, err := builder.Finish()
	if err != nil {
		t.Fatal(err)
	}
	if err := builder.Close(); err != nil {
		t.Fatal(err)
	}
	if got, err := fs.ReadFile(snapshot.FS(), "file"); err != nil || string(got) != "data" {
		t.Fatalf("snapshot after builder close = %q, %v", got, err)
	}
	if err := snapshot.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := fs.Stat(snapshot.FS(), "."); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("snapshot remains after Close: %v", err)
	}
}

func TestBuilderCloseRetainsStagingAfterRemovalFailure(t *testing.T) {
	builder, err := NewBuilder(t.TempDir(), PrototypeLimits())
	if err != nil {
		t.Fatal(err)
	}
	staging := builder.path
	injected := errors.New("injected removal failure")
	builder.removeAll = func(string) error { return injected }
	if err := builder.Close(); !errors.Is(err, injected) {
		t.Fatalf("first Close error = %v, want injected failure", err)
	}
	if builder.closed || builder.path != staging {
		t.Fatal("failed Close released builder staging ownership")
	}
	if _, err := os.Stat(staging); err != nil {
		t.Fatalf("staging after failed Close: %v", err)
	}
	builder.removeAll = os.RemoveAll
	if err := builder.Close(); err != nil {
		t.Fatal(err)
	}
	if !builder.closed || builder.path != "" {
		t.Fatal("successful Close retained builder staging ownership")
	}
	if _, err := os.Stat(staging); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("staging after retried Close: %v", err)
	}
}

func TestSnapshotCloseRetainsStagingAfterRemovalFailure(t *testing.T) {
	builder, err := NewBuilder(t.TempDir(), PrototypeLimits())
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := builder.Finish()
	if err != nil {
		t.Fatal(err)
	}
	staging := snapshot.path
	injected := errors.New("injected removal failure")
	snapshot.removeAll = func(string) error { return injected }
	if err := snapshot.Close(); !errors.Is(err, injected) {
		t.Fatalf("first Close error = %v, want injected failure", err)
	}
	if snapshot.closed || snapshot.path != staging {
		t.Fatal("failed Close released snapshot staging ownership")
	}
	if _, err := fs.Stat(snapshot.FS(), "."); err != nil {
		t.Fatalf("snapshot after failed Close: %v", err)
	}
	snapshot.removeAll = os.RemoveAll
	if err := snapshot.Close(); err != nil {
		t.Fatal(err)
	}
	if !snapshot.closed {
		t.Fatal("successful Close did not mark snapshot closed")
	}
	if _, err := fs.Stat(snapshot.FS(), "."); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("staging after retried Close: %v", err)
	}
}

func TestStageFSPreservesRootAndRejectsLinks(t *testing.T) {
	source := fstest.MapFS{"skill/SKILL.md": &fstest.MapFile{Data: []byte("data")}}
	snapshot, err := StageFS(context.Background(), t.TempDir(), source, "skill", PrototypeLimits())
	if err != nil {
		t.Fatal(err)
	}
	defer snapshot.Close()
	if got, err := fs.ReadFile(snapshot.FS(), "skill/SKILL.md"); err != nil || string(got) != "data" {
		t.Fatalf("preserved root file = %q, %v", got, err)
	}

	linked := fstest.MapFS{"skill/link": &fstest.MapFile{Mode: os.ModeSymlink}}
	if _, err := StageFS(context.Background(), t.TempDir(), linked, "skill", PrototypeLimits()); !errors.Is(err, ErrInvalidPath) {
		t.Fatalf("symlink error = %v, want ErrInvalidPath", err)
	}
}
