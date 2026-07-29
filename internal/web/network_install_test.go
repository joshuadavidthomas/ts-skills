//go:build ignore

// TODO(plan 005): move this end-to-end client test to cmd/ts-skills.
package web

import (
	"bytes"
	"context"
	"io/fs"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/joshuadavidthomas/ts-skills/internal/agentskill"
	"github.com/joshuadavidthomas/ts-skills/internal/client"
	"github.com/joshuadavidthomas/ts-skills/internal/install"
	"github.com/joshuadavidthomas/ts-skills/internal/protocol"
	"github.com/joshuadavidthomas/ts-skills/internal/safetree"
)

func publishWebCandidate(t *testing.T, fixture *webFixture, instructions string) string {
	t.Helper()
	candidatePath := fixture.uploadDirectory(instructions)
	digest := digestPattern.FindString(fixture.get(candidatePath))
	response := postForm(t, fixture, candidatePath+"/publish", nil)
	_ = response.Body.Close()
	if response.StatusCode != http.StatusSeeOther {
		t.Fatalf("publish status = %d", response.StatusCode)
	}
	return digest
}

func networkInstaller(t *testing.T, fixture *webFixture) (*install.Installer, agentskill.SkillID) {
	t.Helper()
	return networkInstallerWithClient(t, fixture, &http.Client{Timeout: 10 * time.Second})
}

func networkInstallerWithClient(t *testing.T, fixture *webFixture, httpClient *http.Client) (*install.Installer, agentskill.SkillID) {
	t.Helper()
	origin, err := client.ParseOrigin(fixture.server.URL)
	if err != nil {
		t.Fatal(err)
	}
	remote, err := client.NewRemote(origin, httpClient, t.TempDir(), safetree.PrototypeLimits())
	if err != nil {
		t.Fatal(err)
	}
	installer, err := install.NewInstaller(remote)
	if err != nil {
		t.Fatal(err)
	}
	skill, err := agentskill.ParseSkillID("team/sample")
	if err != nil {
		t.Fatal(err)
	}
	return installer, skill
}

type recordingTransport struct {
	mu       sync.Mutex
	delegate http.RoundTripper
	requests []string
}

func (r *recordingTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	r.mu.Lock()
	r.requests = append(r.requests, request.Method+" "+request.URL.Path)
	r.mu.Unlock()
	return r.delegate.RoundTrip(request)
}

func (r *recordingTransport) takeRequests() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	requests := append([]string(nil), r.requests...)
	r.requests = nil
	return requests
}

func readTreeBytes(t *testing.T, root string) map[string][]byte {
	t.Helper()
	tree := os.DirFS(root)
	files := make(map[string][]byte)
	err := fs.WalkDir(tree, ".", func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			return nil
		}
		contents, err := fs.ReadFile(tree, path)
		if err != nil {
			return err
		}
		files[path] = contents
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return files
}

