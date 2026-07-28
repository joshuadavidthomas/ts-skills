package cli

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/joshuadavidthomas/ts-skill-registry/internal/agentskill"
	"github.com/joshuadavidthomas/ts-skill-registry/internal/protocol"
)

func TestRunInstallsCurrentAndRestoresLockedTree(t *testing.T) {
	files := fstest.MapFS{
		"SKILL.md":  {Data: []byte("---\nname: sample\ndescription: CLI test\n---\nInstructions.\n")},
		"asset.txt": {Data: []byte("asset")},
	}
	digest, err := agentskill.SumTree(files, ".")
	if err != nil {
		t.Fatal(err)
	}
	var archive bytes.Buffer
	writer := zip.NewWriter(&archive)
	for _, name := range []string{"SKILL.md", "asset.txt"} {
		header := &zip.FileHeader{Name: name, Method: zip.Store}
		header.SetMode(0o644)
		entry, createErr := writer.CreateHeader(header)
		if createErr != nil {
			t.Fatal(createErr)
		}
		_, _ = entry.Write(files[name].Data)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/current") {
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(protocol.CurrentResponse{Namespace: "team", Name: "sample", Digest: digest.String()})
			return
		}
		w.Header().Set("Content-Type", "application/zip")
		w.Header().Set(protocol.HeaderPublicationNamespace, "team")
		w.Header().Set(protocol.HeaderPublicationName, "sample")
		w.Header().Set(protocol.HeaderPublicationDigest, digest.String())
		_, _ = w.Write(archive.Bytes())
	}))
	defer server.Close()

	configPath := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(configPath, []byte("registry = \""+server.URL+"\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	project := t.TempDir()
	var stdout, stderr bytes.Buffer
	if err := Run(context.Background(), []string{"install", "--project", project, "--config", configPath, "team/sample"}, strings.NewReader(""), &stdout, &stderr); err != nil {
		t.Fatalf("install: %v; stderr=%s", err, stderr.String())
	}
	if !strings.Contains(stdout.String(), digest.String()) {
		t.Fatalf("install output = %q", stdout.String())
	}
	destination := filepath.Join(project, ".agents", "skills", "sample")
	if err := os.RemoveAll(destination); err != nil {
		t.Fatal(err)
	}
	stdout.Reset()
	stderr.Reset()
	if err := Run(context.Background(), []string{"restore", "--project", project, "--config", configPath}, strings.NewReader(""), &stdout, &stderr); err != nil {
		t.Fatalf("restore: %v; stderr=%s", err, stderr.String())
	}
	if contents, err := os.ReadFile(filepath.Join(destination, "asset.txt")); err != nil || string(contents) != "asset" {
		t.Fatalf("restored asset = %q, %v", contents, err)
	}
}

func TestRunRejectsMissingProjectAndInvalidDigest(t *testing.T) {
	streams := func() (*bytes.Buffer, *bytes.Buffer) { return &bytes.Buffer{}, &bytes.Buffer{} }
	stdout, stderr := streams()
	if err := Run(context.Background(), []string{"install", "team/sample"}, strings.NewReader(""), stdout, stderr); err == nil || !strings.Contains(err.Error(), "--project") {
		t.Fatalf("missing project error = %v", err)
	}
	stdout, stderr = streams()
	if err := Run(context.Background(), []string{"install", "--project", t.TempDir(), "--digest", "bad", "team/sample"}, strings.NewReader(""), stdout, stderr); err == nil || !strings.Contains(err.Error(), "--digest") {
		t.Fatalf("invalid digest error = %v", err)
	}
}
