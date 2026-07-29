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

func TestInstallUpgradeReplacesDestination(t *testing.T) {
	installer, project, requirement, update := testInstaller(t, "old")
	if _, err := installer.Install(context.Background(), project, requirement); err != nil {
		t.Fatal(err)
	}
	update("new")
	if _, err := installer.Install(context.Background(), project, requirement); err != nil {
		t.Fatal(err)
	}
	contents, err := os.ReadFile(filepath.Join(project.SkillsDir(), "sample", "assets", "data.txt"))
	if err != nil || string(contents) != "asset" {
		t.Fatalf("installed contents = %q, %v", contents, err)
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
	t.Cleanup(func() { _ = writer.close() })
	for _, path := range []string{stage, trash} {
		if _, err := os.Stat(path); !errors.Is(err, fs.ErrNotExist) {
			t.Fatalf("litter remains %q: %v", path, err)
		}
	}
	if _, err := os.Stat(real); err != nil {
		t.Fatalf("real skill removed: %v", err)
	}
}
