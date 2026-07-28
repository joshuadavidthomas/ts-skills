package daemon

import (
	"archive/zip"
	"bytes"
	"context"
	"errors"
	"io"
	"mime/multipart"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/joshuadavidthomas/ts-skill-registry/internal/storage"
)

type recordingListener struct {
	net.Listener
	onClose func()
	once    sync.Once
}

func (l *recordingListener) Close() error {
	l.once.Do(l.onClose)
	return l.Listener.Close()
}

func TestHTTPServerHasFiniteTimeouts(t *testing.T) {
	server := newHTTPServer(http.NotFoundHandler(), newHandlerGate(nil))
	timeouts := map[string]struct {
		got  time.Duration
		want time.Duration
	}{
		"read header": {got: server.ReadHeaderTimeout, want: readHeaderTimeout},
		"read":        {got: server.ReadTimeout, want: readTimeout},
		"write":       {got: server.WriteTimeout, want: writeTimeout},
		"idle":        {got: server.IdleTimeout, want: idleTimeout},
	}
	for name, timeout := range timeouts {
		if timeout.got != timeout.want {
			t.Errorf("%s timeout = %v, want %v", name, timeout.got, timeout.want)
		}
		if timeout.got <= 0 {
			t.Errorf("%s timeout must be finite and positive", name)
		}
	}
	for name, timeout := range map[string]time.Duration{
		"read":  server.ReadTimeout,
		"write": server.WriteTimeout,
	} {
		if timeout < 2*time.Minute {
			t.Errorf("%s timeout = %v, want at least two minutes for a maximum-size prototype transfer", name, timeout)
		}
	}
}

func TestRunWaitsForHandlersAfterForcedHTTPShutdown(t *testing.T) {
	baseListener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}

	var eventMu sync.Mutex
	var events []string
	record := func(event string) {
		eventMu.Lock()
		events = append(events, event)
		eventMu.Unlock()
	}
	listenerClosed := make(chan struct{})
	listener := &recordingListener{Listener: baseListener, onClose: func() {
		record("listener-closed")
		close(listenerClosed)
	}}
	handlerStarted := make(chan struct{})
	releaseHandler := make(chan struct{})
	tailnetClosed := make(chan struct{})
	storageClosed := make(chan struct{})
	handler := http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		record("handler-started")
		close(handlerStarted)
		defer record("tree-closed")
		<-releaseHandler
		record("handler-finished")
	})
	cleanup := &runtimeCleanup{
		closeNetwork: func() error {
			record("tailnet-closed")
			close(tailnetClosed)
			return nil
		},
		closeStorage: func() error {
			record("storage-closed")
			close(storageClosed)
			return nil
		},
	}
	factory := func(context.Context, Config) (*runtime, error) {
		return &runtime{
			listener: listener,
			handler:  handler,
			close:    cleanup.close,
		}, nil
	}

	ctx, cancel := context.WithCancel(context.Background())
	stateDir := t.TempDir()
	runResult := make(chan error, 1)
	go func() {
		runResult <- runWithHTTPShutdownTimeout(ctx, Config{
			StateDir: stateDir,
			Hostname: "test-daemon",
		}, factory, 20*time.Millisecond)
	}()

	responseResult := make(chan error, 1)
	go func() {
		response, err := http.Get("http://" + baseListener.Addr().String())
		if err == nil {
			err = response.Body.Close()
		}
		responseResult <- err
	}()
	select {
	case <-handlerStarted:
	case <-time.After(5 * time.Second):
		t.Fatal("HTTP handler did not start")
	}

	cancel()
	select {
	case <-listenerClosed:
	case <-time.After(5 * time.Second):
		t.Fatal("HTTP listener did not stop accepting during shutdown")
	}
	select {
	case err := <-responseResult:
		if err == nil {
			t.Fatal("HTTP connection remained open after forced shutdown")
		}
		record("connection-forced-closed")
	case <-time.After(5 * time.Second):
		t.Fatal("HTTP connection was not closed after the shutdown timeout")
	}
	select {
	case <-tailnetClosed:
		t.Fatal("Tailnet runtime closed before the active handler finished")
	default:
	}
	select {
	case <-storageClosed:
		t.Fatal("storage closed before the active handler finished")
	default:
	}

	close(releaseHandler)
	select {
	case err := <-runResult:
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("run error = %v, want HTTP shutdown deadline", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("daemon did not finish cleanup after the handler returned")
	}

	eventMu.Lock()
	gotEvents := append([]string(nil), events...)
	eventMu.Unlock()
	wantEvents := []string{
		"handler-started",
		"listener-closed",
		"connection-forced-closed",
		"handler-finished",
		"tree-closed",
		"tailnet-closed",
		"storage-closed",
	}
	if !reflect.DeepEqual(gotEvents, wantEvents) {
		t.Fatalf("lifecycle events = %v, want %v", gotEvents, wantEvents)
	}
}

