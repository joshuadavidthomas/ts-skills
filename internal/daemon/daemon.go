package daemon

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
	"unicode"

	"github.com/joshuadavidthomas/ts-skills/internal/registry"
	"github.com/joshuadavidthomas/ts-skills/internal/safetree"
	"github.com/joshuadavidthomas/ts-skills/internal/storage"
	"github.com/joshuadavidthomas/ts-skills/internal/tailnet"
	"github.com/joshuadavidthomas/ts-skills/internal/web"
	"tailscale.com/ipn"
	"tailscale.com/types/persist"
)

const (
	defaultHostname     = "ts-skillsd"
	readHeaderTimeout   = 10 * time.Second
	readTimeout         = 5 * time.Minute
	writeTimeout        = 5 * time.Minute
	idleTimeout         = 5 * time.Minute
	shutdownTimeout     = 30 * time.Second
	maxRequestBodyBytes = int64(32 << 20)
)

type Config struct {
	StateDir string
	Hostname string
	AuthKey  string
	Tag      string
	Verbose  bool
}

// DevConfig serves the registry over plain HTTP on a loopback address with a
// fixed dev actor. It exists only for local development; it never opens a
// Tailnet node and must never be reachable from other machines.
type DevConfig struct {
	StateDir string
	Listen   string
	// EphemeralFallback retries on port 0 when Listen is busy. It is set only
	// when the listen address was defaulted, so an explicit address that is
	// busy still fails loudly.
	EphemeralFallback bool
	Started           func(net.Addr)
}

// DevModeFromEnv reports whether TS_SKILLSD_DEV enables dev mode.
func DevModeFromEnv() (bool, error) {
	value := os.Getenv("TS_SKILLSD_DEV")
	if value == "" {
		return false, nil
	}
	enabled, err := strconv.ParseBool(value)
	if err != nil {
		return false, fmt.Errorf("TS_SKILLSD_DEV must be a boolean value such as 1 or true")
	}
	return enabled, nil
}

func DevConfigFromEnv() (DevConfig, error) {
	listen := os.Getenv("TS_SKILLSD_DEV_LISTEN")
	ephemeralFallback := listen == ""
	if listen == "" {
		listen = "127.0.0.1:8080"
	}
	if os.Getenv("TS_SKILLSD_AUTHKEY_FILE") != "" {
		return DevConfig{}, fmt.Errorf("dev mode does not enroll a Tailnet node; unset TS_SKILLSD_AUTHKEY_FILE")
	}
	if os.Getenv("TS_AUTHKEY") != "" {
		return DevConfig{}, fmt.Errorf("dev mode does not enroll a Tailnet node; unset TS_AUTHKEY")
	}
	stateDir := os.Getenv("TS_SKILLSD_STATE_DIR")
	if stateDir == "" {
		cache, err := os.UserCacheDir()
		if err != nil {
			return DevConfig{}, fmt.Errorf("resolve default dev state directory: %w", err)
		}
		stateDir = filepath.Join(cache, "ts-skillsd-dev")
	}
	return normalizeDevConfig(DevConfig{StateDir: stateDir, Listen: listen, EphemeralFallback: ephemeralFallback})
}

func normalizeDevConfig(config DevConfig) (DevConfig, error) {
	if strings.TrimSpace(config.StateDir) == "" {
		return DevConfig{}, fmt.Errorf("dev state directory is required")
	}
	absolute, err := filepath.Abs(config.StateDir)
	if err != nil {
		return DevConfig{}, fmt.Errorf("resolve dev state directory: %w", err)
	}
	config.StateDir = absolute

	host, port, err := net.SplitHostPort(config.Listen)
	if err != nil {
		return DevConfig{}, fmt.Errorf("dev listen address must be host:port: %w", err)
	}
	if port == "" {
		return DevConfig{}, fmt.Errorf("dev listen address %q must include a port", config.Listen)
	}
	if !isLoopbackHost(host) {
		return DevConfig{}, fmt.Errorf("dev listen address %q must use a loopback host", config.Listen)
	}
	return config, nil
}

func isLoopbackHost(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	address := net.ParseIP(host)
	return address != nil && address.IsLoopback()
}

