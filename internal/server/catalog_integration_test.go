package server

import (
	"context"
	"errors"
	"io/fs"
	"strings"
	"sync"
	"testing"
	"testing/fstest"
	"time"

	"github.com/joshuadavidthomas/ts-skills/internal/registry"
	"github.com/joshuadavidthomas/ts-skills/internal/safetree"
)

func newCatalogFixture(t *testing.T) (*catalog, registry.Namespace, curator, string, time.Time) {
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
	namespace, err := registry.ParseNamespace("team")
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

func stageTree(t *testing.T, source fs.FS) *safetree.Snapshot {
	t.Helper()
	builder, err := safetree.NewBuilder(t.TempDir(), safetree.PrototypeLimits())
	if err != nil {
		t.Fatal(err)
	}
	if err := fs.WalkDir(source, "sample", func(name string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil || entry.IsDir() {
			return walkErr
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		file, err := source.Open(name)
		if err != nil {
			return err
		}
		return errors.Join(builder.AddFile(context.Background(), name, info.Size(), file), file.Close())
	}); err != nil {
		_ = builder.Close()
		t.Fatal(err)
	}
	snapshot, err := builder.Finish()
	if err != nil {
		_ = builder.Close()
		t.Fatal(err)
	}
	return snapshot
}

func capture(t *testing.T, catalog *catalog, namespace registry.Namespace, curator curator, source string, submittedAt time.Time, tree fs.FS) candidate {
	t.Helper()
	snapshot := stageTree(t, tree)
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
	snapshot := stageTree(t, skillSource("# Instructions\n", "asset"))
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

func TestCatalogCaptureRejectsInvalidSourceBeforeMutating(t *testing.T) {
	catalog, namespace, curator, _, submittedAt := newCatalogFixture(t)
	snapshot := stageTree(t, skillSource("# Invalid\n", "invalid"))
	defer func() {
		if err := snapshot.Close(); err != nil {
			t.Error(err)
		}
	}()

	for _, source := range []string{"", "source\x00", string([]byte{0xff}), strings.Repeat("a", 257)} {
		if _, err := catalog.capture(context.Background(), curator, captureRequest{
			Namespace: namespace, Staged: snapshot, Root: "sample", Source: source, SubmittedAt: submittedAt,
		}); err == nil {
			t.Fatalf("capture accepted source %q", source)
		}
	}

	var candidates int
	if err := catalog.db.QueryRowContext(context.Background(), "SELECT COUNT(*) FROM candidates").Scan(&candidates); err != nil {
		t.Fatal(err)
	}
	if candidates != 0 {
		t.Fatalf("candidates after rejected captures = %d, want 0", candidates)
	}
}

func TestCatalogRejectsInvalidCuratorsBeforeMutating(t *testing.T) {
	catalog, namespace, validCurator, source, submittedAt := newCatalogFixture(t)
	ctx := context.Background()

	snapshot := stageTree(t, skillSource("# Invalid\n", "invalid"))
	defer func() {
		if err := snapshot.Close(); err != nil {
			t.Error(err)
		}
	}()
	if _, err := catalog.capture(ctx, curator{}, captureRequest{
		Namespace: namespace, Staged: snapshot, Root: "sample", Source: source, SubmittedAt: submittedAt,
	}); err == nil {
		t.Fatal("capture accepted a zero curator")
	}
	var candidates int
	if err := catalog.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM candidates").Scan(&candidates); err != nil {
		t.Fatal(err)
	}
	if candidates != 0 {
		t.Fatalf("candidates after rejected capture = %d, want 0", candidates)
	}

	firstCandidate := capture(t, catalog, namespace, validCurator, source, submittedAt, skillSource("# First\n", "first"))
	invalid := curator{Actor: actor{ID: "user\n1", Display: "Invalid User"}}
	if _, err := catalog.publish(ctx, firstCandidate.ID, invalid, submittedAt); err == nil {
		t.Fatal("publish accepted an invalid curator")
	}
	var publications int
	if err := catalog.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM publications").Scan(&publications); err != nil {
		t.Fatal(err)
	}
	if publications != 0 {
		t.Fatalf("publications after rejected publish = %d, want 0", publications)
	}

	first, err := catalog.publish(ctx, firstCandidate.ID, validCurator, submittedAt)
	if err != nil {
		t.Fatal(err)
	}
	secondCandidate := capture(t, catalog, namespace, validCurator, source, submittedAt, skillSource("# Second\n", "second"))
	second, err := catalog.publish(ctx, secondCandidate.ID, validCurator, submittedAt.Add(time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if err := catalog.setCurrent(ctx, second.ID, curator{}, submittedAt.Add(2*time.Second)); err == nil {
		t.Fatal("setCurrent accepted a zero curator")
	}
	current, err := catalog.currentPublication(ctx, firstCandidate.Skill)
	if err != nil {
		t.Fatal(err)
	}
	if current.ID != first.ID {
		t.Fatalf("current publication after rejected selection = %s, want %s", current.ID.Tree(), first.ID.Tree())
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
	namespace, err := registry.ParseNamespace("team")
	if err != nil {
		t.Fatal(err)
	}
	actor := actor{ID: "user:1", Display: "Test User"}
	source := "sample"
	curator := testCurator(actor)
	catalog := store
	snapshot := stageTree(t, skillSource("# Instructions\n", "asset"))
	defer func() {
		if err := snapshot.Close(); err != nil {
			t.Error(err)
		}
	}()
	inspection, err := registry.Inspect(context.Background(), snapshot.FS(), "sample")
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
	missingCandidate, err := newCandidateID()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := catalog.publish(ctx, missingCandidate, curator, time.Now()); !errors.Is(err, errNotFound) {
		t.Fatalf("publish unknown candidate error = %v", err)
	}
	original := skillSource("# First\n", "first")
	firstCandidate := capture(t, catalog, namespace, curator, source, submittedAt, original)

	original["sample/assets/data.txt"].Data = []byte("mutated after capture")
	candidateTree, err := catalog.openTree(ctx, firstCandidate.Tree)
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
	current, err := catalog.currentPublication(ctx, firstCandidate.Skill)
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
	current, err = catalog.currentPublication(ctx, firstCandidate.Skill)
	if err != nil {
		t.Fatal(err)
	}
	if current.ID != firstPublish.ID {
		t.Fatalf("second publish moved current to %s", current.ID.Tree())
	}
	if err := catalog.setCurrent(ctx, secondPublish.ID, testCurator(laterActor), publishedAt.Add(4*time.Hour)); err != nil {
		t.Fatal(err)
	}
	current, err = catalog.currentPublication(ctx, firstCandidate.Skill)
	if err != nil {
		t.Fatal(err)
	}
	if current.ID != secondPublish.ID {
		t.Fatal("explicit selection did not move current")
	}
	summaries, err := catalog.listPublishedSkills(ctx)
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
	publicationTree, err := catalog.openTree(ctx, exact.ID.Tree())
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

	unknownDigest, err := registry.ParseTreeDigest("sha256:ffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff")
	if err != nil {
		t.Fatal(err)
	}
	unknownPublication, err := registry.NewPublicationID(firstCandidate.Skill, unknownDigest)
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
	current, err := catalog.currentPublication(context.Background(), first.Skill)
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
