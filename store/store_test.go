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

// TestForkThread_PreservesOldContentAndCopiesSharedPrefix verifies the
// non-destructive edit/retry mechanism: editing/regenerating no longer
// deletes anything (the old DeleteMessagesFromAndAddMessage behavior) —
// the reply being replaced gets forked off into its own thread first, so
// it stays fully intact and reachable via VariantsAt.
func TestForkThread_PreservesOldContentAndCopiesSharedPrefix(t *testing.T) {
	s := openTestStore(t)
	if err := s.CreateThread("root", "Thread", "test-model", "web"); err != nil {
		t.Fatalf("CreateThread: %v", err)
	}
	if _, err := s.AddMessage("root", "user", "q1", "[]", "[]", 0, ""); err != nil {
		t.Fatalf("AddMessage: %v", err)
	}
	if _, err := s.AddMessage("root", "assistant", "a1", "[]", "[]", 0.01, ""); err != nil {
		t.Fatalf("AddMessage: %v", err)
	}

	// Regenerating "a1" forks at index 1 (the assistant message's own
	// position) — the fork should end up with just the shared prefix (q1),
	// ready for the caller to add the new reply.
	forkID, err := s.ForkThread("root", "root", 1)
	if err != nil {
		t.Fatalf("ForkThread: %v", err)
	}
	forkMsgs, err := s.GetMessages(forkID)
	if err != nil {
		t.Fatalf("GetMessages(fork): %v", err)
	}
	if len(forkMsgs) != 1 || forkMsgs[0].Content != "q1" {
		t.Fatalf("fork messages = %+v, want just the shared prefix [q1]", forkMsgs)
	}

	// root's own original content — the thing being "edited away" —
	// must still be completely untouched.
	rootMsgs, err := s.GetMessages("root")
	if err != nil {
		t.Fatalf("GetMessages(root): %v", err)
	}
	if len(rootMsgs) != 2 || rootMsgs[1].Content != "a1" {
		t.Errorf("root messages = %+v, want the original [q1, a1] still intact", rootMsgs)
	}

	// The fork must show up as a variant at index 1, alongside root's own
	// original content (which still reaches that far).
	variants, err := s.VariantsAt("root", 1)
	if err != nil {
		t.Fatalf("VariantsAt: %v", err)
	}
	if len(variants) != 2 || variants[0] != "root" || variants[1] != forkID {
		t.Errorf("VariantsAt = %v, want [root, %s]", variants, forkID)
	}
}

// TestForkThread_CopiesEventsForSharedPrefix guards against exactly the
// bug this shipped with once already: ForkThread copied messages but not
// their events, so a fork's shared-prefix replies had the right text but
// a silently empty reasoning/tool-call timeline — ListEvents filters by
// thread_id, and those events were still sitting under the source
// thread's id, invisible when queried by the fork's own id.
func TestForkThread_CopiesEventsForSharedPrefix(t *testing.T) {
	s := openTestStore(t)
	if err := s.CreateThread("root", "Thread", "test-model", "web"); err != nil {
		t.Fatalf("CreateThread: %v", err)
	}
	if _, err := s.AddMessage("root", "user", "q1", "[]", "[]", 0, "turn-1"); err != nil {
		t.Fatalf("AddMessage: %v", err)
	}
	if _, err := s.AddMessage("root", "assistant", "a1", "[]", "[]", 0.01, "turn-1"); err != nil {
		t.Fatalf("AddMessage: %v", err)
	}
	s.LogEvent("root", "info", "tool.web_search", "tool call started", map[string]interface{}{"query": "q1"}, "turn-1")
	s.LogEvent("root", "info", "turn", "reasoning", map[string]interface{}{"content": "thinking about q1"}, "turn-1")

	// A second, later turn whose events must NOT be copied into a fork
	// that only reaches the first turn — proof this isn't just copying
	// srcID's entire event history wholesale.
	if _, err := s.AddMessage("root", "user", "q2", "[]", "[]", 0, "turn-2"); err != nil {
		t.Fatalf("AddMessage: %v", err)
	}
	if _, err := s.AddMessage("root", "assistant", "a2", "[]", "[]", 0.01, "turn-2"); err != nil {
		t.Fatalf("AddMessage: %v", err)
	}
	s.LogEvent("root", "info", "turn", "reasoning", map[string]interface{}{"content": "thinking about q2"}, "turn-2")

	// Fork at index 2 — covers turn-1 (q1/a1) only, not turn-2.
	forkID, err := s.ForkThread("root", "root", 2)
	if err != nil {
		t.Fatalf("ForkThread: %v", err)
	}

	events, err := s.ListEvents(forkID, 100)
	if err != nil {
		t.Fatalf("ListEvents(fork): %v", err)
	}
	if len(events) != 2 {
		t.Fatalf("fork events = %+v, want exactly turn-1's tool call + reasoning", events)
	}
	for _, e := range events {
		if e.TurnID != "turn-1" {
			t.Errorf("event %+v has turn_id %q, want only turn-1's events copied", e, e.TurnID)
		}
	}

	// root's own events must be completely untouched — ForkThread reads,
	// never mutates, the source.
	rootEvents, err := s.ListEvents("root", 100)
	if err != nil {
		t.Fatalf("ListEvents(root): %v", err)
	}
	if len(rootEvents) != 3 {
		t.Errorf("root events = %+v, want all 3 original events still there", rootEvents)
	}
}

