package server

import (
	"archive/zip"
	"bytes"
	"context"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"io"
	"io/fs"
	"log/slog"
	"mime"
	"mime/multipart"
	"net/http"
	"net/url"
	"os"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/gorilla/csrf"
	"github.com/joshuadavidthomas/ts-skills/internal/agentskill"
	"github.com/joshuadavidthomas/ts-skills/internal/safetree"
)

//go:embed templates
var templatesFS embed.FS

//go:embed static
var staticFS embed.FS

const (
	defaultTreeWorkLimit = 4
	maxPreviewBytes      = 256 << 10
)

type csrfKey [32]byte

func newCSRFKey(src []byte) (csrfKey, error) {
	var key csrfKey
	if len(src) != len(key) {
		return key, fmt.Errorf("CSRF key must contain exactly %d bytes", len(key))
	}
	copy(key[:], src)
	if key == (csrfKey{}) {
		return csrfKey{}, fmt.Errorf("CSRF key must not be all zero")
	}
	return key, nil
}

type handlerOptions struct {
	StagingParent       string
	Limits              safetree.Limits
	MaxRequestBodyBytes int64
	MaxTreeWork         int
	CSRFKey             csrfKey
	SecureCookies       bool
	// Logger receives diagnostics for unexpected request failures and
	// post-commit cleanup failures; nil selects slog.Default().
	Logger *slog.Logger
	// treeWork is a package-private test seam. Production allocates the
	// bounded work channel from MaxTreeWork.
	treeWork chan struct{}
}

type handler struct {
	catalog         *catalog
	curator         func(*http.Request) (curator, error)
	options         handlerOptions
	pages           *template.Template
	maxArchiveBytes int64
	treeWork        chan struct{}
}

func newHandler(catalog *catalog, resolveCurator func(*http.Request) (curator, error), options handlerOptions) (http.Handler, error) {
	if catalog == nil {
		return nil, fmt.Errorf("web catalog must be provided")
	}
	if resolveCurator == nil {
		return nil, fmt.Errorf("curator resolver must be provided")
	}
	if options.CSRFKey == (csrfKey{}) {
		return nil, fmt.Errorf("CSRF key must be provided")
	}
	if err := safetree.ValidateLimits(options.Limits); err != nil {
		return nil, fmt.Errorf("web upload limits: %w", err)
	}
	minimumBodyCap, err := uploadBodyCap(options.Limits)
	if err != nil {
		return nil, fmt.Errorf("derive web request body cap: %w", err)
	}
	if options.MaxRequestBodyBytes < minimumBodyCap {
		return nil, fmt.Errorf("web request body cap %d is smaller than upload minimum %d", options.MaxRequestBodyBytes, minimumBodyCap)
	}
	if options.MaxTreeWork < 0 {
		return nil, fmt.Errorf("web tree work limit must not be negative")
	}
	if options.MaxTreeWork == 0 {
		options.MaxTreeWork = defaultTreeWorkLimit
	}
	maxArchiveBytes := agentskill.TreeArchiveMaxBytes
	if options.Logger == nil {
		options.Logger = slog.Default()
	}
	info, err := os.Stat(options.StagingParent)
	if err != nil {
		return nil, fmt.Errorf("stat web staging parent: %w", err)
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("web staging parent must be a directory")
	}
	pages, err := template.ParseFS(templatesFS, "templates/*.html")
	if err != nil {
		return nil, fmt.Errorf("parse web templates: %w", err)
	}
	staticFiles, err := fs.Sub(staticFS, "static")
	if err != nil {
		return nil, fmt.Errorf("open embedded web assets: %w", err)
	}
	treeWork := options.treeWork
	if treeWork == nil {
		treeWork = make(chan struct{}, options.MaxTreeWork)
	}
	h := &handler{catalog: catalog, curator: resolveCurator, options: options, pages: pages, maxArchiveBytes: maxArchiveBytes, treeWork: treeWork}
	mux := http.NewServeMux()
	mux.Handle("GET /static/", http.StripPrefix("/static/", http.FileServerFS(staticFiles)))
	mux.HandleFunc("GET /", h.catalogPage)
	mux.HandleFunc("GET /skills/{namespace}/{name}", h.skillPage)
	mux.HandleFunc("GET /upload", h.uploadPage)
	mux.HandleFunc("POST /candidates", h.createCandidate)
	mux.HandleFunc("GET /candidates/{candidate}", h.reviewCandidate)
	mux.HandleFunc("POST /candidates/{candidate}/publish", h.publishCandidate)
	mux.HandleFunc("POST /current", h.setCurrent)
	apiPattern := "GET /api/" + apiVersion + "/skills/{namespace}/{name}"
	mux.HandleFunc(apiPattern+"/current", h.currentPublication)
	mux.HandleFunc(apiPattern+"/publications/{digest}/tree.zip", h.publicationTree)

	csrfFailure := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h.renderError(w, http.StatusForbidden, "Request could not be verified", "Reload the page and try again.")
	})
	protected := csrf.Protect(
		options.CSRFKey[:],
		csrf.Secure(options.SecureCookies),
		csrf.SameSite(csrf.SameSiteLaxMode),
		csrf.Path("/"),
		csrf.HttpOnly(true),
		csrf.ErrorHandler(csrfFailure),
	)(mux)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !options.SecureCookies {
			r = csrf.PlaintextHTTPRequest(r)
		}
		protected.ServeHTTP(w, r)
	}), nil
}

