package server

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"testing/fstest"
	"time"

	"github.com/gofrs/flock"
	"github.com/joshuadavidthomas/ts-skills/internal/agentskill"
)

type catalogFixture struct {
	directory  agentskill.Directory
	digest     agentskill.TreeDigest
	namespace  agentskill.Namespace
	actor      actor
	provenance provenance
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
	digest, err := agentskill.SumTree(context.Background(), directory.FS(), ".")
	if err != nil {
		t.Fatal(err)
	}
	namespace, err := agentskill.ParseNamespace("team")
	if err != nil {
		t.Fatal(err)
	}
	actor := actor{ID: "user:1", Display: "Test User"}
	provenance := provenance{
		Source: "sample", SubmittedBy: actor,
		SubmittedAt: time.Date(2026, 2, 3, 4, 5, 6, 7, time.UTC),
	}
	return catalogFixture{directory: directory, digest: digest, namespace: namespace, actor: actor, provenance: provenance}
}

func (f catalogFixture) candidate(t *testing.T) candidate {
	t.Helper()
	id, err := agentskill.NewCandidateID()
	if err != nil {
		t.Fatal(err)
	}
	skill, err := agentskill.NewSkillID(f.namespace, f.directory.Document().Name)
	if err != nil {
		t.Fatal(err)
	}
	return candidate{ID: id, Skill: skill, Tree: f.digest, Provenance: f.provenance}
}

func openTestCatalog(t *testing.T, state string) *catalog {
	t.Helper()
	catalog, err := openCatalog(context.Background(), state)
	if err != nil {
		t.Fatal(err)
	}
	return catalog
}

func closeCatalog(t *testing.T, catalog *catalog) {
	t.Helper()
	if err := catalog.close(); err != nil {
		t.Fatal(err)
	}
}

