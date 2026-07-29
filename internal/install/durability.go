package install

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// transactionFailure is a package-private test seam. Production leaves it nil.
var transactionFailure func(string) error

func transactionPoint(name string) error {
	if transactionFailure != nil {
		if err := transactionFailure(name); err != nil {
			return err
		}
	}
	return nil
}

func writeSyncedFile(path string, contents []byte, mode fs.FileMode, label string) error {
	if err := rejectPathComponents(path, true); err != nil {
		return err
	}
	if err := transactionPoint("before-write-" + label); err != nil {
		return err
	}
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, mode)
	if err != nil {
		return fmt.Errorf("write %s: %w", label, err)
	}
	_, writeErr := file.Write(contents)
	if writeErr == nil {
		writeErr = syncOpenFile(file, label)
	}
	closeErr := file.Close()
	if err := errors.Join(writeErr, closeErr); err != nil {
		return fmt.Errorf("write %s: %w", label, err)
	}
	if err := transactionPoint("after-write-" + label); err != nil {
		return err
	}
	return nil
}

func durableRename(oldPath, newPath, syncParent, label string) error {
	if err := rejectPathComponents(oldPath, false); err != nil {
		return err
	}
	if err := rejectPathComponents(newPath, true); err != nil {
		return err
	}
	if err := ensureRealDirectory(syncParent, false); err != nil {
		return err
	}
	if err := transactionPoint("before-rename-" + label); err != nil {
		return err
	}
	if err := os.Rename(oldPath, newPath); err != nil {
		return fmt.Errorf("rename %s: %w", label, err)
	}
	if err := transactionPoint("after-rename-" + label); err != nil {
		return err
	}
	parents := []struct {
		path string
		role string
	}{
		{path: filepath.Dir(oldPath), role: "source-parent"},
		{path: filepath.Dir(newPath), role: "destination-parent"},
		{path: syncParent, role: "requested-parent"},
	}
	synced := make(map[string]struct{}, len(parents))
	for _, parent := range parents {
		path := filepath.Clean(parent.path)
		if _, found := synced[path]; found {
			continue
		}
		if err := syncDirectory(path, label+"-"+parent.role); err != nil {
			return err
		}
		synced[path] = struct{}{}
	}
	return nil
}

func syncOpenFile(file *os.File, label string) error {
	if err := transactionPoint("before-fsync-" + label + "-file"); err != nil {
		return err
	}
	if err := file.Sync(); err != nil {
		return err
	}
	return transactionPoint("after-fsync-" + label + "-file")
}

func syncFile(path, label string) error {
	if err := rejectPathComponents(path, false); err != nil {
		return err
	}
	file, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("open file for %s durability: %w", label, err)
	}
	syncErr := syncOpenFile(file, label)
	closeErr := file.Close()
	if err := errors.Join(syncErr, closeErr); err != nil {
		return fmt.Errorf("sync file for %s durability: %w", label, err)
	}
	return nil
}

func syncDirectory(path, label string) error {
	if err := rejectPathComponents(path, false); err != nil {
		return err
	}
	if err := transactionPoint("before-fsync-" + label); err != nil {
		return err
	}
	directory, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("open directory for %s durability: %w", label, err)
	}
	syncErr := directory.Sync()
	closeErr := directory.Close()
	if err := errors.Join(syncErr, closeErr); err != nil {
		return fmt.Errorf("sync directory for %s durability: %w", label, err)
	}
	return transactionPoint("after-fsync-" + label)
}

func syncTree(ctx context.Context, root, label string) error {
	directories := make([]string, 0)
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
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
		if pathInfoIsLink(info) {
			return fmt.Errorf("tree path %q is a symbolic link or reparse point", path)
		}
		if info.IsDir() {
			directories = append(directories, path)
			return nil
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("tree path %q is not regular", path)
		}
		return syncFile(path, label)
	})
	if err != nil {
		return fmt.Errorf("sync %s: %w", label, err)
	}
	sort.Slice(directories, func(i, j int) bool {
		return strings.Count(directories[i], string(filepath.Separator)) > strings.Count(directories[j], string(filepath.Separator))
	})
	for _, directory := range directories {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := syncDirectory(directory, label+"-directory"); err != nil {
			return err
		}
	}
	return nil
}

func durableRemoveAll(path, parent, label string) error {
	if err := rejectPathComponents(path, true); err != nil {
		return err
	}
	if err := ensureRealDirectory(parent, false); err != nil {
		return err
	}
	if err := transactionPoint("before-remove-" + label); err != nil {
		return err
	}
	if err := os.RemoveAll(path); err != nil {
		return fmt.Errorf("remove %s: %w", label, err)
	}
	if err := transactionPoint("after-remove-" + label); err != nil {
		return err
	}
	return syncDirectory(parent, label)
}

func durableRemoveFile(path, parent, label string) error {
	if err := rejectPathComponents(path, true); err != nil {
		return err
	}
	if err := ensureRealDirectory(parent, false); err != nil {
		return err
	}
	if err := transactionPoint("before-remove-" + label); err != nil {
		return err
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("remove %s: %w", label, err)
	}
	if err := transactionPoint("after-remove-" + label); err != nil {
		return err
	}
	return syncDirectory(parent, label)
}
