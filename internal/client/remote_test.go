package client

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"
	"time"
	"unicode"

	"github.com/joshuadavidthomas/ts-skills/internal/protocol"
	"github.com/joshuadavidthomas/ts-skills/internal/registry"
	"github.com/joshuadavidthomas/ts-skills/internal/tree"
)

func clientSkill(t *testing.T) registry.SkillID {
	t.Helper()
	skill, err := registry.ParseSkillID("team/sample")
	if err != nil {
		t.Fatal(err)
	}
	return skill
}

func clientTree(t *testing.T, body string) (registry.TreeDigest, []byte) {
	t.Helper()
	files := fstest.MapFS{
		"SKILL.md":        {Data: []byte("---\nname: sample\ndescription: Client test\n---\n" + body)},
		"assets/data.txt": {Data: []byte("asset")},
	}
	digest, err := registry.SumTree(context.Background(), files, ".")
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

func remoteForServer(t *testing.T, server *httptest.Server) *remote {
	return remoteForServerWithLimits(t, server, tree.PrototypeLimits())
}

func remoteForServerWithLimits(t *testing.T, server *httptest.Server, limits tree.Limits) *remote {
	t.Helper()
	origin, err := parseOrigin(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	remote, err := newRemote(origin, &http.Client{Timeout: 5 * time.Second}, t.TempDir(), limits)
	if err != nil {
		t.Fatal(err)
	}
	return remote
}

func TestParseOrigin(t *testing.T) {
	valid := map[string]string{
		"HTTPS":          "https://registry.example.ts.net",
		"HTTPS slash":    "https://registry.example.ts.net/",
		"localhost HTTP": "http://localhost:8080",
		"IPv4 loopback":  "http://127.0.0.1:8080",
		"IPv6 loopback":  "http://[::1]:8080",
	}
	for name, text := range valid {
		t.Run(name, func(t *testing.T) {
			origin, err := parseOrigin(text)
			if err != nil {
				t.Fatalf("ParseOrigin(%q): %v", text, err)
			}
			if got := origin.asURL().String(); got != strings.TrimSuffix(text, "/") {
				t.Fatalf("origin.URL().String() = %q, want %q", got, strings.TrimSuffix(text, "/"))
			}
		})
	}

	invalid := []string{
		"",
		"/relative",
		"ftp://registry.example.ts.net",
		"https://",
		"mailto:registry@example.ts.net",
		"http://registry.example.ts.net",
		"https://user@registry.example.ts.net",
		"https://registry.example.ts.net/path",
		"https://registry.example.ts.net?query=yes",
		"https://registry.example.ts.net?",
		"https://registry.example.ts.net#fragment",
	}
	for _, text := range invalid {
		t.Run("reject "+text, func(t *testing.T) {
			if _, err := parseOrigin(text); err == nil {
				t.Fatalf("ParseOrigin(%q) succeeded", text)
			}
		})
	}
}

func TestOriginURLReturnsFreshCopy(t *testing.T) {
	origin, err := parseOrigin("https://registry.example.ts.net")
	if err != nil {
		t.Fatal(err)
	}
	url := origin.asURL()
	url.Path = "/changed"
	if got := origin.asURL().String(); got != "https://registry.example.ts.net" {
		t.Fatalf("origin.URL().String() after URL mutation = %q", got)
	}
}

func setClientTreeHeaders(header http.Header, namespace, name, digest string) {
	header.Set(protocol.HeaderPublicationNamespace, namespace)
	header.Set(protocol.HeaderPublicationName, name)
	header.Set(protocol.HeaderPublicationDigest, digest)
}

func TestRemoteFetchEnforcesTreeArchiveCeiling(t *testing.T) {
	limits := tree.Limits{
		MaxFiles: 2, MaxPathBytes: 16, MaxDepth: 2, MaxFileBytes: 80, MaxExpandedBytes: 100,
	}
	digest, archive := clientTree(t, "archive ceiling")
	maximum, err := tree.MaxArchiveBytes(limits)
	if err != nil {
		t.Fatal(err)
	}
	responseBody := archive
	oversized := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/zip")
		setClientTreeHeaders(w.Header(), "team", "sample", digest.String())
		if oversized {
			w.Header().Set("Content-Length", fmt.Sprint(maximum+1))
		}
		_, _ = w.Write(responseBody)
	}))
	defer server.Close()
	remote := remoteForServerWithLimits(t, server, limits)
	requirement, err := exact(clientSkill(t), digest)
	if err != nil {
		t.Fatal(err)
	}
	fetched, err := remote.fetch(context.Background(), requirement)
	if err != nil {
		t.Fatalf("fetch tree archive: %v", err)
	}
	if err := fetched.tree.Close(); err != nil {
		t.Fatal(err)
	}

	oversized = true
	if _, err := remote.fetch(context.Background(), requirement); !errors.Is(err, tree.ErrLimitExceeded) {
		t.Fatalf("oversize tree archive error = %v, want %v", err, tree.ErrLimitExceeded)
	}
}

