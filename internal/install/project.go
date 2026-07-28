package install

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
)

const installStagingPrefix = "staging-"

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
	if err := rejectPathComponents(canonical, false); err != nil {
		return Project{}, fmt.Errorf("inspect project path: %w", err)
	}
	info, err := os.Lstat(canonical)
	if err != nil {
		return Project{}, fmt.Errorf("inspect project path: %w", err)
	}
	if pathInfoIsLink(info) || !info.IsDir() {
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
	if !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("inspect managed path %q: %w", name, err)
	}
	if !create {
		return fmt.Errorf("inspect managed path %q: %w", name, err)
	}
	parent := filepath.Dir(name)
	if err := ensureRealDirectory(parent, false); err != nil {
		return err
	}
	if err := os.Mkdir(name, 0o755); err != nil {
		return fmt.Errorf("create managed directory %q: %w", name, err)
	}
	if err := ensureRealDirectory(name, false); err != nil {
		return err
	}
	if err := syncDirectory(name, "managed-directory-created"); err != nil {
		return err
	}
	if err := syncDirectory(parent, "managed-directory-parent"); err != nil {
		return err
	}
	return nil
}

func createManagedDirectory(name string, mode fs.FileMode, label string) error {
	if err := rejectPathComponents(name, true); err != nil {
		return err
	}
	if err := ensureRealDirectory(filepath.Dir(name), false); err != nil {
		return err
	}
	if err := os.Mkdir(name, mode); err != nil {
		return fmt.Errorf("create %s: %w", label, err)
	}
	if err := ensureRealDirectory(name, false); err != nil {
		return err
	}
	if err := syncDirectory(name, label+"-created"); err != nil {
		return err
	}
	return syncDirectory(filepath.Dir(name), label+"-parent")
}

func createManagedTempDirectory(parent, pattern string) (string, error) {
	if err := ensureRealDirectory(parent, false); err != nil {
		return "", err
	}
	name, err := os.MkdirTemp(parent, pattern)
	if err != nil {
		return "", err
	}
	if err := ensureRealDirectory(name, false); err != nil {
		return "", errors.Join(err, os.RemoveAll(name))
	}
	if err := syncDirectory(name, "install-staging-created"); err != nil {
		return "", errors.Join(err, os.RemoveAll(name))
	}
	if err := syncDirectory(parent, "install-staging-parent"); err != nil {
		return "", errors.Join(err, os.RemoveAll(name))
	}
	return name, nil
}

func rejectPathComponents(name string, allowMissing bool) error {
	clean := filepath.Clean(name)
	if err := rejectPlatformPathComponents(clean); err != nil {
		return err
	}
	paths := make([]string, 0)
	for current := clean; ; current = filepath.Dir(current) {
		paths = append(paths, current)
		parent := filepath.Dir(current)
		if parent == current {
			break
		}
	}
	for index := len(paths) - 1; index >= 0; index-- {
		path := paths[index]
		info, err := os.Lstat(path)
		if errors.Is(err, fs.ErrNotExist) && allowMissing {
			return nil
		}
		if err != nil {
			return fmt.Errorf("inspect managed path component %q: %w", path, err)
		}
		if pathInfoIsLink(info) {
			return fmt.Errorf("managed path component %q must not be a symbolic link or reparse point", path)
		}
	}
	return nil
}

func rejectLink(name string, allowMissing bool) error {
	return rejectPathComponents(name, allowMissing)
}

func rejectRegularFile(path string) error {
	if err := rejectPathComponents(path, false); err != nil {
		return err
	}
	info, err := os.Lstat(path)
	if err != nil {
		return fmt.Errorf("inspect managed file %q: %w", path, err)
	}
	if pathInfoIsLink(info) || !info.Mode().IsRegular() {
		return fmt.Errorf("managed path %q must be a real regular file", path)
	}
	return nil
}

func inspectDestination(destination string) (bool, error) {
	if err := rejectPathComponents(destination, true); err != nil {
		return false, fmt.Errorf("inspect skill destination: %w", err)
	}
	info, err := os.Lstat(destination)
	if errors.Is(err, fs.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("inspect skill destination: %w", err)
	}
	if pathInfoIsLink(info) || !info.IsDir() {
		return false, fmt.Errorf("%w: managed destination must be a real directory", ErrUnmanagedDestination)
	}
	return true, nil
}
