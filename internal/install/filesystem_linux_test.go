//go:build linux

package install

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/joshuadavidthomas/ts-skill-registry/internal/agentskill"
)

func TestAcquireWriterDurablyCreatesManagedAncestors(t *testing.T) {
	project, err := OpenProject(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	points := make(map[string]int)
	transactionFailure = func(point string) error {
		points[point]++
		return nil
	}
	t.Cleanup(func() { transactionFailure = nil })
	writer, err := project.acquireWriter(context.Background())
	transactionFailure = nil
	if err != nil {
		t.Fatal(err)
	}
	if err := writer.close(); err != nil {
		t.Fatal(err)
	}
	if got := points["before-fsync-managed-directory-created"]; got != 4 {
		t.Fatalf("new managed directory fsync count = %d, want 4", got)
	}
	if got := points["before-fsync-managed-directory-parent"]; got != 4 {
		t.Fatalf("new managed parent fsync count = %d, want 4", got)
	}
	for _, path := range []string{
		filepath.Join(project.root, ".agents"),
		project.SkillsDir(),
		project.StateDir(),
		project.operationsDir(),
	} {
		info, err := os.Lstat(path)
		if err != nil || !info.IsDir() {
			t.Fatalf("managed ancestor %q = %v, %v", path, info, err)
		}
	}
}

func TestAcquireWriterRejectsSymlinkComponents(t *testing.T) {
	for _, test := range []struct {
		name string
		path func(Project) string
	}{
		{name: "agents", path: func(project Project) string { return filepath.Join(project.root, ".agents") }},
		{name: "skills", path: func(project Project) string { return project.SkillsDir() }},
		{name: "state", path: func(project Project) string { return project.StateDir() }},
	} {
		t.Run(test.name, func(t *testing.T) {
			project, err := OpenProject(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			path := test.path(project)
			if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
				t.Fatal(err)
			}
			target := t.TempDir()
			if err := os.Symlink(target, path); err != nil {
				t.Fatal(err)
			}
			if _, err := project.acquireWriter(context.Background()); err == nil || !strings.Contains(err.Error(), "symbolic link or reparse point") {
				t.Fatalf("acquire error = %v", err)
			}
		})
	}
}

func TestAcquireWriterRejectsManagedFileAndOperationSymlinks(t *testing.T) {
	for _, test := range []struct {
		name string
		path func(Project) string
	}{
		{name: "lock", path: func(project Project) string { return project.LockPath() }},
		{name: "operation", path: func(project Project) string {
			return filepath.Join(project.operationsDir(), strings.Repeat("a", 32))
		}},
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
			if err := os.Symlink(t.TempDir(), test.path(project)); err != nil {
				t.Fatal(err)
			}
			writer, err = project.acquireWriter(context.Background())
			if writer != nil {
				_ = writer.close()
			}
			if err == nil {
				t.Fatal("acquire accepted a managed symlink")
			}
		})
	}
}

func TestInstallerRejectsSymlinkDestination(t *testing.T) {
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
	writer, err := project.acquireWriter(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if err := writer.close(); err != nil {
		t.Fatal(err)
	}
	target := t.TempDir()
	if err := os.Symlink(target, project.destination("sample")); err != nil {
		t.Fatal(err)
	}
	requirement, err := Current(skill)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := installer.Install(context.Background(), project, requirement); err == nil {
		t.Fatal("install accepted a symlink destination")
	}
	entries, err := os.ReadDir(target)
	if err != nil || len(entries) != 0 {
		t.Fatalf("symlink target changed: entries=%d, err=%v", len(entries), err)
	}
}

func TestTransactionPreflightRejectsDestinationOnAnotherDevice(t *testing.T) {
	project, installer, requirement, skill := prepareUpdate(t)
	destination := project.destination(skill.Name().String())
	oldDigest, err := agentskillDigest(destination)
	if err != nil {
		t.Fatal(err)
	}

	originalIdentity := filesystemIdentityForPath
	filesystemIdentityForPath = func(path string) (uint64, error) {
		identity, err := originalIdentity(path)
		if err == nil && filepath.Clean(path) == filepath.Clean(destination) {
			identity++
		}
		return identity, err
	}
	t.Cleanup(func() { filesystemIdentityForPath = originalIdentity })
	if _, err := installer.Install(context.Background(), project, requirement); err == nil || !strings.Contains(err.Error(), "not on the project filesystem") {
		t.Fatalf("install error = %v", err)
	}
	filesystemIdentityForPath = originalIdentity

	actualDigest, err := agentskillDigest(destination)
	if err != nil {
		t.Fatal(err)
	}
	if actualDigest != oldDigest {
		t.Fatal("destination changed after cross-device preflight failure")
	}
	operations, err := os.ReadDir(project.operationsDir())
	if err != nil {
		t.Fatal(err)
	}
	for _, operation := range operations {
		_, err := os.Lstat(filepath.Join(project.operationsDir(), operation.Name(), journalName))
		if !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("durable journal exists after preflight failure: %v", err)
		}
	}
}

func agentskillDigest(path string) (agentskill.TreeDigest, error) {
	return agentskill.SumTree(os.DirFS(path), ".")
}
