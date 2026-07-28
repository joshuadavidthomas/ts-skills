package registry_test

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"sort"
	"sync"
	"testing"
	"testing/fstest"
	"time"

	"github.com/joshuadavidthomas/ts-skills/internal/agentskill"
	"github.com/joshuadavidthomas/ts-skills/internal/install"
	"github.com/joshuadavidthomas/ts-skills/internal/registry"
	"github.com/joshuadavidthomas/ts-skills/internal/safetree"
)

type memoryCatalogRecords struct {
	mu             sync.Mutex
	candidates     map[registry.CandidateID]registry.Candidate
	candidateTrees map[registry.CandidateID]fstest.MapFS
	publications   map[registry.PublicationID]registry.Publication
	current        map[registry.SkillID]registry.CurrentPublication
}

func newMemoryCatalogRecords() *memoryCatalogRecords {
	return &memoryCatalogRecords{
		candidates:     make(map[registry.CandidateID]registry.Candidate),
		candidateTrees: make(map[registry.CandidateID]fstest.MapFS),
		publications:   make(map[registry.PublicationID]registry.Publication),
		current:        make(map[registry.SkillID]registry.CurrentPublication),
	}
}

func (m *memoryCatalogRecords) RecordCandidate(ctx context.Context, candidate registry.Candidate, directory agentskill.Directory) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	tree, err := copyTree(directory.FS())
	if err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, exists := m.candidates[candidate.ID()]; exists {
		return registry.ErrConflict
	}
	m.candidates[candidate.ID()] = candidate
	m.candidateTrees[candidate.ID()] = tree
	return nil
}

