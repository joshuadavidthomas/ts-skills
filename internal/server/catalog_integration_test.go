package server

import (
	"context"
	"errors"
	"io/fs"
	"sync"
	"testing"
	"testing/fstest"
	"time"

	"github.com/joshuadavidthomas/ts-skills/internal/agentskill"
	"github.com/joshuadavidthomas/ts-skills/internal/safetree"
)

func newCatalogFixture(t *testing.T) (*catalog, agentskill.Namespace, curator, string, time.Time) {
	t.Helper()
	store, err := openCatalog(context.Background(), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := store.close(); err != nil {
			t.Error(err)
		}
	})
	namespace, err := agentskill.ParseNamespace("team")
	if err != nil {
		t.Fatal(err)
	}
	actor := actor{ID: "user:1", Display: "Test User"}
	source := "sample"
	submittedAt := time.Date(2026, 1, 2, 3, 4, 5, 0, time.FixedZone("test", 3600))
	return store, namespace, testCurator(actor), source, submittedAt
}

func skillSource(instructions, asset string) fstest.MapFS {
	return fstest.MapFS{
		"sample/SKILL.md":        {Data: []byte("---\nname: sample\ndescription: Test skill\n---\n" + instructions)},
		"sample/assets/data.txt": {Data: []byte(asset)},
		"sample/scripts/run.sh":  {Data: []byte("echo inert\n"), Mode: 0o777},
	}
}

func capture(t *testing.T, catalog *catalog, namespace agentskill.Namespace, curator curator, source string, submittedAt time.Time, tree fs.FS) candidate {
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
	candidate, err := catalog.capture(context.Background(), curator, captureRequest{
		Namespace: namespace, Staged: snapshot, Root: "sample", Source: source, SubmittedAt: submittedAt,
	})
	if err != nil {
		t.Fatal(err)
	}
	return candidate
}