// ConfigFromEnv reads daemon configuration. For first enrollment, it resolves
// TS_SKILLSD_AUTHKEY_FILE before TS_AUTHKEY. Later starts need neither value
// when the tsnet state file already contains an enrolled node key.
func ConfigFromEnv() (Config, error) {
	config := Config{
		StateDir: os.Getenv("TS_SKILLSD_STATE_DIR"),
		Hostname: os.Getenv("TS_SKILLSD_HOSTNAME"),
		Tag:      os.Getenv("TS_SKILLSD_TAG"),
	}
	if config.Hostname == "" {
		config.Hostname = defaultHostname
	}
	if value := os.Getenv("TS_SKILLSD_VERBOSE"); value != "" {
		verbose, err := strconv.ParseBool(value)
		if err != nil {
			return Config{}, fmt.Errorf("TS_SKILLSD_VERBOSE must be a boolean value such as 1 or true")
		}
		config.Verbose = verbose
	}
	var err error
	config, err = normalizeConfig(config)
	if err != nil {
		return Config{}, err
	}
	config.AuthKey, err = authKeyFromEnv()
	if err != nil {
		return Config{}, err
	}
	if config.AuthKey != "" {
		return normalizeConfig(config)
	}

	hasState, err := tsnetStateHasNodeKey(filepath.Join(config.StateDir, "tsnet", "tailscaled.state"))
	if err != nil {
		return Config{}, err
	}
	if hasState {
		return config, nil
	}
	if variable := unsupportedTsnetCredential(); variable != "" {
		return Config{}, fmt.Errorf("%s is not supported for ts-skillsd enrollment; use TS_SKILLSD_AUTHKEY_FILE or TS_AUTHKEY", variable)
	}
	return Config{}, fmt.Errorf("first enrollment requires TS_SKILLSD_AUTHKEY_FILE or TS_AUTHKEY; no node key exists in %q", filepath.Join(config.StateDir, "tsnet", "tailscaled.state"))
}

func authKeyFromEnv() (string, error) {
	if authKeyFile := os.Getenv("TS_SKILLSD_AUTHKEY_FILE"); authKeyFile != "" {
		contents, err := os.ReadFile(authKeyFile)
		if err != nil {
			return "", fmt.Errorf("read TS_SKILLSD_AUTHKEY_FILE %q: %w", authKeyFile, err)
		}
		authKey := strings.TrimSpace(string(contents))
		if authKey == "" {
			return "", fmt.Errorf("TS_SKILLSD_AUTHKEY_FILE %q is empty", authKeyFile)
		}
		return authKey, nil
	}
	return strings.TrimSpace(os.Getenv("TS_AUTHKEY")), nil
}

func tsnetStateHasNodeKey(path string) (bool, error) {
	contents, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("read tsnet state %q: %w", path, err)
	}
	var state map[string][]byte
	if err := json.Unmarshal(contents, &state); err != nil {
		return false, fmt.Errorf("parse tsnet state %q: %w", path, err)
	}
	profileKey := ipn.StateKey(state[string(ipn.CurrentProfileKey(""))])
	if profileKey == "" {
		profileKey = ipn.LegacyGlobalDaemonStateKey
	}
	profile, found := state[string(profileKey)]
	if !found || len(profile) == 0 {
		return false, nil
	}
	prefs := ipn.NewPrefs()
	prefs.Persist = &persist.Persist{}
	if err := ipn.PrefsFromBytes(profile, prefs); err != nil {
		return false, fmt.Errorf("parse tsnet profile %q in %q: %w", profileKey, path, err)
	}
	return !prefs.Persist.PrivateNodeKey.IsZero(), nil
}

var unsupportedTsnetCredentials = []string{
	"TS_AUTH_KEY",
	"TS_CLIENT_SECRET",
	"TS_CLIENT_ID",
	"TS_ID_TOKEN",
	"TS_AUDIENCE",
	"TSNET_FORCE_LOGIN",
	"TS_CONTROL_URL",
}

func unsupportedTsnetCredential() string {
	for _, variable := range unsupportedTsnetCredentials {
		if os.Getenv(variable) != "" {
			return variable
		}
	}
	return ""
}

func clearTsnetCredentialEnvironment() {
	for _, variable := range append([]string{"TS_AUTHKEY"}, unsupportedTsnetCredentials...) {
		_ = os.Unsetenv(variable)
	}
}

