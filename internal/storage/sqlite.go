package storage

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"path/filepath"

	"modernc.org/sqlite"
)

const schemaVersion = 2

const schema = `
CREATE TABLE candidates(
  id BLOB PRIMARY KEY CHECK(length(id) = 16),
  namespace TEXT NOT NULL,
  name TEXT NOT NULL,
  tree_digest BLOB NOT NULL CHECK(length(tree_digest) = 32),
  source_label TEXT NOT NULL,
  submitted_actor_id TEXT NOT NULL,
  submitted_actor_display TEXT NOT NULL,
  submitted_at_ns INTEGER NOT NULL,
  UNIQUE(id, namespace, name, tree_digest)
);

CREATE TABLE publications(
  namespace TEXT NOT NULL,
  name TEXT NOT NULL,
  tree_digest BLOB NOT NULL CHECK(length(tree_digest) = 32),
  candidate_id BLOB NOT NULL,
  published_actor_id TEXT NOT NULL,
  published_actor_display TEXT NOT NULL,
  published_at_ns INTEGER NOT NULL,
  PRIMARY KEY(namespace, name, tree_digest),
  FOREIGN KEY(candidate_id, namespace, name, tree_digest)
    REFERENCES candidates(id, namespace, name, tree_digest)
);

CREATE TABLE current_publications(
  namespace TEXT NOT NULL,
  name TEXT NOT NULL,
  tree_digest BLOB NOT NULL,
  selected_actor_id TEXT NOT NULL,
  selected_actor_display TEXT NOT NULL,
  selected_at_ns INTEGER NOT NULL,
  PRIMARY KEY(namespace, name),
  FOREIGN KEY(namespace, name, tree_digest)
    REFERENCES publications(namespace, name, tree_digest)
);
`

func openDatabase(ctx context.Context, databasePath string) (_ *sql.DB, err error) {
	values := make(url.Values)
	values.Add("_pragma", "foreign_keys(1)")
	values.Add("_pragma", "journal_mode(WAL)")
	values.Add("_pragma", "synchronous(FULL)")
	values.Add("_pragma", "busy_timeout(5000)")
	values.Set("_txlock", "immediate")
	dsn := (&url.URL{
		Scheme:   "file",
		Path:     filepath.ToSlash(databasePath),
		RawQuery: values.Encode(),
	}).String()

	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("open registry database: %w", err)
	}
	defer func() {
		if err != nil {
			err = errors.Join(err, db.Close())
		}
	}()
	// The DSN applies all four pragmas whenever database/sql opens a new
	// connection. A small pool allows readers without weakening that contract.
	db.SetMaxOpenConns(4)
	db.SetMaxIdleConns(4)
	if err := db.PingContext(ctx); err != nil {
		return nil, fmt.Errorf("connect registry database: %w", err)
	}
	if err := initializeSchema(ctx, db); err != nil {
		return nil, err
	}
	return db, nil
}

func initializeSchema(ctx context.Context, db *sql.DB) (_ error) {
	var version int
	if err := db.QueryRowContext(ctx, `PRAGMA user_version`).Scan(&version); err != nil {
		return fmt.Errorf("read registry schema version: %w", err)
	}
	switch version {
	case schemaVersion:
		return verifyPragmas(ctx, db)
	case 0:
	case -1:
		fallthrough
	default:
		return fmt.Errorf("unsupported registry schema version %d", version)
	}

	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin registry schema transaction: %w", err)
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, schema); err != nil {
		return fmt.Errorf("create registry schema: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `PRAGMA user_version = 2`); err != nil {
		return fmt.Errorf("set registry schema version: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit registry schema: %w", err)
	}
	return verifyPragmas(ctx, db)
}

func verifyPragmas(ctx context.Context, db *sql.DB) error {
	checks := []struct {
		query string
		want  string
	}{
		{`PRAGMA foreign_keys`, "1"},
		{`PRAGMA journal_mode`, "wal"},
		{`PRAGMA synchronous`, "2"},
		{`PRAGMA busy_timeout`, "5000"},
	}
	for _, check := range checks {
		var got string
		if err := db.QueryRowContext(ctx, check.query).Scan(&got); err != nil {
			return fmt.Errorf("verify %s: %w", check.query, err)
		}
		if got != check.want {
			return fmt.Errorf("verify %s: got %q, want %q", check.query, got, check.want)
		}
	}
	return nil
}

func isConstraintError(err error) bool {
	var sqliteErr *sqlite.Error
	return errors.As(err, &sqliteErr) && sqliteErr.Code()&0xff == 19
}
