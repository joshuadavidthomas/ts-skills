package server

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"syscall"

	servercatalog "github.com/joshuadavidthomas/ts-skills/internal/server/catalog"
	serverweb "github.com/joshuadavidthomas/ts-skills/internal/server/web"
	"github.com/joshuadavidthomas/ts-skills/internal/tree"
)

type runtime struct {
	listener            net.Listener
	handler             http.Handler
	maxRequestBodyBytes int64
	close               func() error
}

func Run(ctx context.Context, config Config) error {
	if ctx == nil {
		return fmt.Errorf("run server: context must be provided")
	}
	if config == (Config{}) {
		var err error
		config, err = configFromEnv()
		if err != nil {
			return err
		}
	}
	config, err := normalizeConfig(config)
	if err != nil {
		return err
	}
	active, err := buildRuntime(ctx, config)
	if err != nil {
		return fmt.Errorf("build server runtime: %w", err)
	}
	return serve(ctx, active)
}

func RunDev(ctx context.Context, config DevConfig) error {
	if ctx == nil {
		return fmt.Errorf("run server: context must be provided")
	}
	if config.StateDir == "" && config.Listen == "" {
		started := config.Started
		var err error
		config, err = devConfigFromEnv()
		if err != nil {
			return err
		}
		config.Started = started
	}
	config, err := normalizeDevConfig(config)
	if err != nil {
		return err
	}
	active, err := buildDevRuntime(ctx, config)
	if err != nil {
		return fmt.Errorf("build server runtime: %w", err)
	}
	return serve(ctx, active)
}

func openRegistryCore(ctx context.Context, stateDir string) (_ *servercatalog.Catalog, _ csrfKey, err error) {
	catalog, err := servercatalog.Open(ctx, stateDir)
	if err != nil {
		return nil, csrfKey{}, fmt.Errorf("open registry storage: %w", err)
	}
	defer func() {
		if err != nil {
			err = errors.Join(err, catalog.Close())
		}
	}()

	key, err := loadOrCreateCSRFKey(stateDir)
	if err != nil {
		return nil, csrfKey{}, err
	}
	return catalog, key, nil
}

