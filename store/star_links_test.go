package store

import (
	"testing"
)

func TestStarSources_UpsertNotDuplicate(t *testing.T) {
	s := openTestStore(t)
	threadID := seedThread(t, s)
	starID, _ := s.CreateStar(Star{Title: "Topic", Category: "technology", Status: "auto"})

	if err := s.LinkStarSource(starID, threadID); err != nil {
		t.Fatalf("LinkStarSource: %v", err)
	}
	if err := s.LinkStarSource(starID, threadID); err != nil {
		t.Fatalf("LinkStarSource (second time, same thread): %v", err)
	}

	sources, err := s.StarSources(starID)
	if err != nil {
		t.Fatalf("StarSources: %v", err)
	}
	if len(sources) != 1 {
		t.Errorf("StarSources = %+v, want exactly one row (upsert, not duplicate)", sources)
	}
}

func TestStarsByThread_ReturnsContributedStars(t *testing.T) {
	s := openTestStore(t)
	threadID := seedThread(t, s)
	otherThreadID := seedThread(t, s)

	starA, _ := s.CreateStar(Star{Title: "A", Category: "technology", Status: "auto"})
	starB, _ := s.CreateStar(Star{Title: "B", Category: "technology", Status: "auto"})
	unrelated, _ := s.CreateStar(Star{Title: "C", Category: "technology", Status: "auto"})

	if err := s.LinkStarSource(starA, threadID); err != nil {
		t.Fatalf("LinkStarSource: %v", err)
	}
	if err := s.LinkStarSource(starB, threadID); err != nil {
		t.Fatalf("LinkStarSource: %v", err)
	}
	if err := s.LinkStarSource(unrelated, otherThreadID); err != nil {
		t.Fatalf("LinkStarSource: %v", err)
	}

	stars, err := s.StarsByThread(threadID)
	if err != nil {
		t.Fatalf("StarsByThread: %v", err)
	}
	if len(stars) != 2 {
		t.Fatalf("StarsByThread = %+v, want exactly the 2 stars this thread contributed to", stars)
	}
	for _, star := range stars {
		if star.ID == unrelated {
			t.Errorf("StarsByThread included a star from a different thread: %+v", star)
		}
	}
}

func TestStarEdges_LinkIsIdempotentAndUndirected(t *testing.T) {
	s := openTestStore(t)
	a, _ := s.CreateStar(Star{Title: "A", Category: "technology", Status: "auto"})
	b, _ := s.CreateStar(Star{Title: "B", Category: "technology", Status: "auto"})

	if err := s.LinkStars(a, b, "both concern edge compute"); err != nil {
		t.Fatalf("LinkStars: %v", err)
	}
	// Idempotent no-op regardless of argument order.
	if err := s.LinkStars(b, a, "duplicate call"); err != nil {
		t.Fatalf("LinkStars (reverse order): %v", err)
	}

	edges, err := s.StarEdges(a)
	if err != nil {
		t.Fatalf("StarEdges: %v", err)
	}
	if len(edges) != 1 {
		t.Errorf("StarEdges(a) = %+v, want exactly one edge (idempotent link)", edges)
	}

	edgesB, err := s.StarEdges(b)
	if err != nil {
		t.Fatalf("StarEdges(b): %v", err)
	}
	if len(edgesB) != 1 {
		t.Errorf("StarEdges(b) = %+v, want the same edge visible from either side", edgesB)
	}
}
