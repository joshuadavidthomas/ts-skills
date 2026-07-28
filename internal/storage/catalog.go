package storage

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"path/filepath"
	"sync"
	"time"

	"github.com/gofrs/flock"
	"github.com/joshuadavidthomas/ts-skill-registry/internal/agentskill"
	"github.com/joshuadavidthomas/ts-skill-registry/internal/registry"
)

var ErrTreesOpen = errors.New("registry trees remain open")

// Catalog stores registry facts in SQLite and tree contents in digest-addressed
// directories. Its lifetime lock makes the state directory a single-writer,
// single-process resource.
var _ registry.CatalogRecords = (*Catalog)(nil)

type Catalog struct {
	db          *sql.DB
	lock        *flock.Flock
	stateDir    string
	treesDir    string
	tmpDir      string
	stateMu     sync.RWMutex
	closed      bool
	refsMu      sync.Mutex
	openTrees   int
	digestMu    sync.Mutex
	digestLocks map[agentskill.TreeDigest]*digestMutex

	// afterFilesystemStep is a package-private failure seam used to prove that
	// metadata is never committed ahead of its tree. Production leaves it nil.
	afterFilesystemStep func(string) error
}

type digestMutex struct {
	mu   sync.Mutex
	refs int
}

func OpenCatalog(ctx context.Context, stateDir string) (_ *Catalog, err error) {
	if ctx == nil {
		return nil, fmt.Errorf("open registry storage: context must be provided")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	absolute, err := filepath.Abs(stateDir)
	if err != nil {
		return nil, fmt.Errorf("resolve registry state directory: %w", err)
	}
	if err := ensureStateDirectory(absolute); err != nil {
		return nil, fmt.Errorf("prepare registry state directory: %w", err)
	}

	stateLock := flock.New(filepath.Join(absolute, "registry.lock"), flock.SetPermissions(0o600))
	locked, err := stateLock.TryLock()
	if err != nil {
		return nil, fmt.Errorf("lock registry state directory: %w", err)
	}
	if !locked {
		_ = stateLock.Close()
		return nil, fmt.Errorf("lock registry state directory: %w", registry.ErrConflict)
	}
	defer func() {
		if err != nil {
			err = errors.Join(err, stateLock.Close())
		}
	}()

	treesDir := filepath.Join(absolute, "trees", "sha256")
	tmpDir := filepath.Join(absolute, "tmp")
	if err := ensureStorageDirectories(absolute, treesDir, tmpDir); err != nil {
		return nil, err
	}

	db, err := openDatabase(ctx, filepath.Join(absolute, "registry.sqlite"))
	if err != nil {
		return nil, err
	}
	defer func() {
		if err != nil {
			err = errors.Join(err, db.Close())
		}
	}()

	catalog := &Catalog{
		db:          db,
		lock:        stateLock,
		stateDir:    absolute,
		treesDir:    treesDir,
		tmpDir:      tmpDir,
		digestLocks: make(map[agentskill.TreeDigest]*digestMutex),
	}
	return catalog, nil
}

func (c *Catalog) Close() error {
	if c == nil {
		return nil
	}
	c.stateMu.Lock()
	defer c.stateMu.Unlock()
	if c.closed {
		return nil
	}
	c.refsMu.Lock()
	openTrees := c.openTrees
	c.refsMu.Unlock()
	if openTrees != 0 {
		return fmt.Errorf("%w: %d", ErrTreesOpen, openTrees)
	}

	databaseErr := c.db.Close()
	lockErr := c.lock.Close()
	c.closed = true
	return errors.Join(databaseErr, lockErr)
}

func (c *Catalog) withOpenState() (func(), error) {
	if c == nil {
		return nil, fmt.Errorf("registry storage is nil")
	}
	c.stateMu.RLock()
	if c.closed {
		c.stateMu.RUnlock()
		return nil, fmt.Errorf("registry storage is closed")
	}
	return c.stateMu.RUnlock, nil
}

func (c *Catalog) lockDigest(digest agentskill.TreeDigest) func() {
	c.digestMu.Lock()
	entry := c.digestLocks[digest]
	if entry == nil {
		entry = &digestMutex{}
		c.digestLocks[digest] = entry
	}
	entry.refs++
	c.digestMu.Unlock()

	entry.mu.Lock()
	return func() {
		entry.mu.Unlock()
		c.digestMu.Lock()
		entry.refs--
		if entry.refs == 0 {
			delete(c.digestLocks, digest)
		}
		c.digestMu.Unlock()
	}
}

func (c *Catalog) RecordCandidate(ctx context.Context, candidate registry.Candidate, directory agentskill.Directory) error {
	done, err := c.withOpenState()
	if err != nil {
		return err
	}
	defer done()
	if err := ctx.Err(); err != nil {
		return err
	}

	unlock := c.lockDigest(candidate.Tree())
	defer unlock()
	if err := c.materializeTree(ctx, candidate.Tree(), directory.FS()); err != nil {
		return fmt.Errorf("materialize candidate tree: %w", err)
	}

	provenance := candidate.Provenance()
	result, err := c.db.ExecContext(ctx, `
		INSERT INTO candidates(
			id, namespace, name, tree_digest, source_kind, source_label,
			submitted_actor_id, submitted_actor_display, submitted_at_ns
		) VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		candidateIDBlob(candidate.ID()),
		candidate.Skill().Namespace().String(),
		candidate.Skill().Name().String(),
		digestBlob(candidate.Tree()),
		int64(provenance.Source().Kind()),
		provenance.Source().Label(),
		provenance.SubmittedBy().ID(),
		provenance.SubmittedBy().Display(),
		provenance.SubmittedAt().UnixNano(),
	)
	if err != nil {
		if isConstraintError(err) {
			return fmt.Errorf("insert candidate: %w", registry.ErrConflict)
		}
		return fmt.Errorf("insert candidate: %w", err)
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("read inserted candidate row count: %w", err)
	}
	if rows != 1 {
		return fmt.Errorf("insert candidate affected %d rows, want 1", rows)
	}
	return nil
}

func (c *Catalog) Candidate(ctx context.Context, id registry.CandidateID) (registry.Candidate, error) {
	done, err := c.withOpenState()
	if err != nil {
		return registry.Candidate{}, err
	}
	defer done()
	return queryCandidate(ctx, c.db, id)
}

func (c *Catalog) PublishCandidate(ctx context.Context, id registry.CandidateID, actor registry.Actor, at time.Time) (_ registry.PublishResult, err error) {
	done, err := c.withOpenState()
	if err != nil {
		return registry.PublishResult{}, err
	}
	defer done()
	tx, err := c.db.BeginTx(ctx, nil)
	if err != nil {
		return registry.PublishResult{}, fmt.Errorf("begin publish transaction: %w", err)
	}
	defer func() {
		if err != nil {
			err = errors.Join(err, tx.Rollback())
		}
	}()

	candidate, err := queryCandidate(ctx, tx, id)
	if err != nil {
		return registry.PublishResult{}, err
	}
	publicationID, err := registry.NewPublicationID(candidate.Skill(), candidate.Tree())
	if err != nil {
		return registry.PublishResult{}, err
	}
	publication, err := registry.NewPublication(publicationID, id, actor, at)
	if err != nil {
		return registry.PublishResult{}, err
	}

	result, err := tx.ExecContext(ctx, `
		INSERT INTO publications(
			namespace, name, tree_digest, candidate_id,
			published_actor_id, published_actor_display, published_at_ns
		) VALUES(?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(namespace, name, tree_digest) DO NOTHING`,
		candidate.Skill().Namespace().String(), candidate.Skill().Name().String(), digestBlob(candidate.Tree()), candidateIDBlob(id),
		actor.ID(), actor.Display(), at.UnixNano(),
	)
	if err != nil {
		return registry.PublishResult{}, fmt.Errorf("insert publication: %w", err)
	}
	inserted, err := exactlyZeroOrOne(result)
	if err != nil {
		return registry.PublishResult{}, fmt.Errorf("insert publication: %w", err)
	}
	if !inserted {
		publication, err = queryPublication(ctx, tx, publicationID)
		if err != nil {
			return registry.PublishResult{}, err
		}
		if err := tx.Commit(); err != nil {
			return registry.PublishResult{}, fmt.Errorf("commit repeated publication: %w", err)
		}
		return registry.NewPublishResult(publication, false, false)
	}

	currentResult, err := tx.ExecContext(ctx, `
		INSERT INTO current_publications(
			namespace, name, tree_digest,
			selected_actor_id, selected_actor_display, selected_at_ns
		) VALUES(?, ?, ?, ?, ?, ?)
		ON CONFLICT(namespace, name) DO NOTHING`,
		candidate.Skill().Namespace().String(), candidate.Skill().Name().String(), digestBlob(candidate.Tree()),
		actor.ID(), actor.Display(), at.UnixNano(),
	)
	if err != nil {
		return registry.PublishResult{}, fmt.Errorf("insert first current publication: %w", err)
	}
	becameCurrent, err := exactlyZeroOrOne(currentResult)
	if err != nil {
		return registry.PublishResult{}, fmt.Errorf("insert first current publication: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return registry.PublishResult{}, fmt.Errorf("commit publication: %w", err)
	}
	return registry.NewPublishResult(publication, true, becameCurrent)
}

func (c *Catalog) SelectCurrent(ctx context.Context, id registry.PublicationID, actor registry.Actor, at time.Time) (_ registry.CurrentPublication, err error) {
	done, err := c.withOpenState()
	if err != nil {
		return registry.CurrentPublication{}, err
	}
	defer done()
	selected, err := registry.NewCurrentPublication(id, actor, at)
	if err != nil {
		return registry.CurrentPublication{}, err
	}

	tx, err := c.db.BeginTx(ctx, nil)
	if err != nil {
		return registry.CurrentPublication{}, fmt.Errorf("begin current selection transaction: %w", err)
	}
	defer func() {
		if err != nil {
			err = errors.Join(err, tx.Rollback())
		}
	}()
	if _, err := queryPublication(ctx, tx, id); err != nil {
		return registry.CurrentPublication{}, err
	}
	_, err = tx.ExecContext(ctx, `
		INSERT INTO current_publications(
			namespace, name, tree_digest,
			selected_actor_id, selected_actor_display, selected_at_ns
		) VALUES(?, ?, ?, ?, ?, ?)
		ON CONFLICT(namespace, name) DO UPDATE SET
			tree_digest = excluded.tree_digest,
			selected_actor_id = excluded.selected_actor_id,
			selected_actor_display = excluded.selected_actor_display,
			selected_at_ns = excluded.selected_at_ns`,
		id.Skill().Namespace().String(), id.Skill().Name().String(), digestBlob(id.Tree()),
		actor.ID(), actor.Display(), at.UnixNano(),
	)
	if err != nil {
		return registry.CurrentPublication{}, fmt.Errorf("select current publication: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return registry.CurrentPublication{}, fmt.Errorf("commit current selection: %w", err)
	}
	return selected, nil
}

func exactlyZeroOrOne(result sql.Result) (bool, error) {
	rows, err := result.RowsAffected()
	if err != nil {
		return false, err
	}
	if rows < 0 || rows > 1 {
		return false, fmt.Errorf("expected at most one affected row, got %d", rows)
	}
	return rows == 1, nil
}
