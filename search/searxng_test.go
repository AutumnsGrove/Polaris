package search

import (
	"context"
	"net/http"
	"net/http/httptest"
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

	client := NewSearXNGClient(srv.URL)
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

	client := NewSearXNGClient(srv.URL)
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

	client := NewSearXNGClient(srv.URL)
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

	client := NewSearXNGClient(srv.URL)
	if _, err := client.Search(context.Background(), "q", 5, ""); err == nil {
		t.Fatal("expected an error for a 500 response")
	}
}
