package search

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

func TestSearch_QueryAndCategoryParams(t *testing.T) {
	var gotQuery, gotCategory string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotQuery = r.URL.Query().Get("q")
		gotCategory = r.URL.Query().Get("categories")
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"query":"golang","results":[]}`))
	}))
	defer srv.Close()

	client := NewSearXNGClient(srv.URL, nil)
	if _, err := client.Search(context.Background(), "golang", 5, "news", 1); err != nil {
		t.Fatalf("Search returned error: %v", err)
	}
	if gotQuery != "golang" {
		t.Errorf("query param = %q, want golang", gotQuery)
	}
	if gotCategory != "news" {
		t.Errorf("categories param = %q, want news", gotCategory)
	}
}

func TestSearch_FusesMultiEnginePositions(t *testing.T) {
	// "single" is ranked #1 by one engine only. "double" is ranked #2 by
	// two engines. Under RRF, agreement across engines should outrank a
	// single engine's #1 pick — this is the whole point of fusing on
	// per-engine rank instead of a raw, non-comparable score.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"results":[
			{"title":"single","url":"https://a.com/1","engine":"brave","engines":["brave"],"positions":[1]},
			{"title":"double","url":"https://b.com/1","engine":"duckduckgo","engines":["duckduckgo","brave"],"positions":[2,2]}
		]}`))
	}))
	defer srv.Close()

	client := NewSearXNGClient(srv.URL, nil)
	resp, err := client.Search(context.Background(), "q", 5, "", 1)
	if err != nil {
		t.Fatalf("Search returned error: %v", err)
	}
	if len(resp.Results) != 2 {
		t.Fatalf("got %d results, want 2", len(resp.Results))
	}
	if resp.Results[0].Title != "double" {
		t.Errorf("Results[0].Title = %q, want %q (two engines agreeing on rank 2 should beat one engine's rank 1)", resp.Results[0].Title, "double")
	}
	if len(resp.Results[0].Engines) != 2 {
		t.Errorf("Results[0].Engines = %v, want 2 engines", resp.Results[0].Engines)
	}
}

func TestSearch_FallsBackToListPositionWhenPositionsOmitted(t *testing.T) {
	// Older SearXNG versions (and hand-built fixtures) may omit "positions"
	// entirely — Search should still rank by SearXNG's own list order
	// rather than erroring or scoring everything identically.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"results":[
			{"title":"first","url":"https://a.com/1"},
			{"title":"second","url":"https://a.com/2"}
		]}`))
	}))
	defer srv.Close()

	client := NewSearXNGClient(srv.URL, nil)
	resp, err := client.Search(context.Background(), "q", 5, "", 1)
	if err != nil {
		t.Fatalf("Search returned error: %v", err)
	}
	if len(resp.Results) != 2 {
		t.Fatalf("got %d results, want 2", len(resp.Results))
	}
	if resp.Results[0].Title != "first" || resp.Results[0].Score <= resp.Results[1].Score {
		t.Errorf("expected %q (list position 1) to outrank %q (list position 2), got scores %v, %v",
			"first", "second", resp.Results[0].Score, resp.Results[1].Score)
	}
}

func TestSearch_TruncatesToMaxResults(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"results":[
			{"title":"1","url":"https://a.com/1"},
			{"title":"2","url":"https://a.com/2"},
			{"title":"3","url":"https://a.com/3"}
		]}`))
	}))
	defer srv.Close()

	client := NewSearXNGClient(srv.URL, nil)
	resp, err := client.Search(context.Background(), "q", 2, "", 1)
	if err != nil {
		t.Fatalf("Search returned error: %v", err)
	}
	if len(resp.Results) != 2 {
		t.Errorf("got %d results, want capped at 2", len(resp.Results))
	}
}

