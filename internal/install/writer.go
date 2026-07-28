package install

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"time"

	"github.com/gofrs/flock"
	"github.com/joshuadavidthomas/ts-skill-registry/internal/agentskill"
	"github.com/joshuadavidthomas/ts-skill-registry/internal/registry"
)

type verifiedTree struct {
	publication registry.PublicationID
	path        string
	owned       bool
	writer      *projectWriter
}

func (v *verifiedTree) close() error {
	if v == nil || !v.owned {
		return nil
	}
	v.owned = false
	if v.writer != nil {
		delete(v.writer.staging, v.path)
	}
	return os.RemoveAll(v.path)
}

func (v *verifiedTree) transfer() (string, error) {
	if v == nil || !v.owned || v.path == "" {
		return "", fmt.Errorf("verified tree ownership has already been transferred")
	}
	v.owned = false
	if v.writer != nil {
		delete(v.writer.staging, v.path)
	}
	return v.path, nil
}

type projectWriter struct {
	project Project
	lock    *flock.Flock
	staging map[string]struct{}
	closed  bool
}

func (p Project) acquireWriter(ctx context.Context) (*projectWriter, error) {
	if ctx == nil {
		return nil, fmt.Errorf("acquire project writer: context is nil")
	}
	if err := prepareManagedDirectories(p); err != nil {
		return nil, fmt.Errorf("prepare project transaction paths: %w", err)
	}
	projectDevice, err := filesystemDevice(p.root)
	if err != nil {
		return nil, fmt.Errorf("identify project filesystem: %w", err)
	}
	for _, path := range []string{filepath.Join(p.root, ".agents"), p.SkillsDir(), p.StateDir(), p.operationsDir()} {
		device, err := filesystemDevice(path)
		if err != nil {
			return nil, fmt.Errorf("identify managed filesystem: %w", err)
		}
		if device != projectDevice {
			return nil, fmt.Errorf("managed path %q is not on the project filesystem", path)
		}
	}

	lockPath := filepath.Join(p.StateDir(), "write.lock")
	if err := rejectLink(lockPath, true); err != nil {
		return nil, err
	}
	fileLock := flock.New(lockPath, flock.SetPermissions(0o600))
	locked, err := fileLock.TryLockContext(ctx, 10*time.Millisecond)
	if err != nil {
		_ = fileLock.Close()
		if ctxErr := ctx.Err(); ctxErr != nil {
			return nil, fmt.Errorf("%w: %w", ErrBusy, ctxErr)
		}
		return nil, fmt.Errorf("acquire project writer lock: %w", err)
	}
	if !locked {
		_ = fileLock.Close()
		cause := ctx.Err()
		if cause == nil {
			cause = errors.New("writer lock was not acquired")
		}
		return nil, fmt.Errorf("%w: %w", ErrBusy, cause)
	}
	if err := rejectRegularFile(lockPath); err != nil {
		_ = fileLock.Close()
		return nil, err
	}
	writer := &projectWriter{project: p, lock: fileLock, staging: make(map[string]struct{})}
	if err := writer.recover(); err != nil {
		_ = writer.close()
		return nil, err
	}
	return writer, nil
}

func rejectRegularFile(path string) error {
	info, err := os.Lstat(path)
	if err != nil {
		return fmt.Errorf("inspect managed file %q: %w", path, err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return fmt.Errorf("managed path %q must be a real regular file", path)
	}
	return nil
}

func (w *projectWriter) close() error {
	if w == nil || w.closed {
		return nil
	}
	w.closed = true
	var result error
	for path := range w.staging {
		result = errors.Join(result, os.RemoveAll(path))
	}
	if w.lock != nil {
		result = errors.Join(result, w.lock.Close())
	}
	return result
}

func (w *projectWriter) readLock() (Lock, []byte, bool, error) {
	if err := rejectLink(w.project.LockPath(), true); err != nil {
		return Lock{}, nil, false, err
	}
	contents, err := os.ReadFile(w.project.LockPath())
	if errors.Is(err, fs.ErrNotExist) {
		lock, lockErr := NewLock(nil)
		return lock, nil, false, lockErr
	}
	if err != nil {
		return Lock{}, nil, false, fmt.Errorf("read project lock: %w", err)
	}
	lock, err := DecodeLock(bytes.NewReader(contents))
	if err != nil {
		return Lock{}, nil, true, err
	}
	return lock, contents, true, nil
}

func (w *projectWriter) preflight(lock Lock, skill registry.SkillID) (bool, error) {
	destination := w.project.destination(skill.Name().String())
	exists, err := inspectDestination(destination)
	if err != nil {
		return false, err
	}
	locked, managed := lock.Lookup(skill)
	if !managed {
		if exists {
			return false, fmt.Errorf("%w: %s", ErrUnmanagedDestination, skill.Name().String())
		}
		return false, nil
	}
	if !exists {
		return false, nil
	}
	actual, err := agentskill.SumTree(os.DirFS(destination), ".")
	if err != nil {
		return false, fmt.Errorf("verify installed skill %s: %w", skill.String(), err)
	}
	if actual != locked.Publication().Tree() {
		return false, fmt.Errorf("%w: %s", ErrLocalChanges, skill.String())
	}
	return true, nil
}
