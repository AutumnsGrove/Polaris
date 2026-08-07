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

func TestCreateAndGetThread(t *testing.T) {
	s := openTestStore(t)

	if err := s.CreateThread("t1", "My Thread", "test-model", "web"); err != nil {
		t.Fatalf("CreateThread returned error: %v", err)
	}

	thread, err := s.GetThread("t1")
	if err != nil {
		t.Fatalf("GetThread returned error: %v", err)
	}
	if thread.Title != "My Thread" || thread.Model != "test-model" {
		t.Errorf("thread = %+v, want title=My Thread model=test-model", thread)
	}
	if thread.CostUSD != 0 {
		t.Errorf("CostUSD = %v, want 0 for a brand new thread", thread.CostUSD)
	}
}

func TestSetThreadTitle(t *testing.T) {
	s := openTestStore(t)
	if err := s.CreateThread("t1", "placeholder title", "test-model", "web"); err != nil {
		t.Fatalf("CreateThread: %v", err)
	}

	if err := s.SetThreadTitle("t1", "Capital of France"); err != nil {
		t.Fatalf("SetThreadTitle: %v", err)
	}

	thread, err := s.GetThread("t1")
	if err != nil {
		t.Fatalf("GetThread: %v", err)
	}
	if thread.Title != "Capital of France" {
		t.Errorf("Title = %q, want %q", thread.Title, "Capital of France")
	}

	// A second rename must simply overwrite — no "locked" state, whether
	// the first title came from the LLM or a previous manual rename.
	if err := s.SetThreadTitle("t1", "Renamed Again"); err != nil {
		t.Fatalf("SetThreadTitle (second): %v", err)
	}
	thread, err = s.GetThread("t1")
	if err != nil {
		t.Fatalf("GetThread: %v", err)
	}
	if thread.Title != "Renamed Again" {
		t.Errorf("Title = %q, want %q", thread.Title, "Renamed Again")
	}
}

func TestSetThreadFavorite(t *testing.T) {
	s := openTestStore(t)
	if err := s.CreateThread("t1", "placeholder title", "test-model", "web"); err != nil {
		t.Fatalf("CreateThread: %v", err)
	}

	thread, err := s.GetThread("t1")
	if err != nil {
		t.Fatalf("GetThread: %v", err)
	}
	if thread.Favorite {
		t.Error("expected a new thread to default to not favorited")
	}

	if err := s.SetThreadFavorite("t1", true); err != nil {
		t.Fatalf("SetThreadFavorite(true): %v", err)
	}
	thread, err = s.GetThread("t1")
	if err != nil {
		t.Fatalf("GetThread: %v", err)
	}
	if !thread.Favorite {
		t.Error("expected Favorite to be true after SetThreadFavorite(true)")
	}

	if err := s.SetThreadFavorite("t1", false); err != nil {
		t.Fatalf("SetThreadFavorite(false): %v", err)
	}
	thread, err = s.GetThread("t1")
	if err != nil {
		t.Fatalf("GetThread: %v", err)
	}
	if thread.Favorite {
		t.Error("expected Favorite to be false after SetThreadFavorite(false)")
	}
}

func TestGetThread_NotFound(t *testing.T) {
	s := openTestStore(t)
	if _, err := s.GetThread("does-not-exist"); err == nil {
		t.Fatal("expected an error for a nonexistent thread")
	}
}

func TestAddMessage_AccumulatesThreadCost(t *testing.T) {
	s := openTestStore(t)
	if err := s.CreateThread("t1", "Thread", "test-model", "web"); err != nil {
		t.Fatalf("CreateThread: %v", err)
	}

	if _, err := s.AddMessage("t1", "user", "hello", "[]", "[]", 0, ""); err != nil {
		t.Fatalf("AddMessage (user): %v", err)
	}
	if _, err := s.AddMessage("t1", "assistant", "hi there", "[]", "[]", 0.0025, ""); err != nil {
		t.Fatalf("AddMessage (assistant): %v", err)
	}

	thread, err := s.GetThread("t1")
	if err != nil {
		t.Fatalf("GetThread: %v", err)
	}
	if thread.CostUSD != 0.0025 {
		t.Errorf("thread.CostUSD = %v, want 0.0025", thread.CostUSD)
	}

	msgs, err := s.GetMessages("t1")
	if err != nil {
		t.Fatalf("GetMessages: %v", err)
	}
	if len(msgs) != 2 {
		t.Fatalf("got %d messages, want 2", len(msgs))
	}
	if msgs[0].Role != "user" || msgs[1].Role != "assistant" {
		t.Errorf("message order/roles wrong: %+v", msgs)
	}
}

