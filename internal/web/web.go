package web

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
	"github.com/joshuadavidthomas/ts-skills/internal/protocol"
	"github.com/joshuadavidthomas/ts-skills/internal/registry"
	"github.com/joshuadavidthomas/ts-skills/internal/safetree"
	"github.com/joshuadavidthomas/ts-skills/internal/upload"
)

const maxRequestBytes int64 = 32 << 20

//go:embed templates
var templatesFS embed.FS

//go:embed static
var staticFS embed.FS

type Catalog interface {
	Capture(context.Context, registry.CaptureRequest) (registry.Candidate, error)
	Candidate(context.Context, registry.CandidateID) (registry.Candidate, error)
	OpenCandidateTree(context.Context, registry.CandidateID) (registry.Tree, error)
	Publish(context.Context, registry.CandidateID, registry.Actor, time.Time) (registry.PublishResult, error)
	SetCurrent(context.Context, registry.PublicationID, registry.Actor, time.Time) (registry.CurrentPublication, error)
	ListSkills(context.Context) ([]registry.SkillSummary, error)
	ResolveCurrent(context.Context, registry.SkillID) (registry.Publication, error)
	Publication(context.Context, registry.PublicationID) (registry.Publication, error)
	OpenPublicationTree(context.Context, registry.PublicationID) (registry.Tree, error)
}

type Identity struct {
	Actor     registry.Actor
	CanCurate bool
}

type ActorResolver interface {
	Identify(*http.Request) (Identity, error)
}

type CSRFKey [32]byte

func NewCSRFKey(src []byte) (CSRFKey, error) {
	var key CSRFKey
	if len(src) != len(key) {
		return key, fmt.Errorf("CSRF key must contain exactly %d bytes", len(key))
	}
	copy(key[:], src)
	if key == (CSRFKey{}) {
		return CSRFKey{}, fmt.Errorf("CSRF key must not be all zero")
	}
	return key, nil
}

type Options struct {
	StagingParent string
	Limits        safetree.Limits
	CSRFKey       CSRFKey
	SecureCookies bool
	// Logger receives diagnostics for unexpected request failures and
	// post-commit cleanup failures; nil selects slog.Default().
	Logger *slog.Logger
}

type handler struct {
	catalog Catalog
	actors  ActorResolver
	options Options
	pages   *template.Template
}

func NewHandler(catalog Catalog, actors ActorResolver, options Options) (http.Handler, error) {
	if catalog == nil {
		return nil, fmt.Errorf("web catalog must be provided")
	}
	if actors == nil {
		return nil, fmt.Errorf("actor resolver must be provided")
	}
	if options.CSRFKey == (CSRFKey{}) {
		return nil, fmt.Errorf("CSRF key must be provided")
	}
	if err := safetree.ValidateLimits(options.Limits); err != nil {
		return nil, fmt.Errorf("web upload limits: %w", err)
	}
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
	h := &handler{catalog: catalog, actors: actors, options: options, pages: pages}
	mux := http.NewServeMux()
	mux.Handle("GET /static/", http.StripPrefix("/static/", http.FileServerFS(staticFiles)))
	mux.HandleFunc("GET /", h.catalogPage)
	mux.HandleFunc("GET /skills/{namespace}/{name}", h.skillPage)
	mux.HandleFunc("GET /upload", h.uploadPage)
	mux.HandleFunc("POST /candidates", h.createCandidate)
	mux.HandleFunc("GET /candidates/{candidate}", h.reviewCandidate)
	mux.HandleFunc("POST /candidates/{candidate}/publish", h.publishCandidate)
	mux.HandleFunc("POST /current", h.setCurrent)
	apiPattern := "GET /api/" + protocol.Version + "/skills/{namespace}/{name}"
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
		if r.Method == http.MethodPost && r.URL.Path == "/candidates" {
			r.Body = http.MaxBytesReader(w, r.Body, maxRequestBytes)
		}
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
	Digest      string
	ShortDigest string
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
	summaries, err := h.catalog.ListSkills(r.Context())
	if err != nil {
		h.handleError(w, r, err)
		return
	}
	views := make([]skillView, 0, len(summaries))
	for _, summary := range summaries {
		digest := summary.Current().Tree().String()
		views = append(views, skillView{Skill: summary.Skill().String(), Digest: digest, ShortDigest: shortDigest(digest)})
	}
	h.render(w, http.StatusOK, "catalog", catalogPageData{Skills: views})
}

