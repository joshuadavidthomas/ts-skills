package client

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"testing"
	"testing/fstest"
	"time"

	"github.com/joshuadavidthomas/ts-skills/internal/agentskill"
	"github.com/joshuadavidthomas/ts-skills/internal/install"
	"github.com/joshuadavidthomas/ts-skills/internal/protocol"
	"github.com/joshuadavidthomas/ts-skills/internal/registry"
	"github.com/joshuadavidthomas/ts-skills/internal/safetree"
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
	digest, err := agentskill.SumTree(context.Background(), files, ".")
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
	return remoteForServerWithLimits(t, server, safetree.PrototypeLimits())
}

func remoteForServerWithLimits(t *testing.T, server *httptest.Server, limits safetree.Limits) *Remote {
	t.Helper()
	origin, err := url.Parse(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	remote, err := NewRemote(origin, &http.Client{Timeout: 5 * time.Second}, t.TempDir(), limits)
	if err != nil {
		t.Fatal(err)
	}
	return remote
}

func setClientTreeHeaders(header http.Header, namespace, name, digest string) {
	header.Set(protocol.HeaderPublicationNamespace, namespace)
	header.Set(protocol.HeaderPublicationName, name)
	header.Set(protocol.HeaderPublicationDigest, digest)
}

func TestRemoteFetchEnforcesTreeArchiveCeiling(t *testing.T) {
	limits := safetree.Limits{
		MaxFiles: 2, MaxPathBytes: 16, MaxDepth: 2, MaxFileBytes: 80, MaxExpandedBytes: 100,
	}
	digest, archive := clientTree(t, "archive ceiling")
	maximum, err := protocol.TreeArchiveCeiling(limits)
	if err != nil {
		t.Fatal(err)
	}
	responseBody := archive
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/zip")
		setClientTreeHeaders(w.Header(), "team", "sample", digest.String())
		_, _ = w.Write(responseBody)
	}))
	defer server.Close()
	remote := remoteForServerWithLimits(t, server, limits)
	requirement, err := install.Exact(clientSkill(t), digest)
	if err != nil {
		t.Fatal(err)
	}
	fetched, err := remote.Fetch(context.Background(), requirement)
	if err != nil {
		t.Fatalf("fetch tree archive: %v", err)
	}
	if err := fetched.Tree().Close(); err != nil {
		t.Fatal(err)
	}

	responseBody = append(bytes.Clone(archive), bytes.Repeat([]byte{0}, int(maximum)+1-len(archive))...)
	if _, err := remote.Fetch(context.Background(), requirement); !errors.Is(err, safetree.ErrLimitExceeded) {
		t.Fatalf("oversize tree archive error = %v, want %v", err, safetree.ErrLimitExceeded)
	}
}

// TestDecodeZIPPreservesCancellation stays below Fetch because Fetch has no
// synchronization point between spooling an HTTP response and decoding it.
// It verifies that AddFile cancellation stays distinct from protocol errors.
func TestDecodeZIPPreservesCancellation(t *testing.T) {
	_, archive := clientTree(t, "cancellation")
	parent := t.TempDir()
	archivePath := filepath.Join(parent, "tree.zip")
	if err := os.WriteFile(archivePath, archive, 0o600); err != nil {
		t.Fatal(err)
	}
	remote := &Remote{stagingParent: t.TempDir(), limits: safetree.PrototypeLimits()}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := remote.decodeZIP(ctx, archivePath)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled decodeZIP error = %v, want context.Canceled", err)
	}
	if errors.Is(err, protocol.ErrProtocol) {
		t.Fatalf("canceled decodeZIP error flattened into ErrProtocol: %v", err)
	}
}

func TestRemoteMapsRegistryErrorCodesToSentinels(t *testing.T) {
	tests := map[string]struct {
		code string
		want error
	}{
		"not-found":       {protocol.CodeNotFound, registry.ErrNotFound},
		"invalid-request": {protocol.CodeInvalidRequest, protocol.ErrInvalidRequest},
		"too-large":       {protocol.CodeTooLarge, safetree.ErrLimitExceeded},
		"internal":        {protocol.CodeInternal, protocol.ErrInternal},
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
			requirement, err := install.Exact(clientSkill(t), digest)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := remote.Fetch(context.Background(), requirement); !errors.Is(err, test.want) {
				t.Fatalf("Fetch error = %v, want errors.Is %v", err, test.want)
			}
		})
	}
}