func TestRemoteReturnsProtocolFailures(t *testing.T) {
	tests := map[string]struct {
		code protocol.Code
	}{
		"not-found":       {protocol.CodeNotFound},
		"invalid-request": {protocol.CodeInvalidRequest},
		"too-large":       {protocol.CodeTooLarge},
		"internal":        {protocol.CodeInternal},
		"unavailable":     {protocol.CodeUnavailable},
	}
	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			status, known := protocol.StatusForCode(test.code)
			if !known {
				t.Fatalf("test code %q has no wire status", test.code)
			}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(status)
				_ = json.NewEncoder(w).Encode(protocol.ErrorResponse{Code: test.code, Message: "error mapping test"})
			}))
			defer server.Close()
			remote := remoteForServer(t, server)
			digest, _ := clientTree(t, "error mapping")
			requirement, err := exact(clientSkill(t), digest)
			if err != nil {
				t.Fatal(err)
			}
			_, err = remote.fetch(context.Background(), requirement)
			var failure *protocol.Failure
			if !errors.As(err, &failure) || failure.Code != test.code {
				t.Fatalf("Fetch error = %v, want protocol failure %q", err, test.code)
			}
			if test.code == protocol.CodeTooLarge && errors.Is(err, tree.ErrLimitExceeded) {
				t.Fatalf("remote too-large error = %v, must not be a local tree limit", err)
			}
		})
	}
}

func TestResponseErrorSanitizesServerMessage(t *testing.T) {
	messageError := func(t *testing.T, code protocol.Code, message string) error {
		t.Helper()
		status, ok := protocol.StatusForCode(code)
		if !ok {
			t.Fatalf("unknown code %q", code)
		}
		body, err := json.Marshal(protocol.ErrorResponse{Code: code, Message: message})
		if err != nil {
			t.Fatal(err)
		}
		return protocol.ReadFailure(&http.Response{
			StatusCode:    status,
			Header:        http.Header{"Content-Type": {"application/json"}},
			Body:          io.NopCloser(bytes.NewReader(body)),
			ContentLength: int64(len(body)),
		})
	}

	for name, test := range map[string]struct {
		code          protocol.Code
		message, want string
	}{
		"control characters fall back":    {protocol.CodeInvalidRequest, "bad\x1b]0;title", "registry rejected the request"},
		"long message falls back":         {protocol.CodeInternal, strings.Repeat("x", 201), "registry encountered an internal error"},
		"ordinary message passes through": {protocol.CodeInvalidRequest, "manifest is invalid", "manifest is invalid"},
	} {
		t.Run(name, func(t *testing.T) {
			err := messageError(t, test.code, test.message)
			if !strings.Contains(err.Error(), test.want) {
				t.Fatalf("response error = %q, want %q", err, test.want)
			}
			for _, r := range err.Error() {
				if unicode.IsControl(r) {
					t.Fatalf("response error contains control character: %q", err)
				}
			}
		})
	}
}

