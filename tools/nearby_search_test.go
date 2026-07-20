package tools

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"polaris/search"
)

func TestHandleNearbySearch_QueryRequired(t *testing.T) {
	ctx := &Context{Ctx: context.Background(), Emit: func(string, map[string]interface{}) {}}
	result := handleNearbySearch(`{}`, ctx)
	if result == "" || result[:6] != "error:" {
		t.Errorf("result = %q, want a query-required error", result)
	}
}

func TestHandleNearbySearch_NoLocationAndNoDefault(t *testing.T) {
	ctx := &Context{Ctx: context.Background(), Emit: func(string, map[string]interface{}) {}}
	result := handleNearbySearch(`{"query":"coffee shop"}`, ctx)
	if result == "" || result[:6] != "error:" {
		t.Errorf("result = %q, want an error for a missing location with no default configured", result)
	}
}

func TestHandleNearbySearch_UsesDefaultLocationWhenOmitted(t *testing.T) {
	// Coordinate-pair "location" skips the Nominatim network call
	// entirely (see places.Geocode), so this test needs no fake geocoder.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"results":[]}`))
	}))
	defer srv.Close()

	ctx := &Context{
		Ctx:             context.Background(),
		DefaultLocation: "47.6062, -122.3321",
		SearXNG:         search.NewSearXNGClient(srv.URL),
		Emit:            func(string, map[string]interface{}) {},
	}
	result := handleNearbySearch(`{"query":"coffee shop"}`, ctx)
	if result == "" {
		t.Error("expected some result using the default location, got empty string")
	}
}

func TestHandleNearbySearch_FoursquareNotConfigured_FallsBackToSearXNG(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"results":[{"title":"Best Coffee Shop","url":"https://example.com/1","content":"great coffee","score":9}]}`))
	}))
	defer srv.Close()

	ctx := &Context{
		Ctx:     context.Background(),
		SearXNG: search.NewSearXNGClient(srv.URL),
		Emit:    func(string, map[string]interface{}) {},
	}
	// Foursquare left nil — no API key configured.
	result := handleNearbySearch(`{"query":"coffee shop","location":"47.6062, -122.3321"}`, ctx)
	if result == "" {
		t.Fatal("expected a formatted result from the SearXNG fallback")
	}
	if len(ctx.Citations) != 1 || ctx.Citations[0].URL != "https://example.com/1" {
		t.Errorf("Citations = %+v, want the SearXNG result added", ctx.Citations)
	}
}

// The Foursquare-configured path (ctx.Foursquare non-nil) isn't
// separately covered here: places.FoursquareClient's fields are
// unexported, so a fake baseURL can't be injected from outside that
// package — and places.TestSearchNearby already exercises that HTTP
// interaction directly and thoroughly. What's specific to
// handleNearbySearch (parsing args, the SearXNG fallback, citations) is
// covered by the tests above using the always-nil-Foursquare path,
// which is also this app's most common real configuration (see
// config.yaml.example: Foursquare is optional).
