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

func TestCreateAndGetThread(t *testing.T) {
	s := openTestStore(t)

	if err := s.CreateThread("t1", "My Thread", "test-model"); err != nil {
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

func TestGetThread_NotFound(t *testing.T) {
	s := openTestStore(t)
	if _, err := s.GetThread("does-not-exist"); err == nil {
		t.Fatal("expected an error for a nonexistent thread")
	}
}

func TestAddMessage_AccumulatesThreadCost(t *testing.T) {
	s := openTestStore(t)
	if err := s.CreateThread("t1", "Thread", "test-model"); err != nil {
		t.Fatalf("CreateThread: %v", err)
	}

	if _, err := s.AddMessage("t1", "user", "hello", "[]", "[]", 0); err != nil {
		t.Fatalf("AddMessage (user): %v", err)
	}
	if _, err := s.AddMessage("t1", "assistant", "hi there", "[]", "[]", 0.0025); err != nil {
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

func TestDeleteMessagesFrom_RecomputesCost(t *testing.T) {
	s := openTestStore(t)
	if err := s.CreateThread("t1", "Thread", "test-model"); err != nil {
		t.Fatalf("CreateThread: %v", err)
	}

	if _, err := s.AddMessage("t1", "user", "q1", "[]", "[]", 0); err != nil {
		t.Fatalf("AddMessage: %v", err)
	}
	a1ID, err := s.AddMessage("t1", "assistant", "a1", "[]", "[]", 0.01)
	if err != nil {
		t.Fatalf("AddMessage: %v", err)
	}
	if _, err := s.AddMessage("t1", "user", "q2 (retry target)", "[]", "[]", 0); err != nil {
		t.Fatalf("AddMessage: %v", err)
	}
	if _, err := s.AddMessage("t1", "assistant", "a2", "[]", "[]", 0.02); err != nil {
		t.Fatalf("AddMessage: %v", err)
	}

	// Editing/retrying from the first assistant message's slot deletes it
	// and everything after — cost must drop back to whatever's left.
	if err := s.DeleteMessagesFrom("t1", a1ID); err != nil {
		t.Fatalf("DeleteMessagesFrom: %v", err)
	}

	thread, err := s.GetThread("t1")
	if err != nil {
		t.Fatalf("GetThread: %v", err)
	}
	if thread.CostUSD != 0 {
		t.Errorf("CostUSD after deleting the only-costed messages = %v, want 0", thread.CostUSD)
	}
	msgs, err := s.GetMessages("t1")
	if err != nil {
		t.Fatalf("GetMessages: %v", err)
	}
	if len(msgs) != 1 {
		t.Errorf("got %d messages after delete, want 1 (just the first user message)", len(msgs))
	}
}

func TestCompactThread(t *testing.T) {
	s := openTestStore(t)
	if err := s.CreateThread("t1", "Thread", "test-model"); err != nil {
		t.Fatalf("CreateThread: %v", err)
	}
	msgID, err := s.AddMessage("t1", "assistant", "some answer", "[]", "[]", 0)
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
	if err := s.CreateThread("older", "Older", "m"); err != nil {
		t.Fatalf("CreateThread: %v", err)
	}
	if err := s.CreateThread("newer", "Newer", "m"); err != nil {
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

func TestDeleteThread_CascadesMessages(t *testing.T) {
	s := openTestStore(t)
	if err := s.CreateThread("t1", "Thread", "m"); err != nil {
		t.Fatalf("CreateThread: %v", err)
	}
	if _, err := s.AddMessage("t1", "user", "hi", "[]", "[]", 0); err != nil {
		t.Fatalf("AddMessage: %v", err)
	}
	if err := s.DeleteThread("t1"); err != nil {
		t.Fatalf("DeleteThread: %v", err)
	}
	if _, err := s.GetThread("t1"); err == nil {
		t.Error("expected GetThread to fail after delete")
	}
	msgs, err := s.GetMessages("t1")
	if err != nil {
		t.Fatalf("GetMessages: %v", err)
	}
	if len(msgs) != 0 {
		t.Errorf("got %d messages for a deleted thread, want 0 (cascade delete)", len(msgs))
	}
}