func TestRemoteFetchMapsInvalidTreeArchiveToProtocolError(t *testing.T) {
	digest, _ := clientTree(t, "archive format")
	var deflated bytes.Buffer
	writer := zip.NewWriter(&deflated)
	deflatedFiles := map[string][]byte{
		"SKILL.md":        []byte("---\nname: sample\ndescription: Client test\n---\narchive format"),
		"assets/data.txt": []byte("asset"),
	}
	for _, name := range []string{"SKILL.md", "assets/data.txt"} {
		entry, err := writer.CreateHeader(&zip.FileHeader{Name: name, Method: zip.Deflate})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := entry.Write(deflatedFiles[name]); err != nil {
			t.Fatal(err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/zip")
		setClientTreeHeaders(w.Header(), "team", "sample", digest.String())
		_, _ = w.Write(deflated.Bytes())
	}))
	defer server.Close()
	requirement, err := exact(clientSkill(t), digest)
	if err != nil {
		t.Fatal(err)
	}
	_, err = remoteForServer(t, server).fetch(context.Background(), requirement)
	if !errors.Is(err, protocol.ErrInvalidResponse) {
		t.Fatalf("Fetch error = %v, want errors.Is %v", err, protocol.ErrInvalidResponse)
	}
	if errors.Is(err, tree.ErrLimitExceeded) {
		t.Fatalf("Fetch error = %v, want format rejection", err)
	}
}

func TestMismatchedTreeLeavesInstalledDestinationAndLockUnchanged(t *testing.T) {
	skill := clientSkill(t)
	firstDigest, firstZIP := clientTree(t, "first")
	secondDigest, _ := clientTree(t, "second")
	currentDigest := firstDigest
	treeZIP := firstZIP
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/"+protocol.Version+"/skills/team/sample/current" {
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(protocol.CurrentResponse{Namespace: "team", Name: "sample", Digest: currentDigest.String()})
			return
		}
		w.Header().Set("Content-Type", "application/zip")
		setClientTreeHeaders(w.Header(), "team", "sample", currentDigest.String())
		_, _ = w.Write(treeZIP)
	}))
	defer server.Close()
	installer := &installer{remote: remoteForServer(t, server)}
	project, _ := openProject(t.TempDir())
	requirement, _ := current(skill)
	if _, err := installer.install(context.Background(), project, requirement); err != nil {
		t.Fatal(err)
	}
	beforeTree, err := os.ReadFile(filepath.Join(project.skillsDir(), "sample", "SKILL.md"))
	if err != nil {
		t.Fatalf("read installed baseline tree: %v", err)
	}
	beforeLock, err := os.ReadFile(project.lockPath())
	if err != nil {
		t.Fatalf("read installed baseline lock: %v", err)
	}

	currentDigest = secondDigest
	if _, err := installer.install(context.Background(), project, requirement); !errors.Is(err, protocol.ErrInvalidResponse) {
		t.Fatalf("mismatched install error = %v", err)
	}
	afterTree, err := os.ReadFile(filepath.Join(project.skillsDir(), "sample", "SKILL.md"))
	if err != nil {
		t.Fatalf("read tree after rejected install: %v", err)
	}
	afterLock, err := os.ReadFile(project.lockPath())
	if err != nil {
		t.Fatalf("read lock after rejected install: %v", err)
	}
	if !bytes.Equal(beforeTree, afterTree) || !bytes.Equal(beforeLock, afterLock) {
		t.Fatal("mismatched response changed the installed destination or lock")
	}
}