type catalogPageData struct {
	Skills []skillView
}

type skillView struct {
	Skill       string
	Path        string
	Digest      string
	ShortDigest string
}

func skillPagePath(skill agentskill.SkillID) string {
	return "/skills/" + url.PathEscape(skill.Namespace().String()) + "/" + url.PathEscape(skill.Name().String())
}

func publicationTreePath(publication agentskill.PublicationID) string {
	skill := publication.Skill()
	return "/api/" + apiVersion + "/skills/" + url.PathEscape(skill.Namespace().String()) +
		"/" + url.PathEscape(skill.Name().String()) + "/publications/" + publication.Tree().String() + "/tree.zip"
}

func shortDigest(digest string) string {
	const visible = len("sha256:") + 12
	if len(digest) <= visible {
		return digest
	}
	return digest[:visible] + "…"
}

func (h *handler) catalogPage(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		h.renderError(w, http.StatusNotFound, "Page was not found", "Return to the catalog.")
		return
	}
	summaries, err := h.catalog.listPublishedSkills(r.Context())
	if err != nil {
		h.handleError(w, r, err)
		return
	}
	views := make([]skillView, 0, len(summaries))
	for _, summary := range summaries {
		digest := summary.Current.Tree().String()
		views = append(views, skillView{Skill: summary.Skill.String(), Path: skillPagePath(summary.Skill), Digest: digest, ShortDigest: shortDigest(digest)})
	}
	h.render(w, http.StatusOK, "catalog", catalogPageData{Skills: views})
}

type skillPageData struct {
	Skill             string
	Digest            string
	PublishedBy       string
	PublishedAt       string
	DownloadPath      string
	FileTree          *fileTreeNode
	SelectedPath      string
	SelectedContent   string
	SelectedBinary    bool
	SelectedTruncated bool
}

