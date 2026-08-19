package gateway

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"polaris/brave"
	"polaris/search"
)

// fakeDegradedSearXNGForSearch serves the shape handleSearch treats as a
// full outage — zero results, every general-category engine
// unresponsive — same fixture shape as tools/web_search_test.go's
// fakeDegradedSearXNG, duplicated here since that one's unexported in a
// different package.
func fakeDegradedSearXNGForSearch(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"query":   r.URL.Query().Get("q"),
			"results": []map[string]interface{}{},
			"unresponsive_engines": [][]string{
				{"brave", "Suspended: too many requests"},
				{"duckduckgo", "CAPTCHA"},
				{"google cse", "Suspended: too many requests"},
				{"startpage", "Suspended: CAPTCHA"},
			},
		})
	}))
	t.Cleanup(srv.Close)
	return srv
}

// fakeBraveForSearch serves a Brave-Web-Search-API-shaped response with
// 15 results (enough to span both halves of one virtual-paging split).
func fakeBraveForSearch(t *testing.T, moreAvailable bool) (srv *httptest.Server, hits *int) {
	t.Helper()
	count := 0
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		count++
		results := make([]map[string]interface{}, 15)
		for i := range results {
			results[i] = map[string]interface{}{
				"title":       "Result",
				"url":         "https://example.com/" + string(rune('a'+i)),
				"description": "snippet",
			}
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"query": map[string]interface{}{"more_results_available": moreAvailable},
			"web":   map[string]interface{}{"results": results},
		})
	}))
	t.Cleanup(srv.Close)
	return srv, &count
}

// fakeHealthySearXNGForSearch serves a fixed set of results, counting
// how many times it was actually hit — for asserting the DB cache
// avoids repeat real requests.
func fakeHealthySearXNGForSearch(t *testing.T, results []map[string]interface{}) (srv *httptest.Server, hits *int) {
	t.Helper()
	count := 0
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		count++
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"query":   r.URL.Query().Get("q"),
			"results": results,
		})
	}))
	t.Cleanup(srv.Close)
	return srv, &count
}

