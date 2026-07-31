// Package web serves the browser catalog and curation workflow.
package web

import (
	"bytes"
	"context"
	"embed"
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

	"github.com/joshuadavidthomas/ts-skills/internal/agentskill"
	"github.com/joshuadavidthomas/ts-skills/internal/protocol"
	"github.com/joshuadavidthomas/ts-skills/internal/registry"
	servercatalog "github.com/joshuadavidthomas/ts-skills/internal/server/catalog"
	"github.com/joshuadavidthomas/ts-skills/internal/tree"
	"golang.org/x/sync/semaphore"
)

//go:embed templates
var templatesFS embed.FS

//go:embed static
var staticFS embed.FS

const maxPreviewBytes = 256 << 10

type Options struct {
	StagingParent string
	Limits        tree.Limits
	Logger        *slog.Logger
	TreeWork      *semaphore.Weighted
}

type webHandler struct {
	catalog            *servercatalog.Catalog
	curator            func(*http.Request) (servercatalog.Curator, error)
	options            Options
	pages              map[string]*template.Template
	treeWork           *semaphore.Weighted
	maxUploadBodyBytes int64
}

type pageView struct {
	Title   string
	Content any
}

var pageNames = [...]string{"catalog", "error", "review", "skill", "upload"}

func parsePages() (map[string]*template.Template, error) {
	pages := make(map[string]*template.Template, len(pageNames))
	for _, name := range pageNames {
		page, err := template.ParseFS(
			templatesFS,
			"templates/base.html",
			"templates/filetree.html",
			"templates/"+name+".html",
		)
		if err != nil {
			return nil, fmt.Errorf("parse web %s template: %w", name, err)
		}
		pages[name] = page
	}
	return pages, nil
}

func New(catalog *servercatalog.Catalog, resolveCurator func(*http.Request) (servercatalog.Curator, error), options Options) (http.Handler, error) {
	if catalog == nil {
		return nil, fmt.Errorf("web catalog must be provided")
	}
	if resolveCurator == nil {
		return nil, fmt.Errorf("web curator resolver must be provided")
	}
	if err := tree.ValidateLimits(options.Limits); err != nil {
		return nil, fmt.Errorf("web upload limits: %w", err)
	}
	maxUploadBodyBytes, err := uploadBodyCap(options.Limits)
	if err != nil {
		return nil, fmt.Errorf("derive web request body cap: %w", err)
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
	pages, err := parsePages()
	if err != nil {
		return nil, err
	}
	staticFiles, err := fs.Sub(staticFS, "static")
	if err != nil {
		return nil, fmt.Errorf("open embedded web assets: %w", err)
	}
	if options.TreeWork == nil {
		return nil, fmt.Errorf("web tree work limiter must be provided")
	}
	h := &webHandler{
		catalog: catalog, curator: resolveCurator, options: options, pages: pages,
		treeWork: options.TreeWork, maxUploadBodyBytes: maxUploadBodyBytes,
	}
	routes := h.routes(http.StripPrefix("/static/", http.FileServerFS(staticFiles)))

	csrfFailure := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h.renderError(w, http.StatusForbidden, "Request could not be verified", "Reload the page and try again.")
	})
	protection := http.NewCrossOriginProtection()
	protection.SetDenyHandler(csrfFailure)
	return protection.Handler(routes), nil
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

func skillPagePath(skill registry.SkillID) string {
	return "/skills/" + url.PathEscape(skill.Namespace().String()) + "/" + url.PathEscape(skill.Name().String())
}

func publicationTreePath(publication registry.PublicationID) string {
	return protocol.TreePath(publication)
}

func shortDigest(digest string) string {
	const visible = len("sha256:") + 12
	if len(digest) <= visible {
		return digest
	}
	return digest[:visible] + "…"
}

func (h *webHandler) catalogPage(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		h.renderError(w, http.StatusNotFound, "Page was not found", "Return to the catalog.")
		return
	}
	summaries, err := h.catalog.ListPublishedSkills(r.Context())
	if err != nil {
		h.handleError(w, r, err)
		return
	}
	views := make([]skillView, 0, len(summaries))
	for _, summary := range summaries {
		digest := summary.Current.Tree().String()
		views = append(views, skillView{Skill: summary.Skill.String(), Path: skillPagePath(summary.Skill), Digest: digest, ShortDigest: shortDigest(digest)})
	}
	h.render(w, http.StatusOK, "catalog", pageView{Content: catalogPageData{Skills: views}})
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

