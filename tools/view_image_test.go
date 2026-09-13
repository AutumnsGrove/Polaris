package tools

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"polaris/search"
)

// fakeImageServer serves a tiny fixed byte payload with an image Content-Type
// — real image bytes aren't needed since handleViewImage never decodes the
// image itself, only base64-encodes and forwards it.
func fakeImageServer(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/png")
		w.Write([]byte("fake-png-bytes"))
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestHandleViewImage_MissingCardIndex(t *testing.T) {
	ctx := newTestContext()
	result := handleViewImage(`{}`, ctx, "call-1")
	if !strings.Contains(result, "card_index is required") {
		t.Errorf("result = %q, want a card_index-required error", result)
	}
}

func TestHandleViewImage_PathNotYetSupported(t *testing.T) {
	ctx := newTestContext()
	result := handleViewImage(`{"card_index":1,"path":"dataset.csv"}`, ctx, "call-1")
	if !strings.Contains(result, "isn't supported yet") {
		t.Errorf("result = %q, want a not-yet-supported error for path", result)
	}
}

func TestHandleViewImage_CardIndexOutOfRange(t *testing.T) {
	ctx := newTestContext()
	ctx.AddCard(Card{Title: "one", URL: "https://example.com/1", ImageURL: "https://example.com/1.png", Kind: "image"})
	result := handleViewImage(`{"card_index":5}`, ctx, "call-1")
	if !strings.Contains(result, "out of range") {
		t.Errorf("result = %q, want an out-of-range error", result)
	}
}

func TestHandleViewImage_BlockedSourceRejectedWithoutFetching(t *testing.T) {
	fetched := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fetched = true
		w.Header().Set("Content-Type", "image/png")
		w.Write([]byte("fake-png-bytes"))
	}))
	t.Cleanup(srv.Close)

	parsedURL, err := url.Parse(srv.URL)
	if err != nil {
		t.Fatalf("parsing test server URL: %v", err)
	}
	bl, err := search.LoadBlocklist(writeBlocklistFile(t, parsedURL.Hostname()+"\n"))
	if err != nil {
		t.Fatalf("LoadBlocklist returned error: %v", err)
	}

	ctx := newTestContext()
	ctx.Blocklist = bl
	ctx.AddCard(Card{Title: "one", URL: "https://example.com/1", FullImageURL: srv.URL, Kind: "image"})

	result := handleViewImage(`{"card_index":1}`, ctx, "call-1")
	if !strings.Contains(result, "blocked") {
		t.Errorf("result = %q, want a blocked-source error", result)
	}
	if fetched {
		t.Error("handleViewImage fetched a blocklisted image URL instead of rejecting it up front")
	}
}

func TestHandleViewImage_UnknownMode(t *testing.T) {
	ctx := newTestContext()
	ctx.AddCard(Card{Title: "one", URL: "https://example.com/1", ImageURL: "https://example.com/1.png", Kind: "image"})
	result := handleViewImage(`{"card_index":1,"mode":"stare"}`, ctx, "call-1")
	if !strings.Contains(result, `unknown mode "stare"`) {
		t.Errorf("result = %q, want an unknown-mode error", result)
	}
}

func TestHandleViewImage_SeeRejectedWhenNotMultimodal(t *testing.T) {
	ctx := newTestContext()
	ctx.Multimodal = false
	ctx.AddCard(Card{Title: "one", URL: "https://example.com/1", ImageURL: "https://example.com/1.png", Kind: "image"})
	result := handleViewImage(`{"card_index":1,"mode":"see"}`, ctx, "call-1")
	if !strings.Contains(result, `only available to a multimodal model`) {
		t.Errorf("result = %q, want a not-multimodal error", result)
	}
	if len(ctx.PendingImageMessages) != 0 {
		t.Error("PendingImageMessages should stay empty when see mode is rejected")
	}
}

func TestHandleViewImage_DescribeMode(t *testing.T) {
	srv := fakeImageServer(t)
	ctx := newTestContext()
	ctx.AddCard(Card{Title: "a red bicycle", URL: "https://example.com/1", FullImageURL: srv.URL, Kind: "image"})

	var gotInstructions string
	ctx.DescribeImage = func(_ context.Context, imageBase64, mimeType, instructions string) (string, float64, error) {
		gotInstructions = instructions
		if mimeType != "image/png" {
			t.Errorf("mimeType = %q, want image/png", mimeType)
		}
		if imageBase64 == "" {
			t.Error("imageBase64 was empty")
		}
		return "a red bicycle leaning against a wall", 0.001, nil
	}

	result := handleViewImage(`{"card_index":1,"instructions":"the frame color"}`, ctx, "call-1")
	if result != "a red bicycle leaning against a wall" {
		t.Errorf("result = %q, want the description text", result)
	}
	if gotInstructions != "the frame color" {
		t.Errorf("instructions passed to DescribeImage = %q, want %q", gotInstructions, "the frame color")
	}
	if ctx.ExtraCostUSD != 0.001 {
		t.Errorf("ExtraCostUSD = %v, want 0.001 (DescribeImage's cost should be recorded)", ctx.ExtraCostUSD)
	}
}

func TestHandleViewImage_DescribeModeNoVisionConfigured(t *testing.T) {
	srv := fakeImageServer(t)
	ctx := newTestContext()
	ctx.AddCard(Card{Title: "one", URL: "https://example.com/1", FullImageURL: srv.URL, Kind: "image"})
	// ctx.DescribeImage left nil — no multimodal model configured at all.
	result := handleViewImage(`{"card_index":1}`, ctx, "call-1")
	if !strings.Contains(result, "no multimodal model is configured") {
		t.Errorf("result = %q, want a no-vision-model error", result)
	}
}

func TestHandleViewImage_SeeMode(t *testing.T) {
	srv := fakeImageServer(t)
	ctx := newTestContext()
	ctx.Multimodal = true
	ctx.AddCard(Card{Title: "a red bicycle", URL: "https://example.com/1", FullImageURL: srv.URL, Kind: "image"})

	result := handleViewImage(`{"card_index":1,"mode":"see"}`, ctx, "call-1")
	if !strings.Contains(result, "now viewing card 1") {
		t.Errorf("result = %q, want an acknowledgement mentioning card 1", result)
	}

	pending := ctx.FlushPendingImageMessages()
	if len(pending) != 1 {
		t.Fatalf("PendingImageMessages has %d entries, want 1", len(pending))
	}
	msg := pending[0]
	if msg.Role != "user" {
		t.Errorf("pending message role = %q, want %q", msg.Role, "user")
	}
	if len(msg.ImageURLs) != 1 || !strings.HasPrefix(msg.ImageURLs[0], "data:image/png;base64,") {
		t.Errorf("pending message ImageURLs = %v, want one data:image/png;base64,... URL", msg.ImageURLs)
	}
}

func TestHandleViewImage_FullImageURLFallsBackToImageURL(t *testing.T) {
	srv := fakeImageServer(t)
	ctx := newTestContext()
	ctx.DescribeImage = func(context.Context, string, string, string) (string, float64, error) {
		return "described", 0, nil
	}
	// No FullImageURL set — handleViewImage should fall back to ImageURL.
	ctx.AddCard(Card{Title: "one", URL: "https://example.com/1", ImageURL: srv.URL, Kind: "image"})
	result := handleViewImage(`{"card_index":1}`, ctx, "call-1")
	if result != "described" {
		t.Errorf("result = %q, want the description (ImageURL fallback should have worked)", result)
	}
}