func TestRemoteFetchRejectsInvalidTreeArchives(t *testing.T) {
	digest, archive := clientTree(t, "archive format")
	end := bytes.LastIndex(archive, []byte{'P', 'K', 5, 6})
	if end < 0 {
		t.Fatal("test ZIP has no end record")
	}
	underreported := bytes.Clone(archive)
	binary.LittleEndian.PutUint16(underreported[end+8:end+10], 1)
	binary.LittleEndian.PutUint16(underreported[end+10:end+12], 1)
	zip64 := bytes.Clone(archive)
	binary.LittleEndian.PutUint16(zip64[end+8:end+10], ^uint16(0))
	binary.LittleEndian.PutUint16(zip64[end+10:end+12], ^uint16(0))

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

	limited := safetree.PrototypeLimits()
	limited.MaxFiles = 1
	tests := map[string]struct {
		archive []byte
		limits  safetree.Limits
		want    error
	}{
		"too many entries":      {archive: archive, limits: limited, want: safetree.ErrLimitExceeded},
		"underreported entries": {archive: underreported, limits: safetree.PrototypeLimits(), want: protocol.ErrProtocol},
		"ZIP64":                 {archive: zip64, limits: safetree.PrototypeLimits(), want: protocol.ErrProtocol},
		"deflated entry":        {archive: deflated.Bytes(), limits: safetree.PrototypeLimits(), want: protocol.ErrProtocol},
	}
	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "application/zip")
				setClientTreeHeaders(w.Header(), "team", "sample", digest.String())
				_, _ = w.Write(test.archive)
			}))
			defer server.Close()
			requirement, err := install.Exact(clientSkill(t), digest)
			if err != nil {
				t.Fatal(err)
			}
			_, err = remoteForServerWithLimits(t, server, test.limits).Fetch(context.Background(), requirement)
			if !errors.Is(err, test.want) {
				t.Fatalf("Fetch error = %v, want errors.Is %v", err, test.want)
			}
			if name == "ZIP64" && errors.Is(err, safetree.ErrLimitExceeded) {
				t.Fatalf("ZIP64 error = %v, want format rejection", err)
			}
		})
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
	installer, err := install.NewInstaller(remoteForServer(t, server))
	if err != nil {
		t.Fatal(err)
	}
	project, _ := install.OpenProject(t.TempDir())
	requirement, _ := install.Current(skill)
	if _, err := installer.Install(context.Background(), project, requirement); err != nil {
		t.Fatal(err)
	}
	beforeTree, err := os.ReadFile(filepath.Join(project.SkillsDir(), "sample", "SKILL.md"))
	if err != nil {
		t.Fatalf("read installed baseline tree: %v", err)
	}
	beforeLock, err := os.ReadFile(project.LockPath())
	if err != nil {
		t.Fatalf("read installed baseline lock: %v", err)
	}

	currentDigest = secondDigest
	if _, err := installer.Install(context.Background(), project, requirement); !errors.Is(err, install.ErrDigestMismatch) {
		t.Fatalf("mismatched install error = %v", err)
	}
	afterTree, err := os.ReadFile(filepath.Join(project.SkillsDir(), "sample", "SKILL.md"))
	if err != nil {
		t.Fatalf("read tree after rejected install: %v", err)
	}
	afterLock, err := os.ReadFile(project.LockPath())
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

	installer, err := install.NewInstaller(remoteForServer(t, server))
	if err != nil {
		t.Fatal(err)
	}
	project, err := install.OpenProject(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	requirement, err := install.Current(skill)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := installer.Install(context.Background(), project, requirement); err != nil {
		t.Fatal(err)
	}
	documentPath := filepath.Join(project.SkillsDir(), "sample", "SKILL.md")
	assetPath := filepath.Join(project.SkillsDir(), "sample", "assets", "data.txt")
	documentBefore, err := os.ReadFile(documentPath)
	if err != nil {
		t.Fatal(err)
	}
	assetBefore, err := os.ReadFile(assetPath)
	if err != nil {
		t.Fatal(err)
	}
	lockBefore, err := os.ReadFile(project.LockPath())
	if err != nil {
		t.Fatal(err)
	}

	currentDigest = secondDigest
	treeZIP = secondZIP
	truncateTree = true
	if _, err := installer.Install(context.Background(), project, requirement); !errors.Is(err, io.ErrUnexpectedEOF) {
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
	lockAfter, err := os.ReadFile(project.LockPath())
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
	requirement, _ := install.Exact(skill, digest)
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
			expectedErr: protocol.ErrProtocol,
		},
		{
			name: "declared size",
			handler: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "application/zip")
				w.Header().Set("Content-Length", fmt.Sprint(int64(1)<<40))
				setClientTreeHeaders(w.Header(), "team", "sample", digest.String())
				w.WriteHeader(http.StatusOK)
			}),
			expectedErr: safetree.ErrLimitExceeded,
		},
		{
			name: "unsafe path",
			handler: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "application/zip")
				setClientTreeHeaders(w.Header(), "team", "sample", digest.String())
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
			expectedErr: protocol.ErrProtocol,
		},
		{
			name: "wrong namespace",
			change: func(header http.Header) {
				header.Set(protocol.HeaderPublicationNamespace, "other")
			},
			expectedErr: install.ErrIdentityMismatch,
		},
		{
			name: "missing name",
			change: func(header http.Header) {
				header.Del(protocol.HeaderPublicationName)
			},
			expectedErr: protocol.ErrProtocol,
		},
		{
			name: "wrong name",
			change: func(header http.Header) {
				header.Set(protocol.HeaderPublicationName, "other")
			},
			expectedErr: install.ErrIdentityMismatch,
		},
		{
			name: "missing digest",
			change: func(header http.Header) {
				header.Del(protocol.HeaderPublicationDigest)
			},
			expectedErr: protocol.ErrProtocol,
		},
		{
			name: "wrong digest",
			change: func(header http.Header) {
				header.Set(protocol.HeaderPublicationDigest, otherDigest.String())
			},
			expectedErr: install.ErrIdentityMismatch,
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

				var requirement install.Requirement
				var err error
				if mode == "exact" {
					requirement, err = install.Exact(skill, digest)
				} else {
					requirement, err = install.Current(skill)
				}
				if err != nil {
					t.Fatal(err)
				}
				fetched, err := remoteForServer(t, server).Fetch(context.Background(), requirement)
				if test.expectedErr != nil {
					if !errors.Is(err, test.expectedErr) {
						t.Fatalf("fetch error = %v, want %v", err, test.expectedErr)
					}
					return
				}
				if err != nil {
					t.Fatal(err)
				}
				if fetched.Publication().Skill() != skill || fetched.Publication().Tree() != digest {
					t.Fatalf("fetched publication = %s@%s", fetched.Publication().Skill().String(), fetched.Publication().Tree().String())
				}
				if contents, err := fs.ReadFile(fetched.Tree(), "assets/data.txt"); err != nil || string(contents) != "asset" {
					t.Fatalf("rootless fetched asset = %q, %v", contents, err)
				}
				if err := fetched.Tree().Close(); err != nil {
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
	requirement, _ := install.Current(clientSkill(t))
	_, err := remoteForServer(t, server).Fetch(context.Background(), requirement)
	if !errors.Is(err, install.ErrIdentityMismatch) {
		t.Fatalf("identity mismatch error = %v", err)
	}
}

func TestFetchedTreeCloseRetainsSnapshotAfterFailure(t *testing.T) {
	builder, err := safetree.NewBuilder(t.TempDir(), safetree.PrototypeLimits())
	if err != nil {
		t.Fatal(err)
	}
	if err := builder.AddFile(context.Background(), "SKILL.md", 4, bytes.NewReader([]byte("data"))); err != nil {
		t.Fatal(err)
	}
	snapshot, err := builder.Finish()
	if err != nil {
		t.Fatal(err)
	}
	tree := &fetchedTree{snapshot: snapshot}
	injected := errors.New("injected snapshot close failure")
	tree.closeSnapshot = func(*safetree.Snapshot) error { return injected }
	if err := tree.Close(); !errors.Is(err, injected) {
		t.Fatalf("first Close error = %v, want injected failure", err)
	}
	if tree.snapshot == nil {
		t.Fatal("failed Close released fetched snapshot ownership")
	}
	if _, err := fs.ReadFile(tree, "SKILL.md"); err != nil {
		t.Fatalf("fetched tree after failed Close: %v", err)
	}
	tree.closeSnapshot = nil
	if err := tree.Close(); err != nil {
		t.Fatal(err)
	}
	if tree.snapshot != nil {
		t.Fatal("successful Close retained fetched snapshot ownership")
	}
}
