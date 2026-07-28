package install

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"testing"
	"testing/fstest"

	"github.com/joshuadavidthomas/ts-skill-registry/internal/agentskill"
	"github.com/joshuadavidthomas/ts-skill-registry/internal/registry"
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
	publication registry.PublicationID
	files       fstest.MapFS
	last        *fetchedMap
}

func (r *scriptedRemote) Fetch(context.Context, Requirement) (FetchedSkill, error) {
	r.last = &fetchedMap{MapFS: r.files}
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
