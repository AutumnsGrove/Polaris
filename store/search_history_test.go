package store

import (
	"database/sql"
	"errors"
	"testing"
	"time"
)

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

func TestListSearchHistory_SameSecondInsertsSortByMillisecond(t *testing.T) {
	// Two distinct (never-bumped) queries recorded a few milliseconds
	// apart — well within the same wall-clock second, the scenario that
	// motivated giving search_history's created_at/updated_at columns
	// millisecond rather than second precision (see the schema comment on
	// that table). A second-precision timestamp would tie here and leave
	// relative order up to SQLite's unspecified tiebreak instead of
	// reflecting insertion order.
	s := openTestStore(t)
	if err := s.RecordSearch("alpha query"); err != nil {
		t.Fatalf("RecordSearch(alpha): %v", err)
	}
	time.Sleep(5 * time.Millisecond)
	if err := s.RecordSearch("beta query"); err != nil {
		t.Fatalf("RecordSearch(beta): %v", err)
	}

	entries, err := s.ListSearchHistory(10)
	if err != nil {
		t.Fatalf("ListSearchHistory: %v", err)
	}
	if len(entries) != 2 || entries[0].Query != "beta query" {
		t.Fatalf("entries = %+v, want beta query first (most recently inserted)", entries)
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

func TestSetSearchHistoryFavorite_NonexistentIDReturnsErrNoRows(t *testing.T) {
	s := openTestStore(t)
	if err := s.SetSearchHistoryFavorite(999, true); !errors.Is(err, sql.ErrNoRows) {
		t.Errorf("SetSearchHistoryFavorite(nonexistent) = %v, want sql.ErrNoRows", err)
	}
}
