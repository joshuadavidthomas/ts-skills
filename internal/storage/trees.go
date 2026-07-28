package storage

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
	"sync"

	"github.com/joshuadavidthomas/ts-skills/internal/agentskill"
	"github.com/joshuadavidthomas/ts-skills/internal/registry"
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

func (c *Catalog) materializeTree(ctx context.Context, expected agentskill.TreeDigest, expectedName agentskill.Name, source fs.FS) (err error) {
	staging, err := os.MkdirTemp(c.tmpDir, ".tree-")
	if err != nil {
		return fmt.Errorf("create tree staging directory: %w", err)
	}
	defer func() {
		if staging != "" {
			err = errors.Join(err, os.RemoveAll(staging))
		}
	}()
	if err := c.step("create staging directory"); err != nil {
		return err
	}
	if err := copyTree(ctx, source, staging, c.step); err != nil {
		return err
	}
	if err := c.step("copy staged tree"); err != nil {
		return err
	}

	actual, err := agentskill.SumTree(ctx, os.DirFS(staging), ".")
	if err != nil {
		return fmt.Errorf("hash staged tree: %w", err)
	}
	if actual != expected {
		return fmt.Errorf("%w: candidate says %s, copied tree hashes to %s", registry.ErrTreeMismatch, expected, actual)
	}
	directory, err := agentskill.Load(os.DirFS(staging), ".")
	if err != nil {
		return fmt.Errorf("load copied Agent Skill: %w", err)
	}
	if actualName := directory.Document().Name; actualName != expectedName {
		return fmt.Errorf("candidate names %s but SKILL.md names %s", expectedName, actualName)
	}
	if err := c.step("verify staged tree digest"); err != nil {
		return err
	}
	if err := syncTree(staging, c.step); err != nil {
		return fmt.Errorf("sync staged tree: %w", err)
	}
	if err := c.step("sync staged tree"); err != nil {
		return err
	}

	shard, final := c.treePaths(expected)
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
			return fmt.Errorf("%w: digest path is not a real directory", registry.ErrTreeMismatch)
		}
		if err := verifyTree(ctx, final, expected); err != nil {
			return err
		}
		if err := c.step("verify existing digest tree"); err != nil {
			return err
		}
		if err := syncTree(final, c.step); err != nil {
			return fmt.Errorf("sync existing digest tree: %w", err)
		}
		if err := c.syncDirectory(shard); err != nil {
			return fmt.Errorf("sync existing digest tree parent: %w", err)
		}
		if err := c.step("sync existing digest tree"); err != nil {
			return err
		}
		return nil
	case !errors.Is(statErr, fs.ErrNotExist):
		return fmt.Errorf("inspect digest tree: %w", statErr)
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
	return nil
}

func copyTree(ctx context.Context, source fs.FS, destination string, step func(string) error) error {
	if source == nil {
		return fmt.Errorf("tree source must be provided")
	}
	return fs.WalkDir(source, ".", func(name string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if name == "." {
			if !entry.IsDir() {
				return fmt.Errorf("tree source root is not a directory")
			}
			return nil
		}
		target := filepath.Join(destination, filepath.FromSlash(name))
		info, err := entry.Info()
		if err != nil {
			return fmt.Errorf("inspect tree entry %q: %w", name, err)
		}
		if info.IsDir() {
			if err := os.Mkdir(target, 0o755); err != nil {
				return fmt.Errorf("create staged tree directory %q: %w", name, err)
			}
			return step("create staged tree directory")
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("tree entry %q is not a regular file", name)
		}
		input, err := source.Open(name)
		if err != nil {
			return fmt.Errorf("open tree entry %q: %w", name, err)
		}
		output, err := os.OpenFile(target, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
		if err != nil {
			return fmt.Errorf("create staged tree file %q: %w", name, errors.Join(err, input.Close()))
		}
		_, copyErr := io.Copy(output, &contextReader{ctx: ctx, source: input})
		closeOutputErr := output.Close()
		closeInputErr := input.Close()
		if err := errors.Join(copyErr, closeOutputErr, closeInputErr); err != nil {
			return fmt.Errorf("copy tree entry %q: %w", name, err)
		}
		return step("copy staged tree file")
	})
}

type contextReader struct {
	ctx    context.Context
	source io.Reader
}

func (r *contextReader) Read(buffer []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.source.Read(buffer)
}

func verifyTree(ctx context.Context, directory string, expected agentskill.TreeDigest) error {
	actual, err := agentskill.SumTree(ctx, os.DirFS(directory), ".")
	if err != nil {
		return fmt.Errorf("%w: verify digest tree: %v", registry.ErrTreeMismatch, err)
	}
	if actual != expected {
		return fmt.Errorf("%w: digest path says %s, tree hashes to %s", registry.ErrTreeMismatch, expected, actual)
	}
	return nil
}

func syncTree(root string, step func(string) error) error {
	var directories []string
	err := filepath.WalkDir(root, func(name string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
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
		if err := errors.Join(syncErr, closeErr); err != nil {
			return err
		}
		return step("sync tree file")
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
		if err := syncDirectory(directory); err != nil {
			return err
		}
		if err := step("sync tree directory"); err != nil {
			return err
		}
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

func (c *Catalog) step(name string) error {
	if c.afterFilesystemStep == nil {
		return nil
	}
	if err := c.afterFilesystemStep(name); err != nil {
		return fmt.Errorf("after %s: %w", name, err)
	}
	return nil
}

func (c *Catalog) treePaths(digest agentskill.TreeDigest) (string, string) {
	hexDigest := strings.TrimPrefix(digest.String(), "sha256:")
	shard := filepath.Join(c.treesDir, hexDigest[:2])
	return shard, filepath.Join(shard, hexDigest[2:])
}

func (c *Catalog) OpenCandidateTree(ctx context.Context, id registry.CandidateID) (registry.Tree, error) {
	done, err := c.withOpenState()
	if err != nil {
		return nil, err
	}
	defer done()
	candidate, err := queryCandidate(ctx, c.db.QueryRowContext, id)
	if err != nil {
		return nil, err
	}
	return c.openTree(ctx, candidate.Tree())
}

func (c *Catalog) OpenPublicationTree(ctx context.Context, id registry.PublicationID) (registry.Tree, error) {
	done, err := c.withOpenState()
	if err != nil {
		return nil, err
	}
	defer done()
	publication, err := queryPublication(ctx, c.db.QueryRowContext, id)
	if err != nil {
		return nil, err
	}
	return c.openTree(ctx, publication.ID().Tree())
}

func (c *Catalog) openTree(ctx context.Context, digest agentskill.TreeDigest) (registry.Tree, error) {
	_, final := c.treePaths(digest)
	if err := verifyTree(ctx, final, digest); err != nil {
		return nil, err
	}
	c.refsMu.Lock()
	c.openTrees++
	c.refsMu.Unlock()
	return &treeView{files: os.DirFS(final), release: c.releaseTree}, nil
}

func (c *Catalog) releaseTree() {
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