func TestTruncatedTreeUpdateLeavesInstalledDestinationAndLockUnchanged(t *testing.T) {
	skill := clientSkill(t)
	firstDigest, firstZIP := clientTree(t, "first complete tree")
	secondDigest, secondZIP := clientTree(t, "second truncated tree")
	currentDigest := firstDigest
	treeZIP := firstZIP
	truncateTree := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/"+protocol.Version+"/skills/team/sample/current" {
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(protocol.CurrentResponse{
				Namespace: "team", Name: "sample", Digest: currentDigest.String(),
			})
			return
		}
		w.Header().Set("Content-Type", "application/zip")
		setClientTreeHeaders(w.Header(), "team", "sample", currentDigest.String())
		if truncateTree {
			w.Header().Set("Content-Length", fmt.Sprint(len(treeZIP)))
			_, _ = w.Write(treeZIP[:len(treeZIP)/2])
			return
		}
		_, _ = w.Write(treeZIP)
	}))
	defer server.Close()

	installer := &installer{remote: remoteForServer(t, server)}
	project, err := openProject(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	requirement, err := current(skill)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := installer.install(context.Background(), project, requirement); err != nil {
		t.Fatal(err)
	}
	documentPath := filepath.Join(project.skillsDir(), "sample", "SKILL.md")
	assetPath := filepath.Join(project.skillsDir(), "sample", "assets", "data.txt")
	documentBefore, err := os.ReadFile(documentPath)
	if err != nil {
		t.Fatal(err)
	}
	assetBefore, err := os.ReadFile(assetPath)
	if err != nil {
		t.Fatal(err)
	}
	lockBefore, err := os.ReadFile(project.lockPath())
	if err != nil {
		t.Fatal(err)
	}

	currentDigest = secondDigest
	treeZIP = secondZIP
	truncateTree = true
	if _, err := installer.install(context.Background(), project, requirement); !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Fatalf("truncated update error = %v, want io.ErrUnexpectedEOF", err)
	}
	documentAfter, err := os.ReadFile(documentPath)
	if err != nil {
		t.Fatal(err)
	}
	assetAfter, err := os.ReadFile(assetPath)
	if err != nil {
		t.Fatal(err)
	}
	lockAfter, err := os.ReadFile(project.lockPath())
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(documentAfter, documentBefore) {
		t.Fatalf("installed SKILL.md changed from %q to %q", documentBefore, documentAfter)
	}
	if !bytes.Equal(assetAfter, assetBefore) {
		t.Fatalf("installed asset changed from %q to %q", assetBefore, assetAfter)
	}
	if !bytes.Equal(lockAfter, lockBefore) {
		t.Fatal("truncated update changed the project lock")
	}
}

func TestRemoteRejectsRedirectsContentTypeSizeAndUnsafeZIP(t *testing.T) {
	skill := clientSkill(t)
	digest, validZIP := clientTree(t, "valid")
	requirement, _ := exact(skill, digest)
	var unsafe bytes.Buffer
	unsafeWriter := zip.NewWriter(&unsafe)
	entry, _ := unsafeWriter.CreateHeader(&zip.FileHeader{Name: "../SKILL.md", Method: zip.Store})
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
				setClientTreeHeaders(w.Header(), "team", "sample", digest.String())
				_, _ = w.Write(validZIP)
			}),
			expectedErr: protocol.ErrInvalidResponse,
		},
		{
			name: "declared size",
			handler: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "application/zip")
				w.Header().Set("Content-Length", fmt.Sprint(int64(1)<<40))
				setClientTreeHeaders(w.Header(), "team", "sample", digest.String())
				w.WriteHeader(http.StatusOK)
			}),
			expectedErr: tree.ErrLimitExceeded,
		},
		{
			name: "unsafe path",
			handler: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "application/zip")
				setClientTreeHeaders(w.Header(), "team", "sample", digest.String())
				_, _ = w.Write(unsafe.Bytes())
			}),
			expectedErr: protocol.ErrInvalidResponse,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(test.handler)
			defer server.Close()
			_, err := remoteForServer(t, server).fetch(context.Background(), requirement)
			if err == nil {
				t.Fatal("invalid response was accepted")
			}
			if test.expectedErr != nil && !errors.Is(err, test.expectedErr) {
				t.Fatalf("error = %v, want %v", err, test.expectedErr)
			}
		})
	}
}

