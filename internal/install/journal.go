package install

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/joshuadavidthomas/ts-skills/internal/agentskill"
	"github.com/joshuadavidthomas/ts-skills/internal/registry"
)

// transactionJournal is the stable on-disk encoding. Recovery converts it
// into recoveryOperation before it makes any decision from its contents.
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

type recoveryPhase uint8

const (
	recoveryPhaseInvalid recoveryPhase = iota
	recoveryPhasePrepared
	recoveryPhaseBackupCreated
	recoveryPhaseDestinationSwapped
	recoveryPhaseLockCommitted
	recoveryPhaseCleanupCommitted
	recoveryPhaseCleanupRolledBack
)

type lockSnapshot struct {
	contents []byte
	lock     Lock
}

type recoveryLock struct {
	hash     string
	snapshot *lockSnapshot
}

type recoveryOperation struct {
	id             string
	skill          registry.SkillID
	phase          recoveryPhase
	oldDigest      *agentskill.TreeDigest
	newDigest      agentskill.TreeDigest
	oldLock        *recoveryLock
	newLock        recoveryLock
	hadDestination bool
}

func (o recoveryOperation) hadLock() bool { return o.oldLock != nil }

func (o recoveryOperation) journalAt(phase recoveryPhase) transactionJournal {
	journal := transactionJournal{
		Schema:         2,
		Operation:      o.id,
		Skill:          o.skill.String(),
		NewDigest:      o.newDigest.String(),
		NewLockHash:    o.newLock.hash,
		HadLock:        o.hadLock(),
		HadDestination: o.hadDestination,
		Phase:          phase.String(),
	}
	if o.oldDigest != nil {
		journal.OldDigest = o.oldDigest.String()
	}
	if o.oldLock != nil {
		journal.OldLockHash = o.oldLock.hash
	}
	return journal
}

func (p recoveryPhase) String() string {
	switch p {
	case recoveryPhasePrepared:
		return "prepared"
	case recoveryPhaseBackupCreated:
		return "backup-created"
	case recoveryPhaseDestinationSwapped:
		return "destination-swapped"
	case recoveryPhaseLockCommitted:
		return "lock-committed"
	case recoveryPhaseCleanupCommitted:
		return "cleanup-committed"
	case recoveryPhaseCleanupRolledBack:
		return "cleanup-rolled-back"
	default:
		return ""
	}
}

func parseRecoveryPhase(value string) recoveryPhase {
	for phase := recoveryPhasePrepared; phase <= recoveryPhaseCleanupRolledBack; phase++ {
		if phase.String() == value {
			return phase
		}
	}
	return recoveryPhaseInvalid
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

func readLockSnapshot(path, expectedHash string) (*lockSnapshot, error) {
	contents, exists, err := readOptionalFile(path)
	if err != nil || !exists {
		return nil, err
	}
	if lockSnapshotHash(contents) != expectedHash {
		return nil, fmt.Errorf("snapshot contents do not match the journal hash")
	}
	lock, err := DecodeLock(bytes.NewReader(contents))
	if err != nil {
		return nil, err
	}
	return &lockSnapshot{contents: contents, lock: lock}, nil
}

func lockSnapshotHash(contents []byte) string {
	digest := sha256.Sum256(contents)
	return hex.EncodeToString(digest[:])
}

func readJournal(operationDir, operation string) (recoveryOperation, error) {
	journalPath := filepath.Join(operationDir, journalName)
	if err := rejectRegularFile(journalPath); err != nil {
		return recoveryOperation{}, recoveryError("inspect journal", err)
	}
	contents, err := os.ReadFile(journalPath)
	if err != nil {
		return recoveryOperation{}, recoveryError("read journal", err)
	}
	var journal transactionJournal
	decoder := json.NewDecoder(bytes.NewReader(contents))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&journal); err != nil {
		return recoveryOperation{}, recoveryError("decode journal", err)
	}
	if err := ensureJSONEOF(decoder); err != nil {
		return recoveryOperation{}, recoveryError("decode journal", err)
	}
	phase := parseRecoveryPhase(journal.Phase)
	if journal.Schema != 2 || journal.Operation != operation || !validOperationID(operation) {
		return recoveryOperation{}, recoveryError("journal identity or schema is invalid", nil)
	}
	if phase == recoveryPhaseInvalid {
		return recoveryOperation{}, recoveryError("journal phase is invalid", nil)
	}
	if !validSnapshotHash(journal.NewLockHash) || (journal.HadLock && !validSnapshotHash(journal.OldLockHash)) || (!journal.HadLock && journal.OldLockHash != "") {
		return recoveryOperation{}, recoveryError("journal lock snapshot hashes are invalid", nil)
	}
	skill, err := registry.ParseSkillID(journal.Skill)
	if err != nil {
		return recoveryOperation{}, recoveryError("journal skill is invalid", err)
	}
	newDigest, err := agentskill.ParseTreeDigest(journal.NewDigest)
	if err != nil {
		return recoveryOperation{}, recoveryError("journal new digest is invalid", err)
	}
	var oldDigest *agentskill.TreeDigest
	if journal.OldDigest != "" {
		digest, err := agentskill.ParseTreeDigest(journal.OldDigest)
		if err != nil {
			return recoveryOperation{}, recoveryError("journal old digest is invalid", err)
		}
		oldDigest = &digest
	}
	if journal.HadDestination && (oldDigest == nil || !journal.HadLock) {
		return recoveryOperation{}, recoveryError("journal destination provenance is invalid", nil)
	}
	if !journal.HadLock && oldDigest != nil {
		return recoveryOperation{}, recoveryError("journal old digest has no old lock", nil)
	}

	op := recoveryOperation{id: operation, skill: skill, phase: phase, oldDigest: oldDigest, newDigest: newDigest, hadDestination: journal.HadDestination, newLock: recoveryLock{hash: journal.NewLockHash}}
	if journal.HadLock {
		op.oldLock = &recoveryLock{hash: journal.OldLockHash}
	}
	if op.oldLock != nil {
		op.oldLock.snapshot, err = readLockSnapshot(filepath.Join(operationDir, oldLockName), op.oldLock.hash)
		if err != nil {
			return recoveryOperation{}, recoveryError("old lock snapshot is invalid", err)
		}
	} else if snapshot, snapshotErr := readLockSnapshot(filepath.Join(operationDir, oldLockName), journal.OldLockHash); snapshotErr != nil || snapshot != nil {
		return recoveryOperation{}, recoveryError("unexpected old lock snapshot", snapshotErr)
	}
	op.newLock.snapshot, err = readLockSnapshot(filepath.Join(operationDir, newLockName), op.newLock.hash)
	if err != nil {
		return recoveryOperation{}, recoveryError("new lock snapshot is invalid", err)
	}
	if phase < recoveryPhaseLockCommitted && (op.newLock.snapshot == nil || (op.oldLock != nil && op.oldLock.snapshot == nil)) {
		return recoveryOperation{}, recoveryError("lock snapshots were removed before commit", nil)
	}
	if err := op.validateSnapshots(); err != nil {
		return recoveryOperation{}, recoveryError("lock snapshots disagree with the journal", err)
	}
	return op, nil
}

