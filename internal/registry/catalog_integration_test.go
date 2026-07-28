package registry_test

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"sync"
	"testing"
	"testing/fstest"
	"time"

	"github.com/joshuadavidthomas/ts-skills/internal/agentskill"
	"github.com/joshuadavidthomas/ts-skills/internal/install"
	"github.com/joshuadavidthomas/ts-skills/internal/registry"
	"github.com/joshuadavidthomas/ts-skills/internal/safetree"
	"github.com/joshuadavidthomas/ts-skills/internal/storage"
)

type observingCatalogStore struct {
	registry.CatalogStore
	beforeRecord func()
}

func (s observingCatalogStore) RecordCandidate(ctx context.Context, candidate registry.Candidate, directory agentskill.Directory) error {
	if s.beforeRecord != nil {
		s.beforeRecord()
	}
	return s.CatalogStore.RecordCandidate(ctx, candidate, directory)
}

type staleCurrentStore struct {
	registry.CatalogStore
	arrived   chan<- struct{}
	release   <-chan struct{}
	mu        sync.Mutex
	remaining int
}

func (s *staleCurrentStore) CurrentPublication(ctx context.Context, skill registry.SkillID) (registry.Publication, error) {
	s.mu.Lock()
	if s.remaining > 0 {
		s.remaining--
		s.mu.Unlock()
		s.arrived <- struct{}{}
		<-s.release
		return registry.Publication{}, registry.ErrNotFound
	}
	s.mu.Unlock()
	return s.CatalogStore.CurrentPublication(ctx, skill)
}

func newCatalogFixture(t *testing.T) (*registry.Catalog, *storage.Catalog, string, registry.Namespace, registry.Curator, registry.UploadSource, time.Time) {
	return newCatalogFixtureWithStore(t, func(store registry.CatalogStore) registry.CatalogStore { return store })
}

func newCatalogFixtureWithStore(t *testing.T, decorate func(registry.CatalogStore) registry.CatalogStore) (*registry.Catalog, *storage.Catalog, string, registry.Namespace, registry.Curator, registry.UploadSource, time.Time) {
	t.Helper()
	store, err := storage.OpenCatalog(context.Background(), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := store.Close(); err != nil {
			t.Error(err)
		}
	})
	staging := t.TempDir()
	catalog, err := registry.NewCatalog(decorate(store), staging, safetree.PrototypeLimits())
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
	submittedAt := time.Date(2026, 1, 2, 3, 4, 5, 0, time.FixedZone("test", 3600))
	return catalog, store, staging, namespace, registry.NewCurator(actor), source, submittedAt
}

func skillSource(instructions, asset string) fstest.MapFS {
	return fstest.MapFS{
		"sample/SKILL.md":        {Data: []byte("---\nname: sample\ndescription: Test skill\n---\n" + instructions)},
		"sample/assets/data.txt": {Data: []byte(asset)},
		"sample/scripts/run.sh":  {Data: []byte("echo inert\n"), Mode: 0o777},
	}
}

func capture(t *testing.T, catalog *registry.Catalog, namespace registry.Namespace, curator registry.Curator, source registry.UploadSource, submittedAt time.Time, tree fs.FS) registry.Candidate {
	t.Helper()
	snapshot, err := safetree.StageFS(context.Background(), t.TempDir(), tree, "sample", safetree.PrototypeLimits())
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := snapshot.Close(); err != nil {
			t.Error(err)
		}
	}()
	candidate, err := catalog.Capture(context.Background(), curator, registry.CaptureRequest{
		Namespace: namespace, Staged: snapshot, Root: "sample", Source: source, SubmittedAt: submittedAt,
	})
	if err != nil {
		t.Fatal(err)
	}
	return candidate
}

