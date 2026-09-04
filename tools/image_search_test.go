package tools

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"polaris/brave"
	"polaris/search"
)

func fakeSearXNGImages(t *testing.T, results []map[string]interface{}) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("categories") != "images" {
			t.Errorf("categories = %q, want images", r.URL.Query().Get("categories"))
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{"query": r.URL.Query().Get("q"), "results": results})
	}))
	t.Cleanup(srv.Close)
	return srv
}

func fakeBraveImages(t *testing.T) (srv *httptest.Server, hits *int) {
	t.Helper()
	count := 0
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		count++
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"results": []map[string]interface{}{
				{
					"title":      "From Brave Images",
					"url":        "https://example.com/brave-photo-page",
					"source":     "example.com",
					"thumbnail":  map[string]interface{}{"src": "https://example.com/brave-thumb.jpg"},
					"properties": map[string]interface{}{"url": "https://example.com/brave-full-res.jpg"},
				},
			},
		})
	}))
	t.Cleanup(srv.Close)
	return srv, &count
}

func TestHandleImageSearch_FormatsResultsAsImageCards(t *testing.T) {
	srv := fakeSearXNGImages(t, []map[string]interface{}{
		{"title": "A curtain-bang shag", "url": "https://example.com/photo-page", "thumbnail": "https://example.com/thumb.jpg",
			"img_src": "https://example.com/full-res.jpg", "content": ""},
	})
	ctx := &Context{
		Ctx:     context.Background(),
		SearXNG: search.NewSearXNGClient(srv.URL, nil),
		Emit:    func(string, map[string]interface{}) {},
	}

	result := handleImageSearch(`{"query":"curtain bang shag"}`, ctx)

	if !strings.Contains(result, "[via SearXNG]") {
		t.Errorf("result = %q, want a provider tag naming SearXNG", result)
	}
	if len(ctx.Cards) != 1 {
		t.Fatalf("Cards = %+v, want 1 card", ctx.Cards)
	}
	card := ctx.Cards[0]
	if card.Kind != "image" {
		t.Errorf("Kind = %q, want %q", card.Kind, "image")
	}
	if card.Title != "A curtain-bang shag" || card.ImageURL != "https://example.com/thumb.jpg" ||
		card.URL != "https://example.com/photo-page" || card.Subtitle != "example.com" {
		t.Errorf("card = %+v, want the SearXNG result mapped through", card)
	}
	if card.FullImageURL != "https://example.com/full-res.jpg" {
		t.Errorf("FullImageURL = %q, want SearXNG's img_src", card.FullImageURL)
	}
}

func TestHandleImageSearch_QueryRequired(t *testing.T) {
	ctx := newTestContext()
	result := handleImageSearch(`{}`, ctx)
	if !strings.HasPrefix(result, "error:") {
		t.Errorf("result = %q, want an error", result)
	}
}

func TestHandleImageSearch_NoResultsNotDegraded(t *testing.T) {
	srv := fakeSearXNGImages(t, nil)
	ctx := &Context{
		Ctx:     context.Background(),
		SearXNG: search.NewSearXNGClient(srv.URL, nil),
		Emit:    func(string, map[string]interface{}) {},
	}

	result := handleImageSearch(`{"query":"something obscure"}`, ctx)
	if result != "no images found" {
		t.Errorf("result = %q, want %q", result, "no images found")
	}
	if len(ctx.Cards) != 0 {
		t.Errorf("Cards = %+v, want none", ctx.Cards)
	}
}

// tripCooldown makes one general-category (category=="") call against a
// fakeDegradedSearXNG server, which is the only category
// search.SearXNGClient can self-detect a full outage for (see
// searxng.go's Degraded computation) — this trips the client's shared,
// instance-wide cooldown, which every subsequent call on the same client
// (any category, including "images") then inherits via the early
// inCooldown() check at the top of Search. That's the real mechanism
// image_search's own fallback rides; a standalone "images" call has no
// way to detect degradation on its own.
func tripCooldown(t *testing.T, client *search.SearXNGClient) {
	t.Helper()
	if _, err := client.Search(context.Background(), "trigger", 5, "", 1); err != nil {
		t.Fatalf("priming the cooldown failed: %v", err)
	}
}

