package tools

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"polaris/brave"
	"polaris/search"
)

// stubThumbnailProbe replaces the real reachability probe for a test, so the
// fixture URLs (example.com) are never dialed. dead maps a thumbnail URL to
// "do not load".
func stubThumbnailProbe(t *testing.T, dead map[string]bool) {
	t.Helper()
	orig := probeThumbnail
	probeThumbnail = func(_ context.Context, raw string) bool { return !dead[raw] }
	t.Cleanup(func() { probeThumbnail = orig })
}

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

// fakeBraveImages serves one Brave image result per entry in titles.
func fakeBraveImages(t *testing.T, titles ...string) (srv *httptest.Server, hits *int32) {
	t.Helper()
	if len(titles) == 0 {
		titles = []string{"From Brave Images"}
	}
	var count int32
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&count, 1)
		results := make([]map[string]interface{}, 0, len(titles))
		for i, title := range titles {
			slug := strings.ToLower(strings.ReplaceAll(title, " ", "-"))
			results = append(results, map[string]interface{}{
				"title":      title,
				"url":        "https://example.com/" + slug + "-page",
				"source":     "example.com",
				"thumbnail":  map[string]interface{}{"src": "https://example.com/" + slug + "-thumb.jpg"},
				"properties": map[string]interface{}{"url": "https://example.com/" + slug + "-full.jpg"},
				"_i":         i,
			})
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{"results": results})
	}))
	t.Cleanup(srv.Close)
	return srv, &count
}

// braveContext wires a Context whose only provider is Brave.
func braveContext(t *testing.T, braveSrv string, used int) (*Context, *int) {
	t.Helper()
	incremented := 0
	return &Context{
		Ctx:                 context.Background(),
		Brave:               brave.NewClientForTest("test-key", braveSrv),
		BraveUsageThisMonth: func() (int, error) { return used, nil },
		IncrementBraveUsage: func() error { incremented++; return nil },
		Emit:                func(string, map[string]interface{}) {},
	}, &incremented
}

// Brave is image_search's primary provider — the whole point of the order
// flip, so it must be the one answering an ordinary call.
func TestHandleImageSearch_BraveIsPrimary(t *testing.T) {
	braveSrv, braveHits := fakeBraveImages(t)
	ctx, incremented := braveContext(t, braveSrv.URL, 0)
	// A SearXNG instance that must never be reached on the happy path.
	ctx.SearXNG = search.NewSearXNGClient(fakeSearXNGImages(t, []map[string]interface{}{
		{"title": "Should never appear", "url": "https://example.com/searxng-page", "thumbnail": "https://example.com/sx-thumb.jpg"},
	}).URL, nil)

	result := handleImageSearch(`{"query":"curtain bang shag","attach_gallery":true}`, ctx, "test-call")

	if !strings.Contains(result, "[via Brave]") {
		t.Errorf("result = %q, want a provider tag naming Brave", result)
	}
	if *braveHits != 1 {
		t.Errorf("brave hits = %d, want 1", *braveHits)
	}
	if *incremented != 1 {
		t.Errorf("IncrementBraveUsage called %d times, want 1", *incremented)
	}
	if len(ctx.Cards) != 1 {
		t.Fatalf("Cards = %+v, want 1 card", ctx.Cards)
	}
	card := ctx.Cards[0]
	if card.Kind != "image" {
		t.Errorf("Kind = %q, want %q", card.Kind, "image")
	}
	if card.Title != "From Brave Images" || card.ImageURL != "https://example.com/from-brave-images-thumb.jpg" {
		t.Errorf("card = %+v, want the Brave result mapped through", card)
	}
	if card.FullImageURL != "https://example.com/from-brave-images-full.jpg" {
		t.Errorf("FullImageURL = %q, want Brave's properties.url", card.FullImageURL)
	}
	if card.Subtitle != "example.com" {
		t.Errorf("Subtitle = %q, want the source domain", card.Subtitle)
	}
}

func TestHandleImageSearch_QueryRequired(t *testing.T) {
	ctx := newTestContext()
	result := handleImageSearch(`{}`, ctx, "test-call")
	if !strings.HasPrefix(result, "error:") {
		t.Errorf("result = %q, want an error", result)
	}
}

// Brave being unconfigured is the dev/no-key case, not an outage: SearXNG is
// still free and already wired, so it answers instead.
func TestHandleImageSearch_NoBraveFallsBackToSearXNG(t *testing.T) {
	stubThumbnailProbe(t, nil)
	srv := fakeSearXNGImages(t, []map[string]interface{}{
		{"title": "A curtain-bang shag", "url": "https://example.com/photo-page", "thumbnail": "https://example.com/thumb.jpg",
			"img_src": "https://example.com/full-res.jpg", "content": ""},
	})
	ctx := &Context{
		Ctx:     context.Background(),
		SearXNG: search.NewSearXNGClient(srv.URL, nil),
		Emit:    func(string, map[string]interface{}) {},
	}

	result := handleImageSearch(`{"query":"curtain bang shag"}`, ctx, "test-call")

	if !strings.Contains(result, "[via SearXNG]") {
		t.Errorf("result = %q, want a provider tag naming SearXNG", result)
	}
	if len(ctx.ImageCandidates) != 1 {
		t.Fatalf("ImageCandidates = %+v, want 1", ctx.ImageCandidates)
	}
	if got := ctx.ImageCandidates[0].FullImageURL; got != "https://example.com/full-res.jpg" {
		t.Errorf("FullImageURL = %q, want SearXNG's img_src", got)
	}
	if len(ctx.Cards) != 0 {
		t.Errorf("Cards = %+v, want none — the default is judge-first", ctx.Cards)
	}
}

