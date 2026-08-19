package store

import (
	"testing"
	"time"
)

func TestGetCachedSearchPage_MissReturnsFalse(t *testing.T) {
	s := openTestStore(t)
	page, ok, err := s.GetCachedSearchPage("searxng", "golang release", "", 1, 20)
	if err != nil {
		t.Fatalf("GetCachedSearchPage returned error: %v", err)
	}
	if ok || page != nil {
		t.Errorf("ok = %v, page = %+v, want a miss (false, nil)", ok, page)
	}
}

func TestSaveAndGetCachedSearchPage_RoundTrips(t *testing.T) {
	s := openTestStore(t)
	results := []CachedSearchResult{
		{Title: "First", URL: "https://a.com/1", Content: "c1", Score: 2.0, Engine: "brave", Engines: []string{"brave", "duckduckgo"}, RankState: "raise"},
		{Title: "Second", URL: "https://a.com/2", Content: "c2", Score: 1.0},
	}
	if err := s.SaveCachedSearchPage("searxng", "golang release", "news", 1, 20, results, true, false); err != nil {
		t.Fatalf("SaveCachedSearchPage returned error: %v", err)
	}

	page, ok, err := s.GetCachedSearchPage("searxng", "golang release", "news", 1, 20)
	if err != nil {
		t.Fatalf("GetCachedSearchPage returned error: %v", err)
	}
	if !ok {
		t.Fatal("ok = false, want a hit after saving")
	}
	if !page.HasMore || page.Degraded {
		t.Errorf("HasMore = %v, Degraded = %v, want true, false", page.HasMore, page.Degraded)
	}
	if len(page.Results) != 2 {
		t.Fatalf("got %d results, want 2", len(page.Results))
	}
	// Order preserved (position ASC), not insertion-order-by-luck.
	if page.Results[0].Title != "First" || page.Results[1].Title != "Second" {
		t.Errorf("results = %+v, want First then Second in order", page.Results)
	}
	if page.Results[0].RankState != "raise" {
		t.Errorf("Results[0].RankState = %q, want %q", page.Results[0].RankState, "raise")
	}
	if len(page.Results[0].Engines) != 2 || page.Results[0].Engines[0] != "brave" {
		t.Errorf("Results[0].Engines = %v, want [brave duckduckgo]", page.Results[0].Engines)
	}
	if page.FetchedAt.IsZero() {
		t.Error("FetchedAt is zero, want a real timestamp")
	}
}

func TestSaveCachedSearchPage_DifferentProvidersDoNotCollide(t *testing.T) {
	s := openTestStore(t)
	if err := s.SaveCachedSearchPage("searxng", "q", "", 1, 20, []CachedSearchResult{{Title: "From SearXNG", URL: "https://a.com/1"}}, false, false); err != nil {
		t.Fatalf("saving searxng page: %v", err)
	}
	if err := s.SaveCachedSearchPage("brave", "q", "", 1, 20, []CachedSearchResult{{Title: "From Brave", URL: "https://b.com/1"}}, false, false); err != nil {
		t.Fatalf("saving brave page: %v", err)
	}

	searxngPage, ok, err := s.GetCachedSearchPage("searxng", "q", "", 1, 20)
	if err != nil || !ok {
		t.Fatalf("GetCachedSearchPage(searxng) = ok=%v err=%v", ok, err)
	}
	bravePage, ok, err := s.GetCachedSearchPage("brave", "q", "", 1, 20)
	if err != nil || !ok {
		t.Fatalf("GetCachedSearchPage(brave) = ok=%v err=%v", ok, err)
	}
	if searxngPage.Results[0].Title != "From SearXNG" {
		t.Errorf("searxng cache = %+v, want the searxng-tagged row", searxngPage.Results)
	}
	if bravePage.Results[0].Title != "From Brave" {
		t.Errorf("brave cache = %+v, want the brave-tagged row", bravePage.Results)
	}
}

func TestSaveCachedSearchPage_OverwritesPreviousResultsForSameKey(t *testing.T) {
	s := openTestStore(t)
	if err := s.SaveCachedSearchPage("searxng", "q", "", 1, 20, []CachedSearchResult{
		{Title: "Old 1", URL: "https://a.com/1"},
		{Title: "Old 2", URL: "https://a.com/2"},
	}, false, false); err != nil {
		t.Fatalf("first save: %v", err)
	}

	// A fresh live fetch for the same key replaces the old rows entirely
	// (fewer results this time) — must not leave "Old 2" behind as a
	// stale leftover alongside the new single result.
	if err := s.SaveCachedSearchPage("searxng", "q", "", 1, 20, []CachedSearchResult{
		{Title: "New 1", URL: "https://a.com/3"},
	}, true, false); err != nil {
		t.Fatalf("second save: %v", err)
	}

	page, ok, err := s.GetCachedSearchPage("searxng", "q", "", 1, 20)
	if err != nil || !ok {
		t.Fatalf("GetCachedSearchPage = ok=%v err=%v", ok, err)
	}
	if len(page.Results) != 1 || page.Results[0].Title != "New 1" {
		t.Errorf("results = %+v, want only [New 1] after overwrite", page.Results)
	}
	if !page.HasMore {
		t.Error("HasMore = false, want true (the second save's value, not the first's)")
	}
}

func TestGetCachedSearchPage_FetchedAtReflectsRealTime(t *testing.T) {
	s := openTestStore(t)
	before := time.Now().Add(-time.Second)
	if err := s.SaveCachedSearchPage("searxng", "q", "", 1, 20, nil, false, false); err != nil {
		t.Fatalf("SaveCachedSearchPage: %v", err)
	}
	page, ok, err := s.GetCachedSearchPage("searxng", "q", "", 1, 20)
	if err != nil || !ok {
		t.Fatalf("GetCachedSearchPage = ok=%v err=%v", ok, err)
	}
	if page.FetchedAt.Before(before) {
		t.Errorf("FetchedAt = %v, want a time at/after %v (this test's own start)", page.FetchedAt, before)
	}
}
