package brave

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestNewClient_EmptyKeyReturnsNil(t *testing.T) {
	if c := NewClient(""); c != nil {
		t.Errorf("NewClient(\"\") = %+v, want nil", c)
	}
}

func TestSearch_RequestShape(t *testing.T) {
	var gotHeader, gotQuery, gotCount, gotOffset string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotHeader = r.Header.Get("X-Subscription-Token")
		gotQuery = r.URL.Query().Get("q")
		gotCount = r.URL.Query().Get("count")
		gotOffset = r.URL.Query().Get("offset")
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"query": map[string]interface{}{"more_results_available": false},
			"web":   map[string]interface{}{"results": []interface{}{}},
		})
	}))
	defer srv.Close()

	client := NewClientForTest("test-key", srv.URL)
	if _, err := client.Search(context.Background(), "cold brew tea", 2); err != nil {
		t.Fatalf("Search returned error: %v", err)
	}
	if gotHeader != "test-key" {
		t.Errorf("X-Subscription-Token = %q, want %q", gotHeader, "test-key")
	}
	if gotQuery != "cold brew tea" {
		t.Errorf("q = %q, want %q", gotQuery, "cold brew tea")
	}
	if gotCount != "20" {
		t.Errorf("count = %q, want %q (Brave's own max, always requested)", gotCount, "20")
	}
	if gotOffset != "2" {
		t.Errorf("offset = %q, want %q", gotOffset, "2")
	}
}

func TestSearch_ClampsOffsetToValidRange(t *testing.T) {
	var gotOffset string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotOffset = r.URL.Query().Get("offset")
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"query": map[string]interface{}{"more_results_available": false},
			"web":   map[string]interface{}{"results": []interface{}{}},
		})
	}))
	defer srv.Close()

	client := NewClientForTest("test-key", srv.URL)

	if _, err := client.Search(context.Background(), "q", -5); err != nil {
		t.Fatalf("Search returned error: %v", err)
	}
	if gotOffset != "0" {
		t.Errorf("offset for negative input = %q, want clamped to %q", gotOffset, "0")
	}

	if _, err := client.Search(context.Background(), "q", 99); err != nil {
		t.Fatalf("Search returned error: %v", err)
	}
	if gotOffset != "9" {
		t.Errorf("offset for out-of-range input = %q, want clamped to Brave's max %q", gotOffset, "9")
	}
}

func TestSearch_ParsesResultsAndSnippets(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{
			"query": {"more_results_available": true},
			"web": {"results": [
				{"title": "Cold Brew Green Tea", "url": "https://example.com/1", "description": "Steep overnight.", "extra_snippets": ["Use filtered water.", "Chill for 12 hours."]},
				{"title": "No Snippets Result", "url": "https://example.com/2", "description": "Just a description."}
			]}
		}`))
	}))
	defer srv.Close()

	client := NewClientForTest("test-key", srv.URL)
	resp, err := client.Search(context.Background(), "q", 0)
	if err != nil {
		t.Fatalf("Search returned error: %v", err)
	}
	if !resp.MoreAvailable {
		t.Error("MoreAvailable = false, want true")
	}
	if len(resp.Results) != 2 {
		t.Fatalf("got %d results, want 2", len(resp.Results))
	}
	first := resp.Results[0]
	if first.Title != "Cold Brew Green Tea" || first.URL != "https://example.com/1" {
		t.Errorf("first result = %+v, unexpected title/url", first)
	}
	wantContent := "Steep overnight.\n\nUse filtered water.\n\nChill for 12 hours."
	if first.Content != wantContent {
		t.Errorf("first.Content = %q, want %q", first.Content, wantContent)
	}
	if resp.Results[1].Content != "Just a description." {
		t.Errorf("second.Content = %q, want just the description (no extra_snippets)", resp.Results[1].Content)
	}
}

func TestSearch_NonOKStatus(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		w.Write([]byte("invalid subscription token"))
	}))
	defer srv.Close()

	client := NewClientForTest("bad-key", srv.URL)
	if _, err := client.Search(context.Background(), "q", 0); err == nil {
		t.Fatal("expected an error for a 401 response")
	}
}