func (h *handler) skillPage(w http.ResponseWriter, r *http.Request) {
	skill, err := parseAPISkill(r.PathValue("namespace"), r.PathValue("name"))
	if err != nil {
		h.renderError(w, http.StatusNotFound, "Skill was not found", "Return to the catalog and choose another skill.")
		return
	}
	publication, err := h.catalog.currentPublication(r.Context(), skill)
	if err != nil {
		h.handleError(w, r, err)
		return
	}
	tree, err := h.catalog.openTree(r.Context(), publication.ID.Tree())
	if err != nil {
		h.handleError(w, r, err)
		return
	}
	defer func() {
		if err := tree.Close(); err != nil {
			h.options.Logger.Warn("web publication tree close failed", "error", err)
		}
	}()
	if !h.admitTreeWork(w, false) {
		return
	}
	defer h.releaseTreeWork()
	selected, err := resolveTreeFile(r.Context(), tree, r.URL.Query())
	if errors.Is(err, errTreeFileNotFound) {
		h.renderError(w, http.StatusNotFound, "File was not found", "Choose a file from this skill.")
		return
	}
	if err != nil {
		h.handleError(w, r, err)
		return
	}
	resolved := publication.ID
	data := skillPageData{
		Skill:        resolved.Skill().String(),
		Digest:       resolved.Tree().String(),
		PublishedBy:  publication.PublishedBy.Display,
		PublishedAt:  publication.PublishedAt.Format(time.RFC3339),
		DownloadPath: publicationTreePath(resolved),
		FileTree:     selected.Tree, SelectedPath: selected.Path,
		SelectedContent: selected.Content, SelectedBinary: selected.Binary, SelectedTruncated: selected.Truncated,
	}
	h.render(w, http.StatusOK, "skill", data)
}

func (h *handler) uploadPage(w http.ResponseWriter, r *http.Request) {
	token := csrf.Token(r)
	w.Header().Set("X-CSRF-Token", token)
	h.render(w, http.StatusOK, "upload", struct{ CSRFToken string }{CSRFToken: token})
}

func (h *handler) createCandidate(w http.ResponseWriter, r *http.Request) {
	curator, ok := h.resolveCurator(w, r)
	if !ok {
		return
	}
	mediaType, parameters, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || mediaType != "multipart/form-data" || parameters["boundary"] == "" {
		h.renderError(w, http.StatusBadRequest, "Upload format is invalid", "Submit the directory from the upload page.")
		return
	}
	body := multipart.NewReader(r.Body, parameters["boundary"])
	namespaceText, err := nextTextPart(body, "namespace", 1024)
	if err != nil {
		h.handleError(w, r, err)
		return
	}
	namespace, err := agentskill.ParseNamespace(namespaceText)
	if err != nil {
		h.renderError(w, http.StatusBadRequest, "Namespace is invalid", "Enter a namespace without spaces or path separators.")
		return
	}
	submission, err := stageBrowserDirectory(r.Context(), h.options.StagingParent, body, h.options.Limits)
	if err != nil {
		h.handleError(w, r, err)
		return
	}
	submissionClosed := false
	defer func() {
		// A failed close leaves the snapshot open for retry. The candidate or
		// error response is already committed, so cleanup failures are
		// operator-only diagnostics.
		if !submissionClosed {
			if err := submission.Close(); err != nil {
				h.options.Logger.Warn("web upload cleanup failed", "error", err)
			}
		}
	}()

	source := submission.Label()
	if err := validateRecordText("upload source label", source); err != nil {
		h.renderError(w, http.StatusBadRequest, "Upload label is invalid", "Rename the selected directory and try again.")
		return
	}
	candidate, err := h.catalog.capture(r.Context(), curator, captureRequest{
		Namespace: namespace, Staged: submission.Snapshot(), Root: submission.Root(), Source: source, SubmittedAt: time.Now().UTC(),
	})
	if err != nil {
		h.handleError(w, r, err)
		return
	}
	if err := submission.Close(); err != nil {
		h.options.Logger.Warn("web upload cleanup failed", "error", err)
	} else {
		submissionClosed = true
	}
	http.Redirect(w, r, "/candidates/"+candidate.ID.String(), http.StatusSeeOther)
}

type reviewPageData struct {
	CandidateID       string
	Skill             string
	SkillPath         string
	Digest            string
	Source            string
	SubmittedBy       string
	SubmittedAt       string
	FileTree          *fileTreeNode
	SelectedPath      string
	SelectedContent   string
	SelectedBinary    bool
	SelectedTruncated bool
	Published         bool
	CSRFField         template.HTML
}

