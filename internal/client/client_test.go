package client

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"testing"
	"testing/fstest"
	"time"

	"github.com/joshuadavidthomas/ts-skill-registry/internal/agentskill"
	"github.com/joshuadavidthomas/ts-skill-registry/internal/install"
	"github.com/joshuadavidthomas/ts-skill-registry/internal/protocol"
	"github.com/joshuadavidthomas/ts-skill-registry/internal/registry"
	"github.com/joshuadavidthomas/ts-skill-registry/internal/safetree"
)

func clientSkill(t *testing.T) registry.SkillID {
	t.Helper()
	skill, err := registry.ParseSkillID("team/sample")
	if err != nil {
		t.Fatal(err)
	}
	return skill
}

func clientTree(t *testing.T, body string) (agentskill.TreeDigest, []byte) {
	t.Helper()
	files := fstest.MapFS{
		"SKILL.md":        {Data: []byte("---\nname: sample\ndescription: Client test\n---\n" + body)},
		"assets/data.txt": {Data: []byte("asset")},
	}
	digest, err := agentskill.SumTree(files, ".")
	if err != nil {
		t.Fatal(err)
	}
	var archive bytes.Buffer
	writer := zip.NewWriter(&archive)
	for _, name := range []string{"SKILL.md", "assets/data.txt"} {
		header := &zip.FileHeader{Name: name, Method: zip.Store}
		header.SetMode(0o644)
		entry, err := writer.CreateHeader(header)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := entry.Write(files[name].Data); err != nil {
			t.Fatal(err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	return digest, archive.Bytes()
}

func remoteForServer(t *testing.T, server *httptest.Server) *Remote {
	t.Helper()
	origin, err := url.Parse(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	remote, err := NewRemote(origin, &http.Client{Timeout: 5 * time.Second}, t.TempDir(), safetree.PrototypeLimits())
	if err != nil {
		t.Fatal(err)
	}
	return remote
}

func TestMismatchedTreeLeavesInstalledDestinationAndLockUnchanged(t *testing.T) {
	skill := clientSkill(t)
	firstDigest, firstZIP := clientTree(t, "first")
	secondDigest, _ := clientTree(t, "second")
	currentDigest := firstDigest
	treeZIP := firstZIP
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v1/skills/team/sample/current" {
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(protocol.CurrentResponse{Namespace: "team", Name: "sample", Digest: currentDigest.String()})
			return
		}
		w.Header().Set("Content-Type", "application/zip")
		_, _ = w.Write(treeZIP)
	}))
	defer server.Close()
	installer, err := install.NewInstaller(remoteForServer(t, server))
	if err != nil {
		t.Fatal(err)
	}
	project, _ := install.OpenProject(t.TempDir())
	requirement, _ := install.Current(skill)
	if _, err := installer.Install(context.Background(), project, requirement); err != nil {
		t.Fatal(err)
	}
	beforeTree, _ := os.ReadFile(filepath.Join(project.SkillsDir(), "sample", "SKILL.md"))
	beforeLock, _ := os.ReadFile(project.LockPath())

	currentDigest = secondDigest
	if _, err := installer.Install(context.Background(), project, requirement); !errors.Is(err, install.ErrDigestMismatch) {
		t.Fatalf("mismatched install error = %v", err)
	}
	afterTree, _ := os.ReadFile(filepath.Join(project.SkillsDir(), "sample", "SKILL.md"))
	afterLock, _ := os.ReadFile(project.LockPath())
	if !bytes.Equal(beforeTree, afterTree) || !bytes.Equal(beforeLock, afterLock) {
		t.Fatal("mismatched response changed the installed destination or lock")
	}
}

func TestRemoteRejectsRedirectsContentTypeSizeAndUnsafeZIP(t *testing.T) {
	skill := clientSkill(t)
	digest, validZIP := clientTree(t, "valid")
	requirement, _ := install.Exact(skill, digest)
	var unsafe bytes.Buffer
	unsafeWriter := zip.NewWriter(&unsafe)
	entry, _ := unsafeWriter.Create("../SKILL.md")
	_, _ = entry.Write([]byte("unsafe"))
	_ = unsafeWriter.Close()

	tests := []struct {
		name        string
		handler     http.Handler
		expectedErr error
	}{
		{
			name: "redirect",
			handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				http.Redirect(w, r, "/elsewhere", http.StatusFound)
			}),
		},
		{
			name: "content type",
			handler: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "application/octet-stream")
				_, _ = w.Write(validZIP)
			}),
			expectedErr: protocol.ErrProtocol,
		},
		{
			name: "declared size",
			handler: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "application/zip")
				w.Header().Set("Content-Length", fmt.Sprint(int64(1)<<40))
				w.WriteHeader(http.StatusOK)
			}),
			expectedErr: safetree.ErrLimitExceeded,
		},
		{
			name: "unsafe path",
			handler: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "application/zip")
				_, _ = w.Write(unsafe.Bytes())
			}),
			expectedErr: protocol.ErrProtocol,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(test.handler)
			defer server.Close()
			_, err := remoteForServer(t, server).Fetch(context.Background(), requirement)
			if err == nil {
				t.Fatal("invalid response was accepted")
			}
			if test.expectedErr != nil && !errors.Is(err, test.expectedErr) {
				t.Fatalf("error = %v, want %v", err, test.expectedErr)
			}
		})
	}
}

func TestRemoteRejectsCurrentResponseForAnotherSkill(t *testing.T) {
	digest, _ := clientTree(t, "valid")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(protocol.CurrentResponse{Namespace: "other", Name: "sample", Digest: digest.String()})
	}))
	defer server.Close()
	requirement, _ := install.Current(clientSkill(t))
	_, err := remoteForServer(t, server).Fetch(context.Background(), requirement)
	if !errors.Is(err, install.ErrIdentityMismatch) {
		t.Fatalf("identity mismatch error = %v", err)
	}
}
