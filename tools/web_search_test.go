package tools

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"polaris/parallel"
	"polaris/search"
	"polaris/tavily"
)

// fakeDegradedSearXNG serves a zero-results, all-engines-unresponsive
// response — the shape that should trigger the Tavily fallback / degraded
// message path, not the plain "no results found" one.
func fakeDegradedSearXNG(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"query":   r.URL.Query().Get("q"),
			"results": []map[string]interface{}{},
			// All 4 of the general category's known engines (see
			// search.generalCategoryEngineCount) — a real full outage,
			// not just one or two engines having a bad moment.
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

// fakeSearXNG serves canned SearXNG-shaped JSON, recording the query
// params it was asked with so tests can assert on what handleWebSearch
// actually sent.
func fakeSearXNG(t *testing.T, results []map[string]interface{}) (*httptest.Server, *http.Request) {
	t.Helper()
	var lastReq *http.Request
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		lastReq = r.Clone(r.Context())
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{"query": r.URL.Query().Get("q"), "results": results})
	}))
	t.Cleanup(srv.Close)
	return srv, lastReq
}

func TestHandleWebSearch_FormatsResultsAndAddsCitations(t *testing.T) {
	srv, _ := fakeSearXNG(t, []map[string]interface{}{
		{"title": "Go 1.24 released", "url": "https://go.dev/blog/go1.24", "content": "New release", "score": 8.0},
	})
	ctx := &Context{
		Ctx:     context.Background(),
		SearXNG: search.NewSearXNGClient(srv.URL, nil),
		Emit:    func(string, map[string]interface{}) {},
	}

	result := handleWebSearch(`{"query":"golang release"}`, ctx)
	if result == "" || result == "no results found" {
		t.Fatalf("result = %q, want formatted results", result)
	}
	if len(ctx.Citations) != 1 || ctx.Citations[0].URL != "https://go.dev/blog/go1.24" {
		t.Errorf("Citations = %+v, want the one result added", ctx.Citations)
	}
}

func TestHandleWebSearch_NoResults(t *testing.T) {
	srv, _ := fakeSearXNG(t, nil)
	ctx := &Context{
		Ctx:     context.Background(),
		SearXNG: search.NewSearXNGClient(srv.URL, nil),
		Emit:    func(string, map[string]interface{}) {},
	}

	result := handleWebSearch(`{"query":"something obscure"}`, ctx)
	if result != "no results found" {
		t.Errorf("result = %q, want %q", result, "no results found")
	}
	if len(ctx.Citations) != 0 {
		t.Errorf("Citations = %+v, want none", ctx.Citations)
	}
}

func TestHandleWebSearch_DegradedWithoutTavilyReturnsDistinctMessage(t *testing.T) {
	srv := fakeDegradedSearXNG(t)
	ctx := &Context{
		Ctx:     context.Background(),
		SearXNG: search.NewSearXNGClient(srv.URL, nil),
		Tavily:  nil, // not configured — must fall through to the degraded message, not a fallback
		Emit:    func(string, map[string]interface{}) {},
	}

	result := handleWebSearch(`{"query":"how to brew cold green tea at home"}`, ctx)

	if result == "no results found" {
		t.Error("result = \"no results found\" — a degraded SearXNG response must not be reported as a confirmed empty result")
	}
	if !strings.Contains(result, "degraded") {
		t.Errorf("result = %q, want it to mention the search being degraded", result)
	}
	if !strings.Contains(result, "brave") || !strings.Contains(result, "duckduckgo") {
		t.Errorf("result = %q, want the unresponsive engines named", result)
	}
}