func (h *handler) reviewCandidate(w http.ResponseWriter, r *http.Request) {
	if _, ok := h.resolveCurator(w, r); !ok {
		return
	}
	id, err := agentskill.ParseCandidateID(r.PathValue("candidate"))
	if err != nil {
		h.renderError(w, http.StatusNotFound, "Candidate was not found", "Return to the catalog and choose another candidate.")
		return
	}
	candidate, err := h.catalog.candidate(r.Context(), id)
	if err != nil {
		h.handleError(w, r, err)
		return
	}
	tree, err := h.catalog.openTree(r.Context(), candidate.Tree)
	if err != nil {
		h.handleError(w, r, err)
		return
	}
	defer func() {
		if err := tree.Close(); err != nil {
			h.options.Logger.Warn("web candidate tree close failed", "error", err)
		}
	}()
	if !h.admitTreeWork(w, false) {
		return
	}
	defer h.releaseTreeWork()
	selected, err := resolveTreeFile(r.Context(), tree, r.URL.Query())
	if errors.Is(err, errTreeFileNotFound) {
		h.renderError(w, http.StatusNotFound, "File was not found", "Choose a file from this candidate.")
		return
	}
	if err != nil {
		h.handleError(w, r, err)
		return
	}
	publicationID, err := agentskill.NewPublicationID(candidate.Skill, candidate.Tree)
	if err != nil {
		h.handleError(w, r, err)
		return
	}
	_, publicationErr := h.catalog.publication(r.Context(), publicationID)
	published := publicationErr == nil
	if publicationErr != nil && !errors.Is(publicationErr, errNotFound) {
		h.handleError(w, r, publicationErr)
		return
	}
	provenance := candidate.Provenance
	data := reviewPageData{
		CandidateID: id.String(), Skill: candidate.Skill.String(), SkillPath: skillPagePath(candidate.Skill), Digest: candidate.Tree.String(),
		Source: provenance.Source, SubmittedBy: provenance.SubmittedBy.Display,
		SubmittedAt: provenance.SubmittedAt.Format(time.RFC3339), FileTree: selected.Tree,
		SelectedPath: selected.Path, SelectedContent: selected.Content, SelectedBinary: selected.Binary, SelectedTruncated: selected.Truncated,
		Published: published, CSRFField: csrf.TemplateField(r),
	}
	h.render(w, http.StatusOK, "review", data)
}

func (h *handler) publishCandidate(w http.ResponseWriter, r *http.Request) {
	id, err := agentskill.ParseCandidateID(r.PathValue("candidate"))
	if err != nil {
		h.renderError(w, http.StatusNotFound, "Candidate was not found", "Return to the catalog and choose another candidate.")
		return
	}
	curator, ok := h.resolveCurator(w, r)
	if !ok {
		return
	}
	if _, err := h.catalog.publish(r.Context(), id, curator, time.Now().UTC()); err != nil {
		h.handleError(w, r, err)
		return
	}
	http.Redirect(w, r, "/candidates/"+id.String(), http.StatusSeeOther)
}