func normalizeConfig(config Config) (Config, error) {
	if strings.TrimSpace(config.StateDir) == "" {
		return Config{}, fmt.Errorf("TS_SKILLSD_STATE_DIR is required")
	}
	absolute, err := filepath.Abs(config.StateDir)
	if err != nil {
		return Config{}, fmt.Errorf("resolve daemon state directory: %w", err)
	}
	config.StateDir = absolute

	if !validHostname(config.Hostname) {
		return Config{}, fmt.Errorf("TS_SKILLSD_HOSTNAME must be a DNS label of at most 63 characters")
	}
	if config.Tag != "" && !validTag(config.Tag) {
		return Config{}, fmt.Errorf("TS_SKILLSD_TAG must have the form tag:name using letters, numbers, and hyphens")
	}
	if strings.IndexFunc(config.AuthKey, unicode.IsControl) >= 0 {
		return Config{}, fmt.Errorf("tailnet auth key contains a control character")
	}
	return config, nil
}

func validHostname(hostname string) bool {
	if len(hostname) == 0 || len(hostname) > 63 || hostname[0] == '-' || hostname[len(hostname)-1] == '-' {
		return false
	}
	for _, char := range hostname {
		if (char >= 'a' && char <= 'z') || (char >= 'A' && char <= 'Z') || (char >= '0' && char <= '9') || char == '-' {
			continue
		}
		return false
	}
	return true
}

func validTag(tag string) bool {
	name, found := strings.CutPrefix(tag, "tag:")
	if !found || name == "" || !isASCIILetter(name[0]) {
		return false
	}
	for index := range len(name) {
		char := name[index]
		if isASCIILetter(char) || (char >= '0' && char <= '9') || char == '-' {
			continue
		}
		return false
	}
	return name[len(name)-1] != '-'
}

func isASCIILetter(char byte) bool {
	return (char >= 'a' && char <= 'z') || (char >= 'A' && char <= 'Z')
}

type runtime struct {
	listener net.Listener
	handler  http.Handler
	close    func() error
}

func Run(ctx context.Context, config Config) error {
	if ctx == nil {
		return fmt.Errorf("run daemon: context must be provided")
	}
	config, err := normalizeConfig(config)
	if err != nil {
		return err
	}
	active, err := buildRuntime(ctx, config)
	if err != nil {
		return fmt.Errorf("build daemon runtime: %w", err)
	}
	return serve(ctx, active)
}

func RunDev(ctx context.Context, config DevConfig) error {
	if ctx == nil {
		return fmt.Errorf("run daemon: context must be provided")
	}
	config, err := normalizeDevConfig(config)
	if err != nil {
		return err
	}
	active, err := buildDevRuntime(ctx, config)
	if err != nil {
		return fmt.Errorf("build daemon runtime: %w", err)
	}
	return serve(ctx, active)
}

func serve(ctx context.Context, active runtime) error {
	return serveWithHTTPShutdownTimeout(ctx, active, shutdownTimeout)
}

func serveWithHTTPShutdownTimeout(ctx context.Context, active runtime, timeout time.Duration) error {
	return serveWithHandlerGate(ctx, active, timeout, newHandlerGate(nil))
}

