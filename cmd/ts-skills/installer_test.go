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
	"sync"
	"testing"
	"testing/fstest"

	"github.com/joshuadavidthomas/ts-skills/internal/protocol"
	"github.com/joshuadavidthomas/ts-skills/internal/registry"
)

func testInstaller(t *testing.T, body string) (*installer, project, requirement, func(string)) {
	t.Helper()
	digest, archive := clientTree(t, body)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/"+protocol.Version+"/skills/team/sample/current" {
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(protocol.CurrentResponse{Namespace: "team", Name: "sample", Digest: digest.String()})
			return
		}
		w.Header().Set("Content-Type", "application/zip")
		setClientTreeHeaders(w.Header(), "team", "sample", digest.String())
		_, _ = w.Write(archive)
	}))
	t.Cleanup(server.Close)
	installer := &installer{remote: remoteForServer(t, server)}
	project, err := openProject(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	requirement, err := current(clientSkill(t))
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
	installer := &installer{remote: remoteForServer(t, server)}
	requirement, err := current(clientSkill(t))
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

	if _, err := installer.install(context.Background(), project{}, requirement); err == nil {
		t.Fatal("Install accepted a zero Project")
	}
	if err := installer.restore(context.Background(), project{}); err == nil {
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
	if _, err := installer.install(context.Background(), project, requirement); err != nil {
		t.Fatal(err)
	}
	first, err := os.ReadFile(project.lockPath())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := installer.install(context.Background(), project, requirement); err != nil {
		t.Fatal(err)
	}
	second, err := os.ReadFile(project.lockPath())
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(first, second) {
		t.Fatal("idempotent install changed lock bytes")
	}
}

func TestInstallConvergesWhenAnotherInstallUpdatesTheLockDuringFetch(t *testing.T) {
	digest, archive := clientTree(t, "sample")
	project, err := openProject(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	other, err := registry.ParseSkillID("team/other")
	if err != nil {
		t.Fatal(err)
	}
	otherPublication, err := registry.NewPublicationID(other, digest)
	if err != nil {
		t.Fatal(err)
	}
	concurrentLock, err := newLock([]lockedSkill{{publication: otherPublication}})
	if err != nil {
		t.Fatal(err)
	}
	concurrentBytes, err := encodeLockBytes(concurrentLock)
	if err != nil {
		t.Fatal(err)
	}
	var mutationMu sync.Mutex
	var mutationErr error
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/" + protocol.Version + "/skills/team/sample/current":
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(protocol.CurrentResponse{Namespace: "team", Name: "sample", Digest: digest.String()})
		case "/api/" + protocol.Version + "/skills/team/sample/publications/" + digest.String() + "/tree.zip":
			err := os.MkdirAll(filepath.Dir(project.lockPath()), 0o755)
			if err == nil {
				err = writeSyncedFile(project.lockPath(), concurrentBytes, 0o600)
			}
			mutationMu.Lock()
			mutationErr = err
			mutationMu.Unlock()
			if err != nil {
				w.WriteHeader(http.StatusInternalServerError)
				return
			}
			w.Header().Set("Content-Type", "application/zip")
			setClientTreeHeaders(w.Header(), "team", "sample", digest.String())
			_, _ = w.Write(archive)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(server.Close)
	requirement, err := current(clientSkill(t))
	if err != nil {
		t.Fatal(err)
	}
	installer := &installer{remote: remoteForServer(t, server)}
	if _, err := installer.install(context.Background(), project, requirement); err != nil {
		t.Fatal(err)
	}
	mutationMu.Lock()
	err = mutationErr
	mutationMu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	writer, err := project.acquireWriter(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = writer.close() }()
	installed, _, _, err := writer.readLock()
	if err != nil {
		t.Fatal(err)
	}
	for _, skill := range []registry.SkillID{clientSkill(t), other} {
		if _, found := installed.lookup(skill); !found {
			t.Fatalf("final lock is missing %s", skill.String())
		}
	}
}

func TestInstallUpgradeReplacesManagedDestination(t *testing.T) {
	installer, project, requirement, update := testInstaller(t, "old")
	if _, err := installer.install(context.Background(), project, requirement); err != nil {
		t.Fatal(err)
	}
	update("new")
	if _, err := installer.install(context.Background(), project, requirement); err != nil {
		t.Fatal(err)
	}
	contents, err := os.ReadFile(filepath.Join(project.skillsDir(), "sample", "SKILL.md"))
	if err != nil || !bytes.Contains(contents, []byte("new")) {
		t.Fatalf("installed contents = %q, %v", contents, err)
	}
}

func TestInstallDoesNotReplaceUnmanagedDestination(t *testing.T) {
	installer, project, requirement, _ := testInstaller(t, "registry")
	destination := filepath.Join(project.skillsDir(), "sample")
	if err := os.MkdirAll(destination, 0o755); err != nil {
		t.Fatal(err)
	}
	local := filepath.Join(destination, "SKILL.md")
	if err := os.WriteFile(local, []byte("local"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := installer.install(context.Background(), project, requirement); !errors.Is(err, errLocalChanges) {
		t.Fatalf("Install() error = %v, want ErrLocalChanges", err)
	}
	contents, err := os.ReadFile(local)
	if err != nil || string(contents) != "local" {
		t.Fatalf("unmanaged skill contents = %q, %v", contents, err)
	}
	if _, err := os.Stat(project.lockPath()); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("lock exists after rejected install: %v", err)
	}
}

func TestInstallDoesNotReplaceModifiedDestination(t *testing.T) {
	installer, project, requirement, update := testInstaller(t, "old")
	if _, err := installer.install(context.Background(), project, requirement); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(project.lockPath())
	if err != nil {
		t.Fatal(err)
	}
	local := filepath.Join(project.skillsDir(), "sample", "local.txt")
	if err := os.WriteFile(local, []byte("edit"), 0o644); err != nil {
		t.Fatal(err)
	}
	update("new")
	if _, err := installer.install(context.Background(), project, requirement); !errors.Is(err, errLocalChanges) {
		t.Fatalf("Install() error = %v, want ErrLocalChanges", err)
	}
	if _, err := os.Stat(local); err != nil {
		t.Fatalf("local edit removed: %v", err)
	}
	after, err := os.ReadFile(project.lockPath())
	if err != nil || !bytes.Equal(before, after) {
		t.Fatalf("lock changed after rejected install: %q, %v", after, err)
	}
}

func TestRestoreReplacesChangedLockedDestinationAndPreservesOtherPaths(t *testing.T) {
	installer, project, requirement, _ := testInstaller(t, "locked")
	if _, err := installer.install(context.Background(), project, requirement); err != nil {
		t.Fatal(err)
	}
	lock, _ := os.ReadFile(project.lockPath())
	if err := os.WriteFile(filepath.Join(project.skillsDir(), "sample", "local.txt"), []byte("edit"), 0o644); err != nil {
		t.Fatal(err)
	}
	other := filepath.Join(project.skillsDir(), "other", "keep.txt")
	if err := os.MkdirAll(filepath.Dir(other), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(other, []byte("keep"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := installer.restore(context.Background(), project); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(project.skillsDir(), "sample", "local.txt")); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("local edit remains: %v", err)
	}
	if got, _ := os.ReadFile(other); string(got) != "keep" {
		t.Fatalf("other path = %q", got)
	}
	after, _ := os.ReadFile(project.lockPath())
	if !bytes.Equal(lock, after) {
		t.Fatal("restore rewrote lock")
	}
}

func TestWriterReadLockRejectsSymlink(t *testing.T) {
	project, err := openProject(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(project.lockPath()), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(t.TempDir(), project.lockPath()); err != nil {
		t.Skipf("create lock symlink: %v", err)
	}
	writer := &projectWriter{project: project}
	if _, _, _, err := writer.readLock(); err == nil || !strings.Contains(err.Error(), "symbolic link") {
		t.Fatalf("readLock() error = %v, want symbolic-link rejection", err)
	}
}

func TestWriterReadLockRejectsSymlinkedParent(t *testing.T) {
	project, err := openProject(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(t.TempDir(), filepath.Join(project.root, ".agents")); err != nil {
		t.Skipf("create managed-directory symlink: %v", err)
	}
	writer := &projectWriter{project: project}
	if _, _, _, err := writer.readLock(); err == nil || !strings.Contains(err.Error(), "symbolic link") {
		t.Fatalf("readLock() error = %v, want symbolic-link rejection", err)
	}
}

func stagedUpgrade(t *testing.T) (project, *projectWriter, *verifiedTree, lock) {
	t.Helper()
	installer, project, requirement, update := testInstaller(t, "old")
	if _, err := installer.install(context.Background(), project, requirement); err != nil {
		t.Fatal(err)
	}
	update("new")
	fetched, err := installer.remote.fetch(context.Background(), requirement)
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
	newLock, err := oldLock.with(lockedSkill{publication: verified.publication})
	if err != nil {
		t.Fatal(err)
	}
	return project, writer, verified, newLock
}

func TestReplaceRollsBackWhenDestinationSyncFails(t *testing.T) {
	project, writer, verified, newLock := stagedUpgrade(t)
	before, err := os.ReadFile(project.lockPath())
	if err != nil {
		t.Fatal(err)
	}
	skillsSyncs := 0
	writer.syncDirectory = func(path string) error {
		if path == project.skillsDir() {
			skillsSyncs++
			if skillsSyncs == 3 {
				return errors.New("sync skills directory")
			}
		}
		return syncDirectory(path)
	}
	if err := writer.replace(context.Background(), verified, newLock, true); err == nil {
		t.Fatal("replace succeeded after destination sync failure")
	}
	contents, err := os.ReadFile(filepath.Join(project.skillsDir(), "sample", "SKILL.md"))
	if err != nil || !bytes.Contains(contents, []byte("old")) {
		t.Fatalf("destination after rollback = %q, %v", contents, err)
	}
	after, err := os.ReadFile(project.lockPath())
	if err != nil || !bytes.Equal(before, after) {
		t.Fatalf("lock after rollback = %q, %v", after, err)
	}
}

func TestReplaceRetainsRecoveryTrashWhenRestoreSyncFails(t *testing.T) {
	project, writer, verified, newLock := stagedUpgrade(t)
	skillsSyncs := 0
	writer.syncDirectory = func(path string) error {
		if path == project.skillsDir() {
			skillsSyncs++
			if skillsSyncs == 3 || skillsSyncs == 5 {
				return errors.New("sync skills directory")
			}
		}
		return syncDirectory(path)
	}
	if err := writer.replace(context.Background(), verified, newLock, true); err == nil {
		t.Fatal("replace succeeded after the rollback sync failed")
	}
	entries, err := os.ReadDir(project.skillsDir())
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), installTrashRecoveryPrefix) {
			return
		}
	}
	t.Fatal("recovery trash was discarded after the rollback sync failed")
}

func TestReplaceRestoresOldTreeWhenMoveSyncFails(t *testing.T) {
	project, writer, verified, newLock := stagedUpgrade(t)
	writer.syncDirectory = func(path string) error {
		if strings.HasPrefix(filepath.Base(path), installTrashPendingPrefix) {
			return errors.New("sync install trash")
		}
		return syncDirectory(path)
	}
	if err := writer.replace(context.Background(), verified, newLock, true); err == nil {
		t.Fatal("replace succeeded after moving the old tree could not sync")
	}
	contents, err := os.ReadFile(filepath.Join(project.skillsDir(), "sample", "SKILL.md"))
	if err != nil || !bytes.Contains(contents, []byte("old")) {
		t.Fatalf("destination after rollback = %q, %v", contents, err)
	}
}

func TestReplaceRollsBackWhenVerifiedTreeWasAlreadyTransferred(t *testing.T) {
	project, writer, verified, newLock := stagedUpgrade(t)
	staged, err := verified.transfer()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(staged) })
	if err := writer.replace(context.Background(), verified, newLock, true); err == nil {
		t.Fatal("replace succeeded after verified tree transfer")
	}
	contents, err := os.ReadFile(filepath.Join(project.skillsDir(), "sample", "SKILL.md"))
	if err != nil || !bytes.Contains(contents, []byte("old")) {
		t.Fatalf("destination after rollback = %q, %v", contents, err)
	}
	entries, err := os.ReadDir(project.skillsDir())
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), installTrashPendingPrefix) {
			t.Fatalf("pending trash remains after rollback: %s", entry.Name())
		}
	}
}