func (h *handler) setCurrent(w http.ResponseWriter, r *http.Request) {
	curator, ok := h.resolveCurator(w, r)
	if !ok {
		return
	}
	if err := r.ParseForm(); err != nil {
		h.handleError(w, r, malformedRequest("current publication form is invalid", err))
		return
	}
	if len(r.Form["skill"]) != 1 || len(r.Form["digest"]) != 1 {
		h.handleError(w, r, malformedRequest("current publication form must contain one skill and digest", nil))
		return
	}
	skill, err := agentskill.ParseSkillID(r.Form.Get("skill"))
	if err != nil {
		h.handleError(w, r, malformedRequest("current skill identity is invalid", err))
		return
	}
	digest, err := agentskill.ParseTreeDigest(r.Form.Get("digest"))
	if err != nil {
		h.handleError(w, r, malformedRequest("current tree digest is invalid", err))
		return
	}
	publication, err := agentskill.NewPublicationID(skill, digest)
	if err != nil {
		h.handleError(w, r, err)
		return
	}
	if err := h.catalog.setCurrent(r.Context(), publication, curator, time.Now().UTC()); err != nil {
		h.handleError(w, r, err)
		return
	}
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

func (h *handler) resolveCurator(w http.ResponseWriter, r *http.Request) (curator, bool) {
	resolved, err := h.curator(r)
	switch {
	case errors.Is(err, errCurationDenied):
		h.renderError(w, http.StatusForbidden, "You do not have permission to curate skills", "Ask your Tailnet admin to grant joshuadavidthomas.com/cap/ts-skills with curate:true, then try again.")
	case err != nil:
		h.renderError(w, http.StatusUnauthorized, "Identity could not be verified", "Reconnect to the Tailnet and try again.")
	default:
		return resolved, true
	}
	return curator{}, false
}

func (h *handler) currentPublication(w http.ResponseWriter, r *http.Request) {
	skill, err := parseAPISkill(r.PathValue("namespace"), r.PathValue("name"))
	if err != nil {
		h.writeAPIError(w, codeInvalidRequest)
		return
	}
	publication, err := h.catalog.currentPublication(r.Context(), skill)
	if err != nil {
		h.writeAPIDomainError(w, r, err)
		return
	}
	response := currentResponse{
		Namespace: publication.ID.Skill().Namespace().String(),
		Name:      publication.ID.Skill().Name().String(),
		Digest:    publication.ID.Tree().String(),
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(response)
}

func (h *handler) publicationTree(w http.ResponseWriter, r *http.Request) {
	skill, err := parseAPISkill(r.PathValue("namespace"), r.PathValue("name"))
	if err != nil {
		h.writeAPIError(w, codeInvalidRequest)
		return
	}
	digest, err := agentskill.ParseTreeDigest(r.PathValue("digest"))
	if err != nil || digest.String() != r.PathValue("digest") {
		h.writeAPIError(w, codeInvalidRequest)
		return
	}
	requestedPublication, err := agentskill.NewPublicationID(skill, digest)
	if err != nil {
		h.writeAPIError(w, codeInvalidRequest)
		return
	}
	publication, err := h.catalog.publication(r.Context(), requestedPublication)
	if err != nil {
		h.writeAPIDomainError(w, r, err)
		return
	}
	resolvedPublication := publication.ID
	tree, err := h.catalog.openTree(r.Context(), resolvedPublication.Tree())
	if err != nil {
		h.writeAPIDomainError(w, r, err)
		return
	}
	if !h.admitTreeWork(w, true) {
		if closeErr := tree.Close(); closeErr != nil {
			h.options.Logger.Warn("web publication tree close failed", "error", closeErr)
		}
		return
	}
	defer h.releaseTreeWork()
	archive, err := h.rootlessZIP(r.Context(), tree)
	if archive != nil {
		defer func() {
			// Cleanup runs after the ZIP response is committed, so a failure is
			// operator-only diagnostics.
			name := archive.Name()
			if err := errors.Join(archive.Close(), os.Remove(name)); err != nil {
				h.options.Logger.Warn("web archive cleanup failed", "archive", name, "error", err)
			}
		}()
	}
	// The archive holds everything the response needs, so the tree closes
	// before any bytes are written and its close failure is still reportable.
	if closeErr := tree.Close(); closeErr != nil {
		err = errors.Join(err, closeErr)
	}
	if err != nil {
		h.writeAPIDomainError(w, r, err)
		return
	}
	resolvedSkill := resolvedPublication.Skill()
	w.Header().Set("Content-Type", "application/zip")
	w.Header().Set("Content-Disposition", `attachment; filename="`+resolvedSkill.Name().String()+`.zip"`)
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set(headerPublicationNamespace, resolvedSkill.Namespace().String())
	w.Header().Set(headerPublicationName, resolvedSkill.Name().String())
	w.Header().Set(headerPublicationDigest, resolvedPublication.Tree().String())
	http.ServeContent(w, r, resolvedSkill.Name().String()+".zip", time.Time{}, archive)
}

func parseAPISkill(namespaceText, nameText string) (agentskill.SkillID, error) {
	namespace, err := agentskill.ParseNamespace(namespaceText)
	if err != nil || namespace.String() != namespaceText {
		return agentskill.SkillID{}, fmt.Errorf("invalid namespace")
	}
	name, err := agentskill.ParseName(nameText)
	if err != nil || name.String() != nameText {
		return agentskill.SkillID{}, fmt.Errorf("invalid Agent Skill name")
	}
	return agentskill.NewSkillID(namespace, name)
}

func (h *handler) rootlessZIP(ctx context.Context, tree fs.FS) (_ *os.File, err error) {
	files := make([]string, 0)
	err = fs.WalkDir(tree, ".", func(name string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if name == "." || entry.IsDir() {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("published tree contains unsupported path")
		}
		files = append(files, name)
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("list published tree: %w", err)
	}
	sort.Strings(files)

	archive, err := os.CreateTemp(h.options.StagingParent, ".ts-skills-download-*.zip")
	if err != nil {
		return nil, fmt.Errorf("create tree archive: %w", err)
	}
	owned := true
	defer func() {
		if owned {
			name := archive.Name()
			err = errors.Join(err, archive.Close(), os.Remove(name))
		}
	}()
	writer := zip.NewWriter(archive)
	for _, name := range files {
		if err := ctx.Err(); err != nil {
			return nil, errors.Join(err, writer.Close())
		}
		// V1 tree archives use agentskill.TreeArchiveZIPMethod for every entry.
		header := &zip.FileHeader{Name: name, Method: agentskill.TreeArchiveZIPMethod}
		header.Modified = time.Date(1980, time.January, 1, 0, 0, 0, 0, time.UTC)
		header.SetMode(0o644)
		output, err := writer.CreateHeader(header)
		if err != nil {
			return nil, errors.Join(fmt.Errorf("create tree archive entry: %w", err), writer.Close())
		}
		input, err := tree.Open(name)
		if err != nil {
			return nil, errors.Join(fmt.Errorf("open published tree file: %w", err), writer.Close())
		}
		_, copyErr := io.Copy(output, &requestContextReader{ctx: ctx, source: input})
		closeInputErr := input.Close()
		if err := errors.Join(copyErr, closeInputErr); err != nil {
			return nil, errors.Join(fmt.Errorf("write tree archive entry: %w", err), writer.Close())
		}
	}
	if err := writer.Close(); err != nil {
		return nil, fmt.Errorf("finish tree archive: %w", err)
	}
	info, err := archive.Stat()
	if err != nil {
		return nil, fmt.Errorf("stat tree archive: %w", err)
	}
	if info.Size() > h.maxArchiveBytes {
		return nil, &safetree.LimitError{Limit: "archive bytes", Max: h.maxArchiveBytes, Actual: info.Size()}
	}
	if err := archive.Sync(); err != nil {
		return nil, fmt.Errorf("sync tree archive: %w", err)
	}
	if _, err := archive.Seek(0, io.SeekStart); err != nil {
		return nil, fmt.Errorf("rewind tree archive: %w", err)
	}
	owned = false
	return archive, nil
}

func (h *handler) writeAPIDomainError(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, errNotFound):
		h.writeAPIError(w, codeNotFound)
	case errors.Is(err, safetree.ErrLimitExceeded):
		h.writeAPIError(w, codeTooLarge)
	default:
		h.options.Logger.Error("API request failed", "method", r.Method, "path", r.URL.Path, "error", err)
		h.writeAPIError(w, codeInternal)
	}
}

func (h *handler) writeAPIError(w http.ResponseWriter, code string) {
	status, known := statusForCode(code)
	if !known {
		code = codeInternal
		status, _ = statusForCode(code)
	}
	message := map[string]string{
		codeNotFound:       "Skill publication was not found.",
		codeInvalidRequest: "Request path is invalid.",
		codeTooLarge:       "Skill tree is too large to download.",
		codeInternal:       "Registry request could not be completed.",
	}[code]
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(errorResponse{Code: code, Message: message})
}

func nextTextPart(body *multipart.Reader, expected string, maximum int64) (string, error) {
	part, err := body.NextPart()
	if err != nil {
		return "", malformedRequest(expected+" must be the next multipart part", err)
	}
	if part.FormName() != expected || part.FileName() != "" {
		return "", malformedRequest(expected+" must be the next multipart text part", nil)
	}
	contents, err := io.ReadAll(io.LimitReader(part, maximum+1))
	if err != nil {
		return "", malformedRequest("cannot read "+expected, err)
	}
	if int64(len(contents)) > maximum {
		return "", &safetree.LimitError{Limit: expected + " bytes", Max: maximum, Actual: int64(len(contents))}
	}
	if !utf8.Valid(contents) {
		return "", malformedRequest(expected+" must be valid UTF-8", nil)
	}
	return string(contents), nil
}

var errTreeFileNotFound = errors.New("tree file not found")

type fileTreeNode struct {
	Name     string
	Path     string
	IsDir    bool
	Selected bool
	Children []*fileTreeNode
}

// QueryEscape percent-encodes slashes; restoring them keeps hrefs readable
// while every other byte stays escaped, so the result is URL-context safe.
func (n *fileTreeNode) Href() template.URL {
	return template.URL("?file=" + strings.ReplaceAll(url.QueryEscape(n.Path), "%2F", "/"))
}

type resolvedTreeFile struct {
	Tree      *fileTreeNode
	Path      string
	Content   string
	Binary    bool
	Truncated bool
}

func resolveTreeFile(ctx context.Context, tree fs.FS, query map[string][]string) (resolvedTreeFile, error) {
	selectedPath := agentskill.Filename
	if requested, ok := query["file"]; ok {
		if len(requested) != 1 {
			return resolvedTreeFile{}, errTreeFileNotFound
		}
		selectedPath = requested[0]
	}

	root := &fileTreeNode{IsDir: true}
	directories := map[string]*fileTreeNode{".": root}
	selectedFound := false
	err := fs.WalkDir(tree, ".", func(name string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if name == "." {
			return nil
		}

		parentPath := "."
		baseName := name
		if separator := strings.LastIndexByte(name, '/'); separator >= 0 {
			parentPath = name[:separator]
			baseName = name[separator+1:]
		}
		parent, ok := directories[parentPath]
		if !ok {
			return fmt.Errorf("tree path %q has no parent directory", name)
		}
		node := &fileTreeNode{Name: baseName, Path: name, IsDir: entry.IsDir()}
		parent.Children = append(parent.Children, node)
		if node.IsDir {
			directories[name] = node
			return nil
		}

		info, err := entry.Info()
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("tree contains non-regular file %q", name)
		}
		if name == selectedPath {
			node.Selected = true
			selectedFound = true
		}
		return nil
	})
	if err != nil {
		return resolvedTreeFile{}, fmt.Errorf("list tree: %w", err)
	}
	if !selectedFound {
		return resolvedTreeFile{}, errTreeFileNotFound
	}

	sortFileTree(root)
	file, err := tree.Open(selectedPath)
	if err != nil {
		return resolvedTreeFile{}, fmt.Errorf("read tree file %q: %w", selectedPath, err)
	}
	contents, copyErr := io.ReadAll(io.LimitReader(&requestContextReader{ctx: ctx, source: file}, maxPreviewBytes+1))
	closeErr := file.Close()
	if err := errors.Join(copyErr, closeErr); err != nil {
		return resolvedTreeFile{}, fmt.Errorf("read tree file %q: %w", selectedPath, err)
	}
	truncated := int64(len(contents)) > maxPreviewBytes
	if truncated {
		contents = contents[:maxPreviewBytes]
	}
	if !utf8.Valid(contents) {
		return resolvedTreeFile{Tree: root, Path: selectedPath, Binary: true, Truncated: truncated}, nil
	}
	return resolvedTreeFile{Tree: root, Path: selectedPath, Content: string(contents), Truncated: truncated}, nil
}

