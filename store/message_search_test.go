package store

import (
	"fmt"
	"strings"
	"testing"
)

func TestSearchMessages_FindsContentAndRespectsVisibility(t *testing.T) {
	s := openTestStore(t)

	if err := s.CreateThread("t1", "Go modules question", "test-model", "web"); err != nil {
		t.Fatalf("CreateThread(t1): %v", err)
	}
	if _, err := s.AddMessage("t1", "user", "what's the current stable version of Go?", "[]", "[]", 0, ""); err != nil {
		t.Fatalf("AddMessage: %v", err)
	}
	if _, err := s.AddMessage("t1", "assistant", "Go 1.26 is the current stable release.", "[]", "[]", 0, ""); err != nil {
		t.Fatalf("AddMessage: %v", err)
	}

	if err := s.CreateThread("t2", "unrelated", "test-model", "web"); err != nil {
		t.Fatalf("CreateThread(t2): %v", err)
	}
	if _, err := s.AddMessage("t2", "user", "find a coffee shop near the Space Needle", "[]", "[]", 0, ""); err != nil {
		t.Fatalf("AddMessage: %v", err)
	}

	// Disabled threads must not surface in search — same visibility rule
	// as ListThreads.
	if err := s.CreateThread("t3", "disabled thread", "test-model", "web"); err != nil {
		t.Fatalf("CreateThread(t3): %v", err)
	}
	if _, err := s.AddMessage("t3", "user", "golang stable release info", "[]", "[]", 0, ""); err != nil {
		t.Fatalf("AddMessage: %v", err)
	}
	if err := s.DeleteThread("t3"); err != nil {
		t.Fatalf("DeleteThread(t3): %v", err)
	}

	results, err := s.SearchMessages("stable", 30)
	if err != nil {
		t.Fatalf("SearchMessages: %v", err)
	}
	// Both t1 messages contain "stable" — t3's matching message is
	// excluded because its thread is disabled.
	if len(results) != 2 {
		t.Fatalf("SearchMessages(\"stable\") returned %d results, want 2 (disabled thread must be excluded): %+v", len(results), results)
	}
	for _, r := range results {
		if r.ThreadID != "t1" {
			t.Errorf("result thread = %q, want t1 (t3 is disabled)", r.ThreadID)
		}
	}
	if !strings.Contains(results[0].Snippet, "\x02stable\x03") {
		t.Errorf("snippet = %q, want it to wrap the matched term in \\x02...\\x03 markers", results[0].Snippet)
	}

	// Prefix matching: "cof" should find "coffee" without the full word.
	prefixResults, err := s.SearchMessages("cof", 30)
	if err != nil {
		t.Fatalf("SearchMessages(prefix): %v", err)
	}
	if len(prefixResults) != 1 || prefixResults[0].ThreadID != "t2" {
		t.Errorf("SearchMessages(\"cof\") = %+v, want one result from t2 (prefix match on \"coffee\")", prefixResults)
	}

	if got, err := s.SearchMessages("nonexistentterm", 30); err != nil || len(got) != 0 {
		t.Errorf("SearchMessages(no match) = %+v, err=%v, want empty, nil", got, err)
	}
}

// TestSearchMessages_BackfillsExistingMessages simulates upgrading a
// database that already has messages before messages_fts existed: reset
// user_version to just before the backfill migration, delete the FTS
// index's own rows (leaving messages untouched, like a pre-migration
// database really would), then confirm re-running migrations makes old
// content searchable again.
func TestSearchMessages_BackfillsExistingMessages(t *testing.T) {
	s := openTestStore(t)

	const oldContent = "a message from before the search feature existed"
	if err := s.CreateThread("t1", "old thread", "test-model", "web"); err != nil {
		t.Fatalf("CreateThread: %v", err)
	}
	msgID, err := s.AddMessage("t1", "user", oldContent, "[]", "[]", 0, "")
	if err != nil {
		t.Fatalf("AddMessage: %v", err)
	}

	// Simulate "before the backfill ran": remove this row from the FTS
	// index (the AI trigger already populated it above) via the same
	// per-row 'delete' special command the AD trigger itself uses,
	// leaving the real messages row untouched -- that's exactly the
	// state a database predating messages_fts is in. Deliberately not
	// the bulk 'delete-all' command: confirmed live it's a no-op against
	// an external-content table on this driver (modernc.org/sqlite) --
	// this per-row form is the same path the AD trigger already
	// exercises in production, so it's a closer simulation anyway.
	if _, err := s.db.Exec(`INSERT INTO messages_fts(messages_fts, rowid, content) VALUES ('delete', ?, ?)`, msgID, oldContent); err != nil {
		t.Fatalf("clearing messages_fts: %v", err)
	}
	// Rewind to just before the backfill migration specifically — found by
	// content, not assumed to be the last entry in the slice, since later
	// migrations get appended after it over time (see migrations' own
	// doc comment: append-only, never reordered).
	backfillIdx := -1
	for i, m := range migrations {
		if strings.Contains(m, "messages_fts") && strings.Contains(m, "INSERT") {
			backfillIdx = i
			break
		}
	}
	if backfillIdx == -1 {
		t.Fatal("couldn't find the messages_fts backfill migration in the migrations slice")
	}
	// PRAGMA doesn't accept bound parameters — same reasoning as
	// applyMigrations' own `PRAGMA user_version = %d` write.
	if _, err := s.db.Exec(fmt.Sprintf(`PRAGMA user_version = %d`, backfillIdx)); err != nil {
		t.Fatalf("rewinding user_version: %v", err)
	}

	if got, err := s.SearchMessages("before the search feature", 30); err != nil || len(got) != 0 {
		t.Fatalf("SearchMessages before backfill = %+v, err=%v, want empty (index cleared)", got, err)
	}

	if err := applyMigrations(s.db); err != nil {
		t.Fatalf("applyMigrations (backfill): %v", err)
	}

	got, err := s.SearchMessages("before the search feature", 30)
	if err != nil {
		t.Fatalf("SearchMessages after backfill: %v", err)
	}
	if len(got) != 1 || got[0].ThreadID != "t1" {
		t.Fatalf("SearchMessages after backfill = %+v, want one result from t1", got)
	}
}

