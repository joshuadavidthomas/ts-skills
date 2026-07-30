package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"testing/fstest"

	"github.com/joshuadavidthomas/ts-skills/internal/protocol"
	"github.com/joshuadavidthomas/ts-skills/internal/tree"
)

type cancelAfterReadTree struct {
	files  fstest.MapFS
	cancel context.CancelFunc
}

func (t cancelAfterReadTree) Open(name string) (fs.File, error) {
	file, err := t.files.Open(name)
	if err != nil || name == "." {
		return file, err
	}
	return &cancelAfterReadTreeFile{File: file, cancel: t.cancel}, nil
}

func (t cancelAfterReadTree) ReadDir(name string) ([]fs.DirEntry, error) {
	return t.files.ReadDir(name)
}

type cancelAfterReadTreeFile struct {
	fs.File
	cancelled bool
	cancel    context.CancelFunc
}

func (t *cancelAfterReadTreeFile) Read(buffer []byte) (int, error) {
	read, err := t.File.Read(buffer)
	if read > 0 && !t.cancelled {
		t.cancelled = true
		t.cancel()
	}
	return read, err
}

func mustArchiveCap(t *testing.T, limits tree.Limits) int64 {
	t.Helper()
	cap, err := tree.MaxArchiveBytes(limits)
	if err != nil {
		t.Fatal(err)
	}
	return cap
}

func TestRootlessZIPHonorsCancellationWhileStreaming(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	staging := t.TempDir()
	h := &apiHandler{
		options:         Options{StagingParent: staging, Limits: tree.PrototypeLimits()},
		maxArchiveBytes: mustArchiveCap(t, tree.PrototypeLimits()),
	}
	source := cancelAfterReadTree{
		files:  fstest.MapFS{"large": {Data: bytes.Repeat([]byte("x"), 128<<10)}},
		cancel: cancel,
	}
	if _, err := h.rootlessZIP(ctx, source); !errors.Is(err, context.Canceled) {
		t.Fatalf("rootlessZIP() error = %v, want context cancellation", err)
	}
	entries, err := os.ReadDir(staging)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("partial archive remains after cancellation: %v", entries)
	}
}

func TestRootlessZIPFitsProtocolMetadataAllowance(t *testing.T) {
	ceiling := mustArchiveCap(t, tree.PrototypeLimits())
	h := &apiHandler{
		options:         Options{StagingParent: t.TempDir(), Limits: tree.PrototypeLimits()},
		maxArchiveBytes: ceiling,
	}
	archive, err := h.rootlessZIP(context.Background(), fstest.MapFS{
		"SKILL.md":        {Data: []byte("s")},
		"assets/data.txt": {Data: []byte("a")},
	})
	if err != nil {
		t.Fatal(err)
	}
	size := archive.Size()
	if err := archive.Close(); err != nil {
		t.Fatal(err)
	}
	if size > ceiling {
		t.Fatalf("tree ZIP bytes = %d, exceeds protocol ceiling %d", size, ceiling)
	}
}

type recordSlogHandler struct {
	mu      sync.Mutex
	records []slog.Record
}

func (h *recordSlogHandler) Enabled(context.Context, slog.Level) bool { return true }
func (h *recordSlogHandler) WithAttrs([]slog.Attr) slog.Handler       { return h }
func (h *recordSlogHandler) WithGroup(string) slog.Handler            { return h }

func (h *recordSlogHandler) Handle(_ context.Context, record slog.Record) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.records = append(h.records, record)
	return nil
}

func (h *recordSlogHandler) contains(want string) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	for _, record := range h.records {
		if strings.Contains(record.Message, want) {
			return true
		}
		found := false
		record.Attrs(func(attr slog.Attr) bool {
			found = strings.Contains(attr.Value.String(), want)
			return !found
		})
		if found {
			return true
		}
	}
	return false
}

func TestWriteDomainErrorLogsUnexpectedFailure(t *testing.T) {
	captured := &recordSlogHandler{}
	h := &apiHandler{options: Options{Logger: slog.New(captured)}}
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/api/"+protocol.Version+"/skills/team/sample/current", nil)

	h.writeDomainError(recorder, request, errors.New("catalog storage unavailable"))

	if recorder.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusInternalServerError)
	}
	for _, want := range []string{"API request failed", "catalog storage unavailable", "GET", request.URL.Path} {
		if !captured.contains(want) {
			t.Errorf("log has no record carrying %q", want)
		}
	}
}

func TestWriteDomainErrorMapsTreeLimit(t *testing.T) {
	h := &apiHandler{options: Options{Logger: slog.Default()}}
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/api/"+protocol.Version+"/skills/team/sample/publications/digest/tree.zip", nil)

	h.writeDomainError(recorder, request, &tree.LimitError{Limit: "archive bytes", Max: 1, Actual: 2})

	if recorder.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusRequestEntityTooLarge)
	}
	var response protocol.ErrorResponse
	if err := json.NewDecoder(recorder.Body).Decode(&response); err != nil {
		t.Fatal(err)
	}
	if response.Code != protocol.CodeTooLarge {
		t.Fatalf("code = %q, want %q", response.Code, protocol.CodeTooLarge)
	}
}