func (h *webHandler) skillPage(w http.ResponseWriter, r *http.Request) {
	skill, err := protocol.ParseSkill(r.PathValue("namespace"), r.PathValue("name"))
	if err != nil {
		h.renderError(w, http.StatusNotFound, "Skill was not found", "Return to the catalog and choose another skill.")
		return
	}
	publication, err := h.catalog.CurrentPublication(r.Context(), skill)
	if err != nil {
		h.handleError(w, r, err)
		return
	}
	tree, err := h.catalog.OpenTree(r.Context(), publication.ID.Tree())
	if err != nil {
		h.handleError(w, r, err)
		return
	}
	defer func() {
		if err := tree.Close(); err != nil {
			h.options.Logger.Warn("web publication tree close failed", "error", err)
		}
	}()
	if !h.admitTreeWork() {
		h.renderBusy(w)
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
	h.render(w, http.StatusOK, "skill", pageView{Title: data.Skill, Content: data})
}

func (h *webHandler) uploadPage(w http.ResponseWriter, r *http.Request) {
	h.render(w, http.StatusOK, "upload", pageView{
		Title: "Upload a skill",
	})
}

func (h *webHandler) createCandidate(w http.ResponseWriter, r *http.Request) {
	curator, ok := h.resolveCurator(w, r)
	if !ok {
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, h.maxUploadBodyBytes)
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
		h.renderError(w, http.StatusBadRequest, "Namespace is invalid", "Use 1–64 lowercase letters, digits, and internal hyphens.")
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
			h.closeUpload(submission)
		}
	}()

	source := submission.Label()
	candidate, err := h.catalog.Capture(r.Context(), curator, servercatalog.CaptureRequest{
		Namespace: namespace, Staged: submission.Snapshot(), Root: submission.Root(), Source: source, SubmittedAt: time.Now().UTC(),
	})
	if err != nil {
		h.handleError(w, r, err)
		return
	}
	if h.closeUpload(submission) {
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
}

