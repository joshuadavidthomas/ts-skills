package install

import (
	"fmt"
	"os"
	"path/filepath"
)

type Project struct {
	root string
}

func OpenProject(src string) (Project, error) {
	if src == "" {
		return Project{}, fmt.Errorf("project path must be explicit")
	}
	absolute, err := filepath.Abs(src)
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
	info, err := os.Stat(canonical)
	if err != nil {
		return Project{}, fmt.Errorf("stat project path: %w", err)
	}
	if !info.IsDir() {
		return Project{}, fmt.Errorf("project path is not a directory")
	}
	return Project{root: filepath.Clean(canonical)}, nil
}

func (p Project) Root() string      { return p.root }
func (p Project) SkillsDir() string { return filepath.Join(p.root, ".agents", "skills") }
func (p Project) LockPath() string  { return filepath.Join(p.root, ".agents", "ts-skills.lock") }
func (p Project) StateDir() string {
	return filepath.Join(p.root, ".agents", ".ts-skills")
}
