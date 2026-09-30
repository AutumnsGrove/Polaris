package store

import (
	"testing"
)

func TestStar_CreateGetUpdate(t *testing.T) {
	s := openTestStore(t)

	id, err := s.CreateStar(Star{
		Title:    "Cloudflare Workers",
		Category: "technology",
		Summary:  "Edge compute platform",
		Body:     "## Cloudflare Workers\n\nRuns JS at the edge.",
		Tags:     []string{"cloudflare", "edge"},
		Status:   "auto",
	})
	if err != nil {
		t.Fatalf("CreateStar: %v", err)
	}
	if id == 0 {
		t.Fatal("CreateStar returned id 0")
	}

	got, err := s.GetStar(id)
	if err != nil {
		t.Fatalf("GetStar: %v", err)
	}
	if got.Title != "Cloudflare Workers" || got.Category != "technology" || len(got.Tags) != 2 {
		t.Errorf("GetStar = %+v, want the values just written", got)
	}
	if got.Status != "auto" || got.IsPersonal {
		t.Errorf("GetStar defaults = %+v, want status=auto, is_personal=false", got)
	}

	if err := s.UpdateStar(id, "", "Updated summary", "Updated body", []string{"cloudflare", "workers", "edge"}, "fuzzy", boolPtr(false), "test"); err != nil {
		t.Fatalf("UpdateStar: %v", err)
	}
	got, err = s.GetStar(id)
	if err != nil {
		t.Fatalf("GetStar (after update): %v", err)
	}
	if got.Summary != "Updated summary" || len(got.Tags) != 3 || got.Confidence != "fuzzy" {
		t.Errorf("GetStar after update = %+v, want the values just written", got)
	}

	if _, err := s.GetStar(999999); err != ErrStarNotFound {
		t.Fatalf("GetStar(missing) = %v, want ErrStarNotFound", err)
	}
	if err := s.UpdateStar(999999, "", "x", "x", nil, "", nil, "test"); err != ErrStarNotFound {
		t.Fatalf("UpdateStar(missing id) = %v, want ErrStarNotFound", err)
	}
}

func TestStar_UpdateStarOmittedFieldsLeaveExistingValuesAlone(t *testing.T) {
	s := openTestStore(t)
	id, err := s.CreateStar(Star{
		Title: "Cloudflare Workers", Category: "technology", Summary: "s",
		Body: "original body", Tags: []string{"edge"}, Confidence: "obvious",
		Status: "auto", IsPersonal: true,
	})
	if err != nil {
		t.Fatalf("CreateStar: %v", err)
	}

	// "" for body/confidenceClass, nil for tags/isPersonal — matching what
	// a real update_star tool call that only meant to touch the summary
	// (body/tags/confidence_class/is_personal are all optional in that
	// tool's schema) actually looks like once JSON-unmarshaled. Every one
	// of these must be left exactly as they were, not blanked out.
	if err := s.UpdateStar(id, "", "just a summary fix", "", nil, "", nil, "test"); err != nil {
		t.Fatalf("UpdateStar: %v", err)
	}
	got, err := s.GetStar(id)
	if err != nil {
		t.Fatalf("GetStar: %v", err)
	}
	if got.Summary != "just a summary fix" {
		t.Errorf("Summary = %q, want the new summary", got.Summary)
	}
	if got.Body != "original body" {
		t.Errorf("Body = %q, want it left unchanged (omitted in the call)", got.Body)
	}
	if len(got.Tags) != 1 || got.Tags[0] != "edge" {
		t.Errorf("Tags = %+v, want left unchanged (nil in the call)", got.Tags)
	}
	if got.Confidence != "obvious" {
		t.Errorf("Confidence = %q, want left unchanged (\"\" in the call)", got.Confidence)
	}
	if !got.IsPersonal {
		t.Error("IsPersonal = false, want left unchanged (nil in the call)")
	}

	// An explicit, non-nil empty tags slice really does mean "clear every
	// tag" — distinct from nil, which means "the caller didn't say".
	if err := s.UpdateStar(id, "", "summary", "", []string{}, "", nil, "test"); err != nil {
		t.Fatalf("UpdateStar (explicit empty tags): %v", err)
	}
	got, err = s.GetStar(id)
	if err != nil {
		t.Fatalf("GetStar: %v", err)
	}
	if len(got.Tags) != 0 {
		t.Errorf("Tags after explicit []string{} = %+v, want cleared", got.Tags)
	}

	// An explicit false really does flip is_personal, distinct from nil.
	if err := s.UpdateStar(id, "", "summary", "", nil, "", boolPtr(false), "test"); err != nil {
		t.Fatalf("UpdateStar (explicit is_personal=false): %v", err)
	}
	got, err = s.GetStar(id)
	if err != nil {
		t.Fatalf("GetStar: %v", err)
	}
	if got.IsPersonal {
		t.Error("IsPersonal after explicit false = true, want false")
	}
}

func TestStar_UpdateStarTitle(t *testing.T) {
	s := openTestStore(t)
	id, err := s.CreateStar(Star{Title: "Reads science fiction", Category: "literature", Summary: "s", Status: "auto"})
	if err != nil {
		t.Fatalf("CreateStar: %v", err)
	}

	// "" leaves the title untouched — the normal case for both callers
	// (update_star's title field and Edit/Refine's TITLE: line are each
	// optional, omitted on most calls).
	if err := s.UpdateStar(id, "", "still enjoys sci-fi", "b", nil, "obvious", boolPtr(false), "test"); err != nil {
		t.Fatalf("UpdateStar: %v", err)
	}
	got, err := s.GetStar(id)
	if err != nil {
		t.Fatalf("GetStar: %v", err)
	}
	if got.Title != "Reads science fiction" {
		t.Errorf("Title after empty-title update = %q, want unchanged", got.Title)
	}

	// A non-empty title actually retitles the star — the Edit/Refine
	// correction sheet's path when a correction changes the star's premise.
	if err := s.UpdateStar(id, "Reads fantasy novels", "actually fantasy, not sci-fi", "b", nil, "obvious", boolPtr(false), "test"); err != nil {
		t.Fatalf("UpdateStar: %v", err)
	}
	got, err = s.GetStar(id)
	if err != nil {
		t.Fatalf("GetStar: %v", err)
	}
	if got.Title != "Reads fantasy novels" {
		t.Errorf("Title after non-empty-title update = %q, want the new title", got.Title)
	}
}

