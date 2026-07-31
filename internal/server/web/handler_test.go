package web

import (
	"context"
	"errors"
	"html/template"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"testing/fstest"

	"github.com/joshuadavidthomas/ts-skills/internal/tree"
)

func TestResolveTreeFileRespectsCancellation(t *testing.T) {
	source := fstest.MapFS{
		"SKILL.md":        {Data: []byte("---\nname: sample\ndescription: Cancellation test\n---\nBody.\n")},
		"assets/data.txt": {Data: []byte("asset")},
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := resolveTreeFile(ctx, source, nil); !errors.Is(err, context.Canceled) {
		t.Fatalf("resolveTreeFile with cancelled context error = %v", err)
	}
}

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func failingTemplate() *template.Template {
	funcs := template.FuncMap{
		"boom": func() (string, error) { return "", errors.New("template boom") },
	}
	return template.Must(template.New("page").Funcs(funcs).Parse(`{{boom}}`))
}

func TestRenderFallsBackWhenPageTemplateFails(t *testing.T) {
	errorPage := template.Must(template.New("page").Parse(`<h1>{{.Content.Title}}</h1><p>{{.Content.Action}}</p>`))
	h := &webHandler{
		pages: map[string]*template.Template{
			"broken": failingTemplate(),
			"error":  errorPage,
		},
		options: Options{Logger: discardLogger()},
	}

	recorder := httptest.NewRecorder()
	h.render(recorder, http.StatusOK, "broken", pageView{})

	if recorder.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusInternalServerError)
	}
	if !strings.Contains(recorder.Body.String(), "Page could not be rendered") {
		t.Fatalf("body = %q, want the generic error page", recorder.Body.String())
	}
}

func TestRenderErrorFallsBackToPlaintextWhenErrorTemplateFails(t *testing.T) {
	h := &webHandler{
		pages:   map[string]*template.Template{"error": failingTemplate()},
		options: Options{Logger: discardLogger()},
	}

	recorder := httptest.NewRecorder()
	h.renderError(recorder, http.StatusInternalServerError, "Unreachable", "Never rendered")

	if recorder.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusInternalServerError)
	}
	if got := recorder.Header().Get("Content-Type"); !strings.HasPrefix(got, "text/plain") {
		t.Fatalf("content type = %q, want the plaintext fallback", got)
	}
	if !strings.Contains(recorder.Body.String(), "Internal Server Error") {
		t.Fatalf("body = %q, want the plaintext status text", recorder.Body.String())
	}
}

func TestPageTemplateOwnsCompleteDocumentStructure(t *testing.T) {
	pages, err := parsePages()
	if err != nil {
		t.Fatal(err)
	}
	var rendered strings.Builder
	err = pages["upload"].ExecuteTemplate(&rendered, "page", pageView{
		Title: "Upload a skill",
	})
	if err != nil {
		t.Fatal(err)
	}
	body := rendered.String()
	for _, element := range []string{"<!doctype html>", "<html", "<head>", "</head>", "<body", "</body>", "</html>"} {
		if count := strings.Count(body, element); count != 1 {
			t.Errorf("%q count = %d, want 1", element, count)
		}
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

func TestHandleErrorLogsUnexpectedFailure(t *testing.T) {
	pages, err := parsePages()
	if err != nil {
		t.Fatal(err)
	}
	captured := &recordSlogHandler{}
	h := &webHandler{pages: pages, options: Options{Logger: slog.New(captured)}}

	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/candidates", nil)
	h.handleError(recorder, request, errors.New("catalog storage unavailable"))

	if recorder.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusInternalServerError)
	}
	if body := recorder.Body.String(); strings.Contains(body, "catalog storage unavailable") {
		t.Fatalf("response leaked the internal error: %q", body)
	}
	for _, want := range []string{"web request failed", "catalog storage unavailable", "POST", "/candidates"} {
		if !captured.contains(want) {
			t.Errorf("log has no record carrying %q", want)
		}
	}
}

func TestCloseUploadLogsCleanupFailure(t *testing.T) {
	captured := &recordSlogHandler{}
	injected := errors.New("injected upload cleanup failure")
	h := &webHandler{options: Options{Logger: slog.New(captured)}}
	submission := &submission{
		snapshot:      &tree.Snapshot{},
		closeSnapshot: func(*tree.Snapshot) error { return injected },
	}

	if h.closeUpload(submission) {
		t.Fatal("failed upload cleanup reported success")
	}
	if !captured.contains("web upload cleanup failed") || !captured.contains(injected.Error()) {
		t.Fatal("upload cleanup failure was not logged")
	}
}