func serveWithHandlerGate(ctx context.Context, active runtime, timeout time.Duration, handlers *handlerGate) (err error) {
	defer func() {
		err = errors.Join(err, active.close())
	}()

	// serverCtx owns every admitted request's context; cancelling it after
	// the HTTP shutdown finishes unblocks handlers still observing
	// r.Context(), so the bounded drain below can complete.
	serverCtx, cancelServerWork := context.WithCancel(ctx)
	defer cancelServerWork()

	server := newHTTPServer(serverCtx, active.handler, handlers)
	serveResult := make(chan error, 1)
	go func() {
		serveResult <- server.Serve(active.listener)
	}()

	var serveErr error
	var shutdownErr error
	select {
	case serveErr = <-serveResult:
		handlers.closeAdmission()
		shutdownErr = shutdownHTTP(server, timeout)
		cancelServerWork()
		if errors.Is(serveErr, http.ErrServerClosed) {
			serveErr = nil
		} else if serveErr != nil {
			serveErr = fmt.Errorf("serve Tailnet HTTP: %w", serveErr)
		}
	case <-ctx.Done():
		handlers.closeAdmission()
		shutdownErr = shutdownHTTP(server, timeout)
		cancelServerWork()
		serveErr = <-serveResult
		if errors.Is(serveErr, http.ErrServerClosed) {
			serveErr = nil
		} else if serveErr != nil {
			serveErr = fmt.Errorf("serve Tailnet HTTP during shutdown: %w", serveErr)
		}
	}
	// The drain is bounded by the same deadline as the HTTP shutdown: a
	// handler that ignores its context must not hold the process open
	// forever. The waiter channel is buffered so an abandoned waiter
	// goroutine always sends and exits.
	drained := make(chan struct{}, 1)
	go func() {
		handlers.wait()
		drained <- struct{}{}
	}()
	var drainErr error
	select {
	case <-drained:
	case <-time.After(timeout):
		drainErr = fmt.Errorf("handler drain exceeded %s: abandoning stuck handlers", timeout)
	}
	return errors.Join(shutdownErr, serveErr, drainErr)
}

func newHTTPServer(baseCtx context.Context, handler http.Handler, handlers *handlerGate) *http.Server {
	return &http.Server{
		BaseContext: func(net.Listener) context.Context { return baseCtx },
		Handler: http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
			if !handlers.admit() {
				writer.Header().Set("Connection", "close")
				http.Error(writer, "server is shutting down", http.StatusServiceUnavailable)
				return
			}
			defer handlers.done()
			request.Body = http.MaxBytesReader(writer, request.Body, maxRequestBodyBytes)
			handler.ServeHTTP(writer, request)
		}),
		ReadHeaderTimeout: readHeaderTimeout,
		ReadTimeout:       readTimeout,
		WriteTimeout:      writeTimeout,
		IdleTimeout:       idleTimeout,
	}
}

type handlerGate struct {
	mu              sync.Mutex
	drained         *sync.Cond
	admissionOpen   bool
	active          int
	beforeAdmission func()
	afterAdmission  func(bool)
}

func newHandlerGate(beforeAdmission func()) *handlerGate {
	gate := &handlerGate{
		admissionOpen:   true,
		beforeAdmission: beforeAdmission,
	}
	gate.drained = sync.NewCond(&gate.mu)
	return gate
}

func (g *handlerGate) admit() bool {
	if g.beforeAdmission != nil {
		g.beforeAdmission()
	}
	g.mu.Lock()
	admitted := g.admissionOpen
	if admitted {
		g.active++
	}
	g.mu.Unlock()
	if g.afterAdmission != nil {
		g.afterAdmission(admitted)
	}
	return admitted
}

func (g *handlerGate) done() {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.active--
	if g.active == 0 {
		g.drained.Broadcast()
	}
}

func (g *handlerGate) closeAdmission() {
	g.mu.Lock()
	g.admissionOpen = false
	g.mu.Unlock()
}

func (g *handlerGate) wait() {
	g.mu.Lock()
	defer g.mu.Unlock()
	for g.active != 0 {
		g.drained.Wait()
	}
}

func shutdownHTTP(server *http.Server, timeout time.Duration) error {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	if err := server.Shutdown(ctx); err != nil {
		return errors.Join(fmt.Errorf("gracefully shut down Tailnet HTTP: %w", err), server.Close())
	}
	return nil
}

func openRegistryCore(ctx context.Context, stateDir string) (_ *storage.Catalog, _ *registry.Catalog, _ web.CSRFKey, err error) {
	records, err := storage.OpenCatalog(ctx, stateDir)
	if err != nil {
		return nil, nil, web.CSRFKey{}, fmt.Errorf("open registry storage: %w", err)
	}
	defer func() {
		if err != nil {
			err = errors.Join(err, records.Close())
		}
	}()

	csrfKey, err := loadOrCreateCSRFKey(stateDir)
	if err != nil {
		return nil, nil, web.CSRFKey{}, err
	}
	catalog, err := registry.NewCatalog(records, filepath.Join(stateDir, "tmp"), safetree.PrototypeLimits())
	if err != nil {
		return nil, nil, web.CSRFKey{}, fmt.Errorf("construct registry catalog: %w", err)
	}
	return records, catalog, csrfKey, nil
}