func TestStar_NilTagsEncodeAsEmptyArrayNotNull(t *testing.T) {
	s := openTestStore(t)

	id, err := s.CreateStar(Star{Title: "No tags", Category: "technology", Status: "auto"})
	if err != nil {
		t.Fatalf("CreateStar: %v", err)
	}
	var tagsJSON string
	if err := s.db.QueryRow(`SELECT tags FROM stars WHERE id = ?`, id).Scan(&tagsJSON); err != nil {
		t.Fatalf("querying raw tags column: %v", err)
	}
	if tagsJSON != "[]" {
		t.Errorf("raw tags column = %q, want \"[]\" (nil Tags must not encode as JSON null)", tagsJSON)
	}

	// nil tags on UpdateStar means "leave as-is" (see its doc comment), so
	// this stays "[]" because that's what it already was, not because
	// UpdateStar re-encodes nil itself — TestStar_UpdateStarOmittedFieldsLeaveExistingValuesAlone
	// is what actually exercises that "leave as-is" contract.
	if err := s.UpdateStar(id, "", "summary", "body", nil, "obvious", boolPtr(false), "test"); err != nil {
		t.Fatalf("UpdateStar: %v", err)
	}
	if err := s.db.QueryRow(`SELECT tags FROM stars WHERE id = ?`, id).Scan(&tagsJSON); err != nil {
		t.Fatalf("querying raw tags column after update: %v", err)
	}
	if tagsJSON != "[]" {
		t.Errorf("raw tags column after UpdateStar(nil) = %q, want \"[]\"", tagsJSON)
	}
}

func TestStar_PersonalCreateAndUpdateDoNotForceProposed(t *testing.T) {
	s := openTestStore(t)

	id, err := s.CreateStar(Star{
		Title: "Reads science fiction", Category: "literature", Summary: "Enjoys sci-fi",
		Status: "auto", IsPersonal: true,
	})
	if err != nil {
		t.Fatalf("CreateStar: %v", err)
	}
	got, err := s.GetStar(id)
	if err != nil {
		t.Fatalf("GetStar: %v", err)
	}
	if got.Status != "auto" {
		t.Errorf("personal star Status = %q, want auto (matching the requested status, no forced review gate)", got.Status)
	}

	// Confirm it, then update it — a personal update must not silently
	// bounce a confirmed star back into the review queue.
	if err := s.SetStarStatus(id, "confirmed"); err != nil {
		t.Fatalf("SetStarStatus: %v", err)
	}
	if err := s.UpdateStar(id, "", "Enjoys sci-fi, especially Le Guin", got.Body, got.Tags, got.Confidence, boolPtr(true), "test"); err != nil {
		t.Fatalf("UpdateStar: %v", err)
	}
	got, err = s.GetStar(id)
	if err != nil {
		t.Fatalf("GetStar (after update): %v", err)
	}
	if got.Status != "confirmed" {
		t.Errorf("updating a personal star Status = %q, want confirmed unchanged", got.Status)
	}
	if !got.IsPersonal {
		t.Error("IsPersonal after update = false, want true (update_star must actually persist is_personal)")
	}
}

func TestListStars_GroupingAndFilters(t *testing.T) {
	s := openTestStore(t)

	auto, _ := s.CreateStar(Star{Title: "Go generics", Category: "technology", Status: "auto"})
	proposed, _ := s.CreateStar(Star{Title: "Vague inference", Category: "technology", Status: "proposed"})
	rejected, _ := s.CreateStar(Star{Title: "Wrong guess", Category: "technology", Status: "proposed"})
	if err := s.SetStarStatus(rejected, "rejected"); err != nil {
		t.Fatalf("SetStarStatus: %v", err)
	}
	disabled, _ := s.CreateStar(Star{Title: "Unwanted", Category: "technology", Status: "confirmed"})
	if err := s.SetStarDisabled(disabled, true); err != nil {
		t.Fatalf("SetStarDisabled: %v", err)
	}

	stars, err := s.ListStars(StarFilter{Statuses: []string{"auto", "confirmed"}})
	if err != nil {
		t.Fatalf("ListStars: %v", err)
	}
	if len(stars) != 1 || stars[0].ID != auto {
		t.Errorf("ListStars(auto+confirmed) = %+v, want just the auto star (disabled/proposed/rejected excluded)", stars)
	}

	proposedStars, err := s.ListStars(StarFilter{Statuses: []string{"proposed"}})
	if err != nil {
		t.Fatalf("ListStars(proposed): %v", err)
	}
	if len(proposedStars) != 1 || proposedStars[0].ID != proposed {
		t.Errorf("ListStars(proposed) = %+v, want just the proposed star", proposedStars)
	}

	rejectedStars, err := s.ListStars(StarFilter{Statuses: []string{"rejected"}})
	if err != nil {
		t.Fatalf("ListStars(rejected): %v", err)
	}
	if len(rejectedStars) != 1 || rejectedStars[0].ID != rejected {
		t.Errorf("ListStars(rejected) = %+v, want just the rejected star", rejectedStars)
	}
}