func TestSearch_PageParam(t *testing.T) {
	var gotPageno string
	sawPageno := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sawPageno = r.URL.Query().Has("pageno")
		gotPageno = r.URL.Query().Get("pageno")
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"results":[]}`))
	}))
	defer srv.Close()

	client := NewSearXNGClient(srv.URL, nil)

	// page <= 1 omits &pageno entirely — SearXNG's own default, and the
	// exact request shape every other test above relies on.
	if _, err := client.Search(context.Background(), "q", 5, "", 1); err != nil {
		t.Fatalf("Search returned error: %v", err)
	}
	if sawPageno {
		t.Error("expected no pageno param for page 1")
	}

	if _, err := client.Search(context.Background(), "q", 5, "", 3); err != nil {
		t.Fatalf("Search returned error: %v", err)
	}
	if !sawPageno || gotPageno != "3" {
		t.Errorf("pageno param = %q (present: %v), want \"3\"", gotPageno, sawPageno)
	}
}

func TestSearch_NoCategoryOmitsParam(t *testing.T) {
	var sawCategories bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sawCategories = r.URL.Query().Has("categories")
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"results":[]}`))
	}))
	defer srv.Close()

	client := NewSearXNGClient(srv.URL, nil)
	if _, err := client.Search(context.Background(), "q", 5, "", 1); err != nil {
		t.Fatalf("Search returned error: %v", err)
	}
	if sawCategories {
		t.Error("expected no categories param when category is empty")
	}
}

func TestSearch_NonOKStatus(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte("boom"))
	}))
	defer srv.Close()

	client := NewSearXNGClient(srv.URL, nil)
	if _, err := client.Search(context.Background(), "q", 5, "", 1); err == nil {
		t.Fatal("expected an error for a 500 response")
	}
}

func TestSearch_FiltersBlockedDomainsBeforeTruncating(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"results":[
			{"title":"blocked","url":"https://grokipedia.com/page/1"},
			{"title":"blocked subdomain","url":"https://www.grokipedia.com/page/2"},
			{"title":"good 1","url":"https://a.com/1"},
			{"title":"good 2","url":"https://b.com/1"}
		]}`))
	}))
	defer srv.Close()

	bl, err := LoadBlocklist(writeBlocklistFile(t, "grokipedia.com\n"))
	if err != nil {
		t.Fatalf("LoadBlocklist returned error: %v", err)
	}

	client := NewSearXNGClient(srv.URL, bl)
	resp, err := client.Search(context.Background(), "q", 2, "", 1)
	if err != nil {
		t.Fatalf("Search returned error: %v", err)
	}
	if len(resp.Results) != 2 {
		t.Fatalf("got %d results, want 2 (both blocked results skipped, not counted toward maxResults)", len(resp.Results))
	}
	for _, r := range resp.Results {
		if strings.Contains(r.URL, "grokipedia.com") {
			t.Errorf("blocked URL %q leaked into results", r.URL)
		}
	}
}

func TestSearch_DegradedWhenAllGeneralCategoryEnginesUnresponsive(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"query":"q","results":[],"unresponsive_engines":[
			["brave","Suspended: too many requests"],
			["duckduckgo","CAPTCHA"],
			["google cse","Suspended: too many requests"],
			["startpage","Suspended: CAPTCHA"]
		]}`))
	}))
	defer srv.Close()

	client := NewSearXNGClient(srv.URL, nil)
	resp, err := client.Search(context.Background(), "q", 5, "", 1)
	if err != nil {
		t.Fatalf("Search returned error: %v", err)
	}
	if !resp.Degraded {
		t.Error("Degraded = false, want true (zero results, every known general-category engine unresponsive)")
	}
	want := []string{"brave", "duckduckgo", "google cse", "startpage"}
	if !slicesEqual(resp.UnresponsiveEngines, want) {
		t.Errorf("UnresponsiveEngines = %v, want %v", resp.UnresponsiveEngines, want)
	}
}

func TestSearch_NotDegradedWhenOnlySomeEnginesUnresponsive(t *testing.T) {
	// The actual point of the >= generalCategoryEngineCount check: 2 of 4
	// engines down with the other 2 legitimately finding nothing for this
	// query is a normal empty result, not an outage — must not burn a
	// Tavily fallback credit on it.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"query":"q","results":[],"unresponsive_engines":[
			["brave","Suspended: too many requests"],
			["duckduckgo","CAPTCHA"]
		]}`))
	}))
	defer srv.Close()

	client := NewSearXNGClient(srv.URL, nil)
	resp, err := client.Search(context.Background(), "q", 5, "", 1)
	if err != nil {
		t.Fatalf("Search returned error: %v", err)
	}
	if resp.Degraded {
		t.Error("Degraded = true, want false (only 2 of 4 engines down, not a full outage)")
	}
	// Still reported for logging even though it didn't cross the
	// Degraded threshold.
	if want := []string{"brave", "duckduckgo"}; !slicesEqual(resp.UnresponsiveEngines, want) {
		t.Errorf("UnresponsiveEngines = %v, want %v", resp.UnresponsiveEngines, want)
	}
}

func TestSearch_NotDegradedForNonGeneralCategoryEvenIfUnresponsive(t *testing.T) {
	// No known engine count exists for categories other than "general" —
	// see Search's own comment on why Degraded just never fires for them
	// rather than guessing.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"query":"q","results":[],"unresponsive_engines":[
			["google news","Suspended: too many requests"],
			["bing news","Suspended: too many requests"]
		]}`))
	}))
	defer srv.Close()

	client := NewSearXNGClient(srv.URL, nil)
	resp, err := client.Search(context.Background(), "q", 5, "news", 1)
	if err != nil {
		t.Fatalf("Search returned error: %v", err)
	}
	if resp.Degraded {
		t.Error("Degraded = true, want false (category != general has no known engine count to compare against)")
	}
}

