package store

import "testing"

func TestRecordSearch_DedupesExactRepeatByBumpingUpdatedAt(t *testing.T) {
	s := openTestStore(t)

	if err := s.RecordSearch("rust async runtime"); err != nil {
		t.Fatalf("RecordSearch: %v", err)
	}
	if err := s.RecordSearch("rust async runtime"); err != nil {
		t.Fatalf("RecordSearch (repeat): %v", err)
	}

	entries, err := s.ListSearchHistory(10)
	if err != nil {
		t.Fatalf("ListSearchHistory: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("got %d entries, want 1 (repeat should bump, not duplicate)", len(entries))
	}
}

func TestListSearchHistory_NewestFirst(t *testing.T) {
	s := openTestStore(t)

	for _, q := range []string{"first query", "second query", "third query"} {
		if err := s.RecordSearch(q); err != nil {
			t.Fatalf("RecordSearch(%q): %v", q, err)
		}
	}
	// Re-recording "first query" should bump it back to the top.
	if err := s.RecordSearch("first query"); err != nil {
		t.Fatalf("RecordSearch (re-bump): %v", err)
	}

	entries, err := s.ListSearchHistory(10)
	if err != nil {
		t.Fatalf("ListSearchHistory: %v", err)
	}
	if len(entries) != 3 {
		t.Fatalf("got %d entries, want 3", len(entries))
	}
	if entries[0].Query != "first query" {
		t.Errorf("entries[0].Query = %q, want %q (most recently bumped)", entries[0].Query, "first query")
	}
}

func TestSetSearchHistoryFavorite(t *testing.T) {
	s := openTestStore(t)
	if err := s.RecordSearch("rust async runtime"); err != nil {
		t.Fatalf("RecordSearch: %v", err)
	}
	entries, err := s.ListSearchHistory(10)
	if err != nil || len(entries) != 1 {
		t.Fatalf("ListSearchHistory: entries=%v err=%v", entries, err)
	}
	id := entries[0].ID
	if entries[0].Favorite {
		t.Error("expected a new search entry to default to not favorited")
	}

	if err := s.SetSearchHistoryFavorite(id, true); err != nil {
		t.Fatalf("SetSearchHistoryFavorite(true): %v", err)
	}
	entries, err = s.ListSearchHistory(10)
	if err != nil || len(entries) != 1 {
		t.Fatalf("ListSearchHistory: entries=%v err=%v", entries, err)
	}
	if !entries[0].Favorite {
		t.Error("expected Favorite to be true after SetSearchHistoryFavorite(true)")
	}
}