func TestEffectiveThreadID_FollowsSetActiveVariant(t *testing.T) {
	s := openTestStore(t)
	if err := s.CreateThread("root", "Thread", "test-model", "web"); err != nil {
		t.Fatalf("CreateThread: %v", err)
	}

	effective, err := s.EffectiveThreadID("root")
	if err != nil {
		t.Fatalf("EffectiveThreadID: %v", err)
	}
	if effective != "root" {
		t.Errorf("EffectiveThreadID = %q, want %q (no variant set yet)", effective, "root")
	}

	forkID, err := s.ForkThread("root", "root", 0)
	if err != nil {
		t.Fatalf("ForkThread: %v", err)
	}
	if err := s.SetActiveVariant("root", forkID); err != nil {
		t.Fatalf("SetActiveVariant: %v", err)
	}
	effective, err = s.EffectiveThreadID("root")
	if err != nil {
		t.Fatalf("EffectiveThreadID: %v", err)
	}
	if effective != forkID {
		t.Errorf("EffectiveThreadID = %q, want the fork %q after browsing to it", effective, forkID)
	}

	// Swapping back to root itself resets it — no lingering pointer.
	if err := s.SetActiveVariant("root", "root"); err != nil {
		t.Fatalf("SetActiveVariant(back to root): %v", err)
	}
	effective, err = s.EffectiveThreadID("root")
	if err != nil {
		t.Fatalf("EffectiveThreadID: %v", err)
	}
	if effective != "root" {
		t.Errorf("EffectiveThreadID = %q, want %q after switching back", effective, "root")
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

// TestTouchUpdatedAt_KeepsRootRecentAfterVariantSwitch is a regression test
// for the "thread bump-back" symptom: once a thread has ever been edited/
// retried (SetActiveVariant points it at a fork), every later AddMessage
// writes to that fork's own row, not the root's — so without an explicit
// TouchUpdatedAt on the root, ListThreads' recency order silently freezes
// that thread in place even while it's actively being used, letting an
// untouched, older thread outrank it and sit above it in the sidebar.
func TestTouchUpdatedAt_KeepsRootRecentAfterVariantSwitch(t *testing.T) {
	s := openTestStore(t)
	if err := s.CreateThread("root", "Root", "m", "web"); err != nil {
		t.Fatalf("CreateThread(root): %v", err)
	}
	if _, err := s.AddMessage("root", "user", "hi", "[]", "[]", 0, "turn1"); err != nil {
		t.Fatalf("AddMessage: %v", err)
	}
	if err := s.CreateThread("other", "Other", "m", "web"); err != nil {
		t.Fatalf("CreateThread(other): %v", err)
	}

	forkID, err := s.ForkThread("root", "root", 1)
	if err != nil {
		t.Fatalf("ForkThread: %v", err)
	}
	if err := s.SetActiveVariant("root", forkID); err != nil {
		t.Fatalf("SetActiveVariant: %v", err)
	}

	// Simulate handleTurn continuing the conversation post-edit: it writes
	// to the effective (forked) thread, then explicitly touches the root.
	effective, err := s.EffectiveThreadID("root")
	if err != nil {
		t.Fatalf("EffectiveThreadID: %v", err)
	}
	if _, err := s.AddMessage(effective, "assistant", "new answer", "[]", "[]", 0, "turn2"); err != nil {
		t.Fatalf("AddMessage(effective): %v", err)
	}
	if err := s.TouchUpdatedAt("root"); err != nil {
		t.Fatalf("TouchUpdatedAt: %v", err)
	}

	threads, err := s.ListThreads(10)
	if err != nil {
		t.Fatalf("ListThreads: %v", err)
	}
	if len(threads) != 2 || threads[0].ID != "root" {
		t.Errorf("threads = %+v, want [root, other] — root should still sort as most recently active", threads)
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

func TestGetAPIUsage_ZeroForNeverUsedProvider(t *testing.T) {
	s := openTestStore(t)
	count, err := s.GetAPIUsage("parallel")
	if err != nil {
		t.Fatalf("GetAPIUsage: %v", err)
	}
	if count != 0 {
		t.Errorf("count = %d, want 0 for a provider that's never been recorded", count)
	}
}

func TestIncrementAPIUsage_CountsUpWithinTheSameMonth(t *testing.T) {
	s := openTestStore(t)
	for i := 1; i <= 3; i++ {
		count, err := s.IncrementAPIUsage("parallel")
		if err != nil {
			t.Fatalf("IncrementAPIUsage (call %d): %v", i, err)
		}
		if count != i {
			t.Errorf("IncrementAPIUsage call %d returned %d, want %d", i, count, i)
		}
	}
	stored, err := s.GetAPIUsage("parallel")
	if err != nil {
		t.Fatalf("GetAPIUsage: %v", err)
	}
	if stored != 3 {
		t.Errorf("GetAPIUsage = %d, want 3", stored)
	}
}

func TestAPIUsage_IsPerProvider(t *testing.T) {
	s := openTestStore(t)
	if _, err := s.IncrementAPIUsage("parallel"); err != nil {
		t.Fatalf("IncrementAPIUsage(parallel): %v", err)
	}
	if _, err := s.IncrementAPIUsage("parallel"); err != nil {
		t.Fatalf("IncrementAPIUsage(parallel): %v", err)
	}
	if _, err := s.IncrementAPIUsage("tavily"); err != nil {
		t.Fatalf("IncrementAPIUsage(tavily): %v", err)
	}

	parallelCount, err := s.GetAPIUsage("parallel")
	if err != nil {
		t.Fatalf("GetAPIUsage(parallel): %v", err)
	}
	if parallelCount != 2 {
		t.Errorf("GetAPIUsage(parallel) = %d, want 2", parallelCount)
	}

	tavilyCount, err := s.GetAPIUsage("tavily")
	if err != nil {
		t.Fatalf("GetAPIUsage(tavily): %v", err)
	}
	if tavilyCount != 1 {
		t.Errorf("GetAPIUsage(tavily) = %d, want 1 — providers must not share a counter", tavilyCount)
	}
}