type skillPageData struct {
	Skill           string
	Digest          string
	PublishedBy     string
	PublishedAt     string
	DownloadPath    string
	FileTree        *fileTreeNode
	SelectedPath    string
	SelectedContent string
	SelectedBinary  bool
}

func (h *handler) skillPage(w http.ResponseWriter, r *http.Request) {
	skill, err := parseAPISkill(r.PathValue("namespace"), r.PathValue("name"))
	if err != nil {
		h.renderError(w, http.StatusNotFound, "Skill was not found", "Return to the catalog and choose another skill.")
		return
	}
	publication, err := h.catalog.ResolveCurrent(r.Context(), skill)
	if err != nil {
		h.handleError(w, r, err)
		return
	}
	tree, err := h.catalog.OpenPublicationTree(r.Context(), publication.ID())
	if err != nil {
		h.handleError(w, r, err)
		return
	}
	defer func() {
		if err := tree.Close(); err != nil {
			h.options.Logger.Warn("web publication tree close failed", "error", err)
		}
	}()
	selected, err := resolveTreeFile(r.Context(), tree, r.URL.Query())
	if errors.Is(err, errTreeFileNotFound) {
		h.renderError(w, http.StatusNotFound, "File was not found", "Choose a file from this skill.")
		return
	}
	if err != nil {
		h.handleError(w, r, err)
		return
	}
	resolved := publication.ID()
	data := skillPageData{
		Skill:       resolved.Skill().String(),
		Digest:      resolved.Tree().String(),
		PublishedBy: publication.PublishedBy().Display(),
		PublishedAt: publication.PublishedAt().Format(time.RFC3339),
		DownloadPath: "/api/" + protocol.Version + "/skills/" + resolved.Skill().Namespace().String() +
			"/" + resolved.Skill().Name().String() + "/publications/" + resolved.Tree().String() + "/tree.zip",
		FileTree: selected.Tree, SelectedPath: selected.Path,
		SelectedContent: selected.Content, SelectedBinary: selected.Binary,
	}
	h.render(w, http.StatusOK, "skill", data)
}

func (h *handler) uploadPage(w http.ResponseWriter, r *http.Request) {
	token := csrf.Token(r)
	w.Header().Set("X-CSRF-Token", token)
	h.render(w, http.StatusOK, "upload", struct{ CSRFToken string }{CSRFToken: token})
}