// buildRuntime constructs the production runtime from normalized config.
func buildRuntime(ctx context.Context, config Config) (_ runtime, err error) {
	catalog, csrfKey, err := openRegistryCore(ctx, config.StateDir)
	if err != nil {
		return runtime{}, err
	}
	closeCatalog := true
	defer func() {
		if closeCatalog {
			err = errors.Join(err, catalog.Close())
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
	tailConfig := tailnetConfig{
		Hostname: config.Hostname,
		StateDir: filepath.Join(config.StateDir, "tsnet"),
		AuthKey:  config.AuthKey,
		Logf:     logf,
	}
	if config.Tag != "" {
		tailConfig.AdvertiseTags = []string{config.Tag}
	}
	tailServer, err := listenTLS(ctx, tailConfig)
	if err != nil {
		return runtime{}, err
	}
	closeTailnet := true
	defer func() {
		if closeTailnet {
			err = errors.Join(err, tailServer.close())
		}
	}()

	localClient, err := tailServer.localClient()
	if err != nil {
		return runtime{}, err
	}
	actors := &actorResolver{local: localClient}
	limits := tree.PrototypeLimits()
	maxRequestBodyBytes, err := serverweb.UploadBodyCap(limits)
	if err != nil {
		return runtime{}, fmt.Errorf("derive registry request body cap: %w", err)
	}
	handler, err := newHandler(catalog, actors.curator, handlerOptions{
		StagingParent:       filepath.Join(config.StateDir, "tmp"),
		Limits:              limits,
		MaxRequestBodyBytes: maxRequestBodyBytes,
		CSRFKey:             csrfKey,
		SecureCookies:       true,
		Logger:              logger,
	})
	if err != nil {
		return runtime{}, fmt.Errorf("construct registry HTTP handler: %w", err)
	}

	cleanup := &runtimeCleanup{
		closeNetwork: tailServer.close,
		closeStorage: catalog.Close,
	}
	closeTailnet = false
	closeCatalog = false
	return runtime{
		listener:            tailServer.listenerAddr(),
		handler:             handler,
		maxRequestBodyBytes: maxRequestBodyBytes,
		close:               cleanup.close,
	}, nil
}

// buildDevRuntime constructs the loopback runtime from normalized config.
func buildDevRuntime(ctx context.Context, config DevConfig) (_ runtime, err error) {
	hasState, err := tsnetStateHasNodeKey(filepath.Join(config.StateDir, "tsnet", "tailscaled.state"))
	if err != nil {
		return runtime{}, fmt.Errorf("check dev state directory for enrolled registry: %w", err)
	}
	if hasState {
		return runtime{}, fmt.Errorf("TS_SKILLSD_DEV=1 will not open enrolled registry state directory %q; use a separate TS_SKILLSD_STATE_DIR", config.StateDir)
	}
	catalog, csrfKey, err := openRegistryCore(ctx, config.StateDir)
	if err != nil {
		return runtime{}, err
	}
	closeCatalog := true
	defer func() {
		if closeCatalog {
			err = errors.Join(err, catalog.Close())
		}
	}()

	devActor, err := servercatalog.NewActor("dev", "dev@localhost")
	if err != nil {
		return runtime{}, fmt.Errorf("construct dev curator: %w", err)
	}
	devCurator := servercatalog.Curator{Actor: devActor}
	limits := tree.PrototypeLimits()
	maxRequestBodyBytes, err := serverweb.UploadBodyCap(limits)
	if err != nil {
		return runtime{}, fmt.Errorf("derive registry request body cap: %w", err)
	}
	handler, err := newHandler(catalog, func(*http.Request) (servercatalog.Curator, error) { return devCurator, nil }, handlerOptions{
		StagingParent:       filepath.Join(config.StateDir, "tmp"),
		Limits:              limits,
		MaxRequestBodyBytes: maxRequestBodyBytes,
		CSRFKey:             csrfKey,
		SecureCookies:       false,
		Logger:              slog.Default(),
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
		closeStorage: catalog.Close,
	}
	closeCatalog = false
	return runtime{
		listener:            listener,
		handler:             handler,
		maxRequestBodyBytes: maxRequestBodyBytes,
		close:               cleanup.close,
	}, nil
}

// closeRuntime gives a failed cleanup one immediate retry. runtimeCleanup and
// catalog can then resume after closing resources that already succeeded.
func closeRuntime(active runtime) error {
	first := active.close()
	if first == nil {
		return nil
	}
	return errors.Join(first, active.close())
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

func loadOrCreateCSRFKey(stateDir string) (csrfKey, error) {
	path := filepath.Join(stateDir, "csrf.key")
	info, err := os.Lstat(path)
	if err == nil {
		if !info.Mode().IsRegular() {
			return csrfKey{}, fmt.Errorf("load CSRF key %q: path is not a regular file", path)
		}
		if err := os.Chmod(path, 0o600); err != nil {
			return csrfKey{}, fmt.Errorf("set CSRF key permissions: %w", err)
		}
		contents, err := os.ReadFile(path)
		if err != nil {
			return csrfKey{}, fmt.Errorf("read CSRF key: %w", err)
		}
		key, err := newCSRFKey(contents)
		if err != nil {
			return csrfKey{}, fmt.Errorf("parse CSRF key: %w", err)
		}
		return key, nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return csrfKey{}, fmt.Errorf("inspect CSRF key: %w", err)
	}

	contents := make([]byte, 32)
	if _, err := io.ReadFull(rand.Reader, contents); err != nil {
		return csrfKey{}, fmt.Errorf("generate CSRF key: %w", err)
	}
	key, err := newCSRFKey(contents)
	if err != nil {
		return csrfKey{}, fmt.Errorf("construct CSRF key: %w", err)
	}
	temporary, err := os.CreateTemp(stateDir, ".csrf-key-")
	if err != nil {
		return csrfKey{}, fmt.Errorf("create temporary CSRF key: %w", err)
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
		return csrfKey{}, fmt.Errorf("set temporary CSRF key permissions: %w", err)
	}
	if _, err := temporary.Write(contents); err != nil {
		_ = temporary.Close()
		return csrfKey{}, fmt.Errorf("write temporary CSRF key: %w", err)
	}
	if err := temporary.Sync(); err != nil {
		_ = temporary.Close()
		return csrfKey{}, fmt.Errorf("sync temporary CSRF key: %w", err)
	}
	if err := temporary.Close(); err != nil {
		return csrfKey{}, fmt.Errorf("close temporary CSRF key: %w", err)
	}
	if err := os.Rename(temporaryPath, path); err != nil {
		return csrfKey{}, fmt.Errorf("install CSRF key: %w", err)
	}
	removeTemporary = false
	if err := syncDirectory(stateDir); err != nil {
		return csrfKey{}, fmt.Errorf("sync CSRF key directory: %w", err)
	}
	return key, nil
}
