package install

import (
	"bytes"
	"context"
	"crypto/rand"
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

	"github.com/joshuadavidthomas/ts-skill-registry/internal/agentskill"
	"github.com/joshuadavidthomas/ts-skill-registry/internal/registry"
)

const (
	journalName = "journal.json"
	stagingName = "staging"
	backupName  = "backup"
	oldLockName = "old-lock"
	newLockName = "new-lock"
)

type transactionJournal struct {
	Schema         int    `json:"schema"`
	Operation      string `json:"operation"`
	Skill          string `json:"skill"`
	OldDigest      string `json:"old_digest"`
	NewDigest      string `json:"new_digest"`
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
	hadDestination, err := w.preflight(oldLock, publication.Skill())
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
	if err := os.Mkdir(operationDir, 0o700); err != nil {
		return fmt.Errorf("create project operation: %w", err)
	}
	if err := syncDirectory(w.project.operationsDir(), "create-operation"); err != nil {
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
	if err := syncTree(operationStaging, "staging-tree"); err != nil {
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
	journal := transactionJournal{
		Schema:         1,
		Operation:      operation,
		Skill:          publication.Skill().String(),
		OldDigest:      oldDigest,
		NewDigest:      publication.Tree().String(),
		HadLock:        hadLock,
		HadDestination: hadDestination,
		Phase:          "prepared",
	}
	if err := writeJournal(operationDir, journal); err != nil {
		return err
	}
	if _, err := verified.transfer(); err != nil {
		return err
	}

	destination := w.project.destination(publication.Skill().Name().String())
	backup := filepath.Join(operationDir, backupName)
	if hadDestination {
		if err := durableRename(destination, backup, w.project.SkillsDir(), "backup-destination"); err != nil {
			return err
		}
	}
	journal.Phase = "backup-created"
	if err := writeJournal(operationDir, journal); err != nil {
		return err
	}
	if err := durableRename(operationStaging, destination, w.project.SkillsDir(), "swap-destination"); err != nil {
		return err
	}
	if err := syncTree(destination, "destination-tree"); err != nil {
		return err
	}
	journal.Phase = "destination-swapped"
	if err := writeJournal(operationDir, journal); err != nil {
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
	if err := transactionPoint("before-write-" + label); err != nil {
		return err
	}
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, mode)
	if err != nil {
		return fmt.Errorf("write %s: %w", label, err)
	}
	_, writeErr := file.Write(contents)
	if writeErr == nil {
		writeErr = file.Sync()
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
	if err := transactionPoint("before-journal-write-" + journal.Phase); err != nil {
		return err
	}
	file, err := os.OpenFile(temporary, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return fmt.Errorf("create project journal: %w", err)
	}
	_, writeErr := file.Write(contents)
	if writeErr == nil {
		writeErr = file.Sync()
	}
	closeErr := file.Close()
	if err := errors.Join(writeErr, closeErr); err != nil {
		return fmt.Errorf("write project journal: %w", err)
	}
	if err := durableRename(temporary, filepath.Join(operationDir, journalName), operationDir, "journal-"+journal.Phase); err != nil {
		return err
	}
	return transactionPoint("after-journal-write-" + journal.Phase)
}

func durableRename(oldPath, newPath, syncParent, label string) error {
	if err := transactionPoint("before-rename-" + label); err != nil {
		return err
	}
	if err := os.Rename(oldPath, newPath); err != nil {
		return fmt.Errorf("rename %s: %w", label, err)
	}
	if err := transactionPoint("after-rename-" + label); err != nil {
		return err
	}
	parents := []string{filepath.Dir(oldPath), filepath.Dir(newPath), syncParent}
	synced := make(map[string]struct{}, len(parents))
	for _, parent := range parents {
		parent = filepath.Clean(parent)
		if _, found := synced[parent]; found {
			continue
		}
		if err := syncDirectory(parent, label); err != nil {
			return err
		}
		synced[parent] = struct{}{}
	}
	return nil
}

func syncDirectory(path, label string) error {
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

func syncTree(root, label string) error {
	directories := make([]string, 0)
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("tree path %q is a symbolic link", path)
		}
		if info.IsDir() {
			directories = append(directories, path)
			return nil
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("tree path %q is not regular", path)
		}
		if err := transactionPoint("before-fsync-" + label + "-file"); err != nil {
			return err
		}
		file, err := os.Open(path)
		if err != nil {
			return err
		}
		syncErr := file.Sync()
		closeErr := file.Close()
		if err := errors.Join(syncErr, closeErr); err != nil {
			return err
		}
		return transactionPoint("after-fsync-" + label + "-file")
	})
	if err != nil {
		return fmt.Errorf("sync %s: %w", label, err)
	}
	sort.Slice(directories, func(i, j int) bool {
		return strings.Count(directories[i], string(filepath.Separator)) > strings.Count(directories[j], string(filepath.Separator))
	})
	for _, directory := range directories {
		if err := syncDirectory(directory, label+"-directory"); err != nil {
			return err
		}
	}
	return nil
}

func replaceLockFrom(project Project, operation, source string) error {
	contents, err := os.ReadFile(source)
	if err != nil {
		return fmt.Errorf("read staged project lock: %w", err)
	}
	temporary := lockTemporaryPath(project, operation)
	if err := rejectLink(temporary, true); err != nil {
		return err
	}
	if _, err := os.Lstat(temporary); err == nil {
		return recoveryError("project lock temporary already exists", nil)
	} else if !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("inspect project lock temporary: %w", err)
	}
	if err := writeSyncedFile(temporary, contents, 0o600, "project-lock-temporary"); err != nil {
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
		info, err := os.Lstat(operationDir)
		if err != nil || info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
			return fmt.Errorf("%w: invalid operation path %q", ErrRecoveryRequired, operationDir)
		}
		journalPath := filepath.Join(operationDir, journalName)
		if _, err := os.Lstat(journalPath); errors.Is(err, fs.ErrNotExist) {
			if err := os.RemoveAll(operationDir); err != nil {
				return fmt.Errorf("remove incomplete operation: %w", err)
			}
			if err := syncDirectory(w.project.operationsDir(), "remove-journal-less-operation"); err != nil {
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
	oldLockPath := filepath.Join(operationDir, oldLockName)
	newLockPath := filepath.Join(operationDir, newLockName)
	var oldLockBytes []byte
	if journal.HadLock {
		oldLockBytes, err = os.ReadFile(oldLockPath)
		if err != nil {
			return recoveryError("old lock snapshot is unavailable", err)
		}
		if _, err := DecodeLock(bytes.NewReader(oldLockBytes)); err != nil {
			return recoveryError("old lock snapshot is invalid", err)
		}
	}
	newLockBytes, err := os.ReadFile(newLockPath)
	if err != nil {
		return recoveryError("new lock snapshot is unavailable", err)
	}
	if _, err := DecodeLock(bytes.NewReader(newLockBytes)); err != nil {
		return recoveryError("new lock snapshot is invalid", err)
	}

	actualLock, lockExists, err := readOptionalFile(w.project.LockPath())
	if err != nil {
		return recoveryError("project lock cannot be read", err)
	}
	lockIsNew := lockExists && bytes.Equal(actualLock, newLockBytes)
	lockIsOld := journal.HadLock && lockExists && bytes.Equal(actualLock, oldLockBytes)
	lockIsAbsentOld := !journal.HadLock && !lockExists
	if !lockIsNew && !lockIsOld && !lockIsAbsentOld {
		return recoveryError("project lock matches neither recorded state", nil)
	}
	// Equal snapshots mean the lock is new, as required for missing-destination restore.
	if lockIsNew && lockIsOld {
		lockIsOld = false
	}

	destination := w.project.destination(skill.Name().String())
	backup := filepath.Join(operationDir, backupName)
	staging := filepath.Join(operationDir, stagingName)
	hasOldDigest := journal.OldDigest != ""
	destinationState, err := classifyTree(destination, oldDigest, newDigest, hasOldDigest)
	if err != nil {
		return recoveryError("destination matches neither recorded tree", err)
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
			return cleanupOperation(w.project, operationDir, operation)
		case destinationState == treeAbsent && stagingState == treeNew:
			if err := durableRename(staging, destination, w.project.SkillsDir(), "recover-new-destination"); err != nil {
				return err
			}
			if err := syncTree(destination, "recovered-destination-tree"); err != nil {
				return err
			}
			return cleanupOperation(w.project, operationDir, operation)
		case oldDigest == newDigest && destinationState == treeAbsent && stagingState == treeAbsent:
			return cleanupOperation(w.project, operationDir, operation)
		case journal.HadLock && (destinationState == treeOld || backupState == treeOld):
			if err := restoreOldState(w.project, operation, journal, destination, backup, destinationState, backupState, oldLockPath); err != nil {
				return err
			}
			return cleanupOperation(w.project, operationDir, operation)
		default:
			return recoveryError("committed lock has no complete new or recoverable old destination", nil)
		}
	}

	if lockIsOld || lockIsAbsentOld {
		switch {
		case journal.HadDestination && destinationState == treeOld:
			return cleanupOperation(w.project, operationDir, operation)
		case !journal.HadDestination && destinationState == treeAbsent:
			return cleanupOperation(w.project, operationDir, operation)
		case journal.HadDestination && destinationState == treeAbsent && backupState == treeOld:
			if err := durableRename(backup, destination, w.project.SkillsDir(), "recover-old-destination"); err != nil {
				return err
			}
			return cleanupOperation(w.project, operationDir, operation)
		case journal.HadDestination && destinationState == treeNew && backupState == treeOld:
			if err := durableRemoveAll(destination, w.project.SkillsDir(), "remove-uncommitted-destination"); err != nil {
				return err
			}
			if err := durableRename(backup, destination, w.project.SkillsDir(), "restore-old-destination"); err != nil {
				return err
			}
			return cleanupOperation(w.project, operationDir, operation)
		case !journal.HadDestination && destinationState == treeNew:
			if err := durableRemoveAll(destination, w.project.SkillsDir(), "remove-first-uncommitted-destination"); err != nil {
				return err
			}
			return cleanupOperation(w.project, operationDir, operation)
		default:
			return recoveryError("old lock has no recoverable old destination", nil)
		}
	}
	return recoveryError("operation state is ambiguous", nil)
}

func readJournal(operationDir, operation string) (transactionJournal, registry.SkillID, agentskill.TreeDigest, agentskill.TreeDigest, error) {
	contents, err := os.ReadFile(filepath.Join(operationDir, journalName))
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
	if journal.Schema != 1 || journal.Operation != operation || !validOperationID(operation) {
		return transactionJournal{}, registry.SkillID{}, agentskill.TreeDigest{}, agentskill.TreeDigest{}, recoveryError("journal identity or schema is invalid", nil)
	}
	switch journal.Phase {
	case "prepared", "backup-created", "destination-swapped", "lock-committed":
	default:
		return transactionJournal{}, registry.SkillID{}, agentskill.TreeDigest{}, agentskill.TreeDigest{}, recoveryError("journal phase is invalid", nil)
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

func classifyTree(path string, oldDigest, newDigest agentskill.TreeDigest, hasOldDigest bool) (treeState, error) {
	exists, err := inspectOptionalRealDirectory(path)
	if err != nil || !exists {
		return treeAbsent, err
	}
	digest, err := agentskill.SumTree(os.DirFS(path), ".")
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
	info, err := os.Lstat(path)
	if errors.Is(err, fs.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return false, fmt.Errorf("%q is not a real directory", path)
	}
	return true, nil
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

func restoreOldState(project Project, operation string, journal transactionJournal, destination, backup string, destinationState, backupState treeState, oldLockPath string) error {
	if destinationState != treeOld && backupState == treeOld {
		if destinationState != treeAbsent {
			if err := durableRemoveAll(destination, project.SkillsDir(), "rollback-new-destination"); err != nil {
				return err
			}
		}
		if err := durableRename(backup, destination, project.SkillsDir(), "rollback-old-destination"); err != nil {
			return err
		}
	}
	if journal.HadLock {
		return replaceLockFrom(project, operation, oldLockPath)
	}
	if err := os.Remove(project.LockPath()); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	return syncDirectory(filepath.Dir(project.LockPath()), "remove-new-project-lock")
}

func durableRemoveAll(path, parent, label string) error {
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
	for _, name := range []string{backupName, stagingName, oldLockName, newLockName, journalName + ".tmp"} {
		if err := os.RemoveAll(filepath.Join(operationDir, name)); err != nil {
			return fmt.Errorf("clean project operation %s: %w", name, err)
		}
	}
	if err := os.Remove(lockTemporaryPath(project, operation)); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("clean project lock temporary: %w", err)
	}
	if err := os.Remove(filepath.Join(operationDir, journalName)); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("remove project journal: %w", err)
	}
	if err := syncDirectory(operationDir, "cleanup-operation"); err != nil {
		return err
	}
	if err := os.Remove(operationDir); err != nil {
		return fmt.Errorf("remove project operation: %w", err)
	}
	return syncDirectory(project.operationsDir(), "cleanup-operations-parent")
}

func recoveryError(problem string, cause error) error {
	if cause == nil {
		return fmt.Errorf("%w: %s", ErrRecoveryRequired, problem)
	}
	return fmt.Errorf("%w: %s: %v", ErrRecoveryRequired, problem, cause)
}
