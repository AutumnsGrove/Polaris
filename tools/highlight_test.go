package tools

import (
	"strings"
	"testing"
)

// highlight's image_index fills title/url/image_url from an image_search
// candidate (issue #124), so the model can put a picked search result on
// screen without retyping a URL the candidate list never showed it.
func TestHandleHighlight_ImageIndexFillsFromCandidate(t *testing.T) {
	ctx := newTestContext()
	ctx.AddImageCandidate(Card{Title: "Red bike", URL: "https://example.com/page", ImageURL: "https://example.com/t.jpg",
		FullImageURL: "https://example.com/full.jpg", Kind: "image"})

	result := handleHighlight(`{"items":[{"image_index":1,"why":"closest match"}]}`, ctx, "call-1")

	if strings.HasPrefix(result, "error:") {
		t.Fatalf("result = %q, want success", result)
	}
	if len(ctx.Cards) != 1 {
		t.Fatalf("Cards = %+v, want 1", ctx.Cards)
	}
	card := ctx.Cards[0]
	if card.Title != "Red bike" || card.URL != "https://example.com/page" || card.ImageURL != "https://example.com/full.jpg" ||
		card.Kind != "highlight" || card.Why != "closest match" {
		t.Errorf("card = %+v, want fields filled from the candidate as a highlight card", card)
	}
}

func TestHandleHighlight_ExplicitFieldsOverrideImageIndex(t *testing.T) {
	ctx := newTestContext()
	ctx.AddImageCandidate(Card{Title: "Red bike", URL: "https://example.com/page", ImageURL: "https://example.com/t.jpg"})
	handleHighlight(`{"items":[{"image_index":1,"title":"My title"}]}`, ctx, "call-1")
	if len(ctx.Cards) != 1 || ctx.Cards[0].Title != "My title" || ctx.Cards[0].URL != "https://example.com/page" {
		t.Errorf("Cards = %+v, want the explicit title kept and the url filled in", ctx.Cards)
	}
}

func TestHandleHighlight_ImageIndexOutOfRange(t *testing.T) {
	result := handleHighlight(`{"items":[{"image_index":2}]}`, newTestContext(), "call-1")
	if !strings.Contains(result, "out of range") {
		t.Errorf("result = %q, want an out-of-range error", result)
	}
}
