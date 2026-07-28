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
	if err := rejectPathComponents(v.path, true); err != nil {
		return err
	}
	removeAll := os.RemoveAll
	if v.writer != nil && v.writer.removeStaging != nil {
		removeAll = v.writer.removeStaging
	}
	if err := removeAll(v.path); err != nil {
		return err
	}
	v.owned = false
	if v.writer != nil {
		delete(v.writer.staging, v.path)
	}
	return nil
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
	project       Project
	lock          *flock.Flock
	staging       map[string]struct{}
	closed        bool
	removeStaging func(string) error
	closeLock     func(*flock.Flock) error
}

// filesystemIdentityForPath is a package-private test seam for mount/device checks.
var filesystemIdentityForPath = filesystemDevice

func requireSameFilesystem(reference string, paths ...string) error {
	referenceDevice, err := filesystemIdentityForPath(reference)
	if err != nil {
		return fmt.Errorf("identify filesystem for %q: %w", reference, err)
	}
	for _, path := range paths {
		device, err := filesystemIdentityForPath(path)
		if err != nil {
			return fmt.Errorf("identify filesystem for %q: %w", path, err)
		}
		if device != referenceDevice {
			return fmt.Errorf("managed path %q is not on the project filesystem", path)
		}
	}
	return nil
}

func (p Project) acquireWriter(ctx context.Context) (*projectWriter, error) {
	if ctx == nil {
		return nil, fmt.Errorf("acquire project writer: context is nil")
	}
	if err := prepareManagedDirectories(p); err != nil {
		return nil, fmt.Errorf("prepare project transaction paths: %w", err)
	}
	managedDirectories := []string{
		filepath.Join(p.root, ".agents"),
		p.SkillsDir(),
		p.StateDir(),
		p.operationsDir(),
		filepath.Dir(p.LockPath()),
	}
	if err := requireSameFilesystem(p.root, managedDirectories...); err != nil {
		return nil, err
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
	writer := &projectWriter{
		project: p, lock: fileLock, staging: make(map[string]struct{}),
		removeStaging: os.RemoveAll, closeLock: (*flock.Flock).Close,
	}
	if err := writer.recover(); err != nil {
		_ = writer.close()
		return nil, err
	}
	return writer, nil
}

func (w *projectWriter) close() error {
	if w == nil || w.closed {
		return nil
	}
	removeStaging := w.removeStaging
	if removeStaging == nil {
		removeStaging = os.RemoveAll
	}
	var result error
	for path := range w.staging {
		if err := rejectPathComponents(path, true); err != nil {
			result = errors.Join(result, err)
			continue
		}
		if err := removeStaging(path); err != nil {
			result = errors.Join(result, err)
			continue
		}
		delete(w.staging, path)
	}
	if result != nil {
		return result
	}
	if w.lock != nil {
		closeLock := w.closeLock
		if closeLock == nil {
			closeLock = (*flock.Flock).Close
		}
		if err := closeLock(w.lock); err != nil {
			return err
		}
		w.lock = nil
	}
	w.closed = true
	return nil
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