func TestSetMessageDuration_RecordsElapsedTime(t *testing.T) {
	s := openTestStore(t)
	if err := s.CreateThread("t1", "Thread", "test-model", "web"); err != nil {
		t.Fatalf("CreateThread: %v", err)
	}
	assistantID, err := s.AddMessage("t1", "assistant", "answer", "[]", "[]", 0, "")
	if err != nil {
		t.Fatalf("AddMessage: %v", err)
	}

	msgs, err := s.GetMessages("t1")
	if err != nil {
		t.Fatalf("GetMessages: %v", err)
	}
	if msgs[0].DurationMs != 0 {
		t.Errorf("DurationMs before SetMessageDuration = %d, want 0", msgs[0].DurationMs)
	}

	if err := s.SetMessageDuration(assistantID, 4200); err != nil {
		t.Fatalf("SetMessageDuration: %v", err)
	}

	msgs, err = s.GetMessages("t1")
	if err != nil {
		t.Fatalf("GetMessages: %v", err)
	}
	if msgs[0].DurationMs != 4200 {
		t.Errorf("DurationMs = %d, want 4200", msgs[0].DurationMs)
	}
}

func TestDeleteMessagesFromAndAddMessage_RecomputesCostAndReplaces(t *testing.T) {
	s := openTestStore(t)
	if err := s.CreateThread("t1", "Thread", "test-model", "web"); err != nil {
		t.Fatalf("CreateThread: %v", err)
	}

	if _, err := s.AddMessage("t1", "user", "q1", "[]", "[]", 0, ""); err != nil {
		t.Fatalf("AddMessage: %v", err)
	}
	a1ID, err := s.AddMessage("t1", "assistant", "a1", "[]", "[]", 0.01, "")
	if err != nil {
		t.Fatalf("AddMessage: %v", err)
	}
	if _, err := s.AddMessage("t1", "user", "q2 (retry target)", "[]", "[]", 0, ""); err != nil {
		t.Fatalf("AddMessage: %v", err)
	}
	if _, err := s.AddMessage("t1", "assistant", "a2", "[]", "[]", 0.02, ""); err != nil {
		t.Fatalf("AddMessage: %v", err)
	}

	// Editing/retrying from the first assistant message's slot replaces it
	// (and everything after) with a single new message in one atomic
	// operation — cost must drop back to whatever's left, plus the new
	// message's own cost.
	newMsgID, err := s.DeleteMessagesFromAndAddMessage("t1", a1ID, "user", "edited q1", "[]", "[]", 0.005, "")
	if err != nil {
		t.Fatalf("DeleteMessagesFromAndAddMessage: %v", err)
	}

	thread, err := s.GetThread("t1")
	if err != nil {
		t.Fatalf("GetThread: %v", err)
	}
	if thread.CostUSD != 0.005 {
		t.Errorf("CostUSD = %v, want 0.005 (only the new message's cost)", thread.CostUSD)
	}
	msgs, err := s.GetMessages("t1")
	if err != nil {
		t.Fatalf("GetMessages: %v", err)
	}
	if len(msgs) != 2 {
		t.Fatalf("got %d messages, want 2 (the original first user message + the new replacement)", len(msgs))
	}
	if msgs[1].ID != newMsgID || msgs[1].Content != "edited q1" {
		t.Errorf("msgs[1] = %+v, want the new replacement message with id %d", msgs[1], newMsgID)
	}
}

func TestDeleteMessagesFromAndAddMessage_RollsBackOnFailure(t *testing.T) {
	s := openTestStore(t)
	if err := s.CreateThread("t1", "Thread", "test-model", "web"); err != nil {
		t.Fatalf("CreateThread: %v", err)
	}
	q1ID, err := s.AddMessage("t1", "user", "q1", "[]", "[]", 0, "")
	if err != nil {
		t.Fatalf("AddMessage: %v", err)
	}

	// A thread_id that doesn't exist violates the messages table's foreign
	// key, failing the INSERT after the DELETE has already run inside the
	// same transaction — the whole thing must roll back, not leave q1
	// deleted with nothing replacing it.
	if _, err := s.DeleteMessagesFromAndAddMessage("does-not-exist", q1ID, "user", "x", "[]", "[]", 0, ""); err == nil {
		t.Fatal("expected an error for a nonexistent thread")
	}

	msgs, err := s.GetMessages("t1")
	if err != nil {
		t.Fatalf("GetMessages: %v", err)
	}
	if len(msgs) != 1 || msgs[0].ID != q1ID {
		t.Errorf("msgs = %+v, want q1 untouched — the failed call must not have deleted it", msgs)
	}
}

