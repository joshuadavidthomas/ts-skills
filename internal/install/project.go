package install

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
)

type Project struct {
	root string
}

func OpenProject(source string) (Project, error) {
	if source == "" {
		return Project{}, fmt.Errorf("project path must be explicit")
	}
	absolute, err := filepath.Abs(source)
	if err != nil {
		return Project{}, fmt.Errorf("resolve project path: %w", err)
	}
	canonical, err := filepath.EvalSymlinks(absolute)
	if err != nil {
		return Project{}, fmt.Errorf("resolve project path symlinks: %w", err)
	}
	canonical, err = filepath.Abs(canonical)
	if err != nil {
		return Project{}, fmt.Errorf("canonicalize project path: %w", err)
	}
	canonical = filepath.Clean(canonical)
	info, err := os.Lstat(canonical)
	if err != nil {
		return Project{}, fmt.Errorf("inspect project path: %w", err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return Project{}, fmt.Errorf("project path is not a real directory")
	}
	return Project{root: canonical}, nil
}

func (p Project) Root() string      { return p.root }
func (p Project) SkillsDir() string { return filepath.Join(p.root, ".agents", "skills") }
func (p Project) LockPath() string  { return filepath.Join(p.root, ".agents", "ts-skills.lock") }
func (p Project) StateDir() string {
	return filepath.Join(p.root, ".agents", ".ts-skills")
}

func (p Project) operationsDir() string { return filepath.Join(p.StateDir(), "operations") }
func (p Project) destination(name string) string {
	return filepath.Join(p.SkillsDir(), name)
}

func prepareManagedDirectories(project Project) error {
	if project.root == "" {
		return fmt.Errorf("invalid project")
	}
	if err := ensureRealDirectory(project.root, false); err != nil {
		return err
	}
	for _, directory := range []string{
		filepath.Join(project.root, ".agents"),
		project.SkillsDir(),
		project.StateDir(),
		project.operationsDir(),
	} {
		if err := ensureRealDirectory(directory, true); err != nil {
			return err
		}
	}
	if err := rejectLink(project.LockPath(), true); err != nil {
		return err
	}
	return nil
}

func ensureRealDirectory(name string, create bool) error {
	info, err := os.Lstat(name)
	if err == nil {
		if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
			return fmt.Errorf("managed path %q must be a real directory", name)
		}
		return nil
	}
	if !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("inspect managed path %q: %w", name, err)
	}
	if !create {
		return fmt.Errorf("inspect managed path %q: %w", name, err)
	}
	if err := ensureRealDirectory(filepath.Dir(name), false); err != nil {
		return err
	}
	if err := os.Mkdir(name, 0o755); err != nil && !errors.Is(err, fs.ErrExist) {
		return fmt.Errorf("create managed directory %q: %w", name, err)
	}
	return ensureRealDirectory(name, false)
}

func rejectLink(name string, allowMissing bool) error {
	info, err := os.Lstat(name)
	if errors.Is(err, fs.ErrNotExist) && allowMissing {
		return nil
	}
	if err != nil {
		return fmt.Errorf("inspect managed path %q: %w", name, err)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("managed path %q must not be a symbolic link", name)
	}
	return nil
}

func inspectDestination(destination string) (bool, error) {
	info, err := os.Lstat(destination)
	if errors.Is(err, fs.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("inspect skill destination: %w", err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return false, fmt.Errorf("%w: managed destination must be a real directory", ErrUnmanagedDestination)
	}
	return true, nil
}
