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

	result := handleImageSearch(`{"query":"curtain bang shag","attach_gallery":true}`, ctx, "test-call")

	if !strings.Contains(result, "[via SearXNG]") {
		t.Errorf("result = %q, want a provider tag naming SearXNG", result)
	}
	if len(ctx.Cards) != 1 {
		t.Fatalf("Cards = %+v, want 1 card", ctx.Cards)
	}
	if len(ctx.ImageCandidates) != 1 {
		t.Errorf("ImageCandidates = %+v, want the result in the pool too so view_image/show can number it", ctx.ImageCandidates)
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
	result := handleImageSearch(`{}`, ctx, "test-call")
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

	result := handleImageSearch(`{"query":"something obscure"}`, ctx, "test-call")
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

	result := handleImageSearch(`{"query":"how to brew cold green tea at home"}`, ctx, "test-call")

	if !strings.Contains(result, "[via Brave") {
		t.Errorf("result = %q, want a provider tag naming Brave as the source", result)
	}
	if *braveHits != 1 {
		t.Errorf("brave hits = %d, want 1", *braveHits)
	}
	if incremented != 1 {
		t.Errorf("IncrementBraveUsage called %d times, want 1", incremented)
	}
	if len(ctx.ImageCandidates) != 1 || ctx.ImageCandidates[0].URL != "https://example.com/brave-photo-page" {
		t.Fatalf("ImageCandidates = %+v, want the Brave fallback result added", ctx.ImageCandidates)
	}
	if ctx.ImageCandidates[0].FullImageURL != "https://example.com/brave-full-res.jpg" {
		t.Errorf("FullImageURL = %q, want Brave's properties.url", ctx.ImageCandidates[0].FullImageURL)
	}
	if len(ctx.Cards) != 0 {
		t.Errorf("Cards = %+v, want none — the Brave path must also default to judge-first", ctx.Cards)
	}
}

// The core of issue #124: by default a search only fills the candidate pool
// — nothing reaches ctx.Cards (what the frontend renders), and the model is
// told the user hasn't seen anything.
func TestHandleImageSearch_DefaultDoesNotAttachGallery(t *testing.T) {
	srv := fakeSearXNGImages(t, []map[string]interface{}{
		{"title": "First", "url": "https://example.com/a", "thumbnail": "https://example.com/a.jpg", "img_src": "https://example.com/a-full.jpg"},
		{"title": "Second", "url": "https://example.com/b", "thumbnail": "https://example.com/b.jpg", "img_src": "https://example.com/b-full.jpg"},
	})
	var emitted map[string]interface{}
	ctx := &Context{
		Ctx:     context.Background(),
		SearXNG: search.NewSearXNGClient(srv.URL, nil),
		Emit: func(event string, payload map[string]interface{}) {
			if event == "tool_result" {
				emitted = payload
			}
		},
	}

	result := handleImageSearch(`{"query":"anything","attach_gallery":false}`, ctx, "test-call")

	if len(ctx.Cards) != 0 {
		t.Errorf("Cards = %+v, want none: nothing may be displayed before the model judges it", ctx.Cards)
	}
	if len(ctx.ImageCandidates) != 2 {
		t.Fatalf("ImageCandidates = %+v, want both results pooled", ctx.ImageCandidates)
	}
	for _, want := range []string{"NOT seen", "1. First (example.com)", "2. Second (example.com)", "image_indices"} {
		if !strings.Contains(result, want) {
			t.Errorf("result = %q, want it to contain %q", result, want)
		}
	}
	if _, has := emitted["cards"]; has {
		t.Errorf("tool_result carried cards %v; the frontend would render them as a gallery", emitted["cards"])
	}
}

// A second search re-surfacing an already-pooled image must keep its
// original number, so numbers the model was already told stay valid.
func TestAddImageCandidate_DedupsAndKeepsNumbers(t *testing.T) {
	ctx := &Context{}
	a := Card{Title: "a", ImageURL: "https://x/a.jpg", FullImageURL: "https://x/a-full.jpg"}
	b := Card{Title: "b", ImageURL: "https://x/b.jpg"}
	if got := ctx.AddImageCandidate(a); got != 1 {
		t.Errorf("first = %d, want 1", got)
	}
	if got := ctx.AddImageCandidate(b); got != 2 {
		t.Errorf("second = %d, want 2", got)
	}
	if got := ctx.AddImageCandidate(a); got != 1 {
		t.Errorf("re-added a = %d, want its original number 1", got)
	}
	if n := len(ctx.ImageCandidatesSnapshot()); n != 2 {
		t.Errorf("pool size = %d, want 2", n)
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

	result := handleImageSearch(`{"query":"how to brew cold green tea at home"}`, ctx, "test-call")

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

	result := handleImageSearch(`{"query":"how to brew cold green tea at home"}`, ctx, "test-call")
	if !strings.Contains(result, "degraded") {
		t.Errorf("result = %q, want it to mention image search being degraded", result)
	}
	if len(ctx.Cards) != 0 {
		t.Errorf("Cards = %+v, want none", ctx.Cards)
	}
}
