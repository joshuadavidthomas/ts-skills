package main

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/joshuadavidthomas/ts-skills/internal/registry"
	"github.com/joshuadavidthomas/ts-skills/internal/safetree"
)

func TestCommandInstallerReportsConstructorAndCleanupFailure(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(configPath, []byte("registry = \"http://127.0.0.1:9\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	constructErr := errors.New("injected registry client construction failure")
	newClientRemote = func(origin, *http.Client, string, safetree.Limits) (*remote, error) {
		return nil, constructErr
	}
	t.Cleanup(func() { newClientRemote = newRemote })
	cleanupErr := errors.New("injected staging removal failure")
	removeClientStaging = func(string) error { return cleanupErr }
	t.Cleanup(func() { removeClientStaging = os.RemoveAll })
	_, _, _, err := commandInstaller(configPath, t.TempDir())
	if !errors.Is(err, constructErr) {
		t.Fatalf("commandInstaller error = %v, want construction failure", err)
	}
	if !errors.Is(err, cleanupErr) {
		t.Fatalf("commandInstaller error = %v, want staging cleanup failure", err)
	}
}

func TestRunInstallsCurrentAndRestoresLockedTree(t *testing.T) {
	files := fstest.MapFS{
		"SKILL.md":  {Data: []byte("---\nname: sample\ndescription: CLI test\n---\nInstructions.\n")},
		"asset.txt": {Data: []byte("asset")},
	}
	digest, err := registry.SumTree(context.Background(), files, ".")
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
			_ = json.NewEncoder(w).Encode(currentResponse{Namespace: "team", Name: "sample", Digest: digest.String()})
			return
		}
		w.Header().Set("Content-Type", "application/zip")
		w.Header().Set(headerPublicationNamespace, "team")
		w.Header().Set(headerPublicationName, "sample")
		w.Header().Set(headerPublicationDigest, digest.String())
		_, _ = w.Write(archive.Bytes())
	}))
	defer server.Close()

	configPath := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(configPath, []byte("registry = \""+server.URL+"\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	project := t.TempDir()
	var stdout, stderr bytes.Buffer
	if err := run(context.Background(), []string{"install", "--project", project, "--config", configPath, "team/sample"}, &stdout, &stderr); err != nil {
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
	if err := run(context.Background(), []string{"restore", "--project", project, "--config", configPath}, &stdout, &stderr); err != nil {
		t.Fatalf("restore: %v; stderr=%s", err, stderr.String())
	}
	if contents, err := os.ReadFile(filepath.Join(destination, "asset.txt")); err != nil || string(contents) != "asset" {
		t.Fatalf("restored asset = %q, %v", contents, err)
	}
}

func TestRunRejectsMissingProjectAndInvalidDigest(t *testing.T) {
	streams := func() (*bytes.Buffer, *bytes.Buffer) { return &bytes.Buffer{}, &bytes.Buffer{} }
	stdout, stderr := streams()
	if err := run(context.Background(), []string{"install", "team/sample"}, stdout, stderr); err == nil || !strings.Contains(err.Error(), "--project") {
		t.Fatalf("missing project error = %v", err)
	}
	stdout, stderr = streams()
	if err := run(context.Background(), []string{"install", "--project", t.TempDir(), "--digest", "bad", "team/sample"}, stdout, stderr); err == nil || !strings.Contains(err.Error(), "--digest") {
		t.Fatalf("invalid digest error = %v", err)
	}
}

func TestRunHelpExitsCleanlyWithUsageOnStderr(t *testing.T) {
	for _, command := range []string{"install", "restore"} {
		var stdout, stderr bytes.Buffer
		if err := run(context.Background(), []string{command, "-h"}, &stdout, &stderr); err != nil {
			t.Fatalf("Run(%q -h) = %v, want nil", command, err)
		}
		if !strings.Contains(stderr.String(), "Usage of ts-skills "+command) {
			t.Fatalf("Run(%q -h) stderr = %q, want usage text", command, stderr.String())
		}
		if stdout.String() != "" {
			t.Fatalf("Run(%q -h) stdout = %q, want empty", command, stdout.String())
		}
	}
}

func TestRunReportsUnknownFlagOnce(t *testing.T) {
	for _, command := range []string{"install", "restore"} {
		var stdout, stderr bytes.Buffer
		err := run(context.Background(), []string{command, "--bogus"}, &stdout, &stderr)
		if err == nil {
			t.Fatalf("Run(%q --bogus) = nil, want error", command)
		}
		if !alreadyReported(err) {
			t.Fatalf("Run(%q --bogus) error = %v, want AlreadyReported", command, err)
		}
		if !strings.Contains(stderr.String(), "flag provided but not defined") {
			t.Fatalf("Run(%q --bogus) stderr = %q, want flag diagnostics", command, stderr.String())
		}
		if got := strings.Count(stderr.String(), err.Error()); got != 1 {
			t.Fatalf("Run(%q --bogus) stderr = %q prints the error %d times", command, stderr.String(), got)
		}
	}
}

func TestAlreadyReportedRejectsUnreportedErrors(t *testing.T) {
	if alreadyReported(errors.New("plain")) {
		t.Fatal("alreadyReported(plain error) = true, want false")
	}
	wrapped := fmt.Errorf("outer: %w", reportedError{errors.New("inner")})
	if !alreadyReported(wrapped) {
		t.Fatal("alreadyReported(wrapped reportedError) = false, want true")
	}
}

func TestCommandErrorMapsRegistryFailureClasses(t *testing.T) {
	tests := map[string]struct {
		err  error
		want string
	}{
		"busy":              {errBusy, "cannot install while another ts-skills process is changing this project; wait and try again"},
		"local changes":     {errLocalChanges, "cannot install because the installed skill differs from ts-skills.lock; restore it or move it aside, then try again"},
		"project changed":   {errProjectChanged, "cannot install because this project changed while the registry was being read; try again"},
		"identity mismatch": {errIdentityMismatch, "cannot install because the registry response did not match the requested skill"},
		"digest mismatch":   {errDigestMismatch, "cannot install because the registry response did not match the requested skill"},
		"not found":         {errNotFound, "cannot install because the requested skill publication was not found"},
		"tree limit":        {safetree.ErrLimitExceeded, "cannot install because the downloaded skill exceeds the configured safety limits"},
		"protocol":          {errProtocol, "cannot install because the registry returned an invalid response"},
		"invalid request":   {errInvalidRequest, "cannot install because the registry rejected the request as invalid"},
		"internal error":    {errInternal, "cannot install because the registry could not complete the request"},
		"other":             {errors.New("other"), "install failed"},
	}
	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			err := fmt.Errorf("resolve current publication: %w", test.err)
			got := commandError("install", err)
			if !strings.Contains(got.Error(), test.want) {
				t.Fatalf("commandError = %q, want wording %q", got.Error(), test.want)
			}
			if !errors.Is(got, test.err) {
				t.Fatalf("commandError = %v, want errors.Is %v", got, test.err)
			}
		})
	}
}

func TestRunVersionReportsBuildVersion(t *testing.T) {
	for _, command := range []string{"version", "--version"} {
		var stdout, stderr bytes.Buffer
		if err := run(context.Background(), []string{command}, &stdout, &stderr); err != nil {
			t.Fatalf("Run(%q) = %v", command, err)
		}
		if got := stdout.String(); got != "ts-skills dev\n" {
			t.Fatalf("Run(%q) output = %q", command, got)
		}
	}
}
