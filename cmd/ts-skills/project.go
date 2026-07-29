package main

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
)

type Project struct{ root string }

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
	if err := rejectPathComponents(canonical, false); err != nil {
		return Project{}, fmt.Errorf("inspect project path: %w", err)
	}
	info, err := os.Lstat(canonical)
	if err != nil || pathInfoIsLink(info) || !info.IsDir() {
		return Project{}, fmt.Errorf("project path is not a real directory")
	}
	return Project{root: filepath.Clean(canonical)}, nil
}

func (p Project) Root() string                   { return p.root }
func (p Project) SkillsDir() string              { return filepath.Join(p.root, ".agents", "skills") }
func (p Project) LockPath() string               { return filepath.Join(p.root, ".agents", "ts-skills.lock") }
func (p Project) StateDir() string               { return filepath.Join(p.root, ".agents", ".ts-skills") }
func (p Project) destination(name string) string { return filepath.Join(p.SkillsDir(), name) }

func prepareManagedDirectories(project Project) error {
	if err := ensureRealDirectory(project.root, false); err != nil {
		return err
	}
	for _, directory := range []string{filepath.Join(project.root, ".agents"), project.SkillsDir(), project.StateDir()} {
		if err := ensureRealDirectory(directory, true); err != nil {
			return err
		}
	}
	return rejectLink(project.LockPath(), true)
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

func temporaryPath(parent, prefix string) (string, error) {
	var bytes [16]byte
	if _, err := rand.Read(bytes[:]); err != nil {
		return "", err
	}
	return filepath.Join(parent, prefix+hex.EncodeToString(bytes[:])), nil
}

func rejectPathComponents(name string, allowMissing bool) error {
	clean := filepath.Clean(name)
	if err := rejectPlatformPathComponents(clean); err != nil {
		return err
	}
	for current := clean; ; current = filepath.Dir(current) {
		info, err := os.Lstat(current)
		if errors.Is(err, fs.ErrNotExist) && allowMissing {
			return nil
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

func inspectDestination(destination string) (bool, error) {
	if err := rejectPathComponents(destination, true); err != nil {
		return false, fmt.Errorf("inspect skill destination: %w", err)
	}
	info, err := os.Lstat(destination)
	if errors.Is(err, fs.ErrNotExist) {
		return false, nil
	}
	if err != nil || pathInfoIsLink(info) || !info.IsDir() {
		return false, fmt.Errorf("inspect skill destination %q: must be a real directory", destination)
	}
	return true, nil
}
