package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func testInstaller(t *testing.T, body string) (*Installer, Project, Requirement, func(string)) {
	t.Helper()
	digest, archive := clientTree(t, body)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/"+apiVersion+"/skills/team/sample/current" {
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(currentResponse{Namespace: "team", Name: "sample", Digest: digest.String()})
			return
		}
		w.Header().Set("Content-Type", "application/zip")
		setClientTreeHeaders(w.Header(), "team", "sample", digest.String())
		_, _ = w.Write(archive)
	}))
	t.Cleanup(server.Close)
	installer, err := NewInstaller(remoteForServer(t, server))
	if err != nil {
		t.Fatal(err)
	}
	project, err := OpenProject(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	requirement, err := Current(clientSkill(t))
	if err != nil {
		t.Fatal(err)
	}
	return installer, project, requirement, func(next string) { digest, archive = clientTree(t, next) }
}

func TestInstallerRejectsZeroProjectBeforeFilesystemOrNetworkAccess(t *testing.T) {
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		requests++
	}))
	t.Cleanup(server.Close)
	installer, err := NewInstaller(remoteForServer(t, server))
	if err != nil {
		t.Fatal(err)
	}
	requirement, err := Current(clientSkill(t))
	if err != nil {
		t.Fatal(err)
	}
	workingDirectory := t.TempDir()
	originalDirectory, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(workingDirectory); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(originalDirectory) })

	if _, err := installer.Install(context.Background(), Project{}, requirement); err == nil {
		t.Fatal("Install accepted a zero Project")
	}
	if err := installer.Restore(context.Background(), Project{}); err == nil {
		t.Fatal("Restore accepted a zero Project")
	}
	if requests != 0 {
		t.Fatalf("registry requests = %d, want 0", requests)
	}
	if _, err := os.Stat(filepath.Join(workingDirectory, ".agents")); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("zero project created managed paths: %v", err)
	}
}

func TestInstallIsIdempotentAndKeepsCanonicalLock(t *testing.T) {
	installer, project, requirement, _ := testInstaller(t, "one")
	if _, err := installer.Install(context.Background(), project, requirement); err != nil {
		t.Fatal(err)
	}
	first, err := os.ReadFile(project.LockPath())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := installer.Install(context.Background(), project, requirement); err != nil {
		t.Fatal(err)
	}
	second, err := os.ReadFile(project.LockPath())
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(first, second) {
		t.Fatal("idempotent install changed lock bytes")
	}
}

func TestInstallUpgradeReplacesManagedDestination(t *testing.T) {
	installer, project, requirement, update := testInstaller(t, "old")
	if _, err := installer.Install(context.Background(), project, requirement); err != nil {
		t.Fatal(err)
	}
	update("new")
	if _, err := installer.Install(context.Background(), project, requirement); err != nil {
		t.Fatal(err)
	}
	contents, err := os.ReadFile(filepath.Join(project.SkillsDir(), "sample", "SKILL.md"))
	if err != nil || !bytes.Contains(contents, []byte("new")) {
		t.Fatalf("installed contents = %q, %v", contents, err)
	}
}

