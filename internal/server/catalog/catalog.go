package catalog

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"

	"github.com/gofrs/flock"
	"github.com/joshuadavidthomas/ts-skills/internal/agentskill"
	"github.com/joshuadavidthomas/ts-skills/internal/registry"
)

var errTreesOpen = errors.New("registry trees remain open")

// catalog stores registry facts in SQLite and tree contents in digest-addressed
// directories. Its lifetime lock makes the state directory a single-writer,
// single-process resource.

// closePhase states which owned resource Close releases next, so a failed
// Close can be retried and resumes exactly where it stopped. The legal
// progression is linear: catalogOpen accepts operations; catalogDatabaseOpen
// has passed the open-tree check and rejects operations but has not yet
// closed the database; catalogLockHeld has closed the database and still
// holds the lifetime lock; catalogClosed has released everything.
type closePhase uint8

const (
	catalogOpen closePhase = iota
	catalogDatabaseOpen
	catalogLockHeld
	catalogClosed
)

type catalog struct {
	db        *sql.DB
	lock      *flock.Flock
	stateDir  string
	treesDir  string
	tmpDir    string
	stateMu   sync.RWMutex
	phase     closePhase
	closeDB   func(*sql.DB) error
	closeLock func(*flock.Flock) error
	// rollbackTx is a package-private failure seam for proving transaction
	// rollback failures are reported. Production uses (*sql.Tx).Rollback.
	rollbackTx  func(*sql.Tx) error
	refsMu      sync.Mutex
	openTrees   int
	verifiedMu  sync.Mutex
	verified    map[registry.TreeDigest]struct{}
	digestMu    sync.Mutex
	digestLocks map[registry.TreeDigest]*digestMutex

	// Package-private failure and synchronization seams used by tests.
	afterFilesystemStep       func(string) error
	syncDirectory             func(string) error
	beforeRecordCandidate     func()
	afterMissingCurrentLookup func()
}

type digestMutex struct {
	mu   sync.Mutex
	refs int
}

func openCatalog(ctx context.Context, stateDir string) (_ *catalog, err error) {
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
		return nil, errors.Join(
			fmt.Errorf("lock registry state directory: %w", err),
			stateLock.Close(),
		)
	}
	if !locked {
		return nil, errors.Join(
			fmt.Errorf("lock registry state directory: %w", errConflict),
			stateLock.Close(),
		)
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
	if err := sweepTemporaryDirectory(ctx, tmpDir); err != nil {
		return nil, fmt.Errorf("sweep registry temporary files: %w", err)
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

	catalog := &catalog{
		db:            db,
		lock:          stateLock,
		stateDir:      absolute,
		treesDir:      treesDir,
		tmpDir:        tmpDir,
		digestLocks:   make(map[registry.TreeDigest]*digestMutex),
		verified:      make(map[registry.TreeDigest]struct{}),
		syncDirectory: syncDirectory,
		closeDB:       (*sql.DB).Close,
		closeLock:     (*flock.Flock).Close,
		rollbackTx:    (*sql.Tx).Rollback,
	}
	return catalog, nil
}

func sweepTemporaryDirectory(ctx context.Context, temporaryDirectory string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	entries, err := os.ReadDir(temporaryDirectory)
	if err != nil {
		return fmt.Errorf("read registry temporary directory: %w", err)
	}
	var sweepErr error
	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			return errors.Join(sweepErr, err)
		}
		path := filepath.Join(temporaryDirectory, entry.Name())
		if err := os.RemoveAll(path); err != nil {
			sweepErr = errors.Join(sweepErr, fmt.Errorf("remove registry temporary entry %q: %w", path, err))
		}
	}
	return sweepErr
}

func (c *catalog) close() error {
	if c == nil {
		return nil
	}
	c.stateMu.Lock()
	defer c.stateMu.Unlock()
	switch c.phase {
	case catalogClosed:
		return nil
	case catalogOpen:
		c.refsMu.Lock()
		openTrees := c.openTrees
		c.refsMu.Unlock()
		if openTrees != 0 {
			return fmt.Errorf("%w: %d", errTreesOpen, openTrees)
		}
		c.phase = catalogDatabaseOpen
		fallthrough
	case catalogDatabaseOpen:
		closeDB := c.closeDB
		if closeDB == nil {
			closeDB = (*sql.DB).Close
		}
		if err := closeDB(c.db); err != nil {
			return fmt.Errorf("close registry database: %w", err)
		}
		c.phase = catalogLockHeld
		fallthrough
	case catalogLockHeld:
		closeLock := c.closeLock
		if closeLock == nil {
			closeLock = (*flock.Flock).Close
		}
		if err := closeLock(c.lock); err != nil {
			return fmt.Errorf("close registry lifetime lock: %w", err)
		}
		c.phase = catalogClosed
	}
	return nil
}

func (c *catalog) withOpenState() (func(), error) {
	if c == nil {
		return nil, fmt.Errorf("registry storage is nil")
	}
	c.stateMu.RLock()
	if c.phase != catalogOpen {
		c.stateMu.RUnlock()
		return nil, fmt.Errorf("registry storage is closed")
	}
	return c.stateMu.RUnlock, nil
}

