package install

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/joshuadavidthomas/ts-skills/internal/agentskill"
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

type transactionJournal struct {
	Schema         int    `json:"schema"`
	Operation      string `json:"operation"`
	Skill          string `json:"skill"`
	OldDigest      string `json:"old_digest"`
	NewDigest      string `json:"new_digest"`
	OldLockHash    string `json:"old_lock_hash"`
	NewLockHash    string `json:"new_lock_hash"`
	HadLock        bool   `json:"had_lock"`
	HadDestination bool   `json:"had_destination"`
	Phase          string `json:"phase"`
}

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
	operationDir := filepath.Join(w.project.operationsDir(), operation)
	if err := createManagedDirectory(operationDir, 0o700, "project-operation"); err != nil {
		return err
	}

	operationStaging := filepath.Join(operationDir, stagingName)
	oldStaging := verified.path
	if err := durableRename(oldStaging, operationStaging, w.project.StateDir(), "stage-operation"); err != nil {
		return err
	}
	delete(w.staging, oldStaging)
	w.staging[operationStaging] = struct{}{}
	verified.path = operationStaging
	if err := syncTree(ctx, operationStaging, "staging-tree"); err != nil {
		return err
	}
	if hadLock {
		if err := writeSyncedFile(filepath.Join(operationDir, oldLockName), oldLockBytes, 0o600, "old-lock"); err != nil {
			return err
		}
	}
	if err := writeSyncedFile(filepath.Join(operationDir, newLockName), newLockBytes, 0o600, "new-lock"); err != nil {
		return err
	}
	destination := w.project.destination(publication.Skill().Name().String())
	backup := filepath.Join(operationDir, backupName)
	if err := w.preflightTransactionFilesystem(ctx, oldLock, publication.Skill(), hadDestination, hadLock, oldLockBytes, operationDir, operationStaging, backup, destination); err != nil {
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
	if err := writeJournal(operationDir, journal); err != nil {
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
		if err := durableRename(destination, backup, w.project.SkillsDir(), "backup-destination"); err != nil {
			return err
		}
	}
	journal.Phase = "backup-created"
	if err := writeJournal(operationDir, journal); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := durableRename(operationStaging, destination, w.project.SkillsDir(), "swap-destination"); err != nil {
		return err
	}
	if err := syncTree(ctx, destination, "destination-tree"); err != nil {
		return err
	}
	journal.Phase = "destination-swapped"
	if err := writeJournal(operationDir, journal); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := replaceLockFrom(w.project, operation, filepath.Join(operationDir, newLockName)); err != nil {
		return err
	}
	journal.Phase = "lock-committed"
	if err := writeJournal(operationDir, journal); err != nil {
		// The lock and destination already agree. Recovery will classify this as committed.
		return nil
	}
	// Cleanup cannot revoke a committed install. A later writer retries it.
	_ = cleanupOperation(w.project, operationDir, operation)
	return nil
}

