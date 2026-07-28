package install

import (
	"bytes"
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/joshuadavidthomas/ts-skill-registry/internal/agentskill"
	"github.com/joshuadavidthomas/ts-skill-registry/internal/registry"
)

func testPublication(t *testing.T, namespaceText, nameText, body string) (registry.SkillID, registry.PublicationID, fstest.MapFS) {
	t.Helper()
	name, err := agentskill.ParseName(nameText)
	if err != nil {
		t.Fatal(err)
	}
	namespace, err := registry.ParseNamespace(namespaceText)
	if err != nil {
		t.Fatal(err)
	}
	skill, err := registry.NewSkillID(namespace, name)
	if err != nil {
		t.Fatal(err)
	}
	files := fstest.MapFS{"SKILL.md": {Data: []byte("---\nname: " + nameText + "\ndescription: test\n---\n" + body)}}
	digest, err := agentskill.SumTree(files, ".")
	if err != nil {
		t.Fatal(err)
	}
	publication, err := registry.NewPublicationID(skill, digest)
	if err != nil {
		t.Fatal(err)
	}
	return skill, publication, files
}

func TestDecodeLockSortsCanonicalSkillIdentities(t *testing.T) {
	_, firstPublication, _ := testPublication(t, "z", "a", "one")
	_, secondPublication, _ := testPublication(t, "zz", "b", "two")
	encoded := "schema = 1\n" +
		"\n[[skills]]\nskill = \"ｚ/a\"\ndigest = \"" + firstPublication.Tree().String() + "\"\n" +
		"\n[[skills]]\nskill = \"zz/b\"\ndigest = \"" + secondPublication.Tree().String() + "\"\n"
	lock, err := DecodeLock(strings.NewReader(encoded))
	if err != nil {
		t.Fatal(err)
	}
	if _, found := lock.Lookup(firstPublication.Skill()); !found {
		t.Fatal("decoded lock did not canonicalize the first skill identity")
	}
}

func TestLockCodecIsCanonicalAndStrict(t *testing.T) {
	_, publication, _ := testPublication(t, "team", "sample", "one")
	locked, err := NewLockedSkill(publication)
	if err != nil {
		t.Fatal(err)
	}
	lock, err := NewLock([]LockedSkill{locked})
	if err != nil {
		t.Fatal(err)
	}
	var encoded bytes.Buffer
	if err := EncodeLock(&encoded, lock); err != nil {
		t.Fatal(err)
	}
	decoded, err := DecodeLock(strings.NewReader(encoded.String()))
	if err != nil {
		t.Fatal(err)
	}
	if got, ok := decoded.Lookup(publication.Skill()); !ok || got.Publication() != publication {
		t.Fatal("round trip changed lock")
	}
	for _, malformed := range []string{"", "schema = 2\n", "schema = 1\nunknown = true\n", "schema = 1\n[[skills]]\nskill = \"team/sample\"\ndigest = \"SHA256:bad\"\n"} {
		if _, err := DecodeLock(strings.NewReader(malformed)); err == nil {
			t.Fatalf("accepted malformed lock %q", malformed)
		}
	}
}

func TestDecodeLockRejectsDuplicateSkillEntries(t *testing.T) {
	_, first, _ := testPublication(t, "team", "sample", "one")
	_, second, _ := testPublication(t, "team", "sample", "two")
	entry := func(publication registry.PublicationID) string {
		return "\n[[skills]]\nskill = \"team/sample\"\ndigest = \"" + publication.Tree().String() + "\"\n"
	}

	for _, test := range []struct {
		name    string
		encoded string
	}{
		{name: "identical full entries", encoded: "schema = 1\n" + entry(first) + entry(first)},
		{name: "same identity with another digest", encoded: "schema = 1\n" + entry(first) + entry(second)},
	} {
		t.Run(test.name, func(t *testing.T) {
			if _, err := DecodeLock(strings.NewReader(test.encoded)); err == nil {
				t.Fatal("DecodeLock accepted duplicate full SkillID entries")
			}
		})
	}
}

