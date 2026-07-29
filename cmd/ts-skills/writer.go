package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/gofrs/flock"
	"github.com/joshuadavidthomas/ts-skills/internal/agentskill"
)

const (
	installStagingPrefix = ".ts-skills-stage-"
	installTrashPrefix   = ".ts-skills-trash-"
	lockTemporaryPrefix  = ".ts-skills-lock-"
)

type verifiedTree struct {
	publication agentskill.PublicationID
	path        string
	owned       bool
	writer      *projectWriter
}

func (v *verifiedTree) close() error {
	if v == nil || !v.owned {
		return nil
	}
	if err := os.RemoveAll(v.path); err != nil {
		return err
	}
	v.owned = false
	delete(v.writer.staging, v.path)
	return nil
}

func (v *verifiedTree) transfer() (string, error) {
	if v == nil || !v.owned {
		return "", fmt.Errorf("verified tree ownership has already been transferred")
	}
	v.owned = false
	delete(v.writer.staging, v.path)
	return v.path, nil
}

type projectWriter struct {
	project       Project
	lock          *flock.Flock
	staging       map[string]struct{}
	syncDirectory func(string) error
	rename        func(string, string) error
	closed        bool
}

func (p Project) acquireWriter(ctx context.Context) (*projectWriter, error) {
	if ctx == nil {
		return nil, fmt.Errorf("acquire project writer: context is nil")
	}
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("acquire project writer: %w", err)
	}
	if err := prepareManagedDirectories(p); err != nil {
		return nil, fmt.Errorf("prepare project paths: %w", err)
	}
	lockPath := filepath.Join(p.StateDir(), "write.lock")
	if err := rejectLink(lockPath, true); err != nil {
		return nil, err
	}
	fileLock := flock.New(lockPath, flock.SetPermissions(0o600))
	locked, err := fileLock.TryLockContext(ctx, 10*time.Millisecond)
	if err != nil {
		return nil, errors.Join(fmt.Errorf("acquire project writer lock: %w", err), fileLock.Close())
	}
	if !locked {
		return nil, errors.Join(ErrBusy, fileLock.Close())
	}
	writer := &projectWriter{project: p, lock: fileLock, staging: make(map[string]struct{}), syncDirectory: syncDirectory, rename: os.Rename}
	if err := writer.sweepLitter(); err != nil {
		return nil, errors.Join(err, writer.close())
	}
	return writer, nil
}

func (w *projectWriter) close() error {
	if w == nil || w.closed {
		return nil
	}
	var err error
	for path := range w.staging {
		err = errors.Join(err, os.RemoveAll(path))
	}
	w.staging = nil
	if w.lock != nil {
		err = errors.Join(err, w.lock.Close())
		w.lock = nil
	}
	w.closed = true
	return err
}

func (w *projectWriter) sweepLitter() error {
	var sweepErr error
	for _, location := range []struct {
		parent   string
		prefixes []string
	}{
		{w.project.SkillsDir(), []string{installStagingPrefix}},
		// A trash directory may contain the only recoverable old skill after
		// a replacement rollback fails. Successful replacements remove their
		// trash directly; leave any remaining directory for manual recovery.
		{filepath.Dir(w.project.LockPath()), []string{lockTemporaryPrefix}},
	} {
		entries, err := os.ReadDir(location.parent)
		if err != nil {
			sweepErr = errors.Join(sweepErr, fmt.Errorf("read install litter: %w", err))
			continue
		}
		for _, entry := range entries {
			matched := false
			for _, prefix := range location.prefixes {
				matched = matched || strings.HasPrefix(entry.Name(), prefix)
			}
			if !matched {
				continue
			}
			path := filepath.Join(location.parent, entry.Name())
			info, statErr := os.Lstat(path)
			if statErr != nil {
				sweepErr = errors.Join(sweepErr, statErr)
				continue
			}
			if pathInfoIsLink(info) || (!info.IsDir() && location.parent == w.project.SkillsDir()) || (info.IsDir() && location.parent != w.project.SkillsDir()) {
				sweepErr = errors.Join(sweepErr, fmt.Errorf("install litter %q has an unsafe shape", path))
				continue
			}
			if err := os.RemoveAll(path); err != nil {
				sweepErr = errors.Join(sweepErr, fmt.Errorf("remove install litter %q: %w", path, err))
			}
		}
	}
	return sweepErr
}

func (w *projectWriter) readLock() (Lock, []byte, bool, error) {
	if err := rejectLink(w.project.LockPath(), true); err != nil {
		return Lock{}, nil, false, fmt.Errorf("inspect project lock: %w", err)
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
	return lock, contents, true, err
}

type destinationState struct {
	exists bool
	digest agentskill.TreeDigest
}

func (w *projectWriter) destinationState(ctx context.Context, skill agentskill.SkillID) (destinationState, error) {
	destination := w.project.destination(skill.Name().String())
	exists, err := inspectDestination(destination)
	if err != nil || !exists {
		return destinationState{exists: exists}, err
	}
	digest, err := agentskill.SumTree(ctx, os.DirFS(destination), ".")
	if err != nil {
		return destinationState{}, fmt.Errorf("verify installed skill %s: %w", skill.String(), err)
	}
	return destinationState{exists: true, digest: digest}, nil
}

func sameDestination(a, b destinationState) bool {
	return a.exists == b.exists && (!a.exists || a.digest == b.digest)
}