func TestRunRejectsDispatchPausedBeforeAdmissionDuringShutdown(t *testing.T) {
	baseListener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}

	dispatchPaused := make(chan struct{})
	releaseAdmission := make(chan struct{})
	var releaseOnce sync.Once
	release := func() { releaseOnce.Do(func() { close(releaseAdmission) }) }
	t.Cleanup(release)
	var pauseOnce sync.Once
	gate := newHandlerGate(func() {
		pauseOnce.Do(func() {
			close(dispatchPaused)
			<-releaseAdmission
		})
	})
	admissionResult := make(chan bool, 1)
	gate.afterAdmission = func(admitted bool) { admissionResult <- admitted }
	handlerCalled := make(chan struct{}, 1)
	runtimeClosed := make(chan struct{})
	factory := func(context.Context, Config) (*runtime, error) {
		return &runtime{
			listener: baseListener,
			handler: http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
				handlerCalled <- struct{}{}
			}),
			close: func() error {
				close(runtimeClosed)
				return nil
			},
		}, nil
	}

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	stateDir := t.TempDir()
	runResult := make(chan error, 1)
	go func() {
		runResult <- runWithHandlerGate(ctx, Config{
			StateDir: stateDir,
			Hostname: "test-daemon",
		}, factory, 20*time.Millisecond, gate)
	}()

	requestResult := make(chan error, 1)
	go func() {
		response, err := http.Get("http://" + baseListener.Addr().String())
		if err == nil {
			err = response.Body.Close()
		}
		requestResult <- err
	}()
	select {
	case <-dispatchPaused:
	case <-time.After(5 * time.Second):
		t.Fatal("HTTP dispatch did not pause before handler admission")
	}

	cancel()
	select {
	case err := <-runResult:
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("run error = %v, want HTTP shutdown deadline", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("daemon cleanup waited for a dispatch that was never admitted")
	}
	select {
	case <-runtimeClosed:
	case <-time.After(5 * time.Second):
		t.Fatal("runtime was not closed after admission closed")
	}
	select {
	case <-handlerCalled:
		t.Fatal("dispatch touched the runtime handler before admission resumed")
	default:
	}

	release()
	select {
	case admitted := <-admissionResult:
		if admitted {
			t.Fatal("dispatch paused before shutdown was admitted after cleanup")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("paused HTTP dispatch did not reach its admission decision")
	}
	select {
	case <-requestResult:
	case <-time.After(5 * time.Second):
		t.Fatal("rejected HTTP dispatch did not return")
	}
	select {
	case <-handlerCalled:
		t.Fatal("dispatch paused before admission touched runtime resources after cleanup")
	default:
	}
}

func TestRuntimeCleanupRetriesFailedStorageClose(t *testing.T) {
	var events []string
	storageAttempts := 0
	cleanup := &runtimeCleanup{
		closeNetwork: func() error {
			events = append(events, "tailnet-closed")
			return nil
		},
		closeStorage: func() error {
			events = append(events, "storage-close-attempted")
			storageAttempts++
			if storageAttempts == 1 {
				return storage.ErrTreesOpen
			}
			return nil
		},
	}

	if err := cleanup.close(); !errors.Is(err, storage.ErrTreesOpen) {
		t.Fatalf("first cleanup error = %v, want ErrTreesOpen", err)
	}
	if err := cleanup.close(); err != nil {
		t.Fatalf("retry cleanup: %v", err)
	}
	if err := cleanup.close(); err != nil {
		t.Fatalf("repeat completed cleanup: %v", err)
	}
	wantEvents := []string{"tailnet-closed", "storage-close-attempted", "storage-close-attempted"}
	if !reflect.DeepEqual(events, wantEvents) {
		t.Fatalf("cleanup events = %v, want %v", events, wantEvents)
	}
}

func TestRunGracefullyStopsHTTPBeforeRuntimeCleanup(t *testing.T) {
	baseListener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}

	var eventMu sync.Mutex
	var events []string
	record := func(event string) {
		eventMu.Lock()
		events = append(events, event)
		eventMu.Unlock()
	}
	listenerClosed := make(chan struct{})
	listener := &recordingListener{Listener: baseListener, onClose: func() {
		record("listener-closed")
		close(listenerClosed)
	}}
	handlerStarted := make(chan struct{})
	releaseHandler := make(chan struct{})
	runtimeClosed := make(chan struct{})
	handler := http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		record("handler-started")
		close(handlerStarted)
		<-releaseHandler
		record("handler-finished")
		writer.WriteHeader(http.StatusNoContent)
	})
	factory := func(context.Context, Config) (*runtime, error) {
		return &runtime{
			listener: listener,
			handler:  handler,
			close: func() error {
				record("runtime-closed")
				close(runtimeClosed)
				return nil
			},
		}, nil
	}

	ctx, cancel := context.WithCancel(context.Background())
	stateDir := t.TempDir()
	runResult := make(chan error, 1)
	go func() {
		runResult <- run(ctx, Config{StateDir: stateDir, Hostname: "test-daemon"}, factory)
	}()

	responseResult := make(chan error, 1)
	go func() {
		response, err := http.Get("http://" + baseListener.Addr().String())
		if err == nil {
			_, _ = io.Copy(io.Discard, response.Body)
			err = response.Body.Close()
		}
		responseResult <- err
	}()
	select {
	case <-handlerStarted:
	case <-time.After(5 * time.Second):
		t.Fatal("HTTP handler did not start")
	}

	cancel()
	select {
	case <-listenerClosed:
	case <-time.After(5 * time.Second):
		t.Fatal("HTTP listener did not close during graceful shutdown")
	}
	select {
	case <-runtimeClosed:
		t.Fatal("runtime closed before the active handler finished")
	default:
	}
	close(releaseHandler)

	select {
	case err := <-runResult:
		if err != nil {
			t.Fatalf("run returned an error during cancellation: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("daemon did not finish shutdown")
	}
	if err := <-responseResult; err != nil {
		t.Fatalf("HTTP request failed: %v", err)
	}

	eventMu.Lock()
	gotEvents := append([]string(nil), events...)
	eventMu.Unlock()
	wantEvents := []string{"handler-started", "listener-closed", "handler-finished", "runtime-closed"}
	if !reflect.DeepEqual(gotEvents, wantEvents) {
		t.Fatalf("lifecycle events = %v, want %v", gotEvents, wantEvents)
	}
}

func TestRunClosesRuntimeAfterServeFailure(t *testing.T) {
	baseListener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	if err := baseListener.Close(); err != nil {
		t.Fatal(err)
	}
	var events []string
	listener := &recordingListener{Listener: baseListener, onClose: func() {
		events = append(events, "listener-closed")
	}}
	factory := func(context.Context, Config) (*runtime, error) {
		return &runtime{
			listener: listener,
			handler:  http.NotFoundHandler(),
			close: func() error {
				events = append(events, "runtime-closed")
				return nil
			},
		}, nil
	}

	err = run(context.Background(), Config{StateDir: t.TempDir(), Hostname: "test-daemon"}, factory)
	if err == nil || !strings.Contains(err.Error(), "serve Tailnet HTTP") {
		t.Fatalf("run error = %v, want serve failure", err)
	}
	if want := []string{"listener-closed", "runtime-closed"}; !reflect.DeepEqual(events, want) {
		t.Fatalf("cleanup events = %v, want %v", events, want)
	}
}

func TestRunClosesIncompleteRuntime(t *testing.T) {
	closed := false
	factory := func(context.Context, Config) (*runtime, error) {
		return &runtime{close: func() error { closed = true; return nil }}, nil
	}
	err := run(context.Background(), Config{StateDir: t.TempDir(), Hostname: "test-daemon"}, factory)
	if err == nil {
		t.Fatal("run succeeded with an incomplete runtime")
	}
	if !closed {
		t.Fatal("incomplete runtime was not closed")
	}
}

func TestConfigFromEnvReadsEnrollmentKeyOnce(t *testing.T) {
	stateDir := t.TempDir()
	authKeyPath := filepath.Join(t.TempDir(), "auth.key")
	if err := os.WriteFile(authKeyPath, []byte("  tskey-auth-test\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("TS_SKILLSD_STATE_DIR", stateDir)
	t.Setenv("TS_SKILLSD_HOSTNAME", "")
	t.Setenv("TS_SKILLSD_AUTHKEY_FILE", authKeyPath)
	t.Setenv("TS_SKILLSD_TAG", "tag:skills-registry")
	t.Setenv("TS_SKILLSD_VERBOSE", "true")

	config, err := ConfigFromEnv()
	if err != nil {
		t.Fatal(err)
	}
	if config.StateDir != stateDir {
		t.Fatalf("state directory = %q, want %q", config.StateDir, stateDir)
	}
	if config.Hostname != defaultHostname || config.AuthKey != "tskey-auth-test" || config.Tag != "tag:skills-registry" || !config.Verbose {
		t.Fatalf("config = %#v", config)
	}
	if err := os.WriteFile(authKeyPath, []byte("changed"), 0o600); err != nil {
		t.Fatal(err)
	}
	if config.AuthKey != "tskey-auth-test" {
		t.Fatal("loaded config changed after its auth-key file changed")
	}
}

func TestConfigFromEnvRequiresStateDirectory(t *testing.T) {
	t.Setenv("TS_SKILLSD_STATE_DIR", "")
	t.Setenv("TS_SKILLSD_HOSTNAME", "")
	t.Setenv("TS_SKILLSD_AUTHKEY_FILE", "")
	t.Setenv("TS_SKILLSD_TAG", "")
	t.Setenv("TS_SKILLSD_VERBOSE", "")
	if _, err := ConfigFromEnv(); err == nil || !strings.Contains(err.Error(), "TS_SKILLSD_STATE_DIR") {
		t.Fatalf("ConfigFromEnv error = %v", err)
	}
}

func TestConfigFromEnvParsesVerbose(t *testing.T) {
	for value, want := range map[string]bool{"": false, "0": false, "false": false, "1": true, "true": true} {
		t.Setenv("TS_SKILLSD_STATE_DIR", t.TempDir())
		t.Setenv("TS_SKILLSD_VERBOSE", value)
		config, err := ConfigFromEnv()
		if err != nil {
			t.Fatalf("ConfigFromEnv with TS_SKILLSD_VERBOSE=%q: %v", value, err)
		}
		if config.Verbose != want {
			t.Errorf("ConfigFromEnv with TS_SKILLSD_VERBOSE=%q: verbose = %v, want %v", value, config.Verbose, want)
		}
	}
	t.Setenv("TS_SKILLSD_STATE_DIR", t.TempDir())
	t.Setenv("TS_SKILLSD_VERBOSE", "maybe")
	if _, err := ConfigFromEnv(); err == nil || !strings.Contains(err.Error(), "TS_SKILLSD_VERBOSE") {
		t.Errorf("ConfigFromEnv accepted a non-boolean TS_SKILLSD_VERBOSE: %v", err)
	}
}

func TestPersistentCSRFKey(t *testing.T) {
	stateDir := t.TempDir()
	first, err := loadOrCreateCSRFKey(stateDir)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(stateDir, "csrf.key")
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if permission := info.Mode().Perm(); permission != 0o600 {
		t.Fatalf("CSRF key permissions = %o, want 600", permission)
	}
	contents, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(contents) != 32 {
		t.Fatalf("CSRF key size = %d, want 32", len(contents))
	}

	if err := os.Chmod(path, 0o644); err != nil {
		t.Fatal(err)
	}
	second, err := loadOrCreateCSRFKey(stateDir)
	if err != nil {
		t.Fatal(err)
	}
	if first != second {
		t.Fatal("CSRF key changed between loads")
	}
	info, err = os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if permission := info.Mode().Perm(); permission != 0o600 {
		t.Fatalf("reloaded CSRF key permissions = %o, want 600", permission)
	}
}

func TestPersistentCSRFKeyRejectsMalformedFile(t *testing.T) {
	stateDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(stateDir, "csrf.key"), []byte("short"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := loadOrCreateCSRFKey(stateDir); err == nil {
		t.Fatal("malformed CSRF key was accepted")
	}
}

func TestRunReportsFactoryFailureWithoutCleanup(t *testing.T) {
	factoryErr := errors.New("factory failed")
	err := run(context.Background(), Config{StateDir: t.TempDir(), Hostname: "test-daemon"}, func(context.Context, Config) (*runtime, error) {
		return nil, factoryErr
	})
	if !errors.Is(err, factoryErr) {
		t.Fatalf("run error = %v, want factory failure", err)
	}
}

func TestNormalizeDevConfigChecksListenAddress(t *testing.T) {
	for _, listen := range []string{"", "127.0.0.1", "127.0.0.1:", "0.0.0.0:8080", "example.com:8080", "[::]:8080", "192.168.1.10:8080"} {
		if _, err := normalizeDevConfig(DevConfig{StateDir: t.TempDir(), Listen: listen}); err == nil {
			t.Errorf("normalizeDevConfig accepted %q", listen)
		}
	}
	for _, listen := range []string{"127.0.0.1:0", "localhost:8080", "[::1]:8080"} {
		if _, err := normalizeDevConfig(DevConfig{StateDir: t.TempDir(), Listen: listen}); err != nil {
			t.Errorf("normalizeDevConfig(%q): %v", listen, err)
		}
	}
}

func TestDevConfigFromEnvRefusesEnrollmentKey(t *testing.T) {
	t.Setenv("TS_SKILLSD_DEV_LISTEN", "127.0.0.1:8080")
	t.Setenv("TS_SKILLSD_STATE_DIR", t.TempDir())
	t.Setenv("TS_SKILLSD_AUTHKEY_FILE", filepath.Join(t.TempDir(), "authkey"))
	t.Setenv("TS_AUTHKEY", "")
	if _, err := DevConfigFromEnv(); err == nil {
		t.Fatal("dev config accepted an enrollment auth key file")
	}
}

func TestDevConfigFromEnvRefusesStandardEnrollmentKey(t *testing.T) {
	t.Setenv("TS_SKILLSD_DEV_LISTEN", "127.0.0.1:8080")
	t.Setenv("TS_SKILLSD_STATE_DIR", t.TempDir())
	t.Setenv("TS_SKILLSD_AUTHKEY_FILE", "")
	t.Setenv("TS_AUTHKEY", "tskey-auth-test")
	if _, err := DevConfigFromEnv(); err == nil || !strings.Contains(err.Error(), "TS_AUTHKEY") {
		t.Fatalf("dev config accepted TS_AUTHKEY: %v", err)
	}
}

func TestDevConfigFromEnvDefaultsToLoopbackListen(t *testing.T) {
	t.Setenv("TS_SKILLSD_DEV_LISTEN", "")
	t.Setenv("TS_SKILLSD_AUTHKEY_FILE", "")
	t.Setenv("TS_AUTHKEY", "")
	t.Setenv("TS_SKILLSD_STATE_DIR", t.TempDir())
	config, err := DevConfigFromEnv()
	if err != nil {
		t.Fatal(err)
	}
	if config.Listen != "127.0.0.1:8080" {
		t.Fatalf("default dev listen = %q", config.Listen)
	}
}

func TestDevModeFromEnv(t *testing.T) {
	for value, want := range map[string]bool{"": false, "0": false, "false": false, "1": true, "true": true} {
		t.Setenv("TS_SKILLSD_DEV", value)
		enabled, err := DevModeFromEnv()
		if err != nil {
			t.Fatalf("DevModeFromEnv(%q): %v", value, err)
		}
		if enabled != want {
			t.Errorf("DevModeFromEnv(%q) = %v, want %v", value, enabled, want)
		}
	}
	t.Setenv("TS_SKILLSD_DEV", "maybe")
	if _, err := DevModeFromEnv(); err == nil {
		t.Error("DevModeFromEnv accepted a non-boolean value")
	}
}

func TestDevRuntimeServesLoopbackHTTPAsDevActor(t *testing.T) {
	rt, err := buildDevRuntime(context.Background(), DevConfig{StateDir: t.TempDir(), Listen: "127.0.0.1:0"})
	if err != nil {
		t.Fatal(err)
	}
	server := &http.Server{Handler: rt.handler}
	go func() { _ = server.Serve(rt.listener) }()
	defer func() {
		_ = server.Close()
		if err := rt.close(); err != nil {
			t.Errorf("close dev runtime: %v", err)
		}
	}()

	base := "http://" + rt.listener.Addr().String()
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}

	uploadPage, err := client.Get(base + "/upload")
	if err != nil {
		t.Fatal(err)
	}
	_, _ = io.Copy(io.Discard, uploadPage.Body)
	_ = uploadPage.Body.Close()
	if uploadPage.StatusCode != http.StatusOK {
		t.Fatalf("GET /upload status = %d", uploadPage.StatusCode)
	}
	token := uploadPage.Header.Get("X-CSRF-Token")
	cookies := uploadPage.Cookies()
	if token == "" || len(cookies) != 1 {
		t.Fatalf("upload page did not provide CSRF material: token=%q cookies=%v", token, cookies)
	}

	var archive bytes.Buffer
	zipWriter := zip.NewWriter(&archive)
	entry, err := zipWriter.Create("SKILL.md")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := io.WriteString(entry, "---\nname: sample\ndescription: Dev mode test\n---\nInstructions.\n"); err != nil {
		t.Fatal(err)
	}
	if err := zipWriter.Close(); err != nil {
		t.Fatal(err)
	}

	var form bytes.Buffer
	formWriter := multipart.NewWriter(&form)
	if err := formWriter.WriteField("namespace", "team"); err != nil {
		t.Fatal(err)
	}
	filePart, err := formWriter.CreateFormFile("archive", "sample.zip")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := filePart.Write(archive.Bytes()); err != nil {
		t.Fatal(err)
	}
	if err := formWriter.Close(); err != nil {
		t.Fatal(err)
	}

	request, err := http.NewRequest(http.MethodPost, base+"/candidates", &form)
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Content-Type", formWriter.FormDataContentType())
	request.Header.Set("X-CSRF-Token", token)
	request.AddCookie(cookies[0])
	created, err := client.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(created.Body)
	_ = created.Body.Close()
	if created.StatusCode != http.StatusSeeOther {
		t.Fatalf("POST /candidates status = %d: %s", created.StatusCode, body)
	}

	reviewRequest, err := http.NewRequest(http.MethodGet, base+created.Header.Get("Location"), nil)
	if err != nil {
		t.Fatal(err)
	}
	reviewRequest.AddCookie(cookies[0])
	review, err := client.Do(reviewRequest)
	if err != nil {
		t.Fatal(err)
	}
	reviewBody, err := io.ReadAll(review.Body)
	_ = review.Body.Close()
	if err != nil {
		t.Fatal(err)
	}
	if review.StatusCode != http.StatusOK {
		t.Fatalf("GET review status = %d: %s", review.StatusCode, reviewBody)
	}
	if !strings.Contains(string(reviewBody), "dev@localhost") {
		t.Fatalf("review page does not attribute the upload to dev@localhost: %s", reviewBody)
	}
}
