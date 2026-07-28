package storage

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"testing/fstest"
	"time"

	"github.com/joshuadavidthomas/ts-skill-registry/internal/agentskill"
	"github.com/joshuadavidthomas/ts-skill-registry/internal/registry"
)

type catalogFixture struct {
	directory  agentskill.Directory
	digest     agentskill.TreeDigest
	namespace  registry.Namespace
	actor      registry.Actor
	provenance registry.Provenance
}

func newFixture(t *testing.T, instructions, asset string) catalogFixture {
	t.Helper()
	files := fstest.MapFS{
		"sample/SKILL.md":        {Data: []byte("---\nname: sample\ndescription: Stored skill\n---\n" + instructions), Mode: 0o644},
		"sample/assets/data.txt": {Data: []byte(asset), Mode: 0o644},
		"sample/scripts/run.sh":  {Data: []byte("echo inert\n"), Mode: 0o755},
	}
	directory, err := agentskill.Load(files, "sample")
	if err != nil {
		t.Fatal(err)
	}
	digest, err := agentskill.SumTree(directory.FS(), ".")
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
	source, err := registry.NewUploadSource(registry.UploadDirectory, "sample")
	if err != nil {
		t.Fatal(err)
	}
	provenance, err := registry.NewProvenance(source, actor, time.Date(2026, 2, 3, 4, 5, 6, 7, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	return catalogFixture{directory: directory, digest: digest, namespace: namespace, actor: actor, provenance: provenance}
}

func (f catalogFixture) candidate(t *testing.T) registry.Candidate {
	t.Helper()
	id, err := registry.NewCandidateID()
	if err != nil {
		t.Fatal(err)
	}
	skill, err := registry.NewSkillID(f.namespace, f.directory.Document().Name)
	if err != nil {
		t.Fatal(err)
	}
	candidate, err := registry.NewCandidate(id, skill, f.digest, f.provenance)
	if err != nil {
		t.Fatal(err)
	}
	return candidate
}

func openCatalog(t *testing.T, state string) *Catalog {
	t.Helper()
	catalog, err := OpenCatalog(context.Background(), state)
	if err != nil {
		t.Fatal(err)
	}
	return catalog
}

func closeCatalog(t *testing.T, catalog *Catalog) {
	t.Helper()
	if err := catalog.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestCatalogPersistsFactsTreesAndTransitionsAcrossRestart(t *testing.T) {
	ctx := context.Background()
	state := t.TempDir()
	firstFixture := newFixture(t, "# First\n", "first asset")
	secondFixture := newFixture(t, "# Second\n", "second asset")
	catalog := openCatalog(t, state)

	first := firstFixture.candidate(t)
	if err := catalog.RecordCandidate(ctx, first, firstFixture.directory); err != nil {
		t.Fatal(err)
	}
	stored, err := catalog.Candidate(ctx, first.ID())
	if err != nil {
		t.Fatal(err)
	}
	if stored.ID() != first.ID() || stored.Skill() != first.Skill() || stored.Tree() != first.Tree() {
		t.Fatalf("stored candidate = %#v, want %#v", stored, first)
	}
	if stored.Provenance().Source().Kind() != registry.UploadDirectory || stored.Provenance().Source().Label() != "sample" ||
		stored.Provenance().SubmittedBy() != firstFixture.actor || !stored.Provenance().SubmittedAt().Equal(first.Provenance().SubmittedAt()) {
		t.Fatalf("stored provenance = %#v, want %#v", stored.Provenance(), first.Provenance())
	}

	candidateTree, err := catalog.OpenCandidateTree(ctx, first.ID())
	if err != nil {
		t.Fatal(err)
	}
	asset, err := fs.ReadFile(candidateTree, "assets/data.txt")
	if err != nil {
		t.Fatal(err)
	}
	if string(asset) != "first asset" {
		t.Fatalf("candidate asset = %q", asset)
	}
	if err := candidateTree.Close(); err != nil {
		t.Fatal(err)
	}

	publishedAt := time.Date(2026, 2, 4, 5, 6, 7, 8, time.FixedZone("offset", 3600))
	firstPublished, err := catalog.PublishCandidate(ctx, first.ID(), firstFixture.actor, publishedAt)
	if err != nil {
		t.Fatal(err)
	}
	if !firstPublished.Created() || !firstPublished.BecameCurrent() {
		t.Fatalf("first publication flags = created %t, current %t", firstPublished.Created(), firstPublished.BecameCurrent())
	}
	repeated, err := catalog.PublishCandidate(ctx, first.ID(), firstFixture.actor, publishedAt.Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if repeated.Created() || repeated.BecameCurrent() || repeated.Publication() != firstPublished.Publication() {
		t.Fatalf("repeated publication = %#v", repeated)
	}

	equivalent := firstFixture.candidate(t)
	if err := catalog.RecordCandidate(ctx, equivalent, firstFixture.directory); err != nil {
		t.Fatal(err)
	}
	equivalentPublished, err := catalog.PublishCandidate(ctx, equivalent.ID(), firstFixture.actor, publishedAt.Add(2*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if equivalentPublished.Created() || equivalentPublished.Publication().Candidate() != first.ID() {
		t.Fatalf("equivalent publication = %#v", equivalentPublished)
	}

	second := secondFixture.candidate(t)
	if err := catalog.RecordCandidate(ctx, second, secondFixture.directory); err != nil {
		t.Fatal(err)
	}
	secondPublished, err := catalog.PublishCandidate(ctx, second.ID(), firstFixture.actor, publishedAt.Add(3*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if !secondPublished.Created() || secondPublished.BecameCurrent() {
		t.Fatalf("second publication flags = created %t, current %t", secondPublished.Created(), secondPublished.BecameCurrent())
	}
	selectedAt := publishedAt.Add(4 * time.Hour)
	selected, err := catalog.SelectCurrent(ctx, secondPublished.Publication().ID(), firstFixture.actor, selectedAt)
	if err != nil {
		t.Fatal(err)
	}
	if selected.Publication() != secondPublished.Publication().ID() || !selected.SelectedAt().Equal(selectedAt) {
		t.Fatalf("selected publication = %#v", selected)
	}

	closeCatalog(t, catalog)
	catalog = openCatalog(t, state)
	defer closeCatalog(t, catalog)

	stored, err = catalog.Candidate(ctx, first.ID())
	if err != nil || stored != first {
		t.Fatalf("candidate after restart = %#v, %v", stored, err)
	}
	exact, err := catalog.Publication(ctx, firstPublished.Publication().ID())
	if err != nil {
		t.Fatal(err)
	}
	if exact != firstPublished.Publication() {
		t.Fatalf("publication after restart = %#v, want %#v", exact, firstPublished.Publication())
	}
	current, err := catalog.ResolveCurrent(ctx, first.Skill())
	if err != nil {
		t.Fatal(err)
	}
	if current.ID() != secondPublished.Publication().ID() {
		t.Fatalf("current after restart = %#v", current.ID())
	}
	summaries, err := catalog.ListPublishedSkills(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(summaries) != 1 || summaries[0].Skill() != first.Skill() || summaries[0].Current() != secondPublished.Publication().ID() {
		t.Fatalf("summaries after restart = %#v", summaries)
	}
	publicationTree, err := catalog.OpenPublicationTree(ctx, secondPublished.Publication().ID())
	if err != nil {
		t.Fatal(err)
	}
	asset, err = fs.ReadFile(publicationTree, "assets/data.txt")
	if err != nil {
		t.Fatal(err)
	}
	if err := publicationTree.Close(); err != nil {
		t.Fatal(err)
	}
	if string(asset) != "second asset" {
		t.Fatalf("publication asset after restart = %q", asset)
	}
}

func TestCatalogLifetimeLockAndTreeOwnership(t *testing.T) {
	state := t.TempDir()
	catalog := openCatalog(t, state)
	fixture := newFixture(t, "# Tree\n", "asset")
	candidate := fixture.candidate(t)
	if err := catalog.RecordCandidate(context.Background(), candidate, fixture.directory); err != nil {
		t.Fatal(err)
	}

	if _, err := OpenCatalog(context.Background(), state); !errors.Is(err, registry.ErrConflict) {
		t.Fatalf("second OpenCatalog error = %v, want conflict", err)
	}
	tree, err := catalog.OpenCandidateTree(context.Background(), candidate.ID())
	if err != nil {
		t.Fatal(err)
	}
	if err := catalog.Close(); !errors.Is(err, ErrTreesOpen) {
		t.Fatalf("Close with open tree = %v, want ErrTreesOpen", err)
	}
	if _, err := catalog.Candidate(context.Background(), candidate.ID()); err != nil {
		t.Fatalf("catalog closed after ErrTreesOpen: %v", err)
	}
	if err := tree.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := tree.Open("SKILL.md"); !errors.Is(err, fs.ErrClosed) {
		t.Fatalf("open closed tree error = %v", err)
	}
	if err := tree.Close(); !errors.Is(err, fs.ErrClosed) {
		t.Fatalf("second tree close error = %v", err)
	}
	closeCatalog(t, catalog)

	reopened := openCatalog(t, state)
	closeCatalog(t, reopened)
}

func TestCatalogSchemaAndConnectionPragmas(t *testing.T) {
	catalog := openCatalog(t, t.TempDir())
	defer closeCatalog(t, catalog)
	ctx := context.Background()

	var version int
	if err := catalog.db.QueryRowContext(ctx, `PRAGMA user_version`).Scan(&version); err != nil {
		t.Fatal(err)
	}
	if version != schemaVersion {
		t.Fatalf("schema version = %d, want %d", version, schemaVersion)
	}
	for _, table := range []string{"candidates", "publications", "current_publications"} {
		var tableSQL string
		if err := catalog.db.QueryRowContext(ctx, `SELECT sql FROM sqlite_master WHERE type = 'table' AND name = ?`, table).Scan(&tableSQL); err != nil {
			t.Fatalf("read %s schema: %v", table, err)
		}
		if tableSQL == "" {
			t.Fatalf("empty schema for %s", table)
		}
	}

	connections := make([]*sql.Conn, 4)
	for i := range connections {
		connection, err := catalog.db.Conn(ctx)
		if err != nil {
			t.Fatal(err)
		}
		connections[i] = connection
	}
	defer func() {
		for _, connection := range connections {
			_ = connection.Close()
		}
	}()
	for i, connection := range connections {
		checks := map[string]string{
			`PRAGMA foreign_keys`: "1",
			`PRAGMA journal_mode`: "wal",
			`PRAGMA synchronous`:  "2",
			`PRAGMA busy_timeout`: "5000",
		}
		for query, want := range checks {
			var got string
			if err := connection.QueryRowContext(ctx, query).Scan(&got); err != nil {
				t.Fatalf("connection %d %s: %v", i, query, err)
			}
			if got != want {
				t.Fatalf("connection %d %s = %q, want %q", i, query, got, want)
			}
		}
	}
}

func TestOpenCatalogRejectsUnknownSchema(t *testing.T) {
	state := t.TempDir()
	databasePath := filepath.Join(state, "registry.sqlite")
	db, err := sql.Open("sqlite", databasePath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`PRAGMA user_version = 2`); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := OpenCatalog(context.Background(), state); err == nil {
		t.Fatal("OpenCatalog accepted schema version 2")
	}
	catalog := openCatalogAfterResetVersion(t, state, databasePath)
	closeCatalog(t, catalog)
}

func openCatalogAfterResetVersion(t *testing.T, state, databasePath string) *Catalog {
	t.Helper()
	db, err := sql.Open("sqlite", databasePath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`PRAGMA user_version = 0`); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	return openCatalog(t, state)
}

func TestRecordCandidateRejectsDigestMismatchAndConflict(t *testing.T) {
	catalog := openCatalog(t, t.TempDir())
	defer closeCatalog(t, catalog)
	fixture := newFixture(t, "# Original\n", "asset")
	candidate := fixture.candidate(t)
	wrongFixture := newFixture(t, "# Changed\n", "asset")
	if err := catalog.RecordCandidate(context.Background(), candidate, wrongFixture.directory); !errors.Is(err, registry.ErrTreeMismatch) {
		t.Fatalf("mismatched tree error = %v", err)
	}
	if _, err := catalog.Candidate(context.Background(), candidate.ID()); !errors.Is(err, registry.ErrNotFound) {
		t.Fatalf("mismatched candidate lookup = %v", err)
	}
	if err := catalog.RecordCandidate(context.Background(), candidate, fixture.directory); err != nil {
		t.Fatal(err)
	}
	if err := catalog.RecordCandidate(context.Background(), candidate, fixture.directory); !errors.Is(err, registry.ErrConflict) {
		t.Fatalf("duplicate candidate error = %v", err)
	}
}

func TestOpenTreeDetectsCorruptDigestDirectory(t *testing.T) {
	state := t.TempDir()
	catalog := openCatalog(t, state)
	fixture := newFixture(t, "# Stored\n", "asset")
	candidate := fixture.candidate(t)
	if err := catalog.RecordCandidate(context.Background(), candidate, fixture.directory); err != nil {
		t.Fatal(err)
	}
	_, final := catalog.treePaths(candidate.Tree())
	if err := os.WriteFile(filepath.Join(final, "assets", "data.txt"), []byte("corrupt"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := catalog.OpenCandidateTree(context.Background(), candidate.ID()); !errors.Is(err, registry.ErrTreeMismatch) {
		t.Fatalf("open corrupt tree error = %v", err)
	}
	closeCatalog(t, catalog)

	catalog = openCatalog(t, state)
	defer closeCatalog(t, catalog)
	if _, err := catalog.OpenCandidateTree(context.Background(), candidate.ID()); !errors.Is(err, registry.ErrTreeMismatch) {
		t.Fatalf("open corrupt tree after restart error = %v", err)
	}
}

func TestMaterializationFailuresNeverCreateCandidateMetadata(t *testing.T) {
	fixture := newFixture(t, "# Failure\n", "asset")
	probe := openCatalog(t, t.TempDir())
	var steps []string
	probe.afterFilesystemStep = func(step string) error {
		steps = append(steps, step)
		return nil
	}
	if err := probe.RecordCandidate(context.Background(), fixture.candidate(t), fixture.directory); err != nil {
		t.Fatal(err)
	}
	closeCatalog(t, probe)
	if len(steps) < 8 {
		t.Fatalf("materialization exposed only %d failure steps: %v", len(steps), steps)
	}

	injected := errors.New("injected filesystem failure")
	for failAt, stepName := range steps {
		t.Run(fmt.Sprintf("%02d-%s", failAt, stepName), func(t *testing.T) {
			state := t.TempDir()
			catalog := openCatalog(t, state)
			candidate := fixture.candidate(t)
			calls := 0
			catalog.afterFilesystemStep = func(string) error {
				if calls == failAt {
					return injected
				}
				calls++
				return nil
			}
			err := catalog.RecordCandidate(context.Background(), candidate, fixture.directory)
			if !errors.Is(err, injected) {
				t.Fatalf("RecordCandidate error = %v, want injected failure", err)
			}
			catalog.afterFilesystemStep = nil
			if _, err := catalog.Candidate(context.Background(), candidate.ID()); !errors.Is(err, registry.ErrNotFound) {
				t.Fatalf("candidate visible after failed materialization: %v", err)
			}
			closeCatalog(t, catalog)

			catalog = openCatalog(t, state)
			if _, err := catalog.Candidate(context.Background(), candidate.ID()); !errors.Is(err, registry.ErrNotFound) {
				t.Fatalf("candidate visible after failed materialization and restart: %v", err)
			}
			closeCatalog(t, catalog)
		})
	}
}

func TestConcurrentEquivalentMaterializationSharesOneImmutableTree(t *testing.T) {
	state := t.TempDir()
	catalog := openCatalog(t, state)
	defer closeCatalog(t, catalog)
	fixture := newFixture(t, "# Concurrent\n", "asset")
	const count = 12
	candidates := make([]registry.Candidate, count)
	for i := range candidates {
		candidates[i] = fixture.candidate(t)
	}
	start := make(chan struct{})
	errorsByIndex := make([]error, count)
	var wait sync.WaitGroup
	for i := range candidates {
		wait.Add(1)
		go func(index int) {
			defer wait.Done()
			<-start
			errorsByIndex[index] = catalog.RecordCandidate(context.Background(), candidates[index], fixture.directory)
		}(i)
	}
	close(start)
	wait.Wait()
	for i, err := range errorsByIndex {
		if err != nil {
			t.Fatalf("candidate %d: %v", i, err)
		}
	}
	_, final := catalog.treePaths(fixture.digest)
	if err := verifyTree(final, fixture.digest); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(filepath.Dir(final))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Fatalf("digest shard contains %d entries, want 1", len(entries))
	}
}

func TestCatalogNotFoundAndCanceledOperations(t *testing.T) {
	catalog := openCatalog(t, t.TempDir())
	defer closeCatalog(t, catalog)
	fixture := newFixture(t, "# Missing\n", "asset")
	candidate := fixture.candidate(t)
	publicationID, err := registry.NewPublicationID(candidate.Skill(), candidate.Tree())
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if _, err := catalog.Candidate(ctx, candidate.ID()); !errors.Is(err, registry.ErrNotFound) {
		t.Fatalf("Candidate error = %v", err)
	}
	if _, err := catalog.OpenCandidateTree(ctx, candidate.ID()); !errors.Is(err, registry.ErrNotFound) {
		t.Fatalf("OpenCandidateTree error = %v", err)
	}
	if _, err := catalog.PublishCandidate(ctx, candidate.ID(), fixture.actor, time.Now()); !errors.Is(err, registry.ErrNotFound) {
		t.Fatalf("PublishCandidate error = %v", err)
	}
	if _, err := catalog.Publication(ctx, publicationID); !errors.Is(err, registry.ErrNotFound) {
		t.Fatalf("Publication error = %v", err)
	}
	if _, err := catalog.OpenPublicationTree(ctx, publicationID); !errors.Is(err, registry.ErrNotFound) {
		t.Fatalf("OpenPublicationTree error = %v", err)
	}
	if _, err := catalog.ResolveCurrent(ctx, candidate.Skill()); !errors.Is(err, registry.ErrNotFound) {
		t.Fatalf("ResolveCurrent error = %v", err)
	}
	if _, err := catalog.SelectCurrent(ctx, publicationID, fixture.actor, time.Now()); !errors.Is(err, registry.ErrNotFound) {
		t.Fatalf("SelectCurrent error = %v", err)
	}
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	if err := catalog.RecordCandidate(canceled, candidate, fixture.directory); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled RecordCandidate error = %v", err)
	}
}
