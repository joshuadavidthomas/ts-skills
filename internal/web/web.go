package web

import (
	"archive/zip"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"io"
	"io/fs"
	"mime"
	"mime/multipart"
	"net/http"
	"os"
	"sort"
	"time"
	"unicode/utf8"

	"github.com/gorilla/csrf"
	"github.com/joshuadavidthomas/ts-skill-registry/internal/agentskill"
	"github.com/joshuadavidthomas/ts-skill-registry/internal/protocol"
	"github.com/joshuadavidthomas/ts-skill-registry/internal/registry"
	"github.com/joshuadavidthomas/ts-skill-registry/internal/safetree"
	"github.com/joshuadavidthomas/ts-skill-registry/internal/upload"
)

const maxRequestBytes int64 = 32 << 20

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

type ActorResolver interface {
	Actor(*http.Request) (registry.Actor, error)
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
	info, err := os.Stat(options.StagingParent)
	if err != nil {
		return nil, fmt.Errorf("stat web staging parent: %w", err)
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("web staging parent must be a directory")
	}
	pages, err := template.New("pages").Parse(pageTemplates)
	if err != nil {
		return nil, fmt.Errorf("parse web templates: %w", err)
	}
	h := &handler{catalog: catalog, actors: actors, options: options, pages: pages}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /", h.catalogPage)
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
	Skill  string
	Digest string
}

func (h *handler) catalogPage(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		h.renderError(w, http.StatusNotFound, "Page was not found", "Return to the catalog.")
		return
	}
	summaries, err := h.catalog.ListSkills(r.Context())
	if err != nil {
		h.handleError(w, err)
		return
	}
	views := make([]skillView, 0, len(summaries))
	for _, summary := range summaries {
		views = append(views, skillView{Skill: summary.Skill().String(), Digest: summary.Current().Tree().String()})
	}
	h.render(w, http.StatusOK, "catalog", catalogPageData{Skills: views})
}

func (h *handler) uploadPage(w http.ResponseWriter, r *http.Request) {
	token := csrf.Token(r)
	w.Header().Set("X-CSRF-Token", token)
	h.render(w, http.StatusOK, "upload", struct{ CSRFToken string }{CSRFToken: token})
}

func (h *handler) createCandidate(w http.ResponseWriter, r *http.Request) {
	actor, err := h.actors.Actor(r)
	if err != nil {
		h.renderError(w, http.StatusUnauthorized, "Identity could not be verified", "Reconnect to the Tailnet and try again.")
		return
	}
	mediaType, parameters, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || mediaType != "multipart/form-data" || parameters["boundary"] == "" {
		h.renderError(w, http.StatusBadRequest, "Upload format is invalid", "Submit the ZIP or directory from the upload page.")
		return
	}
	body := multipart.NewReader(r.Body, parameters["boundary"])
	namespaceText, err := nextTextPart(body, "namespace", 1024)
	if err != nil {
		h.handleError(w, err)
		return
	}
	namespace, err := registry.ParseNamespace(namespaceText)
	if err != nil {
		h.renderError(w, http.StatusBadRequest, "Namespace is invalid", "Enter a namespace without spaces or path separators.")
		return
	}
	kindText, err := nextTextPart(body, "kind", 32)
	if err != nil {
		h.handleError(w, err)
		return
	}

	var submission *upload.Submission
	var kind registry.UploadKind
	switch kindText {
	case "zip":
		kind = registry.UploadZIP
		part, nextErr := body.NextPart()
		if nextErr != nil || part.FormName() != "archive" || part.FileName() == "" {
			h.handleError(w, malformedRequest("an archive file part must follow kind", nextErr))
			return
		}
		submission, err = upload.StageZIP(r.Context(), h.options.StagingParent, part, part.FileName(), h.options.Limits)
		if err == nil {
			if extra, nextErr := body.NextPart(); nextErr != io.EOF {
				if nextErr == nil {
					_ = extra.Close()
				}
				err = malformedRequest("ZIP upload contains an extra multipart part", nextErr)
			}
		}
	case "directory":
		kind = registry.UploadDirectory
		submission, err = upload.StageBrowserDirectory(r.Context(), h.options.StagingParent, body, h.options.Limits)
	default:
		err = malformedRequest("kind must be zip or directory", nil)
	}
	if err != nil {
		h.handleError(w, err)
		return
	}
	defer submission.Close()

	source, err := registry.NewUploadSource(kind, submission.Label())
	if err != nil {
		h.renderError(w, http.StatusBadRequest, "Upload label is invalid", "Rename the ZIP or selected directory and try again.")
		return
	}
	provenance, err := registry.NewProvenance(source, actor, time.Now().UTC())
	if err != nil {
		h.handleError(w, err)
		return
	}
	candidate, err := h.catalog.Capture(r.Context(), registry.CaptureRequest{
		Namespace: namespace, Source: submission.FS(), Root: submission.Root(), Provenance: provenance,
	})
	if err != nil {
		h.handleError(w, err)
		return
	}
	http.Redirect(w, r, "/candidates/"+candidate.ID().String(), http.StatusSeeOther)
}

