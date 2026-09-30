package store

import (
	"fmt"
	"testing"
)

// setThreadUpdatedAt directly overwrites a thread's updated_at column —
// tests need deterministic, well-separated timestamps to assert on
// ordering/pagination, rather than depending on how fast CreateThread calls
// in a loop actually execute.
func setThreadUpdatedAt(t *testing.T, s *Store, id, rawUpdatedAt string) {
	t.Helper()
	if _, err := s.db.Exec(`UPDATE threads SET updated_at = ? WHERE id = ?`, rawUpdatedAt, id); err != nil {
		t.Fatalf("setThreadUpdatedAt(%s): %v", id, err)
	}
}

// TestListThreadsPage_KeysetPaginationStableAcrossInsert is the plan's own
// stated requirement (docs/plans/search-chats.md's "Recency mode" section):
// a fixed 10-per-page, keyset-paginated by (updated_at, id) rather than
// OFFSET, so a page 2 fetch stays correct even if a new thread arrives
// after page 1 was already returned — an OFFSET-based page 2 would
// silently reshow or skip a row when the underlying order shifts between
// fetches; keyset pagination can't, since it always resumes strictly after
// the last row actually handed back.
func TestListThreadsPage_KeysetPaginationStableAcrossInsert(t *testing.T) {
	s := openTestStore(t)

	// 12 threads, t00 oldest .. t11 newest, explicit updated_at values so
	// ordering is deterministic rather than at the mercy of how fast this
	// loop happens to run.
	for i := 0; i < 12; i++ {
		id := fmt.Sprintf("t%02d", i)
		if err := s.CreateThread(id, fmt.Sprintf("thread %d", i), "test-model", "web"); err != nil {
			t.Fatalf("CreateThread(%s): %v", id, err)
		}
		setThreadUpdatedAt(t, s, id, fmt.Sprintf("2026-01-01 00:%02d:00.000", i))
	}

	page1, cursor1, err := s.ListThreadsPage("")
	if err != nil {
		t.Fatalf("ListThreadsPage(page1): %v", err)
	}
	if len(page1) != RecencyPageSize {
		t.Fatalf("len(page1) = %d, want %d", len(page1), RecencyPageSize)
	}
	if cursor1 == "" {
		t.Fatal("cursor1 is empty, want a next-page cursor (12 threads, page size 10)")
	}
	if page1[0].ID != "t11" {
		t.Errorf("page1[0].ID = %q, want t11 (newest first)", page1[0].ID)
	}
	if page1[9].ID != "t02" {
		t.Errorf("page1[9].ID = %q, want t02 (10th-newest)", page1[9].ID)
	}

	// Simulate a new thread arriving between page 1 and page 2 — newer
	// than everything already on page 1.
	if err := s.CreateThread("new-thread", "just arrived", "test-model", "web"); err != nil {
		t.Fatalf("CreateThread(new-thread): %v", err)
	}
	setThreadUpdatedAt(t, s, "new-thread", "2026-01-01 00:20:00.000")

	page2, cursor2, err := s.ListThreadsPage(cursor1)
	if err != nil {
		t.Fatalf("ListThreadsPage(page2): %v", err)
	}
	if cursor2 != "" {
		t.Errorf("cursor2 = %q, want empty — only t00/t01 remain after page 1's cursor", cursor2)
	}
	if len(page2) != 2 || page2[0].ID != "t01" || page2[1].ID != "t00" {
		t.Fatalf("page2 = %+v, want [t01, t00]", page2)
	}
	for _, th := range page2 {
		if th.ID == "new-thread" {
			t.Error("page2 contains new-thread, which arrived after page 1 was fetched — keyset pagination must exclude it")
		}
	}
}

// TestListThreadsPage_ExcludesSameThreadsAsListThreads pins ListThreadsPage
// to the exact same visibility filter ListThreads already uses (disabled,
// pulsar, unopened Atlas threads) — deliberately not re-testing every case
// TestSearchMessages_FindsContentAndRespectsVisibility already covers in
// depth, just confirming the same filter clauses are actually present.
func TestListThreadsPage_ExcludesDisabledThreads(t *testing.T) {
	s := openTestStore(t)

	if err := s.CreateThread("visible", "visible thread", "test-model", "web"); err != nil {
		t.Fatalf("CreateThread(visible): %v", err)
	}
	if err := s.CreateThread("hidden", "disabled thread", "test-model", "web"); err != nil {
		t.Fatalf("CreateThread(hidden): %v", err)
	}
	if err := s.DeleteThread("hidden"); err != nil {
		t.Fatalf("DeleteThread(hidden): %v", err)
	}

	page, _, err := s.ListThreadsPage("")
	if err != nil {
		t.Fatalf("ListThreadsPage: %v", err)
	}
	if len(page) != 1 || page[0].ID != "visible" {
		t.Errorf("ListThreadsPage = %+v, want only the non-disabled thread", page)
	}
}

// TestListThreadsPage_PreviewIsLatestMessage confirms recency mode's
// preview comes from the thread's most recent message, not its first —
// the "what's this thread actually about most recently" signal the model
// reasons over without a separate read call.
func TestListThreadsPage_PreviewIsLatestMessage(t *testing.T) {
	s := openTestStore(t)

	if err := s.CreateThread("t1", "a thread", "test-model", "web"); err != nil {
		t.Fatalf("CreateThread: %v", err)
	}
	if _, err := s.AddMessage("t1", "user", "first message", "[]", "[]", 0, ""); err != nil {
		t.Fatalf("AddMessage(first): %v", err)
	}
	if _, err := s.AddMessage("t1", "assistant", "most recent message", "[]", "[]", 0, ""); err != nil {
		t.Fatalf("AddMessage(second): %v", err)
	}

	page, _, err := s.ListThreadsPage("")
	if err != nil {
		t.Fatalf("ListThreadsPage: %v", err)
	}
	if len(page) != 1 || page[0].Preview != "most recent message" {
		t.Errorf("ListThreadsPage = %+v, want Preview = %q", page, "most recent message")
	}
}