func TestHandleWebSearch_DegradedFallsBackToTavily(t *testing.T) {
	searxngSrv := fakeDegradedSearXNG(t)

	tavilySrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/search" {
			t.Errorf("tavily path = %s, want /search", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"query": "how to brew cold green tea at home",
			"results": []map[string]interface{}{
				{"title": "Cold Brew Green Tea", "url": "https://example.com/cold-brew", "content": "Steep overnight in the fridge.", "score": 0.9},
			},
		})
	}))
	t.Cleanup(tavilySrv.Close)

	ctx := &Context{
		Ctx:     context.Background(),
		SearXNG: search.NewSearXNGClient(searxngSrv.URL, nil),
		Tavily:  tavily.NewClientForTest("test-key", tavilySrv.URL),
		Emit:    func(string, map[string]interface{}) {},
	}

	result := handleWebSearch(`{"query":"how to brew cold green tea at home"}`, ctx)

	if !strings.Contains(result, "Cold Brew Green Tea") || !strings.Contains(result, "example.com/cold-brew") {
		t.Errorf("result = %q, want the Tavily fallback result formatted in", result)
	}
	if len(ctx.Citations) != 1 || ctx.Citations[0].URL != "https://example.com/cold-brew" {
		t.Errorf("Citations = %+v, want the Tavily result's URL added", ctx.Citations)
	}
}

func TestHandleWebSearch_DegradedTavilyAlsoFailsReturnsDegradedMessage(t *testing.T) {
	searxngSrv := fakeDegradedSearXNG(t)
	tavilySrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	t.Cleanup(tavilySrv.Close)

	ctx := &Context{
		Ctx:     context.Background(),
		SearXNG: search.NewSearXNGClient(searxngSrv.URL, nil),
		Tavily:  tavily.NewClientForTest("test-key", tavilySrv.URL),
		Emit:    func(string, map[string]interface{}) {},
	}

	result := handleWebSearch(`{"query":"how to brew cold green tea at home"}`, ctx)

	if !strings.Contains(result, "degraded") {
		t.Errorf("result = %q, want the degraded message when both SearXNG and the Tavily fallback fail", result)
	}
}

// fakeParallel serves a Parallel-Search-API-shaped response with one
// result, recording how many times it was hit.
func fakeParallel(t *testing.T) (srv *httptest.Server, hits *int) {
	t.Helper()
	count := 0
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		count++
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"search_id": "search_test",
			"results": []map[string]interface{}{
				{"url": "https://example.com/parallel-result", "title": "From Parallel", "excerpts": []string{"steep overnight"}},
			},
		})
	}))
	t.Cleanup(srv.Close)
	return srv, &count
}

func TestHandleWebSearch_DegradedPrefersParallelOverTavily(t *testing.T) {
	searxngSrv := fakeDegradedSearXNG(t)
	parallelSrv, parallelHits := fakeParallel(t)

	tavilyHit := false
	tavilySrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		tavilyHit = true
		w.WriteHeader(http.StatusInternalServerError)
	}))
	t.Cleanup(tavilySrv.Close)

	var incremented int
	ctx := &Context{
		Ctx:                    context.Background(),
		SearXNG:                search.NewSearXNGClient(searxngSrv.URL, nil),
		Parallel:               parallel.NewClientForTest("test-key", parallelSrv.URL),
		ParallelUsageThisMonth: func() (int, error) { return 0, nil },
		IncrementParallelUsage: func() error { incremented++; return nil },
		Tavily:                 tavily.NewClientForTest("test-key", tavilySrv.URL),
		Emit:                   func(string, map[string]interface{}) {},
	}

	result := handleWebSearch(`{"query":"how to brew cold green tea at home"}`, ctx)

	if !strings.Contains(result, "From Parallel") || !strings.Contains(result, "example.com/parallel-result") {
		t.Errorf("result = %q, want the Parallel fallback result formatted in", result)
	}
	if *parallelHits != 1 {
		t.Errorf("parallel hits = %d, want 1", *parallelHits)
	}
	if tavilyHit {
		t.Error("tavily was hit, want it skipped — Parallel succeeded first and is preferred")
	}
	if incremented != 1 {
		t.Errorf("IncrementParallelUsage called %d times, want 1", incremented)
	}
	if len(ctx.Citations) != 1 || ctx.Citations[0].URL != "https://example.com/parallel-result" {
		t.Errorf("Citations = %+v, want the Parallel result's URL added", ctx.Citations)
	}
}