type reviewPageData struct {
	CandidateID string
	Skill       string
	Digest      string
	Source      string
	SubmittedBy string
	SubmittedAt string
	Files       []string
	SkillMD     string
	Published   bool
	CSRFField   template.HTML
}

func (h *handler) reviewCandidate(w http.ResponseWriter, r *http.Request) {
	id, err := registry.ParseCandidateID(r.PathValue("candidate"))
	if err != nil {
		h.renderError(w, http.StatusNotFound, "Candidate was not found", "Return to the catalog and choose another candidate.")
		return
	}
	candidate, err := h.catalog.Candidate(r.Context(), id)
	if err != nil {
		h.handleError(w, err)
		return
	}
	tree, err := h.catalog.OpenCandidateTree(r.Context(), id)
	if err != nil {
		h.handleError(w, err)
		return
	}
	defer tree.Close()
	files, skillDocument, err := reviewTree(tree)
	if err != nil {
		h.handleError(w, err)
		return
	}
	publicationID, err := registry.NewPublicationID(candidate.Skill(), candidate.Tree())
	if err != nil {
		h.handleError(w, err)
		return
	}
	_, publicationErr := h.catalog.Publication(r.Context(), publicationID)
	published := publicationErr == nil
	if publicationErr != nil && !errors.Is(publicationErr, registry.ErrNotFound) {
		h.handleError(w, publicationErr)
		return
	}
	provenance := candidate.Provenance()
	data := reviewPageData{
		CandidateID: id.String(), Skill: candidate.Skill().String(), Digest: candidate.Tree().String(),
		Source: provenance.Source().Label(), SubmittedBy: provenance.SubmittedBy().Display(),
		SubmittedAt: provenance.SubmittedAt().Format(time.RFC3339), Files: files, SkillMD: skillDocument,
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
	actor, err := h.actors.Actor(r)
	if err != nil {
		h.renderError(w, http.StatusUnauthorized, "Identity could not be verified", "Reconnect to the Tailnet and try again.")
		return
	}
	if _, err := h.catalog.Publish(r.Context(), id, actor, time.Now().UTC()); err != nil {
		h.handleError(w, err)
		return
	}
	http.Redirect(w, r, "/candidates/"+id.String(), http.StatusSeeOther)
}

func (h *handler) setCurrent(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		h.handleError(w, malformedRequest("current publication form is invalid", err))
		return
	}
	if len(r.Form["skill"]) != 1 || len(r.Form["digest"]) != 1 {
		h.handleError(w, malformedRequest("current publication form must contain one skill and digest", nil))
		return
	}
	skill, err := registry.ParseSkillID(r.Form.Get("skill"))
	if err != nil {
		h.handleError(w, malformedRequest("current skill identity is invalid", err))
		return
	}
	digest, err := agentskill.ParseTreeDigest(r.Form.Get("digest"))
	if err != nil {
		h.handleError(w, malformedRequest("current tree digest is invalid", err))
		return
	}
	publication, err := registry.NewPublicationID(skill, digest)
	if err != nil {
		h.handleError(w, err)
		return
	}
	actor, err := h.actors.Actor(r)
	if err != nil {
		h.renderError(w, http.StatusUnauthorized, "Identity could not be verified", "Reconnect to the Tailnet and try again.")
		return
	}
	if _, err := h.catalog.SetCurrent(r.Context(), publication, actor, time.Now().UTC()); err != nil {
		h.handleError(w, err)
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
	publicationID, err := registry.NewPublicationID(skill, digest)
	if err != nil {
		h.writeAPIError(w, protocol.CodeInvalidRequest)
		return
	}
	if _, err := h.catalog.Publication(r.Context(), publicationID); err != nil {
		h.writeAPIDomainError(w, err)
		return
	}
	tree, err := h.catalog.OpenPublicationTree(r.Context(), publicationID)
	if err != nil {
		h.writeAPIDomainError(w, err)
		return
	}
	defer tree.Close()

	archive, err := h.rootlessZIP(r.Context(), tree)
	if err != nil {
		h.writeAPIDomainError(w, err)
		return
	}
	defer func() {
		name := archive.Name()
		_ = archive.Close()
		_ = os.Remove(name)
	}()
	w.Header().Set("Content-Type", "application/zip")
	w.Header().Set("Content-Disposition", `attachment; filename="`+skill.Name().String()+`.zip"`)
	http.ServeContent(w, r, skill.Name().String()+".zip", time.Time{}, archive)
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
			_ = writer.Close()
			return nil, err
		}
		header := &zip.FileHeader{Name: name, Method: zip.Store}
		header.Modified = time.Date(1980, time.January, 1, 0, 0, 0, 0, time.UTC)
		header.SetMode(0o644)
		output, err := writer.CreateHeader(header)
		if err != nil {
			_ = writer.Close()
			return nil, fmt.Errorf("create tree archive entry: %w", err)
		}
		input, err := tree.Open(name)
		if err != nil {
			_ = writer.Close()
			return nil, fmt.Errorf("open published tree file: %w", err)
		}
		_, copyErr := io.Copy(output, input)
		closeErr := input.Close()
		if err := errors.Join(copyErr, closeErr); err != nil {
			_ = writer.Close()
			return nil, fmt.Errorf("write tree archive entry: %w", err)
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

func reviewTree(tree fs.FS) ([]string, string, error) {
	var files []string
	err := fs.WalkDir(tree, ".", func(name string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if name == "." || entry.IsDir() {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("review tree contains non-regular file %q", name)
		}
		files = append(files, name)
		return nil
	})
	if err != nil {
		return nil, "", fmt.Errorf("list candidate tree: %w", err)
	}
	sort.Strings(files)
	document, err := fs.ReadFile(tree, agentskill.Filename)
	if err != nil {
		return nil, "", fmt.Errorf("read candidate SKILL.md: %w", err)
	}
	return files, string(document), nil
}

func malformedRequest(problem string, cause error) error {
	if cause == nil {
		return fmt.Errorf("%w: %s", upload.ErrMalformedUpload, problem)
	}
	return fmt.Errorf("%w: %s: %w", upload.ErrMalformedUpload, problem, cause)
}

func (h *handler) handleError(w http.ResponseWriter, err error) {
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
		h.renderError(w, http.StatusInternalServerError, "Request could not be completed", "Try again. If this keeps happening, contact the registry operator.")
	}
}

func (h *handler) render(w http.ResponseWriter, status int, name string, data any) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	if err := h.pages.ExecuteTemplate(w, name, data); err != nil {
		return
	}
}

