package backup

import (
	"database/sql"
	"os"
	"path/filepath"
	"testing"
	"time"

	_ "modernc.org/sqlite"
)

// openTestDB creates a small real SQLite database (WAL mode, matching
// production's DSN — see store.Open) with one row, so Create is
// exercised against the same on-disk shape it'll actually back up.
func openTestDB(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "polaris.db")
	db, err := sql.Open("sqlite", path+"?_journal_mode=WAL&_busy_timeout=5000")
	if err != nil {
		t.Fatalf("opening test db: %v", err)
	}
	defer db.Close()
	if _, err := db.Exec(`CREATE TABLE threads (id TEXT PRIMARY KEY, title TEXT)`); err != nil {
		t.Fatalf("creating table: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO threads (id, title) VALUES ('t1', 'hello')`); err != nil {
		t.Fatalf("inserting row: %v", err)
	}
	return path
}

func TestCreate_ProducesAnOpenableBackupWithNoSidecarFiles(t *testing.T) {
	dbPath := openTestDB(t)
	dir := Dir(dbPath)

	info, err := Create(dbPath, dir)
	if err != nil {
		t.Fatalf("Create returned error: %v", err)
	}
	if info.SizeBytes == 0 {
		t.Error("SizeBytes = 0, want a real file size")
	}

	// VACUUM INTO must produce a single consistent file — a leftover
	// -wal/-shm sidecar would mean Restore's cleanup logic is backing up
	// an assumption that doesn't hold.
	for _, suffix := range []string{"-wal", "-shm"} {
		if _, err := os.Stat(info.Path + suffix); err == nil {
			t.Errorf("unexpected sidecar file %s", info.Path+suffix)
		}
	}

	bdb, err := sql.Open("sqlite", info.Path)
	if err != nil {
		t.Fatalf("opening backup: %v", err)
	}
	defer bdb.Close()
	var title string
	if err := bdb.QueryRow(`SELECT title FROM threads WHERE id = 't1'`).Scan(&title); err != nil {
		t.Fatalf("querying backup: %v", err)
	}
	if title != "hello" {
		t.Errorf("title = %q, want %q", title, "hello")
	}
}

func TestCreate_RefusesWhenDatabaseDoesNotExistYet(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "polaris.db")
	backupDir := Dir(dbPath)

	if _, err := Create(dbPath, backupDir); err == nil {
		t.Fatal("Create returned nil error for a nonexistent database, want a refusal")
	}

	// The whole point: this must not have fabricated an empty database
	// (modernc.org/sqlite's sql.Open+Exec silently creates one at the
	// target path otherwise — confirmed live) or a backups directory
	// with nothing meaningful in it.
	if _, err := os.Stat(dbPath); !os.IsNotExist(err) {
		t.Errorf("Create side-effected a database into existence at %s, want it left absent", dbPath)
	}
	if _, err := os.Stat(backupDir); !os.IsNotExist(err) {
		t.Errorf("Create side-effected a backups directory into existence at %s, want it left absent", backupDir)
	}
}

func TestList_NewestFirstAndSkipsUnrecognizedFiles(t *testing.T) {
	dir := t.TempDir()
	older := fileName(time.Now().Add(-2 * time.Hour))
	newer := fileName(time.Now().Add(-1 * time.Hour))
	for _, name := range []string{older, newer, "readme.txt", "polaris-not-a-timestamp.db"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("x"), 0o644); err != nil {
			t.Fatalf("writing fixture %s: %v", name, err)
		}
	}

	infos, err := List(dir)
	if err != nil {
		t.Fatalf("List returned error: %v", err)
	}
	if len(infos) != 2 {
		t.Fatalf("got %d infos, want 2 (unrecognized files should be skipped): %+v", len(infos), infos)
	}
	if infos[0].Name != newer || infos[1].Name != older {
		t.Errorf("order = [%s, %s], want newest first [%s, %s]", infos[0].Name, infos[1].Name, newer, older)
	}
}

func TestList_MissingDirReturnsEmptyNotError(t *testing.T) {
	infos, err := List(filepath.Join(t.TempDir(), "does-not-exist"))
	if err != nil {
		t.Fatalf("List returned error for a missing dir: %v", err)
	}
	if len(infos) != 0 {
		t.Errorf("got %d infos, want 0", len(infos))
	}
}

func TestPrune_RemovesOnlyExpiredBackups(t *testing.T) {
	dir := t.TempDir()
	old := fileName(time.Now().AddDate(0, 0, -40))
	recent := fileName(time.Now().AddDate(0, 0, -5))
	for _, name := range []string{old, recent} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("x"), 0o644); err != nil {
			t.Fatalf("writing fixture %s: %v", name, err)
		}
	}

	deleted, err := Prune(dir, 30*24*time.Hour)
	if err != nil {
		t.Fatalf("Prune returned error: %v", err)
	}
	if len(deleted) != 1 || deleted[0] != old {
		t.Errorf("deleted = %v, want [%s]", deleted, old)
	}
	if _, err := os.Stat(filepath.Join(dir, recent)); err != nil {
		t.Errorf("recent backup was removed, want it kept: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, old)); !os.IsNotExist(err) {
		t.Errorf("old backup still exists, want it removed")
	}
}

func TestRunScheduler_SkipsCreatingASecondBackupWithinMinInterval(t *testing.T) {
	dbPath := openTestDB(t)
	dir := Dir(dbPath)

	done := make(chan struct{})
	// runOnce() fires synchronously once before RunScheduler starts its
	// ticker loop, so a single call is enough to observe "already have a
	// recent backup, do nothing" without waiting an hour for the ticker.
	go RunScheduler(done, dbPath, dir, 30*24*time.Hour)
	// Give the synchronous first runOnce() a moment to finish before we
	// stop it and inspect the directory.
	time.Sleep(200 * time.Millisecond)
	close(done)

	infos, err := List(dir)
	if err != nil {
		t.Fatalf("List returned error: %v", err)
	}
	if len(infos) != 1 {
		t.Fatalf("got %d backups after one scheduler run, want exactly 1", len(infos))
	}
}