func (h *handler) admitTreeWork(w http.ResponseWriter, api bool) bool {
	select {
	case h.treeWork <- struct{}{}:
		return true
	default:
		w.Header().Set("Retry-After", "1")
		if api {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusServiceUnavailable)
			_ = json.NewEncoder(w).Encode(errorResponse{Code: "unavailable", Message: "Registry tree work is temporarily unavailable."})
		} else {
			h.renderError(w, http.StatusServiceUnavailable, "Registry is busy", "Try again in a moment.")
		}
		return false
	}
}

func (h *handler) releaseTreeWork() {
	<-h.treeWork
}

// requestContextReader aborts a streaming read as soon as ctx is cancelled.
type requestContextReader struct {
	ctx    context.Context
	source io.Reader
}

func (r *requestContextReader) Read(buffer []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.source.Read(buffer)
}

func sortFileTree(node *fileTreeNode) {
	sort.Slice(node.Children, func(i, j int) bool {
		left, right := node.Children[i], node.Children[j]
		if left.IsDir != right.IsDir {
			return left.IsDir
		}
		return left.Name < right.Name
	})
	for _, child := range node.Children {
		if child.IsDir {
			sortFileTree(child)
		}
	}
}

func malformedRequest(problem string, cause error) error {
	if cause == nil {
		return fmt.Errorf("%w: %s", errMalformedUpload, problem)
	}
	return fmt.Errorf("%w: %s: %w", errMalformedUpload, problem, cause)
}