func TestReadOnlyAPICurrentAndExactDownloadsSurviveStorageRestart(t *testing.T) {
	fixture := newWebFixture(t)
	firstDigestText := publishWebCandidate(t, fixture, "First network publication.\n")
	secondDigestText := publishWebCandidate(t, fixture, "Second network publication.\n")
	response := postForm(t, fixture, "/current", url.Values{"skill": {"team/sample"}, "digest": {secondDigestText}})
	_ = response.Body.Close()
	if response.StatusCode != http.StatusSeeOther {
		t.Fatalf("set current status = %d", response.StatusCode)
	}

	fixture.restart()
	installer, skill := networkInstaller(t, fixture)
	currentProject, err := install.OpenProject(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	current, _ := install.Current(skill)
	locked, err := installer.Install(context.Background(), currentProject, current)
	if err != nil {
		t.Fatal(err)
	}
	if locked.Publication.Tree().String() != secondDigestText {
		t.Fatalf("current install digest = %s", locked.Publication.Tree().String())
	}
	currentSkill, err := os.ReadFile(filepath.Join(currentProject.SkillsDir(), "sample", "SKILL.md"))
	if err != nil || !bytes.Contains(currentSkill, []byte("Second network publication.")) {
		t.Fatalf("current SKILL.md = %q, %v", currentSkill, err)
	}

	exactProject, err := install.OpenProject(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	firstDigest, err := agentskill.ParseTreeDigest(firstDigestText)
	if err != nil {
		t.Fatal(err)
	}
	exact, _ := install.Exact(skill, firstDigest)
	locked, err = installer.Install(context.Background(), exactProject, exact)
	if err != nil {
		t.Fatal(err)
	}
	if locked.Publication.Tree() != firstDigest {
		t.Fatalf("exact install digest = %s", locked.Publication.Tree().String())
	}
	exactSkill, err := os.ReadFile(filepath.Join(exactProject.SkillsDir(), "sample", "SKILL.md"))
	if err != nil || !bytes.Contains(exactSkill, []byte("First network publication.")) {
		t.Fatalf("exact SKILL.md = %q, %v", exactSkill, err)
	}
}

func TestRestoreUsesLockedPublicationAfterCurrentChanges(t *testing.T) {
	fixture := newWebFixture(t)
	firstDigestText := publishWebCandidate(t, fixture, "Locked publication A.\n")
	recorder := &recordingTransport{delegate: http.DefaultTransport}
	httpClient := &http.Client{Timeout: 10 * time.Second, Transport: recorder}
	installer, skill := networkInstallerWithClient(t, fixture, httpClient)
	project, err := install.OpenProject(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	requirement, err := install.Current(skill)
	if err != nil {
		t.Fatal(err)
	}
	locked, err := installer.Install(context.Background(), project, requirement)
	if err != nil {
		t.Fatal(err)
	}
	if locked.Publication.Tree().String() != firstDigestText {
		t.Fatalf("initial install digest = %s, want %s", locked.Publication.Tree().String(), firstDigestText)
	}
	destination := filepath.Join(project.SkillsDir(), "sample")
	firstTree := readTreeBytes(t, destination)
	firstLock, err := os.ReadFile(project.LockPath())
	if err != nil {
		t.Fatal(err)
	}

	secondDigestText := publishWebCandidate(t, fixture, "Current publication B.\n")
	if secondDigestText == firstDigestText {
		t.Fatal("publication B has publication A's digest")
	}
	response := postForm(t, fixture, "/current", url.Values{"skill": {"team/sample"}, "digest": {secondDigestText}})
	_ = response.Body.Close()
	if response.StatusCode != http.StatusSeeOther {
		t.Fatalf("set current status = %d", response.StatusCode)
	}
	currentCatalog := fixture.get("/")
	if !strings.Contains(currentCatalog, secondDigestText) || strings.Contains(currentCatalog, firstDigestText) {
		t.Fatalf("catalog did not select publication B: %s", currentCatalog)
	}
	if err := os.RemoveAll(destination); err != nil {
		t.Fatal(err)
	}
	recorder.takeRequests()

	if err := installer.Restore(context.Background(), project); err != nil {
		t.Fatal(err)
	}
	expectedTreeRequest := "GET /api/" + protocol.Version + "/skills/team/sample/publications/" + firstDigestText + "/tree.zip"
	if requests := recorder.takeRequests(); !reflect.DeepEqual(requests, []string{expectedTreeRequest}) {
		t.Fatalf("restore requests = %v, want [%s]", requests, expectedTreeRequest)
	}
	restoredTree := readTreeBytes(t, destination)
	if !reflect.DeepEqual(restoredTree, firstTree) {
		t.Fatalf("restored tree bytes = %q, want publication A bytes %q", restoredTree, firstTree)
	}
	firstDigest, err := agentskill.ParseTreeDigest(firstDigestText)
	if err != nil {
		t.Fatal(err)
	}
	restoredDigest, err := agentskill.SumTree(context.Background(), os.DirFS(destination), ".")
	if err != nil {
		t.Fatal(err)
	}
	if restoredDigest != firstDigest {
		t.Fatalf("restored digest = %s, want %s", restoredDigest.String(), firstDigestText)
	}
	restoredLock, err := os.ReadFile(project.LockPath())
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(restoredLock, firstLock) {
		t.Fatalf("restore changed lock from %q to %q", firstLock, restoredLock)
	}
	if !bytes.Contains(restoredLock, []byte(firstDigestText)) || bytes.Contains(restoredLock, []byte(secondDigestText)) {
		t.Fatalf("restored lock does not retain only publication A: %s", restoredLock)
	}
}

func TestRepeatedExactNetworkInstallLeavesDestinationAndLockUnchanged(t *testing.T) {
	fixture := newWebFixture(t)
	digestText := publishWebCandidate(t, fixture, "Stable publication.\n")
	installer, skill := networkInstaller(t, fixture)
	project, _ := install.OpenProject(t.TempDir())
	digest, _ := agentskill.ParseTreeDigest(digestText)
	requirement, _ := install.Exact(skill, digest)
	if _, err := installer.Install(context.Background(), project, requirement); err != nil {
		t.Fatal(err)
	}
	destination := filepath.Join(project.SkillsDir(), "sample")
	beforeDestination, err := os.Stat(destination)
	if err != nil {
		t.Fatal(err)
	}
	beforeLock, err := os.ReadFile(project.LockPath())
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(10 * time.Millisecond)
	if _, err := installer.Install(context.Background(), project, requirement); err != nil {
		t.Fatal(err)
	}
	afterDestination, err := os.Stat(destination)
	if err != nil {
		t.Fatal(err)
	}
	afterLock, err := os.ReadFile(project.LockPath())
	if err != nil {
		t.Fatal(err)
	}
	if !beforeDestination.ModTime().Equal(afterDestination.ModTime()) || !bytes.Equal(beforeLock, afterLock) {
		t.Fatal("no-change install rewrote the destination or lock")
	}
	entries, err := os.ReadDir(project.SkillsDir())
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), ".ts-skills-") {
			t.Fatalf("no-change install left litter: %v", entries)
		}
	}
	if !strings.Contains(string(afterLock), digestText) {
		t.Fatalf("lock does not contain %s", digestText)
	}
}
