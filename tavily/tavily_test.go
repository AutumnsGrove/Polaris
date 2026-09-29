package tavily

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestSearchRecent_TimeRangeField(t *testing.T) {
	var body map[string]interface{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body = nil
		json.NewDecoder(r.Body).Decode(&body)
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"results":[]}`))
	}))
	defer srv.Close()
	client := NewClientForTest("k", srv.URL)

	if _, err := client.SearchRecent(context.Background(), "q", 5, []string{"a.com"}, "month"); err != nil {
		t.Fatalf("SearchRecent: %v", err)
	}
	if body["time_range"] != "month" {
		t.Errorf("time_range = %v, want \"month\"", body["time_range"])
	}
	if _, ok := body["include_domains"]; !ok {
		t.Error("include_domains was dropped — recency must not displace domains")
	}

	if _, err := client.Search(context.Background(), "q", 5, nil); err != nil {
		t.Fatalf("Search: %v", err)
	}
	if _, ok := body["time_range"]; ok {
		t.Errorf("expected no time_range when recency is unset, got %v", body["time_range"])
	}
}
