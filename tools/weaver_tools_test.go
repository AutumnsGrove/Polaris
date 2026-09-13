package tools

import (
	"context"
	"strings"
	"testing"

	"polaris/store"
)

// fakeStarStore is a minimal in-memory stand-in for the store.Store methods
// Weaver's five tools depend on — enough to exercise the handlers'
// validation/dispatch logic without a real SQLite database, same shape as
// fakeMemoryStore in memory_test.go.
type fakeStarStore struct {
	stars     map[int64]store.Star
	nextID    int64
	edges     map[[2]int64]string
	candidate *struct {
		runID           int64
		title           string
		confidenceClass string
		decision        string
		reasoning       string
		starID          *int64
	}
}

func newFakeStarStore() *fakeStarStore {
	return &fakeStarStore{stars: map[int64]store.Star{}}
}

func (f *fakeStarStore) wireInto(ctx *Context, runID int64) {
	ctx.WeaverRun = true
	ctx.WeaverSearchStars = func(query string) ([]store.StarSearchResult, error) {
		var out []store.StarSearchResult
		for _, s := range f.stars {
			if strings.Contains(strings.ToLower(s.Title), strings.ToLower(query)) || strings.Contains(strings.ToLower(s.Summary), strings.ToLower(query)) {
				out = append(out, store.StarSearchResult{ID: s.ID, Title: s.Title, Summary: s.Summary, Status: s.Status})
			}
		}
		return out, nil
	}
	ctx.WeaverReadStar = func(starID int64) (*store.Star, error) {
		s, ok := f.stars[starID]
		if !ok {
			return nil, store.ErrStarNotFound
		}
		return &s, nil
	}
	ctx.WeaverCreateStar = func(title, category, summary, body string, tags []string, confidenceClass string, isPersonal bool, reasoning string) (int64, error) {
		f.nextID++
		id := f.nextID
		f.stars[id] = store.Star{ID: id, Title: title, Category: category, Summary: summary, Body: body, Tags: tags, Confidence: confidenceClass, IsPersonal: isPersonal, Status: "auto"}
		f.candidate = &struct {
			runID           int64
			title           string
			confidenceClass string
			decision        string
			reasoning       string
			starID          *int64
		}{runID, title, confidenceClass, "new_star", reasoning, &id}
		return id, nil
	}
	ctx.WeaverUpdateStar = func(starID int64, summary, body string, tags []string, confidenceClass string, isPersonal *bool, reasoning string) error {
		s, ok := f.stars[starID]
		if !ok {
			return store.ErrStarNotFound
		}
		s.Summary, s.Body, s.Tags, s.Confidence = summary, body, tags, confidenceClass
		if isPersonal != nil {
			s.IsPersonal = *isPersonal
		}
		f.stars[starID] = s
		return nil
	}
	ctx.WeaverLinkStars = func(starIDA, starIDB int64, reasoning string) error {
		if f.edges == nil {
			f.edges = map[[2]int64]string{}
		}
		a, b := starIDA, starIDB
		if a > b {
			a, b = b, a
		}
		f.edges[[2]int64{a, b}] = reasoning
		return nil
	}
}

func newWeaverTestContext(f *fakeStarStore) *Context {
	ctx := &Context{Ctx: context.Background(), Emit: func(string, map[string]interface{}) {}}
	f.wireInto(ctx, 1)
	return ctx
}

func TestSearchStars_FindsMatch(t *testing.T) {
	f := newFakeStarStore()
	f.stars[1] = store.Star{ID: 1, Title: "Cloudflare Workers", Summary: "Edge compute", Status: "auto"}
	ctx := newWeaverTestContext(f)

	result := handleSearchStars(`{"query":"Cloudflare"}`, ctx, "call1")
	if !strings.Contains(result, "Cloudflare Workers") {
		t.Errorf("handleSearchStars result = %q, want it to mention the matching star", result)
	}
}

func TestSearchStars_NotOfferedOutsideWeaverRun(t *testing.T) {
	ctx := &Context{}
	entry := catalogDefaults["search_stars"]
	if entry.offered(ctx) {
		t.Error("search_stars should not be offered when WeaverRun is false")
	}
}

func TestReadStar_ReturnsFullCard(t *testing.T) {
	f := newFakeStarStore()
	f.stars[1] = store.Star{ID: 1, Title: "Cloudflare Workers", Category: "technology", Summary: "Edge compute", Body: "Full body text", Status: "auto"}
	ctx := newWeaverTestContext(f)

	result := handleReadStar(`{"star_id":1}`, ctx, "call1")
	if !strings.Contains(result, "Full body text") || !strings.Contains(result, "technology") {
		t.Errorf("handleReadStar result = %q, want the full card including body and category", result)
	}
}

