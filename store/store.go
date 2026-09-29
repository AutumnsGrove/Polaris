// Package store persists threads and messages to SQLite so past
// sessions can be revisited, restarted, or continued with a follow-up
// question — and so per-thread cost can be shown in the UI.
package store

import (
	"database/sql"
	"fmt"
	"strings"

	_ "modernc.org/sqlite"
)

type Store struct {
	db *sql.DB
}

func Open(path string) (*Store, error) {
	// _busy_timeout: SQLite allows only one writer at a time; without this,
	// a second concurrent writer (routine now, since every turn does
	// several writes — the message, context tokens, and multiple event-log
	// inserts — across goroutines) gets an immediate SQLITE_BUSY error
	// instead of waiting its turn. _journal_mode=WAL lets readers proceed
	// without blocking on a writer at all, which is what actually makes
	// the busy_timeout the common case rather than the exception.
	//
	// _txlock=immediate makes every db.Begin() a BEGIN IMMEDIATE, taking the
	// write lock up front. Every transaction in this package writes, and
	// with the default deferred BEGIN a transaction first takes a WAL read
	// snapshot and only then asks for the write lock. If another connection
	// (in production: the previous process, still draining during a restart
	// overlap) commits in that gap, SQLite returns SQLITE_BUSY *immediately*
	// — the busy handler is never invoked, so _busy_timeout above doesn't
	// help, because waiting can't fix a snapshot that's already stale. Found
	// via TestCrossProcessCloseRace's flake: ~2% of a transaction's first
	// write failed in under a millisecond; 0 in 400 with this set. Taking
	// the lock first turns that into an ordinary wait on the busy handler.
	db, err := sql.Open("sqlite", path+"?_foreign_keys=on&_journal_mode=WAL&_busy_timeout=5000&_txlock=immediate")
	if err != nil {
		return nil, fmt.Errorf("opening database: %w", err)
	}
	// database/sql pools multiple physical connections by default, and two
	// of them writing at once can still surface as an immediate
	// SQLITE_BUSY/"database is locked" error rather than actually waiting
	// out _busy_timeout above — that pragma governs how long SQLite's own
	// busy handler retries within one connection, not how Go's pool
	// arbitrates between several. Capping the pool to one connection
	// forces every write to queue behind Go's own mutex instead, so two
	// goroutines writing around the same moment (e.g. a turn's detached
	// follow-up-suggestions save landing while the next turn's fork
	// transaction runs — see handleTurn) simply wait their turn rather
	// than erroring. This app's write volume is low enough that
	// serializing all of it through one connection costs nothing
	// noticeable.
	db.SetMaxOpenConns(1)
	if err := renameProjectsToFields(db); err != nil {
		return nil, err
	}
	if _, err := db.Exec(schema); err != nil {
		return nil, fmt.Errorf("applying schema: %w", err)
	}
	if err := applyMigrations(db); err != nil {
		return nil, err
	}
	return &Store{db: db}, nil
}

