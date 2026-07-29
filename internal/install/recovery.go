package install

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/joshuadavidthomas/ts-skills/internal/agentskill"
)

func (w *projectWriter) recover() (bool, error) {
	entries, err := os.ReadDir(w.project.operationsDir())
	if err != nil {
		return false, fmt.Errorf("read project operations: %w", err)
	}
	recovered := false
	for _, entry := range entries {
		operationDir := filepath.Join(w.project.operationsDir(), entry.Name())
		if err := rejectPathComponents(operationDir, false); err != nil {
			return recovered, fmt.Errorf("%w: invalid operation path %q: %v", ErrRecoveryRequired, operationDir, err)
		}
		info, err := os.Lstat(operationDir)
		if err != nil || pathInfoIsLink(info) || !info.IsDir() {
			return recovered, fmt.Errorf("%w: invalid operation path %q", ErrRecoveryRequired, operationDir)
		}
		journalPath := filepath.Join(operationDir, journalName)
		if err := rejectPathComponents(journalPath, true); err != nil {
			return recovered, fmt.Errorf("inspect operation journal: %w", err)
		}
		if _, err := os.Lstat(journalPath); errors.Is(err, fs.ErrNotExist) {
			if err := durableRemoveAll(operationDir, w.project.operationsDir(), "journal-less-operation"); err != nil {
				return recovered, err
			}
			recovered = true
			continue
		} else if err != nil {
			return recovered, fmt.Errorf("inspect operation journal: %w", err)
		}
		if err := w.recoverOperation(operationDir, entry.Name()); err != nil {
			return recovered, err
		}
		recovered = true
	}
	return recovered, nil
}

type treeState uint8

const (
	treeAbsent treeState = iota
	treeOld
	treeNew
)