// The reserve exists so image search keeps a provider when web_search has
// spent its share: Brave's own cap is still MonthlyCap, not
// WebSearchCeiling.
func TestHandleImageSearch_ImageCallsSurviveTheWebSearchCeiling(t *testing.T) {
	braveSrv, braveHits := fakeBraveImages(t)
	ctx, _ := braveContext(t, braveSrv.URL, brave.WebSearchCeiling)

	result := handleImageSearch(`{"query":"curtain bang shag"}`, ctx, "test-call")

	if !strings.Contains(result, "[via Brave]") {
		t.Errorf("result = %q, want Brave to still answer inside the image reserve", result)
	}
	if *braveHits != 1 {
		t.Errorf("brave hits = %d, want 1", *braveHits)
	}
}

func TestHandleImageSearch_NoResultsIsNotDegraded(t *testing.T) {
	stubThumbnailProbe(t, nil)
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
// fakeDegradedSearXNG server — the only category search.SearXNGClient can
// self-detect a full outage for. That trips the shared, instance-wide
// cooldown, which every later call on the same client inherits, so the
// fallback's own degraded branch can be exercised.
func tripCooldown(t *testing.T, client *search.SearXNGClient) {
	t.Helper()
	if _, err := client.Search(context.Background(), "trigger", 5, "", 1); err != nil {
		t.Fatalf("priming the cooldown failed: %v", err)
	}
}

func TestHandleImageSearch_DegradedWithoutAnyProviderReturnsDegradedMessage(t *testing.T) {
	searxngClient := search.NewSearXNGClient(fakeDegradedSearXNG(t).URL, nil)
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

func TestHandleImageSearch_NoProviderConfiguredReturnsDegradedMessage(t *testing.T) {
	result := handleImageSearch(`{"query":"anything"}`, newTestContext(), "test-call")
	if !strings.Contains(result, "degraded") || !strings.Contains(result, "no image provider is configured") {
		t.Errorf("result = %q, want a degraded message naming the missing providers", result)
	}
}

// Brave's cap is still the paid ceiling: past it, image_search drops to the
// free fallback rather than billing the card on file.
func TestHandleImageSearch_BraveAtCapFallsBackToSearXNG(t *testing.T) {
	braveSrv, braveHits := fakeBraveImages(t)
	stubThumbnailProbe(t, nil)
	srv := fakeSearXNGImages(t, []map[string]interface{}{
		{"title": "Fallback photo", "url": "https://example.com/fb-page", "thumbnail": "https://example.com/fb-thumb.jpg"},
	})
	ctx, _ := braveContext(t, braveSrv.URL, brave.MonthlyCap)
	ctx.SearXNG = search.NewSearXNGClient(srv.URL, nil)

	result := handleImageSearch(`{"query":"how to brew cold green tea at home"}`, ctx, "test-call")

	if !strings.Contains(result, "[via SearXNG]") {
		t.Errorf("result = %q, want the SearXNG fallback to answer", result)
	}
	if *braveHits != 0 {
		t.Errorf("brave hits = %d, want 0 — must not call Brave once the monthly cap is reached", *braveHits)
	}
	if len(ctx.ImageCandidates) != 1 || ctx.ImageCandidates[0].Title != "Fallback photo" {
		t.Fatalf("ImageCandidates = %+v, want the SearXNG fallback result", ctx.ImageCandidates)
	}
}

// SearXNG hotlinks source-page thumbnails and a large share of them are dead;
// shipping those to the model costs a view_image call each to discover.
func TestHandleImageSearch_DropsUnreachableThumbnails(t *testing.T) {
	stubThumbnailProbe(t, map[string]bool{
		"https://example.com/broken.jpg":      true,
		"https://example.com/also-broken.jpg": true,
	})
	srv := fakeSearXNGImages(t, []map[string]interface{}{
		{"title": "Broken", "url": "https://example.com/a", "thumbnail": "https://example.com/broken.jpg"},
		{"title": "Good", "url": "https://example.com/b", "thumbnail": "https://example.com/good.jpg"},
		{"title": "Also broken", "url": "https://example.com/c", "thumbnail": "https://example.com/also-broken.jpg"},
	})
	ctx := &Context{
		Ctx:     context.Background(),
		SearXNG: search.NewSearXNGClient(srv.URL, nil),
		Emit:    func(string, map[string]interface{}) {},
	}

	result := handleImageSearch(`{"query":"curtain bang shag"}`, ctx, "test-call")

	if len(ctx.ImageCandidates) != 1 {
		t.Fatalf("ImageCandidates = %+v, want only the reachable one", ctx.ImageCandidates)
	}
	if !strings.Contains(result, "1 image(s)") {
		t.Errorf("result = %q, want it to report exactly the one surviving image", result)
	}
	if ctx.ImageCandidates[0].Title != "Good" {
		t.Errorf("candidate = %+v, want the reachable result", ctx.ImageCandidates[0])
	}
}

// The core of issue #124: by default a search only fills the candidate pool
// — nothing reaches ctx.Cards (what the frontend renders), and the model is
// told the user hasn't seen anything.
func TestHandleImageSearch_DefaultDoesNotAttachGallery(t *testing.T) {
	braveSrv, _ := fakeBraveImages(t, "First", "Second")
	ctx, _ := braveContext(t, braveSrv.URL, 0)
	var emitted map[string]interface{}
	ctx.Emit = func(event string, payload map[string]interface{}) {
		if event == "tool_result" {
			emitted = payload
		}
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
