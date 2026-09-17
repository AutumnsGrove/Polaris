package tools

import (
	"errors"
	"strings"
	"testing"

	"polaris/store"
)

func TestHandleStars_SearchFormatsResults(t *testing.T) {
	ctx := newTestContext()
	ctx.StarsSearch = func(query string, limit int) ([]store.Star, error) {
		if query != "coffee" {
			t.Errorf("query = %q, want %q", query, "coffee")
		}
		if limit != 10 {
			t.Errorf("limit = %d, want 10", limit)
		}
		return []store.Star{
			{ID: 7, Title: "Prefers pour-over coffee", Category: "taste", Summary: "Brews pour-over most mornings, dislikes drip machines."},
		}, nil
	}

	result := handleStars(`{"query":"coffee"}`, ctx, "test-call")

	if !strings.Contains(result, "star_id=7") {
		t.Errorf("result = %q, want it to include star_id=7", result)
	}
	if !strings.Contains(result, "Prefers pour-over coffee") {
		t.Errorf("result = %q, want it to include the title", result)
	}
}

func TestHandleStars_SearchNoResults(t *testing.T) {
	ctx := newTestContext()
	ctx.StarsSearch = func(query string, limit int) ([]store.Star, error) { return nil, nil }

	result := handleStars(`{"query":"nothing matches this"}`, ctx, "test-call")
	if result != "no matching stars found" {
		t.Errorf("result = %q, want the no-match message", result)
	}
}

func TestHandleStars_SearchRequiresQuery(t *testing.T) {
	ctx := newTestContext()
	ctx.StarsSearch = func(query string, limit int) ([]store.Star, error) {
		t.Fatal("StarsSearch should not be called with an empty query")
		return nil, nil
	}

	result := handleStars(`{}`, ctx, "test-call")
	if !strings.HasPrefix(result, "error:") {
		t.Errorf("result = %q, want an error for a missing query", result)
	}
}

func TestHandleStars_ReadReturnsFullCard(t *testing.T) {
	ctx := newTestContext()
	ctx.StarsRead = func(starID int64) (*store.Star, error) {
		if starID != 7 {
			t.Errorf("starID = %d, want 7", starID)
		}
		return &store.Star{
			ID: 7, Title: "Prefers pour-over coffee", Category: "taste",
			Summary: "Brews pour-over most mornings.", Body: "Full body text about coffee habits.",
			Tags: []string{"coffee", "mornings"},
		}, nil
	}

	result := handleStars(`{"star_id":7}`, ctx, "test-call")
	if !strings.Contains(result, "Full body text about coffee habits.") {
		t.Errorf("result = %q, want the full body", result)
	}
	if !strings.Contains(result, "coffee, mornings") {
		t.Errorf("result = %q, want the tags", result)
	}
}

func TestHandleStars_ReadUnknownStarReturnsError(t *testing.T) {
	ctx := newTestContext()
	ctx.StarsRead = func(starID int64) (*store.Star, error) { return nil, store.ErrStarNotFound }

	result := handleStars(`{"star_id":999}`, ctx, "test-call")
	if !strings.Contains(result, "no star with id 999") {
		t.Errorf("result = %q, want a not-found error naming the id", result)
	}
}

func TestHandleStars_ReadPropagatesOtherErrors(t *testing.T) {
	ctx := newTestContext()
	ctx.StarsRead = func(starID int64) (*store.Star, error) { return nil, errors.New("db exploded") }

	result := handleStars(`{"star_id":7}`, ctx, "test-call")
	if !strings.Contains(result, "db exploded") {
		t.Errorf("result = %q, want the underlying error surfaced", result)
	}
}

// TestHandleStars_NotAvailableWithoutClosures covers ghost mode (issue #67):
// gateway/turn.go leaves StarsSearch/StarsRead nil for an anonymous turn, the
// same way it leaves WriteMemory/SearchThreads nil — no persisted-store reads
// should leak into an incognito session.
func TestHandleStars_NotAvailableWithoutClosures(t *testing.T) {
	ctx := newTestContext() // no StarsSearch/StarsRead wired

	if r := handleStars(`{"query":"anything"}`, ctx, "id1"); !strings.Contains(r, "not available") {
		t.Errorf("search result = %q, want a not-available error", r)
	}
	if r := handleStars(`{"star_id":7}`, ctx, "id2"); !strings.Contains(r, "not available") {
		t.Errorf("read result = %q, want a not-available error", r)
	}
}

func TestHandleStars_StarIDTakesPrecedenceOverQuery(t *testing.T) {
	ctx := newTestContext()
	ctx.StarsSearch = func(query string, limit int) ([]store.Star, error) {
		t.Fatal("StarsSearch should not be called when star_id is set")
		return nil, nil
	}
	ctx.StarsRead = func(starID int64) (*store.Star, error) {
		return &store.Star{ID: starID, Title: "found by id"}, nil
	}

	result := handleStars(`{"query":"ignored","star_id":7}`, ctx, "test-call")
	if !strings.Contains(result, "found by id") {
		t.Errorf("result = %q, want the read path to win over search", result)
	}
}
