package client

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRejectWindowsPathComponentsRejectsMissingReservedDestination(t *testing.T) {
	for _, name := range []string{"con", "NUL.txt", "COM²", "lpt9.log"} {
		destination := filepath.Join(t.TempDir(), ".agents", "skills", name)
		err := rejectWindowsPathComponents(destination)
		if err == nil {
			t.Errorf("rejectWindowsPathComponents(%q) accepted a reserved missing destination", destination)
		} else if !strings.Contains(err.Error(), name) {
			t.Errorf("rejectWindowsPathComponents(%q) error = %q, want component name", destination, err)
		}
	}
}

func TestProjectWriterRootContainsConcurrentSymlinkSwap(t *testing.T) {
	project, err := openProject(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	writer, err := project.acquireWriter(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = writer.close() })

	originalSkills := project.skillsDir() + "-original"
	if err := os.Rename(project.skillsDir(), originalSkills); err != nil {
		t.Fatal(err)
	}
	external := t.TempDir()
	source := filepath.Join(external, "source")
	if err := os.WriteFile(source, []byte("outside"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(external, project.skillsDir()); err != nil {
		t.Skipf("create concurrent path swap: %v", err)
	}

	err = writer.rename(filepath.Join(project.skillsDir(), "source"), filepath.Join(project.skillsDir(), "destination"))
	if err == nil {
		t.Fatal("root-relative rename escaped through swapped managed-directory symlink")
	}
	if contents, readErr := os.ReadFile(source); readErr != nil || string(contents) != "outside" {
		t.Fatalf("outside source changed: contents=%q error=%v", contents, readErr)
	}
	if _, statErr := os.Stat(filepath.Join(external, "destination")); !errors.Is(statErr, fs.ErrNotExist) {
		t.Fatalf("outside destination exists after rejected rename: %v", statErr)
	}
}

func TestRejectWindowsPathComponentsAcceptsOrdinaryMissingDestination(t *testing.T) {
	destination := filepath.Join(t.TempDir(), ".agents", "skills", "sample")
	if err := rejectWindowsPathComponents(destination); err != nil {
		t.Fatalf("rejectWindowsPathComponents(%q): %v", destination, err)
	}
}
