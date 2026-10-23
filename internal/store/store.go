// Package store is the control plane's persistence layer: one SQLite database in WAL mode (ADR-005,
// L3-005). Every multi-row write goes through WithTx.
package store

import (
	"context"
	"database/sql"
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	_ "modernc.org/sqlite" // registers the "sqlite" driver
)

//go:embed migrations/*.sql
var migrationFS embed.FS

// ErrNotFound is returned when a row does not exist.
var ErrNotFound = errors.New("not found")

// ErrConflict is returned when a unique or foreign-key constraint blocks a write.
var ErrConflict = errors.New("conflict")

type querier interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

// Queries runs statements against the database or a transaction.
type Queries struct{ q querier }

// Store owns the database handle.
type Store struct {
	*Queries
	db *sql.DB
	// mainID is the main machine's ID, set by EnsureMainMachine before anything runs (MCH-01).
	mainID atomic.Value
}

// Open opens (creating if needed) the database at path and applies pending migrations.
func Open(ctx context.Context, path string) (*Store, error) {
	v := url.Values{}
	for _, p := range []string{"busy_timeout(5000)", "journal_mode(WAL)", "foreign_keys(1)", "synchronous(NORMAL)"} {
		v.Add("_pragma", p)
	}
	v.Set("_txlock", "immediate")
	db, err := sql.Open("sqlite", "file:"+path+"?"+v.Encode())
	if err != nil {
		return nil, fmt.Errorf("open database: %w", err)
	}
	db.SetMaxOpenConns(8)
	if err := db.PingContext(ctx); err != nil {
		db.Close()
		return nil, fmt.Errorf("open database %s: %w", path, err)
	}
	s := &Store{Queries: &Queries{q: db}, db: db}
	if err := s.migrate(ctx); err != nil {
		db.Close()
		return nil, err
	}
	return s, nil
}

// Close closes the database.
func (s *Store) Close() error { return s.db.Close() }

// Ping checks the database answers (readiness, ADR-011).
func (s *Store) Ping(ctx context.Context) error {
	var one int
	return s.db.QueryRowContext(ctx, "SELECT 1").Scan(&one)
}

// WithTx runs fn in one immediate transaction, committing on nil and rolling back otherwise.
func (s *Store) WithTx(ctx context.Context, fn func(q *Queries) error) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin: %w", err)
	}
	if err := fn(&Queries{q: tx}); err != nil {
		_ = tx.Rollback()
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit: %w", err)
	}
	return nil
}

func (s *Store) migrate(ctx context.Context) error {
	if _, err := s.db.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS schema_migrations (version INTEGER PRIMARY KEY, applied_at TEXT NOT NULL)`); err != nil {
		return fmt.Errorf("create schema_migrations: %w", err)
	}
	entries, err := fs.ReadDir(migrationFS, "migrations")
	if err != nil {
		return err
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })
	for _, e := range entries {
		version, err := strconv.Atoi(strings.SplitN(e.Name(), "_", 2)[0])
		if err != nil {
			return fmt.Errorf("migration %s: name must start with a number", e.Name())
		}
		var applied int
		if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM schema_migrations WHERE version = ?`, version).Scan(&applied); err != nil {
			return err
		}
		if applied > 0 {
			continue
		}
		body, err := migrationFS.ReadFile("migrations/" + e.Name())
		if err != nil {
			return err
		}
		if strings.HasPrefix(string(body), rebuildMarker) {
			err = s.applyRebuild(ctx, e.Name(), version, string(body))
		} else {
			err = s.WithTx(ctx, func(q *Queries) error {
				if _, err := q.q.ExecContext(ctx, string(body)); err != nil {
					return fmt.Errorf("apply migration %s: %w", e.Name(), err)
				}
				_, err := q.q.ExecContext(ctx, `INSERT INTO schema_migrations (version, applied_at) VALUES (?, ?)`, version, ts(time.Now()))
				return err
			})
		}
		if err != nil {
			return err
		}
	}
	return nil
}

// rebuildMarker starts a migration that rebuilds a table (SQLite can't alter CHECK constraints).
const rebuildMarker = "-- enclave:rebuild"

// applyRebuild runs a table-rebuild migration the way SQLite documents it: on one connection, with
// foreign keys off (so dropping the old table doesn't cascade), in one transaction, and only commits
// when PRAGMA foreign_key_check finds nothing.
func (s *Store) applyRebuild(ctx context.Context, name string, version int, body string) (err error) {
	conn, err := s.db.Conn(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()
	if _, err := conn.ExecContext(ctx, `PRAGMA foreign_keys = OFF`); err != nil {
		return fmt.Errorf("migration %s: %w", name, err)
	}
	defer func() {
		if _, ferr := conn.ExecContext(context.WithoutCancel(ctx), `PRAGMA foreign_keys = ON`); ferr != nil && err == nil {
			err = fmt.Errorf("migration %s: re-enable foreign keys: %w", name, ferr)
		}
	}()
	tx, err := conn.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("migration %s: begin: %w", name, err)
	}
	defer func() {
		if err != nil {
			_ = tx.Rollback()
		}
	}()
	if _, err := tx.ExecContext(ctx, body); err != nil {
		return fmt.Errorf("apply migration %s: %w", name, err)
	}
	rows, err := tx.QueryContext(ctx, `PRAGMA foreign_key_check`)
	if err != nil {
		return fmt.Errorf("migration %s: foreign key check: %w", name, err)
	}
	broken := rows.Next()
	rows.Close()
	if broken {
		return fmt.Errorf("migration %s: foreign key check failed", name)
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO schema_migrations (version, applied_at) VALUES (?, ?)`, version, ts(time.Now())); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("migration %s: commit: %w", name, err)
	}
	return nil
}

// Time helpers: all times are stored as RFC 3339 UTC text.
// tsLayout is RFC 3339 in UTC with exactly nine fractional digits. Fixed width matters: SQLite compares
// these as text (ORDER BY, next_run_at <= now, cursors), and RFC3339Nano drops trailing zeros, which made
// "…40.1Z" sort after "…40.100182Z". Migration 0005 rewrote older values to this width.
const tsLayout = "2006-01-02T15:04:05.000000000Z"

func ts(t time.Time) string { return t.UTC().Format(tsLayout) }

func tsPtr(t *time.Time) any {
	if t == nil {
		return nil
	}
	return ts(*t)
}

func parseTS(s string) time.Time {
	t, _ := time.Parse(time.RFC3339Nano, s)
	return t
}

func parseNullTS(s sql.NullString) *time.Time {
	if !s.Valid {
		return nil
	}
	t := parseTS(s.String)
	return &t
}

// isUnique reports a UNIQUE violation: a name or email that already exists — the caller's conflict.
func isUnique(err error) bool {
	return err != nil && strings.Contains(err.Error(), "UNIQUE constraint failed")
}

// isForeignKey reports a FOREIGN KEY violation: a row still referenced elsewhere. Other constraint
// failures (CHECK, NOT NULL) are bugs in the control plane and surface as errors, never as conflicts.
func isForeignKey(err error) bool {
	return err != nil && strings.Contains(err.Error(), "FOREIGN KEY constraint failed")
}

// placeholders returns n comma-separated "?" for an IN list.
func placeholders(n int) string { return strings.TrimSuffix(strings.Repeat("?, ", n), ", ") }