func (h *handler) createCandidate(w http.ResponseWriter, r *http.Request) {
	identity, err := h.actors.Identify(r)
	if err != nil {
		h.renderError(w, http.StatusUnauthorized, "Identity could not be verified", "Reconnect to the Tailnet and try again.")
		return
	}
	if !identity.CanCurate {
		h.renderError(w, http.StatusForbidden, "You do not have permission to curate skills", "Ask your Tailnet admin to grant tailscale.com/cap/ts-skills with curate:true, then try again.")
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
	namespace, err := registry.ParseNamespace(namespaceText)
	if err != nil {
		h.renderError(w, http.StatusBadRequest, "Namespace is invalid", "Enter a namespace without spaces or path separators.")
		return
	}
	part, nextErr := body.NextPart()
	if nextErr != nil {
		h.handleError(w, r, malformedRequest("a directory manifest must follow namespace", nextErr))
		return
	}
	if part.FormName() != "manifest" {
		h.handleError(w, r, malformedRequest("skill upload must contain a directory manifest", nil))
		return
	}
	submission, err := upload.StageBrowserDirectory(r.Context(), h.options.StagingParent, part, body, h.options.Limits)
	if err != nil {
		h.handleError(w, r, err)
		return
	}
	submissionClosed := false
	defer func() {
		// On these early-return paths the error response is already
		// committed, so a cleanup failure is operator-only diagnostics.
		if !submissionClosed {
			if err := submission.Close(); err != nil {
				h.options.Logger.Warn("web upload cleanup failed", "error", err)
			}
		}
	}()

	source, err := registry.NewUploadSource(submission.Label())
	if err != nil {
		h.renderError(w, http.StatusBadRequest, "Upload label is invalid", "Rename the selected directory and try again.")
		return
	}
	provenance, err := registry.NewProvenance(source, identity.Actor, time.Now().UTC())
	if err != nil {
		h.handleError(w, r, err)
		return
	}
	candidate, err := h.catalog.Capture(r.Context(), registry.CaptureRequest{
		Namespace: namespace, Source: submission.FS(), Root: submission.Root(), Provenance: provenance,
	})
	if err != nil {
		h.handleError(w, r, err)
		return
	}
	if err := submission.Close(); err != nil {
		h.handleError(w, r, err)
		return
	}
	submissionClosed = true
	http.Redirect(w, r, "/candidates/"+candidate.ID().String(), http.StatusSeeOther)
}

type reviewPageData struct {
	CandidateID     string
	Skill           string
	Digest          string
	Source          string
	SubmittedBy     string
	SubmittedAt     string
	FileTree        *fileTreeNode
	SelectedPath    string
	SelectedContent string
	SelectedBinary  bool
	Published       bool
	CSRFField       template.HTML
}

func (h *handler) reviewCandidate(w http.ResponseWriter, r *http.Request) {
	id, err := registry.ParseCandidateID(r.PathValue("candidate"))
	if err != nil {
		h.renderError(w, http.StatusNotFound, "Candidate was not found", "Return to the catalog and choose another candidate.")
		return
	}
	candidate, err := h.catalog.Candidate(r.Context(), id)
	if err != nil {
		h.handleError(w, r, err)
		return
	}
	tree, err := h.catalog.OpenCandidateTree(r.Context(), id)
	if err != nil {
		h.handleError(w, r, err)
		return
	}
	defer func() {
		if err := tree.Close(); err != nil {
			h.options.Logger.Warn("web candidate tree close failed", "error", err)
		}
	}()
	selected, err := resolveTreeFile(r.Context(), tree, r.URL.Query())
	if errors.Is(err, errTreeFileNotFound) {
		h.renderError(w, http.StatusNotFound, "File was not found", "Choose a file from this candidate.")
		return
	}
	if err != nil {
		h.handleError(w, r, err)
		return
	}
	publicationID, err := registry.NewPublicationID(candidate.Skill(), candidate.Tree())
	if err != nil {
		h.handleError(w, r, err)
		return
	}
	_, publicationErr := h.catalog.Publication(r.Context(), publicationID)
	published := publicationErr == nil
	if publicationErr != nil && !errors.Is(publicationErr, registry.ErrNotFound) {
		h.handleError(w, r, publicationErr)
		return
	}
	provenance := candidate.Provenance()
	data := reviewPageData{
		CandidateID: id.String(), Skill: candidate.Skill().String(), Digest: candidate.Tree().String(),
		Source: provenance.Source().Label(), SubmittedBy: provenance.SubmittedBy().Display(),
		SubmittedAt: provenance.SubmittedAt().Format(time.RFC3339), FileTree: selected.Tree,
		SelectedPath: selected.Path, SelectedContent: selected.Content, SelectedBinary: selected.Binary,
		Published: published, CSRFField: csrf.TemplateField(r),
	}
	h.render(w, http.StatusOK, "review", data)
}

func (h *handler) publishCandidate(w http.ResponseWriter, r *http.Request) {
	id, err := registry.ParseCandidateID(r.PathValue("candidate"))
	if err != nil {
		h.renderError(w, http.StatusNotFound, "Candidate was not found", "Return to the catalog and choose another candidate.")
		return
	}
	identity, err := h.actors.Identify(r)
	if err != nil {
		h.renderError(w, http.StatusUnauthorized, "Identity could not be verified", "Reconnect to the Tailnet and try again.")
		return
	}
	if !identity.CanCurate {
		h.renderError(w, http.StatusForbidden, "You do not have permission to curate skills", "Ask your Tailnet admin to grant tailscale.com/cap/ts-skills with curate:true, then try again.")
		return
	}
	if _, err := h.catalog.Publish(r.Context(), id, identity.Actor, time.Now().UTC()); err != nil {
		h.handleError(w, r, err)
		return
	}
	http.Redirect(w, r, "/candidates/"+id.String(), http.StatusSeeOther)
}

func (h *handler) setCurrent(w http.ResponseWriter, r *http.Request) {
	identity, err := h.actors.Identify(r)
	if err != nil {
		h.renderError(w, http.StatusUnauthorized, "Identity could not be verified", "Reconnect to the Tailnet and try again.")
		return
	}
	if !identity.CanCurate {
		h.renderError(w, http.StatusForbidden, "You do not have permission to curate skills", "Ask your Tailnet admin to grant tailscale.com/cap/ts-skills with curate:true, then try again.")
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
	skill, err := registry.ParseSkillID(r.Form.Get("skill"))
	if err != nil {
		h.handleError(w, r, malformedRequest("current skill identity is invalid", err))
		return
	}
	digest, err := agentskill.ParseTreeDigest(r.Form.Get("digest"))
	if err != nil {
		h.handleError(w, r, malformedRequest("current tree digest is invalid", err))
		return
	}
	publication, err := registry.NewPublicationID(skill, digest)
	if err != nil {
		h.handleError(w, r, err)
		return
	}
	if _, err := h.catalog.SetCurrent(r.Context(), publication, identity.Actor, time.Now().UTC()); err != nil {
		h.handleError(w, r, err)
		return
	}
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

func (h *handler) currentPublication(w http.ResponseWriter, r *http.Request) {
	skill, err := parseAPISkill(r.PathValue("namespace"), r.PathValue("name"))
	if err != nil {
		h.writeAPIError(w, protocol.CodeInvalidRequest)
		return
	}
	publication, err := h.catalog.ResolveCurrent(r.Context(), skill)
	if err != nil {
		h.writeAPIDomainError(w, err)
		return
	}
	response := protocol.CurrentResponse{
		Namespace: publication.ID().Skill().Namespace().String(),
		Name:      publication.ID().Skill().Name().String(),
		Digest:    publication.ID().Tree().String(),
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(response)
}

func (h *handler) publicationTree(w http.ResponseWriter, r *http.Request) {
	skill, err := parseAPISkill(r.PathValue("namespace"), r.PathValue("name"))
	if err != nil {
		h.writeAPIError(w, protocol.CodeInvalidRequest)
		return
	}
	digest, err := agentskill.ParseTreeDigest(r.PathValue("digest"))
	if err != nil || digest.String() != r.PathValue("digest") {
		h.writeAPIError(w, protocol.CodeInvalidRequest)
		return
	}
	requestedPublication, err := registry.NewPublicationID(skill, digest)
	if err != nil {
		h.writeAPIError(w, protocol.CodeInvalidRequest)
		return
	}
	publication, err := h.catalog.Publication(r.Context(), requestedPublication)
	if err != nil {
		h.writeAPIDomainError(w, err)
		return
	}
	resolvedPublication := publication.ID()
	tree, err := h.catalog.OpenPublicationTree(r.Context(), resolvedPublication)
	if err != nil {
		h.writeAPIDomainError(w, err)
		return
	}
	archive, err := h.rootlessZIP(r.Context(), tree)
	// The archive holds everything the response needs, so the tree closes
	// before any bytes are written and its close failure is still reportable.
	if closeErr := tree.Close(); closeErr != nil {
		err = errors.Join(err, closeErr)
	}
	if err != nil {
		h.writeAPIDomainError(w, err)
		return
	}
	defer func() {
		// Cleanup runs after the ZIP response is committed, so a failure is
		// operator-only diagnostics.
		name := archive.Name()
		if err := errors.Join(archive.Close(), os.Remove(name)); err != nil {
			h.options.Logger.Warn("web archive cleanup failed", "archive", name, "error", err)
		}
	}()
	resolvedSkill := resolvedPublication.Skill()
	w.Header().Set("Content-Type", "application/zip")
	w.Header().Set("Content-Disposition", `attachment; filename="`+resolvedSkill.Name().String()+`.zip"`)
	w.Header().Set(protocol.HeaderPublicationNamespace, resolvedSkill.Namespace().String())
	w.Header().Set(protocol.HeaderPublicationName, resolvedSkill.Name().String())
	w.Header().Set(protocol.HeaderPublicationDigest, resolvedPublication.Tree().String())
	http.ServeContent(w, r, resolvedSkill.Name().String()+".zip", time.Time{}, archive)
}

func parseAPISkill(namespaceText, nameText string) (registry.SkillID, error) {
	namespace, err := registry.ParseNamespace(namespaceText)
	if err != nil || namespace.String() != namespaceText {
		return registry.SkillID{}, fmt.Errorf("invalid namespace")
	}
	name, err := agentskill.ParseName(nameText)
	if err != nil || name.String() != nameText {
		return registry.SkillID{}, fmt.Errorf("invalid Agent Skill name")
	}
	return registry.NewSkillID(namespace, name)
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
		header := &zip.FileHeader{Name: name, Method: zip.Store}
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
		_, copyErr := io.Copy(output, input)
		closeInputErr := input.Close()
		if err := errors.Join(copyErr, closeInputErr); err != nil {
			return nil, errors.Join(fmt.Errorf("write tree archive entry: %w", err), writer.Close())
		}
	}
	if err := writer.Close(); err != nil {
		return nil, fmt.Errorf("finish tree archive: %w", err)
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

func (h *handler) writeAPIDomainError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, registry.ErrNotFound):
		h.writeAPIError(w, protocol.CodeNotFound)
	case errors.Is(err, safetree.ErrLimitExceeded):
		h.writeAPIError(w, protocol.CodeTooLarge)
	default:
		h.writeAPIError(w, protocol.CodeInternal)
	}
}

func (h *handler) writeAPIError(w http.ResponseWriter, code string) {
	status, known := protocol.StatusForCode(code)
	if !known {
		code = protocol.CodeInternal
		status, _ = protocol.StatusForCode(code)
	}
	message := map[string]string{
		protocol.CodeNotFound:       "Skill publication was not found.",
		protocol.CodeInvalidRequest: "Request path is invalid.",
		protocol.CodeTooLarge:       "Skill tree is too large to download.",
		protocol.CodeInternal:       "Registry request could not be completed.",
	}[code]
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(protocol.ErrorResponse{Code: code, Message: message})
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
	Tree    *fileTreeNode
	Path    string
	Content string
	Binary  bool
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
	var contents bytes.Buffer
	_, copyErr := io.Copy(&contents, &contextReader{ctx: ctx, source: file})
	closeErr := file.Close()
	if err := errors.Join(copyErr, closeErr); err != nil {
		return resolvedTreeFile{}, fmt.Errorf("read tree file %q: %w", selectedPath, err)
	}
	if !utf8.Valid(contents.Bytes()) {
		return resolvedTreeFile{Tree: root, Path: selectedPath, Binary: true}, nil
	}
	return resolvedTreeFile{Tree: root, Path: selectedPath, Content: contents.String()}, nil
}

// contextReader aborts a streaming read as soon as ctx is cancelled.
type contextReader struct {
	ctx    context.Context
	source io.Reader
}

func (r *contextReader) Read(buffer []byte) (int, error) {
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
		return fmt.Errorf("%w: %s", upload.ErrMalformedUpload, problem)
	}
	return fmt.Errorf("%w: %s: %w", upload.ErrMalformedUpload, problem, cause)
}

func (h *handler) handleError(w http.ResponseWriter, r *http.Request, err error) {
	var maxBytes *http.MaxBytesError
	switch {
	case errors.Is(err, safetree.ErrLimitExceeded), errors.As(err, &maxBytes):
		h.renderError(w, http.StatusRequestEntityTooLarge, "Upload is too large", "Choose a smaller upload and try again.")
	case errors.Is(err, upload.ErrMalformedUpload), errors.Is(err, safetree.ErrInvalidPath),
		errors.Is(err, agentskill.ErrInvalidName), errors.Is(err, agentskill.ErrInvalidDocument), errors.Is(err, agentskill.ErrInvalidTree):
		h.renderError(w, http.StatusBadRequest, "Upload is invalid", "Check the skill files and upload them again.")
	case errors.Is(err, registry.ErrNotFound):
		h.renderError(w, http.StatusNotFound, "Registry item was not found", "Return to the catalog and choose another item.")
	case errors.Is(err, registry.ErrConflict):
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
	w.WriteHeader(status)
	if _, err := w.Write(page); err != nil {
		h.options.Logger.Warn("web response write failed", "error", err)
	}
}
