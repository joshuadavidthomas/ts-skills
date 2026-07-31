package client

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
)

type project struct{ root string }

func openProject(source string) (project, error) {
	if source == "" {
		return project{}, fmt.Errorf("project path must be explicit")
	}
	absolute, err := filepath.Abs(source)
	if err != nil {
		return project{}, fmt.Errorf("resolve project path: %w", err)
	}
	canonical, err := filepath.EvalSymlinks(absolute)
	if err != nil {
		return project{}, fmt.Errorf("resolve project path symlinks: %w", err)
	}
	if err := rejectPathComponents(canonical, false); err != nil {
		return project{}, fmt.Errorf("inspect project path: %w", err)
	}
	info, err := os.Lstat(canonical)
	if err != nil || pathInfoIsLink(info) || !info.IsDir() {
		return project{}, fmt.Errorf("project path is not a real directory")
	}
	return project{root: filepath.Clean(canonical)}, nil
}

func (p project) validate() error {
	if p.root == "" {
		return fmt.Errorf("project path must be opened explicitly")
	}
	return nil
}

func (p project) skillsDir() string              { return filepath.Join(p.root, ".agents", "skills") }
func (p project) lockPath() string               { return filepath.Join(p.root, ".agents", "ts-skills.lock") }
func (p project) stateDir() string               { return filepath.Join(p.root, ".agents", ".ts-skills") }
func (p project) destination(name string) string { return filepath.Join(p.skillsDir(), name) }

func (p project) managedName(name string) (string, error) {
	relative, err := filepath.Rel(p.root, name)
	if err != nil || !filepath.IsLocal(relative) {
		return "", fmt.Errorf("managed path %q is outside project root", name)
	}
	return relative, nil
}

func prepareManagedDirectories(project project, root *os.Root) error {
	if err := ensureRealDirectory(project.root, false); err != nil {
		return err
	}
	for _, name := range []string{".agents", filepath.Join(".agents", "skills"), filepath.Join(".agents", ".ts-skills")} {
		directory := filepath.Join(project.root, name)
		if err := rejectPathComponents(directory, true); err != nil {
			return err
		}
		info, err := root.Lstat(name)
		if err == nil {
			if pathInfoIsLink(info) || !info.IsDir() {
				return fmt.Errorf("managed path %q must be a real directory", directory)
			}
			continue
		}
		if !errors.Is(err, fs.ErrNotExist) {
			return fmt.Errorf("inspect managed path %q: %w", directory, err)
		}
		if err := root.Mkdir(name, 0o755); err != nil {
			return fmt.Errorf("create managed directory %q: %w", directory, err)
		}
		if err := syncRootDirectory(root, filepath.Dir(name)); err != nil {
			return err
		}
	}
	return rejectLink(project.lockPath(), true)
}

func ensureRealDirectory(name string, create bool) error {
	if err := rejectPathComponents(name, true); err != nil {
		return err
	}
	info, err := os.Lstat(name)
	if err == nil {
		if pathInfoIsLink(info) || !info.IsDir() {
			return fmt.Errorf("managed path %q must be a real directory", name)
		}
		return nil
	}
	if !errors.Is(err, fs.ErrNotExist) || !create {
		return fmt.Errorf("inspect managed path %q: %w", name, err)
	}
	if err := ensureRealDirectory(filepath.Dir(name), false); err != nil {
		return err
	}
	if err := os.Mkdir(name, 0o755); err != nil {
		return fmt.Errorf("create managed directory %q: %w", name, err)
	}
	return syncDirectory(filepath.Dir(name))
}

func rejectPathComponents(name string, allowMissing bool) error {
	clean := filepath.Clean(name)
	if err := rejectPlatformPathComponents(clean); err != nil {
		return err
	}
	for current := clean; ; current = filepath.Dir(current) {
		info, err := os.Lstat(current)
		if errors.Is(err, fs.ErrNotExist) && allowMissing {
			parent := filepath.Dir(current)
			if parent == current {
				return nil
			}
			continue
		}
		if err != nil {
			return fmt.Errorf("inspect managed path component %q: %w", current, err)
		}
		if pathInfoIsLink(info) {
			return fmt.Errorf("managed path component %q must not be a symbolic link or reparse point", current)
		}
		parent := filepath.Dir(current)
		if parent == current {
			return nil
		}
	}
}

func rejectLink(name string, allowMissing bool) error {
	return rejectPathComponents(name, allowMissing)
}