// TestSearchMessages_FindsActiveVariantNotSupersededOne covers the fix for
// a real gap: a plain ListThreads-style filter (fork_root_id = ”) would
// make an edited/regenerated message's own *current* content unsearchable
// while the superseded original stayed findable, since the new content
// lives in a hidden fork thread. See SearchMessages' doc comment for the
// full reasoning.
func TestSearchMessages_FindsActiveVariantNotSupersededOne(t *testing.T) {
	s := openTestStore(t)

	if err := s.CreateThread("root", "Go modules question", "test-model", "web"); err != nil {
		t.Fatalf("CreateThread: %v", err)
	}
	if _, err := s.AddMessage("root", "user", "what is the original stableword content?", "[]", "[]", 0, ""); err != nil {
		t.Fatalf("AddMessage: %v", err)
	}

	// atIndex=0: nothing gets copied into the fork (no shared prefix), so
	// forkID starts out with only its own new message below — keeps the
	// assertions unambiguous about which thread a given piece of content
	// actually lives in, since ForkThread's normal copy-the-prefix
	// behavior would otherwise put a legitimate duplicate of root's
	// message inside the fork too (that's covered separately below).
	forkID, err := s.ForkThread("root", "root", 0)
	if err != nil {
		t.Fatalf("ForkThread: %v", err)
	}
	if _, err := s.AddMessage(forkID, "user", "what is the edited stableword content?", "[]", "[]", 0, ""); err != nil {
		t.Fatalf("AddMessage(fork): %v", err)
	}

	// Before swapping: root is still the active variant, so root's own
	// original content should be found and the fork's not-yet-active
	// content should not.
	before, err := s.SearchMessages("stableword", 30)
	if err != nil {
		t.Fatalf("SearchMessages before swap: %v", err)
	}
	if len(before) != 1 || before[0].ThreadID != "root" {
		t.Fatalf("SearchMessages before swap = %+v, want exactly the root's own message", before)
	}
	if !strings.Contains(before[0].Snippet, "original") {
		t.Errorf("SearchMessages before swap snippet = %q, want the root's original content, not the fork's", before[0].Snippet)
	}

	if err := s.SetActiveVariant("root", forkID); err != nil {
		t.Fatalf("SetActiveVariant: %v", err)
	}

	after, err := s.SearchMessages("stableword", 30)
	if err != nil {
		t.Fatalf("SearchMessages after swap: %v", err)
	}
	if len(after) != 1 {
		t.Fatalf("SearchMessages after swap = %+v, want exactly one result (root's now-superseded original must be excluded)", after)
	}
	if !strings.Contains(after[0].Snippet, "edited") {
		t.Errorf("SearchMessages after swap snippet = %q, want the fork's edited content, not root's stale original", after[0].Snippet)
	}
	// ThreadID/ThreadTitle must resolve to the root — forkID itself isn't
	// independently addressable via GetThread, and its own title is
	// always '' (ForkThread never sets one).
	if after[0].ThreadID != "root" {
		t.Errorf("result thread = %q, want root (forkID must never be surfaced directly)", after[0].ThreadID)
	}
	if after[0].ThreadTitle != "Go modules question" {
		t.Errorf("result title = %q, want the root's real title, not the fork's blank one", after[0].ThreadTitle)
	}
}

// TestSearchMessages_NoMatchReturnsEmptySliceNotNil confirms the response
// shape is consistent regardless of *why* nothing matched (an empty query
// vs. a real query with zero hits) — both should encode as JSON `[]`, not
// `null`, so API callers don't need to special-case the two.
func TestSearchMessages_NoMatchReturnsEmptySliceNotNil(t *testing.T) {
	s := openTestStore(t)

	got, err := s.SearchMessages("nothing will ever match this", 30)
	if err != nil {
		t.Fatalf("SearchMessages: %v", err)
	}
	if got == nil {
		t.Error("SearchMessages with zero matches returned nil, want a non-nil empty slice (encodes as JSON null instead of [])")
	}
	if len(got) != 0 {
		t.Errorf("SearchMessages = %+v, want empty", got)
	}
}
