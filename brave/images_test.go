package brave

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestSearchImages_RequestShapeAndParsing(t *testing.T) {
	var gotHeader, gotQuery, gotCount, gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotHeader = r.Header.Get("X-Subscription-Token")
		gotQuery = r.URL.Query().Get("q")
		gotCount = r.URL.Query().Get("count")
		gotPath = r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"results": []map[string]interface{}{
				{
					"title":     "A curtain-bang shag",
					"url":       "https://example.com/photo-page",
					"source":    "example.com",
					"thumbnail": map[string]interface{}{"src": "https://example.com/thumb.jpg"},
				},
			},
		})
	}))
	defer srv.Close()

	client := NewClientForTest("test-key", srv.URL)
	resp, err := client.SearchImages(context.Background(), "curtain bang shag", 10)
	if err != nil {
		t.Fatalf("SearchImages returned error: %v", err)
	}
	if gotHeader != "test-key" {
		t.Errorf("X-Subscription-Token = %q, want %q", gotHeader, "test-key")
	}
	if gotQuery != "curtain bang shag" {
		t.Errorf("q = %q, want %q", gotQuery, "curtain bang shag")
	}
	if gotCount != "10" {
		t.Errorf("count = %q, want %q", gotCount, "10")
	}
	if gotPath != "/images" {
		t.Errorf("path = %q, want the distinct images sub-path", gotPath)
	}
	if len(resp.Results) != 1 {
		t.Fatalf("got %d results, want 1", len(resp.Results))
	}
	r := resp.Results[0]
	if r.Title != "A curtain-bang shag" || r.URL != "https://example.com/photo-page" ||
		r.ImageSrc != "https://example.com/thumb.jpg" || r.Source != "example.com" {
		t.Errorf("result = %+v, want the parsed fields from the fake response", r)
	}
}

func TestSearchImages_FallsBackToPropertiesURLWhenThumbnailEmpty(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"results": []map[string]interface{}{
				{
					"title":      "No thumbnail",
					"url":        "https://example.com/page",
					"properties": map[string]interface{}{"url": "https://example.com/full.jpg"},
				},
			},
		})
	}))
	defer srv.Close()

	client := NewClientForTest("test-key", srv.URL)
	resp, err := client.SearchImages(context.Background(), "q", 0)
	if err != nil {
		t.Fatalf("SearchImages returned error: %v", err)
	}
	if resp.Results[0].ImageSrc != "https://example.com/full.jpg" {
		t.Errorf("ImageSrc = %q, want the properties.url fallback", resp.Results[0].ImageSrc)
	}
}

func TestSearchImages_DerivesSourceFromURLWhenMissing(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"results": []map[string]interface{}{
				{
					"title":     "No source field",
					"url":       "https://sub.example.com/page",
					"thumbnail": map[string]interface{}{"src": "https://example.com/thumb.jpg"},
				},
			},
		})
	}))
	defer srv.Close()

	client := NewClientForTest("test-key", srv.URL)
	resp, err := client.SearchImages(context.Background(), "q", 0)
	if err != nil {
		t.Fatalf("SearchImages returned error: %v", err)
	}
	if resp.Results[0].Source != "sub.example.com" {
		t.Errorf("Source = %q, want the hostname parsed from url", resp.Results[0].Source)
	}
}

func TestSearchImages_DefaultAndClampedCount(t *testing.T) {
	var gotCount string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotCount = r.URL.Query().Get("count")
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{"results": []map[string]interface{}{}})
	}))
	defer srv.Close()
	client := NewClientForTest("test-key", srv.URL)

	if _, err := client.SearchImages(context.Background(), "q", 0); err != nil {
		t.Fatalf("SearchImages returned error: %v", err)
	}
	if gotCount != "10" {
		t.Errorf("count = %q, want the default of 10 when count <= 0", gotCount)
	}

	if _, err := client.SearchImages(context.Background(), "q", 500); err != nil {
		t.Fatalf("SearchImages returned error: %v", err)
	}
	if gotCount != "200" {
		t.Errorf("count = %q, want clamped to Brave's own 200 ceiling", gotCount)
	}
}