func (w *projectWriter) preflightTransactionFilesystem(
	ctx context.Context,
	oldLock Lock,
	skill registry.SkillID,
	hadDestination bool,
	hadLock bool,
	oldLockBytes []byte,
	operationDir string,
	operationStaging string,
	backup string,
	destination string,
) error {
	for _, path := range []string{
		w.project.StateDir(),
		w.project.SkillsDir(),
		filepath.Dir(w.project.LockPath()),
		w.project.LockPath(),
		operationDir,
		operationStaging,
		backup,
		destination,
	} {
		if err := rejectPathComponents(path, true); err != nil {
			return fmt.Errorf("preflight managed transaction path: %w", err)
		}
	}
	if err := ensureRealDirectory(operationDir, false); err != nil {
		return err
	}
	if err := ensureRealDirectory(operationStaging, false); err != nil {
		return err
	}
	if exists, err := inspectOptionalRealDirectory(backup); err != nil {
		return fmt.Errorf("inspect operation backup: %w", err)
	} else if exists {
		return fmt.Errorf("operation backup exists before journal preparation")
	}
	if exists, err := inspectOptionalRealDirectory(filepath.Join(operationDir, discardName)); err != nil {
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

	paths := []string{
		w.project.SkillsDir(),
		w.project.StateDir(),
		filepath.Dir(w.project.LockPath()),
		operationDir,
		operationStaging,
	}
	if stillHadDestination {
		paths = append(paths, destination)
	} else {
		paths = append(paths, w.project.SkillsDir())
	}
	// A missing backup will be created by renaming into its existing operation
	// directory, so the operation directory proves its filesystem placement.
	paths = append(paths, operationDir)
	if lockExists {
		paths = append(paths, w.project.LockPath())
	}
	if err := requireSameFilesystem(w.project.root, paths...); err != nil {
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

func writeJournal(operationDir string, journal transactionJournal) error {
	contents, err := json.Marshal(journal)
	if err != nil {
		return fmt.Errorf("encode project journal: %w", err)
	}
	contents = append(contents, '\n')
	temporary := filepath.Join(operationDir, journalName+".tmp")
	if err := rejectPathComponents(temporary, true); err != nil {
		return err
	}
	if err := transactionPoint("before-journal-write-" + journal.Phase); err != nil {
		return err
	}
	temporaryContents, exists, err := readOptionalFile(temporary)
	if err != nil {
		return fmt.Errorf("inspect project journal temporary: %w", err)
	}
	if exists && !bytes.Equal(temporaryContents, contents) {
		// The durable journal remains authoritative until this rename commits.
		// A retry may choose a different final recovery phase than an abandoned
		// temporary prepared by the interrupted attempt.
		if err := durableRemoveFile(temporary, operationDir, "stale-project-journal-temporary"); err != nil {
			return err
		}
		exists = false
	}
	if exists {
		if err := syncFile(temporary, "journal-"+journal.Phase); err != nil {
			return err
		}
	} else {
		file, err := os.OpenFile(temporary, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		if err != nil {
			return fmt.Errorf("create project journal: %w", err)
		}
		_, writeErr := file.Write(contents)
		if writeErr == nil {
			writeErr = syncOpenFile(file, "journal-"+journal.Phase)
		}
		closeErr := file.Close()
		if err := errors.Join(writeErr, closeErr); err != nil {
			return fmt.Errorf("write project journal: %w", err)
		}
	}
	if err := durableRename(temporary, filepath.Join(operationDir, journalName), operationDir, "journal-"+journal.Phase); err != nil {
		return err
	}
	return transactionPoint("after-journal-write-" + journal.Phase)
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

func (w *projectWriter) recover() error {
	entries, err := os.ReadDir(w.project.operationsDir())
	if err != nil {
		return fmt.Errorf("read project operations: %w", err)
	}
	for _, entry := range entries {
		operationDir := filepath.Join(w.project.operationsDir(), entry.Name())
		if err := rejectPathComponents(operationDir, false); err != nil {
			return fmt.Errorf("%w: invalid operation path %q: %v", ErrRecoveryRequired, operationDir, err)
		}
		info, err := os.Lstat(operationDir)
		if err != nil || pathInfoIsLink(info) || !info.IsDir() {
			return fmt.Errorf("%w: invalid operation path %q", ErrRecoveryRequired, operationDir)
		}
		journalPath := filepath.Join(operationDir, journalName)
		if err := rejectPathComponents(journalPath, true); err != nil {
			return fmt.Errorf("inspect operation journal: %w", err)
		}
		if _, err := os.Lstat(journalPath); errors.Is(err, fs.ErrNotExist) {
			if err := durableRemoveAll(operationDir, w.project.operationsDir(), "journal-less-operation"); err != nil {
				return err
			}
			continue
		} else if err != nil {
			return fmt.Errorf("inspect operation journal: %w", err)
		}
		if err := w.recoverOperation(operationDir, entry.Name()); err != nil {
			return err
		}
	}
	return nil
}

type treeState uint8

const (
	treeAbsent treeState = iota
	treeOld
	treeNew
)

func (w *projectWriter) recoverOperation(operationDir, operation string) error {
	journal, skill, oldDigest, newDigest, err := readJournal(operationDir, operation)
	if err != nil {
		return err
	}

	actualLockBytes, lockExists, err := readOptionalFile(w.project.LockPath())
	if err != nil {
		return recoveryError("project lock cannot be read", err)
	}
	if lockExists {
		if _, err := DecodeLock(bytes.NewReader(actualLockBytes)); err != nil {
			return recoveryError("project lock is invalid", err)
		}
	}

	destination := w.project.destination(skill.Name().String())
	backup := filepath.Join(operationDir, backupName)
	staging := filepath.Join(operationDir, stagingName)
	hasOldDigest := journal.OldDigest != ""
	destinationState, err := classifyTree(destination, oldDigest, newDigest, hasOldDigest)
	if err != nil {
		return recoveryError("destination matches neither recorded tree", err)
	}

	oldLockPath := filepath.Join(operationDir, oldLockName)
	newLockPath := filepath.Join(operationDir, newLockName)
	_, oldLockExists, err := readLockSnapshot(oldLockPath, journal.OldLockHash)
	if err != nil {
		return recoveryError("old lock snapshot is invalid", err)
	}
	if oldLockExists != journal.HadLock && oldLockExists {
		return recoveryError("unexpected old lock snapshot", nil)
	}
	_, newLockExists, err := readLockSnapshot(newLockPath, journal.NewLockHash)
	if err != nil {
		return recoveryError("new lock snapshot is invalid", err)
	}

	lockIsNew := lockExists && lockSnapshotHash(actualLockBytes) == journal.NewLockHash
	lockIsOld := journal.HadLock && lockExists && lockSnapshotHash(actualLockBytes) == journal.OldLockHash
	lockIsAbsentOld := !journal.HadLock && !lockExists
	if !lockIsNew && !lockIsOld && !lockIsAbsentOld {
		return recoveryError("project lock matches neither exact recorded state", nil)
	}
	// Recovery prefers the new state below when equal snapshots match both.
	// Keep both matches so a persisted rolled-back cleanup phase can still
	// validate the unchanged lock.
	lockSnapshotsEqual := lockIsNew && lockIsOld

	switch journal.Phase {
	case "cleanup-committed":
		if !lockIsNew || destinationState != treeNew {
			return recoveryError("committed cleanup state no longer matches its final plan", nil)
		}
		return finishRecovery(w.project, operationDir, operation, journal, destination, treeNew, "cleanup-committed")
	case "cleanup-rolled-back":
		oldDestinationState := treeAbsent
		if journal.HadDestination {
			oldDestinationState = treeOld
		}
		if (!lockIsOld && !lockIsAbsentOld) || destinationState != oldDestinationState {
			return recoveryError("rolled-back cleanup state no longer matches its final plan", nil)
		}
		return finishRecovery(w.project, operationDir, operation, journal, destination, oldDestinationState, "cleanup-rolled-back")
	case "lock-committed":
		if lockIsNew && destinationState == treeNew {
			return finishRecovery(w.project, operationDir, operation, journal, destination, treeNew, "cleanup-committed")
		}
	}

	if (journal.HadLock && !oldLockExists) || !newLockExists {
		return recoveryError("lock snapshots were removed before cleanup was committed", nil)
	}

	backupState, err := classifyTree(backup, oldDigest, newDigest, hasOldDigest)
	if hasOldDigest && oldDigest == newDigest && backupState == treeNew {
		backupState = treeOld
	}
	if err != nil || (backupState != treeAbsent && backupState != treeOld) {
		return recoveryError("backup matches neither recorded old tree nor absence", err)
	}
	stagingState, err := classifyTree(staging, oldDigest, newDigest, hasOldDigest)
	if err != nil || (stagingState != treeAbsent && stagingState != treeNew) {
		return recoveryError("staging matches neither recorded new tree nor absence", err)
	}

	if lockIsNew {
		switch {
		case destinationState == treeNew:
			return finishRecovery(w.project, operationDir, operation, journal, destination, treeNew, "cleanup-committed")
		case destinationState == treeAbsent && stagingState == treeNew:
			if err := durableRename(staging, destination, w.project.SkillsDir(), "recover-new-destination"); err != nil {
				return err
			}
			return finishRecovery(w.project, operationDir, operation, journal, destination, treeNew, "cleanup-committed")
		case journal.Phase == "prepared" && lockSnapshotsEqual && !journal.HadDestination && destinationState == treeAbsent && stagingState == treeAbsent && backupState == treeAbsent:
			// Restore can prepare an install for a missing destination without
			// changing the lock. If it stops after writing the journal but before
			// the writer drops its staging entry, writer cleanup normally removes
			// that tree. Roll back the empty operation so this or a later Restore
			// can fetch it again. If cleanup leaves the tree intact, the
			// staging-present case above finishes it.
			return finishRecovery(w.project, operationDir, operation, journal, destination, treeAbsent, "cleanup-rolled-back")
		case journal.HadLock && (destinationState == treeOld || backupState == treeOld):
			if err := restoreOldState(w.project, operationDir, operation, journal, destination, backup, destinationState, backupState, oldLockPath); err != nil {
				return err
			}
			return finishRecovery(w.project, operationDir, operation, journal, destination, treeOld, "cleanup-rolled-back")
		default:
			return recoveryError("committed lock has no complete new or recoverable old destination", nil)
		}
	}

	if lockIsOld || lockIsAbsentOld {
		switch {
		case journal.HadDestination && destinationState == treeOld:
			return finishRecovery(w.project, operationDir, operation, journal, destination, treeOld, "cleanup-rolled-back")
		case !journal.HadDestination && destinationState == treeAbsent:
			return finishRecovery(w.project, operationDir, operation, journal, destination, treeAbsent, "cleanup-rolled-back")
		case journal.HadDestination && destinationState == treeAbsent && backupState == treeOld:
			if err := durableRename(backup, destination, w.project.SkillsDir(), "recover-old-destination"); err != nil {
				return err
			}
			return finishRecovery(w.project, operationDir, operation, journal, destination, treeOld, "cleanup-rolled-back")
		case journal.HadDestination && destinationState == treeNew && backupState == treeOld:
			if err := discardDestination(w.project, operationDir, destination, "uncommitted-destination"); err != nil {
				return err
			}
			if err := durableRename(backup, destination, w.project.SkillsDir(), "restore-old-destination"); err != nil {
				return err
			}
			return finishRecovery(w.project, operationDir, operation, journal, destination, treeOld, "cleanup-rolled-back")
		case !journal.HadDestination && destinationState == treeNew:
			if err := discardDestination(w.project, operationDir, destination, "first-uncommitted-destination"); err != nil {
				return err
			}
			return finishRecovery(w.project, operationDir, operation, journal, destination, treeAbsent, "cleanup-rolled-back")
		default:
			return recoveryError("old lock has no recoverable old destination", nil)
		}
	}
	return recoveryError("operation state is ambiguous", nil)
}

func readLockSnapshot(path, expectedHash string) ([]byte, bool, error) {
	contents, exists, err := readOptionalFile(path)
	if err != nil || !exists {
		return nil, exists, err
	}
	if lockSnapshotHash(contents) != expectedHash {
		return nil, true, fmt.Errorf("snapshot contents do not match the journal hash")
	}
	if _, err := DecodeLock(bytes.NewReader(contents)); err != nil {
		return nil, true, err
	}
	return contents, true, nil
}

func lockSnapshotHash(contents []byte) string {
	digest := sha256.Sum256(contents)
	return hex.EncodeToString(digest[:])
}

func finishRecovery(project Project, operationDir, operation string, journal transactionJournal, destination string, destinationState treeState, phase string) error {
	if err := stabilizeDestination(project, destination, destinationState); err != nil {
		return err
	}
	if journal.Phase != phase {
		journal.Phase = phase
		if err := writeJournal(operationDir, journal); err != nil {
			return err
		}
	}
	return cleanupOperation(project, operationDir, operation)
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

func discardDestination(project Project, operationDir, destination, label string) error {
	discard := filepath.Join(operationDir, discardName)
	discardExists, err := inspectOptionalRealDirectory(discard)
	if err != nil {
		return recoveryError("inspect operation discard", err)
	}
	if discardExists {
		return recoveryError("operation discard already exists while its destination remains live", nil)
	}
	if err := durableRename(destination, discard, project.SkillsDir(), "discard-"+label); err != nil {
		return err
	}
	return durableRemoveAll(discard, operationDir, "discard-"+label)
}

func readJournal(operationDir, operation string) (transactionJournal, registry.SkillID, agentskill.TreeDigest, agentskill.TreeDigest, error) {
	journalPath := filepath.Join(operationDir, journalName)
	if err := rejectRegularFile(journalPath); err != nil {
		return transactionJournal{}, registry.SkillID{}, agentskill.TreeDigest{}, agentskill.TreeDigest{}, recoveryError("inspect journal", err)
	}
	contents, err := os.ReadFile(journalPath)
	if err != nil {
		return transactionJournal{}, registry.SkillID{}, agentskill.TreeDigest{}, agentskill.TreeDigest{}, recoveryError("read journal", err)
	}
	var journal transactionJournal
	decoder := json.NewDecoder(bytes.NewReader(contents))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&journal); err != nil {
		return transactionJournal{}, registry.SkillID{}, agentskill.TreeDigest{}, agentskill.TreeDigest{}, recoveryError("decode journal", err)
	}
	if err := ensureJSONEOF(decoder); err != nil {
		return transactionJournal{}, registry.SkillID{}, agentskill.TreeDigest{}, agentskill.TreeDigest{}, recoveryError("decode journal", err)
	}
	if journal.Schema != 2 || journal.Operation != operation || !validOperationID(operation) {
		return transactionJournal{}, registry.SkillID{}, agentskill.TreeDigest{}, agentskill.TreeDigest{}, recoveryError("journal identity or schema is invalid", nil)
	}
	switch journal.Phase {
	case "prepared", "backup-created", "destination-swapped", "lock-committed", "cleanup-committed", "cleanup-rolled-back":
	default:
		return transactionJournal{}, registry.SkillID{}, agentskill.TreeDigest{}, agentskill.TreeDigest{}, recoveryError("journal phase is invalid", nil)
	}
	if !validSnapshotHash(journal.NewLockHash) || (journal.HadLock != validSnapshotHash(journal.OldLockHash)) {
		return transactionJournal{}, registry.SkillID{}, agentskill.TreeDigest{}, agentskill.TreeDigest{}, recoveryError("journal lock snapshot hashes are invalid", nil)
	}
	skill, err := registry.ParseSkillID(journal.Skill)
	if err != nil {
		return transactionJournal{}, registry.SkillID{}, agentskill.TreeDigest{}, agentskill.TreeDigest{}, recoveryError("journal skill is invalid", err)
	}
	newDigest, err := agentskill.ParseTreeDigest(journal.NewDigest)
	if err != nil {
		return transactionJournal{}, registry.SkillID{}, agentskill.TreeDigest{}, agentskill.TreeDigest{}, recoveryError("journal new digest is invalid", err)
	}
	var oldDigest agentskill.TreeDigest
	if journal.OldDigest != "" {
		oldDigest, err = agentskill.ParseTreeDigest(journal.OldDigest)
		if err != nil {
			return transactionJournal{}, registry.SkillID{}, agentskill.TreeDigest{}, agentskill.TreeDigest{}, recoveryError("journal old digest is invalid", err)
		}
	} else if journal.HadDestination {
		return transactionJournal{}, registry.SkillID{}, agentskill.TreeDigest{}, agentskill.TreeDigest{}, recoveryError("journal omits the old destination digest", nil)
	}
	return journal, skill, oldDigest, newDigest, nil
}

func ensureJSONEOF(decoder *json.Decoder) error {
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		if err == nil {
			return fmt.Errorf("multiple JSON values")
		}
		return err
	}
	return nil
}

func validOperationID(value string) bool {
	if len(value) != 32 {
		return false
	}
	decoded, err := hex.DecodeString(value)
	return err == nil && hex.EncodeToString(decoded) == value
}

func validSnapshotHash(value string) bool {
	if len(value) != sha256.Size*2 {
		return false
	}
	decoded, err := hex.DecodeString(value)
	return err == nil && hex.EncodeToString(decoded) == value
}

func classifyTree(path string, oldDigest, newDigest agentskill.TreeDigest, hasOldDigest bool) (treeState, error) {
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
	if hasOldDigest && digest == oldDigest {
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

func restoreOldState(project Project, operationDir, operation string, journal transactionJournal, destination, backup string, destinationState, backupState treeState, oldLockPath string) error {
	if destinationState != treeOld && backupState == treeOld {
		if destinationState != treeAbsent {
			if err := discardDestination(project, operationDir, destination, "rollback-new-destination"); err != nil {
				return err
			}
		}
		if err := durableRename(backup, destination, project.SkillsDir(), "rollback-old-destination"); err != nil {
			return err
		}
	}
	if journal.HadLock {
		newLockPath := filepath.Join(filepath.Dir(oldLockPath), newLockName)
		if err := prepareOldLockRestoreTemporary(project, operation, oldLockPath, newLockPath); err != nil {
			return err
		}
		return replaceLockFrom(project, operation, oldLockPath)
	}
	return durableRemoveFile(project.LockPath(), filepath.Dir(project.LockPath()), "new-project-lock")
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

func recoveryError(problem string, cause error) error {
	if cause == nil {
		return fmt.Errorf("%w: %s", ErrRecoveryRequired, problem)
	}
	return fmt.Errorf("%w: %s: %v", ErrRecoveryRequired, problem, cause)
}