func (h *handler) renderError(w http.ResponseWriter, status int, title, action string) {
	h.render(w, status, "error", struct{ Title, Action string }{title, action})
}

const pageTemplates = `
{{define "head"}}<!doctype html><html lang="en"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1"><title>ts-skills</title></head><body><nav><a href="/">Catalog</a> <a href="/upload">Upload</a></nav>{{end}}
{{define "catalog"}}{{template "head" .}}<main><h1>Published skills</h1>{{if .Skills}}<ul>{{range .Skills}}<li><strong>{{.Skill}}</strong> <code>{{.Digest}}</code></li>{{end}}</ul>{{else}}<p>No skills have been published.</p>{{end}}</main></body></html>{{end}}
{{define "upload"}}{{template "head" .}}<meta name="csrf-token" content="{{.CSRFToken}}"><main><h1>Upload a skill</h1><p>Choose one ZIP file or one directory. Uploaded files are stored for review and are never run.</p><form id="upload-form"><label>Namespace <input name="namespace" required maxlength="256"></label><fieldset><legend>Upload type</legend><label><input type="radio" name="kind" value="zip" checked> ZIP</label><label><input type="radio" name="kind" value="directory"> Directory</label></fieldset><label>ZIP file <input type="file" id="archive" accept=".zip,application/zip"></label><label>Directory <input type="file" id="directory" webkitdirectory multiple></label><button type="submit">Stage for review</button></form><p id="upload-status" role="status"></p></main><script>
const form=document.getElementById('upload-form');
form.addEventListener('submit',async(event)=>{event.preventDefault();const data=new FormData();data.append('namespace',form.elements.namespace.value);const kind=form.elements.kind.value;data.append('kind',kind);if(kind==='zip'){const archive=document.getElementById('archive').files[0];if(!archive){document.getElementById('upload-status').textContent='Choose a ZIP file.';return;}data.append('archive',archive,archive.name);}else{const files=Array.from(document.getElementById('directory').files).sort((a,b)=>a.webkitRelativePath.localeCompare(b.webkitRelativePath));if(files.length===0){document.getElementById('upload-status').textContent='Choose a directory.';return;}const manifest=files.map((file,index)=>({index:index,path:file.webkitRelativePath,size:file.size}));data.append('manifest',JSON.stringify(manifest));files.forEach((file,index)=>data.append('file-'+index,file,file.name));}document.getElementById('upload-status').textContent='Uploading…';const response=await fetch('/candidates',{method:'POST',headers:{'X-CSRF-Token':document.querySelector('meta[name="csrf-token"]').content},body:data});if(response.redirected){window.location=response.url;return;}document.open();document.write(await response.text());document.close();});
</script></body></html>{{end}}
{{define "review"}}{{template "head" .}}<main><h1>Review {{.Skill}}</h1><dl><dt>Candidate</dt><dd><code>{{.CandidateID}}</code></dd><dt>Digest</dt><dd><code>{{.Digest}}</code></dd><dt>Source</dt><dd>{{.Source}}</dd><dt>Submitted by</dt><dd>{{.SubmittedBy}}</dd><dt>Submitted at</dt><dd>{{.SubmittedAt}}</dd></dl><h2>Files</h2><ul>{{range .Files}}<li><code>{{.}}</code></li>{{end}}</ul><h2>SKILL.md</h2><pre>{{.SkillMD}}</pre>{{if .Published}}<p>This candidate is published.</p><form method="post" action="/current">{{.CSRFField}}<input type="hidden" name="skill" value="{{.Skill}}"><input type="hidden" name="digest" value="{{.Digest}}"><button type="submit">Make current</button></form>{{else}}<form method="post" action="/candidates/{{.CandidateID}}/publish">{{.CSRFField}}<button type="submit">Publish</button></form>{{end}}</main></body></html>{{end}}
{{define "error"}}{{template "head" .}}<main><h1>{{.Title}}</h1><p>{{.Action}}</p></main></body></html>{{end}}
`