func TestHandleSearch_FallsBackToBraveWhenSearXNGDegraded_FirstHalf(t *testing.T) {
	h := newTestHarness(t, "")
	h.srvObj.searxng = search.NewSearXNGClient(fakeDegradedSearXNGForSearch(t).URL, nil)
	braveSrv, hits := fakeBraveForSearch(t, false)
	h.srvObj.brave = brave.NewClientForTest("test-key", braveSrv.URL)

	resp, err := http.Get(h.url("/api/search?q=cold+brew+tea&page=1&record=0"))
	if err != nil {
		t.Fatalf("GET /api/search: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}

	var got search.SearchResponse
	if err := json.NewDecoder(resp.Body).Decode(&got); err != nil {
		t.Fatalf("decoding response: %v", err)
	}
	if len(got.Results) != 10 {
		t.Fatalf("got %d results, want 10 (first virtual-page half of Brave's 15 raw results)", len(got.Results))
	}
	if got.Results[0].URL != "https://example.com/a" || got.Results[9].URL != "https://example.com/j" {
		t.Errorf("results = %+v, want the first 10 of Brave's raw slice in order", got.Results)
	}
	if !got.HasMore {
		t.Error("HasMore = false, want true — the second half of this same real Brave fetch still has results")
	}
	if *hits != 1 {
		t.Errorf("brave hits = %d, want 1", *hits)
	}
}

func TestHandleSearch_FallsBackToBraveWhenSearXNGDegraded_SecondHalfSameRealFetch(t *testing.T) {
	h := newTestHarness(t, "")
	h.srvObj.searxng = search.NewSearXNGClient(fakeDegradedSearXNGForSearch(t).URL, nil)
	braveSrv, hits := fakeBraveForSearch(t, false)
	h.srvObj.brave = brave.NewClientForTest("test-key", braveSrv.URL)

	resp, err := http.Get(h.url("/api/search?q=cold+brew+tea&page=2&record=0"))
	if err != nil {
		t.Fatalf("GET /api/search: %v", err)
	}
	defer resp.Body.Close()

	var got search.SearchResponse
	if err := json.NewDecoder(resp.Body).Decode(&got); err != nil {
		t.Fatalf("decoding response: %v", err)
	}
	// Brave's 15 raw results split 10/5 across the two virtual-page
	// halves of one real fetch — page 2 gets whatever's left (5), not a
	// full 10, and no second Brave request should have fired for it.
	if len(got.Results) != 5 {
		t.Fatalf("got %d results, want 5 (second half of the same 15-result real fetch)", len(got.Results))
	}
	if got.Results[0].URL != "https://example.com/k" {
		t.Errorf("results[0] = %+v, want the 11th raw result (index 10)", got.Results[0])
	}
	if got.HasMore {
		t.Error("HasMore = true, want false — Brave itself reported no further pages (more_results_available: false)")
	}
	if *hits != 1 {
		t.Errorf("brave hits = %d, want 1", *hits)
	}
}

func TestHandleSearch_SkipsBraveFallbackWhenMonthlyCapReached(t *testing.T) {
	h := newTestHarness(t, "")
	h.srvObj.searxng = search.NewSearXNGClient(fakeDegradedSearXNGForSearch(t).URL, nil)
	braveSrv, hits := fakeBraveForSearch(t, false)
	h.srvObj.brave = brave.NewClientForTest("test-key", braveSrv.URL)

	for i := 0; i < brave.MonthlyCap; i++ {
		if _, err := h.db.IncrementAPIUsage("brave"); err != nil {
			t.Fatalf("seeding brave usage: %v", err)
		}
	}

	resp, err := http.Get(h.url("/api/search?q=cold+brew+tea&page=1&record=0"))
	if err != nil {
		t.Fatalf("GET /api/search: %v", err)
	}
	defer resp.Body.Close()

	var got search.SearchResponse
	if err := json.NewDecoder(resp.Body).Decode(&got); err != nil {
		t.Fatalf("decoding response: %v", err)
	}
	if !got.Degraded {
		t.Error("Degraded = false, want true — the raw degraded SearXNG response should pass through since Brave was skipped at the cap")
	}
	if *hits != 0 {
		t.Errorf("brave hits = %d, want 0 — must not call Brave once the monthly cap is reached", *hits)
	}
}

func TestHandleSearch_CachesRealSearXNGPageAcrossRequests(t *testing.T) {
	h := newTestHarness(t, "")
	srv, hits := fakeHealthySearXNGForSearch(t, []map[string]interface{}{
		{"title": "Go 1.24 released", "url": "https://go.dev/blog/go1.24", "content": "New release"},
	})
	h.srvObj.searxng = search.NewSearXNGClient(srv.URL, nil)

	for i := 0; i < 2; i++ {
		resp, err := http.Get(h.url("/api/search?q=golang+release&page=1&record=0"))
		if err != nil {
			t.Fatalf("GET /api/search (request %d): %v", i, err)
		}
		var got search.SearchResponse
		if err := json.NewDecoder(resp.Body).Decode(&got); err != nil {
			t.Fatalf("decoding response (request %d): %v", i, err)
		}
		resp.Body.Close()
		if len(got.Results) != 1 || got.Results[0].Title != "Go 1.24 released" {
			t.Fatalf("request %d: results = %+v, want the one SearXNG result", i, got.Results)
		}
	}

	if *hits != 1 {
		t.Errorf("searxng hits = %d, want 1 — the second request should have been served from cache", *hits)
	}
}

// TestHandleSearch_VirtualPage2ReusesCachedRealPage1 exercises the actual
// point of the accumulate-until-enough walk: real page 1 exactly
// satisfies virtual page 1 (10 results, VirtualPageSize), and virtual
// page 2 needs real page 2 too — but must reuse real page 1's already-
// cached entry rather than re-fetching it, so answering virtual page 2
// costs exactly one additional real request, not two.
func TestHandleSearch_VirtualPage2ReusesCachedRealPage1(t *testing.T) {
	h := newTestHarness(t, "")
	var page1Hits, page2Hits int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		pageno := r.URL.Query().Get("pageno")
		if pageno == "" || pageno == "1" {
			page1Hits++
			// Exactly 10 raw results with max_results=10 below makes
			// SearXNGClient.Search's own HasMore heuristic (raw count >=
			// maxResults) true, so the walk knows to try real page 2 once
			// virtual page 2 needs more than page 1 alone can offer.
			results := make([]map[string]interface{}, 10)
			for i := range results {
				results[i] = map[string]interface{}{"title": "p1", "url": "https://a.com/p1", "content": "c"}
			}
			json.NewEncoder(w).Encode(map[string]interface{}{"query": "q", "results": results})
			return
		}
		page2Hits++
		json.NewEncoder(w).Encode(map[string]interface{}{"query": "q", "results": []map[string]interface{}{
			{"title": "p2", "url": "https://a.com/p2", "content": "c"},
		}})
	}))
	t.Cleanup(srv.Close)
	h.srvObj.searxng = search.NewSearXNGClient(srv.URL, nil)

	// Virtual page 1 — exactly satisfied by real page 1 alone (10 results
	// == VirtualPageSize).
	resp1, err := http.Get(h.url("/api/search?q=q&page=1&max_results=10&record=0"))
	if err != nil {
		t.Fatalf("GET /api/search page=1: %v", err)
	}
	var got1 search.SearchResponse
	json.NewDecoder(resp1.Body).Decode(&got1)
	resp1.Body.Close()
	if len(got1.Results) != 10 || got1.Results[0].Title != "p1" {
		t.Fatalf("virtual page 1 results = %+v, want 10 p1 results", got1.Results)
	}
	if page1Hits != 1 || page2Hits != 0 {
		t.Fatalf("after virtual page 1: page1Hits=%d page2Hits=%d, want 1, 0", page1Hits, page2Hits)
	}

	// Virtual page 2 — needs real page 2, but must reuse the cached real
	// page 1 rather than re-fetching it.
	resp2, err := http.Get(h.url("/api/search?q=q&page=2&max_results=10&record=0"))
	if err != nil {
		t.Fatalf("GET /api/search page=2: %v", err)
	}
	var got2 search.SearchResponse
	json.NewDecoder(resp2.Body).Decode(&got2)
	resp2.Body.Close()
	if len(got2.Results) != 1 || got2.Results[0].Title != "p2" {
		t.Fatalf("virtual page 2 results = %+v, want 1 p2 result", got2.Results)
	}
	if page1Hits != 1 {
		t.Errorf("page1Hits = %d after virtual page 2, want still 1 (real page 1 must be served from cache, not re-fetched)", page1Hits)
	}
	if page2Hits != 1 {
		t.Errorf("page2Hits = %d, want 1", page2Hits)
	}
}