func TestHandleWebSearch_DegradedSkipsParallelWhenMonthlyCapReached(t *testing.T) {
	searxngSrv := fakeDegradedSearXNG(t)
	parallelSrv, parallelHits := fakeParallel(t)

	tavilySrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"query": "q",
			"results": []map[string]interface{}{
				{"title": "From Tavily", "url": "https://example.com/tavily-result", "content": "c", "score": 0.5},
			},
		})
	}))
	t.Cleanup(tavilySrv.Close)

	ctx := &Context{
		Ctx:                    context.Background(),
		SearXNG:                search.NewSearXNGClient(searxngSrv.URL, nil),
		Parallel:               parallel.NewClientForTest("test-key", parallelSrv.URL),
		ParallelUsageThisMonth: func() (int, error) { return parallelMonthlyCap, nil }, // already at the cap
		IncrementParallelUsage: func() error {
			t.Error("IncrementParallelUsage must not be called when the cap gates Parallel out")
			return nil
		},
		Tavily: tavily.NewClientForTest("test-key", tavilySrv.URL),
		Emit:   func(string, map[string]interface{}) {},
	}

	result := handleWebSearch(`{"query":"how to brew cold green tea at home"}`, ctx)

	if !strings.Contains(result, "From Tavily") {
		t.Errorf("result = %q, want the Tavily fallback result — Parallel should have been skipped at the cap", result)
	}
	if *parallelHits != 0 {
		t.Errorf("parallel hits = %d, want 0 — must not call Parallel once the monthly cap is reached", *parallelHits)
	}
}

func TestHandleWebSearch_DegradedFallsBackToTavilyWhenParallelErrors(t *testing.T) {
	searxngSrv := fakeDegradedSearXNG(t)
	parallelSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	t.Cleanup(parallelSrv.Close)

	tavilySrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"query": "q",
			"results": []map[string]interface{}{
				{"title": "From Tavily", "url": "https://example.com/tavily-result", "content": "c", "score": 0.5},
			},
		})
	}))
	t.Cleanup(tavilySrv.Close)

	var incremented int
	ctx := &Context{
		Ctx:                    context.Background(),
		SearXNG:                search.NewSearXNGClient(searxngSrv.URL, nil),
		Parallel:               parallel.NewClientForTest("test-key", parallelSrv.URL),
		ParallelUsageThisMonth: func() (int, error) { return 0, nil },
		IncrementParallelUsage: func() error { incremented++; return nil },
		Tavily:                 tavily.NewClientForTest("test-key", tavilySrv.URL),
		Emit:                   func(string, map[string]interface{}) {},
	}

	result := handleWebSearch(`{"query":"how to brew cold green tea at home"}`, ctx)

	if !strings.Contains(result, "From Tavily") {
		t.Errorf("result = %q, want the Tavily fallback result when Parallel itself errors", result)
	}
	if incremented != 0 {
		t.Errorf("IncrementParallelUsage called %d times, want 0 — a failed Parallel request never reached its own billing", incremented)
	}
}

func TestHandleWebSearch_QueryRequired(t *testing.T) {
	ctx := &Context{Ctx: context.Background(), Emit: func(string, map[string]interface{}) {}}
	result := handleWebSearch(`{}`, ctx)
	if result != "error: query is required" {
		t.Errorf("result = %q, want the query-required error", result)
	}
}

func TestHandleWebSearch_InvalidJSON(t *testing.T) {
	ctx := &Context{Ctx: context.Background(), Emit: func(string, map[string]interface{}) {}}
	result := handleWebSearch(`not json`, ctx)
	if result == "" {
		t.Error("expected an error result for invalid JSON")
	}
}