func (w *projectWriter) recoverOperation(operationDir, operation string) error {
	op, err := readJournal(operationDir, operation)
	if err != nil {
		return err
	}

	actualLockBytes, lockExists, err := readOptionalFile(w.project.LockPath())
	if err != nil {
		return recoveryError("project lock cannot be read", err)
	}
	var actualLock *lockSnapshot
	if lockExists {
		lock, err := DecodeLock(bytes.NewReader(actualLockBytes))
		if err != nil {
			return recoveryError("project lock is invalid", err)
		}
		actualLock = &lockSnapshot{contents: actualLockBytes, lock: lock}
	}

	paths := newTransactionPaths(w.project, operation, op.skill)
	destinationState, err := classifyTree(paths.destination, op.oldDigest, op.newDigest)
	if err != nil {
		return recoveryError("destination matches neither recorded tree", err)
	}

	lockIsNew := actualLock != nil && lockSnapshotHash(actualLock.contents) == op.newLock.hash && op.validateLockSelection(actualLock, op.newDigest, true) == nil
	lockIsOld := actualLock != nil && op.oldLock != nil && lockSnapshotHash(actualLock.contents) == op.oldLock.hash && op.validateOldLock(actualLock) == nil
	lockIsAbsentOld := op.oldLock == nil && !lockExists
	if !lockIsNew && !lockIsOld && !lockIsAbsentOld {
		return recoveryError("project lock matches neither exact recorded state", nil)
	}
	// Recovery prefers the new state below when equal snapshots match both.
	// Keep both matches so a persisted rolled-back cleanup phase can still
	// validate the unchanged lock.
	lockSnapshotsEqual := lockIsNew && lockIsOld

	switch op.phase {
	case recoveryPhaseCleanupCommitted:
		if !lockIsNew || destinationState != treeNew {
			return recoveryError("committed cleanup state no longer matches its final plan", nil)
		}
		return finishRecovery(w.project, paths, op, treeNew, recoveryPhaseCleanupCommitted)
	case recoveryPhaseCleanupRolledBack:
		oldDestinationState := treeAbsent
		if op.hadDestination {
			oldDestinationState = treeOld
		}
		if (!lockIsOld && !lockIsAbsentOld) || destinationState != oldDestinationState {
			return recoveryError("rolled-back cleanup state no longer matches its final plan", nil)
		}
		return finishRecovery(w.project, paths, op, oldDestinationState, recoveryPhaseCleanupRolledBack)
	case recoveryPhaseLockCommitted:
		if lockIsNew && destinationState == treeNew {
			return finishRecovery(w.project, paths, op, treeNew, recoveryPhaseCleanupCommitted)
		}
	}

	if (op.oldLock != nil && op.oldLock.snapshot == nil) || op.newLock.snapshot == nil {
		return recoveryError("lock snapshots were removed before cleanup was committed", nil)
	}

	backupState, err := classifyTree(paths.backup, op.oldDigest, op.newDigest)
	if op.oldDigest != nil && *op.oldDigest == op.newDigest && backupState == treeNew {
		backupState = treeOld
	}
	if err != nil || (backupState != treeAbsent && backupState != treeOld) {
		return recoveryError("backup matches neither recorded old tree nor absence", err)
	}
	stagingState, err := classifyTree(paths.staging, op.oldDigest, op.newDigest)
	if err != nil || (stagingState != treeAbsent && stagingState != treeNew) {
		return recoveryError("staging matches neither recorded new tree nor absence", err)
	}

	if lockIsNew {
		switch {
		case destinationState == treeNew:
			return finishRecovery(w.project, paths, op, treeNew, recoveryPhaseCleanupCommitted)
		case destinationState == treeAbsent && stagingState == treeNew:
			if err := durableRename(paths.staging, paths.destination, w.project.SkillsDir(), "recover-new-destination"); err != nil {
				return err
			}
			return finishRecovery(w.project, paths, op, treeNew, recoveryPhaseCleanupCommitted)
		case op.phase == recoveryPhasePrepared && lockSnapshotsEqual && !op.hadDestination && destinationState == treeAbsent && stagingState == treeAbsent && backupState == treeAbsent:
			return finishRecovery(w.project, paths, op, treeAbsent, recoveryPhaseCleanupRolledBack)
		case op.oldLock != nil && (destinationState == treeOld || backupState == treeOld):
			if err := restoreOldState(w.project, paths, op, destinationState, backupState); err != nil {
				return err
			}
			return finishRecovery(w.project, paths, op, treeOld, recoveryPhaseCleanupRolledBack)
		default:
			return recoveryError("committed lock has no complete new or recoverable old destination", nil)
		}
	}

	if lockIsOld || lockIsAbsentOld {
		switch {
		case op.hadDestination && destinationState == treeOld:
			return finishRecovery(w.project, paths, op, treeOld, recoveryPhaseCleanupRolledBack)
		case !op.hadDestination && destinationState == treeAbsent:
			return finishRecovery(w.project, paths, op, treeAbsent, recoveryPhaseCleanupRolledBack)
		case op.hadDestination && destinationState == treeAbsent && backupState == treeOld:
			if err := durableRename(paths.backup, paths.destination, w.project.SkillsDir(), "recover-old-destination"); err != nil {
				return err
			}
			return finishRecovery(w.project, paths, op, treeOld, recoveryPhaseCleanupRolledBack)
		case op.hadDestination && destinationState == treeNew && backupState == treeOld:
			if err := discardDestination(w.project, paths, "uncommitted-destination"); err != nil {
				return err
			}
			if err := durableRename(paths.backup, paths.destination, w.project.SkillsDir(), "restore-old-destination"); err != nil {
				return err
			}
			return finishRecovery(w.project, paths, op, treeOld, recoveryPhaseCleanupRolledBack)
		case !op.hadDestination && destinationState == treeNew:
			if err := discardDestination(w.project, paths, "first-uncommitted-destination"); err != nil {
				return err
			}
			return finishRecovery(w.project, paths, op, treeAbsent, recoveryPhaseCleanupRolledBack)
		default:
			return recoveryError("old lock has no recoverable old destination", nil)
		}
	}
	return recoveryError("operation state is ambiguous", nil)
}

func finishRecovery(project Project, paths transactionPaths, operation recoveryOperation, destinationState treeState, phase recoveryPhase) error {
	if err := stabilizeDestination(project, paths.destination, destinationState); err != nil {
		return err
	}
	if operation.phase != phase {
		if err := writeJournal(paths.operationDir, operation.journalAt(phase)); err != nil {
			return err
		}
	}
	return cleanupOperation(project, paths.operationDir, operation.id)
}

func stabilizeDestination(project Project, destination string, state treeState) error {
	switch state {
	case treeOld, treeNew:
		// Recovery must finish the recorded plan even if the caller's
		// context is cancelled; a half-restored project is worse than a
		// slow one. It deliberately hashes and syncs without cancellation.
		if err := syncTree(context.Background(), destination, "recovery-destination-tree"); err != nil {
			return err
		}
	case treeAbsent:
		exists, err := inspectOptionalRealDirectory(destination)
		if err != nil {
			return err
		}
		if exists {
			return recoveryError("destination was expected to remain absent", nil)
		}
	default:
		return recoveryError("destination has an unknown recovery state", nil)
	}
	return syncDirectory(project.SkillsDir(), "recovery-destination-parent")
}

