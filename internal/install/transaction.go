package install

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"

	"github.com/joshuadavidthomas/ts-skills/internal/registry"
)

const (
	journalName = "journal.json"
	stagingName = "staging"
	backupName  = "backup"
	discardName = "discard"
	oldLockName = "old-lock"
	newLockName = "new-lock"
)

type transactionPaths struct {
	operationDir string
	staging      string
	backup       string
	destination  string
}

func newTransactionPaths(project Project, operation string, skill registry.SkillID) transactionPaths {
	operationDir := filepath.Join(project.operationsDir(), operation)
	return transactionPaths{
		operationDir: operationDir,
		staging:      filepath.Join(operationDir, stagingName),
		backup:       filepath.Join(operationDir, backupName),
		destination:  project.destination(skill.Name().String()),
	}
}

func (w *projectWriter) install(ctx context.Context, verified *verifiedTree, newLock Lock) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if verified == nil || !verified.owned {
		return fmt.Errorf("install requires an owned verified tree")
	}
	publication := verified.publication
	locked, found := newLock.Lookup(publication.Skill())
	if !found || locked.Publication() != publication {
		return fmt.Errorf("new lock does not contain the verified publication")
	}
	oldLock, oldLockBytes, hadLock, err := w.readLock()
	if err != nil {
		return err
	}
	hadDestination, err := w.preflight(ctx, oldLock, publication.Skill())
	if err != nil {
		return err
	}
	oldDigest := ""
	if previous, ok := oldLock.Lookup(publication.Skill()); ok {
		oldDigest = previous.Publication().Tree().String()
	}
	newLockBytes, err := encodeLockBytes(newLock)
	if err != nil {
		return err
	}
	operation, err := newOperationID()
	if err != nil {
		return err
	}
	paths := newTransactionPaths(w.project, operation, publication.Skill())
	if err := createManagedDirectory(paths.operationDir, 0o700, "project-operation"); err != nil {
		return err
	}

	oldStaging := verified.path
	if err := durableRename(oldStaging, paths.staging, w.project.StateDir(), "stage-operation"); err != nil {
		return err
	}
	delete(w.staging, oldStaging)
	w.staging[paths.staging] = struct{}{}
	verified.path = paths.staging
	if err := syncTree(ctx, paths.staging, "staging-tree"); err != nil {
		return err
	}
	if hadLock {
		if err := writeSyncedFile(filepath.Join(paths.operationDir, oldLockName), oldLockBytes, 0o600, "old-lock"); err != nil {
			return err
		}
	}
	if err := writeSyncedFile(filepath.Join(paths.operationDir, newLockName), newLockBytes, 0o600, "new-lock"); err != nil {
		return err
	}
	if err := w.preflightTransactionFilesystem(ctx, oldLock, publication.Skill(), hadDestination, hadLock, oldLockBytes, paths); err != nil {
		return err
	}
	journal := transactionJournal{
		Schema:         2,
		Operation:      operation,
		Skill:          publication.Skill().String(),
		OldDigest:      oldDigest,
		NewDigest:      publication.Tree().String(),
		NewLockHash:    lockSnapshotHash(newLockBytes),
		HadLock:        hadLock,
		HadDestination: hadDestination,
		Phase:          "prepared",
	}
	if hadLock {
		journal.OldLockHash = lockSnapshotHash(oldLockBytes)
	}
	if err := writeJournal(paths.operationDir, journal); err != nil {
		return err
	}
	if _, err := verified.transfer(); err != nil {
		return err
	}
	// Cancellation only interrupts between journaled phases; an interrupted
	// phase leaves a journal the next writer recovers from.
	if err := ctx.Err(); err != nil {
		return err
	}

	if hadDestination {
		if err := durableRename(paths.destination, paths.backup, w.project.SkillsDir(), "backup-destination"); err != nil {
			return err
		}
	}
	journal.Phase = "backup-created"
	if err := writeJournal(paths.operationDir, journal); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := durableRename(paths.staging, paths.destination, w.project.SkillsDir(), "swap-destination"); err != nil {
		return err
	}
	if err := syncTree(ctx, paths.destination, "destination-tree"); err != nil {
		return err
	}
	journal.Phase = "destination-swapped"
	if err := writeJournal(paths.operationDir, journal); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := replaceLockFrom(w.project, operation, filepath.Join(paths.operationDir, newLockName)); err != nil {
		return err
	}
	journal.Phase = "lock-committed"
	if err := writeJournal(paths.operationDir, journal); err != nil {
		// The lock and destination already agree. Recovery will classify this as committed.
		return nil
	}
	// Cleanup cannot revoke a committed install. A later writer retries it.
	_ = cleanupOperation(w.project, paths.operationDir, operation)
	return nil
}