func TestHandleImageSearch_DegradedFallsBackToBrave(t *testing.T) {
	searxngSrv := fakeDegradedSearXNG(t)
	braveSrv, braveHits := fakeBraveImages(t)

	searxngClient := search.NewSearXNGClient(searxngSrv.URL, nil)
	tripCooldown(t, searxngClient)

	var incremented int
	ctx := &Context{
		Ctx:                 context.Background(),
		SearXNG:             searxngClient,
		Brave:               brave.NewClientForTest("test-key", braveSrv.URL),
		BraveUsageThisMonth: func() (int, error) { return 0, nil },
		IncrementBraveUsage: func() error { incremented++; return nil },
		Emit:                func(string, map[string]interface{}) {},
	}

	result := handleImageSearch(`{"query":"how to brew cold green tea at home"}`, ctx)

	if !strings.Contains(result, "[via Brave") {
		t.Errorf("result = %q, want a provider tag naming Brave as the source", result)
	}
	if *braveHits != 1 {
		t.Errorf("brave hits = %d, want 1", *braveHits)
	}
	if incremented != 1 {
		t.Errorf("IncrementBraveUsage called %d times, want 1", incremented)
	}
	if len(ctx.Cards) != 1 || ctx.Cards[0].URL != "https://example.com/brave-photo-page" {
		t.Errorf("Cards = %+v, want the Brave fallback result added", ctx.Cards)
	}
	if ctx.Cards[0].FullImageURL != "https://example.com/brave-full-res.jpg" {
		t.Errorf("FullImageURL = %q, want Brave's properties.url", ctx.Cards[0].FullImageURL)
	}
}

func TestHandleImageSearch_DegradedSkipsBraveWhenMonthlyCapReached(t *testing.T) {
	searxngSrv := fakeDegradedSearXNG(t)
	braveSrv, braveHits := fakeBraveImages(t)

	searxngClient := search.NewSearXNGClient(searxngSrv.URL, nil)
	tripCooldown(t, searxngClient)

	ctx := &Context{
		Ctx:                 context.Background(),
		SearXNG:             searxngClient,
		Brave:               brave.NewClientForTest("test-key", braveSrv.URL),
		BraveUsageThisMonth: func() (int, error) { return brave.MonthlyCap, nil },
		IncrementBraveUsage: func() error {
			t.Error("IncrementBraveUsage must not be called when the cap gates Brave out")
			return nil
		},
		Emit: func(string, map[string]interface{}) {},
	}

	result := handleImageSearch(`{"query":"how to brew cold green tea at home"}`, ctx)

	if !strings.Contains(result, "degraded") {
		t.Errorf("result = %q, want a degraded message", result)
	}
	if *braveHits != 0 {
		t.Errorf("brave hits = %d, want 0 — must not call Brave once the monthly cap is reached", *braveHits)
	}
}

func TestHandleImageSearch_DegradedWithoutBraveReturnsDegradedMessage(t *testing.T) {
	searxngSrv := fakeDegradedSearXNG(t)
	searxngClient := search.NewSearXNGClient(searxngSrv.URL, nil)
	tripCooldown(t, searxngClient)

	ctx := &Context{
		Ctx:     context.Background(),
		SearXNG: searxngClient,
		Emit:    func(string, map[string]interface{}) {},
	}

	result := handleImageSearch(`{"query":"how to brew cold green tea at home"}`, ctx)
	if !strings.Contains(result, "degraded") {
		t.Errorf("result = %q, want it to mention image search being degraded", result)
	}
	if len(ctx.Cards) != 0 {
		t.Errorf("Cards = %+v, want none", ctx.Cards)
	}
}
