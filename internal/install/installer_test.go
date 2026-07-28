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

	"github.com/gofrs/flock"

	"github.com/joshuadavidthomas/ts-skills/internal/agentskill"
	"github.com/joshuadavidthomas/ts-skills/internal/registry"
)

type fetchedMap struct {
	fstest.MapFS
	closed   bool
	closeErr error
}

func (f *fetchedMap) Close() error {
	if f.closed {
		return errors.New("closed twice")
	}
	if f.closeErr != nil {
		return f.closeErr
	}
	f.closed = true
	return nil
}

type scriptedRemote struct {
	publication registry.PublicationID
	files       fstest.MapFS
	closeErr    error
	last        *fetchedMap
}

func (r *scriptedRemote) Fetch(context.Context, Requirement) (FetchedSkill, error) {
	r.last = &fetchedMap{MapFS: r.files, closeErr: r.closeErr}
	return NewFetchedSkill(r.publication, r.last)
}

func TestInstallerVerifiesBeforeReplacingDestination(t *testing.T) {
	name, _ := agentskill.ParseName("sample")
	namespace, _ := registry.ParseNamespace("team")
	skill, _ := registry.NewSkillID(namespace, name)
	files := fstest.MapFS{
		"SKILL.md":        &fstest.MapFile{Data: []byte("---\nname: sample\ndescription: Sample\n---\n# Instructions\n")},
		"assets/data.txt": &fstest.MapFile{Data: []byte("asset")},
		"scripts/run.sh":  &fstest.MapFile{Data: []byte("echo inert\n"), Mode: 0o777},
	}
	digest, err := agentskill.SumTree(context.Background(), files, ".")
	if err != nil {
		t.Fatal(err)
	}
	publication, _ := registry.NewPublicationID(skill, digest)
	remote := &scriptedRemote{publication: publication, files: files}
	installer, _ := NewInstaller(remote)
	project, err := OpenProject(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	requirement, _ := Current(skill)
	locked, err := installer.Install(context.Background(), project, requirement)
	if err != nil {
		t.Fatal(err)
	}
	if locked.Publication() != publication || !remote.last.closed {
		t.Fatal("install did not return publication or close fetched tree")
	}
	installed, err := os.ReadFile(project.SkillsDir() + "/sample/assets/data.txt")
	if err != nil || string(installed) != "asset" {
		t.Fatalf("asset = %q, %v", installed, err)
	}
	info, _ := os.Stat(project.SkillsDir() + "/sample/scripts/run.sh")
	if info.Mode().Perm()&0o111 != 0 {
		t.Fatalf("script remained executable: %v", info.Mode())
	}

	remote.files = fstest.MapFS{"SKILL.md": files["SKILL.md"], "assets/data.txt": &fstest.MapFile{Data: []byte("altered")}}
	if _, err := installer.Install(context.Background(), project, requirement); !errors.Is(err, ErrDigestMismatch) {
		t.Fatalf("altered install error = %v", err)
	}
	installed, _ = os.ReadFile(project.SkillsDir() + "/sample/assets/data.txt")
	if string(installed) != "asset" {
		t.Fatalf("valid destination changed to %q", installed)
	}
	if _, err := fs.Stat(remote.last, "SKILL.md"); err != nil {
		t.Fatal(err)
	}
}

func TestInstallFetchesBeforeRejectingUnmanagedDestination(t *testing.T) {
	skill, publication, files := testPublication(t, "team", "sample", "managed replacement")
	remote := &scriptedRemote{publication: publication, files: files}
	installer, err := NewInstaller(remote)
	if err != nil {
		t.Fatal(err)
	}
	project, err := OpenProject(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	destination := filepath.Join(project.SkillsDir(), "sample")
	if err := os.MkdirAll(destination, 0o755); err != nil {
		t.Fatal(err)
	}
	unmanagedPath := filepath.Join(destination, "SKILL.md")
	unmanagedBytes := []byte("unmanaged bytes must survive\n")
	if err := os.WriteFile(unmanagedPath, unmanagedBytes, 0o644); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(unmanagedPath)
	if err != nil {
		t.Fatal(err)
	}
	requirement, err := Current(skill)
	if err != nil {
		t.Fatal(err)
	}

	if _, err := installer.Install(context.Background(), project, requirement); !errors.Is(err, ErrUnmanagedDestination) {
		t.Fatalf("Install error = %v, want ErrUnmanagedDestination", err)
	}
	after, err := os.ReadFile(unmanagedPath)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(after, before) {
		t.Fatalf("unmanaged destination changed from %q to %q", before, after)
	}
	if remote.last == nil || !remote.last.closed {
		t.Fatal("Install did not close the fetched replacement")
	}
	if _, err := os.Stat(project.LockPath()); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("Install created a lock for the rejected destination: %v", err)
	}
}

func TestRestoreMissingLockedSkillPreservesUnmanagedContents(t *testing.T) {
	skill, publication, files := testPublication(t, "team", "sample", "locked skill")
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
	if err := os.RemoveAll(filepath.Join(project.SkillsDir(), "sample")); err != nil {
		t.Fatal(err)
	}

	unrelatedPath := filepath.Join(project.SkillsDir(), "notes.txt")
	unrelatedBytes := []byte("project notes\n")
	if err := os.WriteFile(unrelatedPath, unrelatedBytes, 0o644); err != nil {
		t.Fatal(err)
	}
	unlockedPath := filepath.Join(project.SkillsDir(), "other", "assets", "preserve.bin")
	if err := os.MkdirAll(filepath.Dir(unlockedPath), 0o755); err != nil {
		t.Fatal(err)
	}
	unlockedBytes := []byte{0x00, 0x01, 0xfe, 0xff}
	if err := os.WriteFile(unlockedPath, unlockedBytes, 0o644); err != nil {
		t.Fatal(err)
	}
	unlockedDocumentPath := filepath.Join(project.SkillsDir(), "other", "SKILL.md")
	unlockedDocumentBytes := []byte("---\nname: other\ndescription: Unlocked\n---\nDo not alter.\n")
	if err := os.WriteFile(unlockedDocumentPath, unlockedDocumentBytes, 0o644); err != nil {
		t.Fatal(err)
	}
	lockBefore, err := os.ReadFile(project.LockPath())
	if err != nil {
		t.Fatal(err)
	}

	if err := installer.Restore(context.Background(), project); err != nil {
		t.Fatal(err)
	}
	if _, err := os.ReadFile(filepath.Join(project.SkillsDir(), "sample", "SKILL.md")); err != nil {
		t.Fatalf("read restored locked skill: %v", err)
	}
	unrelatedAfter, err := os.ReadFile(unrelatedPath)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(unrelatedAfter, unrelatedBytes) {
		t.Fatalf("unrelated file changed from %q to %q", unrelatedBytes, unrelatedAfter)
	}
	unlockedAfter, err := os.ReadFile(unlockedPath)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(unlockedAfter, unlockedBytes) {
		t.Fatalf("unlocked skill asset changed from %v to %v", unlockedBytes, unlockedAfter)
	}
	unlockedDocumentAfter, err := os.ReadFile(unlockedDocumentPath)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(unlockedDocumentAfter, unlockedDocumentBytes) {
		t.Fatalf("unlocked skill document changed from %q to %q", unlockedDocumentBytes, unlockedDocumentAfter)
	}
	lockAfter, err := os.ReadFile(project.LockPath())
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(lockAfter, lockBefore) {
		t.Fatal("Restore rewrote the unchanged project lock")
	}
}

func TestInstallerPropagatesFetchedTreeCloseFailure(t *testing.T) {
	skill, publication, files := testPublication(t, "team", "sample", "body")
	injected := errors.New("injected fetched tree close failure")
	remote := &scriptedRemote{publication: publication, files: files, closeErr: injected}
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
	if _, err := installer.Install(context.Background(), project, requirement); !errors.Is(err, injected) {
		t.Fatalf("Install error = %v, want fetched close failure", err)
	}
	if remote.last.closed {
		t.Fatal("failed fetched tree close reported the tree as closed")
	}
	if _, err := os.Stat(project.destination("sample")); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("failed fetched close installed a destination: %v", err)
	}
	remote.last.closeErr = nil
	if err := remote.last.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestInstallerJoinsFetchedTreeCloseFailureIntoIdentityMismatch(t *testing.T) {
	_, publication, files := testPublication(t, "team", "sample", "body")
	injected := errors.New("injected fetched tree close failure")
	remote := &scriptedRemote{publication: publication, files: files, closeErr: injected}
	installer, err := NewInstaller(remote)
	if err != nil {
		t.Fatal(err)
	}
	project, err := OpenProject(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	namespace, err := registry.ParseNamespace("team")
	if err != nil {
		t.Fatal(err)
	}
	otherName, err := agentskill.ParseName("other")
	if err != nil {
		t.Fatal(err)
	}
	other, err := registry.NewSkillID(namespace, otherName)
	if err != nil {
		t.Fatal(err)
	}
	requirement, err := Current(other)
	if err != nil {
		t.Fatal(err)
	}
	_, err = installer.Install(context.Background(), project, requirement)
	if !errors.Is(err, ErrIdentityMismatch) {
		t.Fatalf("Install error = %v, want identity mismatch", err)
	}
	if !errors.Is(err, injected) {
		t.Fatalf("Install error = %v, want fetched close failure joined", err)
	}
	remote.last.closeErr = nil
	if err := remote.last.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestLockRejectsUnqualifiedNameCollision(t *testing.T) {
	name, _ := agentskill.ParseName("same")
	a, _ := registry.ParseNamespace("a")
	b, _ := registry.ParseNamespace("b")
	sa, _ := registry.NewSkillID(a, name)
	sb, _ := registry.NewSkillID(b, name)
	pa, _ := registry.NewPublicationID(sa, agentskill.TreeDigest{})
	pb, _ := registry.NewPublicationID(sb, agentskill.TreeDigest{})
	la, _ := NewLockedSkill(pa)
	lb, _ := NewLockedSkill(pb)
	if _, err := NewLock([]LockedSkill{la, lb}); err == nil {
		t.Fatal("lock accepted destination collision")
	}
}

func TestVerifiedTreeCloseRetainsOwnershipAfterRemovalFailure(t *testing.T) {
	project, err := OpenProject(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	writer, err := project.acquireWriter(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		writer.removeStaging = os.RemoveAll
		_ = writer.close()
	})
	staging, err := os.MkdirTemp(project.StateDir(), "staging-")
	if err != nil {
		t.Fatal(err)
	}
	writer.staging[staging] = struct{}{}
	verified := &verifiedTree{path: staging, owned: true, writer: writer}
	injected := errors.New("injected staging removal failure")
	writer.removeStaging = func(string) error { return injected }
	if err := verified.close(); !errors.Is(err, injected) {
		t.Fatalf("first close error = %v, want injected failure", err)
	}
	if !verified.owned {
		t.Fatal("failed close released verified tree ownership")
	}
	if _, found := writer.staging[staging]; !found {
		t.Fatal("failed close removed verified tree from writer tracking")
	}
	writer.removeStaging = os.RemoveAll
	if err := verified.close(); err != nil {
		t.Fatal(err)
	}
	if verified.owned {
		t.Fatal("successful close retained verified tree ownership")
	}
	if _, found := writer.staging[staging]; found {
		t.Fatal("successful close retained writer staging entry")
	}
}

func TestProjectWriterCloseHandsFailedStagingToNextWriter(t *testing.T) {
	project, err := OpenProject(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	writer, err := project.acquireWriter(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	failedStaging, err := os.MkdirTemp(project.StateDir(), "staging-failed-")
	if err != nil {
		t.Fatal(err)
	}
	cleanedStaging, err := os.MkdirTemp(project.StateDir(), "staging-cleaned-")
	if err != nil {
		t.Fatal(err)
	}
	writer.staging[failedStaging] = struct{}{}
	writer.staging[cleanedStaging] = struct{}{}
	injected := errors.New("injected staging removal failure")
	lockCloseCalls := 0
	writer.removeStaging = func(path string) error {
		if path == failedStaging {
			return injected
		}
		return os.RemoveAll(path)
	}
	writer.closeLock = func(lock *flock.Flock) error {
		lockCloseCalls++
		return lock.Close()
	}
	if err := writer.close(); !errors.Is(err, injected) {
		t.Fatalf("close error = %v, want injected failure", err)
	}
	if !writer.closed || writer.lock != nil || lockCloseCalls != 1 {
		t.Fatal("writer did not release its lock after staging cleanup failed")
	}
	if writer.staging != nil {
		t.Fatal("closed writer retained staging ownership")
	}
	if _, err := os.Stat(cleanedStaging); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("successfully cleaned staging remains: %v", err)
	}
	if _, err := os.Stat(failedStaging); err != nil {
		t.Fatalf("failed staging was not left for handoff: %v", err)
	}

	reopened, err := project.acquireWriter(context.Background())
	if errors.Is(err, ErrBusy) {
		t.Fatalf("orphan handoff retained the writer lock: %v", err)
	}
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(failedStaging); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("next writer did not remove orphan staging: %v", err)
	}
	if err := reopened.close(); err != nil {
		t.Fatal(err)
	}
}

func TestInstallCloseCleanupFailureIsRetriedByNextWriter(t *testing.T) {
	skill, publication, files := testPublication(t, "team", "sample", "body")
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

	injected := errors.New("injected close staging cleanup failure")
	projectWriterRemoveStaging = func(string) error { return injected }
	t.Cleanup(func() { projectWriterRemoveStaging = os.RemoveAll })
	if _, err := installer.Install(context.Background(), project, requirement); !errors.Is(err, injected) {
		t.Fatalf("Install error = %v, want staging cleanup failure", err)
	}
	projectWriterRemoveStaging = os.RemoveAll

	var orphan string
	entries, err := os.ReadDir(project.StateDir())
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), installStagingPrefix) {
			if orphan != "" {
				t.Fatal("Install left more than one orphan staging directory")
			}
			orphan = filepath.Join(project.StateDir(), entry.Name())
		}
	}
	if orphan == "" {
		t.Fatal("Install did not leave failed staging for the next writer")
	}

	writer, err := project.acquireWriter(context.Background())
	if errors.Is(err, ErrBusy) {
		t.Fatalf("next writer saw ErrBusy after close failure: %v", err)
	}
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(orphan); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("next writer did not durably remove orphan staging: %v", err)
	}
	if err := writer.close(); err != nil {
		t.Fatal(err)
	}
}