func TestSearch_NotDegradedWhenResultsPresentDespiteUnresponsiveEngines(t *testing.T) {
	// Some engines down doesn't mean the search failed — the ones that
	// answered may have been enough. Degraded is specifically "zero
	// results AND something's reporting failure", not "anything's down".
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"query":"q","results":[
			{"title":"fine","url":"https://a.com/1","score":1.0}
		],"unresponsive_engines":[["brave","Suspended: too many requests"]]}`))
	}))
	defer srv.Close()

	client := NewSearXNGClient(srv.URL, nil)
	resp, err := client.Search(context.Background(), "q", 5, "", 1)
	if err != nil {
		t.Fatalf("Search returned error: %v", err)
	}
	if resp.Degraded {
		t.Error("Degraded = true, want false (results still came back)")
	}
}

func TestSearch_NotDegradedWhenGenuinelyEmpty(t *testing.T) {
	// Zero results with no unresponsive engines at all is a real "nothing
	// found for this query" — must not be mistaken for an outage.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"query":"q","results":[]}`))
	}))
	defer srv.Close()

	client := NewSearXNGClient(srv.URL, nil)
	resp, err := client.Search(context.Background(), "q", 5, "", 1)
	if err != nil {
		t.Fatalf("Search returned error: %v", err)
	}
	if resp.Degraded {
		t.Error("Degraded = true, want false (genuinely empty, not an outage)")
	}
}

func slicesEqual(a, b []string) bool {
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

func TestSearch_EntersCooldownAfterFullOutageAndSkipsSubsequentRequests(t *testing.T) {
	var requestCount int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestCount++
		w.Header().Set("Content-Type", "application/json")
		// Every request from this server looks like a full outage — the
		// point is proving the *second* Search call never reaches here at
		// all, not exercising a real recovery.
		w.Write([]byte(`{"query":"q","results":[],"unresponsive_engines":[
			["brave","Suspended: too many requests"],
			["duckduckgo","CAPTCHA"],
			["google cse","Suspended: too many requests"],
			["startpage","Suspended: CAPTCHA"]
		]}`))
	}))
	defer srv.Close()

	client := NewSearXNGClient(srv.URL, nil)

	first, err := client.Search(context.Background(), "q", 5, "", 1)
	if err != nil {
		t.Fatalf("first Search returned error: %v", err)
	}
	if !first.Degraded {
		t.Fatal("first response Degraded = false, want true (sets up the cooldown this test is actually checking)")
	}
	if requestCount != 1 {
		t.Fatalf("requestCount after first Search = %d, want 1", requestCount)
	}

	second, err := client.Search(context.Background(), "q", 5, "", 1)
	if err != nil {
		t.Fatalf("second Search returned error: %v", err)
	}
	if requestCount != 1 {
		t.Errorf("requestCount after second Search = %d, want still 1 (cooldown should skip the network call entirely)", requestCount)
	}
	if !second.Degraded {
		t.Error("second response Degraded = false, want true (served from cooldown short-circuit)")
	}
	if second.RetryAfter.IsZero() {
		t.Error("second response RetryAfter is zero, want the cooldown's expiry time set")
	}
}

