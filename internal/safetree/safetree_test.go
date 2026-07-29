package safetree

import (
	"bytes"
	"context"
	"errors"
	"io/fs"
	"os"
	"testing"
)

func TestBuilderRejectsPathsAndCollisionsBeforeWriting(t *testing.T) {
	builder, err := NewBuilder(t.TempDir(), PrototypeLimits())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = builder.Close() }()
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
	if err := builder.AddFile(context.Background(), "nested/child", 1, bytes.NewReader([]byte("x"))); err != nil {
		t.Fatal(err)
	}
	if err := builder.AddFile(context.Background(), "nested", 1, bytes.NewReader([]byte("x"))); !errors.Is(err, ErrInvalidPath) {
		t.Fatalf("descendant collision error = %v", err)
	}
}

func TestBuilderRejectsWindowsReservedNamesOnAllPlatforms(t *testing.T) {
	builder, err := NewBuilder(t.TempDir(), PrototypeLimits())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = builder.Close() }()

	for _, name := range []string{"CON.txt", "con", "LPT1.md", "trailing.", "trailing "} {
		err := builder.AddFile(context.Background(), name, 1, bytes.NewReader([]byte("x")))
		if !errors.Is(err, ErrInvalidPath) {
			t.Errorf("AddFile(%q) error = %v, want ErrInvalidPath", name, err)
		}
	}
}

func TestBuilderRejectsCaseAliasOnAllPlatforms(t *testing.T) {
	builder, err := NewBuilder(t.TempDir(), PrototypeLimits())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = builder.Close() }()

	if err := builder.AddFile(context.Background(), "Readme.md", 1, bytes.NewReader([]byte("x"))); err != nil {
		t.Fatal(err)
	}
	if err := builder.AddFile(context.Background(), "README.md", 1, bytes.NewReader([]byte("x"))); !errors.Is(err, ErrInvalidPath) {
		t.Fatalf("case alias error = %v, want ErrInvalidPath", err)
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
	defer func() { _ = builder.Close() }()
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

func TestBuilderRejectsDeclaredSizeMismatch(t *testing.T) {
	builder, err := NewBuilder(t.TempDir(), PrototypeLimits())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = builder.Close() }()

	for _, test := range []struct {
		name     string
		declared int64
		contents string
	}{
		{name: "short", declared: 3, contents: "ab"},
		{name: "long", declared: 1, contents: "ab"},
	} {
		t.Run(test.name, func(t *testing.T) {
			err := builder.AddFile(context.Background(), test.name, test.declared, bytes.NewBufferString(test.contents))
			if !errors.Is(err, ErrSizeMismatch) {
				t.Fatalf("AddFile() error = %v, want ErrSizeMismatch", err)
			}
		})
	}
	if err := builder.AddFile(context.Background(), "exact", 2, bytes.NewBufferString("ab")); err != nil {
		t.Fatalf("AddFile() after mismatch: %v", err)
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
	if _, err := snapshot.FS().Open("."); !errors.Is(err, fs.ErrClosed) {
		t.Fatalf("closed snapshot FS = %v, want fs.ErrClosed", err)
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
	if _, err := snapshot.FS().Open("."); !errors.Is(err, fs.ErrClosed) {
		t.Fatalf("snapshot FS after retried Close = %v, want fs.ErrClosed", err)
	}
}

func TestSnapshotFSNilZeroAndClosedNeverResolveAmbientFiles(t *testing.T) {
	var nilSnapshot *Snapshot
	if _, err := nilSnapshot.FS().Open("SKILL.md"); !errors.Is(err, fs.ErrClosed) {
		t.Fatalf("nil snapshot FS = %v, want fs.ErrClosed", err)
	}
	var zeroSnapshot Snapshot
	if _, err := zeroSnapshot.FS().Open("SKILL.md"); !errors.Is(err, fs.ErrClosed) {
		t.Fatalf("zero snapshot FS = %v, want fs.ErrClosed", err)
	}
	if err := zeroSnapshot.Close(); err != nil {
		t.Fatalf("zero snapshot Close = %v", err)
	}

	builder, err := NewBuilder(t.TempDir(), PrototypeLimits())
	if err != nil {
		t.Fatal(err)
	}
	if err := builder.AddFile(context.Background(), "SKILL.md", 4, bytes.NewReader([]byte("data"))); err != nil {
		t.Fatal(err)
	}
	snapshot, err := builder.Finish()
	if err != nil {
		t.Fatal(err)
	}
	if got, err := fs.ReadFile(snapshot.FS(), "SKILL.md"); err != nil || string(got) != "data" {
		t.Fatalf("live snapshot file = %q, %v", got, err)
	}
	if err := snapshot.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := fs.Stat(snapshot.FS(), "SKILL.md"); !errors.Is(err, fs.ErrClosed) {
		t.Fatalf("closed snapshot FS = %v, want fs.ErrClosed", err)
	}
}

func TestInvalidWindowsPathComponent(t *testing.T) {
	valid := []string{
		"file",
		"report.txt",
		".hidden",
		"clock",
		"console",
		"com0",
		"com10",
		"com⁴",
		"lpt0",
		"COMLPT1",
	}
	for _, component := range valid {
		if InvalidWindowsPathComponent(component) {
			t.Errorf("InvalidWindowsPathComponent(%q) = true, want false", component)
		}
	}

	invalid := []string{
		"name<alias",
		"name>alias",
		`name"alias`,
		"name:stream",
		"name/alias",
		`name\alias`,
		"name|alias",
		"name?alias",
		"name*alias",
		"name\x00alias",
		"name\x1falias",
		"name.",
		"name ",
		"CON",
		"con.txt",
		"CoN .txt",
		"PRN",
		"aux.log",
		"NUL",
		"clock$",
		"CLOCK$.txt",
		"conin$",
		"CONOUT$.log",
		"COM1",
		"com9.txt",
		"LPT1",
		"lpt9.log",
		"COM¹",
		"COM².txt",
		"com³.log",
		"LPT¹",
		"LPT².txt",
		"lpt³.log",
	}
	for _, component := range invalid {
		if !InvalidWindowsPathComponent(component) {
			t.Errorf("InvalidWindowsPathComponent(%q) = false, want true", component)
		}
	}
}

func TestCanonicalPathFoldsCaseAliases(t *testing.T) {
	for _, aliases := range [][2]string{
		{"SKILL.md", "skill.MD"},
		{"Assets/Icon.svg", "assets/icon.SVG"},
		{"Ångström", "ångström"},
	} {
		if first, second := canonicalPath(aliases[0]), canonicalPath(aliases[1]); first != second {
			t.Errorf("canonical aliases %q and %q differ: %q != %q", aliases[0], aliases[1], first, second)
		}
	}
	if parent := canonicalPath("Assets/Icon.svg")[:len("assets")]; parent != canonicalPath("ASSETS") {
		t.Errorf("canonical prefix = %q, want %q", parent, canonicalPath("ASSETS"))
	}
}