func TestCatalogPersistsFactsAndTreesAcrossRestart(t *testing.T) {
	ctx := context.Background()
	state := t.TempDir()
	firstFixture := newFixture(t, "# First\n", "first asset")
	secondFixture := newFixture(t, "# Second\n", "second asset")
	catalog := openTestCatalog(t, state)

	first := firstFixture.candidate(t)
	if err := catalog.recordCandidate(ctx, first, firstFixture.directory); err != nil {
		t.Fatal(err)
	}
	stored, err := catalog.candidate(ctx, first.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored != first {
		t.Fatalf("stored candidate = %#v, want %#v", stored, first)
	}
	candidateTree, err := catalog.openCandidateTree(ctx, first.ID)
	if err != nil {
		t.Fatal(err)
	}
	asset, err := fs.ReadFile(candidateTree, "assets/data.txt")
	if err != nil {
		t.Fatal(err)
	}
	if err := candidateTree.Close(); err != nil {
		t.Fatal(err)
	}
	if string(asset) != "first asset" {
		t.Fatalf("candidate asset = %q", asset)
	}

	publishedAt := time.Unix(0, time.Date(2026, 2, 4, 5, 6, 7, 8, time.FixedZone("offset", 3600)).UnixNano()).UTC()
	firstID, err := agentskill.NewPublicationID(first.Skill, first.Tree)
	if err != nil {
		t.Fatal(err)
	}
	firstPublished := publication{ID: firstID, Candidate: first.ID, PublishedBy: firstFixture.actor, PublishedAt: publishedAt}
	firstCurrent := currentPublication{Publication: firstID, SelectedBy: firstFixture.actor, SelectedAt: publishedAt}
	inserted, err := catalog.persistPublication(ctx, firstPublished, &firstCurrent)
	if err != nil || !inserted {
		t.Fatalf("persist first publication = inserted %t, err %v", inserted, err)
	}
	if inserted, err := catalog.persistPublication(ctx, firstPublished, &firstCurrent); err != nil || inserted {
		t.Fatalf("persist repeated publication = inserted %t, err %v", inserted, err)
	}
	currentAfterFirst, err := catalog.currentPublication(ctx, first.Skill)
	if err != nil || currentAfterFirst != firstPublished {
		t.Fatalf("stored initial current = %#v, %v", currentAfterFirst, err)
	}

	second := secondFixture.candidate(t)
	if err := catalog.recordCandidate(ctx, second, secondFixture.directory); err != nil {
		t.Fatal(err)
	}
	secondID, err := agentskill.NewPublicationID(second.Skill, second.Tree)
	if err != nil {
		t.Fatal(err)
	}
	secondPublished := publication{ID: secondID, Candidate: second.ID, PublishedBy: firstFixture.actor, PublishedAt: publishedAt.Add(time.Hour)}
	if inserted, err := catalog.persistPublication(ctx, secondPublished, nil); err != nil || !inserted {
		t.Fatalf("persist second publication = inserted %t, err %v", inserted, err)
	}
	currentAfterSecond, err := catalog.currentPublication(ctx, first.Skill)
	if err != nil || currentAfterSecond != firstPublished {
		t.Fatalf("second publication changed current to %#v (%v)", currentAfterSecond, err)
	}
	selectedAt := publishedAt.Add(2 * time.Hour)
	selected := currentPublication{Publication: secondID, SelectedBy: firstFixture.actor, SelectedAt: selectedAt}
	if err := catalog.persistCurrent(ctx, selected); err != nil {
		t.Fatal(err)
	}
	selectedNow, err := catalog.currentPublication(ctx, first.Skill)
	if err != nil || selectedNow != secondPublished {
		t.Fatalf("stored current selection = %#v (%v)", selectedNow, err)
	}

	closeCatalog(t, catalog)
	catalog = openTestCatalog(t, state)
	defer closeCatalog(t, catalog)

	stored, err = catalog.candidate(ctx, first.ID)
	if err != nil || stored != first {
		t.Fatalf("candidate after restart = %#v, %v", stored, err)
	}
	exact, err := catalog.publication(ctx, firstPublished.ID)
	if err != nil || exact != firstPublished {
		t.Fatalf("publication after restart = %#v, %v", exact, err)
	}
	current, err := catalog.currentPublication(ctx, first.Skill)
	if err != nil || current != secondPublished {
		t.Fatalf("current after restart = %#v, %v", current, err)
	}
	summaries, err := catalog.listPublishedSkills(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(summaries) != 1 || summaries[0].Skill != first.Skill || summaries[0].Current != secondPublished.ID {
		t.Fatalf("summaries after restart = %#v", summaries)
	}
	publicationTree, err := catalog.openPublicationTree(ctx, secondPublished.ID)
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
	catalog := openTestCatalog(t, state)
	fixture := newFixture(t, "# Tree\n", "asset")
	candidate := fixture.candidate(t)
	if err := catalog.recordCandidate(context.Background(), candidate, fixture.directory); err != nil {
		t.Fatal(err)
	}

	if _, err := openCatalog(context.Background(), state); !errors.Is(err, errConflict) {
		t.Fatalf("second OpenCatalog error = %v, want conflict", err)
	}
	tree, err := catalog.openCandidateTree(context.Background(), candidate.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := catalog.close(); !errors.Is(err, errTreesOpen) {
		t.Fatalf("Close with open tree = %v, want errTreesOpen", err)
	}
	if _, err := catalog.candidate(context.Background(), candidate.ID); err != nil {
		t.Fatalf("catalog operation after errTreesOpen: %v", err)
	}
	secondTree, err := catalog.openCandidateTree(context.Background(), candidate.ID)
	if err != nil {
		t.Fatalf("open tree after errTreesOpen: %v", err)
	}
	if err := secondTree.Close(); err != nil {
		t.Fatal(err)
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

	reopened := openTestCatalog(t, state)
	closeCatalog(t, reopened)
}

func TestCatalogCloseRetriesEachOwnedResource(t *testing.T) {
	state := t.TempDir()
	catalog := openTestCatalog(t, state)
	t.Cleanup(func() {
		catalog.closeDB = (*sql.DB).Close
		catalog.closeLock = (*flock.Flock).Close
		_ = catalog.close()
	})
	fixture := newFixture(t, "# Closing\n", "asset")
	candidate := fixture.candidate(t)
	injectedDB := errors.New("injected database close failure")
	databaseCloseCalls := 0
	catalog.closeDB = func(db *sql.DB) error {
		databaseCloseCalls++
		if databaseCloseCalls == 1 {
			return injectedDB
		}
		return db.Close()
	}
	if err := catalog.close(); !errors.Is(err, injectedDB) {
		t.Fatalf("first Close error = %v, want database failure", err)
	}
	if _, err := catalog.candidate(context.Background(), candidate.ID); err == nil {
		t.Fatal("catalog accepted an operation after database close started")
	}
	if competing, err := openCatalog(context.Background(), state); !errors.Is(err, errConflict) {
		if competing != nil {
			_ = competing.close()
		}
		t.Fatalf("lifetime lock after database close failure = %v, want conflict", err)
	}

	injectedLock := errors.New("injected lifetime lock close failure")
	lockCloseCalls := 0
	catalog.closeLock = func(*flock.Flock) error {
		lockCloseCalls++
		return injectedLock
	}
	if err := catalog.close(); !errors.Is(err, injectedLock) {
		t.Fatalf("second Close error = %v, want lock failure", err)
	}
	// The retry resumed at the database close (second call, succeeding this
	// time) and then failed on the lock.
	if databaseCloseCalls != 2 {
		t.Fatalf("database close calls = %d, want 2", databaseCloseCalls)
	}
	if competing, err := openCatalog(context.Background(), state); !errors.Is(err, errConflict) {
		if competing != nil {
			_ = competing.close()
		}
		t.Fatalf("lifetime lock after lock close failure = %v, want conflict", err)
	}
	if err := catalog.close(); !errors.Is(err, injectedLock) {
		t.Fatalf("repeated Close error = %v, want lock failure", err)
	}
	if databaseCloseCalls != 2 || lockCloseCalls != 2 {
		t.Fatalf("close calls after retry = database %d, lock %d", databaseCloseCalls, lockCloseCalls)
	}

	catalog.closeLock = (*flock.Flock).Close
	if err := catalog.close(); err != nil {
		t.Fatal(err)
	}
	if _, err := catalog.candidate(context.Background(), candidate.ID); err == nil {
		t.Fatal("catalog accepted an operation after full close")
	}
	if err := catalog.close(); err != nil {
		t.Fatalf("Close after full success: %v", err)
	}
	reopened := openTestCatalog(t, state)
	closeCatalog(t, reopened)
}

func TestCatalogSchemaAndConnectionPragmas(t *testing.T) {
	catalog := openTestCatalog(t, t.TempDir())
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

func TestOpenCatalogRequiresPrivateRealStateDirectory(t *testing.T) {
	t.Run("sets private mode", func(t *testing.T) {
		state := filepath.Join(t.TempDir(), "state")
		if err := os.Mkdir(state, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(state, 0o755); err != nil {
			t.Fatal(err)
		}

		catalog := openTestCatalog(t, state)
		closeCatalog(t, catalog)

		info, err := os.Lstat(state)
		if err != nil {
			t.Fatal(err)
		}
		if !info.IsDir() || info.Mode()&fs.ModeSymlink != 0 {
			t.Fatalf("state mode = %v, want real directory", info.Mode())
		}
		if got := info.Mode().Perm(); got != 0o700 {
			t.Fatalf("state permissions = %04o, want 0700", got)
		}
	})

	t.Run("rejects symbolic link", func(t *testing.T) {
		parent := t.TempDir()
		target := filepath.Join(parent, "target")
		if err := os.Mkdir(target, 0o700); err != nil {
			t.Fatal(err)
		}
		state := filepath.Join(parent, "state")
		if err := os.Symlink(target, state); err != nil {
			t.Fatal(err)
		}

		catalog, err := openCatalog(context.Background(), state)
		if catalog != nil {
			_ = catalog.close()
		}
		if err == nil {
			t.Fatal("OpenCatalog accepted a symbolic-link state directory")
		}
		if _, err := os.Lstat(filepath.Join(target, "registry.lock")); !errors.Is(err, fs.ErrNotExist) {
			t.Fatalf("registry lock created through state symlink: %v", err)
		}
	})

	t.Run("rejects regular file", func(t *testing.T) {
		state := filepath.Join(t.TempDir(), "state")
		if err := os.WriteFile(state, []byte("not a directory"), 0o600); err != nil {
			t.Fatal(err)
		}
		if catalog, err := openCatalog(context.Background(), state); err == nil {
			_ = catalog.close()
			t.Fatal("OpenCatalog accepted a regular-file state path")
		}
	})
}

func TestOpenCatalogMigratesVersion1State(t *testing.T) {
	state := t.TempDir()
	databasePath := filepath.Join(state, "registry.sqlite")
	db, err := sql.Open("sqlite", databasePath)
	if err != nil {
		t.Fatal(err)
	}
	fixture := newFixture(t, "# Migrated\n", "asset")
	candidate := fixture.candidate(t)
	provenance := candidate.Provenance
	if _, err := db.Exec(migrations[0]); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`
		INSERT INTO candidates(
			id, namespace, name, tree_digest, source_kind, source_label,
			submitted_actor_id, submitted_actor_display, submitted_at_ns
		) VALUES(?, ?, ?, ?, 2, ?, ?, ?, ?)`,
		candidateIDBlob(candidate.ID),
		candidate.Skill.Namespace().String(),
		candidate.Skill.Name().String(),
		digestBlob(candidate.Tree),
		provenance.Source,
		provenance.SubmittedBy.ID,
		provenance.SubmittedBy.Display,
		provenance.SubmittedAt.UnixNano(),
	); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`PRAGMA user_version = 1`); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	catalog := openTestCatalog(t, state)
	defer closeCatalog(t, catalog)
	ctx := context.Background()
	var version int
	if err := catalog.db.QueryRowContext(ctx, `PRAGMA user_version`).Scan(&version); err != nil {
		t.Fatal(err)
	}
	if version != schemaVersion {
		t.Fatalf("schema version after migration = %d, want %d", version, schemaVersion)
	}
	migrated, err := catalog.candidate(ctx, candidate.ID)
	if err != nil {
		t.Fatal(err)
	}
	if migrated.Provenance.Source != provenance.Source {
		t.Fatalf("migrated source label = %q", migrated.Provenance.Source)
	}
	var tableSQL string
	if err := catalog.db.QueryRowContext(ctx, `SELECT sql FROM sqlite_master WHERE name = 'candidates'`).Scan(&tableSQL); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(tableSQL, "source_kind") {
		t.Fatal("source_kind survived migration")
	}
}

func TestOpenCatalogRejectsUnknownSchema(t *testing.T) {
	state := t.TempDir()
	databasePath := filepath.Join(state, "registry.sqlite")
	db, err := sql.Open("sqlite", databasePath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`PRAGMA user_version = 3`); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := openCatalog(context.Background(), state); err == nil {
		t.Fatal("OpenCatalog accepted schema version 3")
	}
	catalog := openCatalogAfterResetVersion(t, state, databasePath)
	closeCatalog(t, catalog)
}

func openCatalogAfterResetVersion(t *testing.T, state, databasePath string) *catalog {
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
	return openTestCatalog(t, state)
}

func TestRecordCandidateRejectsDigestMismatchAndConflict(t *testing.T) {
	catalog := openTestCatalog(t, t.TempDir())
	defer closeCatalog(t, catalog)
	fixture := newFixture(t, "# Original\n", "asset")
	candidate := fixture.candidate(t)
	wrongFixture := newFixture(t, "# Changed\n", "asset")
	if err := catalog.recordCandidate(context.Background(), candidate, wrongFixture.directory); !errors.Is(err, errTreeMismatch) {
		t.Fatalf("mismatched tree error = %v", err)
	}
	if _, err := catalog.candidate(context.Background(), candidate.ID); !errors.Is(err, errNotFound) {
		t.Fatalf("mismatched candidate lookup = %v", err)
	}
	otherName, err := agentskill.ParseName("other")
	if err != nil {
		t.Fatal(err)
	}
	otherSkill, err := agentskill.NewSkillID(fixture.namespace, otherName)
	if err != nil {
		t.Fatal(err)
	}
	otherID, err := agentskill.NewCandidateID()
	if err != nil {
		t.Fatal(err)
	}
	mismatchedName := testCandidate(otherID, otherSkill, fixture.digest, fixture.provenance)
	if err := catalog.recordCandidate(context.Background(), mismatchedName, fixture.directory); err == nil || !strings.Contains(err.Error(), "candidate names other but SKILL.md names sample") {
		t.Fatalf("mismatched candidate name error = %v", err)
	}
	if _, err := catalog.candidate(context.Background(), mismatchedName.ID); !errors.Is(err, errNotFound) {
		t.Fatalf("mismatched-name candidate lookup = %v", err)
	}
	_, final := catalog.treePaths(mismatchedName.Tree)
	if _, err := os.Stat(final); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("mismatched-name tree exists: %v", err)
	}
	if err := catalog.recordCandidate(context.Background(), candidate, fixture.directory); err != nil {
		t.Fatal(err)
	}
	if err := catalog.recordCandidate(context.Background(), candidate, fixture.directory); !errors.Is(err, errConflict) {
		t.Fatalf("duplicate candidate error = %v", err)
	}
}

func TestOpenTreeDetectsCorruptDigestDirectory(t *testing.T) {
	state := t.TempDir()
	catalog := openTestCatalog(t, state)
	fixture := newFixture(t, "# Stored\n", "asset")
	candidate := fixture.candidate(t)
	if err := catalog.recordCandidate(context.Background(), candidate, fixture.directory); err != nil {
		t.Fatal(err)
	}
	_, final := catalog.treePaths(candidate.Tree)
	if err := os.WriteFile(filepath.Join(final, "assets", "data.txt"), []byte("corrupt"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := catalog.openCandidateTree(context.Background(), candidate.ID); !errors.Is(err, errTreeMismatch) {
		t.Fatalf("open corrupt tree error = %v", err)
	}
	closeCatalog(t, catalog)

	catalog = openTestCatalog(t, state)
	defer closeCatalog(t, catalog)
	if _, err := catalog.openCandidateTree(context.Background(), candidate.ID); !errors.Is(err, errTreeMismatch) {
		t.Fatalf("open corrupt tree after restart error = %v", err)
	}
}

func TestMaterializationFailuresNeverCreateCandidateMetadata(t *testing.T) {
	fixture := newFixture(t, "# Failure\n", "asset")
	probe := openTestCatalog(t, t.TempDir())
	var steps []string
	probe.afterFilesystemStep = func(step string) error {
		steps = append(steps, step)
		return nil
	}
	if err := probe.recordCandidate(context.Background(), fixture.candidate(t), fixture.directory); err != nil {
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
			catalog := openTestCatalog(t, state)
			candidate := fixture.candidate(t)
			calls := 0
			catalog.afterFilesystemStep = func(string) error {
				if calls == failAt {
					return injected
				}
				calls++
				return nil
			}
			err := catalog.recordCandidate(context.Background(), candidate, fixture.directory)
			if !errors.Is(err, injected) {
				t.Fatalf("RecordCandidate error = %v, want injected failure", err)
			}
			catalog.afterFilesystemStep = nil
			if _, err := catalog.candidate(context.Background(), candidate.ID); !errors.Is(err, errNotFound) {
				t.Fatalf("candidate visible after failed materialization: %v", err)
			}
			closeCatalog(t, catalog)

			catalog = openTestCatalog(t, state)
			if _, err := catalog.candidate(context.Background(), candidate.ID); !errors.Is(err, errNotFound) {
				t.Fatalf("candidate visible after failed materialization and restart: %v", err)
			}
			closeCatalog(t, catalog)
		})
	}
}

func TestSameShardMaterializationWaitsForItsOwnParentBarrier(t *testing.T) {
	state := t.TempDir()
	catalog := openTestCatalog(t, state)
	defer closeCatalog(t, catalog)

	firstFixture := newFixture(t, "# First same-shard tree\n", "first")
	var secondFixture catalogFixture
	for attempt := 0; attempt < 4096; attempt++ {
		fixture := newFixture(t, fmt.Sprintf("# Same-shard tree %d\n", attempt), fmt.Sprintf("asset %d", attempt))
		if fixture.digest != firstFixture.digest && fixture.digest[0] == firstFixture.digest[0] {
			secondFixture = fixture
			break
		}
	}
	if secondFixture.digest == (agentskill.TreeDigest{}) {
		t.Fatal("could not construct two distinct digests in one shard")
	}

	first := firstFixture.candidate(t)
	second := secondFixture.candidate(t)
	shard, _ := catalog.treePaths(first.Tree)
	creatorAtShardSync := make(chan struct{})
	releaseCreator := make(chan struct{})
	injected := errors.New("injected shard-parent sync failure")
	var hookMu sync.Mutex
	shardSyncs := 0
	parentSyncs := 0
	catalog.syncDirectory = func(directory string) error {
		switch directory {
		case shard:
			hookMu.Lock()
			shardSyncs++
			call := shardSyncs
			hookMu.Unlock()
			if err := syncDirectory(directory); err != nil {
				return err
			}
			if call == 1 {
				close(creatorAtShardSync)
				<-releaseCreator
			}
			return nil
		case catalog.treesDir:
			hookMu.Lock()
			parentSyncs++
			call := parentSyncs
			hookMu.Unlock()
			if call == 1 {
				return injected
			}
		}
		return syncDirectory(directory)
	}

	firstResult := make(chan error, 1)
	go func() {
		firstResult <- catalog.recordCandidate(context.Background(), first, firstFixture.directory)
	}()
	<-creatorAtShardSync

	secondResult := make(chan error, 1)
	go func() {
		secondResult <- catalog.recordCandidate(context.Background(), second, secondFixture.directory)
	}()
	if err := <-secondResult; !errors.Is(err, injected) {
		close(releaseCreator)
		<-firstResult
		t.Fatalf("second RecordCandidate error = %v, want injected parent-sync failure", err)
	}
	if _, err := catalog.candidate(context.Background(), second.ID); !errors.Is(err, errNotFound) {
		close(releaseCreator)
		<-firstResult
		t.Fatalf("second candidate visible before its shard-parent barrier: %v", err)
	}

	close(releaseCreator)
	if err := <-firstResult; err != nil {
		t.Fatal(err)
	}
	if _, err := catalog.candidate(context.Background(), first.ID); err != nil {
		t.Fatalf("first candidate missing after its shard-parent barrier: %v", err)
	}
}

func TestConcurrentEquivalentMaterializationSharesOneImmutableTree(t *testing.T) {
	state := t.TempDir()
	catalog := openTestCatalog(t, state)
	defer closeCatalog(t, catalog)
	fixture := newFixture(t, "# Concurrent\n", "asset")
	const count = 12
	candidates := make([]candidate, count)
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
			errorsByIndex[index] = catalog.recordCandidate(context.Background(), candidates[index], fixture.directory)
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
	if err := verifyTree(context.Background(), final, fixture.digest); err != nil {
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
	catalog := openTestCatalog(t, t.TempDir())
	defer closeCatalog(t, catalog)
	fixture := newFixture(t, "# Missing\n", "asset")
	candidate := fixture.candidate(t)
	publicationID, err := agentskill.NewPublicationID(candidate.Skill, candidate.Tree)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if _, err := catalog.candidate(ctx, candidate.ID); !errors.Is(err, errNotFound) || !strings.Contains(err.Error(), candidate.ID.String()) {
		t.Fatalf("Candidate error = %v", err)
	}
	if _, err := catalog.openCandidateTree(ctx, candidate.ID); !errors.Is(err, errNotFound) {
		t.Fatalf("OpenCandidateTree error = %v", err)
	}
	if _, err := catalog.publication(ctx, publicationID); !errors.Is(err, errNotFound) ||
		!strings.Contains(err.Error(), candidate.Skill.String()) || !strings.Contains(err.Error(), candidate.Tree.String()) {
		t.Fatalf("Publication error = %v", err)
	}
	if _, err := catalog.openPublicationTree(ctx, publicationID); !errors.Is(err, errNotFound) {
		t.Fatalf("OpenPublicationTree error = %v", err)
	}
	if _, err := catalog.currentPublication(ctx, candidate.Skill); !errors.Is(err, errNotFound) || !strings.Contains(err.Error(), candidate.Skill.String()) {
		t.Fatalf("CurrentPublication error = %v", err)
	}
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	if err := catalog.recordCandidate(canceled, candidate, fixture.directory); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled RecordCandidate error = %v", err)
	}
}

func TestPersistPublicationReportsRollbackFailure(t *testing.T) {
	catalog := openTestCatalog(t, t.TempDir())
	defer closeCatalog(t, catalog)
	fixture := newFixture(t, "# Missing\n", "asset")
	candidate := fixture.candidate(t)
	id, err := agentskill.NewPublicationID(candidate.Skill, candidate.Tree)
	if err != nil {
		t.Fatal(err)
	}
	at := time.Date(2026, 2, 4, 5, 6, 7, 0, time.UTC)
	publication := publication{ID: id, Candidate: candidate.ID, PublishedBy: fixture.actor, PublishedAt: at}
	current := currentPublication{Publication: id, SelectedBy: fixture.actor, SelectedAt: at}
	injected := errors.New("injected rollback failure")
	catalog.rollbackTx = func(tx *sql.Tx) error {
		return errors.Join(tx.Rollback(), injected)
	}
	_, err = catalog.persistPublication(context.Background(), publication, &current)
	if !errors.Is(err, injected) {
		t.Fatalf("PersistPublication error = %v, want rollback failure joined", err)
	}
}

func TestCandidateIDBlobRoundTrip(t *testing.T) {
	id, err := agentskill.NewCandidateID()
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := candidateIDFromBlob(candidateIDBlob(id))
	if err != nil || decoded != id {
		t.Fatalf("BLOB round-trip = %v, %v", decoded, err)
	}
	if _, err := candidateIDFromBlob(make([]byte, 15)); err == nil {
		t.Fatal("candidateIDFromBlob accepted a short blob")
	}
	if _, err := candidateIDFromBlob(make([]byte, 16)); err == nil {
		t.Fatal("candidateIDFromBlob accepted a zero identity")
	}
}

func TestPersistPublicationRollsBackWhenInitialCurrentFails(t *testing.T) {
	catalog := openTestCatalog(t, t.TempDir())
	defer closeCatalog(t, catalog)
	fixture := newFixture(t, "# Sample\n", "asset")
	candidate := fixture.candidate(t)
	if err := catalog.recordCandidate(context.Background(), candidate, fixture.directory); err != nil {
		t.Fatal(err)
	}
	id, err := agentskill.NewPublicationID(candidate.Skill, candidate.Tree)
	if err != nil {
		t.Fatal(err)
	}
	at := time.Date(2026, 2, 4, 5, 6, 7, 0, time.UTC)
	publication := publication{ID: id, Candidate: candidate.ID, PublishedBy: fixture.actor, PublishedAt: at}
	current := currentPublication{Publication: id, SelectedBy: fixture.actor, SelectedAt: at}
	if _, err := catalog.db.Exec(`
		CREATE TRIGGER fail_initial_current
		BEFORE INSERT ON current_publications
		BEGIN
			SELECT RAISE(ABORT, 'injected initial-current failure');
		END`); err != nil {
		t.Fatal(err)
	}
	if _, err := catalog.persistPublication(context.Background(), publication, &current); err == nil {
		t.Fatal("PersistPublication succeeded despite a current-publication failure")
	}
	if _, err := catalog.publication(context.Background(), id); !errors.Is(err, errNotFound) {
		t.Fatalf("publication survived failed initial current insert: %v", err)
	}
	if _, err := catalog.currentPublication(context.Background(), candidate.Skill); !errors.Is(err, errNotFound) {
		t.Fatalf("current publication survived failed initial current insert: %v", err)
	}
}

func TestPersistPublicationIgnoresPostCommitRollbackError(t *testing.T) {
	catalog := openTestCatalog(t, t.TempDir())
	defer closeCatalog(t, catalog)
	fixture := newFixture(t, "# Sample\n", "asset")
	candidate := fixture.candidate(t)
	if err := catalog.recordCandidate(context.Background(), candidate, fixture.directory); err != nil {
		t.Fatal(err)
	}
	id, err := agentskill.NewPublicationID(candidate.Skill, candidate.Tree)
	if err != nil {
		t.Fatal(err)
	}
	at := time.Date(2026, 2, 4, 5, 6, 7, 0, time.UTC)
	publication := publication{ID: id, Candidate: candidate.ID, PublishedBy: fixture.actor, PublishedAt: at}
	selection := currentPublication{Publication: id, SelectedBy: fixture.actor, SelectedAt: at}
	// After a successful commit Rollback reports sql.ErrTxDone; that must not
	// be joined into the result. This seam returns it even on the post-commit
	// rollback to prove the deferred rollback filters it.
	catalog.rollbackTx = func(*sql.Tx) error { return sql.ErrTxDone }
	inserted, err := catalog.persistPublication(context.Background(), publication, &selection)
	if err != nil || !inserted {
		t.Fatalf("PersistPublication = inserted %t, err %v; want post-commit sql.ErrTxDone ignored", inserted, err)
	}
	current, err := catalog.currentPublication(context.Background(), candidate.Skill)
	if err != nil || current != publication {
		t.Fatalf("publication not current after ignored rollback error (%#v, %v)", current, err)
	}
}

func TestRecordTextValidationRejectsUntrustedText(t *testing.T) {
	for _, value := range []string{"", "source\x00", string([]byte{0xff}), strings.Repeat("a", 257)} {
		if err := validateRecordText("source", value); err == nil {
			t.Fatalf("validateRecordText(%q) succeeded", value)
		}
	}
}
