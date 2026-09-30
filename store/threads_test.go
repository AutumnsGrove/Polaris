package store

import (
	"database/sql"
	"errors"
	"sort"
	"testing"
)

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

func TestSetThreadFavorite_NonexistentIDReturnsErrNoRows(t *testing.T) {
	s := openTestStore(t)
	if err := s.SetThreadFavorite("does-not-exist", true); !errors.Is(err, sql.ErrNoRows) {
		t.Errorf("SetThreadFavorite(nonexistent) = %v, want sql.ErrNoRows", err)
	}
}

func TestSetThreadTitle_NonexistentIDReturnsErrNoRows(t *testing.T) {
	s := openTestStore(t)
	if err := s.SetThreadTitle("does-not-exist", "new title"); !errors.Is(err, sql.ErrNoRows) {
		t.Errorf("SetThreadTitle(nonexistent) = %v, want sql.ErrNoRows", err)
	}
}

func TestListThreads_ExcludesUncontinuedAtlasThreads(t *testing.T) {
	s := openTestStore(t)
	if err := s.CreateThread("web1", "web thread", "test-model", "web"); err != nil {
		t.Fatalf("CreateThread(web): %v", err)
	}
	if err := s.CreateThread("atlas1", "atlas thread", "test-model", "atlas"); err != nil {
		t.Fatalf("CreateThread(atlas): %v", err)
	}

	threads, err := s.ListThreads(100)
	if err != nil {
		t.Fatalf("ListThreads: %v", err)
	}
	if len(threads) != 1 || threads[0].ID != "web1" {
		t.Fatalf("threads = %+v, want only web1 (atlas1 not yet continued)", threads)
	}

	if err := s.MarkThreadContinued("atlas1"); err != nil {
		t.Fatalf("MarkThreadContinued: %v", err)
	}
	threads, err = s.ListThreads(100)
	if err != nil {
		t.Fatalf("ListThreads (after continue): %v", err)
	}
	if len(threads) != 2 {
		t.Fatalf("threads = %+v, want both web1 and atlas1 once continued", threads)
	}
}