func (c *catalog) lockDigest(digest registry.TreeDigest) func() {
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

func (c *catalog) recordCandidate(ctx context.Context, candidate candidate, directory agentskill.Directory) error {
	done, err := c.withOpenState()
	if err != nil {
		return err
	}
	defer done()
	if err := ctx.Err(); err != nil {
		return err
	}
	if c.beforeRecordCandidate != nil {
		c.beforeRecordCandidate()
	}

	unlock := c.lockDigest(candidate.Tree)
	defer unlock()
	expected, err := registry.NewPublicationID(candidate.Skill, candidate.Tree)
	if err != nil {
		return err
	}
	if err := c.materializeTree(ctx, expected, directory.FS()); err != nil {
		return fmt.Errorf("materialize candidate tree: %w", err)
	}

	provenance := candidate.Provenance
	result, err := c.db.ExecContext(ctx, `
		INSERT INTO candidates(
			id, namespace, name, tree_digest, source_label,
			submitted_actor_id, submitted_actor_display, submitted_at_ns
		) VALUES(?, ?, ?, ?, ?, ?, ?, ?)`,
		candidateIDBlob(candidate.ID),
		candidate.Skill.Namespace().String(),
		candidate.Skill.Name().String(),
		digestBlob(candidate.Tree),
		provenance.Source,
		provenance.SubmittedBy.ID,
		provenance.SubmittedBy.Display,
		provenance.SubmittedAt.UnixNano(),
	)
	if err != nil {
		if isConstraintError(err) {
			return fmt.Errorf("insert candidate: %w", errConflict)
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

func (c *catalog) candidate(ctx context.Context, id candidateID) (candidate, error) {
	done, err := c.withOpenState()
	if err != nil {
		return candidate{}, err
	}
	defer done()
	return queryCandidate(ctx, c.db.QueryRowContext, id)
}

func (c *catalog) persistPublication(ctx context.Context, publication publication, initialCurrent *currentPublication) (_ bool, err error) {
	if initialCurrent != nil && initialCurrent.Publication != publication.ID {
		return false, fmt.Errorf("initial current publication must match publication")
	}
	done, err := c.withOpenState()
	if err != nil {
		return false, err
	}
	defer done()
	tx, err := c.db.BeginTx(ctx, nil)
	if err != nil {
		return false, fmt.Errorf("begin publication transaction: %w", err)
	}
	defer func() {
		err = c.rollbackTransaction(tx, err)
	}()

	id := publication.ID
	result, err := tx.ExecContext(ctx, `
		INSERT INTO publications(
			namespace, name, tree_digest, candidate_id,
			published_actor_id, published_actor_display, published_at_ns
		) VALUES(?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(namespace, name, tree_digest) DO NOTHING`,
		id.Skill().Namespace().String(), id.Skill().Name().String(), digestBlob(id.Tree()), candidateIDBlob(publication.Candidate),
		publication.PublishedBy.ID, publication.PublishedBy.Display, publication.PublishedAt.UnixNano(),
	)
	if err != nil {
		return false, fmt.Errorf("insert publication: %w", err)
	}
	inserted, err := exactlyZeroOrOne(result)
	if err != nil {
		return false, fmt.Errorf("insert publication: %w", err)
	}
	if !inserted {
		if err := tx.Commit(); err != nil {
			return false, fmt.Errorf("commit repeated publication: %w", err)
		}
		return false, nil
	}

	if initialCurrent != nil {
		currentID := initialCurrent.Publication
		currentResult, err := tx.ExecContext(ctx, `
			INSERT INTO current_publications(
				namespace, name, tree_digest,
				selected_actor_id, selected_actor_display, selected_at_ns
			) VALUES(?, ?, ?, ?, ?, ?)
			ON CONFLICT(namespace, name) DO NOTHING`,
			currentID.Skill().Namespace().String(), currentID.Skill().Name().String(), digestBlob(currentID.Tree()),
			initialCurrent.SelectedBy.ID, initialCurrent.SelectedBy.Display, initialCurrent.SelectedAt.UnixNano(),
		)
		if err != nil {
			return false, fmt.Errorf("insert initial current publication: %w", err)
		}
		if _, err := exactlyZeroOrOne(currentResult); err != nil {
			return false, fmt.Errorf("insert initial current publication: %w", err)
		}
	}
	if err := tx.Commit(); err != nil {
		return false, fmt.Errorf("commit publication: %w", err)
	}
	return true, nil
}

// rollbackTransaction always rolls the transaction back so a panic cannot leak
// it, joins a genuine rollback failure into err, and ignores the benign
// sql.ErrTxDone returned after a successful commit.
func (c *catalog) rollbackTransaction(tx *sql.Tx, err error) error {
	rollback := c.rollbackTx
	if rollback == nil {
		rollback = (*sql.Tx).Rollback
	}
	if rbErr := rollback(tx); rbErr != nil && !errors.Is(rbErr, sql.ErrTxDone) {
		return errors.Join(err, rbErr)
	}
	return err
}

func (c *catalog) persistCurrent(ctx context.Context, selection currentPublication) error {
	done, err := c.withOpenState()
	if err != nil {
		return err
	}
	defer done()
	id := selection.Publication
	_, err = c.db.ExecContext(ctx, `
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
		selection.SelectedBy.ID, selection.SelectedBy.Display, selection.SelectedAt.UnixNano(),
	)
	if err != nil {
		return fmt.Errorf("persist current publication: %w", err)
	}
	return nil
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
