package server

import (
	"bytes"
	"context"
	"encoding/json"
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

	"tailscale.com/ipn"
	"tailscale.com/types/key"
	"tailscale.com/types/persist"
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
	server := newHTTPServer(context.Background(), http.NotFoundHandler(), newHandlerGate(nil))
	if server.BaseContext == nil {
		t.Error("HTTP server has no base context for handler cancellation")
	}
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

func TestServeBoundsDrainWhenHandlerIgnoresShutdown(t *testing.T) {
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
	handlerReturned := make(chan struct{})
	tailnetClosed := make(chan struct{})
	storageClosed := make(chan struct{})
	handler := http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		record("handler-started")
		close(handlerStarted)
		defer close(handlerReturned)
		defer record("tree-closed")
		<-releaseHandler // ignores r.Context() like a pre-hardening handler
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
	active := runtime{
		listener: listener,
		handler:  handler,
		close:    cleanup.close,
	}

	ctx, cancel := context.WithCancel(context.Background())
	runResult := make(chan error, 1)
	go func() {
		runResult <- serveWithHTTPShutdownTimeout(ctx, active, 20*time.Millisecond)
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

	shutdownStarted := time.Now()
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

	// The stuck handler must not hold the daemon open: run returns once the
	// drain bound expires, reporting both the forced HTTP shutdown and the
	// bounded drain.
	select {
	case err := <-runResult:
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("run error = %v, want HTTP shutdown deadline", err)
		}
		if !strings.Contains(err.Error(), "handler drain exceeded 20ms") {
			t.Fatalf("run error = %v, want the drain bound reported", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("daemon hung on a handler that ignores shutdown")
	}
	if elapsed := time.Since(shutdownStarted); elapsed > 2*time.Second {
		t.Fatalf("shutdown took %s, want a generous multiple of the 20ms bound", elapsed)
	}

	// Cleanup ran while the stuck handler was still blocked.
	select {
	case <-tailnetClosed:
	default:
		t.Fatal("Tailnet runtime was not closed after the drain bound expired")
	}
	select {
	case <-storageClosed:
	default:
		t.Fatal("storage was not closed after the drain bound expired")
	}

	close(releaseHandler)
	select {
	case <-handlerReturned:
	case <-time.After(5 * time.Second):
		t.Fatal("stuck handler did not return after release")
	}

	eventMu.Lock()
	gotEvents := append([]string(nil), events...)
	eventMu.Unlock()
	if gotEvents[0] != "handler-started" {
		t.Fatalf("lifecycle events = %v, want handler-started first", gotEvents)
	}
	tailnetIndex := indexOf(gotEvents, "tailnet-closed")
	if tailnetIndex < 0 || !containsAll(gotEvents, "listener-closed", "connection-forced-closed", "storage-closed", "handler-finished", "tree-closed") {
		t.Fatalf("lifecycle events = %v, want the full lifecycle set", gotEvents)
	}
	if gotEvents[tailnetIndex+1] != "storage-closed" {
		t.Fatalf("lifecycle events = %v, want storage-closed right after tailnet-closed", gotEvents)
	}
	if indexOf(gotEvents, "handler-finished") < tailnetIndex {
		t.Fatalf("lifecycle events = %v, want the stuck handler abandoned before it finished", gotEvents)
	}
}

func TestServeDrainsHandlerObservingRequestContext(t *testing.T) {
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
	listener := &recordingListener{Listener: baseListener, onClose: func() {
		record("listener-closed")
	}}
	handlerStarted := make(chan struct{})
	handlerReturned := make(chan struct{})
	tailnetClosed := make(chan struct{})
	handler := http.HandlerFunc(func(_ http.ResponseWriter, request *http.Request) {
		record("handler-started")
		close(handlerStarted)
		defer close(handlerReturned)
		defer record("tree-closed")
		<-request.Context().Done()
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
			return nil
		},
	}
	active := runtime{
		listener: listener,
		handler:  handler,
		close:    cleanup.close,
	}

	ctx, cancel := context.WithCancel(context.Background())
	runResult := make(chan error, 1)
	go func() {
		runResult <- serveWithHTTPShutdownTimeout(ctx, active, 20*time.Millisecond)
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
	case err := <-runResult:
		if err != nil && strings.Contains(err.Error(), "handler drain exceeded") {
			t.Fatalf("run error = %v, want no drain error for a context-observing handler", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("daemon did not drain a context-observing handler")
	}
	select {
	case <-handlerReturned:
	case <-time.After(5 * time.Second):
		t.Fatal("context-observing handler was abandoned")
	}
	// The handler drains within the graceful window, so the client gets a
	// completed response instead of a force-closed connection.
	if err := <-responseResult; err != nil {
		t.Fatalf("gracefully drained request failed: %v", err)
	}

	eventMu.Lock()
	gotEvents := append([]string(nil), events...)
	eventMu.Unlock()
	if !containsAll(gotEvents, "handler-started", "listener-closed", "handler-finished", "tree-closed", "tailnet-closed", "storage-closed") {
		t.Fatalf("lifecycle events = %v, want the full lifecycle set", gotEvents)
	}
	if tailnetIndex := indexOf(gotEvents, "tailnet-closed"); indexOf(gotEvents, "handler-finished") > tailnetIndex || indexOf(gotEvents, "tree-closed") > tailnetIndex {
		t.Fatalf("lifecycle events = %v, want the handler drained before runtime cleanup", gotEvents)
	}
}

func indexOf(events []string, want string) int {
	for index, event := range events {
		if event == want {
			return index
		}
	}
	return -1
}

func containsAll(events []string, wants ...string) bool {
	for _, want := range wants {
		if indexOf(events, want) < 0 {
			return false
		}
	}
	return true
}

func TestServeRejectsDispatchPausedBeforeAdmissionDuringShutdown(t *testing.T) {
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
	active := runtime{
		listener: baseListener,
		handler: http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
			handlerCalled <- struct{}{}
		}),
		close: func() error {
			close(runtimeClosed)
			return nil
		},
	}

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	runResult := make(chan error, 1)
	go func() {
		runResult <- serveWithHandlerGate(ctx, active, 20*time.Millisecond, gate)
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

func TestCloseRuntimeRetriesFailedCleanup(t *testing.T) {
	attempts := 0
	closeErr := errors.New("close failed")
	active := runtime{close: func() error {
		attempts++
		if attempts == 1 {
			return closeErr
		}
		return nil
	}}
	if err := closeRuntime(active); !errors.Is(err, closeErr) {
		t.Fatalf("close runtime error = %v, want close failure", err)
	}
	if attempts != 2 {
		t.Fatalf("cleanup attempts = %d, want 2", attempts)
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
				return errTreesOpen
			}
			return nil
		},
	}

	if err := cleanup.close(); !errors.Is(err, errTreesOpen) {
		t.Fatalf("first cleanup error = %v, want errTreesOpen", err)
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

func TestServeGracefullyStopsHTTPBeforeRuntimeCleanup(t *testing.T) {
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
	active := runtime{
		listener: listener,
		handler:  handler,
		close: func() error {
			record("runtime-closed")
			close(runtimeClosed)
			return nil
		},
	}

	ctx, cancel := context.WithCancel(context.Background())
	runResult := make(chan error, 1)
	go func() {
		runResult <- serve(ctx, active)
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

func TestServeClosesRuntimeAfterServeFailure(t *testing.T) {
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
	active := runtime{
		listener: listener,
		handler:  http.NotFoundHandler(),
		close: func() error {
			events = append(events, "runtime-closed")
			return nil
		},
	}

	err = serve(context.Background(), active)
	if err == nil || !strings.Contains(err.Error(), "serve Tailnet HTTP") {
		t.Fatalf("run error = %v, want serve failure", err)
	}
	if want := []string{"listener-closed", "runtime-closed"}; !reflect.DeepEqual(events, want) {
		t.Fatalf("cleanup events = %v, want %v", events, want)
	}
}

func writeEnrolledTSNetState(t *testing.T, stateDir string) {
	t.Helper()
	prefs := ipn.NewPrefs()
	prefs.Persist = &persist.Persist{PrivateNodeKey: key.NewNode()}
	profileKey := ipn.StateKey("profile-test")
	state, err := json.Marshal(map[string][]byte{
		string(ipn.CurrentProfileKey("")): []byte(profileKey),
		string(profileKey):                prefs.ToBytes(),
	})
	if err != nil {
		t.Fatal(err)
	}
	statePath := filepath.Join(stateDir, "tsnet", "tailscaled.state")
	if err := os.MkdirAll(filepath.Dir(statePath), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(statePath, state, 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestClearTsnetCredentialEnvironment(t *testing.T) {
	t.Setenv("TS_AUTHKEY", "tskey-auth-test")
	for _, variable := range unsupportedTsnetCredentials {
		t.Setenv(variable, "configured")
	}

	clearTsnetCredentialEnvironment()

	if value := os.Getenv("TS_AUTHKEY"); value != "" {
		t.Fatalf("TS_AUTHKEY = %q after cleanup", value)
	}
	for _, variable := range unsupportedTsnetCredentials {
		if value := os.Getenv(variable); value != "" {
			t.Errorf("%s = %q after cleanup", variable, value)
		}
	}
}

func TestConfigFromEnvReadsEnrollmentKeyOnce(t *testing.T) {
	stateDir := t.TempDir()
	t.Setenv("TS_AUTHKEY", "")
	for _, variable := range unsupportedTsnetCredentials {
		t.Setenv(variable, "")
	}
	authKeyPath := filepath.Join(t.TempDir(), "auth.key")
	if err := os.WriteFile(authKeyPath, []byte("  tskey-auth-test\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("TS_SKILLSD_STATE_DIR", stateDir)
	t.Setenv("TS_SKILLSD_HOSTNAME", "")
	t.Setenv("TS_SKILLSD_AUTHKEY_FILE", authKeyPath)
	t.Setenv("TS_SKILLSD_TAG", "tag:skills-registry")
	t.Setenv("TS_SKILLSD_VERBOSE", "true")

	config, err := configFromEnv()
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
	t.Setenv("TS_AUTHKEY", "")
	for _, variable := range unsupportedTsnetCredentials {
		t.Setenv(variable, "")
	}
	t.Setenv("TS_SKILLSD_HOSTNAME", "")
	t.Setenv("TS_SKILLSD_AUTHKEY_FILE", "")
	t.Setenv("TS_SKILLSD_TAG", "")
	t.Setenv("TS_SKILLSD_VERBOSE", "")
	if _, err := configFromEnv(); err == nil || !strings.Contains(err.Error(), "TS_SKILLSD_STATE_DIR") {
		t.Fatalf("configFromEnv error = %v", err)
	}
}

func TestConfigFromEnvParsesVerbose(t *testing.T) {
	t.Setenv("TS_AUTHKEY", "tskey-auth-test")
	for _, variable := range unsupportedTsnetCredentials {
		t.Setenv(variable, "")
	}
	for value, want := range map[string]bool{"": false, "0": false, "false": false, "1": true, "true": true} {
		t.Setenv("TS_SKILLSD_STATE_DIR", t.TempDir())
		t.Setenv("TS_SKILLSD_VERBOSE", value)
		config, err := configFromEnv()
		if err != nil {
			t.Fatalf("configFromEnv with TS_SKILLSD_VERBOSE=%q: %v", value, err)
		}
		if config.Verbose != want {
			t.Errorf("configFromEnv with TS_SKILLSD_VERBOSE=%q: verbose = %v, want %v", value, config.Verbose, want)
		}
	}
	t.Setenv("TS_SKILLSD_STATE_DIR", t.TempDir())
	t.Setenv("TS_SKILLSD_VERBOSE", "maybe")
	if _, err := configFromEnv(); err == nil || !strings.Contains(err.Error(), "TS_SKILLSD_VERBOSE") {
		t.Errorf("configFromEnv accepted a non-boolean TS_SKILLSD_VERBOSE: %v", err)
	}
}

func TestConfigFromEnvResolvesEnrollmentCredentials(t *testing.T) {
	setEnv := func(t *testing.T, stateDir string) {
		t.Helper()
		t.Setenv("TS_SKILLSD_STATE_DIR", stateDir)
		t.Setenv("TS_SKILLSD_HOSTNAME", "")
		t.Setenv("TS_SKILLSD_TAG", "")
		t.Setenv("TS_SKILLSD_VERBOSE", "")
		t.Setenv("TS_SKILLSD_AUTHKEY_FILE", "")
		t.Setenv("TS_AUTHKEY", "")
		for _, variable := range unsupportedTsnetCredentials {
			t.Setenv(variable, "")
		}
	}

	t.Run("file takes precedence over TS_AUTHKEY", func(t *testing.T) {
		stateDir := t.TempDir()
		setEnv(t, stateDir)
		authKeyPath := filepath.Join(t.TempDir(), "auth.key")
		if err := os.WriteFile(authKeyPath, []byte("tskey-auth-file"), 0o600); err != nil {
			t.Fatal(err)
		}
		t.Setenv("TS_SKILLSD_AUTHKEY_FILE", authKeyPath)
		t.Setenv("TS_AUTHKEY", "tskey-auth-environment")

		config, err := configFromEnv()
		if err != nil {
			t.Fatal(err)
		}
		if config.AuthKey != "tskey-auth-file" {
			t.Fatalf("auth key = %q, want file value", config.AuthKey)
		}
	})

	t.Run("TS_AUTHKEY supplies first enrollment", func(t *testing.T) {
		setEnv(t, t.TempDir())
		t.Setenv("TS_AUTHKEY", " tskey-auth-environment\n")

		config, err := configFromEnv()
		if err != nil {
			t.Fatal(err)
		}
		if config.AuthKey != "tskey-auth-environment" {
			t.Fatalf("auth key = %q, want environment value", config.AuthKey)
		}
	})

	t.Run("empty file fails instead of falling back to TS_AUTHKEY", func(t *testing.T) {
		setEnv(t, t.TempDir())
		authKeyPath := filepath.Join(t.TempDir(), "auth.key")
		if err := os.WriteFile(authKeyPath, nil, 0o600); err != nil {
			t.Fatal(err)
		}
		t.Setenv("TS_SKILLSD_AUTHKEY_FILE", authKeyPath)
		t.Setenv("TS_AUTHKEY", "tskey-auth-environment")

		_, err := configFromEnv()
		if err == nil || !strings.Contains(err.Error(), "is empty") {
			t.Fatalf("configFromEnv error = %v, want empty file failure", err)
		}
	})

	t.Run("supported credential wins over unsupported settings", func(t *testing.T) {
		setEnv(t, t.TempDir())
		t.Setenv("TS_AUTHKEY", "tskey-auth-environment")
		t.Setenv("TS_CLIENT_SECRET", "tskey-client-secret")

		config, err := configFromEnv()
		if err != nil {
			t.Fatal(err)
		}
		if config.AuthKey != "tskey-auth-environment" {
			t.Fatalf("auth key = %q, want supported credential", config.AuthKey)
		}
	})

	t.Run("stored node key needs no credential", func(t *testing.T) {
		stateDir := t.TempDir()
		setEnv(t, stateDir)
		writeEnrolledTSNetState(t, stateDir)

		config, err := configFromEnv()
		if err != nil {
			t.Fatal(err)
		}
		if config.AuthKey != "" {
			t.Fatalf("auth key = %q, want no first-enrollment key", config.AuthKey)
		}
	})

	t.Run("unsupported credential fails without stored state", func(t *testing.T) {
		setEnv(t, t.TempDir())
		t.Setenv("TS_CLIENT_SECRET", "tskey-client-secret")

		_, err := configFromEnv()
		if err == nil || !strings.Contains(err.Error(), "TS_CLIENT_SECRET") {
			t.Fatalf("configFromEnv error = %v, want unsupported variable named", err)
		}
	})

	t.Run("unsupported credential is ignored with stored state", func(t *testing.T) {
		stateDir := t.TempDir()
		setEnv(t, stateDir)
		writeEnrolledTSNetState(t, stateDir)
		t.Setenv("TS_AUTH_KEY", "tskey-auth-legacy")

		if _, err := configFromEnv(); err != nil {
			t.Fatalf("configFromEnv with stored state: %v", err)
		}
	})

	t.Run("machine key without enrollment fails", func(t *testing.T) {
		stateDir := t.TempDir()
		setEnv(t, stateDir)
		statePath := filepath.Join(stateDir, "tsnet", "tailscaled.state")
		if err := os.MkdirAll(filepath.Dir(statePath), 0o700); err != nil {
			t.Fatal(err)
		}
		state, err := json.Marshal(map[string][]byte{"_machinekey": []byte("private-machine-key")})
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(statePath, state, 0o600); err != nil {
			t.Fatal(err)
		}

		_, err = configFromEnv()
		if err == nil || !strings.Contains(err.Error(), "first enrollment requires") {
			t.Fatalf("configFromEnv error = %v, want missing enrollment credentials", err)
		}
	})

	t.Run("missing credential and state fails", func(t *testing.T) {
		setEnv(t, t.TempDir())
		_, err := configFromEnv()
		if err == nil || !strings.Contains(err.Error(), "first enrollment requires") {
			t.Fatalf("configFromEnv error = %v, want missing enrollment credentials", err)
		}
	})
}

func TestPersistentcsrfKey(t *testing.T) {
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

func TestPersistentcsrfKeyRejectsMalformedFile(t *testing.T) {
	stateDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(stateDir, "csrf.key"), []byte("short"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := loadOrCreateCSRFKey(stateDir); err == nil {
		t.Fatal("malformed CSRF key was accepted")
	}
}

func TestRunDevRejectsInvalidConfigBeforeRuntimeConstruction(t *testing.T) {
	started := false
	err := RunDev(context.Background(), DevConfig{
		StateDir: t.TempDir(),
		Listen:   "0.0.0.0:0",
		Started: func(net.Addr) {
			started = true
		},
	})
	if err == nil || !strings.Contains(err.Error(), "must use a loopback host") {
		t.Fatalf("RunDev error = %v, want loopback validation failure", err)
	}
	if started {
		t.Fatal("RunDev started a listener after rejecting its config")
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
	if _, err := devConfigFromEnv(); err == nil {
		t.Fatal("dev config accepted an enrollment auth key file")
	}
}

func TestDevConfigFromEnvRefusesStandardEnrollmentKey(t *testing.T) {
	t.Setenv("TS_SKILLSD_DEV_LISTEN", "127.0.0.1:8080")
	t.Setenv("TS_SKILLSD_STATE_DIR", t.TempDir())
	t.Setenv("TS_SKILLSD_AUTHKEY_FILE", "")
	t.Setenv("TS_AUTHKEY", "tskey-auth-test")
	if _, err := devConfigFromEnv(); err == nil || !strings.Contains(err.Error(), "TS_AUTHKEY") {
		t.Fatalf("dev config accepted TS_AUTHKEY: %v", err)
	}
}

func TestDevConfigFromEnvDefaultsToLoopbackListen(t *testing.T) {
	t.Setenv("TS_SKILLSD_DEV_LISTEN", "")
	t.Setenv("TS_SKILLSD_AUTHKEY_FILE", "")
	t.Setenv("TS_AUTHKEY", "")
	t.Setenv("TS_SKILLSD_STATE_DIR", t.TempDir())
	config, err := devConfigFromEnv()
	if err != nil {
		t.Fatal(err)
	}
	if config.Listen != "127.0.0.1:8080" {
		t.Fatalf("default dev listen = %q", config.Listen)
	}
	if !config.EphemeralFallback {
		t.Fatal("defaulted dev listen did not enable ephemeral fallback")
	}

	t.Setenv("TS_SKILLSD_DEV_LISTEN", "127.0.0.1:9000")
	explicit, err := devConfigFromEnv()
	if err != nil {
		t.Fatal(err)
	}
	if explicit.EphemeralFallback {
		t.Fatal("explicit dev listen enabled ephemeral fallback")
	}
}

func TestDevRuntimeBusyDefaultPortFallsBackToEphemeral(t *testing.T) {
	squatter, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = squatter.Close() }()
	busy := squatter.Addr().String()

	var started net.Addr
	rt, err := buildDevRuntime(context.Background(), DevConfig{
		StateDir: t.TempDir(), Listen: busy, EphemeralFallback: true,
		Started: func(address net.Addr) { started = address },
	})
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := rt.close(); err != nil {
			t.Errorf("close dev runtime: %v", err)
		}
	}()
	bound := rt.listener.Addr().String()
	if bound == busy {
		t.Fatalf("fallback bound the busy address %q", busy)
	}
	if started == nil || started.String() != bound {
		t.Fatalf("Started hook reported %v, listener bound %q", started, bound)
	}
}

func TestDevRuntimeBusyExplicitPortFails(t *testing.T) {
	squatter, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = squatter.Close() }()

	rt, err := buildDevRuntime(context.Background(), DevConfig{StateDir: t.TempDir(), Listen: squatter.Addr().String()})
	if err == nil {
		_ = rt.close()
		t.Fatal("buildDevRuntime bound an explicitly requested busy address")
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

	var form bytes.Buffer
	formWriter := multipart.NewWriter(&form)
	if err := formWriter.WriteField("namespace", "team"); err != nil {
		t.Fatal(err)
	}
	if err := formWriter.WriteField("manifest", `[{"index":0,"path":"sample/SKILL.md","size":62}]`); err != nil {
		t.Fatal(err)
	}
	filePart, err := formWriter.CreateFormFile("file-0", "SKILL.md")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := io.WriteString(filePart, "---\nname: sample\ndescription: Dev mode test\n---\nInstructions.\n"); err != nil {
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
