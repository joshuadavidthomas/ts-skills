package safetree

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"
	"unicode/utf8"
)

var (
	ErrInvalidPath   = errors.New("invalid tree path")
	ErrLimitExceeded = errors.New("tree limit exceeded")
)

type Limits struct {
	MaxFiles         int
	MaxPathBytes     int
	MaxDepth         int
	MaxFileBytes     int64
	MaxExpandedBytes int64
}

func PrototypeLimits() Limits {
	return Limits{
		MaxFiles:         2048,
		MaxPathBytes:     1024,
		MaxDepth:         32,
		MaxFileBytes:     16 << 20,
		MaxExpandedBytes: 128 << 20,
	}
}

func ValidateLimits(limits Limits) error {
	if limits.MaxFiles <= 0 || limits.MaxPathBytes <= 0 || limits.MaxDepth <= 0 || limits.MaxFileBytes <= 0 || limits.MaxExpandedBytes <= 0 {
		return fmt.Errorf("safetree limits must all be positive")
	}
	if limits.MaxFileBytes > limits.MaxExpandedBytes {
		return fmt.Errorf("maximum file size cannot exceed maximum expanded size")
	}
	return nil
}

type LimitError struct {
	Limit  string
	Max    int64
	Actual int64
}

func (e *LimitError) Error() string {
	return fmt.Sprintf("%v: %s maximum is %d, got %d", ErrLimitExceeded, e.Limit, e.Max, e.Actual)
}

func (e *LimitError) Unwrap() error { return ErrLimitExceeded }

type Builder struct {
	path        string
	limits      Limits
	files       map[string]string
	directories map[string]string
	bytes       int64
	finished    bool
	closed      bool
	removeAll   func(string) error
}

func NewBuilder(parent string, limits Limits) (*Builder, error) {
	if err := ValidateLimits(limits); err != nil {
		return nil, err
	}
	info, err := os.Stat(parent)
	if err != nil {
		return nil, fmt.Errorf("stat staging parent: %w", err)
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("staging parent is not a directory")
	}
	staging, err := os.MkdirTemp(parent, ".ts-skills-tree-")
	if err != nil {
		return nil, fmt.Errorf("create tree staging: %w", err)
	}
	return &Builder{
		path: staging, limits: limits, files: make(map[string]string), directories: make(map[string]string), removeAll: os.RemoveAll,
	}, nil
}

