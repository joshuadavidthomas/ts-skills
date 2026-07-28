package daemon

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/joshuadavidthomas/ts-skill-registry/internal/registry"
	"github.com/joshuadavidthomas/ts-skill-registry/internal/safetree"
	"github.com/joshuadavidthomas/ts-skill-registry/internal/storage"
	"github.com/joshuadavidthomas/ts-skill-registry/internal/tailnet"
	"github.com/joshuadavidthomas/ts-skill-registry/internal/web"
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
	return normalizeDevConfig(DevConfig{StateDir: stateDir, Listen: listen})
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

// ConfigFromEnv reads daemon configuration and, when configured, reads the
// first-enrollment auth key once from its file.
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
	if authKeyFile := os.Getenv("TS_SKILLSD_AUTHKEY_FILE"); authKeyFile != "" {
		contents, err := os.ReadFile(authKeyFile)
		if err != nil {
			return Config{}, fmt.Errorf("read TS_SKILLSD_AUTHKEY_FILE %q: %w", authKeyFile, err)
		}
		config.AuthKey = strings.TrimSpace(string(contents))
		if config.AuthKey == "" {
			return Config{}, fmt.Errorf("TS_SKILLSD_AUTHKEY_FILE %q is empty", authKeyFile)
		}
	}
	return normalizeConfig(config)
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
		return Config{}, fmt.Errorf("Tailnet auth key contains a control character")
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

type runtimeFactory func(context.Context, Config) (*runtime, error)

func Run(ctx context.Context, config Config) error {
	return run(ctx, config, buildRuntime)
}

func RunDev(ctx context.Context, config DevConfig) error {
	normalized, err := normalizeDevConfig(config)
	if err != nil {
		return err
	}
	factory := func(ctx context.Context, _ Config) (*runtime, error) {
		return buildDevRuntime(ctx, normalized)
	}
	return run(ctx, Config{StateDir: normalized.StateDir, Hostname: defaultHostname}, factory)
}

func run(ctx context.Context, config Config, factory runtimeFactory) error {
	return runWithHTTPShutdownTimeout(ctx, config, factory, shutdownTimeout)
}

func runWithHTTPShutdownTimeout(ctx context.Context, config Config, factory runtimeFactory, timeout time.Duration) error {
	return runWithHandlerGate(ctx, config, factory, timeout, newHandlerGate(nil))
}

func runWithHandlerGate(ctx context.Context, config Config, factory runtimeFactory, timeout time.Duration, handlers *handlerGate) (err error) {
	if ctx == nil {
		return fmt.Errorf("run daemon: context must be provided")
	}
	config, err = normalizeConfig(config)
	if err != nil {
		return err
	}
	if factory == nil {
		return fmt.Errorf("run daemon: runtime factory must be provided")
	}

	active, err := factory(ctx, config)
	if err != nil {
		return fmt.Errorf("build daemon runtime: %w", err)
	}
	if active == nil || active.listener == nil || active.handler == nil || active.close == nil {
		if active != nil && active.close != nil {
			return errors.Join(fmt.Errorf("build daemon runtime: factory returned an incomplete runtime"), active.close())
		}
		return fmt.Errorf("build daemon runtime: factory returned an incomplete runtime")
	}
	defer func() {
		err = errors.Join(err, active.close())
	}()

	server := newHTTPServer(active.handler, handlers)
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
		if errors.Is(serveErr, http.ErrServerClosed) {
			serveErr = nil
		} else if serveErr != nil {
			serveErr = fmt.Errorf("serve Tailnet HTTP: %w", serveErr)
		}
	case <-ctx.Done():
		handlers.closeAdmission()
		shutdownErr = shutdownHTTP(server, timeout)
		serveErr = <-serveResult
		if errors.Is(serveErr, http.ErrServerClosed) {
			serveErr = nil
		} else if serveErr != nil {
			serveErr = fmt.Errorf("serve Tailnet HTTP during shutdown: %w", serveErr)
		}
	}
	handlers.wait()
	return errors.Join(shutdownErr, serveErr)
}