func TestRemoteBindsExactAndCurrentFetchesToTreeResponseIdentity(t *testing.T) {
	skill := clientSkill(t)
	digest, archive := clientTree(t, "valid")
	otherDigest, _ := clientTree(t, "another publication")

	tests := []struct {
		name        string
		change      func(http.Header)
		expectedErr error
	}{
		{name: "success"},
		{
			name: "missing namespace",
			change: func(header http.Header) {
				header.Del(protocol.HeaderPublicationNamespace)
			},
			expectedErr: protocol.ErrInvalidResponse,
		},
		{
			name: "wrong namespace",
			change: func(header http.Header) {
				header.Set(protocol.HeaderPublicationNamespace, "other")
			},
			expectedErr: protocol.ErrInvalidResponse,
		},
		{
			name: "missing name",
			change: func(header http.Header) {
				header.Del(protocol.HeaderPublicationName)
			},
			expectedErr: protocol.ErrInvalidResponse,
		},
		{
			name: "wrong name",
			change: func(header http.Header) {
				header.Set(protocol.HeaderPublicationName, "other")
			},
			expectedErr: protocol.ErrInvalidResponse,
		},
		{
			name: "missing digest",
			change: func(header http.Header) {
				header.Del(protocol.HeaderPublicationDigest)
			},
			expectedErr: protocol.ErrInvalidResponse,
		},
		{
			name: "wrong digest",
			change: func(header http.Header) {
				header.Set(protocol.HeaderPublicationDigest, otherDigest.String())
			},
			expectedErr: protocol.ErrInvalidResponse,
		},
	}

	for _, mode := range []string{"exact", "current"} {
		for _, test := range tests {
			t.Run(mode+"/"+test.name, func(t *testing.T) {
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					if r.URL.Path == "/api/"+protocol.Version+"/skills/team/sample/current" {
						w.Header().Set("Content-Type", "application/json")
						_ = json.NewEncoder(w).Encode(protocol.CurrentResponse{
							Namespace: "team", Name: "sample", Digest: digest.String(),
						})
						return
					}
					w.Header().Set("Content-Type", "application/zip")
					setClientTreeHeaders(w.Header(), "team", "sample", digest.String())
					if test.change != nil {
						test.change(w.Header())
					}
					_, _ = w.Write(archive)
				}))
				defer server.Close()

				var requirement requirement
				var err error
				if mode == "exact" {
					requirement, err = exact(skill, digest)
				} else {
					requirement, err = current(skill)
				}
				if err != nil {
					t.Fatal(err)
				}
				fetched, err := remoteForServer(t, server).fetch(context.Background(), requirement)
				if test.expectedErr != nil {
					if !errors.Is(err, test.expectedErr) {
						t.Fatalf("fetch error = %v, want %v", err, test.expectedErr)
					}
					return
				}
				if err != nil {
					t.Fatal(err)
				}
				if fetched.publication.Skill() != skill || fetched.publication.Tree() != digest {
					t.Fatalf("fetched publication = %s@%s", fetched.publication.Skill().String(), fetched.publication.Tree().String())
				}
				if contents, err := fs.ReadFile(fetched.tree, "assets/data.txt"); err != nil || string(contents) != "asset" {
					t.Fatalf("rootless fetched asset = %q, %v", contents, err)
				}
				if err := fetched.tree.Close(); err != nil {
					t.Fatal(err)
				}
			})
		}
	}
}

func TestRemoteRejectsCurrentResponseForAnotherSkill(t *testing.T) {
	digest, _ := clientTree(t, "valid")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(protocol.CurrentResponse{Namespace: "other", Name: "sample", Digest: digest.String()})
	}))
	defer server.Close()
	requirement, _ := current(clientSkill(t))
	_, err := remoteForServer(t, server).fetch(context.Background(), requirement)
	if !errors.Is(err, protocol.ErrInvalidResponse) {
		t.Fatalf("identity mismatch error = %v", err)
	}
}
