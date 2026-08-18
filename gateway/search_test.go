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