func TestCompactThread(t *testing.T) {
	s := openTestStore(t)
	if err := s.CreateThread("t1", "Thread", "test-model", "web"); err != nil {
		t.Fatalf("CreateThread: %v", err)
	}
	msgID, err := s.AddMessage("t1", "assistant", "some answer", "[]", "[]", 0, "")
	if err != nil {
		t.Fatalf("AddMessage: %v", err)
	}

	if err := s.CompactThread("t1", "a concise summary", msgID, 0.003, 42); err != nil {
		t.Fatalf("CompactThread: %v", err)
	}

	thread, err := s.GetThread("t1")
	if err != nil {
		t.Fatalf("GetThread: %v", err)
	}
	if thread.CompactedSummary != "a concise summary" {
		t.Errorf("CompactedSummary = %q, want %q", thread.CompactedSummary, "a concise summary")
	}
	if thread.CompactedThroughID != msgID {
		t.Errorf("CompactedThroughID = %d, want %d", thread.CompactedThroughID, msgID)
	}
	if thread.ContextTokens != 42 {
		t.Errorf("ContextTokens = %d, want 42", thread.ContextTokens)
	}
	if thread.CostUSD != 0.003 {
		t.Errorf("CostUSD = %v, want 0.003 (compaction's own cost)", thread.CostUSD)
	}
}

func TestSettings_GetSetAndListAll(t *testing.T) {
	s := openTestStore(t)

	if v, err := s.GetSetting("theme"); err != nil || v != "" {
		t.Fatalf("GetSetting on unset key = (%q, %v), want (\"\", nil)", v, err)
	}

	if err := s.SetSetting("theme", "light"); err != nil {
		t.Fatalf("SetSetting: %v", err)
	}
	if v, err := s.GetSetting("theme"); err != nil || v != "light" {
		t.Fatalf("GetSetting after set = (%q, %v), want (\"light\", nil)", v, err)
	}

	// Upsert: setting the same key again replaces, not duplicates.
	if err := s.SetSetting("theme", "dark"); err != nil {
		t.Fatalf("SetSetting (update): %v", err)
	}
	all, err := s.AllSettings()
	if err != nil {
		t.Fatalf("AllSettings: %v", err)
	}
	if all["theme"] != "dark" {
		t.Errorf("AllSettings()[\"theme\"] = %q, want %q", all["theme"], "dark")
	}
}

func TestListThreads_NewestFirst(t *testing.T) {
	s := openTestStore(t)
	if err := s.CreateThread("older", "Older", "m", "web"); err != nil {
		t.Fatalf("CreateThread: %v", err)
	}
	if err := s.CreateThread("newer", "Newer", "m", "web"); err != nil {
		t.Fatalf("CreateThread: %v", err)
	}
	// Bump "older"'s updated_at so ordering isn't just insertion order.
	if err := s.AddCost("newer", 0.001); err != nil {
		t.Fatalf("AddCost: %v", err)
	}

	threads, err := s.ListThreads(10)
	if err != nil {
		t.Fatalf("ListThreads: %v", err)
	}
	if len(threads) != 2 || threads[0].ID != "newer" {
		t.Errorf("threads = %+v, want [newer, older]", threads)
	}
}

// TestDeleteThread_SoftDeletePreservesMessages verifies DeleteThread is a
// soft delete: the thread disappears from every read path (GetThread,
// ListThreads) but its messages survive untouched, since the row itself
// is never actually removed — only flagged disabled.
func TestDeleteThread_SoftDeletePreservesMessages(t *testing.T) {
	s := openTestStore(t)
	if err := s.CreateThread("t1", "Thread", "m", "web"); err != nil {
		t.Fatalf("CreateThread: %v", err)
	}
	if _, err := s.AddMessage("t1", "user", "hi", "[]", "[]", 0, ""); err != nil {
		t.Fatalf("AddMessage: %v", err)
	}
	if err := s.DeleteThread("t1"); err != nil {
		t.Fatalf("DeleteThread: %v", err)
	}
	if _, err := s.GetThread("t1"); err == nil {
		t.Error("expected GetThread to fail after delete (soft-deleted threads are hidden)")
	}
	threads, err := s.ListThreads(100)
	if err != nil {
		t.Fatalf("ListThreads: %v", err)
	}
	for _, th := range threads {
		if th.ID == "t1" {
			t.Error("expected ListThreads to omit a soft-deleted thread")
		}
	}
	msgs, err := s.GetMessages("t1")
	if err != nil {
		t.Fatalf("GetMessages: %v", err)
	}
	if len(msgs) != 1 {
		t.Errorf("got %d messages for a soft-deleted thread, want 1 (messages must survive)", len(msgs))
	}
}