func (w *projectWriter) preflightTransactionFilesystem(
	ctx context.Context,
	oldLock Lock,
	skill registry.SkillID,
	hadDestination bool,
	hadLock bool,
	oldLockBytes []byte,
	paths transactionPaths,
) error {
	for _, path := range []string{
		w.project.StateDir(),
		w.project.SkillsDir(),
		filepath.Dir(w.project.LockPath()),
		w.project.LockPath(),
		paths.operationDir,
		paths.staging,
		paths.backup,
		paths.destination,
	} {
		if err := rejectPathComponents(path, true); err != nil {
			return fmt.Errorf("preflight managed transaction path: %w", err)
		}
	}
	if err := ensureRealDirectory(paths.operationDir, false); err != nil {
		return err
	}
	if err := ensureRealDirectory(paths.staging, false); err != nil {
		return err
	}
	if exists, err := inspectOptionalRealDirectory(paths.backup); err != nil {
		return fmt.Errorf("inspect operation backup: %w", err)
	} else if exists {
		return fmt.Errorf("operation backup exists before journal preparation")
	}
	if exists, err := inspectOptionalRealDirectory(filepath.Join(paths.operationDir, discardName)); err != nil {
		return fmt.Errorf("inspect operation discard: %w", err)
	} else if exists {
		return fmt.Errorf("operation discard exists before journal preparation")
	}
	stillHadDestination, err := w.preflight(ctx, oldLock, skill)
	if err != nil {
		return err
	}
	if stillHadDestination != hadDestination {
		return fmt.Errorf("skill destination changed during transaction preflight")
	}
	actualLockBytes, lockExists, err := readOptionalFile(w.project.LockPath())
	if err != nil {
		return fmt.Errorf("inspect project lock during transaction preflight: %w", err)
	}
	if lockExists != hadLock || (lockExists && !bytes.Equal(actualLockBytes, oldLockBytes)) {
		return fmt.Errorf("project lock changed during transaction preflight")
	}
	if lockExists {
		if err := rejectRegularFile(w.project.LockPath()); err != nil {
			return err
		}
	}

	filesystemPaths := []string{
		w.project.SkillsDir(),
		w.project.StateDir(),
		filepath.Dir(w.project.LockPath()),
		paths.operationDir,
		paths.staging,
	}
	if stillHadDestination {
		filesystemPaths = append(filesystemPaths, paths.destination)
	} else {
		filesystemPaths = append(filesystemPaths, w.project.SkillsDir())
	}
	// A missing backup will be created by renaming into its existing operation
	// directory, so the operation directory proves its filesystem placement.
	filesystemPaths = append(filesystemPaths, paths.operationDir)
	if lockExists {
		filesystemPaths = append(filesystemPaths, w.project.LockPath())
	}
	if err := requireSameFilesystem(w.project.root, filesystemPaths...); err != nil {
		return fmt.Errorf("preflight project transaction filesystem: %w", err)
	}
	return nil
}

func encodeLockBytes(lock Lock) ([]byte, error) {
	var buffer bytes.Buffer
	if err := EncodeLock(&buffer, lock); err != nil {
		return nil, err
	}
	return buffer.Bytes(), nil
}

func newOperationID() (string, error) {
	var value [16]byte
	if _, err := rand.Read(value[:]); err != nil {
		return "", fmt.Errorf("create project operation identity: %w", err)
	}
	return hex.EncodeToString(value[:]), nil
}

func replaceLockFrom(project Project, operation, source string) error {
	contents, err := readManagedFile(source)
	if err != nil {
		return fmt.Errorf("read staged project lock: %w", err)
	}
	temporary := lockTemporaryPath(project, operation)
	if err := rejectLink(temporary, true); err != nil {
		return err
	}
	temporaryContents, exists, err := readOptionalFile(temporary)
	if err != nil {
		return fmt.Errorf("inspect project lock temporary: %w", err)
	}
	if exists {
		if !bytes.Equal(temporaryContents, contents) {
			return recoveryError("project lock temporary does not match its operation", nil)
		}
		// The first attempt may have stopped after writing but before its rename.
		// Sync again so this retry does not rely on that attempt reaching its barrier.
		if err := syncFile(temporary, "project-lock-temporary"); err != nil {
			return err
		}
	} else if err := writeSyncedFile(temporary, contents, 0o600, "project-lock-temporary"); err != nil {
		return err
	}
	return durableRename(temporary, project.LockPath(), filepath.Dir(project.LockPath()), "project-lock")
}

func lockTemporaryPath(project Project, operation string) string {
	return filepath.Join(filepath.Dir(project.LockPath()), ".ts-skills-lock-"+operation+".tmp")
}

func cleanupOperation(project Project, operationDir, operation string) error {
	for _, name := range []string{backupName, stagingName, discardName, oldLockName, newLockName, journalName + ".tmp"} {
		label := "cleanup-" + name
		path := filepath.Join(operationDir, name)
		if err := rejectPathComponents(path, true); err != nil {
			return err
		}
		if err := transactionPoint("before-remove-" + label); err != nil {
			return err
		}
		if err := os.RemoveAll(path); err != nil {
			return fmt.Errorf("clean project operation %s: %w", name, err)
		}
		if err := transactionPoint("after-remove-" + label); err != nil {
			return err
		}
	}
	// Persist snapshot removal before removing the journal. If the journal
	// survives an interruption, recovery classifies the actual committed state.
	if err := syncDirectory(operationDir, "cleanup-snapshots"); err != nil {
		return err
	}

	if err := durableRemoveFile(lockTemporaryPath(project, operation), filepath.Dir(project.LockPath()), "cleanup-project-lock-temporary"); err != nil {
		return err
	}
	if err := durableRemoveFile(filepath.Join(operationDir, journalName), operationDir, "cleanup-journal"); err != nil {
		return err
	}
	if err := rejectPathComponents(operationDir, false); err != nil {
		return err
	}
	if err := transactionPoint("before-remove-cleanup-operation"); err != nil {
		return err
	}
	if err := os.Remove(operationDir); err != nil {
		return fmt.Errorf("remove project operation: %w", err)
	}
	if err := transactionPoint("after-remove-cleanup-operation"); err != nil {
		return err
	}
	return syncDirectory(project.operationsDir(), "cleanup-operations-parent")
}
