package daemon

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"
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

	config, err := ConfigFromEnv()
	if err != nil {
		t.Fatal(err)
	}
	if config.StateDir != stateDir {
		t.Fatalf("state directory = %q, want %q", config.StateDir, stateDir)
	}
	if config.Hostname != defaultHostname || config.AuthKey != "tskey-auth-test" || config.Tag != "tag:skills-registry" {
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
	if _, err := ConfigFromEnv(); err == nil || !strings.Contains(err.Error(), "TS_SKILLSD_STATE_DIR") {
		t.Fatalf("ConfigFromEnv error = %v", err)
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
