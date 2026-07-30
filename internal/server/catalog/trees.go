package catalog

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/joshuadavidthomas/ts-skills/internal/registry"
	"github.com/joshuadavidthomas/ts-skills/internal/tree"
)

func ensureStateDirectory(stateDir string) error {
	if err := os.MkdirAll(stateDir, 0o700); err != nil {
		return err
	}
	info, err := os.Lstat(stateDir)
	if err != nil {
		return err
	}
	if !info.IsDir() || info.Mode()&fs.ModeSymlink != 0 {
		return fmt.Errorf("%q is not a real directory", stateDir)
	}
	if err := os.Chmod(stateDir, 0o700); err != nil {
		return fmt.Errorf("set private directory permissions: %w", err)
	}
	info, err = os.Lstat(stateDir)
	if err != nil {
		return err
	}
	if !info.IsDir() || info.Mode()&fs.ModeSymlink != 0 {
		return fmt.Errorf("%q is not a real directory", stateDir)
	}
	if info.Mode().Perm() != 0o700 {
		return fmt.Errorf("%q has mode %04o, want 0700", stateDir, info.Mode().Perm())
	}
	return nil
}

func ensureStorageDirectories(stateDir, treesDir, tmpDir string) error {
	paths := []string{
		filepath.Join(stateDir, "trees"),
		treesDir,
		tmpDir,
	}
	for _, directory := range paths {
		created, err := createDirectory(directory, 0o700)
		if err != nil {
			return fmt.Errorf("create storage directory %q: %w", directory, err)
		}
		if created {
			if err := syncDirectory(directory); err != nil {
				return fmt.Errorf("sync storage directory %q: %w", directory, err)
			}
			if err := syncDirectory(filepath.Dir(directory)); err != nil {
				return fmt.Errorf("sync storage parent %q: %w", filepath.Dir(directory), err)
			}
		}
	}
	return nil
}

func createDirectory(name string, mode fs.FileMode) (bool, error) {
	err := os.Mkdir(name, mode)
	if err == nil {
		return true, nil
	}
	if !errors.Is(err, fs.ErrExist) {
		return false, err
	}
	info, statErr := os.Lstat(name)
	if statErr != nil {
		return false, statErr
	}
	if !info.IsDir() || info.Mode()&fs.ModeSymlink != 0 {
		return false, fmt.Errorf("existing path is not a real directory")
	}
	return false, nil
}

func (c *catalog) materializeTree(ctx context.Context, expected registry.PublicationID, source fs.FS) (err error) {
	staged, err := tree.Stage(ctx, c.tmpDir, ".tree-", source)
	if err != nil {
		return err
	}
	staging := ""
	defer func() {
		err = errors.Join(err, staged.Close())
		if staging != "" {
			err = errors.Join(err, os.RemoveAll(staging))
		}
	}()
	if err := c.step("stage durable tree"); err != nil {
		return err
	}

	inspection, err := registry.Inspect(ctx, staged.FS(), ".")
	if err != nil {
		return fmt.Errorf("inspect staged Agent Skill: %w", err)
	}
	if err := inspection.Verify(expected); err != nil {
		return fmt.Errorf("%w: %v", errTreeMismatch, err)
	}
	if err := c.step("verify staged tree digest"); err != nil {
		return err
	}

	digest := expected.Tree()
	shard, final := c.treePaths(digest)
	if _, err := createDirectory(shard, 0o700); err != nil {
		return fmt.Errorf("create tree digest shard: %w", err)
	}
	// Another digest in this shard may observe the directory before the
	// goroutine that created it has synced its parent. Every materialization
	// completes that mkdir barrier itself before it can commit metadata.
	if err := c.syncDirectory(shard); err != nil {
		return fmt.Errorf("sync tree digest shard: %w", err)
	}
	if err := c.syncDirectory(filepath.Dir(shard)); err != nil {
		return fmt.Errorf("sync tree digest shard parent: %w", err)
	}
	if err := c.step("prepare digest shard"); err != nil {
		return err
	}

	info, statErr := os.Lstat(final)
	switch {
	case statErr == nil:
		if !info.IsDir() || info.Mode()&fs.ModeSymlink != 0 {
			return fmt.Errorf("%w: digest path is not a real directory", errTreeMismatch)
		}
		if err := verifyTree(ctx, final, digest); err != nil {
			return err
		}
		if err := c.step("verify existing digest tree"); err != nil {
			return err
		}
		if err := tree.Sync(ctx, final); err != nil {
			return fmt.Errorf("sync existing digest tree: %w", err)
		}
		if err := c.syncDirectory(shard); err != nil {
			return fmt.Errorf("sync existing digest tree parent: %w", err)
		}
		if err := c.step("sync existing digest tree"); err != nil {
			return err
		}
		c.markTreeVerified(digest)
		return nil
	case !errors.Is(statErr, fs.ErrNotExist):
		return fmt.Errorf("inspect digest tree: %w", statErr)
	}

	staging, err = staged.TakePath()
	if err != nil {
		return fmt.Errorf("take staged tree path: %w", err)
	}
	if err := os.Rename(staging, final); err != nil {
		return fmt.Errorf("install immutable digest tree: %w", err)
	}
	staging = ""
	if err := c.step("rename digest tree"); err != nil {
		return err
	}
	if err := c.syncDirectory(shard); err != nil {
		return fmt.Errorf("sync installed digest tree parent: %w", err)
	}
	if err := c.step("sync installed digest tree parent"); err != nil {
		return err
	}
	c.markTreeVerified(digest)
	return nil
}