// renameProjectsToFields carries a database created while this feature was
// still called "Projects" over to its final name, Fields. It has to run
// BEFORE the `schema` constant: that constant's CREATE TABLE IF NOT EXISTS
// fields would otherwise happily create a second, empty table beside the
// real projects one, orphaning every existing group. It can't be an entry
// in `migrations` for the same reason (those run after the schema).
//
// SQLite rewrites the threads.project_id -> projects(id) foreign key on its
// own when the referenced table is renamed (legacy_alter_table is off by
// default), so only the table and the column need naming here. Both steps
// check current state instead of trusting a version counter, which makes a
// fresh database (neither exists yet) and an already-renamed one no-ops.
func renameProjectsToFields(db *sql.DB) error {
	var n int
	if err := db.QueryRow(
		`SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = 'projects'`,
	).Scan(&n); err != nil {
		return fmt.Errorf("checking for legacy projects table: %w", err)
	}
	if n > 0 {
		if _, err := db.Exec(`ALTER TABLE projects RENAME TO fields`); err != nil {
			return fmt.Errorf("renaming projects table to fields: %w", err)
		}
	}
	if err := db.QueryRow(
		`SELECT COUNT(*) FROM pragma_table_info('threads') WHERE name = 'project_id'`,
	).Scan(&n); err != nil {
		return fmt.Errorf("checking for legacy threads.project_id column: %w", err)
	}
	if n > 0 {
		if _, err := db.Exec(`ALTER TABLE threads RENAME COLUMN project_id TO field_id`); err != nil {
			return fmt.Errorf("renaming threads.project_id to field_id: %w", err)
		}
	}
	// The save_to_project tool became save_to_field. An operator who turned
	// it off has its old name in the disabled_tools JSON list, which would
	// otherwise silently re-enable the renamed tool. Idempotent, so it needs
	// no "did the rename just happen" gating; only a brand-new database
	// (no settings table yet) skips it.
	if err := db.QueryRow(
		`SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = 'settings'`,
	).Scan(&n); err != nil {
		return fmt.Errorf("checking for settings table: %w", err)
	}
	if n > 0 {
		if _, err := db.Exec(
			`UPDATE settings SET value = replace(value, '"save_to_project"', '"save_to_field"') WHERE key = 'disabled_tools'`,
		); err != nil {
			return fmt.Errorf("renaming save_to_project in disabled_tools: %w", err)
		}
	}
	return nil
}

// applyMigrations runs whichever entries in `migrations` haven't been
// applied yet, tracked via SQLite's built-in PRAGMA user_version — no
// extra table needed, it's exactly the "how far have we gotten" counter
// this needs. Previously "already applied" was detected only by
// string-matching each ALTER TABLE's error against "duplicate column",
// which broke silently if that exact wording ever changed across a
// go-sqlite3/SQLite version bump, and couldn't detect completion for any
// migration shape other than ADD COLUMN.
//
// A fresh user_version of 0 covers two cases identically, and both are
// handled correctly by just running the full list: a brand-new database
// (the `schema` constant above already creates every column any migration
// would add, so each one below is a harmless no-op there) and a
// pre-versioning database from before this counter existed (every
// migration actually needs to run there). The "duplicate column" check is
// kept as a tolerance for exactly that transitional case — going forward,
// user_version itself is what prevents a migration from ever being
// re-attempted, not error-message sniffing.
//
// Each migration's success (real or tolerated-duplicate) is recorded
// immediately, one at a time — so a real failure partway through the list
// leaves user_version accurately at the last one that actually landed,
// and the next Open() resumes from exactly there instead of re-running
// (and re-risking) everything from the start.
func applyMigrations(db *sql.DB) error {
	var version int
	if err := db.QueryRow(`PRAGMA user_version`).Scan(&version); err != nil {
		return fmt.Errorf("reading schema version: %w", err)
	}
	for i := version; i < len(migrations); i++ {
		if _, err := db.Exec(migrations[i]); err != nil && !strings.Contains(err.Error(), "duplicate column") {
			return fmt.Errorf("applying migration %d %q: %w", i, migrations[i], err)
		}
		if _, err := db.Exec(fmt.Sprintf(`PRAGMA user_version = %d`, i+1)); err != nil {
			return fmt.Errorf("recording schema version %d: %w", i+1, err)
		}
	}
	return nil
}

func (s *Store) Close() error { return s.db.Close() }

// Ping verifies the database connection is alive, for /healthz — a
// dropped connection or a locked/corrupt file surfaces here rather than
// only on the next real request.
func (s *Store) Ping() error { return s.db.Ping() }

// execOne runs a write that's expected to touch exactly one existing row
// (an UPDATE targeting a single id), translating "the id didn't match
// anything" into sql.ErrNoRows so callers — and, following the same
// errors.Is(err, sql.ErrNoRows) -> 404 convention handleGetThread/
// handleRegenerateTitle already use, their HTTP handlers — can tell a
// missing id apart from a real database error instead of both silently
// reporting success.
func execOne(res sql.Result, err error) error {
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return sql.ErrNoRows
	}
	return nil
}
