package store

import (
	"testing"
)

func TestSearchStars_FTS(t *testing.T) {
	s := openTestStore(t)

	id1, _ := s.CreateStar(Star{Title: "Cloudflare Workers", Category: "technology", Summary: "Edge compute platform", Status: "auto"})
	_, _ = s.CreateStar(Star{Title: "Sourdough baking", Category: "cooking", Summary: "Fermentation and hydration ratios", Status: "auto"})

	results, err := s.SearchStars("Cloudflare", 10)
	if err != nil {
		t.Fatalf("SearchStars: %v", err)
	}
	if len(results) != 1 || results[0].ID != id1 {
		t.Errorf("SearchStars(Cloudflare) = %+v, want just the Cloudflare star", results)
	}

	// A rejected star must still be findable — search_stars is also
	// dedup-checking duty, and Weaver needs to see "this was already said no
	// to" rather than silently re-proposing it.
	id3, _ := s.CreateStar(Star{Title: "Rejected topic", Category: "technology", Summary: "Something declined", Status: "proposed"})
	if err := s.SetStarStatus(id3, "rejected"); err != nil {
		t.Fatalf("SetStarStatus: %v", err)
	}
	results, err = s.SearchStars("declined", 10)
	if err != nil {
		t.Fatalf("SearchStars(declined): %v", err)
	}
	if len(results) != 1 || results[0].ID != id3 {
		t.Errorf("SearchStars(declined) = %+v, want the rejected star to still be findable", results)
	}
}

func TestSearchLibraryStars_ExcludesRejectedAndDisabled(t *testing.T) {
	s := openTestStore(t)

	visible, _ := s.CreateStar(Star{Title: "Cloudflare Workers", Category: "technology", Summary: "Edge compute platform", Status: "auto"})

	rejected, _ := s.CreateStar(Star{Title: "Cloudflare pages rejected", Category: "technology", Summary: "Something declined about Cloudflare", Status: "proposed"})
	if err := s.SetStarStatus(rejected, "rejected"); err != nil {
		t.Fatalf("SetStarStatus: %v", err)
	}

	disabled, _ := s.CreateStar(Star{Title: "Cloudflare disabled topic", Category: "technology", Summary: "Cloudflare but disabled", Status: "confirmed"})
	if err := s.SetStarDisabled(disabled, true); err != nil {
		t.Fatalf("SetStarDisabled: %v", err)
	}

	results, err := s.SearchLibraryStars("Cloudflare", 10)
	if err != nil {
		t.Fatalf("SearchLibraryStars: %v", err)
	}
	if len(results) != 1 || results[0].ID != visible {
		t.Errorf("SearchLibraryStars(Cloudflare) = %+v, want just the one visible star (not the rejected or disabled ones)", results)
	}
	if results[0].Category != "technology" {
		t.Errorf("Category = %q, want technology", results[0].Category)
	}
	if results[0].Tags == nil {
		t.Error("Tags should never be nil, even for a star created with no tags")
	}
}