// buildRuntime constructs the production runtime from normalized config.
func buildRuntime(ctx context.Context, config Config) (_ runtime, err error) {
	records, catalog, csrfKey, err := openRegistryCore(ctx, config.StateDir)
	if err != nil {
		return runtime{}, err
	}
	closeRecords := true
	defer func() {
		if closeRecords {
			err = errors.Join(err, records.Close())
		}
	}()

	// tsnet also discovers credentials from its process environment. ConfigFromEnv
	// resolved the only supported credential chain, so remove tsnet fallbacks
	// before constructing the embedded node.
	clearTsnetCredentialEnvironment()
	logger := slog.Default()
	var logf func(string, ...any)
	if config.Verbose {
		logf = func(format string, args ...any) {
			logger.Info(fmt.Sprintf(format, args...))
		}
	}
	tailConfig := tailnet.ServerConfig{
		Hostname: config.Hostname,
		StateDir: filepath.Join(config.StateDir, "tsnet"),
		AuthKey:  config.AuthKey,
		Logf:     logf,
	}
	if config.Tag != "" {
		tailConfig.AdvertiseTags = []string{config.Tag}
	}
	tailServer, err := tailnet.ListenTLS(ctx, tailConfig)
	if err != nil {
		return runtime{}, err
	}
	closeTailnet := true
	defer func() {
		if closeTailnet {
			err = errors.Join(err, tailServer.Close())
		}
	}()

	localClient, err := tailServer.LocalClient()
	if err != nil {
		return runtime{}, err
	}
	actors, err := tailnet.NewActorResolver(localClient)
	if err != nil {
		return runtime{}, err
	}
	handler, err := web.NewHandler(catalog, actors, web.Options{
		StagingParent: filepath.Join(config.StateDir, "tmp"),
		Limits:        safetree.PrototypeLimits(),
		CSRFKey:       csrfKey,
		SecureCookies: true,
		Logger:        logger,
	})
	if err != nil {
		return runtime{}, fmt.Errorf("construct registry HTTP handler: %w", err)
	}

	cleanup := &runtimeCleanup{
		closeNetwork: tailServer.Close,
		closeStorage: records.Close,
	}
	closeTailnet = false
	closeRecords = false
	return runtime{
		listener: tailServer.Listener(),
		handler:  handler,
		close:    cleanup.close,
	}, nil
}

// buildDevRuntime constructs the loopback runtime from normalized config.
func buildDevRuntime(ctx context.Context, config DevConfig) (_ runtime, err error) {
	records, catalog, csrfKey, err := openRegistryCore(ctx, config.StateDir)
	if err != nil {
		return runtime{}, err
	}
	closeRecords := true
	defer func() {
		if closeRecords {
			err = errors.Join(err, records.Close())
		}
	}()

	actor, err := registry.NewActor("dev", "dev@localhost")
	if err != nil {
		return runtime{}, fmt.Errorf("construct dev actor: %w", err)
	}
	handler, err := web.NewHandler(catalog, staticCuratorResolver{curator: registry.NewCurator(actor)}, web.Options{
		StagingParent: filepath.Join(config.StateDir, "tmp"),
		Limits:        safetree.PrototypeLimits(),
		CSRFKey:       csrfKey,
		SecureCookies: false,
		Logger:        slog.Default(),
	})
	if err != nil {
		return runtime{}, fmt.Errorf("construct registry HTTP handler: %w", err)
	}

	listener, err := net.Listen("tcp", config.Listen)
	if err != nil && config.EphemeralFallback && errors.Is(err, syscall.EADDRINUSE) {
		host, _, splitErr := net.SplitHostPort(config.Listen)
		if splitErr != nil {
			return runtime{}, fmt.Errorf("listen on loopback address %q: %w", config.Listen, err)
		}
		listener, err = net.Listen("tcp", net.JoinHostPort(host, "0"))
	}
	if err != nil {
		return runtime{}, fmt.Errorf("listen on loopback address %q: %w", config.Listen, err)
	}
	if config.Started != nil {
		config.Started(listener.Addr())
	}

	cleanup := &runtimeCleanup{
		closeNetwork: func() error {
			if err := listener.Close(); err != nil && !errors.Is(err, net.ErrClosed) {
				return err
			}
			return nil
		},
		closeStorage: records.Close,
	}
	closeRecords = false
	return runtime{
		listener: listener,
		handler:  handler,
		close:    cleanup.close,
	}, nil
}