func TestReplaceDoesNotRemoveDestinationAfterStagedRenameFailure(t *testing.T) {
	project, writer, verified, newLock := stagedUpgrade(t)
	destination := filepath.Join(project.skillsDir(), "sample")
	renameErr := errors.New("replace staged skill")
	writer.rename = func(old, new string) error {
		if old == verified.path && new == destination {
			if err := os.Mkdir(destination, 0o755); err != nil {
				return err
			}
			if err := os.WriteFile(filepath.Join(destination, "SKILL.md"), []byte("outside writer"), 0o644); err != nil {
				return err
			}
			return renameErr
		}
		return os.Rename(old, new)
	}
	if err := writer.replace(context.Background(), verified, newLock, true); err == nil {
		t.Fatal("replace succeeded after staged rename failure")
	}
	contents, err := os.ReadFile(filepath.Join(destination, "SKILL.md"))
	if err != nil || string(contents) != "outside writer" {
		t.Fatalf("destination after failed staged rename = %q, %v", contents, err)
	}
}

func TestReplaceRollsBackWhenLockRenameFails(t *testing.T) {
	project, writer, verified, newLock := stagedUpgrade(t)
	before, err := os.ReadFile(project.lockPath())
	if err != nil {
		t.Fatal(err)
	}
	writer.rename = func(old, new string) error {
		if new == project.lockPath() {
			return errors.New("replace lock")
		}
		return os.Rename(old, new)
	}
	if err := writer.replace(context.Background(), verified, newLock, true); err == nil {
		t.Fatal("replace succeeded after lock rename failure")
	}
	contents, err := os.ReadFile(filepath.Join(project.skillsDir(), "sample", "SKILL.md"))
	if err != nil || !bytes.Contains(contents, []byte("old")) {
		t.Fatalf("destination after rollback = %q, %v", contents, err)
	}
	after, err := os.ReadFile(project.lockPath())
	if err != nil || !bytes.Equal(before, after) {
		t.Fatalf("lock after rollback = %q, %v", after, err)
	}
}

