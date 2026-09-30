package store

import (
	"testing"
)

func TestShootingStarCandidateAndEvent_Recorded(t *testing.T) {
	s := openTestStore(t)
	threadID := seedThread(t, s)
	runID, err := s.StartShootingStarRun(threadID, 1)
	if err != nil {
		t.Fatalf("StartShootingStarRun: %v", err)
	}
	starID, _ := s.CreateStar(Star{Title: "Topic", Category: "technology", Status: "auto"})

	if err := s.RecordShootingStarCandidate(runID, "Cloudflare Workers", "obvious", "new_star", "clearly discussed at length", &starID); err != nil {
		t.Fatalf("RecordShootingStarCandidate: %v", err)
	}
	if err := s.RecordShootingStarEvent(runID, "create_star", `{"title":"Cloudflare Workers"}`, "created star 1", 0.0012); err != nil {
		t.Fatalf("RecordShootingStarEvent: %v", err)
	}
	if err := s.RecordShootingStarEvent(runID, "final_answer", "{}", "Noted a new interest.", 0.0003); err != nil {
		t.Fatalf("RecordShootingStarEvent (final_answer): %v", err)
	}

	if err := s.FinishShootingStarRun(runID, "Noted a new interest.", "", false); err != nil {
		t.Fatalf("FinishShootingStarRun: %v", err)
	}
	last, err := s.LastShootingStarRun(threadID)
	if err != nil {
		t.Fatalf("LastShootingStarRun: %v", err)
	}
	// cost_usd is a cached rollup of shooting_star_events.cost_usd for this run.
	want := 0.0012 + 0.0003
	if diff := last.CostUSD - want; diff > 0.00001 || diff < -0.00001 {
		t.Errorf("CostUSD = %v, want rollup %v of the two logged events", last.CostUSD, want)
	}
}

func TestStarReview_Recorded(t *testing.T) {
	s := openTestStore(t)
	starID, _ := s.CreateStar(Star{Title: "Proposed thing", Category: "technology", Status: "proposed"})

	if err := s.RecordStarReview(starID, "approved", ""); err != nil {
		t.Fatalf("RecordStarReview: %v", err)
	}
	if err := s.SetStarStatus(starID, "confirmed"); err != nil {
		t.Fatalf("SetStarStatus: %v", err)
	}
	got, err := s.GetStar(starID)
	if err != nil {
		t.Fatalf("GetStar: %v", err)
	}
	if got.Status != "confirmed" {
		t.Errorf("Status = %q, want confirmed after approve", got.Status)
	}
}

func TestSetStarStatusAndRecordReview_WritesBothAtomically(t *testing.T) {
	s := openTestStore(t)
	starID, _ := s.CreateStar(Star{Title: "A", Category: "technology", Status: "proposed"})

	if err := s.SetStarStatusAndRecordReview(starID, "confirmed", "approved", ""); err != nil {
		t.Fatalf("SetStarStatusAndRecordReview: %v", err)
	}

	star, err := s.GetStar(starID)
	if err != nil {
		t.Fatalf("GetStar: %v", err)
	}
	if star.Status != "confirmed" {
		t.Errorf("Status = %q, want confirmed", star.Status)
	}
	stats, err := s.GetConstellationStats(0)
	if err != nil {
		t.Fatalf("GetConstellationStats: %v", err)
	}
	if stats.ReviewActionCounts["approved"] != 1 {
		t.Errorf("ReviewActionCounts = %+v, want approved=1 to have been written alongside the status change", stats.ReviewActionCounts)
	}
}

func TestLatestCandidateReasoningBulk_ReturnsMostRecentPerStar(t *testing.T) {
	s := openTestStore(t)
	threadID := seedThread(t, s)
	runID, err := s.StartShootingStarRun(threadID, 1)
	if err != nil {
		t.Fatalf("StartShootingStarRun: %v", err)
	}
	starA, _ := s.CreateStar(Star{Title: "A", Category: "technology", Status: "proposed"})
	starB, _ := s.CreateStar(Star{Title: "B", Category: "technology", Status: "proposed"})

	if err := s.RecordShootingStarCandidate(runID, "A", "unsure", "new_star", "stale reason", &starA); err != nil {
		t.Fatalf("RecordShootingStarCandidate: %v", err)
	}
	if err := s.RecordShootingStarCandidate(runID, "A", "unsure", "new_star", "fresh reason", &starA); err != nil {
		t.Fatalf("RecordShootingStarCandidate: %v", err)
	}
	if err := s.RecordShootingStarCandidate(runID, "B", "unsure", "new_star", "reason for B", &starB); err != nil {
		t.Fatalf("RecordShootingStarCandidate: %v", err)
	}

	got, err := s.LatestCandidateReasoningBulk([]int64{starA, starB})
	if err != nil {
		t.Fatalf("LatestCandidateReasoningBulk: %v", err)
	}
	if got[starA] != "fresh reason" {
		t.Errorf("reasoning[A] = %q, want the most recently recorded row", got[starA])
	}
	if got[starB] != "reason for B" {
		t.Errorf("reasoning[B] = %q, want %q", got[starB], "reason for B")
	}
}
