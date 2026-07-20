package tools

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"polaris/search"
)

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
		SearXNG: search.NewSearXNGClient(srv.URL),
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
		SearXNG: search.NewSearXNGClient(srv.URL),
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