// staticCuratorResolver grants every request one fixed curator. It is only
// safe behind a loopback listener where every caller is the developer.
type staticCuratorResolver struct {
	curator registry.Curator
}

func (r staticCuratorResolver) Curator(*http.Request) (registry.Curator, error) {
	return r.curator, nil
}

type runtimeCleanup struct {
	mu            sync.Mutex
	closeNetwork  func() error
	closeStorage  func() error
	networkClosed bool
	storageClosed bool
}

func (c *runtimeCleanup) close() error {
	c.mu.Lock()
	defer c.mu.Unlock()

	var networkErr error
	if !c.networkClosed {
		networkErr = c.closeNetwork()
		c.networkClosed = networkErr == nil
	}
	var storageErr error
	if !c.storageClosed {
		storageErr = c.closeStorage()
		c.storageClosed = storageErr == nil
	}
	return errors.Join(networkErr, storageErr)
}

func loadOrCreateCSRFKey(stateDir string) (web.CSRFKey, error) {
	path := filepath.Join(stateDir, "csrf.key")
	info, err := os.Lstat(path)
	if err == nil {
		if !info.Mode().IsRegular() {
			return web.CSRFKey{}, fmt.Errorf("load CSRF key %q: path is not a regular file", path)
		}
		if err := os.Chmod(path, 0o600); err != nil {
			return web.CSRFKey{}, fmt.Errorf("set CSRF key permissions: %w", err)
		}
		contents, err := os.ReadFile(path)
		if err != nil {
			return web.CSRFKey{}, fmt.Errorf("read CSRF key: %w", err)
		}
		key, err := web.NewCSRFKey(contents)
		if err != nil {
			return web.CSRFKey{}, fmt.Errorf("parse CSRF key: %w", err)
		}
		return key, nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return web.CSRFKey{}, fmt.Errorf("inspect CSRF key: %w", err)
	}

	contents := make([]byte, 32)
	if _, err := io.ReadFull(rand.Reader, contents); err != nil {
		return web.CSRFKey{}, fmt.Errorf("generate CSRF key: %w", err)
	}
	key, err := web.NewCSRFKey(contents)
	if err != nil {
		return web.CSRFKey{}, fmt.Errorf("construct CSRF key: %w", err)
	}
	temporary, err := os.CreateTemp(stateDir, ".csrf-key-")
	if err != nil {
		return web.CSRFKey{}, fmt.Errorf("create temporary CSRF key: %w", err)
	}
	temporaryPath := temporary.Name()
	removeTemporary := true
	defer func() {
		if removeTemporary {
			_ = os.Remove(temporaryPath)
		}
	}()
	if err := temporary.Chmod(0o600); err != nil {
		_ = temporary.Close()
		return web.CSRFKey{}, fmt.Errorf("set temporary CSRF key permissions: %w", err)
	}
	if _, err := temporary.Write(contents); err != nil {
		_ = temporary.Close()
		return web.CSRFKey{}, fmt.Errorf("write temporary CSRF key: %w", err)
	}
	if err := temporary.Sync(); err != nil {
		_ = temporary.Close()
		return web.CSRFKey{}, fmt.Errorf("sync temporary CSRF key: %w", err)
	}
	if err := temporary.Close(); err != nil {
		return web.CSRFKey{}, fmt.Errorf("close temporary CSRF key: %w", err)
	}
	if err := os.Rename(temporaryPath, path); err != nil {
		return web.CSRFKey{}, fmt.Errorf("install CSRF key: %w", err)
	}
	removeTemporary = false
	if err := syncDirectory(stateDir); err != nil {
		return web.CSRFKey{}, fmt.Errorf("sync CSRF key directory: %w", err)
	}
	return key, nil
}

func syncDirectory(path string) error {
	directory, err := os.Open(path)
	if err != nil {
		return err
	}
	syncErr := directory.Sync()
	closeErr := directory.Close()
	return errors.Join(syncErr, closeErr)
}
