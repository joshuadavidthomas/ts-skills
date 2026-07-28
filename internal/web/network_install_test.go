package web

import (
	"bytes"
	"context"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/joshuadavidthomas/ts-skill-registry/internal/agentskill"
	"github.com/joshuadavidthomas/ts-skill-registry/internal/client"
	"github.com/joshuadavidthomas/ts-skill-registry/internal/install"
	"github.com/joshuadavidthomas/ts-skill-registry/internal/registry"
	"github.com/joshuadavidthomas/ts-skill-registry/internal/safetree"
)

func publishWebCandidate(t *testing.T, fixture *webFixture, instructions string) string {
	t.Helper()
	candidatePath := fixture.uploadZIP(instructions)
	digest := digestPattern.FindString(fixture.get(candidatePath))
	response := postForm(t, fixture, candidatePath+"/publish", nil)
	_ = response.Body.Close()
	if response.StatusCode != http.StatusSeeOther {
		t.Fatalf("publish status = %d", response.StatusCode)
	}
	return digest
}

func networkInstaller(t *testing.T, fixture *webFixture) (*install.Installer, registry.SkillID) {
	t.Helper()
	origin, err := url.Parse(fixture.server.URL)
	if err != nil {
		t.Fatal(err)
	}
	remote, err := client.NewRemote(origin, &http.Client{Timeout: 10 * time.Second}, t.TempDir(), safetree.PrototypeLimits())
	if err != nil {
		t.Fatal(err)
	}
	installer, err := install.NewInstaller(remote)
	if err != nil {
		t.Fatal(err)
	}
	skill, err := registry.ParseSkillID("team/sample")
	if err != nil {
		t.Fatal(err)
	}
	return installer, skill
}

func TestReadOnlyAPIInstallsCurrentAndExactPublications(t *testing.T) {
	fixture := newWebFixture(t)
	firstDigestText := publishWebCandidate(t, fixture, "First network publication.\n")
	secondDigestText := publishWebCandidate(t, fixture, "Second network publication.\n")
	response := postForm(t, fixture, "/current", url.Values{"skill": {"team/sample"}, "digest": {secondDigestText}})
	_ = response.Body.Close()
	if response.StatusCode != http.StatusSeeOther {
		t.Fatalf("set current status = %d", response.StatusCode)
	}

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
	if locked.Publication().Tree().String() != secondDigestText {
		t.Fatalf("current install digest = %s", locked.Publication().Tree().String())
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
	if locked.Publication().Tree() != firstDigest {
		t.Fatalf("exact install digest = %s", locked.Publication().Tree().String())
	}
	exactSkill, err := os.ReadFile(filepath.Join(exactProject.SkillsDir(), "sample", "SKILL.md"))
	if err != nil || !bytes.Contains(exactSkill, []byte("First network publication.")) {
		t.Fatalf("exact SKILL.md = %q, %v", exactSkill, err)
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
	entries, err := os.ReadDir(filepath.Join(project.StateDir(), "operations"))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("no-change install left operations: %v", entries)
	}
	if !strings.Contains(string(afterLock), digestText) {
		t.Fatalf("lock does not contain %s", digestText)
	}
}