func (m *memoryCatalogRecords) Candidate(ctx context.Context, id registry.CandidateID) (registry.Candidate, error) {
	if err := ctx.Err(); err != nil {
		return registry.Candidate{}, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	candidate, exists := m.candidates[id]
	if !exists {
		return registry.Candidate{}, registry.ErrNotFound
	}
	return candidate, nil
}

func (m *memoryCatalogRecords) OpenCandidateTree(ctx context.Context, id registry.CandidateID) (registry.Tree, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	tree, exists := m.candidateTrees[id]
	if !exists {
		return nil, registry.ErrNotFound
	}
	return &memoryTree{files: cloneMapFS(tree)}, nil
}

func (m *memoryCatalogRecords) PublishCandidate(ctx context.Context, id registry.CandidateID, actor registry.Actor, at time.Time) (registry.PublishResult, error) {
	if err := ctx.Err(); err != nil {
		return registry.PublishResult{}, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	candidate, exists := m.candidates[id]
	if !exists {
		return registry.PublishResult{}, registry.ErrNotFound
	}
	publicationID, err := registry.NewPublicationID(candidate.Skill(), candidate.Tree())
	if err != nil {
		return registry.PublishResult{}, err
	}
	if publication, exists := m.publications[publicationID]; exists {
		return registry.NewPublishResult(publication, false, false)
	}
	publication, err := registry.NewPublication(publicationID, id, actor, at)
	if err != nil {
		return registry.PublishResult{}, err
	}
	m.publications[publicationID] = publication
	becameCurrent := false
	if _, exists := m.current[candidate.Skill()]; !exists {
		selected, err := registry.NewCurrentPublication(publicationID, actor, at)
		if err != nil {
			return registry.PublishResult{}, err
		}
		m.current[candidate.Skill()] = selected
		becameCurrent = true
	}
	return registry.NewPublishResult(publication, true, becameCurrent)
}

func (m *memoryCatalogRecords) SelectCurrent(ctx context.Context, id registry.PublicationID, actor registry.Actor, at time.Time) (registry.CurrentPublication, error) {
	if err := ctx.Err(); err != nil {
		return registry.CurrentPublication{}, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, exists := m.publications[id]; !exists {
		return registry.CurrentPublication{}, registry.ErrNotFound
	}
	selected, err := registry.NewCurrentPublication(id, actor, at)
	if err != nil {
		return registry.CurrentPublication{}, err
	}
	m.current[id.Skill()] = selected
	return selected, nil
}

func (m *memoryCatalogRecords) ListPublishedSkills(ctx context.Context) ([]registry.SkillSummary, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	summaries := make([]registry.SkillSummary, 0, len(m.current))
	for skill, current := range m.current {
		summary, err := registry.NewSkillSummary(skill, current.Publication())
		if err != nil {
			return nil, err
		}
		summaries = append(summaries, summary)
	}
	sort.Slice(summaries, func(i, j int) bool {
		return summaries[i].Skill().String() < summaries[j].Skill().String()
	})
	return summaries, nil
}

func (m *memoryCatalogRecords) ResolveCurrent(ctx context.Context, skill registry.SkillID) (registry.Publication, error) {
	if err := ctx.Err(); err != nil {
		return registry.Publication{}, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	current, exists := m.current[skill]
	if !exists {
		return registry.Publication{}, registry.ErrNotFound
	}
	publication, exists := m.publications[current.Publication()]
	if !exists {
		return registry.Publication{}, registry.ErrNotFound
	}
	return publication, nil
}

func (m *memoryCatalogRecords) Publication(ctx context.Context, id registry.PublicationID) (registry.Publication, error) {
	if err := ctx.Err(); err != nil {
		return registry.Publication{}, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	publication, exists := m.publications[id]
	if !exists {
		return registry.Publication{}, registry.ErrNotFound
	}
	return publication, nil
}

func (m *memoryCatalogRecords) OpenPublicationTree(ctx context.Context, id registry.PublicationID) (registry.Tree, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	publication, exists := m.publications[id]
	if !exists {
		return nil, registry.ErrNotFound
	}
	tree, exists := m.candidateTrees[publication.Candidate()]
	if !exists {
		return nil, registry.ErrNotFound
	}
	return &memoryTree{files: cloneMapFS(tree)}, nil
}

type memoryTree struct {
	files  fstest.MapFS
	closed bool
}

func (t *memoryTree) Open(name string) (fs.File, error) {
	if t.closed {
		return nil, fs.ErrClosed
	}
	return t.files.Open(name)
}

func (t *memoryTree) Close() error {
	if t.closed {
		return fmt.Errorf("memory tree closed more than once")
	}
	t.closed = true
	return nil
}

func copyTree(source fs.FS) (fstest.MapFS, error) {
	copied := make(fstest.MapFS)
	err := fs.WalkDir(source, ".", func(name string, entry fs.DirEntry, walkErr error) error {
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
			return fmt.Errorf("copy test tree: %q is not regular", name)
		}
		contents, err := fs.ReadFile(source, name)
		if err != nil {
			return err
		}
		copied[name] = &fstest.MapFile{Data: append([]byte(nil), contents...), Mode: 0o644}
		return nil
	})
	return copied, err
}

func cloneMapFS(source fstest.MapFS) fstest.MapFS {
	cloned := make(fstest.MapFS, len(source))
	for name, file := range source {
		copy := *file
		copy.Data = append([]byte(nil), file.Data...)
		cloned[name] = &copy
	}
	return cloned
}

func newCatalogFixture(t *testing.T) (*registry.Catalog, *memoryCatalogRecords, registry.Namespace, registry.Actor, registry.Provenance) {
	t.Helper()
	records := newMemoryCatalogRecords()
	catalog, err := registry.NewCatalog(records, t.TempDir(), safetree.PrototypeLimits())
	if err != nil {
		t.Fatal(err)
	}
	namespace, err := registry.ParseNamespace("team")
	if err != nil {
		t.Fatal(err)
	}
	actor, err := registry.NewActor("user:1", "Test User")
	if err != nil {
		t.Fatal(err)
	}
	source, err := registry.NewUploadSource("sample")
	if err != nil {
		t.Fatal(err)
	}
	provenance, err := registry.NewProvenance(source, actor, time.Date(2026, 1, 2, 3, 4, 5, 0, time.FixedZone("test", 3600)))
	if err != nil {
		t.Fatal(err)
	}
	return catalog, records, namespace, actor, provenance
}

func skillSource(instructions, asset string) fstest.MapFS {
	return fstest.MapFS{
		"sample/SKILL.md":        &fstest.MapFile{Data: []byte("---\nname: sample\ndescription: Test skill\n---\n" + instructions)},
		"sample/assets/data.txt": &fstest.MapFile{Data: []byte(asset)},
		"sample/scripts/run.sh":  &fstest.MapFile{Data: []byte("echo inert\n"), Mode: 0o777},
	}
}

func capture(t *testing.T, catalog *registry.Catalog, namespace registry.Namespace, provenance registry.Provenance, source fs.FS) registry.Candidate {
	t.Helper()
	candidate, err := catalog.Capture(context.Background(), registry.CaptureRequest{
		Namespace:  namespace,
		Source:     source,
		Root:       "sample",
		Provenance: provenance,
	})
	if err != nil {
		t.Fatal(err)
	}
	return candidate
}

func TestCatalogCapturePublishAndCurrentTransitions(t *testing.T) {
	catalog, _, namespace, actor, provenance := newCatalogFixture(t)
	ctx := context.Background()
	original := skillSource("# First\n", "first")
	firstCandidate := capture(t, catalog, namespace, provenance, original)

	original["sample/assets/data.txt"].Data = []byte("mutated after capture")
	candidateTree, err := catalog.OpenCandidateTree(ctx, firstCandidate.ID())
	if err != nil {
		t.Fatal(err)
	}
	capturedAsset, err := fs.ReadFile(candidateTree, "assets/data.txt")
	if err != nil {
		t.Fatal(err)
	}
	if err := candidateTree.Close(); err != nil {
		t.Fatal(err)
	}
	if string(capturedAsset) != "first" {
		t.Fatalf("candidate tree changed with source: %q", capturedAsset)
	}

	firstPublish, err := catalog.Publish(ctx, firstCandidate.ID(), actor, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if !firstPublish.Created() || !firstPublish.BecameCurrent() {
		t.Fatalf("first publish flags = created %t, current %t", firstPublish.Created(), firstPublish.BecameCurrent())
	}
	repeated, err := catalog.Publish(ctx, firstCandidate.ID(), actor, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if repeated.Created() || repeated.BecameCurrent() || repeated.Publication().ID() != firstPublish.Publication().ID() {
		t.Fatalf("repeated publish = %#v", repeated)
	}
	equivalentCandidate := capture(t, catalog, namespace, provenance, skillSource("# First\n", "first"))
	equivalent, err := catalog.Publish(ctx, equivalentCandidate.ID(), actor, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if equivalent.Created() || equivalent.Publication().ID() != firstPublish.Publication().ID() || equivalent.Publication().Candidate() != firstCandidate.ID() {
		t.Fatalf("equivalent candidate created another publication: %#v", equivalent)
	}

	secondCandidate := capture(t, catalog, namespace, provenance, skillSource("# Second\n", "second"))
	secondPublish, err := catalog.Publish(ctx, secondCandidate.ID(), actor, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if !secondPublish.Created() || secondPublish.BecameCurrent() {
		t.Fatalf("second publish flags = created %t, current %t", secondPublish.Created(), secondPublish.BecameCurrent())
	}
	current, err := catalog.ResolveCurrent(ctx, firstCandidate.Skill())
	if err != nil {
		t.Fatal(err)
	}
	if current.ID() != firstPublish.Publication().ID() {
		t.Fatalf("second publish moved current to %s", current.ID().Tree())
	}
	selected, err := catalog.SetCurrent(ctx, secondPublish.Publication().ID(), actor, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if selected.Publication() != secondPublish.Publication().ID() {
		t.Fatalf("selected publication = %#v", selected.Publication())
	}
	current, err = catalog.ResolveCurrent(ctx, firstCandidate.Skill())
	if err != nil {
		t.Fatal(err)
	}
	if current.ID() != secondPublish.Publication().ID() {
		t.Fatalf("explicit selection did not move current")
	}
	summaries, err := catalog.ListSkills(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(summaries) != 1 || summaries[0].Current() != secondPublish.Publication().ID() {
		t.Fatalf("skill summaries = %#v", summaries)
	}
	exact, err := catalog.Publication(ctx, secondPublish.Publication().ID())
	if err != nil {
		t.Fatal(err)
	}
	if exact.ID() != secondPublish.Publication().ID() {
		t.Fatalf("exact publication lookup = %#v", exact.ID())
	}
	publicationTree, err := catalog.OpenPublicationTree(ctx, exact.ID())
	if err != nil {
		t.Fatal(err)
	}
	publishedAsset, err := fs.ReadFile(publicationTree, "assets/data.txt")
	if err != nil {
		t.Fatal(err)
	}
	if err := publicationTree.Close(); err != nil {
		t.Fatal(err)
	}
	if string(publishedAsset) != "second" {
		t.Fatalf("published tree asset = %q", publishedAsset)
	}

	unknownDigest, err := agentskill.ParseTreeDigest("sha256:ffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff")
	if err != nil {
		t.Fatal(err)
	}
	unknownPublication, err := registry.NewPublicationID(firstCandidate.Skill(), unknownDigest)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := catalog.SetCurrent(ctx, unknownPublication, actor, time.Now()); !errors.Is(err, registry.ErrNotFound) {
		t.Fatalf("select unpublished identity error = %v", err)
	}
}

type catalogRemote struct {
	catalog *registry.Catalog
}

func (r catalogRemote) Fetch(ctx context.Context, requirement install.Requirement) (install.FetchedSkill, error) {
	var (
		publication registry.Publication
		err         error
	)
	if digest, exact := requirement.ExactDigest(); exact {
		id, idErr := registry.NewPublicationID(requirement.Skill(), digest)
		if idErr != nil {
			return install.FetchedSkill{}, idErr
		}
		publication, err = r.catalog.Publication(ctx, id)
	} else {
		publication, err = r.catalog.ResolveCurrent(ctx, requirement.Skill())
	}
	if err != nil {
		return install.FetchedSkill{}, err
	}
	tree, err := r.catalog.OpenPublicationTree(ctx, publication.ID())
	if err != nil {
		return install.FetchedSkill{}, err
	}
	fetched, err := install.NewFetchedSkill(publication.ID(), tree)
	if err != nil {
		_ = tree.Close()
		return install.FetchedSkill{}, err
	}
	return fetched, nil
}

func TestCatalogBackedInstallUsesCapturedImmutableTree(t *testing.T) {
	catalog, _, namespace, actor, provenance := newCatalogFixture(t)
	source := skillSource("# Install me\n", "captured asset")
	candidate := capture(t, catalog, namespace, provenance, source)

	source["sample/SKILL.md"].Data = []byte("destroyed")
	source["sample/assets/data.txt"].Data = []byte("mutated asset")
	published, err := catalog.Publish(context.Background(), candidate.ID(), actor, time.Now())
	if err != nil {
		t.Fatal(err)
	}

	installer, err := install.NewInstaller(catalogRemote{catalog: catalog})
	if err != nil {
		t.Fatal(err)
	}
	project, err := install.OpenProject(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	requirement, err := install.Current(candidate.Skill())
	if err != nil {
		t.Fatal(err)
	}
	locked, err := installer.Install(context.Background(), project, requirement)
	if err != nil {
		t.Fatal(err)
	}
	if locked.Publication() != published.Publication().ID() {
		t.Fatalf("locked publication = %#v, want %#v", locked.Publication(), published.Publication().ID())
	}

	asset, err := os.ReadFile(project.SkillsDir() + "/sample/assets/data.txt")
	if err != nil {
		t.Fatal(err)
	}
	if string(asset) != "captured asset" {
		t.Fatalf("installed mutable source bytes %q", asset)
	}
	script, err := os.Stat(project.SkillsDir() + "/sample/scripts/run.sh")
	if err != nil {
		t.Fatal(err)
	}
	if script.Mode().Perm()&0o111 != 0 {
		t.Fatalf("installed script is executable: %v", script.Mode())
	}
	installedDigest, err := agentskill.SumTree(context.Background(), os.DirFS(project.SkillsDir()), "sample")
	if err != nil {
		t.Fatal(err)
	}
	if installedDigest != published.Publication().ID().Tree() {
		t.Fatalf("installed digest = %s, want %s", installedDigest, published.Publication().ID().Tree())
	}
}