func TestRestoreRepairsMissingTreeAndRefusesLocalEdits(t *testing.T) {
	skill, publication, files := testPublication(t, "team", "sample", "original")
	remote := &scriptedRemote{publication: publication, files: files}
	installer, _ := NewInstaller(remote)
	project, _ := OpenProject(t.TempDir())
	requirement, _ := Current(skill)
	if _, err := installer.Install(context.Background(), project, requirement); err != nil {
		t.Fatal(err)
	}
	destination := project.destination("sample")
	if err := os.RemoveAll(destination); err != nil {
		t.Fatal(err)
	}
	if err := installer.Restore(context.Background(), project); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(destination); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(destination+"/local.txt", []byte("edit"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := installer.Restore(context.Background(), project); !errors.Is(err, ErrLocalChanges) {
		t.Fatalf("restore error = %v", err)
	}
}

func TestTransactionFailurePointsRecoverToLockDestinationAgreement(t *testing.T) {
	points := collectUpdateTransactionPoints(t)
	for _, phase := range []string{"prepared", "backup-created", "destination-swapped", "lock-committed"} {
		requirePoint(t, points, "before-journal-write-"+phase)
		requirePoint(t, points, "after-journal-write-"+phase)
	}
	for _, prefix := range []string{"before-rename-", "after-rename-", "before-fsync-", "after-fsync-", "before-write-", "after-write-", "before-remove-cleanup-", "after-remove-cleanup-"} {
		requirePointPrefix(t, points, prefix)
	}

	for _, point := range points {
		t.Run(point, func(t *testing.T) {
			project, installer, requirement, skill := prepareUpdate(t)
			injected := false
			transactionFailure = func(actual string) error {
				if actual == point && !injected {
					injected = true
					return errors.New("injected interruption")
				}
				return nil
			}
			_, _ = installer.Install(context.Background(), project, requirement)
			transactionFailure = nil
			if !injected {
				t.Fatalf("transaction point %q was not reached", point)
			}
			reacquireAndAssertAgreement(t, project, skill)
		})
	}
}

func TestInterruptedRecoveryActionsCanBeRetried(t *testing.T) {
	points := collectRollbackRecoveryPoints(t)
	for _, prefix := range []string{"before-rename-discard-uncommitted-destination", "before-remove-discard-uncommitted-destination", "before-rename-restore-old-destination", "before-remove-cleanup-"} {
		requirePointPrefix(t, points, prefix)
	}

	for _, point := range points {
		t.Run(point, func(t *testing.T) {
			project, skill := prepareInterruptedUpdate(t, "before-rename-project-lock")
			injected := false
			transactionFailure = func(actual string) error {
				if actual == point && !injected {
					injected = true
					return errors.New("interrupted recovery")
				}
				return nil
			}
			_, err := project.acquireWriter(context.Background())
			transactionFailure = nil
			if !injected {
				t.Fatalf("recovery point %q was not reached", point)
			}
			if err == nil {
				t.Fatal("interrupted recovery acquired a writer")
			}
			reacquireAndAssertAgreement(t, project, skill)
		})
	}
}

func TestInterruptedRollForwardRecoveryCanBeRetried(t *testing.T) {
	project, installer, _, skill := prepareInstalledSkill(t)
	if err := os.RemoveAll(project.destination(skill.Name().String())); err != nil {
		t.Fatal(err)
	}
	transactionFailure = failOnceAt("before-rename-swap-destination")
	if err := installer.Restore(context.Background(), project); err == nil {
		t.Fatal("restore interruption did not fire")
	}
	transactionFailure = nil

	transactionFailure = failOnceAt("after-rename-recover-new-destination")
	if _, err := project.acquireWriter(context.Background()); err == nil {
		t.Fatal("recovery interruption did not fire")
	}
	transactionFailure = nil

	stabilizedParent := false
	transactionFailure = func(point string) error {
		if point == "before-fsync-recovery-destination-parent" {
			stabilizedParent = true
		}
		return nil
	}
	reacquireAndAssertAgreement(t, project, skill)
	transactionFailure = nil
	if !stabilizedParent {
		t.Fatal("recovery did not sync SkillsDir after an interrupted destination rename")
	}
}

func TestPreparedMissingTreeRestoreCleansEqualLockOperationAndRefetches(t *testing.T) {
	project, installer, _, skill := prepareInstalledSkill(t)
	destination := project.destination(skill.Name().String())
	lockBefore, err := os.ReadFile(project.LockPath())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(destination); err != nil {
		t.Fatal(err)
	}

	transactionFailure = failOnceAt("after-journal-write-prepared")
	if err := installer.Restore(context.Background(), project); err == nil {
		t.Fatal("prepared restore interruption did not fire")
	}
	transactionFailure = nil

	operations, err := os.ReadDir(project.operationsDir())
	if err != nil || len(operations) != 1 {
		t.Fatalf("operations after interruption = %d, %v", len(operations), err)
	}
	operationDir := filepath.Join(project.operationsDir(), operations[0].Name())
	journal, _, _, _, err := readJournal(operationDir, operations[0].Name())
	if err != nil {
		t.Fatal(err)
	}
	if journal.Phase != "prepared" || journal.OldLockHash != journal.NewLockHash {
		t.Fatalf("prepared journal phase and lock hashes = %q, %q, %q", journal.Phase, journal.OldLockHash, journal.NewLockHash)
	}
	if _, err := os.Stat(filepath.Join(operationDir, stagingName)); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("prepared staging remains: %v", err)
	}
	lockAfterInterruption, err := os.ReadFile(project.LockPath())
	if err != nil || !bytes.Equal(lockAfterInterruption, lockBefore) {
		t.Fatalf("lock changed during interrupted restore: %v", err)
	}
	if _, err := os.Stat(destination); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("destination exists after interrupted restore: %v", err)
	}
	interruptedFetch := installer.remote.(*scriptedRemote).last

	if err := installer.Restore(context.Background(), project); err != nil {
		t.Fatal(err)
	}
	if installer.remote.(*scriptedRemote).last == interruptedFetch {
		t.Fatal("retry reused the interrupted fetch instead of refetching")
	}
	operations, err = os.ReadDir(project.operationsDir())
	if err != nil || len(operations) != 0 {
		t.Fatalf("operations after restored retry = %d, %v", len(operations), err)
	}
	reacquireAndAssertAgreement(t, project, skill)
}

