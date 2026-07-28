package install

import (
	"bytes"
	"context"
	"errors"
	"os"
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

func TestLockCodecIsCanonicalAndStrict(t *testing.T) {
	_, publication, _ := testPublication(t, "team", "sample", "one")
	locked, _ := NewLockedSkill(publication)
	lock, _ := NewLock([]LockedSkill{locked})
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