func (h *handler) handleError(w http.ResponseWriter, r *http.Request, err error) {
	var maxBytes *http.MaxBytesError
	switch {
	case errors.Is(err, safetree.ErrLimitExceeded), errors.As(err, &maxBytes):
		h.renderError(w, http.StatusRequestEntityTooLarge, "Upload is too large", "Choose a smaller upload and try again.")
	case errors.Is(err, errMalformedUpload), errors.Is(err, safetree.ErrInvalidPath),
		errors.Is(err, agentskill.ErrInvalidName), errors.Is(err, agentskill.ErrInvalidDocument), errors.Is(err, agentskill.ErrInvalidTree):
		h.renderError(w, http.StatusBadRequest, "Upload is invalid", "Check the skill files and upload them again.")
	case errors.Is(err, errNotFound):
		h.renderError(w, http.StatusNotFound, "Registry item was not found", "Return to the catalog and choose another item.")
	case errors.Is(err, errConflict):
		h.renderError(w, http.StatusConflict, "Registry item conflicts with existing data", "Reload the page before trying again.")
	default:
		h.options.Logger.Error("web request failed", "method", r.Method, "path", r.URL.Path, "error", err)
		h.renderError(w, http.StatusInternalServerError, "Request could not be completed", "Try again. If this keeps happening, contact the registry operator.")
	}
}