func TestRecoveryConvergesAfterPartialDiscardCleanup(t *testing.T) {
	project, skill := prepareInterruptedUpdate(t, "before-rename-project-lock")
	transactionFailure = failOnceAt("before-remove-discard-uncommitted-destination")
	if _, err := project.acquireWriter(context.Background()); err == nil {
		t.Fatal("discard cleanup interruption did not fire")
	}
	transactionFailure = nil

	operations, err := os.ReadDir(project.operationsDir())
	if err != nil || len(operations) != 1 {
		t.Fatalf("operations = %d, %v", len(operations), err)
	}
	discard := filepath.Join(project.operationsDir(), operations[0].Name(), discardName)
	if err := os.Remove(filepath.Join(discard, "SKILL.md")); err != nil {
		t.Fatal(err)
	}
	reacquireAndAssertAgreement(t, project, skill)
}

func TestCommittedCleanupWithoutSnapshotsRemainsRecoverable(t *testing.T) {
	project, installer, requirement, skill := prepareUpdate(t)
	transactionFailure = failOnceAt("after-remove-cleanup-new-lock")
	if _, err := installer.Install(context.Background(), project, requirement); err != nil {
		t.Fatalf("committed install reported cleanup interruption: %v", err)
	}
	transactionFailure = nil

	entries, err := os.ReadDir(project.operationsDir())
	if err != nil || len(entries) != 1 {
		t.Fatalf("retained operations = %d, %v", len(entries), err)
	}
	operationDir := filepath.Join(project.operationsDir(), entries[0].Name())
	if _, err := os.Stat(filepath.Join(operationDir, journalName)); err != nil {
		t.Fatalf("journal was not retained: %v", err)
	}
	if _, err := os.Stat(filepath.Join(operationDir, newLockName)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("new lock snapshot still exists or cannot be inspected: %v", err)
	}
	reacquireAndAssertAgreement(t, project, skill)
}

