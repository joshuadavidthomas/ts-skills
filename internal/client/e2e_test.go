package client

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"mime/multipart"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"testing"
	"time"

	"github.com/joshuadavidthomas/ts-skills/internal/protocol"
	"github.com/joshuadavidthomas/ts-skills/internal/registry"
	"github.com/joshuadavidthomas/ts-skills/internal/server"
)

func TestInstallThroughDevDaemon(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	started := make(chan net.Addr, 1)
	done := make(chan error, 1)
	go func() {
		done <- server.RunDev(ctx, server.DevConfig{StateDir: t.TempDir(), Listen: "127.0.0.1:0", Started: func(addr net.Addr) { started <- addr }})
	}()
	var addr net.Addr
	select {
	case addr = <-started:
	case err := <-done:
		t.Fatalf("start dev daemon: %v", err)
	case <-time.After(5 * time.Second):
		t.Fatal("dev daemon did not start")
	}
	defer func() {
		cancel()
		if err := <-done; err != nil {
			t.Errorf("stop dev daemon: %v", err)
		}
	}()

	base := "http://" + addr.String()
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	upload, err := client.Get(base + "/upload")
	if err != nil {
		t.Fatal(err)
	}
	token, cookie := upload.Header.Get("X-CSRF-Token"), upload.Cookies()[0]
	_ = upload.Body.Close()
	if token == "" {
		t.Fatal("upload page did not provide a CSRF token")
	}

	body := "---\nname: sample\ndescription: End-to-end test\n---\nInstall this through HTTP.\n"
	var form bytes.Buffer
	writer := multipart.NewWriter(&form)
	if err := writer.WriteField("namespace", "team"); err != nil {
		t.Fatal(err)
	}
	if err := writer.WriteField("manifest", `[{"index":0,"path":"sample/SKILL.md","size":`+strconv.Itoa(len(body))+`}]`); err != nil {
		t.Fatal(err)
	}
	part, err := writer.CreateFormFile("file-0", "SKILL.md")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := io.WriteString(part, body); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	request, err := http.NewRequest(http.MethodPost, base+"/candidates", &form)
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Content-Type", writer.FormDataContentType())
	request.Header.Set("X-CSRF-Token", token)
	request.AddCookie(cookie)
	created, err := client.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	_ = created.Body.Close()
	if created.StatusCode != http.StatusSeeOther {
		t.Fatalf("create candidate status = %d", created.StatusCode)
	}
	candidate := created.Header.Get("Location")

	review, err := client.Get(base + candidate)
	if err != nil {
		t.Fatal(err)
	}
	reviewBody, err := io.ReadAll(review.Body)
	_ = review.Body.Close()
	if err != nil {
		t.Fatal(err)
	}
	digest := regexp.MustCompile(`sha256:[0-9a-f]{64}`).FindString(string(reviewBody))
	if digest == "" {
		t.Fatal("review page did not show a digest")
	}
	for _, endpoint := range []struct {
		path string
		form string
	}{
		{candidate + "/publish", ""},
		{"/current", "skill=team%2Fsample&digest=" + digest},
	} {
		req, err := http.NewRequest(http.MethodPost, base+endpoint.path, bytes.NewBufferString(endpoint.form))
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("X-CSRF-Token", token)
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		req.AddCookie(cookie)
		response, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		_ = response.Body.Close()
		if response.StatusCode != http.StatusSeeOther {
			t.Fatalf("POST %s status = %d", endpoint.path, response.StatusCode)
		}
	}

	project := t.TempDir()
	config := filepath.Join(project, "config.toml")
	if err := os.WriteFile(config, []byte("registry = \""+base+"\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	if err := Run(context.Background(), []string{"install", "--project", project, "--config", config, "team/sample"}, &stdout, &stderr); err != nil {
		t.Fatalf("install: %v; stderr=%s", err, stderr.String())
	}
	installed, err := registry.SumTree(context.Background(), os.DirFS(filepath.Join(project, ".agents", "skills", "sample")), ".")
	if err != nil {
		t.Fatal(err)
	}
	if installed.String() != digest {
		t.Fatalf("installed digest = %s, want %s", installed, digest)
	}
	var current protocol.CurrentResponse
	response, err := client.Get(base + "/api/" + protocol.Version + "/skills/team/sample/current")
	if err != nil {
		t.Fatal(err)
	}
	if err := json.NewDecoder(response.Body).Decode(&current); err != nil {
		t.Fatal(err)
	}
	_ = response.Body.Close()
	if current.Digest != digest {
		t.Fatalf("current digest = %s, want %s", current.Digest, digest)
	}
}
