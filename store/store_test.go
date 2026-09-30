package store

import (
	"path/filepath"
	"testing"
)

// openTestStore opens a fresh SQLite file per test (t.TempDir() is
// unique and cleaned up automatically) rather than ":memory:" — this
// exercises the exact same DSN/pragma path (_journal_mode=WAL,
// _busy_timeout) that production uses.
func openTestStore(t *testing.T) *Store {
	t.Helper()
	s, err := Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("Open returned error: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

// TestOpen_MigrationsAreIdempotentAndVersioned covers the PRAGMA
// user_version tracking in applyMigrations: a brand-new database should
// end up at the full migration count after one Open, and reopening the
// same file must not error or double-apply anything (each ALTER TABLE
// would fail loudly the second time if user_version weren't preventing
// the re-run).
func TestOpen_MigrationsAreIdempotentAndVersioned(t *testing.T) {
	path := filepath.Join(t.TempDir(), "test.db")

	s1, err := Open(path)
	if err != nil {
		t.Fatalf("first Open returned error: %v", err)
	}
	var version int
	if err := s1.db.QueryRow(`PRAGMA user_version`).Scan(&version); err != nil {
		t.Fatalf("reading user_version: %v", err)
	}
	if version != len(migrations) {
		t.Errorf("user_version = %d after first Open, want %d (len(migrations))", version, len(migrations))
	}
	if err := s1.Close(); err != nil {
		t.Fatalf("closing store: %v", err)
	}

	s2, err := Open(path)
	if err != nil {
		t.Fatalf("second Open on the same file returned error: %v", err)
	}
	defer s2.Close()
	if err := s2.db.QueryRow(`PRAGMA user_version`).Scan(&version); err != nil {
		t.Fatalf("reading user_version after reopen: %v", err)
	}
	if version != len(migrations) {
		t.Errorf("user_version = %d after reopen, want it to stay at %d", version, len(migrations))
	}
}

// TestApplyMigrations_LegacyDatabaseWithZeroVersion simulates a database
// created before PRAGMA user_version tracking existed: the base schema
// already has every migrated-in column (CREATE TABLE IF NOT EXISTS never
// ran here with an old shape, so this isn't a perfect stand-in for a real
// legacy file, but it does exercise the same "user_version starts at 0,
// every migration hits the duplicate-column tolerance path" behavior a
// real one would).
func TestApplyMigrations_LegacyDatabaseWithZeroVersion(t *testing.T) {
	s := openTestStore(t)

	// user_version is already len(migrations) from Open() — reset it to 0
	// to simulate a pre-versioning database, then re-run applyMigrations
	// directly and confirm it still lands on the right version without error.
	if _, err := s.db.Exec(`PRAGMA user_version = 0`); err != nil {
		t.Fatalf("resetting user_version: %v", err)
	}
	if err := applyMigrations(s.db); err != nil {
		t.Fatalf("applyMigrations on a zero-version database returned error: %v", err)
	}
	var version int
	if err := s.db.QueryRow(`PRAGMA user_version`).Scan(&version); err != nil {
		t.Fatalf("reading user_version: %v", err)
	}
	if version != len(migrations) {
		t.Errorf("user_version = %d, want %d after re-applying from a reset version", version, len(migrations))
	}
}