func discardDestination(project Project, paths transactionPaths, label string) error {
	discard := filepath.Join(paths.operationDir, discardName)
	discardExists, err := inspectOptionalRealDirectory(discard)
	if err != nil {
		return recoveryError("inspect operation discard", err)
	}
	if discardExists {
		return recoveryError("operation discard already exists while its destination remains live", nil)
	}
	if err := durableRename(paths.destination, discard, project.SkillsDir(), "discard-"+label); err != nil {
		return err
	}
	return durableRemoveAll(discard, paths.operationDir, "discard-"+label)
}

func classifyTree(path string, oldDigest *agentskill.TreeDigest, newDigest agentskill.TreeDigest) (treeState, error) {
	exists, err := inspectOptionalRealDirectory(path)
	if err != nil || !exists {
		return treeAbsent, err
	}
	// This helper only runs during journal replay, which must never bail
	// out mid-recovery, so it ignores caller cancellation on purpose.
	digest, err := agentskill.SumTree(context.Background(), os.DirFS(path), ".")
	if err != nil {
		return treeAbsent, err
	}
	// The new classification wins when old and new are equal.
	if digest == newDigest {
		return treeNew, nil
	}
	if oldDigest != nil && digest == *oldDigest {
		return treeOld, nil
	}
	return treeAbsent, fmt.Errorf("unexpected digest %s", digest.String())
}

func inspectOptionalRealDirectory(path string) (bool, error) {
	if err := rejectPathComponents(path, true); err != nil {
		return false, err
	}
	info, err := os.Lstat(path)
	if errors.Is(err, fs.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if pathInfoIsLink(info) || !info.IsDir() {
		return false, fmt.Errorf("%q is not a real directory", path)
	}
	return true, nil
}

func readManagedFile(path string) ([]byte, error) {
	if err := rejectRegularFile(path); err != nil {
		return nil, err
	}
	return os.ReadFile(path)
}

func readOptionalFile(path string) ([]byte, bool, error) {
	if err := rejectLink(path, true); err != nil {
		return nil, false, err
	}
	contents, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	return contents, true, nil
}

func restoreOldState(project Project, paths transactionPaths, operation recoveryOperation, destinationState, backupState treeState) error {
	if destinationState != treeOld && backupState == treeOld {
		if destinationState != treeAbsent {
			if err := discardDestination(project, paths, "rollback-new-destination"); err != nil {
				return err
			}
		}
		if err := durableRename(paths.backup, paths.destination, project.SkillsDir(), "rollback-old-destination"); err != nil {
			return err
		}
	}
	if operation.oldLock == nil || operation.oldLock.snapshot == nil {
		return recoveryError("old lock snapshot is unavailable while restoring", nil)
	}
	oldLockPath := filepath.Join(paths.operationDir, oldLockName)
	newLockPath := filepath.Join(paths.operationDir, newLockName)
	if err := prepareOldLockRestoreTemporary(project, operation.id, oldLockPath, newLockPath); err != nil {
		return err
	}
	return replaceLockFrom(project, operation.id, oldLockPath)
}

func prepareOldLockRestoreTemporary(project Project, operation, oldLockPath, newLockPath string) error {
	temporary := lockTemporaryPath(project, operation)
	temporaryContents, exists, err := readOptionalFile(temporary)
	if err != nil || !exists {
		return err
	}
	oldLockContents, err := readManagedFile(oldLockPath)
	if err != nil {
		return recoveryError("old lock snapshot is unavailable while inspecting its temporary", err)
	}
	if bytes.Equal(temporaryContents, oldLockContents) {
		// An interrupted old-lock replacement can resume its rename.
		return nil
	}
	newLockContents, err := readManagedFile(newLockPath)
	if err != nil {
		return recoveryError("new lock snapshot is unavailable while discarding its temporary", err)
	}
	if !bytes.Equal(temporaryContents, newLockContents) {
		return recoveryError("project lock temporary matches neither recovery action", nil)
	}
	return durableRemoveFile(temporary, filepath.Dir(project.LockPath()), "abandoned-new-project-lock-temporary")
}

func recoveryError(problem string, cause error) error {
	if cause == nil {
		return fmt.Errorf("%w: %s", ErrRecoveryRequired, problem)
	}
	return fmt.Errorf("%w: %s: %v", ErrRecoveryRequired, problem, cause)
}