func (b *Builder) AddFile(ctx context.Context, name string, declaredSize int64, source io.Reader) error {
	if b == nil || b.closed || b.finished {
		return fmt.Errorf("tree builder is closed")
	}
	if source == nil {
		return fmt.Errorf("add tree file %q: reader is nil", name)
	}
	if err := validatePath(name, b.limits); err != nil {
		return err
	}
	if declaredSize < 0 {
		return fmt.Errorf("%w: declared size must be nonnegative", ErrInvalidPath)
	}
	if declaredSize > b.limits.MaxFileBytes {
		return &LimitError{Limit: "file bytes", Max: b.limits.MaxFileBytes, Actual: declaredSize}
	}
	if len(b.files)+1 > b.limits.MaxFiles {
		return &LimitError{Limit: "files", Max: int64(b.limits.MaxFiles), Actual: int64(len(b.files) + 1)}
	}
	key := canonicalPlatformPath(name)
	if existing, exists := b.files[key]; exists {
		return fmt.Errorf("%w: duplicate files %q and %q", ErrInvalidPath, existing, name)
	}
	if descendant, exists := b.directories[key]; exists {
		return fmt.Errorf("%w: file and directory prefix collision between %q and %q", ErrInvalidPath, name, descendant)
	}
	var ancestor string
	visitPathParents(key, func(parent string) bool {
		if existing, exists := b.files[parent]; exists {
			ancestor = existing
			return false
		}
		return true
	})
	if ancestor != "" {
		return fmt.Errorf("%w: file and directory prefix collision between %q and %q", ErrInvalidPath, name, ancestor)
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	destination := filepath.Join(b.path, filepath.FromSlash(name))
	if err := os.MkdirAll(filepath.Dir(destination), 0o755); err != nil {
		return fmt.Errorf("create staged directories for %q: %w", name, err)
	}
	file, err := os.OpenFile(destination, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return fmt.Errorf("create staged file %q: %w", name, err)
	}
	remainingTotal := b.limits.MaxExpandedBytes - b.bytes
	maximum := min(b.limits.MaxFileBytes, remainingTotal)
	written, copyErr := copyContext(ctx, file, io.LimitReader(source, maximum+1))
	closeErr := file.Close()
	if copyErr != nil || closeErr != nil {
		_ = os.Remove(destination)
		return errors.Join(copyErr, closeErr)
	}
	if written > b.limits.MaxFileBytes {
		_ = os.Remove(destination)
		return &LimitError{Limit: "file bytes", Max: b.limits.MaxFileBytes, Actual: written}
	}
	if written > remainingTotal {
		_ = os.Remove(destination)
		return &LimitError{Limit: "expanded bytes", Max: b.limits.MaxExpandedBytes, Actual: b.bytes + written}
	}
	if err := os.Chmod(destination, 0o644); err != nil {
		_ = os.Remove(destination)
		return fmt.Errorf("normalize staged file %q: %w", name, err)
	}
	b.files[key] = name
	visitPathParents(key, func(parent string) bool {
		if _, exists := b.directories[parent]; !exists {
			b.directories[parent] = name
		}
		return true
	})
	b.bytes += written
	return nil
}

func visitPathParents(name string, visit func(string) bool) {
	start := 0
	for {
		relative := strings.IndexByte(name[start:], '/')
		if relative < 0 {
			return
		}
		separator := start + relative
		if !visit(name[:separator]) {
			return
		}
		start = separator + 1
	}
}

func copyContext(ctx context.Context, dst io.Writer, src io.Reader) (int64, error) {
	buffer := make([]byte, 32*1024)
	var total int64
	for {
		if err := ctx.Err(); err != nil {
			return total, err
		}
		read, readErr := src.Read(buffer)
		if read > 0 {
			written, writeErr := dst.Write(buffer[:read])
			total += int64(written)
			if writeErr != nil {
				return total, writeErr
			}
			if written != read {
				return total, io.ErrShortWrite
			}
		}
		if readErr == io.EOF {
			return total, nil
		}
		if readErr != nil {
			return total, readErr
		}
		if read == 0 {
			return total, io.ErrNoProgress
		}
	}
}

func (b *Builder) Finish() (*Snapshot, error) {
	if b == nil || b.closed || b.finished {
		return nil, fmt.Errorf("tree builder is closed")
	}
	b.finished = true
	staging := b.path
	b.path = ""
	return &Snapshot{path: staging, removeAll: b.removeAll}, nil
}

func (b *Builder) Close() error {
	if b == nil || b.closed {
		return nil
	}
	if b.path != "" {
		removeAll := b.removeAll
		if removeAll == nil {
			removeAll = os.RemoveAll
		}
		if err := removeAll(b.path); err != nil {
			return err
		}
		b.path = ""
	}
	b.closed = true
	return nil
}

type Snapshot struct {
	path      string
	closed    bool
	removeAll func(string) error
}

func (s *Snapshot) FS() fs.FS {
	if s == nil {
		return os.DirFS("")
	}
	return os.DirFS(s.path)
}

func (s *Snapshot) Close() error {
	if s == nil || s.closed {
		return nil
	}
	removeAll := s.removeAll
	if removeAll == nil {
		removeAll = os.RemoveAll
	}
	if err := removeAll(s.path); err != nil {
		return err
	}
	s.closed = true
	return nil
}

func StageFS(ctx context.Context, parent string, source fs.FS, root string, limits Limits) (_ *Snapshot, err error) {
	if source == nil {
		return nil, fmt.Errorf("stage tree: source filesystem is nil")
	}
	if root == "." || !fs.ValidPath(root) || !utf8.ValidString(root) || strings.Contains(root, "\\") {
		return nil, fmt.Errorf("%w: root %q must be a non-dot valid path", ErrInvalidPath, root)
	}
	builder, err := NewBuilder(parent, limits)
	if err != nil {
		return nil, err
	}
	defer func() {
		if err != nil {
			err = errors.Join(err, builder.Close())
		}
	}()
	err = fs.WalkDir(source, root, func(name string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if info.IsDir() {
			return nil
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("%w: %q is not a regular file", ErrInvalidPath, name)
		}
		file, err := source.Open(name)
		if err != nil {
			return err
		}
		addErr := builder.AddFile(ctx, name, info.Size(), file)
		closeErr := file.Close()
		return errors.Join(addErr, closeErr)
	})
	if err != nil {
		return nil, fmt.Errorf("stage filesystem tree %q: %w", root, err)
	}
	snapshot, err := builder.Finish()
	if err != nil {
		return nil, err
	}
	return snapshot, nil
}

func validatePath(name string, limits Limits) error {
	if name == "." || !fs.ValidPath(name) || !utf8.ValidString(name) || strings.Contains(name, "\\") {
		return fmt.Errorf("%w: %q", ErrInvalidPath, name)
	}
	if len(name) > limits.MaxPathBytes {
		return &LimitError{Limit: "path bytes", Max: int64(limits.MaxPathBytes), Actual: int64(len(name))}
	}
	depth := strings.Count(name, "/") + 1
	if depth > limits.MaxDepth {
		return &LimitError{Limit: "path depth", Max: int64(limits.MaxDepth), Actual: int64(depth)}
	}
	for _, part := range strings.Split(name, "/") {
		if part == "" || part == "." || part == ".." || path.Clean(part) != part || invalidPlatformPathComponent(part) {
			return fmt.Errorf("%w: %q", ErrInvalidPath, name)
		}
	}
	return nil
}
