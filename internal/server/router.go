package server

import (
	"fmt"
	"log/slog"
	"net/http"

	serverapi "github.com/joshuadavidthomas/ts-skills/internal/server/api"
	servercatalog "github.com/joshuadavidthomas/ts-skills/internal/server/catalog"
	serverweb "github.com/joshuadavidthomas/ts-skills/internal/server/web"
	"github.com/joshuadavidthomas/ts-skills/internal/tree"
	"golang.org/x/sync/semaphore"
)

const defaultTreeWorkLimit = 4

type csrfKey = serverweb.CSRFKey

func newCSRFKey(src []byte) (csrfKey, error) {
	return serverweb.NewCSRFKey(src)
}

type handlerOptions struct {
	StagingParent       string
	Limits              tree.Limits
	MaxRequestBodyBytes int64
	MaxTreeWork         int
	CSRFKey             csrfKey
	SecureCookies       bool
	// Logger receives diagnostics for unexpected request failures and
	// post-commit cleanup failures; nil selects slog.Default().
	Logger *slog.Logger
	// treeWork is a package-private test seam.
	treeWork *semaphore.Weighted
}

// newHandler composes the independently routed registry API and browser UI.
// Shared storage and bounded tree work are implementation details of this
// composition point; HTTP representations and middleware remain local to each
// handler.
func newHandler(catalog *servercatalog.Catalog, resolveCurator func(*http.Request) (servercatalog.Curator, error), options handlerOptions) (http.Handler, error) {
	if options.MaxTreeWork < 0 {
		return nil, fmt.Errorf("tree work limit must not be negative")
	}
	if options.MaxTreeWork == 0 {
		options.MaxTreeWork = defaultTreeWorkLimit
	}
	if options.Logger == nil {
		options.Logger = slog.Default()
	}
	treeWork := options.treeWork
	if treeWork == nil {
		treeWork = semaphore.NewWeighted(int64(options.MaxTreeWork))
	}

	api, err := serverapi.New(catalog, serverapi.Options{
		StagingParent: options.StagingParent,
		Limits:        options.Limits,
		Logger:        options.Logger,
		TreeWork:      treeWork,
	})
	if err != nil {
		return nil, err
	}
	web, err := serverweb.New(catalog, resolveCurator, serverweb.Options{
		StagingParent:       options.StagingParent,
		Limits:              options.Limits,
		MaxRequestBodyBytes: options.MaxRequestBodyBytes,
		CSRFKey:             options.CSRFKey,
		SecureCookies:       options.SecureCookies,
		Logger:              options.Logger,
		TreeWork:            treeWork,
	})
	if err != nil {
		return nil, err
	}

	mux := http.NewServeMux()
	mux.Handle("/api/", api)
	mux.Handle("/", web)
	return mux, nil
}