func TestReadStar_MissingStar(t *testing.T) {
	f := newFakeStarStore()
	ctx := newWeaverTestContext(f)

	result := handleReadStar(`{"star_id":999}`, ctx, "call1")
	if !strings.HasPrefix(result, "error:") {
		t.Errorf("handleReadStar(missing) = %q, want an error result", result)
	}
}

func TestCreateStar_WritesAndReturnsID(t *testing.T) {
	f := newFakeStarStore()
	ctx := newWeaverTestContext(f)

	result := handleCreateStar(`{"title":"Cloudflare Workers","category":"technology","summary":"Edge compute","body":"body","tags":["edge"],"confidence_class":"obvious"}`, ctx, "call1")
	if !strings.Contains(result, "1") {
		t.Errorf("handleCreateStar result = %q, want it to reference the new star id", result)
	}
	if len(f.stars) != 1 {
		t.Fatalf("f.stars = %+v, want exactly one star written", f.stars)
	}
	if f.stars[1].Status != "auto" {
		t.Errorf("non-personal star Status = %q, want auto", f.stars[1].Status)
	}
}

func TestCreateStar_PersonalGoesStraightToAuto(t *testing.T) {
	f := newFakeStarStore()
	ctx := newWeaverTestContext(f)

	handleCreateStar(`{"title":"Reads science fiction","category":"literature","summary":"Enjoys sci-fi","confidence_class":"obvious","is_personal":true}`, ctx, "call1")
	if f.stars[1].Status != "auto" {
		t.Errorf("personal star Status = %q, want auto (no forced review gate)", f.stars[1].Status)
	}
}

func TestCreateStar_MissingRequiredFields(t *testing.T) {
	f := newFakeStarStore()
	ctx := newWeaverTestContext(f)

	result := handleCreateStar(`{"title":"","category":"technology"}`, ctx, "call1")
	if !strings.HasPrefix(result, "error:") {
		t.Errorf("handleCreateStar(empty title) = %q, want an error result", result)
	}
}

func TestUpdateStar_MergesContent(t *testing.T) {
	f := newFakeStarStore()
	f.stars[1] = store.Star{ID: 1, Title: "Cloudflare Workers", Category: "technology", Summary: "old", Body: "old body", Status: "auto"}
	ctx := newWeaverTestContext(f)

	result := handleUpdateStar(`{"star_id":1,"summary":"new summary","body":"new body","tags":["edge"],"confidence_class":"fuzzy"}`, ctx, "call1")
	if strings.HasPrefix(result, "error:") {
		t.Fatalf("handleUpdateStar returned error: %q", result)
	}
	if f.stars[1].Summary != "new summary" {
		t.Errorf("Summary = %q, want new summary", f.stars[1].Summary)
	}
}

func TestUpdateStar_PersonalDoesNotResetStatus(t *testing.T) {
	f := newFakeStarStore()
	f.stars[1] = store.Star{ID: 1, Title: "Reads science fiction", Status: "confirmed", IsPersonal: true}
	ctx := newWeaverTestContext(f)

	handleUpdateStar(`{"star_id":1,"summary":"still true","is_personal":true}`, ctx, "call1")
	if f.stars[1].Status != "confirmed" {
		t.Errorf("Status after updating a personal star = %q, want confirmed unchanged (no forced review gate)", f.stars[1].Status)
	}
}

func TestLinkStars_CreatesEdge(t *testing.T) {
	f := newFakeStarStore()
	f.stars[1] = store.Star{ID: 1, Title: "A"}
	f.stars[2] = store.Star{ID: 2, Title: "B"}
	ctx := newWeaverTestContext(f)

	result := handleLinkStars(`{"star_id_a":1,"star_id_b":2,"reasoning":"both concern edge compute"}`, ctx, "call1")
	if strings.HasPrefix(result, "error:") {
		t.Fatalf("handleLinkStars returned error: %q", result)
	}
	if reasoning, ok := f.edges[[2]int64{1, 2}]; !ok || reasoning != "both concern edge compute" {
		t.Errorf("edges = %+v, want the edge just created", f.edges)
	}
}

func TestLinkStars_NotOfferedOutsideWeaverRun(t *testing.T) {
	ctx := &Context{}
	entry := catalogDefaults["link_stars"]
	if entry.offered(ctx) {
		t.Error("link_stars should not be offered when WeaverRun is false")
	}
}