func TestInstallDoesNotReplaceUnmanagedDestination(t *testing.T) {
	installer, project, requirement, _ := testInstaller(t, "registry")
	destination := filepath.Join(project.SkillsDir(), "sample")
	if err := os.MkdirAll(destination, 0o755); err != nil {
		t.Fatal(err)
	}
	local := filepath.Join(destination, "SKILL.md")
	if err := os.WriteFile(local, []byte("local"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := installer.Install(context.Background(), project, requirement); !errors.Is(err, ErrLocalChanges) {
		t.Fatalf("Install() error = %v, want ErrLocalChanges", err)
	}
	contents, err := os.ReadFile(local)
	if err != nil || string(contents) != "local" {
		t.Fatalf("unmanaged skill contents = %q, %v", contents, err)
	}
	if _, err := os.Stat(project.LockPath()); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("lock exists after rejected install: %v", err)
	}
}

func TestInstallDoesNotReplaceModifiedDestination(t *testing.T) {
	installer, project, requirement, update := testInstaller(t, "old")
	if _, err := installer.Install(context.Background(), project, requirement); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(project.LockPath())
	if err != nil {
		t.Fatal(err)
	}
	local := filepath.Join(project.SkillsDir(), "sample", "local.txt")
	if err := os.WriteFile(local, []byte("edit"), 0o644); err != nil {
		t.Fatal(err)
	}
	update("new")
	if _, err := installer.Install(context.Background(), project, requirement); !errors.Is(err, ErrLocalChanges) {
		t.Fatalf("Install() error = %v, want ErrLocalChanges", err)
	}
	if _, err := os.Stat(local); err != nil {
		t.Fatalf("local edit removed: %v", err)
	}
	after, err := os.ReadFile(project.LockPath())
	if err != nil || !bytes.Equal(before, after) {
		t.Fatalf("lock changed after rejected install: %q, %v", after, err)
	}
}

func TestRestoreReplacesChangedLockedDestinationAndPreservesOtherPaths(t *testing.T) {
	installer, project, requirement, _ := testInstaller(t, "locked")
	if _, err := installer.Install(context.Background(), project, requirement); err != nil {
		t.Fatal(err)
	}
	lock, _ := os.ReadFile(project.LockPath())
	if err := os.WriteFile(filepath.Join(project.SkillsDir(), "sample", "local.txt"), []byte("edit"), 0o644); err != nil {
		t.Fatal(err)
	}
	other := filepath.Join(project.SkillsDir(), "other", "keep.txt")
	if err := os.MkdirAll(filepath.Dir(other), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(other, []byte("keep"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := installer.Restore(context.Background(), project); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(project.SkillsDir(), "sample", "local.txt")); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("local edit remains: %v", err)
	}
	if got, _ := os.ReadFile(other); string(got) != "keep" {
		t.Fatalf("other path = %q", got)
	}
	after, _ := os.ReadFile(project.LockPath())
	if !bytes.Equal(lock, after) {
		t.Fatal("restore rewrote lock")
	}
}

func TestReadLockSnapshotRejectsSymlink(t *testing.T) {
	project, err := OpenProject(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(project.LockPath()), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(t.TempDir(), project.LockPath()); err != nil {
		t.Skipf("create lock symlink: %v", err)
	}
	if _, _, err := readLockSnapshot(project); err == nil || !strings.Contains(err.Error(), "symbolic link") {
		t.Fatalf("readLockSnapshot() error = %v, want symbolic-link rejection", err)
	}
}

func TestReadLockSnapshotRejectsSymlinkedParent(t *testing.T) {
	project, err := OpenProject(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(t.TempDir(), filepath.Join(project.root, ".agents")); err != nil {
		t.Skipf("create managed-directory symlink: %v", err)
	}
	if _, _, err := readLockSnapshot(project); err == nil || !strings.Contains(err.Error(), "symbolic link") {
		t.Fatalf("readLockSnapshot() error = %v, want symbolic-link rejection", err)
	}
}

func stagedUpgrade(t *testing.T) (Project, *projectWriter, *verifiedTree, Lock) {
	t.Helper()
	installer, project, requirement, update := testInstaller(t, "old")
	if _, err := installer.Install(context.Background(), project, requirement); err != nil {
		t.Fatal(err)
	}
	update("new")
	fetched, err := installer.remote.Fetch(context.Background(), requirement)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = closeFetchedTree(fetched) })
	writer, err := project.acquireWriter(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = writer.close() })
	oldLock, _, _, err := writer.readLock()
	if err != nil {
		t.Fatal(err)
	}
	verified, err := writer.stageAndVerify(context.Background(), requirement, fetched)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = verified.close() })
	newLock, err := oldLock.With(LockedSkill{Publication: verified.publication})
	if err != nil {
		t.Fatal(err)
	}
	return project, writer, verified, newLock
}

func TestReplaceRollsBackWhenDestinationSyncFails(t *testing.T) {
	project, writer, verified, newLock := stagedUpgrade(t)
	before, err := os.ReadFile(project.LockPath())
	if err != nil {
		t.Fatal(err)
	}
	failed := false
	writer.syncDirectory = func(path string) error {
		if path == project.SkillsDir() && !failed {
			failed = true
			return errors.New("sync skills directory")
		}
		return syncDirectory(path)
	}
	if err := writer.replace(context.Background(), verified, newLock, true); err == nil {
		t.Fatal("replace succeeded after destination sync failure")
	}
	contents, err := os.ReadFile(filepath.Join(project.SkillsDir(), "sample", "SKILL.md"))
	if err != nil || !bytes.Contains(contents, []byte("old")) {
		t.Fatalf("destination after rollback = %q, %v", contents, err)
	}
	after, err := os.ReadFile(project.LockPath())
	if err != nil || !bytes.Equal(before, after) {
		t.Fatalf("lock after rollback = %q, %v", after, err)
	}
}

func TestReplaceRollsBackWhenLockRenameFails(t *testing.T) {
	project, writer, verified, newLock := stagedUpgrade(t)
	before, err := os.ReadFile(project.LockPath())
	if err != nil {
		t.Fatal(err)
	}
	writer.rename = func(old, new string) error {
		if new == project.LockPath() {
			return errors.New("replace lock")
		}
		return os.Rename(old, new)
	}
	if err := writer.replace(context.Background(), verified, newLock, true); err == nil {
		t.Fatal("replace succeeded after lock rename failure")
	}
	contents, err := os.ReadFile(filepath.Join(project.SkillsDir(), "sample", "SKILL.md"))
	if err != nil || !bytes.Contains(contents, []byte("old")) {
		t.Fatalf("destination after rollback = %q, %v", contents, err)
	}
	after, err := os.ReadFile(project.LockPath())
	if err != nil || !bytes.Equal(before, after) {
		t.Fatalf("lock after rollback = %q, %v", after, err)
	}
}