func TestReplacePreservesOldTreeWhenRollbackCannotRestoreIt(t *testing.T) {
	project, writer, verified, newLock := stagedUpgrade(t)
	destination := filepath.Join(project.skillsDir(), "sample")
	restoreErr := errors.New("restore old skill")
	skillsSyncs := 0
	writer.syncDirectory = func(path string) error {
		if path == project.skillsDir() {
			skillsSyncs++
			if skillsSyncs == 3 {
				return errors.New("sync skills directory")
			}
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

	entries, err := os.ReadDir(project.skillsDir())
	if err != nil {
		t.Fatal(err)
	}
	var trash string
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), installTrashPrefix) {
			trash = filepath.Join(project.skillsDir(), entry.Name())
			break
		}
	}
	if trash == "" {
		t.Fatal("old tree was not retained after rollback failure")
	}
	contents, err := os.ReadFile(filepath.Join(trash, trashTreeName, "SKILL.md"))
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
	if _, err := os.Stat(trash); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("recovery trash remains: %v", err)
	}
	contents, err = os.ReadFile(filepath.Join(project.skillsDir(), "sample", "SKILL.md"))
	if err != nil || !bytes.Contains(contents, []byte("old")) {
		t.Fatalf("recovered destination = %q, %v", contents, err)
	}
}

