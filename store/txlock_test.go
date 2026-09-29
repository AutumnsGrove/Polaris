package store

import (
	"path/filepath"
	"testing"
	"time"
)

// TestWriteTransactionSurvivesAnotherConnectionCommittingMidway pins the
// reason Open sets _txlock=immediate. With SQLite's default deferred BEGIN, a
// transaction that has read something holds a WAL snapshot; if another
// connection commits before this one writes, the write fails with
// SQLITE_BUSY immediately (the busy handler is never invoked — waiting can't
// refresh a stale snapshot), so _busy_timeout is no protection. That is what
// used to make TestCrossProcessCloseRace flake, and in production it is the
// window where the previous process is still finishing a turn while the new
// one is already serving requests.
//
// Deterministic on purpose: the stress test that found this hit it ~2% of the
// time, far too rarely to guard a regression. Here connection A reads inside
// a transaction, connection B tries to commit while A is open, and A then
// writes. Under a deferred BEGIN, B commits during the pause and A's write
// fails; under BEGIN IMMEDIATE A already holds the write lock, so B simply
// waits its turn and both writes land.
func TestWriteTransactionSurvivesAnotherConnectionCommittingMidway(t *testing.T) {
	path := filepath.Join(t.TempDir(), "polaris.db")
	a, err := Open(path)
	if err != nil {
		t.Fatalf("open a: %v", err)
	}
	defer a.Close()
	b, err := Open(path)
	if err != nil {
		t.Fatalf("open b: %v", err)
	}
	defer b.Close()
	if err := a.CreateThread("t1", "Thread", "m", "web"); err != nil {
		t.Fatalf("CreateThread: %v", err)
	}

	tx, err := a.db.Begin()
	if err != nil {
		t.Fatalf("a.Begin: %v", err)
	}
	defer tx.Rollback()

	// The read that pins a snapshot under a deferred BEGIN.
	var n int
	if err := tx.QueryRow(`SELECT COUNT(*) FROM messages`).Scan(&n); err != nil {
		t.Fatalf("a read: %v", err)
	}

	bDone := make(chan error, 1)
	go func() {
		_, err := b.AddMessage("t1", "user", "from b", "[]", "[]", 0, "")
		bDone <- err
	}()
	// Long enough for b to commit if nothing stops it.
	time.Sleep(150 * time.Millisecond)

	if _, err := tx.Exec(
		`INSERT INTO messages (thread_id, role, content, citations, suggestions, cost_usd, cost_answer_usd, turn_id) VALUES ('t1', 'assistant', 'from a', '[]', '[]', 0, 0, '')`,
	); err != nil {
		t.Fatalf("a's write failed after b committed in between (stale snapshot, busy handler skipped): %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("a.Commit: %v", err)
	}
	if err := <-bDone; err != nil {
		t.Fatalf("b.AddMessage should have waited for a's lock, got: %v", err)
	}

	msgs, err := a.GetMessages("t1")
	if err != nil {
		t.Fatalf("GetMessages: %v", err)
	}
	if len(msgs) != 2 {
		t.Errorf("want both writes to land, got %d messages", len(msgs))
	}
}
