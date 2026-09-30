package store

import (
	"testing"
)

func TestGetConstellationStats_Aggregates(t *testing.T) {
	s := openTestStore(t)
	threadID := seedThread(t, s)

	starID, _ := s.CreateStar(Star{Title: "A", Category: "technology", Status: "auto"})
	proposedID, _ := s.CreateStar(Star{Title: "B", Category: "technology", Status: "proposed"})
	other, _ := s.CreateStar(Star{Title: "C", Category: "technology", Status: "auto"})
	if err := s.LinkStars(starID, other, "related"); err != nil {
		t.Fatalf("LinkStars: %v", err)
	}
	if err := s.RecordStarReview(proposedID, "approved", ""); err != nil {
		t.Fatalf("RecordStarReview: %v", err)
	}

	runID, err := s.StartShootingStarRun(threadID, 1)
	if err != nil {
		t.Fatalf("StartShootingStarRun: %v", err)
	}
	if err := s.RecordShootingStarEvent(runID, "search_stars", "{}", "[]", 0.001); err != nil {
		t.Fatalf("RecordShootingStarEvent: %v", err)
	}
	if err := s.FinishShootingStarRun(runID, "", "max_turns_exceeded", true); err != nil {
		t.Fatalf("FinishShootingStarRun: %v", err)
	}

	stats, err := s.GetConstellationStats(0)
	if err != nil {
		t.Fatalf("GetConstellationStats: %v", err)
	}
	if stats.TotalCostUSD < 0.001 {
		t.Errorf("TotalCostUSD = %v, want at least 0.001", stats.TotalCostUSD)
	}
	if stats.ShootingStarCount != 1 {
		t.Errorf("ShootingStarCount = %d, want 1", stats.ShootingStarCount)
	}
	if stats.StarCountsByStatus["auto"] != 2 || stats.StarCountsByStatus["proposed"] != 1 {
		t.Errorf("StarCountsByStatus = %+v, want auto=2 proposed=1", stats.StarCountsByStatus)
	}
	if stats.ToolCallCounts["search_stars"] != 1 {
		t.Errorf("ToolCallCounts = %+v, want search_stars=1", stats.ToolCallCounts)
	}
	if stats.ReviewActionCounts["approved"] != 1 {
		t.Errorf("ReviewActionCounts = %+v, want approved=1", stats.ReviewActionCounts)
	}
	if stats.LinksCreatedCount != 1 {
		t.Errorf("LinksCreatedCount = %d, want 1", stats.LinksCreatedCount)
	}
	if stats.MaxTurnsCount != 1 {
		t.Errorf("MaxTurnsCount = %d, want 1", stats.MaxTurnsCount)
	}
	if stats.NeedsRetryCount != 1 {
		t.Errorf("NeedsRetryCount = %d, want 1", stats.NeedsRetryCount)
	}
}

func TestRecordStarReconcileCost_RollsIntoStats(t *testing.T) {
	s := openTestStore(t)
	starID, _ := s.CreateStar(Star{Title: "A", Category: "technology", Status: "confirmed"})

	// A Refine/Edit correction has no shooting_star_runs row to hang a
	// RecordShootingStarEvent call off of — this is its own cost trail,
	// which GetConstellationStats must still fold into the same
	// Total/PeriodCostUSD figure Weaver's own runs feed (see
	// gateway/constellation_routes.go's reconcileAndSaveStar, which
	// previously discarded this cost entirely).
	if err := s.RecordStarReconcileCost(starID, 0.0042); err != nil {
		t.Fatalf("RecordStarReconcileCost: %v", err)
	}

	stats, err := s.GetConstellationStats(0)
	if err != nil {
		t.Fatalf("GetConstellationStats: %v", err)
	}
	if diff := stats.TotalCostUSD - 0.0042; diff > 0.00001 || diff < -0.00001 {
		t.Errorf("TotalCostUSD = %v, want to include the 0.0042 reconcile cost", stats.TotalCostUSD)
	}
	if diff := stats.PeriodCostUSD - 0.0042; diff > 0.00001 || diff < -0.00001 {
		t.Errorf("PeriodCostUSD = %v, want to include the 0.0042 reconcile cost", stats.PeriodCostUSD)
	}
}
