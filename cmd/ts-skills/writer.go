package main

import (
	"bytes"
	"context"
	"encoding/json"
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
	installStagingPrefix       = ".ts-skills-stage-"
	installTrashPrefix         = ".ts-skills-trash-"
	installTrashPendingPrefix  = installTrashPrefix + "pending-"
	installTrashRecoveryPrefix = installTrashPrefix + "recovery-"
	installTrashGarbagePrefix  = installTrashPrefix + "garbage-"
	trashRecordName            = "record.json"
	trashTreeName              = "tree"
	lockTemporaryPrefix        = ".ts-skills-lock-"
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
	project       project
	lock          *flock.Flock
	staging       map[string]struct{}
	syncDirectory func(string) error
	rename        func(string, string) error
	closed        bool
}

func (p project) acquireWriter(ctx context.Context) (*projectWriter, error) {
	if ctx == nil {
		return nil, fmt.Errorf("acquire project writer: context is nil")
	}
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("acquire project writer: %w", err)
	}
	if err := prepareManagedDirectories(p); err != nil {
		return nil, fmt.Errorf("prepare project paths: %w", err)
	}
	lockPath := filepath.Join(p.stateDir(), "write.lock")
	if err := rejectLink(lockPath, true); err != nil {
		return nil, err
	}
	fileLock := flock.New(lockPath, flock.SetPermissions(0o600))
	locked, err := fileLock.TryLockContext(ctx, 10*time.Millisecond)
	if err != nil {
		return nil, errors.Join(fmt.Errorf("acquire project writer lock: %w", err), fileLock.Close())
	}
	if !locked {
		return nil, errors.Join(errBusy, fileLock.Close())
	}
	writer := &projectWriter{project: p, lock: fileLock, staging: make(map[string]struct{}), syncDirectory: syncDirectory, rename: os.Rename}
	if err := writer.sweepLitter(ctx); err != nil {
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

func (w *projectWriter) sweepLitter(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	var sweepErr error
	for _, location := range []struct {
		parent   string
		prefixes []string
	}{
		{w.project.skillsDir(), []string{installStagingPrefix, installTrashGarbagePrefix}},
		{filepath.Dir(w.project.lockPath()), []string{lockTemporaryPrefix}},
	} {
		if err := ctx.Err(); err != nil {
			return errors.Join(sweepErr, err)
		}
		entries, err := os.ReadDir(location.parent)
		if err != nil {
			sweepErr = errors.Join(sweepErr, fmt.Errorf("read install litter: %w", err))
			continue
		}
		for _, entry := range entries {
			if err := ctx.Err(); err != nil {
				return errors.Join(sweepErr, err)
			}
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
			if pathInfoIsLink(info) || (!info.IsDir() && location.parent == w.project.skillsDir()) || (info.IsDir() && location.parent != w.project.skillsDir()) {
				sweepErr = errors.Join(sweepErr, fmt.Errorf("install litter %q has an unsafe shape", path))
				continue
			}
			if err := os.RemoveAll(path); err != nil {
				sweepErr = errors.Join(sweepErr, fmt.Errorf("remove install litter %q: %w", path, err))
			}
		}
	}
	entries, err := os.ReadDir(w.project.skillsDir())
	if err != nil {
		return errors.Join(sweepErr, fmt.Errorf("read install litter: %w", err))
	}
	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			return errors.Join(sweepErr, err)
		}
		if !strings.HasPrefix(entry.Name(), installTrashPendingPrefix) && !strings.HasPrefix(entry.Name(), installTrashRecoveryPrefix) {
			continue
		}
		path := filepath.Join(w.project.skillsDir(), entry.Name())
		recovered, err := w.recoverLockedTrash(ctx, path)
		if err != nil {
			sweepErr = errors.Join(sweepErr, err)
			continue
		}
		if recovered {
			continue
		}
		stale, err := w.trashIsStale(ctx, path)
		if err != nil {
			sweepErr = errors.Join(sweepErr, err)
			continue
		}
		if stale != nil {
			if err := w.syncDirectory(filepath.Dir(w.project.lockPath())); err != nil {
				sweepErr = errors.Join(sweepErr, fmt.Errorf("sync project lock directory: %w", err))
				continue
			}
			if err := w.syncDirectory(w.project.skillsDir()); err != nil {
				sweepErr = errors.Join(sweepErr, fmt.Errorf("sync skill directory: %w", err))
				continue
			}
			if stale.removeDestination {
				if err := os.RemoveAll(w.project.destination(stale.skill.Name().String())); err != nil {
					sweepErr = errors.Join(sweepErr, fmt.Errorf("remove uncommitted install destination: %w", err))
					continue
				}
				if err := w.syncDirectory(w.project.skillsDir()); err != nil {
					sweepErr = errors.Join(sweepErr, fmt.Errorf("sync removed install destination: %w", err))
					continue
				}
				if err := w.discardTrash(path); err != nil {
					sweepErr = errors.Join(sweepErr, err)
				}
				continue
			}
			if err := os.RemoveAll(path); err != nil {
				sweepErr = errors.Join(sweepErr, fmt.Errorf("remove install litter %q: %w", path, err))
			}
		}
	}
	return sweepErr
}

type trashRecord struct {
	Skill          string `json:"skill"`
	Tree           string `json:"tree"`
	HadDestination bool   `json:"had_destination"`
}