func TestRestore_ReplacesDatabaseAndPreservesAPreRestoreCopy(t *testing.T) {
	dbPath := openTestDB(t)
	dir := Dir(dbPath)

	backupInfo, err := Create(dbPath, dir)
	if err != nil {
		t.Fatalf("Create returned error: %v", err)
	}

	// Mutate the live database after taking the backup, so restoring it
	// is a real, observable change.
	db, err := sql.Open("sqlite", dbPath+"?_journal_mode=WAL")
	if err != nil {
		t.Fatalf("reopening live db: %v", err)
	}
	if _, err := db.Exec(`UPDATE threads SET title = 'changed' WHERE id = 't1'`); err != nil {
		t.Fatalf("mutating live db: %v", err)
	}
	db.Close()

	safetyCopy, err := Restore(dbPath, backupInfo.Path)
	if err != nil {
		t.Fatalf("Restore returned error: %v", err)
	}
	if safetyCopy == "" {
		t.Fatal("safetyCopy path is empty, want a preserved pre-restore copy")
	}
	if _, err := os.Stat(safetyCopy); err != nil {
		t.Errorf("safety copy does not exist on disk: %v", err)
	}

	// The restored live db should have the original ("hello") value, not
	// the post-backup mutation.
	restored, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("opening restored db: %v", err)
	}
	defer restored.Close()
	var title string
	if err := restored.QueryRow(`SELECT title FROM threads WHERE id = 't1'`).Scan(&title); err != nil {
		t.Fatalf("querying restored db: %v", err)
	}
	if title != "hello" {
		t.Errorf("restored title = %q, want %q", title, "hello")
	}

	// The safety copy should have the pre-restore ("changed") value —
	// proof restoring never silently threw away what was live before.
	preRestore, err := sql.Open("sqlite", safetyCopy)
	if err != nil {
		t.Fatalf("opening safety copy: %v", err)
	}
	defer preRestore.Close()
	var preTitle string
	if err := preRestore.QueryRow(`SELECT title FROM threads WHERE id = 't1'`).Scan(&preTitle); err != nil {
		t.Fatalf("querying safety copy: %v", err)
	}
	if preTitle != "changed" {
		t.Errorf("safety copy title = %q, want %q", preTitle, "changed")
	}

	for _, suffix := range []string{"-wal", "-shm"} {
		if _, err := os.Stat(dbPath + suffix); err == nil {
			t.Errorf("stale sidecar file %s still present after restore", dbPath+suffix)
		}
	}
}

func TestRestore_RejectsACorruptBackup(t *testing.T) {
	dbPath := openTestDB(t)
	dir := Dir(dbPath)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("creating backup dir: %v", err)
	}
	badBackup := filepath.Join(dir, fileName(time.Now()))
	if err := os.WriteFile(badBackup, []byte("this is not a sqlite database"), 0o644); err != nil {
		t.Fatalf("writing corrupt fixture: %v", err)
	}

	if _, err := Restore(dbPath, badBackup); err == nil {
		t.Fatal("Restore returned nil error for a corrupt backup file, want it to refuse")
	}

	// The live db must be untouched — a rejected restore should never
	// have gotten far enough to overwrite anything.
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("opening live db: %v", err)
	}
	defer db.Close()
	var title string
	if err := db.QueryRow(`SELECT title FROM threads WHERE id = 't1'`).Scan(&title); err != nil {
		t.Fatalf("live db appears damaged after a rejected restore: %v", err)
	}
	if title != "hello" {
		t.Errorf("title = %q, want %q (live db should be untouched)", title, "hello")
	}
}

func TestRestore_NoExistingDatabaseYieldsNoSafetyCopy(t *testing.T) {
	dbPath := openTestDB(t)
	dir := Dir(dbPath)
	backupInfo, err := Create(dbPath, dir)
	if err != nil {
		t.Fatalf("Create returned error: %v", err)
	}

	freshDBPath := filepath.Join(t.TempDir(), "polaris.db")
	safetyCopy, err := Restore(freshDBPath, backupInfo.Path)
	if err != nil {
		t.Fatalf("Restore returned error: %v", err)
	}
	if safetyCopy != "" {
		t.Errorf("safetyCopy = %q, want empty (nothing existed at the target path to preserve)", safetyCopy)
	}
	if _, err := os.Stat(freshDBPath); err != nil {
		t.Errorf("restored db does not exist at target path: %v", err)
	}
}

func TestFileNameAndParseTime_RoundTrip(t *testing.T) {
	// Truncate to the second — nameLayout has second precision, so a
	// sub-second component wouldn't survive the round trip.
	now := time.Now().UTC().Truncate(time.Second)
	name := fileName(now)
	got, ok := parseTime(name)
	if !ok {
		t.Fatalf("parseTime(%q) failed to parse a name this package itself generated", name)
	}
	if !got.Equal(now) {
		t.Errorf("parseTime round trip = %v, want %v", got, now)
	}
}

func TestParseTime_RejectsUnrecognizedNames(t *testing.T) {
	for _, name := range []string{"readme.txt", "polaris.db", "polaris-garbage.db", "other-20250101-000000.db"} {
		if _, ok := parseTime(name); ok {
			t.Errorf("parseTime(%q) = ok, want it rejected", name)
		}
	}
}
