package tree

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// Stage copies source into a new durable staging directory beneath parent.
// Only directories and regular files are accepted. The returned Snapshot owns
// the staging directory until it is closed or its path is taken.
func Stage(ctx context.Context, parent, pattern string, source fs.FS) (_ *Snapshot, err error) {
	if ctx == nil {
		return nil, fmt.Errorf("stage tree context must be provided")
	}
	validated, err := NewSource(ctx, source, ".", PrototypeLimits())
	if err != nil {
		return nil, fmt.Errorf("stage tree source: %w", err)
	}
	staging, err := os.MkdirTemp(parent, pattern)
	if err != nil {
		return nil, fmt.Errorf("create tree staging directory: %w", err)
	}
	snapshot := &Snapshot{path: staging, removeAll: os.RemoveAll}
	defer func() {
		if err != nil {
			err = errors.Join(err, snapshot.Close())
		}
	}()
	if err := copyInto(ctx, validated, staging); err != nil {
		return nil, err
	}
	if err := Sync(ctx, staging); err != nil {
		return nil, fmt.Errorf("sync staged tree: %w", err)
	}
	return snapshot, nil
}

func copyInto(ctx context.Context, source Source, destination string) error {
	for _, file := range source.Files() {
		if err := ctx.Err(); err != nil {
			return err
		}
		target := filepath.Join(destination, filepath.FromSlash(file.Path))
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return fmt.Errorf("create staged tree directories for %q: %w", file.Path, err)
		}
		input, err := source.Open(file)
		if err != nil {
			return fmt.Errorf("open tree entry %q: %w", file.Path, err)
		}
		output, err := os.OpenFile(target, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
		if err != nil {
			return fmt.Errorf("create staged tree file %q: %w", file.Path, errors.Join(err, input.Close()))
		}
		copied, copyErr := io.Copy(output, &contextReader{ctx: ctx, source: input})
		closeOutputErr := output.Close()
		closeInputErr := input.Close()
		if err := errors.Join(copyErr, closeOutputErr, closeInputErr); err != nil {
			return fmt.Errorf("copy tree entry %q: %w", file.Path, err)
		}
		if copied != file.Size {
			return fmt.Errorf("copy tree entry %q: %w", file.Path, io.ErrUnexpectedEOF)
		}
	}
	return nil
}

// Sync makes every regular file and directory in root durable. Directories are
// synced deepest-first so child entries reach storage before their parents.
func Sync(ctx context.Context, root string) error {
	if ctx == nil {
		return fmt.Errorf("sync tree context must be provided")
	}
	var directories []string
	err := filepath.WalkDir(root, func(name string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if entry.Type()&fs.ModeSymlink != 0 {
			return fmt.Errorf("refuse to sync symbolic link %q", name)
		}
		if entry.IsDir() {
			directories = append(directories, name)
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("refuse to sync non-regular file %q", name)
		}
		file, err := os.Open(name)
		if err != nil {
			return err
		}
		syncErr := file.Sync()
		closeErr := file.Close()
		return errors.Join(syncErr, closeErr)
	})
	if err != nil {
		return err
	}
	sort.Slice(directories, func(i, j int) bool {
		leftDepth := strings.Count(filepath.Clean(directories[i]), string(filepath.Separator))
		rightDepth := strings.Count(filepath.Clean(directories[j]), string(filepath.Separator))
		if leftDepth == rightDepth {
			return directories[i] > directories[j]
		}
		return leftDepth > rightDepth
	})
	for _, directory := range directories {
		if err := ctx.Err(); err != nil {
			return err
		}
		file, err := os.Open(directory)
		if err != nil {
			return err
		}
		syncErr := file.Sync()
		closeErr := file.Close()
		if err := errors.Join(syncErr, closeErr); err != nil {
			return err
		}
	}
	return nil
}