func (w *projectWriter) recoverLockedTrash(ctx context.Context, path string) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	record, skill, err := readTrashRecord(path)
	if err != nil || !record.HadDestination {
		return false, nil
	}
	inFlight, err := agentskill.ParseTreeDigest(record.Tree)
	if err != nil {
		return false, nil
	}
	lock, _, _, err := w.readLock()
	if err != nil {
		return false, err
	}
	locked, found := lock.lookup(skill)
	if !found {
		return false, nil
	}
	state, err := w.destinationState(ctx, skill)
	if err != nil || (state.exists && state.digest == locked.publication.Tree()) {
		return false, err
	}
	if state.exists && state.digest != inFlight {
		return false, nil
	}
	digest, err := agentskill.SumTree(ctx, os.DirFS(filepath.Join(path, trashTreeName)), ".")
	if err != nil {
		if ctx.Err() != nil {
			return false, err
		}
		return false, nil
	}
	if digest != locked.publication.Tree() {
		return false, nil
	}
	if err := ctx.Err(); err != nil {
		return false, err
	}
	if strings.HasPrefix(filepath.Base(path), installTrashPendingPrefix) {
		path, err = w.transitionTrash(path, installTrashPendingPrefix, installTrashRecoveryPrefix)
		if err != nil {
			return false, err
		}
	}
	if state.exists {
		if err := os.RemoveAll(w.project.destination(skill.Name().String())); err != nil {
			return false, err
		}
	}
	if err := w.rename(filepath.Join(path, trashTreeName), w.project.destination(skill.Name().String())); err != nil {
		return false, err
	}
	if err := w.syncDirectory(w.project.skillsDir()); err != nil {
		return false, err
	}
	return true, w.discardTrash(path)
}

type staleTrash struct {
	skill             agentskill.SkillID
	removeDestination bool
}

func (w *projectWriter) trashIsStale(ctx context.Context, path string) (*staleTrash, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	info, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if pathInfoIsLink(info) || !info.IsDir() {
		return nil, fmt.Errorf("install litter %q has an unsafe shape", path)
	}
	record, skill, err := readTrashRecord(path)
	if err != nil {
		return nil, nil
	}
	lock, _, _, err := w.readLock()
	if err != nil {
		return nil, err
	}
	state, err := w.destinationState(ctx, skill)
	if err != nil {
		return nil, err
	}
	if !record.HadDestination && !state.exists {
		return &staleTrash{skill: skill}, nil
	}
	if locked, found := lock.lookup(skill); found {
		if state.exists && state.digest == locked.publication.Tree() {
			return &staleTrash{skill: skill}, nil
		}
		return nil, nil
	}
	if record.HadDestination {
		return nil, nil
	}
	tree, err := agentskill.ParseTreeDigest(record.Tree)
	if err != nil || state.digest != tree {
		return nil, nil
	}
	return &staleTrash{skill: skill, removeDestination: true}, nil
}

func readTrashRecord(path string) (trashRecord, agentskill.SkillID, error) {
	recordPath := filepath.Join(path, trashRecordName)
	if err := rejectLink(recordPath, false); err != nil {
		return trashRecord{}, agentskill.SkillID{}, err
	}
	contents, err := os.ReadFile(recordPath)
	if err != nil {
		return trashRecord{}, agentskill.SkillID{}, err
	}
	var record trashRecord
	if err := json.Unmarshal(contents, &record); err != nil {
		return trashRecord{}, agentskill.SkillID{}, err
	}
	skill, err := agentskill.ParseSkillID(record.Skill)
	if err != nil {
		return trashRecord{}, agentskill.SkillID{}, err
	}
	return record, skill, nil
}

func (w *projectWriter) createTrash(publication agentskill.PublicationID, hadDestination bool) (string, error) {
	path, err := temporaryPath(w.project.skillsDir(), installTrashPendingPrefix)
	if err != nil {
		return "", err
	}
	if err := os.Mkdir(path, 0o700); err != nil {
		return "", err
	}
	contents, err := json.Marshal(trashRecord{Skill: publication.Skill().String(), Tree: publication.Tree().String(), HadDestination: hadDestination})
	if err != nil {
		return "", err
	}
	if err := writeSyncedFile(filepath.Join(path, trashRecordName), contents, 0o600); err != nil {
		return "", errors.Join(err, os.RemoveAll(path))
	}
	if err := w.syncDirectory(path); err != nil {
		return "", errors.Join(err, os.RemoveAll(path))
	}
	if err := w.syncDirectory(w.project.skillsDir()); err != nil {
		return "", err
	}
	return path, nil
}

func (w *projectWriter) transitionTrash(path, from, to string) (string, error) {
	name := filepath.Base(path)
	if !strings.HasPrefix(name, from) {
		return "", fmt.Errorf("install trash %q is not %s", path, from)
	}
	next := filepath.Join(filepath.Dir(path), to+strings.TrimPrefix(name, from))
	if err := w.rename(path, next); err != nil {
		return "", err
	}
	if err := w.syncDirectory(w.project.skillsDir()); err != nil {
		return "", err
	}
	return next, nil
}

func (w *projectWriter) readLock() (lock, []byte, bool, error) {
	if err := rejectLink(w.project.lockPath(), true); err != nil {
		return lock{}, nil, false, fmt.Errorf("inspect project lock: %w", err)
	}
	contents, err := os.ReadFile(w.project.lockPath())
	if errors.Is(err, fs.ErrNotExist) {
		lock, lockErr := newLock(nil)
		return lock, nil, false, lockErr
	}
	if err != nil {
		return lock{}, nil, false, fmt.Errorf("read project lock: %w", err)
	}
	lock, err := decodeLock(bytes.NewReader(contents))
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
