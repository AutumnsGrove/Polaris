package tools

import (
	"strconv"
	"strings"
	"testing"
)

// Deezer returns the parent album's cover for a track, so several recommended
// tracks share one image — they must still get distinct pool numbers.
func TestAddRecommendationCandidate_SharedCoverKeepsDistinctNumbers(t *testing.T) {
	ctx := newTestContext()
	a := ctx.AddRecommendationCandidate(Card{Title: "Track A", URL: "https://last.fm/a", ImageURL: "https://cdn/album.jpg"})
	b := ctx.AddRecommendationCandidate(Card{Title: "Track B", URL: "https://last.fm/b", ImageURL: "https://cdn/album.jpg"})
	again := ctx.AddRecommendationCandidate(Card{Title: "Track A", URL: "https://last.fm/a", ImageURL: "https://cdn/album.jpg"})
	if a != 1 || b != 2 || again != 1 {
		t.Errorf("numbers = %d, %d, %d; want 1, 2, 1 (same URL re-surfaces, same image doesn't)", a, b, again)
	}
}

// A recommendation with no cover is a real candidate, not a gap.
func TestImageCandidate_CoverlessRecommendationResolves(t *testing.T) {
	ctx := newTestContext()
	n := ctx.AddRecommendationCandidate(Card{Title: "Obscure Book", URL: "https://hardcover.app/books/x"})
	if _, ok := ctx.ImageCandidate(n); !ok {
		t.Errorf("ImageCandidate(%d) not ok, want a coverless recommendation to resolve", n)
	}
	ctx.SeedImageCandidates([]Card{{}, {Title: "kept", URL: "https://example.com/k"}})
	if _, ok := ctx.ImageCandidate(1); ok {
		t.Error("a zero-value placeholder must still read as a gap")
	}
}

func TestHandleShow_CoverlessRecommendationPointsAtHighlight(t *testing.T) {
	ctx := newTestContext()
	n := ctx.AddRecommendationCandidate(Card{Title: "Obscure Book", URL: "https://hardcover.app/books/x"})
	result := handleShow(`{"image_indices":[`+strconv.Itoa(n)+`]}`, ctx, "call-1")
	if !strings.HasPrefix(result, "error:") || !strings.Contains(result, "highlight") {
		t.Errorf("result = %q, want an error pointing at highlight", result)
	}
}

func TestHandleHighlight_RecommendationCandidateRoundTrip(t *testing.T) {
	ctx := newTestContext()
	n := ctx.AddRecommendationCandidate(Card{Title: "Dune", Subtitle: "Frank Herbert", URL: "https://hardcover.app/books/dune",
		ImageURL: "https://cdn/dune-m.jpg", FullImageURL: "https://cdn/dune-l.jpg"})
	result := handleHighlight(`{"items":[{"image_index":`+strconv.Itoa(n)+`,"why":"desert politics"}]}`, ctx, "call-1")
	if strings.HasPrefix(result, "error:") {
		t.Fatalf("result = %q, want success", result)
	}
	if len(ctx.Cards) != 1 || ctx.Cards[0].Title != "Dune" || ctx.Cards[0].ImageURL != "https://cdn/dune-l.jpg" {
		t.Errorf("Cards = %+v, want a highlight card built from the recommendation", ctx.Cards)
	}
}

func TestWithRecommendationsFooter(t *testing.T) {
	if got := withRecommendationsFooter("(none found)", nil); got != "(none found)" {
		t.Errorf("empty result got a footer: %q", got)
	}
	got := withRecommendationsFooter("1. A", []int{1})
	if !strings.Contains(got, "NOT seen") || !strings.Contains(got, "highlight") || !strings.Contains(got, "show") {
		t.Errorf("footer = %q, want the display instruction", got)
	}
}

func TestLargeBookCoverURL(t *testing.T) {
	if got := largeBookCoverURL("https://covers.openlibrary.org/b/id/1-M.jpg"); got != "https://covers.openlibrary.org/b/id/1-L.jpg" {
		t.Errorf("got %q", got)
	}
	if got := largeBookCoverURL("https://assets.hardcover.app/x.jpg"); got != "https://assets.hardcover.app/x.jpg" {
		t.Errorf("got %q, want passthrough", got)
	}
}
