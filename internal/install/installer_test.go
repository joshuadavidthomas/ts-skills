package install

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"testing"
	"testing/fstest"
	"time"

	"github.com/gofrs/flock"

	"github.com/joshuadavidthomas/ts-skill-registry/internal/agentskill"
	"github.com/joshuadavidthomas/ts-skill-registry/internal/registry"
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
	digest, err := agentskill.SumTree(files, ".")
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

func TestProjectWriterCloseCleansStagingBeforeReleasingLock(t *testing.T) {
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
		writer.closeLock = (*flock.Flock).Close
		_ = writer.close()
	})
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
	writer.closeLock = func(*flock.Flock) error {
		lockCloseCalls++
		return nil
	}
	if err := writer.close(); !errors.Is(err, injected) {
		t.Fatalf("first close error = %v, want injected failure", err)
	}
	if writer.closed || writer.lock == nil || lockCloseCalls != 0 {
		t.Fatal("writer released its lock before all staging was clean")
	}
	if len(writer.staging) != 1 {
		t.Fatalf("tracked staging after failed close = %d, want 1", len(writer.staging))
	}
	if _, found := writer.staging[failedStaging]; !found {
		t.Fatal("failed staging entry was not retained")
	}
	if _, err := os.Stat(cleanedStaging); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("successfully cleaned staging remains: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	if _, err := project.acquireWriter(ctx); !errors.Is(err, ErrBusy) {
		t.Fatalf("second writer while cleanup is pending = %v, want ErrBusy", err)
	}

	writer.removeStaging = os.RemoveAll
	writer.closeLock = (*flock.Flock).Close
	if err := writer.close(); err != nil {
		t.Fatal(err)
	}
	if !writer.closed || writer.lock != nil || len(writer.staging) != 0 {
		t.Fatal("retried close did not release cleaned writer")
	}
	reopened, err := project.acquireWriter(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if err := reopened.close(); err != nil {
		t.Fatal(err)
	}
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
