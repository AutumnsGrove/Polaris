package search

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"polaris/store"
)

func openVirtualPageTestStore(t *testing.T) *store.Store {
	t.Helper()
	db, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

// fakeFetcher returns canned pages (1-indexed) and counts how many times
// it was actually called, so tests can assert on live-call counts.
func fakeFetcher(pages map[int][]store.CachedSearchResult, hasMore map[int]bool) (PageFetcher, *int) {
	calls := 0
	return func(ctx context.Context, realPage int) ([]store.CachedSearchResult, bool, bool, []string, error) {
		calls++
		return pages[realPage], hasMore[realPage], false, nil, nil
	}, &calls
}

func TestResolveVirtualPage_CacheMissCallsFetchAndSaves(t *testing.T) {
	db := openVirtualPageTestStore(t)
	page1 := []store.CachedSearchResult{{Title: "1", URL: "https://a.com/1"}}
	fetch, calls := fakeFetcher(map[int][]store.CachedSearchResult{1: page1}, map[int]bool{1: false})

	result, err := ResolveVirtualPage(context.Background(), db, "searxng", "q", "", 1, 20, fetch)
	if err != nil {
		t.Fatalf("ResolveVirtualPage returned error: %v", err)
	}
	if *calls != 1 {
		t.Errorf("fetch called %d times, want 1", *calls)
	}
	if result.CacheHit {
		t.Error("CacheHit = true, want false on a genuine miss")
	}
	if len(result.Results) != 1 || result.Results[0].Title != "1" {
		t.Errorf("Results = %+v, want [{Title: 1}]", result.Results)
	}

	cached, ok, err := db.GetCachedSearchPage("searxng", "q", "", 1, 20)
	if err != nil || !ok {
		t.Fatalf("expected the fetch to have been cached: ok=%v err=%v", ok, err)
	}
	if len(cached.Results) != 1 {
		t.Errorf("cached results = %+v, want the fetched page saved", cached.Results)
	}
}

func TestResolveVirtualPage_FreshCacheSkipsFetchEntirely(t *testing.T) {
	db := openVirtualPageTestStore(t)
	if err := db.SaveCachedSearchPage("searxng", "q", "", 1, 20, []store.CachedSearchResult{{Title: "cached", URL: "https://a.com/1"}}, false, false); err != nil {
		t.Fatalf("seeding cache: %v", err)
	}
	fetch, calls := fakeFetcher(nil, nil)

	result, err := ResolveVirtualPage(context.Background(), db, "searxng", "q", "", 1, 20, fetch)
	if err != nil {
		t.Fatalf("ResolveVirtualPage returned error: %v", err)
	}
	if *calls != 0 {
		t.Errorf("fetch called %d times, want 0 (fresh cache should skip it entirely)", *calls)
	}
	if !result.CacheHit {
		t.Error("CacheHit = false, want true")
	}
	if len(result.Results) != 1 || result.Results[0].Title != "cached" {
		t.Errorf("Results = %+v, want the cached row", result.Results)
	}
}

func TestResolveVirtualPage_AccumulatesAcrossMultipleRealPages(t *testing.T) {
	db := openVirtualPageTestStore(t)
	// Real page 1 has only 6 results (fewer than VirtualPageSize=10), so
	// virtual page 1 must walk into real page 2 to fill out to 10.
	page1 := make([]store.CachedSearchResult, 6)
	for i := range page1 {
		page1[i] = store.CachedSearchResult{Title: "p1", URL: "https://a.com/p1"}
	}
	page2 := make([]store.CachedSearchResult, 6)
	for i := range page2 {
		page2[i] = store.CachedSearchResult{Title: "p2", URL: "https://a.com/p2"}
	}
	// page2's hasMore=false — it's the real last page, so a virtual page
	// asking for more than page1+page2 can offer stops there rather than
	// walking into a nonexistent real page 3.
	fetch, calls := fakeFetcher(
		map[int][]store.CachedSearchResult{1: page1, 2: page2},
		map[int]bool{1: true, 2: false},
	)

	result, err := ResolveVirtualPage(context.Background(), db, "searxng", "q", "", 1, 20, fetch)
	if err != nil {
		t.Fatalf("ResolveVirtualPage returned error: %v", err)
	}
	if *calls != 2 {
		t.Errorf("fetch called %d times, want 2 (real page 1 alone wasn't enough)", *calls)
	}
	if len(result.Results) != 10 {
		t.Fatalf("got %d results, want 10 (6 from page 1 + 4 from page 2)", len(result.Results))
	}
	if result.Results[9].Title != "p2" {
		t.Errorf("Results[9] = %+v, want the 4th result of page 2", result.Results[9])
	}
	if !result.HasMore {
		t.Error("HasMore = false, want true — 2 leftover results from page 2 weren't consumed by virtual page 1")
	}
	if result.RealPagesFetched != 2 {
		t.Errorf("RealPagesFetched = %d, want 2", result.RealPagesFetched)
	}

	// Virtual page 2 should now be servable entirely from the cache
	// SaveCachedSearchPage wrote during the walk above — no live calls.
	result2, err := ResolveVirtualPage(context.Background(), db, "searxng", "q", "", 2, 20, fetch)
	if err != nil {
		t.Fatalf("ResolveVirtualPage (page 2) returned error: %v", err)
	}
	if *calls != 2 {
		t.Errorf("fetch called %d times after virtual page 2, want still 2 (both real pages already cached)", *calls)
	}
	if len(result2.Results) != 2 {
		t.Fatalf("virtual page 2 got %d results, want 2 (the leftover from real page 2)", len(result2.Results))
	}
	if result2.HasMore {
		t.Error("HasMore = true for virtual page 2, want false — real page 2 itself reported no more")
	}
}

func TestResolveVirtualPage_StopsWalkingOnDegraded(t *testing.T) {
	db := openVirtualPageTestStore(t)
	calls := 0
	fetch := func(ctx context.Context, realPage int) ([]store.CachedSearchResult, bool, bool, []string, error) {
		calls++
		if realPage == 1 {
			// Only 3 results — not enough to fill a virtual page — but
			// degraded=true must still stop the walk rather than trying
			// real page 2.
			return []store.CachedSearchResult{{Title: "1"}, {Title: "2"}, {Title: "3"}}, true, true, []string{"engine1", "engine2"}, nil
		}
		t.Fatalf("fetch called for real page %d, want the walk to have stopped at page 1 (degraded)", realPage)
		return nil, false, false, nil, nil
	}

	result, err := ResolveVirtualPage(context.Background(), db, "searxng", "q", "", 1, 20, fetch)
	if err != nil {
		t.Fatalf("ResolveVirtualPage returned error: %v", err)
	}
	if calls != 1 {
		t.Errorf("fetch called %d times, want 1", calls)
	}
	if !result.Degraded {
		t.Error("Degraded = false, want true")
	}
	if len(result.UnresponsiveEngines) != 2 {
		t.Errorf("UnresponsiveEngines = %v, want the 2 engines from the degraded page", result.UnresponsiveEngines)
	}
}

func TestResolveVirtualPage_FallsBackToStaleCacheOnLiveError(t *testing.T) {
	db := openVirtualPageTestStore(t)
	if err := db.SaveCachedSearchPage("searxng", "q", "", 1, 20, []store.CachedSearchResult{{Title: "stale-but-good", URL: "https://a.com/1"}}, false, false); err != nil {
		t.Fatalf("seeding cache: %v", err)
	}
	// Backdate past cacheTTL (24h) so ResolveVirtualPage treats this
	// entry as stale and attempts a live fetch — which then fails below,
	// exercising the actual fallback-to-stale-cache path rather than the
	// "never even tried fetch" fresh-cache-hit path.
	if err := db.SetCachedSearchPageAgeForTest("searxng", "q", "", 1, 20, 48*time.Hour); err != nil {
		t.Fatalf("backdating cache row: %v", err)
	}

	fetch := func(ctx context.Context, realPage int) ([]store.CachedSearchResult, bool, bool, []string, error) {
		return nil, false, false, nil, context.DeadlineExceeded
	}

	result, err := ResolveVirtualPage(context.Background(), db, "searxng", "q", "", 1, 20, fetch)
	if err != nil {
		t.Fatalf("ResolveVirtualPage returned error: %v, want it to fall back to the stale cache instead", err)
	}
	if len(result.Results) != 1 || result.Results[0].Title != "stale-but-good" {
		t.Errorf("Results = %+v, want the stale cached row served as a fallback", result.Results)
	}
	if !result.CacheHit {
		t.Error("CacheHit = false, want true — served from cache (stale-fallback still counts as a cache hit for observability, not a live call)")
	}
}

func TestResolveVirtualPage_MissingResultsAndLiveErrorReturnsError(t *testing.T) {
	db := openVirtualPageTestStore(t)
	fetch := func(ctx context.Context, realPage int) ([]store.CachedSearchResult, bool, bool, []string, error) {
		return nil, false, false, nil, context.DeadlineExceeded
	}
	if _, err := ResolveVirtualPage(context.Background(), db, "searxng", "q", "", 1, 20, fetch); err == nil {
		t.Fatal("expected an error when there's no cache to fall back to and the live fetch fails")
	}
}

func TestResolveVirtualPage_VirtualPageBeyondCacheReturnsEmptyNotError(t *testing.T) {
	db := openVirtualPageTestStore(t)
	fetch := func(ctx context.Context, realPage int) ([]store.CachedSearchResult, bool, bool, []string, error) {
		if realPage == 1 {
			return []store.CachedSearchResult{{Title: "only one"}}, false, false, nil, nil
		}
		t.Fatalf("fetch called for real page %d, want the walk to stop once real page 1 reports no more", realPage)
		return nil, false, false, nil, nil
	}

	// Virtual page 3 starts at offset 20 — real page 1 has only 1 result
	// and no more, so this should come back empty, not error.
	result, err := ResolveVirtualPage(context.Background(), db, "searxng", "q", "", 3, 20, fetch)
	if err != nil {
		t.Fatalf("ResolveVirtualPage returned error: %v, want an empty result instead", err)
	}
	if len(result.Results) != 0 {
		t.Errorf("Results = %+v, want empty", result.Results)
	}
	if result.HasMore {
		t.Error("HasMore = true, want false")
	}
}