func TestSearch_CooldownDoesNotAffectAFreshClient(t *testing.T) {
	// Sanity check that the cooldown is per-client state, not some
	// process-wide flag that would leak between independent SearXNGClient
	// instances (e.g. in tests, or if the app ever constructed more than
	// one).
	outage := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"query":"q","results":[],"unresponsive_engines":[
			["brave","x"],["duckduckgo","x"],["google cse","x"],["startpage","x"]
		]}`))
	}))
	defer outage.Close()
	outageClient := NewSearXNGClient(outage.URL, nil)
	if resp, err := outageClient.Search(context.Background(), "q", 5, "", 1); err != nil || !resp.Degraded {
		t.Fatalf("setting up the outaged client failed: resp=%+v err=%v", resp, err)
	}

	healthy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"query":"q","results":[{"title":"fine","url":"https://a.com/1","score":1.0}]}`))
	}))
	defer healthy.Close()
	healthyClient := NewSearXNGClient(healthy.URL, nil)

	resp, err := healthyClient.Search(context.Background(), "q", 5, "", 1)
	if err != nil {
		t.Fatalf("healthy client Search returned error: %v", err)
	}
	if resp.Degraded {
		t.Error("a fresh client's Degraded = true, want false — cooldown must not leak across client instances")
	}
	if len(resp.Results) != 1 {
		t.Errorf("got %d results, want 1", len(resp.Results))
	}
}

func TestSearch_DomainRankingBlockExcludesResult(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"results":[
			{"title":"blocked","url":"https://spam.example/1","positions":[1]},
			{"title":"fine","url":"https://a.com/1","positions":[2]}
		]}`))
	}))
	defer srv.Close()

	client := NewSearXNGClient(srv.URL, nil).
		WithDomainRankings(writeRankingsFile(t, "spam.example: block\n"))
	resp, err := client.Search(context.Background(), "q", 5, "", 1)
	if err != nil {
		t.Fatalf("Search returned error: %v", err)
	}
	if len(resp.Results) != 1 || resp.Results[0].Title != "fine" {
		t.Fatalf("got %+v, want only the non-blocked result", resp.Results)
	}
}

func TestSearch_DomainRankingPinForcesToTop(t *testing.T) {
	// "pinned" would rank last by fused score alone (position 5, no other
	// engine agreement) — pin must override that entirely.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"results":[
			{"title":"top-ranked","url":"https://a.com/1","positions":[1,1]},
			{"title":"pinned","url":"https://pin.example/1","positions":[5]}
		]}`))
	}))
	defer srv.Close()

	client := NewSearXNGClient(srv.URL, nil).
		WithDomainRankings(writeRankingsFile(t, "pin.example: pin\n"))
	resp, err := client.Search(context.Background(), "q", 5, "", 1)
	if err != nil {
		t.Fatalf("Search returned error: %v", err)
	}
	if len(resp.Results) != 2 || resp.Results[0].Title != "pinned" {
		t.Fatalf("got %+v, want pinned result first despite its lower fused score", resp.Results)
	}
	if !resp.Results[0].Pinned {
		t.Error("Results[0].Pinned = false, want true")
	}
}

func TestSearch_DomainRankingRaiseCanOutrankHigherPosition(t *testing.T) {
	// "boosted" starts behind "ahead" purely on fused rank, but a strong
	// enough raise multiplier should be able to flip that ordering — this
	// is the actual point of raise/lower existing at all.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"results":[
			{"title":"ahead","url":"https://a.com/1","positions":[1]},
			{"title":"boosted","url":"https://boost.example/1","positions":[3]}
		]}`))
	}))
	defer srv.Close()

	client := NewSearXNGClient(srv.URL, nil).
		WithDomainRankings(writeRankingsFile(t, "boost.example: raise\n"))
	resp, err := client.Search(context.Background(), "q", 5, "", 1)
	if err != nil {
		t.Fatalf("Search returned error: %v", err)
	}
	if resp.Results[0].Title != "boosted" {
		t.Errorf("Results[0].Title = %q, want %q (raiseMultiplier should have flipped the ordering)", resp.Results[0].Title, "boosted")
	}
	if resp.Results[0].RankState != string(RankRaise) {
		t.Errorf("Results[0].RankState = %q, want %q", resp.Results[0].RankState, RankRaise)
	}
}

func writeBlocklistFile(t *testing.T, contents string) string {
	t.Helper()
	path := t.TempDir() + "/blocked_sources.txt"
	if err := os.WriteFile(path, []byte(contents), 0o644); err != nil {
		t.Fatalf("writing blocklist file: %v", err)
	}
	return path
}