func TestCatalogCaptureReturnsNoCandidateWhenStorageRejects(t *testing.T) {
	catalog, namespace, curator, source, submittedAt := newCatalogFixture(t)
	if err := catalog.close(); err != nil {
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
	got, err := catalog.capture(context.Background(), curator, captureRequest{
		Namespace: namespace, Staged: snapshot, Root: "sample", Source: source, SubmittedAt: submittedAt,
	})
	if err == nil {
		t.Fatal("capture succeeded after catalog close")
	}
	if got != (candidate{}) {
		t.Fatalf("candidate on failed capture = %#v", got)
	}
}

func TestCatalogCaptureBorrowsValidatedSnapshot(t *testing.T) {
	store, err := openCatalog(context.Background(), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := store.close(); err != nil {
			t.Error(err)
		}
	})
	namespace, err := agentskill.ParseNamespace("team")
	if err != nil {
		t.Fatal(err)
	}
	actor := actor{ID: "user:1", Display: "Test User"}
	source := "sample"
	curator := testCurator(actor)
	catalog := store
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
	candidate, err := catalog.capture(context.Background(), curator, captureRequest{
		Namespace: namespace, Staged: snapshot, Root: "sample", Source: source, SubmittedAt: time.Now(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if candidate.Tree != inspection.Digest() {
		t.Fatalf("candidate digest = %s, want staged digest %s", candidate.Tree, inspection.Digest())
	}
	if got := candidate.Provenance.SubmittedBy; got != curator.Actor {
		t.Fatalf("candidate submitter = %#v, want curator actor %#v", got, curator.Actor)
	}
	if _, err := fs.ReadFile(snapshot.FS(), "sample/SKILL.md"); err != nil {
		t.Fatalf("Capture closed its borrowed snapshot: %v", err)
	}
}

func TestCatalogCapturePublishAndCurrentTransitions(t *testing.T) {
	catalog, namespace, curator, source, submittedAt := newCatalogFixture(t)
	ctx := context.Background()
	missingCandidate, err := agentskill.NewCandidateID()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := catalog.publish(ctx, missingCandidate, curator, time.Now()); !errors.Is(err, errNotFound) {
		t.Fatalf("publish unknown candidate error = %v", err)
	}
	original := skillSource("# First\n", "first")
	firstCandidate := capture(t, catalog, namespace, curator, source, submittedAt, original)

	original["sample/assets/data.txt"].Data = []byte("mutated after capture")
	candidateTree, err := catalog.openCandidateTree(ctx, firstCandidate.ID)
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
	firstPublish, err := catalog.publish(ctx, firstCandidate.ID, curator, publishedAt)
	if err != nil {
		t.Fatal(err)
	}
	current, err := catalog.resolveCurrent(ctx, firstCandidate.Skill)
	if err != nil || current != firstPublish {
		t.Fatalf("first publish did not become current (%#v, %v)", current, err)
	}
	laterActor := actor{ID: "user:2", Display: "Later User"}
	repeated, err := catalog.publish(ctx, firstCandidate.ID, testCurator(laterActor), publishedAt.Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if repeated != firstPublish {
		t.Fatalf("repeated publish returned %#v, want original %#v", repeated, firstPublish)
	}
	equivalentCandidate := capture(t, catalog, namespace, curator, source, submittedAt, skillSource("# First\n", "first"))
	equivalent, err := catalog.publish(ctx, equivalentCandidate.ID, testCurator(laterActor), publishedAt.Add(2*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if equivalent != firstPublish {
		t.Fatalf("equivalent candidate returned %#v, want original %#v", equivalent, firstPublish)
	}

	secondCandidate := capture(t, catalog, namespace, curator, source, submittedAt, skillSource("# Second\n", "second"))
	secondPublish, err := catalog.publish(ctx, secondCandidate.ID, curator, publishedAt.Add(3*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if secondPublish.ID == firstPublish.ID {
		t.Fatalf("second publish returned the first publication %#v", secondPublish)
	}
	current, err = catalog.resolveCurrent(ctx, firstCandidate.Skill)
	if err != nil {
		t.Fatal(err)
	}
	if current.ID != firstPublish.ID {
		t.Fatalf("second publish moved current to %s", current.ID.Tree())
	}
	if err := catalog.setCurrent(ctx, secondPublish.ID, testCurator(laterActor), publishedAt.Add(4*time.Hour)); err != nil {
		t.Fatal(err)
	}
	current, err = catalog.resolveCurrent(ctx, firstCandidate.Skill)
	if err != nil {
		t.Fatal(err)
	}
	if current.ID != secondPublish.ID {
		t.Fatal("explicit selection did not move current")
	}
	summaries, err := catalog.listSkills(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(summaries) != 1 || summaries[0].Current != secondPublish.ID {
		t.Fatalf("skill summaries = %#v", summaries)
	}
	exact, err := catalog.publication(ctx, secondPublish.ID)
	if err != nil {
		t.Fatal(err)
	}
	if exact.ID != secondPublish.ID {
		t.Fatalf("exact publication lookup = %#v", exact.ID)
	}
	publicationTree, err := catalog.openPublicationTree(ctx, exact.ID)
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
	unknownPublication, err := agentskill.NewPublicationID(firstCandidate.Skill, unknownDigest)
	if err != nil {
		t.Fatal(err)
	}
	if err := catalog.setCurrent(ctx, unknownPublication, curator, time.Now()); !errors.Is(err, errNotFound) {
		t.Fatalf("select unpublished identity error = %v", err)
	}
}

func TestCatalogConcurrentFirstPublicationsChooseOneCurrent(t *testing.T) {
	arrived := make(chan struct{}, 2)
	release := make(chan struct{})
	catalog, namespace, curator, source, submittedAt := newCatalogFixture(t)
	catalog.afterMissingCurrentLookup = func() {
		arrived <- struct{}{}
		<-release
	}
	first := capture(t, catalog, namespace, curator, source, submittedAt, skillSource("# First\n", "first"))
	second := capture(t, catalog, namespace, curator, source, submittedAt, skillSource("# Second\n", "second"))

	type result struct {
		publication publication
		err         error
	}
	start := make(chan struct{})
	results := make(chan result, 2)
	var wait sync.WaitGroup
	for index, entry := range testCandidates(first, second) {
		wait.Add(1)
		go func(index int, item candidate) {
			defer wait.Done()
			<-start
			publication, err := catalog.publish(context.Background(), item.ID, curator, time.Date(2026, 1, 3, 4, 5, 6+index, 0, time.UTC))
			results <- result{publication: publication, err: err}
		}(index, entry)
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

	publications := make([]publication, 0, 2)
	for result := range results {
		if result.err != nil {
			t.Fatal(result.err)
		}
		publications = append(publications, result.publication)
	}
	if len(publications) != 2 || publications[0].ID == publications[1].ID {
		t.Fatalf("concurrent publications = %#v", publications)
	}
	current, err := catalog.resolveCurrent(context.Background(), first.Skill)
	if err != nil {
		t.Fatal(err)
	}
	if current != publications[0] && current != publications[1] {
		t.Fatalf("current = %#v, want one of %#v", current, publications)
	}
	for _, publication := range publications {
		stored, err := catalog.publication(context.Background(), publication.ID)
		if err != nil || stored != publication {
			t.Fatalf("stored concurrent publication = %#v, %v", stored, err)
		}
	}
}