func TestAcquireWriterReportsRecoveryBeforeLaterRecoveryFailure(t *testing.T) {
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
	completed := filepath.Join(project.operationsDir(), "a-completed")
	if err := os.Mkdir(completed, 0o700); err != nil {
		t.Fatal(err)
	}
	malformed := filepath.Join(project.operationsDir(), "b-malformed")
	if err := os.Mkdir(malformed, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(malformed, journalName), []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}

	_, err = project.acquireWriter(context.Background())
	if !errors.Is(err, ErrRecovered) {
		t.Fatalf("writer error = %v, want ErrRecovered", err)
	}
	if !errors.Is(err, ErrRecoveryRequired) {
		t.Fatalf("writer error = %v, want ErrRecoveryRequired", err)
	}
	if _, err := os.Stat(completed); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("completed recovery operation remains: %v", err)
	}
}

func TestAcquireWriterRetriesOrphanCleanupAndRejectsMalformedMatches(t *testing.T) {
	t.Run("cleanup failure releases lock for retry", func(t *testing.T) {
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
		orphan, err := os.MkdirTemp(project.StateDir(), installStagingPrefix)
		if err != nil {
			t.Fatal(err)
		}
		injected := errors.New("injected orphan cleanup failure")
		transactionFailure = func(point string) error {
			if point == "before-remove-orphan-install-staging" {
				return injected
			}
			return nil
		}
		t.Cleanup(func() { transactionFailure = nil })
		if _, err := project.acquireWriter(context.Background()); !errors.Is(err, injected) {
			t.Fatalf("first retry error = %v, want injected cleanup failure", err)
		}
		transactionFailure = nil

		writer, err = project.acquireWriter(context.Background())
		if errors.Is(err, ErrBusy) {
			t.Fatalf("cleanup retry retained writer lock: %v", err)
		}
		if err != nil {
			t.Fatal(err)
		}
		if _, err := os.Stat(orphan); !errors.Is(err, fs.ErrNotExist) {
			t.Fatalf("retry did not remove orphan: %v", err)
		}
		if err := writer.close(); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("matching file is preserved", func(t *testing.T) {
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
		malformed := filepath.Join(project.StateDir(), installStagingPrefix+"malformed")
		contents := []byte("must not be removed")
		if err := os.WriteFile(malformed, contents, 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := project.acquireWriter(context.Background()); !errors.Is(err, ErrRecoveryRequired) {
			t.Fatalf("malformed orphan error = %v, want ErrRecoveryRequired", err)
		}
		actual, err := os.ReadFile(malformed)
		if err != nil || !bytes.Equal(actual, contents) {
			t.Fatalf("malformed orphan changed to %q: %v", actual, err)
		}
		if err := os.Remove(malformed); err != nil {
			t.Fatal(err)
		}
		writer, err = project.acquireWriter(context.Background())
		if errors.Is(err, ErrBusy) {
			t.Fatalf("malformed orphan failure retained writer lock: %v", err)
		}
		if err != nil {
			t.Fatal(err)
		}
		if err := writer.close(); err != nil {
			t.Fatal(err)
		}
	})
}

func TestProjectWriterCloseRetriesLockFailure(t *testing.T) {
	project, err := OpenProject(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	writer, err := project.acquireWriter(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		writer.closeLock = (*flock.Flock).Close
		_ = writer.close()
	})
	injected := errors.New("injected lock close failure")
	writer.closeLock = func(*flock.Flock) error { return injected }
	if err := writer.close(); !errors.Is(err, injected) {
		t.Fatalf("first close error = %v, want injected failure", err)
	}
	if writer.closed || writer.lock == nil {
		t.Fatal("failed close released writer lock ownership")
	}
	writer.closeLock = (*flock.Flock).Close
	if err := writer.close(); err != nil {
		t.Fatal(err)
	}
	if !writer.closed || writer.lock != nil {
		t.Fatal("retried close retained writer lock ownership")
	}
}
