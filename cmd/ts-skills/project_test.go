package main

import (
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

func TestRejectWindowsPathComponentsAcceptsOrdinaryMissingDestination(t *testing.T) {
	destination := filepath.Join(t.TempDir(), ".agents", "skills", "sample")
	if err := rejectWindowsPathComponents(destination); err != nil {
		t.Fatalf("rejectWindowsPathComponents(%q): %v", destination, err)
	}
}
