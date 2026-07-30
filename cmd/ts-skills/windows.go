package main

import (
	"fmt"
	"path/filepath"

	"github.com/joshuadavidthomas/ts-skills/internal/tree"
)

func rejectWindowsPathComponents(name string) error {
	for current := filepath.Clean(name); ; current = filepath.Dir(current) {
		parent := filepath.Dir(current)
		if parent == current {
			return nil
		}
		component := filepath.Base(current)
		if tree.InvalidWindowsPathComponent(component) {
			return fmt.Errorf("managed path component %q is invalid on Windows", component)
		}
	}
}