func TestReplacePreservesOldTreeWhenRollbackCannotRestoreIt(t *testing.T) {
	project, writer, verified, newLock := stagedUpgrade(t)
	destination := filepath.Join(project.SkillsDir(), "sample")
	restoreErr := errors.New("restore old skill")
	writer.syncDirectory = func(path string) error {
		if path == project.SkillsDir() {
			return errors.New("sync skills directory")
		}
		return syncDirectory(path)
	}
	writer.rename = func(old, new string) error {
		if new == destination && old != verified.path {
			return restoreErr
		}
		return os.Rename(old, new)
	}
	if err := writer.replace(context.Background(), verified, newLock, true); !errors.Is(err, restoreErr) {
		t.Fatalf("replace error = %v, want rollback failure", err)
	}
	if _, err := os.Stat(destination); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("destination after failed rollback = %v, want absent", err)
	}

	entries, err := os.ReadDir(project.SkillsDir())
	if err != nil {
		t.Fatal(err)
	}
	var trash string
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), installTrashPrefix) {
			trash = filepath.Join(project.SkillsDir(), entry.Name())
			break
		}
	}
	if trash == "" {
		t.Fatal("old tree was not retained after rollback failure")
	}
	contents, err := os.ReadFile(filepath.Join(trash, "SKILL.md"))
	if err != nil || !bytes.Contains(contents, []byte("old")) {
		t.Fatalf("retained old tree = %q, %v", contents, err)
	}
	if err := writer.close(); err != nil {
		t.Fatal(err)
	}
	writer, err = project.acquireWriter(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if err := writer.close(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(trash); err != nil {
		t.Fatalf("recovery tree was swept: %v", err)
	}
}

func TestReplaceLeavesDestinationAndLockAlignedAfterLockRename(t *testing.T) {
	project, writer, verified, newLock := stagedUpgrade(t)
	writer.syncDirectory = func(path string) error {
		if path == filepath.Dir(project.LockPath()) {
			return errors.New("sync lock directory")
		}
		return syncDirectory(path)
	}
	if err := writer.replace(context.Background(), verified, newLock, true); err == nil {
		t.Fatal("replace succeeded after lock directory sync failure")
	}
	contents, err := os.ReadFile(filepath.Join(project.SkillsDir(), "sample", "SKILL.md"))
	if err != nil || !bytes.Contains(contents, []byte("new")) {
		t.Fatalf("destination after lock rename = %q, %v", contents, err)
	}
	wantLock, err := encodeLockBytes(newLock)
	if err != nil {
		t.Fatal(err)
	}
	gotLock, err := os.ReadFile(project.LockPath())
	if err != nil || !bytes.Equal(gotLock, wantLock) {
		t.Fatalf("lock after lock rename = %q, %v", gotLock, err)
	}
}

func TestRevalidationPreservesOperationalErrors(t *testing.T) {
	installer, project, requirement, _ := testInstaller(t, "installed")
	if _, err := installer.Install(context.Background(), project, requirement); err != nil {
		t.Fatal(err)
	}
	writer, err := project.acquireWriter(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = writer.close() }()
	_, lockBytes, hadLock, err := writer.readLock()
	if err != nil {
		t.Fatal(err)
	}
	before, err := writer.destinationState(context.Background(), requirement.Skill())
	if err != nil {
		t.Fatal(err)
	}
	plan, err := makeRestorePlan(context.Background(), writer)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := writer.assertUnchanged(ctx, requirement.Skill(), lockBytes, hadLock, before); !errors.Is(err, ErrProjectChanged) || !errors.Is(err, context.Canceled) {
		t.Fatalf("install revalidation error = %v, want project change and cancellation", err)
	}
	if err := plan.matches(ctx, writer); !errors.Is(err, ErrProjectChanged) || !errors.Is(err, context.Canceled) {
		t.Fatalf("restore revalidation error = %v, want project change and cancellation", err)
	}
}

func TestWriterSweepsStagingAndKeepsRecoveryTrash(t *testing.T) {
	project, _ := OpenProject(t.TempDir())
	writer, err := project.acquireWriter(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if err := writer.close(); err != nil {
		t.Fatal(err)
	}
	stage := filepath.Join(project.SkillsDir(), installStagingPrefix+"dead")
	trash := filepath.Join(project.SkillsDir(), installTrashPrefix+"dead")
	real := filepath.Join(project.SkillsDir(), "staging-real")
	for _, path := range []string{stage, trash, real} {
		if err := os.Mkdir(path, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	writer, err = project.acquireWriter(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = writer.close() })
	if _, err := os.Stat(stage); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("staging litter remains: %v", err)
	}
	if _, err := os.Stat(trash); err != nil {
		t.Fatalf("recovery trash was removed: %v", err)
	}
	if _, err := os.Stat(real); err != nil {
		t.Fatalf("real skill removed: %v", err)
	}
}
