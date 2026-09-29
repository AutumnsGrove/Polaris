package parallel

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestAfterDate(t *testing.T) {
	now := time.Date(2026, 9, 29, 23, 30, 0, 0, time.UTC)
	cases := map[string]string{
		"day":   "2026-09-28",
		"week":  "2026-09-22",
		"month": "2026-08-30",
		"year":  "2025-09-29",
		"":      "",
		"bogus": "",
	}
	for recency, want := range cases {
		if got := afterDate(now, recency); got != want {
			t.Errorf("afterDate(%q) = %q, want %q", recency, got, want)
		}
	}
}

// TestSearchRecent_SourcePolicyShape pins where after_date goes: nested at
// advanced_settings.source_policy (a top-level source_policy is rejected by
// the real API), and absent entirely when recency is unset.
func TestSearchRecent_SourcePolicyShape(t *testing.T) {
	var body struct {
		AdvancedSettings struct {
			MaxResults   int `json:"max_results"`
			SourcePolicy *struct {
				AfterDate string `json:"after_date"`
			} `json:"source_policy"`
		} `json:"advanced_settings"`
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body.AdvancedSettings.SourcePolicy = nil
		json.NewDecoder(r.Body).Decode(&body)
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"results":[]}`))
	}))
	defer srv.Close()
	client := NewClientForTest("k", srv.URL)

	if _, err := client.SearchRecent(context.Background(), "q", 5, "week"); err != nil {
		t.Fatalf("SearchRecent: %v", err)
	}
	want := afterDate(time.Now(), "week")
	if body.AdvancedSettings.SourcePolicy == nil || body.AdvancedSettings.SourcePolicy.AfterDate != want {
		t.Errorf("source_policy = %+v, want after_date %q", body.AdvancedSettings.SourcePolicy, want)
	}
	if body.AdvancedSettings.MaxResults != 5 {
		t.Errorf("max_results = %d, want 5 (recency must not clobber it)", body.AdvancedSettings.MaxResults)
	}

	if _, err := client.Search(context.Background(), "q", 5); err != nil {
		t.Fatalf("Search: %v", err)
	}
	if body.AdvancedSettings.SourcePolicy != nil {
		t.Errorf("expected no source_policy when recency is unset, got %+v", body.AdvancedSettings.SourcePolicy)
	}
}