// render executes the page template into a buffer before committing any
// response bytes: only a fully rendered page earns a status, and a template
// failure falls back to the generic 500 page instead of leaving a half-written
// body under a 2xx header.
func (h *handler) render(w http.ResponseWriter, status int, name string, data any) {
	var page bytes.Buffer
	if err := h.pages.ExecuteTemplate(&page, name, data); err != nil {
		h.options.Logger.Error("web page template failed", "template", name, "error", err)
		h.renderError(w, http.StatusInternalServerError, "Page could not be rendered", "Try again. If this keeps happening, contact the registry operator.")
		return
	}
	h.writePage(w, status, page.Bytes())
}

func (h *handler) renderError(w http.ResponseWriter, status int, title, action string) {
	var page bytes.Buffer
	if err := h.pages.ExecuteTemplate(&page, "error", struct{ Title, Action string }{title, action}); err != nil {
		// Nothing is committed yet, so plain text can still take over the
		// response; a second WriteHeader would be a bug.
		h.options.Logger.Error("web error template failed", "error", err)
		http.Error(w, http.StatusText(status), status)
		return
	}
	h.writePage(w, status, page.Bytes())
}

// writePage commits a fully rendered page. A body write failure arrives
// after the status is committed, so it is log-only.
func (h *handler) writePage(w http.ResponseWriter, status int, page []byte) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Content-Security-Policy", "default-src 'self'")
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(status)
	if _, err := w.Write(page); err != nil {
		h.options.Logger.Warn("web response write failed", "error", err)
	}
}
