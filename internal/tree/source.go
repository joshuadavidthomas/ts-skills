package tree

import (
	"context"
	"fmt"
	"io/fs"
	"path"
	"sort"
	"strings"
)

// File describes one regular file in a validated portable tree.
type File struct {
	Path string
	Size int64
}

// Manifest is validated portable-tree metadata.
type Manifest struct {
	files []File
}

// NewManifest validates file paths, collisions, counts, and declared sizes.
func NewManifest(files []File, limits Limits) (Manifest, error) {
	if err := ValidateLimits(limits); err != nil {
		return Manifest{}, fmt.Errorf("tree manifest limits: %w", err)
	}
	index := newPathIndex()
	var total int64
	validated := make([]File, 0, len(files))
	for _, file := range files {
		if err := validateFile(file, limits); err != nil {
			return Manifest{}, err
		}
		if err := index.add(file.Path); err != nil {
			return Manifest{}, err
		}
		if len(validated)+1 > limits.MaxFiles {
			return Manifest{}, &LimitError{Limit: "files", Max: int64(limits.MaxFiles), Actual: int64(len(validated) + 1)}
		}
		if file.Size > limits.MaxExpandedBytes-total {
			return Manifest{}, &LimitError{Limit: "expanded bytes", Max: limits.MaxExpandedBytes, Actual: total + file.Size}
		}
		total += file.Size
		validated = append(validated, file)
	}
	sort.Slice(validated, func(i, j int) bool { return validated[i].Path < validated[j].Path })
	return Manifest{files: validated}, nil
}

// Files returns a copy of the validated file metadata.
func (m Manifest) Files() []File {
	return append([]File(nil), m.files...)
}

// Source binds a filesystem tree to its validated, sorted file manifest.
// Construction checks every portability and size invariant used by staging,
// encoding, decoding, and hashing.
type Source struct {
	fsys  fs.FS
	root  string
	files Manifest
}

// NewSource validates the tree rooted at root and returns its immutable
// manifest. The underlying filesystem must remain unchanged while Source is
// consumed.
func NewSource(ctx context.Context, fsys fs.FS, root string, limits Limits) (Source, error) {
	if ctx == nil {
		return Source{}, fmt.Errorf("tree source context must be provided")
	}
	if fsys == nil || !fs.ValidPath(root) {
		return Source{}, fmt.Errorf("tree source must name a root in a filesystem")
	}
	if err := ValidateLimits(limits); err != nil {
		return Source{}, fmt.Errorf("tree source limits: %w", err)
	}

	files := make([]File, 0)
	err := fs.WalkDir(fsys, root, func(name string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if name == root {
			if !entry.IsDir() {
				return fmt.Errorf("tree source root is not a directory")
			}
			return nil
		}
		relative, err := relativePath(root, name)
		if err != nil {
			return err
		}
		if entry.IsDir() {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return fmt.Errorf("inspect tree entry %q: %w", relative, err)
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("tree entry %q is not a regular file", relative)
		}
		files = append(files, File{Path: relative, Size: info.Size()})
		return nil
	})
	if err != nil {
		return Source{}, fmt.Errorf("validate portable tree: %w", err)
	}
	manifest, err := NewManifest(files, limits)
	if err != nil {
		return Source{}, fmt.Errorf("validate portable tree: %w", err)
	}
	return Source{fsys: fsys, root: root, files: manifest}, nil
}

// Files returns a copy of the validated manifest in lexical path order.
func (s Source) Files() []File {
	return s.files.Files()
}

// Open opens a file from the validated manifest.
func (s Source) Open(file File) (fs.File, error) {
	return s.fsys.Open(joinRoot(s.root, file.Path))
}

func relativePath(root, name string) (string, error) {
	if root == "." {
		return name, nil
	}
	prefix := root + "/"
	if !strings.HasPrefix(name, prefix) {
		return "", fmt.Errorf("%w: %q is outside tree root %q", ErrInvalidPath, name, root)
	}
	return strings.TrimPrefix(name, prefix), nil
}

func joinRoot(root, name string) string {
	if root == "." {
		return name
	}
	return path.Join(root, name)
}

func validateFile(file File, limits Limits) error {
	if err := validatePath(file.Path, limits); err != nil {
		return err
	}
	if file.Size < 0 {
		return fmt.Errorf("%w: file %q has a negative size", ErrSizeMismatch, file.Path)
	}
	if file.Size > limits.MaxFileBytes {
		return &LimitError{Limit: "file bytes", Max: limits.MaxFileBytes, Actual: file.Size}
	}
	return nil
}

type pathIndex struct {
	files       map[string]string
	directories map[string]string
}

func newPathIndex() pathIndex {
	return pathIndex{files: make(map[string]string), directories: make(map[string]string)}
}

func (i *pathIndex) add(name string) error {
	if err := i.check(name); err != nil {
		return err
	}
	key := canonicalPath(name)
	i.files[key] = name
	visitPathParents(key, func(parent string) bool {
		if _, exists := i.directories[parent]; !exists {
			i.directories[parent] = name
		}
		return true
	})
	return nil
}

func (i *pathIndex) check(name string) error {
	key := canonicalPath(name)
	if existing, exists := i.files[key]; exists {
		return fmt.Errorf("%w: duplicate files %q and %q", ErrInvalidPath, existing, name)
	}
	if descendant, exists := i.directories[key]; exists {
		return fmt.Errorf("%w: file and directory prefix collision between %q and %q", ErrInvalidPath, name, descendant)
	}
	var ancestor string
	visitPathParents(key, func(parent string) bool {
		if existing, exists := i.files[parent]; exists {
			ancestor = existing
			return false
		}
		return true
	})
	if ancestor != "" {
		return fmt.Errorf("%w: file and directory prefix collision between %q and %q", ErrInvalidPath, name, ancestor)
	}
	return nil
}
