package search

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

func TestSearch_ParsesResultsAndNormalizesScore(t *testing.T) {
	var gotQuery, gotCategory string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotQuery = r.URL.Query().Get("q")
		gotCategory = r.URL.Query().Get("categories")
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"query":"golang","results":[
			{"title":"Go Blog","url":"https://go.dev/blog","content":"News","score":15.0,"thumbnail":""},
			{"title":"Go Docs","url":"https://go.dev/doc","content":"Docs","score":5.0,"thumbnail":""}
		]}`))
	}))
	defer srv.Close()

	client := NewSearXNGClient(srv.URL, nil)
	resp, err := client.Search(context.Background(), "golang", 5, "news")
	if err != nil {
		t.Fatalf("Search returned error: %v", err)
	}
	if gotQuery != "golang" {
		t.Errorf("query param = %q, want golang", gotQuery)
	}
	if gotCategory != "news" {
		t.Errorf("categories param = %q, want news", gotCategory)
	}
	if len(resp.Results) != 2 {
		t.Fatalf("got %d results, want 2", len(resp.Results))
	}
	// score 15.0 / 10.0 = 1.5, clamped to 1.0.
	if resp.Results[0].Score != 1.0 {
		t.Errorf("Results[0].Score = %v, want clamped to 1.0", resp.Results[0].Score)
	}
	if resp.Results[1].Score != 0.5 {
		t.Errorf("Results[1].Score = %v, want 0.5", resp.Results[1].Score)
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
	resp, err := client.Search(context.Background(), "q", 2, "")
	if err != nil {
		t.Fatalf("Search returned error: %v", err)
	}
	if len(resp.Results) != 2 {
		t.Errorf("got %d results, want capped at 2", len(resp.Results))
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
	if _, err := client.Search(context.Background(), "q", 5, ""); err != nil {
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
	if _, err := client.Search(context.Background(), "q", 5, ""); err == nil {
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
	resp, err := client.Search(context.Background(), "q", 2, "")
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

func TestSearch_DegradedWhenEmptyWithUnresponsiveEngines(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"query":"q","results":[],"unresponsive_engines":[
			["brave","Suspended: too many requests"],
			["duckduckgo","CAPTCHA"]
		]}`))
	}))
	defer srv.Close()

	client := NewSearXNGClient(srv.URL, nil)
	resp, err := client.Search(context.Background(), "q", 5, "")
	if err != nil {
		t.Fatalf("Search returned error: %v", err)
	}
	if !resp.Degraded {
		t.Error("Degraded = false, want true (zero results, engines unresponsive)")
	}
	if want := []string{"brave", "duckduckgo"}; !slicesEqual(resp.UnresponsiveEngines, want) {
		t.Errorf("UnresponsiveEngines = %v, want %v", resp.UnresponsiveEngines, want)
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
	resp, err := client.Search(context.Background(), "q", 5, "")
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
	resp, err := client.Search(context.Background(), "q", 5, "")
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

func writeBlocklistFile(t *testing.T, contents string) string {
	t.Helper()
	path := t.TempDir() + "/blocked_sources.txt"
	if err := os.WriteFile(path, []byte(contents), 0o644); err != nil {
		t.Fatalf("writing blocklist file: %v", err)
	}
	return path
}