func TestGetThread_NotFound(t *testing.T) {
	s := openTestStore(t)
	if _, err := s.GetThread("does-not-exist"); err == nil {
		t.Fatal("expected an error for a nonexistent thread")
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

// TestCreateGhostThread_ExcludedFromListingsAndDirectLookup covers the
// visibility half of the ghost-thread-promotion redesign: a ghost thread
// is a fully real row (unlike the old design), but must stay invisible to
// every listing/search path exactly like a genuinely absent thread, while
// still being fully readable via the internal GetThreadRaw/ReadThread
// paths that already knew to bypass disabled/hidden-variant filtering.
func TestCreateGhostThread_ExcludedFromListingsAndDirectLookup(t *testing.T) {
	s := openTestStore(t)
	if err := s.CreateGhostThread("g1", "a ghost question", "m", "web"); err != nil {
		t.Fatalf("CreateGhostThread: %v", err)
	}
	if _, err := s.AddMessage("g1", "user", "a ghost question", "[]", "[]", 0, ""); err != nil {
		t.Fatalf("AddMessage: %v", err)
	}
	if _, err := s.AddMessage("g1", "assistant", "a ghost answer", "[]", "[]", 0.01, ""); err != nil {
		t.Fatalf("AddMessage: %v", err)
	}

	if _, err := s.GetThread("g1"); err == nil {
		t.Error("GetThread succeeded for an unpromoted ghost thread, want it excluded like a missing id")
	}
	raw, err := s.GetThreadRaw("g1")
	if err != nil {
		t.Fatalf("GetThreadRaw: %v", err)
	}
	if !raw.Ghost {
		t.Error("GetThreadRaw's Ghost = false, want true")
	}

	threads, err := s.ListThreads(100)
	if err != nil {
		t.Fatalf("ListThreads: %v", err)
	}
	for _, th := range threads {
		if th.ID == "g1" {
			t.Error("ListThreads included an unpromoted ghost thread, want it excluded")
		}
	}

	summaries, _, err := s.ListThreadsPage("")
	if err != nil {
		t.Fatalf("ListThreadsPage: %v", err)
	}
	for _, th := range summaries {
		if th.ID == "g1" {
			t.Error("ListThreadsPage included an unpromoted ghost thread, want it excluded")
		}
	}

	results, err := s.SearchMessages("ghost", 30)
	if err != nil {
		t.Fatalf("SearchMessages: %v", err)
	}
	for _, r := range results {
		if r.ThreadID == "g1" {
			t.Error("SearchMessages surfaced an unpromoted ghost thread's message, want it excluded")
		}
	}

	if _, err := s.ReadThread("g1"); !errors.Is(err, sql.ErrNoRows) {
		t.Errorf("ReadThread error = %v, want sql.ErrNoRows for an unpromoted ghost thread", err)
	}
}

// TestPromoteGhostThread_MakesItFullyNormal covers the "clearing the flag
// alone is enough" requirement — no other write should be needed for a
// promoted thread to become indistinguishable from any other.
func TestPromoteGhostThread_MakesItFullyNormal(t *testing.T) {
	s := openTestStore(t)
	if err := s.CreateGhostThread("g2", "q", "m", "web"); err != nil {
		t.Fatalf("CreateGhostThread: %v", err)
	}
	if _, err := s.AddMessage("g2", "user", "q", "[]", "[]", 0, ""); err != nil {
		t.Fatalf("AddMessage: %v", err)
	}

	if err := s.PromoteGhostThread("g2"); err != nil {
		t.Fatalf("PromoteGhostThread: %v", err)
	}
	// Idempotent: calling it again must not error or change anything.
	if err := s.PromoteGhostThread("g2"); err != nil {
		t.Fatalf("second PromoteGhostThread: %v", err)
	}

	thread, err := s.GetThread("g2")
	if err != nil {
		t.Fatalf("GetThread after promote: %v, want it to now succeed", err)
	}
	if thread.ID != "g2" {
		t.Errorf("GetThread returned id %q, want %q", thread.ID, "g2")
	}
	found := false
	threads, err := s.ListThreads(100)
	if err != nil {
		t.Fatalf("ListThreads: %v", err)
	}
	for _, th := range threads {
		if th.ID == "g2" {
			found = true
		}
	}
	if !found {
		t.Error("ListThreads omitted a promoted thread, want it included like any other")
	}
}

// TestDeleteThreadPermanently_OnlyDeletesIfStillGhost is the race-safety
// guarantee gateway/ws.go's disconnect-sweep relies on: a promote landing
// between "this connection noted the thread as ghost" and "the sweep
// actually runs" must leave the now-permanent thread untouched.
func TestDeleteThreadPermanently_OnlyDeletesIfStillGhost(t *testing.T) {
	s := openTestStore(t)
	if err := s.CreateGhostThread("g3", "q", "m", "web"); err != nil {
		t.Fatalf("CreateGhostThread: %v", err)
	}
	if _, err := s.AddMessage("g3", "user", "q", "[]", "[]", 0, ""); err != nil {
		t.Fatalf("AddMessage: %v", err)
	}
	if err := s.PromoteGhostThread("g3"); err != nil {
		t.Fatalf("PromoteGhostThread: %v", err)
	}

	if err := s.DeleteThreadPermanently("g3"); err != nil {
		t.Fatalf("DeleteThreadPermanently: %v", err)
	}
	if _, err := s.GetThread("g3"); err != nil {
		t.Errorf("GetThread after DeleteThreadPermanently on a promoted thread: %v, want it to survive", err)
	}

	// An actually-still-ghost thread, by contrast, must really go —
	// including its messages, via the ON DELETE CASCADE FK.
	if err := s.CreateGhostThread("g4", "q", "m", "web"); err != nil {
		t.Fatalf("CreateGhostThread: %v", err)
	}
	if _, err := s.AddMessage("g4", "user", "q", "[]", "[]", 0, ""); err != nil {
		t.Fatalf("AddMessage: %v", err)
	}
	if err := s.DeleteThreadPermanently("g4"); err != nil {
		t.Fatalf("DeleteThreadPermanently: %v", err)
	}
	if _, err := s.GetThreadRaw("g4"); !errors.Is(err, sql.ErrNoRows) {
		t.Errorf("GetThreadRaw after DeleteThreadPermanently on a still-ghost thread: err = %v, want sql.ErrNoRows", err)
	}
	if msgs, err := s.GetMessages("g4"); err != nil {
		t.Fatalf("GetMessages: %v", err)
	} else if len(msgs) != 0 {
		t.Errorf("GetMessages returned %d rows after cascade delete, want 0", len(msgs))
	}
}

// TestDeleteAllGhostThreads_SweepsOnlyGhostThreadsAndReturnsTheirIDs is
// cmd/run.go's startup-crash-sweep path — the belt-and-suspenders
// counterpart to gateway/ws.go's graceful disconnect cleanup, for a ghost
// thread abandoned by a server crash/force-quit that never got a chance
// to run.
func TestDeleteAllGhostThreads_SweepsOnlyGhostThreadsAndReturnsTheirIDs(t *testing.T) {
	s := openTestStore(t)
	if err := s.CreateGhostThread("ghost-a", "q", "m", "web"); err != nil {
		t.Fatalf("CreateGhostThread: %v", err)
	}
	if err := s.CreateGhostThread("ghost-b", "q", "m", "web"); err != nil {
		t.Fatalf("CreateGhostThread: %v", err)
	}
	if err := s.CreateThread("normal", "q", "m", "web"); err != nil {
		t.Fatalf("CreateThread: %v", err)
	}

	ids, err := s.DeleteAllGhostThreads()
	if err != nil {
		t.Fatalf("DeleteAllGhostThreads: %v", err)
	}
	sort.Strings(ids)
	if want := []string{"ghost-a", "ghost-b"}; !equalStrSlices(ids, want) {
		t.Errorf("DeleteAllGhostThreads returned %v, want %v", ids, want)
	}

	if _, err := s.GetThreadRaw("ghost-a"); !errors.Is(err, sql.ErrNoRows) {
		t.Errorf("ghost-a still exists after sweep: err = %v", err)
	}
	if _, err := s.GetThreadRaw("ghost-b"); !errors.Is(err, sql.ErrNoRows) {
		t.Errorf("ghost-b still exists after sweep: err = %v", err)
	}
	if _, err := s.GetThread("normal"); err != nil {
		t.Errorf("normal thread was swept up too: %v, want it untouched", err)
	}

	// A second sweep with nothing left to sweep must be a harmless no-op.
	ids, err = s.DeleteAllGhostThreads()
	if err != nil {
		t.Fatalf("second DeleteAllGhostThreads: %v", err)
	}
	if len(ids) != 0 {
		t.Errorf("second sweep returned %v, want none", ids)
	}
}

func equalStrSlices(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