func newHTTPServer(handler http.Handler, handlers *handlerGate) *http.Server {
	return &http.Server{
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

func buildRuntime(ctx context.Context, config Config) (_ *runtime, err error) {
	config, err = normalizeConfig(config)
	if err != nil {
		return nil, err
	}

	records, catalog, csrfKey, err := openRegistryCore(ctx, config.StateDir)
	if err != nil {
		return nil, err
	}
	closeRecords := true
	defer func() {
		if closeRecords {
			err = errors.Join(err, records.Close())
		}
	}()

	tailConfig := tailnet.ServerConfig{
		Hostname: config.Hostname,
		StateDir: filepath.Join(config.StateDir, "tsnet"),
		AuthKey:  config.AuthKey,
		Verbose:  config.Verbose,
	}
	if config.Tag != "" {
		tailConfig.AdvertiseTags = []string{config.Tag}
	}
	tailServer, err := tailnet.ListenTLS(ctx, tailConfig)
	if err != nil {
		return nil, err
	}
	closeTailnet := true
	defer func() {
		if closeTailnet {
			err = errors.Join(err, tailServer.Close())
		}
	}()

	localClient, err := tailServer.LocalClient()
	if err != nil {
		return nil, err
	}
	actors, err := tailnet.NewActorResolver(localClient)
	if err != nil {
		return nil, err
	}
	handler, err := web.NewHandler(catalog, actors, web.Options{
		StagingParent: filepath.Join(config.StateDir, "tmp"),
		Limits:        safetree.PrototypeLimits(),
		CSRFKey:       csrfKey,
		SecureCookies: true,
	})
	if err != nil {
		return nil, fmt.Errorf("construct registry HTTP handler: %w", err)
	}

	cleanup := &runtimeCleanup{
		closeNetwork: tailServer.Close,
		closeStorage: records.Close,
	}
	closeTailnet = false
	closeRecords = false
	return &runtime{
		listener: tailServer.Listener(),
		handler:  handler,
		close:    cleanup.close,
	}, nil
}

func buildDevRuntime(ctx context.Context, config DevConfig) (_ *runtime, err error) {
	config, err = normalizeDevConfig(config)
	if err != nil {
		return nil, err
	}

	records, catalog, csrfKey, err := openRegistryCore(ctx, config.StateDir)
	if err != nil {
		return nil, err
	}
	closeRecords := true
	defer func() {
		if closeRecords {
			err = errors.Join(err, records.Close())
		}
	}()

	actor, err := registry.NewActor("dev", "dev@localhost")
	if err != nil {
		return nil, fmt.Errorf("construct dev actor: %w", err)
	}
	handler, err := web.NewHandler(catalog, staticActorResolver{identity: web.Identity{Actor: actor, CanCurate: true}}, web.Options{
		StagingParent: filepath.Join(config.StateDir, "tmp"),
		Limits:        safetree.PrototypeLimits(),
		CSRFKey:       csrfKey,
		SecureCookies: false,
	})
	if err != nil {
		return nil, fmt.Errorf("construct registry HTTP handler: %w", err)
	}

	listener, err := net.Listen("tcp", config.Listen)
	if err != nil {
		return nil, fmt.Errorf("listen on loopback address %q: %w", config.Listen, err)
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
	return &runtime{
		listener: listener,
		handler:  handler,
		close:    cleanup.close,
	}, nil
}

// staticActorResolver attributes every request to one fixed actor. It is only
// safe behind a loopback listener where every caller is the developer.
type staticActorResolver struct {
	identity web.Identity
}

func (r staticActorResolver) Identify(*http.Request) (web.Identity, error) {
	return r.identity, nil
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
	defer directory.Close()
	return directory.Sync()
}
