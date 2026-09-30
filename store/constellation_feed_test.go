package store

import (
	"testing"
)

func TestGetConstellationWeekFeed_IncludesStarID(t *testing.T) {
	s := openTestStore(t)

	newID, _ := s.CreateStar(Star{Title: "Brand new", Category: "technology", Status: "auto"})

	updatedID, _ := s.CreateStar(Star{Title: "Will be updated", Category: "technology", Status: "auto"})
	if _, err := s.db.Exec(`UPDATE stars SET created_at = datetime('now', '-30 days') WHERE id = ?`, updatedID); err != nil {
		t.Fatalf("backdating created_at: %v", err)
	}
	if err := s.UpdateStar(updatedID, "", "new summary", "new body", nil, "", boolPtr(false), "test"); err != nil {
		t.Fatalf("UpdateStar: %v", err)
	}

	linkA, _ := s.CreateStar(Star{Title: "Link side A", Category: "technology", Status: "auto"})
	linkB, _ := s.CreateStar(Star{Title: "Link side B", Category: "technology", Status: "auto"})
	if err := s.LinkStars(linkA, linkB, "related"); err != nil {
		t.Fatalf("LinkStars: %v", err)
	}

	items, err := s.GetConstellationWeekFeed()
	if err != nil {
		t.Fatalf("GetConstellationWeekFeed: %v", err)
	}

	byTitle := map[string]ConstellationWeekItem{}
	for _, it := range items {
		byTitle[it.Title] = it
	}

	if got := byTitle["Brand new"]; got.StarID != newID {
		t.Errorf("new item StarID = %d, want %d", got.StarID, newID)
	}
	if got := byTitle["Will be updated"]; got.StarID != updatedID {
		t.Errorf("updated item StarID = %d, want %d", got.StarID, updatedID)
	}
	if got := byTitle["Link side A"]; got.StarID != linkA {
		t.Errorf("linked item StarID = %d, want %d", got.StarID, linkA)
	}
}
