package install

import (
	"bytes"
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"testing"
	"testing/fstest"

	"github.com/joshuadavidthomas/ts-skills/internal/agentskill"
)

type fetchedMap struct {
	fstest.MapFS
	closed bool
}

func (f *fetchedMap) Close() error {
	if f.closed {
		return errors.New("closed twice")
	}
	f.closed = true
	return nil
}

type scriptedRemote struct {
	publication agentskill.PublicationID
	files       fstest.MapFS
	fetch       func(context.Context)
}

func (r *scriptedRemote) Fetch(ctx context.Context, _ Requirement) (FetchedSkill, error) {
	if r.fetch != nil {
		r.fetch(ctx)
	}
	return FetchedSkill{Publication: r.publication, Tree: &fetchedMap{MapFS: r.files}}, nil
}

func publicationFor(t *testing.T, body string) (agentskill.SkillID, agentskill.PublicationID, fstest.MapFS) {
	t.Helper()
	name, _ := agentskill.ParseName("sample")
	namespace, _ := agentskill.ParseNamespace("team")
	skill, _ := agentskill.NewSkillID(namespace, name)
	files := fstest.MapFS{"SKILL.md": {Data: []byte("---\nname: sample\ndescription: Test\n---\n" + body)}, "assets/data.txt": {Data: []byte(body)}}
	digest, err := agentskill.SumTree(context.Background(), files, ".")
	if err != nil {
		t.Fatal(err)
	}
	publication, err := agentskill.NewPublicationID(skill, digest)
	if err != nil {
		t.Fatal(err)
	}
	return skill, publication, files
}

func TestInstallIsIdempotentAndKeepsCanonicalLock(t *testing.T) {
	skill, publication, files := publicationFor(t, "one")
	remote := &scriptedRemote{publication: publication, files: files}
	installer, _ := NewInstaller(remote)
	project, _ := OpenProject(t.TempDir())
	requirement, _ := Current(skill)
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
	if got := string(first); got != "schema = 1\n\n[[skills]]\nskill = \"team/sample\"\ndigest = \""+publication.Tree().String()+"\"\n" {
		t.Fatalf("lock = %q", got)
	}
}

func TestInstallUpgradeReplacesDestination(t *testing.T) {
	skill, oldPublication, oldFiles := publicationFor(t, "old")
	remote := &scriptedRemote{publication: oldPublication, files: oldFiles}
	installer, _ := NewInstaller(remote)
	project, _ := OpenProject(t.TempDir())
	requirement, _ := Current(skill)
	if _, err := installer.Install(context.Background(), project, requirement); err != nil {
		t.Fatal(err)
	}
	_, next, nextFiles := publicationFor(t, "new")
	remote.publication, remote.files = next, nextFiles
	if _, err := installer.Install(context.Background(), project, requirement); err != nil {
		t.Fatal(err)
	}
	contents, err := os.ReadFile(filepath.Join(project.SkillsDir(), "sample", "assets", "data.txt"))
	if err != nil || string(contents) != "new" {
		t.Fatalf("installed contents = %q, %v", contents, err)
	}
}

func TestRestoreReplacesChangedLockedDestinationAndPreservesOtherPaths(t *testing.T) {
	skill, publication, files := publicationFor(t, "locked")
	remote := &scriptedRemote{publication: publication, files: files}
	installer, _ := NewInstaller(remote)
	project, _ := OpenProject(t.TempDir())
	requirement, _ := Current(skill)
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

func TestWriterSweepsReservedLitterOnly(t *testing.T) {
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
	t.Cleanup(func() {
		if err := writer.close(); err != nil {
			t.Error(err)
		}
	})
	for _, path := range []string{stage, trash} {
		if _, err := os.Stat(path); !errors.Is(err, fs.ErrNotExist) {
			t.Fatalf("litter remains %q: %v", path, err)
		}
	}
	if _, err := os.Stat(real); err != nil {
		t.Fatalf("real skill removed: %v", err)
	}
}
