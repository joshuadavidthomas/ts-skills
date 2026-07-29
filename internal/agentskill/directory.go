package agentskill

import (
	"fmt"
	"io/fs"
	"path"
)

type Directory struct {
	document Document
	files    fs.FS
}

func Load(fsys fs.FS, dir string) (Directory, error) {
	if fsys == nil {
		return Directory{}, newValidationError(ErrInvalidTree, "filesystem", "must be provided")
	}
	if !fs.ValidPath(dir) {
		return Directory{}, newValidationError(ErrInvalidTree, "directory", "must be a valid filesystem path")
	}
	entries, err := fs.ReadDir(fsys, dir)
	if err != nil {
		return Directory{}, fmt.Errorf("read Agent Skill directory %q: %w", dir, err)
	}
	found := false
	for _, entry := range entries {
		if entry.Name() == Filename {
			if entry.IsDir() || entry.Type()&fs.ModeType != 0 {
				return Directory{}, newValidationError(ErrInvalidTree, Filename, "must be a regular file")
			}
			found = true
			break
		}
	}
	if !found {
		return Directory{}, fmt.Errorf("read Agent Skill document %q: %w", path.Join(dir, Filename), fs.ErrNotExist)
	}
	contents, err := fs.ReadFile(fsys, path.Join(dir, Filename))
	if err != nil {
		return Directory{}, fmt.Errorf("read Agent Skill document %q: %w", path.Join(dir, Filename), err)
	}
	document, err := Parse(contents)
	if err != nil {
		return Directory{}, err
	}
	if dir != "." {
		basename := path.Base(dir)
		name, err := ParseName(basename)
		if err != nil || name != document.Name {
			return Directory{}, newValidationError(ErrInvalidTree, "directory", "basename must match the canonical frontmatter name")
		}
	}
	rooted, err := fs.Sub(fsys, dir)
	if err != nil {
		return Directory{}, fmt.Errorf("root Agent Skill directory %q: %w", dir, err)
	}
	return Directory{document: cloneDocument(document), files: rooted}, nil
}

func (d Directory) Document() Document { return cloneDocument(d.document) }

func (d Directory) FS() fs.FS { return d.files }