func TestCommittedCleanupRejectsAnotherFullLock(t *testing.T) {
	project, installer, requirement, _ := prepareUpdate(t)
	transactionFailure = failOnceAt("after-remove-cleanup-new-lock")
	if _, err := installer.Install(context.Background(), project, requirement); err != nil {
		t.Fatalf("committed install reported cleanup interruption: %v", err)
	}
	transactionFailure = nil

	lockBytes, err := os.ReadFile(project.LockPath())
	if err != nil {
		t.Fatal(err)
	}
	lock, err := DecodeLock(bytes.NewReader(lockBytes))
	if err != nil {
		t.Fatal(err)
	}
	_, extraPublication, _ := testPublication(t, "team", "extra", "extra")
	extra, err := NewLockedSkill(extraPublication)
	if err != nil {
		t.Fatal(err)
	}
	lock, err = lock.With(extra)
	if err != nil {
		t.Fatal(err)
	}
	var changed bytes.Buffer
	if err := EncodeLock(&changed, lock); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(project.LockPath(), changed.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}

	if _, err := project.acquireWriter(context.Background()); !errors.Is(err, ErrRecoveryRequired) {
		t.Fatalf("recovery error = %v", err)
	}
}

func TestReplaceLockFromRetriesMatchingTemporaryAndRejectsMismatch(t *testing.T) {
	for _, test := range []struct {
		name      string
		temporary []byte
		wantErr   bool
	}{
		{name: "matching", temporary: []byte("schema = 1\n")},
		{name: "mismatched", temporary: []byte("different\n"), wantErr: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			project, err := OpenProject(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			writer, err := project.acquireWriter(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			if err := writer.close(); err != nil {
				t.Fatal(err)
			}
			operation := strings.Repeat("a", 32)
			source := filepath.Join(project.StateDir(), "source-lock")
			contents := []byte("schema = 1\n")
			if err := os.WriteFile(source, contents, 0o600); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(lockTemporaryPath(project, operation), test.temporary, 0o600); err != nil {
				t.Fatal(err)
			}
			err = replaceLockFrom(project, operation, source)
			if test.wantErr {
				if !errors.Is(err, ErrRecoveryRequired) {
					t.Fatalf("replace error = %v", err)
				}
				actual, readErr := os.ReadFile(lockTemporaryPath(project, operation))
				if readErr != nil || !bytes.Equal(actual, test.temporary) {
					t.Fatalf("mismatched evidence changed: %q, %v", actual, readErr)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			actual, err := os.ReadFile(project.LockPath())
			if err != nil || !bytes.Equal(actual, contents) {
				t.Fatalf("project lock = %q, %v", actual, err)
			}
		})
	}
}

func TestReplaceLockFromRetriesAfterFailureBeforeRename(t *testing.T) {
	t.Cleanup(func() { transactionFailure = nil })
	project, err := OpenProject(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	writer, err := project.acquireWriter(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if err := writer.close(); err != nil {
		t.Fatal(err)
	}
	operation := strings.Repeat("b", 32)
	source := filepath.Join(project.StateDir(), "source-lock")
	contents := []byte("schema = 1\n")
	if err := os.WriteFile(source, contents, 0o600); err != nil {
		t.Fatal(err)
	}
	transactionFailure = failOnceAt("before-rename-project-lock")
	if err := replaceLockFrom(project, operation, source); err == nil {
		t.Fatal("injected replacement succeeded")
	}
	transactionFailure = nil
	if err := replaceLockFrom(project, operation, source); err != nil {
		t.Fatal(err)
	}
	actual, err := os.ReadFile(project.LockPath())
	if err != nil || !bytes.Equal(actual, contents) {
		t.Fatalf("project lock = %q, %v", actual, err)
	}
}

func TestRollbackOldLockRestoreRetriesFailureBeforeRename(t *testing.T) {
	project, installer, requirement, skill := prepareUpdate(t)
	oldLock, err := os.ReadFile(project.LockPath())
	if err != nil {
		t.Fatal(err)
	}

	transactionFailure = failOnceAt("after-rename-project-lock")
	if _, err := installer.Install(context.Background(), project, requirement); err == nil {
		t.Fatal("new lock replacement interruption did not fire")
	}
	transactionFailure = nil
	if err := os.RemoveAll(project.destination(skill.Name().String())); err != nil {
		t.Fatal(err)
	}

	transactionFailure = failOnceAt("before-rename-project-lock")
	if writer, err := project.acquireWriter(context.Background()); err == nil {
		_ = writer.close()
		t.Fatal("old lock replacement interruption did not fire")
	}
	transactionFailure = nil

	operations, err := os.ReadDir(project.operationsDir())
	if err != nil || len(operations) != 1 {
		t.Fatalf("operations after old lock interruption = %d, %v", len(operations), err)
	}
	operation := operations[0].Name()
	operationDir := filepath.Join(project.operationsDir(), operation)
	temporary, err := os.ReadFile(lockTemporaryPath(project, operation))
	if err != nil {
		t.Fatal(err)
	}
	oldSnapshot, err := os.ReadFile(filepath.Join(operationDir, oldLockName))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(temporary, oldSnapshot) {
		t.Fatal("old lock retry temporary does not match the old snapshot")
	}

	writer, err := project.acquireWriter(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if err := writer.close(); err != nil {
		t.Fatal(err)
	}
	actualLock, err := os.ReadFile(project.LockPath())
	if err != nil || !bytes.Equal(actualLock, oldLock) {
		t.Fatalf("restored project lock differs from old lock: %v", err)
	}
	contents, err := os.ReadFile(filepath.Join(project.destination(skill.Name().String()), "SKILL.md"))
	if err != nil || !bytes.Contains(contents, []byte("old")) {
		t.Fatalf("restored destination = %q, %v", contents, err)
	}
}

func TestInterruptedSwapRecoversOldDestinationAndLock(t *testing.T) {
	skill, oldPublication, oldFiles := testPublication(t, "team", "sample", "old")
	remote := &scriptedRemote{publication: oldPublication, files: oldFiles}
	installer, _ := NewInstaller(remote)
	project, _ := OpenProject(t.TempDir())
	requirement, _ := Current(skill)
	if _, err := installer.Install(context.Background(), project, requirement); err != nil {
		t.Fatal(err)
	}
	_, newPublication, newFiles := testPublication(t, "team", "sample", "new")
	remote.publication, remote.files = newPublication, newFiles
	transactionFailure = func(point string) error {
		if point == "after-rename-swap-destination" {
			return errors.New("injected interruption")
		}
		return nil
	}
	if _, err := installer.Install(context.Background(), project, requirement); err == nil {
		t.Fatal("injected install succeeded")
	}
	transactionFailure = nil
	t.Cleanup(func() { transactionFailure = nil })
	writer, err := project.acquireWriter(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if err := writer.close(); err != nil {
		t.Fatal(err)
	}
	contents, err := os.ReadFile(project.destination("sample") + "/SKILL.md")
	if err != nil || !bytes.Contains(contents, []byte("old")) {
		t.Fatalf("recovered contents = %q, %v", contents, err)
	}
	lockBytes, err := os.ReadFile(project.LockPath())
	if err != nil {
		t.Fatal(err)
	}
	lock, err := DecodeLock(bytes.NewReader(lockBytes))
	if err != nil {
		t.Fatal(err)
	}
	if got, _ := lock.Lookup(skill); got.Publication() != oldPublication {
		t.Fatal("recovery changed lock")
	}
}

func prepareInstalledSkill(t *testing.T) (Project, *Installer, Requirement, registry.SkillID) {
	t.Helper()
	transactionFailure = nil
	t.Cleanup(func() { transactionFailure = nil })
	skill, publication, files := testPublication(t, "team", "sample", "old")
	remote := &scriptedRemote{publication: publication, files: files}
	installer, err := NewInstaller(remote)
	if err != nil {
		t.Fatal(err)
	}
	project, err := OpenProject(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	requirement, err := Current(skill)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := installer.Install(context.Background(), project, requirement); err != nil {
		t.Fatal(err)
	}
	return project, installer, requirement, skill
}

func prepareUpdate(t *testing.T) (Project, *Installer, Requirement, registry.SkillID) {
	t.Helper()
	project, installer, requirement, skill := prepareInstalledSkill(t)
	_, publication, files := testPublication(t, "team", "sample", "new")
	remote := installer.remote.(*scriptedRemote)
	remote.publication = publication
	remote.files = files
	return project, installer, requirement, skill
}

func prepareInterruptedUpdate(t *testing.T, point string) (Project, registry.SkillID) {
	t.Helper()
	project, installer, requirement, skill := prepareUpdate(t)
	transactionFailure = failOnceAt(point)
	if _, err := installer.Install(context.Background(), project, requirement); err == nil {
		t.Fatalf("transaction interruption %q did not fire", point)
	}
	transactionFailure = nil
	return project, skill
}

func collectUpdateTransactionPoints(t *testing.T) []string {
	t.Helper()
	project, installer, requirement, _ := prepareUpdate(t)
	return collectPoints(t, func() error {
		_, err := installer.Install(context.Background(), project, requirement)
		return err
	})
}

func collectRollbackRecoveryPoints(t *testing.T) []string {
	t.Helper()
	project, _ := prepareInterruptedUpdate(t, "before-rename-project-lock")
	return collectPoints(t, func() error {
		writer, err := project.acquireWriter(context.Background())
		if err != nil {
			return err
		}
		return writer.close()
	})
}

func collectPoints(t *testing.T, run func() error) []string {
	t.Helper()
	seen := make(map[string]struct{})
	points := make([]string, 0)
	transactionFailure = func(point string) error {
		if _, found := seen[point]; !found {
			seen[point] = struct{}{}
			points = append(points, point)
		}
		return nil
	}
	if err := run(); err != nil {
		transactionFailure = nil
		t.Fatal(err)
	}
	transactionFailure = nil
	if len(points) == 0 {
		t.Fatal("transaction did not report failure points")
	}
	return points
}

func requirePoint(t *testing.T, points []string, wanted string) {
	t.Helper()
	for _, point := range points {
		if point == wanted {
			return
		}
	}
	t.Fatalf("transaction point %q was not covered", wanted)
}

func requirePointPrefix(t *testing.T, points []string, prefix string) {
	t.Helper()
	for _, point := range points {
		if strings.HasPrefix(point, prefix) {
			return
		}
	}
	t.Fatalf("no transaction point has prefix %q", prefix)
}

func failOnceAt(wanted string) func(string) error {
	failed := false
	return func(point string) error {
		if point == wanted && !failed {
			failed = true
			return errors.New("injected interruption")
		}
		return nil
	}
}

func reacquireAndAssertAgreement(t *testing.T, project Project, skill registry.SkillID) {
	t.Helper()
	writer, err := project.acquireWriter(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if err := writer.close(); err != nil {
		t.Fatal(err)
	}
	lockBytes, err := os.ReadFile(project.LockPath())
	if err != nil {
		t.Fatal(err)
	}
	lock, err := DecodeLock(bytes.NewReader(lockBytes))
	if err != nil {
		t.Fatal(err)
	}
	locked, found := lock.Lookup(skill)
	if !found {
		t.Fatalf("lock no longer selects %s", skill.String())
	}
	destination := project.destination(skill.Name().String())
	digest, err := agentskill.SumTree(os.DirFS(destination), ".")
	if err != nil {
		t.Fatal(err)
	}
	if digest != locked.Publication().Tree() {
		t.Fatalf("lock selects %s but destination hashes to %s", locked.Publication().Tree().String(), digest.String())
	}
}

type blockingRemote struct {
	started     chan struct{}
	release     chan struct{}
	publication registry.PublicationID
	files       fstest.MapFS
}

func (r *blockingRemote) Fetch(ctx context.Context, _ Requirement) (FetchedSkill, error) {
	select {
	case <-r.started:
	default:
		close(r.started)
	}
	select {
	case <-r.release:
	case <-ctx.Done():
		return FetchedSkill{}, ctx.Err()
	}
	return NewFetchedSkill(r.publication, &fetchedMap{MapFS: r.files})
}

func TestProjectWriterExcludesConcurrentInstaller(t *testing.T) {
	skill, publication, files := testPublication(t, "team", "sample", "body")
	remote := &blockingRemote{started: make(chan struct{}), release: make(chan struct{}), publication: publication, files: files}
	installer, _ := NewInstaller(remote)
	project, _ := OpenProject(t.TempDir())
	requirement, _ := Current(skill)
	firstDone := make(chan error, 1)
	go func() { _, err := installer.Install(context.Background(), project, requirement); firstDone <- err }()
	<-remote.started
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	_, err := installer.Install(ctx, project, requirement)
	if !errors.Is(err, ErrBusy) || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("concurrent error = %v", err)
	}
	close(remote.release)
	if err := <-firstDone; err != nil {
		t.Fatal(err)
	}
}