func (o recoveryOperation) validateSnapshots() error {
	if err := o.validateLockSelection(o.newLock.snapshot, o.newDigest, true); err != nil {
		return fmt.Errorf("new lock: %w", err)
	}
	if o.oldLock == nil {
		if o.newLock.snapshot != nil && len(o.newLock.snapshot.lock.Skills()) != 1 {
			return fmt.Errorf("new lock adds skills to a lockless project")
		}
		return nil
	}
	if o.oldDigest == nil {
		if err := o.validateLockSelection(o.oldLock.snapshot, agentskill.TreeDigest{}, false); err != nil {
			return fmt.Errorf("old lock: %w", err)
		}
	} else if err := o.validateLockSelection(o.oldLock.snapshot, *o.oldDigest, true); err != nil {
		return fmt.Errorf("old lock: %w", err)
	}
	if o.oldLock.snapshot != nil && o.newLock.snapshot != nil && !locksMatchExcept(o.oldLock.snapshot.lock, o.newLock.snapshot.lock, o.skill) {
		return fmt.Errorf("lock entries besides the updated skill changed")
	}
	return nil
}

func locksMatchExcept(old, new Lock, changed registry.SkillID) bool {
	oldSkills := old.Skills()
	newSkills := new.Skills()
	oldIndex, newIndex := 0, 0
	for oldIndex < len(oldSkills) || newIndex < len(newSkills) {
		for oldIndex < len(oldSkills) && oldSkills[oldIndex].Publication().Skill() == changed {
			oldIndex++
		}
		for newIndex < len(newSkills) && newSkills[newIndex].Publication().Skill() == changed {
			newIndex++
		}
		if oldIndex == len(oldSkills) || newIndex == len(newSkills) {
			return oldIndex == len(oldSkills) && newIndex == len(newSkills)
		}
		if oldSkills[oldIndex].Publication() != newSkills[newIndex].Publication() {
			return false
		}
		oldIndex++
		newIndex++
	}
	return true
}

func (o recoveryOperation) validateLockSelection(snapshot *lockSnapshot, digest agentskill.TreeDigest, present bool) error {
	if snapshot == nil {
		return nil
	}
	locked, found := snapshot.lock.Lookup(o.skill)
	if found != present {
		return fmt.Errorf("skill selection presence does not match the journal")
	}
	if found && locked.Publication().Tree() != digest {
		return fmt.Errorf("skill selection digest does not match the journal")
	}
	return nil
}

func (o recoveryOperation) validateOldLock(snapshot *lockSnapshot) error {
	if o.oldLock == nil {
		return fmt.Errorf("old lock is absent")
	}
	if o.oldDigest == nil {
		return o.validateLockSelection(snapshot, agentskill.TreeDigest{}, false)
	}
	return o.validateLockSelection(snapshot, *o.oldDigest, true)
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
