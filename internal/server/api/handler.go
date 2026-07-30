// Package api serves the private machine-readable registry protocol.
package api

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"net/http"
	"os"
	"time"

	"github.com/joshuadavidthomas/ts-skills/internal/protocol"
	servercatalog "github.com/joshuadavidthomas/ts-skills/internal/server/catalog"
	"github.com/joshuadavidthomas/ts-skills/internal/tree"
	"golang.org/x/sync/semaphore"
)

type Options struct {
	StagingParent string
	Limits        tree.Limits
	Logger        *slog.Logger
	TreeWork      *semaphore.Weighted
}

type apiHandler struct {
	catalog         *servercatalog.Catalog
	options         Options
	maxArchiveBytes int64
	treeWork        *semaphore.Weighted
}

func New(catalog *servercatalog.Catalog, options Options) (http.Handler, error) {
	if catalog == nil {
		return nil, fmt.Errorf("API catalog must be provided")
	}
	if err := tree.ValidateLimits(options.Limits); err != nil {
		return nil, fmt.Errorf("API tree limits: %w", err)
	}
	info, err := os.Stat(options.StagingParent)
	if err != nil {
		return nil, fmt.Errorf("stat API staging parent: %w", err)
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("API staging parent must be a directory")
	}
	if options.Logger == nil {
		options.Logger = slog.Default()
	}
	if options.TreeWork == nil {
		return nil, fmt.Errorf("API tree work limiter must be provided")
	}
	maxArchiveBytes, err := tree.MaxArchiveBytes(options.Limits)
	if err != nil {
		return nil, fmt.Errorf("derive API archive limit: %w", err)
	}

	h := &apiHandler{
		catalog:         catalog,
		options:         options,
		maxArchiveBytes: maxArchiveBytes,
		treeWork:        options.TreeWork,
	}
	return h.routes(), nil
}

func (h *apiHandler) currentPublication(w http.ResponseWriter, r *http.Request) {
	skill, err := protocol.ParseSkill(r.PathValue("namespace"), r.PathValue("name"))
	if err != nil {
		h.writeError(w, protocol.CodeInvalidRequest)
		return
	}
	publication, err := h.catalog.CurrentPublication(r.Context(), skill)
	if err != nil {
		h.writeDomainError(w, r, err)
		return
	}
	if err := protocol.WriteCurrent(w, publication.ID); err != nil {
		h.options.Logger.Warn("write current publication response failed", "error", err)
	}
}

func (h *apiHandler) publicationTree(w http.ResponseWriter, r *http.Request) {
	requestedPublication, err := protocol.ParsePublication(r.PathValue("namespace"), r.PathValue("name"), r.PathValue("digest"))
	if err != nil {
		h.writeError(w, protocol.CodeInvalidRequest)
		return
	}
	publication, err := h.catalog.Publication(r.Context(), requestedPublication)
	if err != nil {
		h.writeDomainError(w, r, err)
		return
	}
	resolvedPublication := publication.ID
	source, err := h.catalog.OpenTree(r.Context(), resolvedPublication.Tree())
	if err != nil {
		h.writeDomainError(w, r, err)
		return
	}
	if !h.admitTreeWork() {
		h.writeUnavailable(w)
		if closeErr := source.Close(); closeErr != nil {
			h.options.Logger.Warn("API publication tree close failed", "error", closeErr)
		}
		return
	}
	defer h.releaseTreeWork()
	archive, err := h.rootlessZIP(r.Context(), source)
	if archive != nil {
		defer func() {
			if err := archive.Close(); err != nil {
				h.options.Logger.Warn("API archive cleanup failed", "error", err)
			}
		}()
	}
	// The archive holds everything the response needs, so the tree closes
	// before any bytes are written and its close failure is still reportable.
	if closeErr := source.Close(); closeErr != nil {
		err = errors.Join(err, closeErr)
	}
	if err != nil {
		h.writeDomainError(w, r, err)
		return
	}
	resolvedSkill := resolvedPublication.Skill()
	w.Header().Set("Content-Type", protocol.ZIPMediaType)
	w.Header().Set("Content-Disposition", `attachment; filename="`+resolvedSkill.Name().String()+`.zip"`)
	w.Header().Set("X-Content-Type-Options", "nosniff")
	protocol.SetPublicationHeaders(w.Header(), resolvedPublication)
	http.ServeContent(w, r, resolvedSkill.Name().String()+".zip", time.Time{}, archive)
}

func (h *apiHandler) rootlessZIP(ctx context.Context, source fs.FS) (*tree.Archive, error) {
	return tree.EncodeArchive(ctx, h.options.StagingParent, source, h.options.Limits, h.maxArchiveBytes)
}

func (h *apiHandler) writeDomainError(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, servercatalog.ErrNotFound):
		h.writeError(w, protocol.CodeNotFound)
	case errors.Is(err, tree.ErrLimitExceeded):
		h.writeError(w, protocol.CodeTooLarge)
	default:
		h.options.Logger.Error("API request failed", "method", r.Method, "path", r.URL.Path, "error", err)
		h.writeError(w, protocol.CodeInternal)
	}
}

func (h *apiHandler) writeError(w http.ResponseWriter, code protocol.Code) {
	message := map[protocol.Code]string{
		protocol.CodeNotFound:       "Skill publication was not found.",
		protocol.CodeInvalidRequest: "Request path is invalid.",
		protocol.CodeTooLarge:       "Skill tree is too large to download.",
		protocol.CodeInternal:       "Registry request could not be completed.",
	}[code]
	if err := protocol.WriteFailure(w, code, message); err != nil {
		h.options.Logger.Warn("write API error response failed", "error", err)
	}
}

func (h *apiHandler) writeUnavailable(w http.ResponseWriter) {
	w.Header().Set("Retry-After", "1")
	if err := protocol.WriteFailure(w, protocol.CodeUnavailable, "Registry tree work is temporarily unavailable."); err != nil {
		h.options.Logger.Warn("write API unavailable response failed", "error", err)
	}
}

func (h *apiHandler) admitTreeWork() bool {
	return h.treeWork.TryAcquire(1)
}

func (h *apiHandler) releaseTreeWork() {
	h.treeWork.Release(1)
}