func (h *webHandler) reviewCandidate(w http.ResponseWriter, r *http.Request) {
	if _, ok := h.resolveCurator(w, r); !ok {
		return
	}
	id, err := servercatalog.ParseCandidateID(r.PathValue("candidate"))
	if err != nil {
		h.renderError(w, http.StatusNotFound, "Candidate was not found", "Return to the catalog and choose another candidate.")
		return
	}
	candidate, err := h.catalog.Candidate(r.Context(), id)
	if err != nil {
		h.handleError(w, r, err)
		return
	}
	tree, err := h.catalog.OpenTree(r.Context(), candidate.Tree)
	if err != nil {
		h.handleError(w, r, err)
		return
	}
	defer func() {
		if err := tree.Close(); err != nil {
			h.options.Logger.Warn("web candidate tree close failed", "error", err)
		}
	}()
	if !h.admitTreeWork() {
		h.renderBusy(w)
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
	publicationID, err := registry.NewPublicationID(candidate.Skill, candidate.Tree)
	if err != nil {
		h.handleError(w, r, err)
		return
	}
	_, publicationErr := h.catalog.Publication(r.Context(), publicationID)
	published := publicationErr == nil
	if publicationErr != nil && !errors.Is(publicationErr, servercatalog.ErrNotFound) {
		h.handleError(w, r, publicationErr)
		return
	}
	provenance := candidate.Provenance
	data := reviewPageData{
		CandidateID: id.String(), Skill: candidate.Skill.String(), SkillPath: skillPagePath(candidate.Skill), Digest: candidate.Tree.String(),
		Source: provenance.Source, SubmittedBy: provenance.SubmittedBy.Display,
		SubmittedAt: provenance.SubmittedAt.Format(time.RFC3339), FileTree: selected.Tree,
		SelectedPath: selected.Path, SelectedContent: selected.Content, SelectedBinary: selected.Binary, SelectedTruncated: selected.Truncated,
		Published: published,
	}
	h.render(w, http.StatusOK, "review", pageView{
		Title: "Review " + data.Skill, Content: data,
	})
}

func (h *webHandler) publishCandidate(w http.ResponseWriter, r *http.Request) {
	id, err := servercatalog.ParseCandidateID(r.PathValue("candidate"))
	if err != nil {
		h.renderError(w, http.StatusNotFound, "Candidate was not found", "Return to the catalog and choose another candidate.")
		return
	}
	curator, ok := h.resolveCurator(w, r)
	if !ok {
		return
	}
	if _, err := h.catalog.Publish(r.Context(), id, curator, time.Now().UTC()); err != nil {
		h.handleError(w, r, err)
		return
	}
	http.Redirect(w, r, "/candidates/"+id.String(), http.StatusSeeOther)
}

func (h *webHandler) setCurrent(w http.ResponseWriter, r *http.Request) {
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
	skill, err := registry.ParseSkillID(r.Form.Get("skill"))
	if err != nil {
		h.handleError(w, r, malformedRequest("current skill identity is invalid", err))
		return
	}
	digest, err := registry.ParseTreeDigest(r.Form.Get("digest"))
	if err != nil {
		h.handleError(w, r, malformedRequest("current tree digest is invalid", err))
		return
	}
	publication, err := registry.NewPublicationID(skill, digest)
	if err != nil {
		h.handleError(w, r, err)
		return
	}
	if err := h.catalog.SetCurrent(r.Context(), publication, curator, time.Now().UTC()); err != nil {
		h.handleError(w, r, err)
		return
	}
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

func (h *webHandler) resolveCurator(w http.ResponseWriter, r *http.Request) (servercatalog.Curator, bool) {
	resolved, err := h.curator(r)
	switch {
	case errors.Is(err, servercatalog.ErrCurationDenied):
		h.renderError(w, http.StatusForbidden, "You do not have permission to curate skills", "Ask your Tailnet admin to grant joshuadavidthomas.com/cap/ts-skills with curate:true, then try again.")
	case err != nil:
		h.renderError(w, http.StatusUnauthorized, "Identity could not be verified", "Reconnect to the Tailnet and try again.")
	default:
		return resolved, true
	}
	return servercatalog.Curator{}, false
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
		return "", &tree.LimitError{Limit: expected + " bytes", Max: maximum, Actual: int64(len(contents))}
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

func (h *webHandler) renderBusy(w http.ResponseWriter) {
	w.Header().Set("Retry-After", "1")
	h.renderError(w, http.StatusServiceUnavailable, "Registry is busy", "Try again in a moment.")
}

func (h *webHandler) closeUpload(submission *submission) bool {
	if err := submission.Close(); err != nil {
		h.options.Logger.Warn("web upload cleanup failed", "error", err)
		return false
	}
	return true
}

func (h *webHandler) admitTreeWork() bool {
	return h.treeWork.TryAcquire(1)
}

func (h *webHandler) releaseTreeWork() {
	h.treeWork.Release(1)
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

func (h *webHandler) handleError(w http.ResponseWriter, r *http.Request, err error) {
	var maxBytes *http.MaxBytesError
	switch {
	case errors.Is(err, tree.ErrLimitExceeded), errors.As(err, &maxBytes):
		h.renderError(w, http.StatusRequestEntityTooLarge, "Upload is too large", "Choose a smaller upload and try again.")
	case errors.Is(err, errMalformedUpload), errors.Is(err, tree.ErrInvalidPath),
		errors.Is(err, agentskill.ErrInvalidName), errors.Is(err, agentskill.ErrInvalidDocument), errors.Is(err, agentskill.ErrInvalidTree):
		h.renderError(w, http.StatusBadRequest, "Upload is invalid", "Check the skill files and upload them again.")
	case errors.Is(err, servercatalog.ErrNotFound):
		h.renderError(w, http.StatusNotFound, "Registry item was not found", "Return to the catalog and choose another item.")
	case errors.Is(err, servercatalog.ErrConflict):
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
func (h *webHandler) render(w http.ResponseWriter, status int, name string, data pageView) {
	var page bytes.Buffer
	templates, ok := h.pages[name]
	if !ok {
		h.options.Logger.Error("web page template is not configured", "template", name)
		h.renderError(w, http.StatusInternalServerError, "Page could not be rendered", "Try again. If this keeps happening, contact the registry operator.")
		return
	}
	if err := templates.ExecuteTemplate(&page, "page", data); err != nil {
		h.options.Logger.Error("web page template failed", "template", name, "error", err)
		h.renderError(w, http.StatusInternalServerError, "Page could not be rendered", "Try again. If this keeps happening, contact the registry operator.")
		return
	}
	h.writePage(w, status, page.Bytes())
}

func (h *webHandler) renderError(w http.ResponseWriter, status int, title, action string) {
	var page bytes.Buffer
	templates, ok := h.pages["error"]
	if !ok {
		h.options.Logger.Error("web error template is not configured")
		http.Error(w, http.StatusText(status), status)
		return
	}
	data := pageView{
		Title: title,
		Content: struct{ Title, Action string }{
			Title: title, Action: action,
		},
	}
	if err := templates.ExecuteTemplate(&page, "page", data); err != nil {
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
func (h *webHandler) writePage(w http.ResponseWriter, status int, page []byte) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Content-Security-Policy", "default-src 'self'")
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(status)
	if _, err := w.Write(page); err != nil {
		h.options.Logger.Warn("web response write failed", "error", err)
	}
}
