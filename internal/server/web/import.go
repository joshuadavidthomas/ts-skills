package web

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path"
	"sort"
	"strings"
	"time"

	"github.com/joshuadavidthomas/ts-skills/internal/agentskill"
	"github.com/joshuadavidthomas/ts-skills/internal/registry"
	servercatalog "github.com/joshuadavidthomas/ts-skills/internal/server/catalog"
	"github.com/joshuadavidthomas/ts-skills/internal/tree"
)

const maxRepositoryFormBytes = 64 << 10

type repositorySkillView struct {
	Path        string
	Name        string
	Description string
}

type importedCandidateView struct {
	ID    string
	Skill string
}

type importPageData struct {
	Repository string
	Skills     []repositorySkillView
	Candidates []importedCandidateView
	Imported   bool
}

func (h *webHandler) importPage(w http.ResponseWriter, r *http.Request) {
	if _, ok := h.resolveCurator(w, r); !ok {
		return
	}
	h.render(w, http.StatusOK, "import", pageView{Title: "Import repository", Content: importPageData{}})
}

func (h *webHandler) importRepository(w http.ResponseWriter, r *http.Request) {
	curator, ok := h.resolveCurator(w, r)
	if !ok {
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxRepositoryFormBytes)
	if err := r.ParseForm(); err != nil {
		h.renderError(w, http.StatusBadRequest, "Repository request is invalid", "Check the repository URL and try again.")
		return
	}
	repository := strings.TrimSpace(r.Form.Get("repository"))
	if err := validateRepositoryURL(repository); err != nil {
		h.renderError(w, http.StatusBadRequest, "Repository URL is invalid", "Use an HTTPS Git clone URL without embedded credentials.")
		return
	}
	if !h.admitTreeWork() {
		h.renderBusy(w)
		return
	}
	defer h.releaseTreeWork()

	repoDir, revision, cleanup, err := cloneRepository(r.Context(), h.options.StagingParent, repository)
	if err != nil {
		h.options.Logger.Info("web repository clone failed", "repository", repository, "error", err)
		h.renderError(w, http.StatusBadRequest, "Repository could not be read", "Confirm that the HTTPS clone URL is public and try again.")
		return
	}
	defer cleanup()
	skills, err := discoverRepositorySkills(r.Context(), repoDir)
	if err != nil {
		h.handleError(w, r, err)
		return
	}
	if len(skills) == 0 {
		h.renderError(w, http.StatusUnprocessableEntity, "No valid skills were found", "The repository must contain at least one valid SKILL.md in its skill directory.")
		return
	}
	if r.Form.Get("action") != "stage" {
		h.render(w, http.StatusOK, "import", pageView{Title: "Choose skills", Content: importPageData{Repository: repository, Skills: skills}})
		return
	}

	namespace, err := registry.ParseNamespace(r.Form.Get("namespace"))
	if err != nil {
		h.renderError(w, http.StatusBadRequest, "Namespace is invalid", "Use 1–64 lowercase letters, digits, and internal hyphens.")
		return
	}
	selected := make(map[string]bool, len(r.Form["skill"]))
	for _, item := range r.Form["skill"] {
		selected[item] = true
	}
	if len(selected) == 0 {
		h.renderError(w, http.StatusBadRequest, "No skills were selected", "Choose at least one skill to stage.")
		return
	}
	available := make(map[string]repositorySkillView, len(skills))
	for _, skill := range skills {
		available[skill.Path] = skill
	}
	for item := range selected {
		if _, ok := available[item]; !ok {
			h.renderError(w, http.StatusBadRequest, "Skill selection is invalid", "Inspect the repository again and choose from the skills shown.")
			return
		}
	}
	for _, skill := range skills {
		if selected[skill.Path] && len(repositorySource(repository, revision, skill.Path)) > 256 {
			h.renderError(w, http.StatusBadRequest, "Repository URL is too long", "Use a shorter clone URL so the selected skill can be recorded with its commit provenance.")
			return
		}
	}

	candidates := make([]importedCandidateView, 0, len(selected))
	for _, skill := range skills {
		if !selected[skill.Path] {
			continue
		}
		rooted, err := fs.Sub(os.DirFS(repoDir), skill.Path)
		if err != nil {
			h.handleError(w, r, err)
			return
		}
		snapshot, err := tree.Stage(r.Context(), h.options.StagingParent, ".ts-skills-import-*", rooted)
		if err != nil {
			h.handleError(w, r, err)
			return
		}
		source := repositorySource(repository, revision, skill.Path)
		candidate, captureErr := h.catalog.Capture(r.Context(), curator, servercatalog.CaptureRequest{
			Namespace: namespace, Staged: snapshot, Root: ".", Source: source, SubmittedAt: time.Now().UTC(),
		})
		closeErr := snapshot.Close()
		if err := errors.Join(captureErr, closeErr); err != nil {
			h.handleError(w, r, err)
			return
		}
		candidates = append(candidates, importedCandidateView{ID: candidate.ID.String(), Skill: candidate.Skill.String()})
	}
	h.render(w, http.StatusCreated, "import", pageView{Title: "Skills staged", Content: importPageData{Imported: true, Candidates: candidates}})
}

func repositorySource(repository, revision, skillPath string) string {
	return repository + "@" + revision + "#" + skillPath
}

func validateRepositoryURL(raw string) error {
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Scheme != "https" || parsed.Host == "" || parsed.User != nil || parsed.Fragment != "" {
		return fmt.Errorf("repository must be an HTTPS URL without credentials or fragment")
	}
	return nil
}

func cloneRepository(ctx context.Context, parent, repository string) (string, string, func(), error) {
	destination, err := os.MkdirTemp(parent, ".ts-skills-repo-*")
	if err != nil {
		return "", "", func() {}, fmt.Errorf("create repository staging directory: %w", err)
	}
	cleanup := func() { _ = os.RemoveAll(destination) }
	command := exec.CommandContext(ctx, "git", "-c", "core.hooksPath=/dev/null", "clone", "--depth=1", "--single-branch", "--", repository, destination)
	command.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
	if output, err := command.CombinedOutput(); err != nil {
		cleanup()
		return "", "", func() {}, fmt.Errorf("clone repository: %w: %s", err, strings.TrimSpace(string(output)))
	}
	revisionCommand := exec.CommandContext(ctx, "git", "-C", destination, "rev-parse", "HEAD")
	revisionBytes, err := revisionCommand.Output()
	if err != nil {
		cleanup()
		return "", "", func() {}, fmt.Errorf("resolve repository revision: %w", err)
	}
	return destination, strings.TrimSpace(string(revisionBytes)), cleanup, nil
}

func discoverRepositorySkills(ctx context.Context, root string) ([]repositorySkillView, error) {
	repository := os.DirFS(root)
	var skills []repositorySkillView
	err := fs.WalkDir(repository, ".", func(name string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if entry.IsDir() && (entry.Name() == ".git" || entry.Name() == "node_modules") {
			return fs.SkipDir
		}
		if entry.IsDir() || entry.Name() != agentskill.Filename {
			return nil
		}
		directory := path.Dir(name)
		skill, err := agentskill.Load(repository, directory)
		if err != nil {
			return nil
		}
		document := skill.Document()
		skills = append(skills, repositorySkillView{Path: directory, Name: document.Name.String(), Description: document.Description})
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("discover repository skills: %w", err)
	}
	sort.Slice(skills, func(i, j int) bool { return skills[i].Path < skills[j].Path })
	return skills, nil
}