func TestWriterRecoveryPreservesChangedDestination(t *testing.T) {
	installer, project, requirement, _ := testInstaller(t, "old")
	if _, err := installer.install(context.Background(), project, requirement); err != nil {
		t.Fatal(err)
	}
	inFlightDigest, _ := clientTree(t, "new")
	trash := filepath.Join(project.skillsDir(), installTrashRecoveryPrefix+"crash")
	if err := os.Mkdir(trash, 0o700); err != nil {
		t.Fatal(err)
	}
	record, err := json.Marshal(trashRecord{
		Skill:          requirement.skillID().String(),
		Tree:           inFlightDigest.String(),
		HadDestination: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(trash, trashRecordName), record, 0o600); err != nil {
		t.Fatal(err)
	}
	destination := filepath.Join(project.skillsDir(), "sample")
	if err := os.Rename(destination, filepath.Join(trash, trashTreeName)); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(destination, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(destination, "SKILL.md"), []byte("post-crash edit"), 0o644); err != nil {
		t.Fatal(err)
	}

	writer, err := project.acquireWriter(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	contents, err := os.ReadFile(filepath.Join(destination, "SKILL.md"))
	if err != nil || string(contents) != "post-crash edit" {
		t.Fatalf("destination after recovery = %q, %v", contents, err)
	}
	if _, err := os.Stat(trash); err != nil {
		t.Fatalf("recovery trash was removed: %v", err)
	}
	if err := writer.close(); err != nil {
		t.Fatal(err)
	}
	if _, err := installer.install(context.Background(), project, requirement); !errors.Is(err, errLocalChanges) {
		t.Fatalf("Install() error = %v, want ErrLocalChanges", err)
	}
}

func TestWriterSweepPreservesChangedUncommittedDestination(t *testing.T) {
	installer, project, requirement, _ := testInstaller(t, "installed")
	if _, err := installer.install(context.Background(), project, requirement); err != nil {
		t.Fatal(err)
	}
	contents, err := os.ReadFile(project.lockPath())
	if err != nil {
		t.Fatal(err)
	}
	lock, err := decodeLock(bytes.NewReader(contents))
	if err != nil {
		t.Fatal(err)
	}
	locked, found := lock.lookup(requirement.skillID())
	if !found {
		t.Fatal("installed skill is missing from the lock")
	}
	if err := os.Remove(project.lockPath()); err != nil {
		t.Fatal(err)
	}
	trash := filepath.Join(project.skillsDir(), installTrashPendingPrefix+"crash")
	if err := os.Mkdir(trash, 0o700); err != nil {
		t.Fatal(err)
	}
	record, err := json.Marshal(trashRecord{Skill: requirement.skillID().String(), Tree: locked.publication.Tree().String()})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(trash, trashRecordName), record, 0o600); err != nil {
		t.Fatal(err)
	}
	destination := filepath.Join(project.skillsDir(), "sample")
	if err := os.WriteFile(filepath.Join(destination, "local.txt"), []byte("post-crash edit"), 0o644); err != nil {
		t.Fatal(err)
	}

	writer, err := project.acquireWriter(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = writer.close() }()
	if _, err := os.Stat(filepath.Join(destination, "local.txt")); err != nil {
		t.Fatalf("post-crash destination edit was removed: %v", err)
	}
	if _, err := os.Stat(trash); err != nil {
		t.Fatalf("recovery trash was removed: %v", err)
	}
}

func TestWriterSweepLitterHonorsCancellationWhileHashingRecoveryTrash(t *testing.T) {
	installer, project, requirement, _ := testInstaller(t, "installed")
	if _, err := installer.install(context.Background(), project, requirement); err != nil {
		t.Fatal(err)
	}
	writer, err := project.acquireWriter(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = writer.close() }()
	lock, _, _, err := writer.readLock()
	if err != nil {
		t.Fatal(err)
	}
	locked, found := lock.lookup(requirement.skillID())
	if !found {
		t.Fatal("installed skill is missing from the lock")
	}
	trash := filepath.Join(project.skillsDir(), installTrashRecoveryPrefix+"crash")
	if err := os.Mkdir(trash, 0o700); err != nil {
		t.Fatal(err)
	}
	record, err := json.Marshal(trashRecord{
		Skill:          requirement.skillID().String(),
		Tree:           locked.publication.Tree().String(),
		HadDestination: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(trash, trashRecordName), record, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(filepath.Join(project.skillsDir(), "sample"), filepath.Join(trash, trashTreeName)); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := writer.sweepLitter(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("sweepLitter() error = %v, want context cancellation", err)
	}
}

func TestReplaceLeavesDestinationAndLockAlignedAfterLockRename(t *testing.T) {
	project, writer, verified, newLock := stagedUpgrade(t)
	writer.syncDirectory = func(path string) error {
		if path == filepath.Dir(project.lockPath()) {
			return errors.New("sync lock directory")
		}
		return syncDirectory(path)
	}
	if err := writer.replace(context.Background(), verified, newLock, true); err == nil {
		t.Fatal("replace succeeded after lock directory sync failure")
	}
	contents, err := os.ReadFile(filepath.Join(project.skillsDir(), "sample", "SKILL.md"))
	if err != nil || !bytes.Contains(contents, []byte("new")) {
		t.Fatalf("destination after lock rename = %q, %v", contents, err)
	}
	wantLock, err := encodeLockBytes(newLock)
	if err != nil {
		t.Fatal(err)
	}
	gotLock, err := os.ReadFile(project.lockPath())
	if err != nil || !bytes.Equal(gotLock, wantLock) {
		t.Fatalf("lock after lock rename = %q, %v", gotLock, err)
	}
}

func TestRevalidationPreservesOperationalErrors(t *testing.T) {
	installer, project, requirement, _ := testInstaller(t, "installed")
	if _, err := installer.install(context.Background(), project, requirement); err != nil {
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
	before, err := writer.destinationState(context.Background(), requirement.skillID())
	if err != nil {
		t.Fatal(err)
	}
	plan, err := makeRestorePlan(context.Background(), writer)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := writer.assertUnchanged(ctx, requirement.skillID(), lockBytes, hadLock, before); !errors.Is(err, errProjectChanged) || !errors.Is(err, context.Canceled) {
		t.Fatalf("install revalidation error = %v, want project change and cancellation", err)
	}
	if err := plan.matches(ctx, writer); !errors.Is(err, errProjectChanged) || !errors.Is(err, context.Canceled) {
		t.Fatalf("restore revalidation error = %v, want project change and cancellation", err)
	}
}

func TestWriterSweepsStagingAndKeepsUnknownTrash(t *testing.T) {
	project, _ := openProject(t.TempDir())
	writer, err := project.acquireWriter(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if err := writer.close(); err != nil {
		t.Fatal(err)
	}
	stage := filepath.Join(project.skillsDir(), installStagingPrefix+"dead")
	trash := filepath.Join(project.skillsDir(), installTrashPrefix+"dead")
	real := filepath.Join(project.skillsDir(), "staging-real")
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
		t.Fatalf("unknown trash was removed: %v", err)
	}
	if _, err := os.Stat(real); err != nil {
		t.Fatalf("real skill removed: %v", err)
	}
}

func TestWriterSweepsUncommittedNewDestination(t *testing.T) {
	installer, project, requirement, _ := testInstaller(t, "installed")
	if _, err := installer.install(context.Background(), project, requirement); err != nil {
		t.Fatal(err)
	}
	contents, err := os.ReadFile(project.lockPath())
	if err != nil {
		t.Fatal(err)
	}
	lock, err := decodeLock(bytes.NewReader(contents))
	if err != nil {
		t.Fatal(err)
	}
	locked, found := lock.lookup(requirement.skillID())
	if !found {
		t.Fatal("installed skill is missing from the lock")
	}
	if err := os.Remove(project.lockPath()); err != nil {
		t.Fatal(err)
	}
	trash := filepath.Join(project.skillsDir(), installTrashPendingPrefix+"dead")
	if err := os.Mkdir(trash, 0o700); err != nil {
		t.Fatal(err)
	}
	record, err := json.Marshal(trashRecord{Skill: requirement.skillID().String(), Tree: locked.publication.Tree().String()})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(trash, trashRecordName), record, 0o600); err != nil {
		t.Fatal(err)
	}

	writer, err := project.acquireWriter(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = writer.close() }()
	for _, path := range []string{filepath.Join(project.skillsDir(), "sample"), trash} {
		if _, err := os.Stat(path); !errors.Is(err, fs.ErrNotExist) {
			t.Fatalf("uncommitted install litter remains %q: %v", path, err)
		}
	}
}

type cancelAfterReadFS struct {
	files  fstest.MapFS
	cancel context.CancelFunc
}

func (f cancelAfterReadFS) Open(name string) (fs.File, error) {
	file, err := f.files.Open(name)
	if err != nil || name == "." {
		return file, err
	}
	return &cancelAfterReadFile{File: file, cancel: f.cancel}, nil
}

func (f cancelAfterReadFS) ReadDir(name string) ([]fs.DirEntry, error) {
	return f.files.ReadDir(name)
}

type cancelAfterReadFile struct {
	fs.File
	cancelled bool
	cancel    context.CancelFunc
}

func (f *cancelAfterReadFile) Read(buffer []byte) (int, error) {
	read, err := f.File.Read(buffer)
	if read > 0 && !f.cancelled {
		f.cancelled = true
		f.cancel()
	}
	return read, err
}

func TestCopyFetchedTreeHonorsCancellationWhileStreaming(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	parent := t.TempDir()
	source := cancelAfterReadFS{
		files:  fstest.MapFS{"large": {Data: bytes.Repeat([]byte("x"), 128<<10)}},
		cancel: cancel,
	}
	if _, err := copyFetchedTree(ctx, parent, source); !errors.Is(err, context.Canceled) {
		t.Fatalf("copyFetchedTree() error = %v, want context cancellation", err)
	}
	entries, err := os.ReadDir(parent)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("partial stage remains after cancellation: %v", entries)
	}
}

func TestWriterSweepsEmptyFreshInstallTrash(t *testing.T) {
	project, err := openProject(t.TempDir())
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
	pending := filepath.Join(project.skillsDir(), installTrashPendingPrefix+"fresh")
	if err := os.Mkdir(pending, 0o700); err != nil {
		t.Fatal(err)
	}
	digest, _ := clientTree(t, "fresh")
	record, err := json.Marshal(trashRecord{Skill: clientSkill(t).String(), Tree: digest.String(), HadDestination: false})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(pending, trashRecordName), record, 0o600); err != nil {
		t.Fatal(err)
	}

	writer, err = project.acquireWriter(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = writer.close() }()
	if _, err := os.Stat(pending); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("empty fresh-install trash remains: %v", err)
	}
}