func TestHandleSetDomainRanking_WritesThroughToTheConfiguredFile(t *testing.T) {
	h := newTestHarness(t, "")

	req, err := http.NewRequest(http.MethodPut, h.url("/api/domain-rankings"), strings.NewReader(`{"domain":"reddit.com","state":"raise"}`))
	if err != nil {
		t.Fatalf("building request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("PUT /api/domain-rankings: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("status = %d, want 204", resp.StatusCode)
	}

	path := h.srvObj.searxng.DomainRankingsPath()
	if path == "" {
		t.Fatal("expected the harness's SearXNGClient to have a domain rankings path configured")
	}
	if got := search.LoadDomainRankings(path).State("https://reddit.com"); got != search.RankRaise {
		t.Errorf("State(reddit.com) = %q, want raise", got)
	}
}

func TestHandleSetDomainRanking_RejectsInvalidState(t *testing.T) {
	h := newTestHarness(t, "")

	req, err := http.NewRequest(http.MethodPut, h.url("/api/domain-rankings"), strings.NewReader(`{"domain":"reddit.com","state":"boost"}`))
	if err != nil {
		t.Fatalf("building request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("PUT /api/domain-rankings: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("status = %d, want 400 for an invalid state", resp.StatusCode)
	}
}

func TestHandleUpdateSearchHistory_NonexistentIDReturns404(t *testing.T) {
	h := newTestHarness(t, "")

	req, err := http.NewRequest(http.MethodPatch, h.url("/api/search-history/999"), strings.NewReader(`{"favorite":true}`))
	if err != nil {
		t.Fatalf("building request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("PATCH /api/search-history/999: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("status = %d, want 404 for a search history id that doesn't exist", resp.StatusCode)
	}
}

func TestHandleSetDomainRanking_RejectsMissingDomain(t *testing.T) {
	h := newTestHarness(t, "")

	req, err := http.NewRequest(http.MethodPut, h.url("/api/domain-rankings"), strings.NewReader(`{"state":"raise"}`))
	if err != nil {
		t.Fatalf("building request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("PUT /api/domain-rankings: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("status = %d, want 400 when domain is missing", resp.StatusCode)
	}
}