func TestCatalogCaptureBorrowsValidatedSnapshot(t *testing.T) {
	store, err := storage.OpenCatalog(context.Background(), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := store.Close(); err != nil {
			t.Error(err)
		}
	})
	staging := t.TempDir()
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
	curator := registry.NewCurator(actor)
	observed := observingCatalogStore{CatalogStore: store}
	observed.beforeRecord = func() {
		entries, err := os.ReadDir(staging)
		if err != nil {
			t.Fatal(err)
		}
		if len(entries) != 0 {
			t.Fatalf("Capture staged a second tree: %v", entries)
		}
	}
	catalog, err := registry.NewCatalog(observed, staging, safetree.PrototypeLimits())
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := safetree.StageFS(context.Background(), t.TempDir(), skillSource("# Instructions\n", "asset"), "sample", safetree.PrototypeLimits())
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := snapshot.Close(); err != nil {
			t.Error(err)
		}
	}()
	inspection, err := agentskill.Inspect(context.Background(), snapshot.FS(), "sample")
	if err != nil {
		t.Fatal(err)
	}
	candidate, err := catalog.Capture(context.Background(), curator, registry.CaptureRequest{
		Namespace: namespace, Staged: snapshot, Root: "sample", Source: source, SubmittedAt: time.Now(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if candidate.Tree() != inspection.Digest() {
		t.Fatalf("candidate digest = %s, want staged digest %s", candidate.Tree(), inspection.Digest())
	}
	if got := candidate.Provenance().SubmittedBy(); got != curator.Actor() {
		t.Fatalf("candidate submitter = %#v, want curator actor %#v", got, curator.Actor())
	}
	if _, err := fs.ReadFile(snapshot.FS(), "sample/SKILL.md"); err != nil {
		t.Fatalf("Capture closed its borrowed snapshot: %v", err)
	}
}

func TestCatalogCapturePublishAndCurrentTransitions(t *testing.T) {
	catalog, _, _, namespace, curator, source, submittedAt := newCatalogFixture(t)
	ctx := context.Background()
	missingCandidate, err := registry.NewCandidateID()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := catalog.Publish(ctx, missingCandidate, curator, time.Now()); !errors.Is(err, registry.ErrNotFound) {
		t.Fatalf("publish unknown candidate error = %v", err)
	}
	original := skillSource("# First\n", "first")
	firstCandidate := capture(t, catalog, namespace, curator, source, submittedAt, original)

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

	publishedAt := time.Date(2026, 1, 3, 4, 5, 6, 0, time.UTC)
	firstPublish, err := catalog.Publish(ctx, firstCandidate.ID(), curator, publishedAt)
	if err != nil {
		t.Fatal(err)
	}
	current, err := catalog.ResolveCurrent(ctx, firstCandidate.Skill())
	if err != nil || current != firstPublish {
		t.Fatalf("first publish did not become current (%#v, %v)", current, err)
	}
	laterActor, err := registry.NewActor("user:2", "Later User")
	if err != nil {
		t.Fatal(err)
	}
	repeated, err := catalog.Publish(ctx, firstCandidate.ID(), registry.NewCurator(laterActor), publishedAt.Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if repeated != firstPublish {
		t.Fatalf("repeated publish returned %#v, want original %#v", repeated, firstPublish)
	}
	equivalentCandidate := capture(t, catalog, namespace, curator, source, submittedAt, skillSource("# First\n", "first"))
	equivalent, err := catalog.Publish(ctx, equivalentCandidate.ID(), registry.NewCurator(laterActor), publishedAt.Add(2*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if equivalent != firstPublish {
		t.Fatalf("equivalent candidate returned %#v, want original %#v", equivalent, firstPublish)
	}

	secondCandidate := capture(t, catalog, namespace, curator, source, submittedAt, skillSource("# Second\n", "second"))
	secondPublish, err := catalog.Publish(ctx, secondCandidate.ID(), curator, publishedAt.Add(3*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if secondPublish.ID() == firstPublish.ID() {
		t.Fatalf("second publish returned the first publication %#v", secondPublish)
	}
	current, err = catalog.ResolveCurrent(ctx, firstCandidate.Skill())
	if err != nil {
		t.Fatal(err)
	}
	if current.ID() != firstPublish.ID() {
		t.Fatalf("second publish moved current to %s", current.ID().Tree())
	}
	if err := catalog.SetCurrent(ctx, secondPublish.ID(), registry.NewCurator(laterActor), publishedAt.Add(4*time.Hour)); err != nil {
		t.Fatal(err)
	}
	current, err = catalog.ResolveCurrent(ctx, firstCandidate.Skill())
	if err != nil {
		t.Fatal(err)
	}
	if current.ID() != secondPublish.ID() {
		t.Fatal("explicit selection did not move current")
	}
	summaries, err := catalog.ListSkills(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(summaries) != 1 || summaries[0].Current() != secondPublish.ID() {
		t.Fatalf("skill summaries = %#v", summaries)
	}
	exact, err := catalog.Publication(ctx, secondPublish.ID())
	if err != nil {
		t.Fatal(err)
	}
	if exact.ID() != secondPublish.ID() {
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
	if err := catalog.SetCurrent(ctx, unknownPublication, curator, time.Now()); !errors.Is(err, registry.ErrNotFound) {
		t.Fatalf("select unpublished identity error = %v", err)
	}
}

func TestCatalogConcurrentFirstPublicationsChooseOneCurrent(t *testing.T) {
	arrived := make(chan struct{}, 2)
	release := make(chan struct{})
	barrier := &staleCurrentStore{arrived: arrived, release: release, remaining: 2}
	catalog, _, _, namespace, curator, source, submittedAt := newCatalogFixtureWithStore(t, func(store registry.CatalogStore) registry.CatalogStore {
		barrier.CatalogStore = store
		return barrier
	})
	first := capture(t, catalog, namespace, curator, source, submittedAt, skillSource("# First\n", "first"))
	second := capture(t, catalog, namespace, curator, source, submittedAt, skillSource("# Second\n", "second"))

	type result struct {
		publication registry.Publication
		err         error
	}
	start := make(chan struct{})
	results := make(chan result, 2)
	var wait sync.WaitGroup
	for index, candidate := range []registry.Candidate{first, second} {
		wait.Add(1)
		go func(index int, candidate registry.Candidate) {
			defer wait.Done()
			<-start
			publication, err := catalog.Publish(context.Background(), candidate.ID(), curator, time.Date(2026, 1, 3, 4, 5, 6+index, 0, time.UTC))
			results <- result{publication: publication, err: err}
		}(index, candidate)
	}
	close(start)
	for range 2 {
		select {
		case <-arrived:
		case <-time.After(time.Second):
			t.Fatal("concurrent publishers did not both observe a missing current publication")
		}
	}
	close(release)
	wait.Wait()
	close(results)

	publications := make([]registry.Publication, 0, 2)
	for result := range results {
		if result.err != nil {
			t.Fatal(result.err)
		}
		publications = append(publications, result.publication)
	}
	if len(publications) != 2 || publications[0].ID() == publications[1].ID() {
		t.Fatalf("concurrent publications = %#v", publications)
	}
	current, err := catalog.ResolveCurrent(context.Background(), first.Skill())
	if err != nil {
		t.Fatal(err)
	}
	if current != publications[0] && current != publications[1] {
		t.Fatalf("current = %#v, want one of %#v", current, publications)
	}
	for _, publication := range publications {
		stored, err := catalog.Publication(context.Background(), publication.ID())
		if err != nil || stored != publication {
			t.Fatalf("stored concurrent publication = %#v, %v", stored, err)
		}
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
	catalog, _, _, namespace, curator, uploadSource, submittedAt := newCatalogFixture(t)
	source := skillSource("# Install me\n", "captured asset")
	candidate := capture(t, catalog, namespace, curator, uploadSource, submittedAt, source)

	source["sample/SKILL.md"].Data = []byte("destroyed")
	source["sample/assets/data.txt"].Data = []byte("mutated asset")
	published, err := catalog.Publish(context.Background(), candidate.ID(), curator, time.Now())
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
	if locked.Publication() != published.ID() {
		t.Fatalf("locked publication = %#v, want %#v", locked.Publication(), published.ID())
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
	if installedDigest != published.ID().Tree() {
		t.Fatalf("installed digest = %s, want %s", installedDigest, published.ID().Tree())
	}
}