func TestWriterSweepsRecordlessTrashWithoutTree(t *testing.T) {
	project, err := openProject(t.TempDir())
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
	trash := filepath.Join(project.skillsDir(), installTrashPendingPrefix+"recordless")
	if err := os.Mkdir(trash, 0o700); err != nil {
		t.Fatal(err)
	}
	writer, err = project.acquireWriter(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = writer.close() }()
	if _, err := os.Stat(trash); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("recordless trash remains: %v", err)
	}
}

func TestWriterRejectsRecordlessTrashWithTree(t *testing.T) {
	project, err := openProject(t.TempDir())
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
	trash := filepath.Join(project.skillsDir(), installTrashPendingPrefix+"recordless-tree")
	if err := os.MkdirAll(filepath.Join(trash, trashTreeName), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(trash, trashTreeName, "SKILL.md"), []byte("previous skill"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := project.acquireWriter(context.Background()); err == nil || !strings.Contains(err.Error(), trash) {
		t.Fatalf("acquireWriter() error = %v, want error naming %q", err, trash)
	}
	if _, err := os.Stat(trash); err != nil {
		t.Fatalf("recordless trash was removed: %v", err)
	}
}

func TestWriterRejectsCorruptTrashRecord(t *testing.T) {
	project, err := openProject(t.TempDir())
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
	trash := filepath.Join(project.skillsDir(), installTrashPendingPrefix+"corrupt")
	if err := os.Mkdir(trash, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(trash, trashRecordName), []byte("not JSON"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := project.acquireWriter(context.Background()); err == nil || !strings.Contains(err.Error(), trash) {
		t.Fatalf("acquireWriter() error = %v, want error naming %q", err, trash)
	}
	if _, err := os.Stat(trash); err != nil {
		t.Fatalf("corrupt trash was removed: %v", err)
	}
}

func TestWriterSweepsEmptyInstallTrashWithLockedMissingDestination(t *testing.T) {
	installer, project, requirement, _ := testInstaller(t, "installed")
	if _, err := installer.install(context.Background(), project, requirement); err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(project.destination(requirement.skillID().Name().String())); err != nil {
		t.Fatal(err)
	}
	pending := filepath.Join(project.skillsDir(), installTrashPendingPrefix+"missing")
	if err := os.Mkdir(pending, 0o700); err != nil {
		t.Fatal(err)
	}
	digest, _ := clientTree(t, "installed")
	record, err := json.Marshal(trashRecord{Skill: requirement.skillID().String(), Tree: digest.String(), HadDestination: false})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(pending, trashRecordName), record, 0o600); err != nil {
		t.Fatal(err)
	}

	writer, err := project.acquireWriter(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = writer.close() }()
	if _, err := os.Stat(pending); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("empty locked-install trash remains: %v", err)
	}
}

func TestWriterSweepsKnownStaleTrash(t *testing.T) {
	installer, project, requirement, _ := testInstaller(t, "installed")
	if _, err := installer.install(context.Background(), project, requirement); err != nil {
		t.Fatal(err)
	}
	pending := filepath.Join(project.skillsDir(), installTrashPendingPrefix+"dead")
	if err := os.Mkdir(pending, 0o700); err != nil {
		t.Fatal(err)
	}
	record, err := json.Marshal(trashRecord{Skill: requirement.skillID().String()})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(pending, trashRecordName), record, 0o600); err != nil {
		t.Fatal(err)
	}
	recovery := filepath.Join(project.skillsDir(), installTrashRecoveryPrefix+"dead")
	if err := os.Mkdir(recovery, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(recovery, trashRecordName), record, 0o600); err != nil {
		t.Fatal(err)
	}
	garbage := filepath.Join(project.skillsDir(), installTrashGarbagePrefix+"dead")
	if err := os.Mkdir(garbage, 0o700); err != nil {
		t.Fatal(err)
	}

	writer, err := project.acquireWriter(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = writer.close() }()
	for _, path := range []string{pending, recovery, garbage} {
		if _, err := os.Stat(path); !errors.Is(err, fs.ErrNotExist) {
			t.Fatalf("stale trash remains %q: %v", path, err)
		}
	}
}