func verifyTree(ctx context.Context, directory string, expected registry.TreeDigest) error {
	actual, err := registry.SumTree(ctx, os.DirFS(directory), ".")
	if err != nil {
		return fmt.Errorf("%w: verify digest tree: %v", errTreeMismatch, err)
	}
	if actual != expected {
		return fmt.Errorf("%w: digest path says %s, tree hashes to %s", errTreeMismatch, expected, actual)
	}
	return nil
}

func syncDirectory(directory string) error {
	file, err := os.Open(directory)
	if err != nil {
		return err
	}
	syncErr := file.Sync()
	closeErr := file.Close()
	return errors.Join(syncErr, closeErr)
}

func (c *catalog) step(name string) error {
	if c.afterFilesystemStep == nil {
		return nil
	}
	if err := c.afterFilesystemStep(name); err != nil {
		return fmt.Errorf("after %s: %w", name, err)
	}
	return nil
}

func (c *catalog) treePaths(digest registry.TreeDigest) (string, string) {
	hexDigest := strings.TrimPrefix(digest.String(), "sha256:")
	shard := filepath.Join(c.treesDir, hexDigest[:2])
	return shard, filepath.Join(shard, hexDigest[2:])
}

func (c *catalog) openTree(ctx context.Context, digest registry.TreeDigest) (*treeView, error) {
	done, err := c.withOpenState()
	if err != nil {
		return nil, err
	}
	defer done()
	_, final := c.treePaths(digest)
	if !c.treeVerified(digest) {
		if err := verifyTree(ctx, final, digest); err != nil {
			return nil, err
		}
		c.markTreeVerified(digest)
	}
	c.refsMu.Lock()
	c.openTrees++
	c.refsMu.Unlock()
	return &treeView{files: os.DirFS(final), release: c.releaseTree}, nil
}

func (c *catalog) treeVerified(digest registry.TreeDigest) bool {
	c.verifiedMu.Lock()
	defer c.verifiedMu.Unlock()
	_, ok := c.verified[digest]
	return ok
}

func (c *catalog) markTreeVerified(digest registry.TreeDigest) {
	c.verifiedMu.Lock()
	defer c.verifiedMu.Unlock()
	c.verified[digest] = struct{}{}
}

func (c *catalog) releaseTree() {
	c.refsMu.Lock()
	defer c.refsMu.Unlock()
	if c.openTrees > 0 {
		c.openTrees--
	}
}

type treeView struct {
	mu      sync.Mutex
	files   fs.FS
	release func()
	closed  bool
}

func (t *treeView) Open(name string) (fs.File, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.closed {
		return nil, fs.ErrClosed
	}
	return t.files.Open(name)
}

func (t *treeView) Close() error {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.closed {
		return fs.ErrClosed
	}
	t.closed = true
	t.release()
	return nil
}
