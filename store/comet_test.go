package store

import (
	"testing"
	"time"
)

func TestCometConfig_DefaultsAndUpdate(t *testing.T) {
	s := openTestStore(t)

	cfg, err := s.GetCometConfig()
	if err != nil {
		t.Fatalf("GetCometConfig: %v", err)
	}
	if cfg.Enabled {
		t.Error("fresh Comet config should default to disabled")
	}
	if cfg.TimeOfDay != "06:00" {
		t.Errorf("TimeOfDay = %q, want the 06:00 default", cfg.TimeOfDay)
	}

	if err := s.UpdateCometConfig(true, "08:30", "deepseek"); err != nil {
		t.Fatalf("UpdateCometConfig: %v", err)
	}
	cfg, err = s.GetCometConfig()
	if err != nil {
		t.Fatalf("GetCometConfig after update: %v", err)
	}
	if !cfg.Enabled || cfg.TimeOfDay != "08:30" || cfg.Model != "deepseek" {
		t.Errorf("config after update = %+v, want enabled/08:30/deepseek", cfg)
	}
}

func TestCometClassifyStarted_IsACompareAndSet(t *testing.T) {
	s := openTestStore(t)

	if err := s.SetCometClassifyStarted(time.Hour); err != nil {
		t.Fatalf("first SetCometClassifyStarted: %v", err)
	}
	if err := s.SetCometClassifyStarted(time.Hour); err != ErrCometClassifyAlreadyRunning {
		t.Fatalf("second SetCometClassifyStarted = %v, want ErrCometClassifyAlreadyRunning", err)
	}
	if err := s.ClearCometClassifyStarted(); err != nil {
		t.Fatalf("ClearCometClassifyStarted: %v", err)
	}
	if err := s.SetCometClassifyStarted(time.Hour); err != nil {
		t.Fatalf("SetCometClassifyStarted after clear: %v", err)
	}
}

// makeCometThread creates an ordinary thread with one user message dated
// into the given period, returning its id.
func makeCometThread(t *testing.T, s *Store, id, period string) string {
	t.Helper()
	if err := s.CreateThread(id, "Thread "+id, "deepseek", "web"); err != nil {
		t.Fatalf("CreateThread: %v", err)
	}
	if _, err := s.AddMessage(id, "user", "question", "[]", "[]", 0, "t-"+id); err != nil {
		t.Fatalf("AddMessage: %v", err)
	}
	// Force the message into the period under test — AddMessage stamps
	// CURRENT_TIMESTAMP.
	when := period + "-15 12:00:00"
	if _, err := s.db.Exec(`UPDATE messages SET created_at = ? WHERE thread_id = ?`, when, id); err != nil {
		t.Fatalf("backdating message: %v", err)
	}
	return id
}

func TestCometTrail_LifecycleAndEligibility(t *testing.T) {
	s := openTestStore(t)
	period := "2026-09"
	start, end := period+"-01 00:00:00", "2026-10-01 00:00:00"

	threadID := makeCometThread(t, s, "11111111-1111-1111-1111-111111111111", period)

	ids, err := s.EligibleCometThreads(start, end, period)
	if err != nil {
		t.Fatalf("EligibleCometThreads: %v", err)
	}
	if len(ids) != 1 || ids[0] != threadID {
		t.Fatalf("eligible = %v, want just %s", ids, threadID)
	}

	trailID, err := s.StartTrail(threadID, period)
	if err != nil {
		t.Fatalf("StartTrail: %v", err)
	}

	// Mid-flight, the thread is still eligible (classified_at IS NULL) and
	// HasInFlightTrail reports busy.
	ids, _ = s.EligibleCometThreads(start, end, period)
	if len(ids) != 1 {
		t.Errorf("eligible while in-flight = %v, want the thread still listed", ids)
	}
	if busy, err := s.HasInFlightTrail(); err != nil || !busy {
		t.Errorf("HasInFlightTrail = %v, %v; want true, nil", busy, err)
	}

	if err := s.FinishTrail(trailID, "technology", "curious", "pick a laptop", "yes", 0.01); err != nil {
		t.Fatalf("FinishTrail: %v", err)
	}

	ids, _ = s.EligibleCometThreads(start, end, period)
	if len(ids) != 0 {
		t.Errorf("eligible after finishing = %v, want none", ids)
	}
	if busy, _ := s.HasInFlightTrail(); busy {
		t.Error("HasInFlightTrail should be false once a trail is complete")
	}

	trail, err := s.GetTrail(threadID, period)
	if err != nil || trail == nil {
		t.Fatalf("GetTrail: %v, %v", trail, err)
	}
	if trail.Topic != "technology" || trail.Mood != "curious" || trail.Resolved != "yes" {
		t.Errorf("trail = %+v, want the finished classification", trail)
	}

	// A failed retry re-opens the same row rather than creating a second.
	if _, err := s.StartTrail(threadID, period); err != nil {
		t.Fatalf("re-StartTrail: %v", err)
	}
	if err := s.FailTrail(trailID, "boom", 0.005); err != nil {
		t.Fatalf("FailTrail: %v", err)
	}
	ids, _ = s.EligibleCometThreads(start, end, period)
	if len(ids) != 1 {
		t.Errorf("eligible after failure = %v, want the thread retried", ids)
	}
	var count int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM trails WHERE thread_id = ?`, threadID).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Errorf("trail rows for the thread = %d, want 1 (retry reuses the row)", count)
	}
}

func TestCometTrail_ExcludesPulsarWeaverAndCometThreads(t *testing.T) {
	s := openTestStore(t)
	period := "2026-09"
	start, end := period+"-01 00:00:00", "2026-10-01 00:00:00"

	for i, source := range []string{"web", "pulsar", "weaver", "comet_approach"} {
		id := string(rune('a'+i)) + "1111111-1111-1111-1111-111111111111"
		if err := s.CreateThread(id, "t", "deepseek", source); err != nil {
			t.Fatalf("CreateThread(%s): %v", source, err)
		}
		if _, err := s.AddMessage(id, "user", "q", "[]", "[]", 0, "t"+id); err != nil {
			t.Fatal(err)
		}
		if _, err := s.db.Exec(`UPDATE messages SET created_at = ? WHERE thread_id = ?`, period+"-15 12:00:00", id); err != nil {
			t.Fatal(err)
		}
	}
	ids, err := s.EligibleCometThreads(start, end, period)
	if err != nil {
		t.Fatal(err)
	}
	if len(ids) != 1 {
		t.Fatalf("eligible = %v, want only the web-source thread", ids)
	}
}

func TestCometEdition_RoundTripAndAggregations(t *testing.T) {
	s := openTestStore(t)
	period := "2026-09"

	data := CometEditionData{
		PeriodLabel: "September 2026",
		Stats:       CometStats{Turns: 12, NewStars: 3, Searches: 40, CostUSD: 1.23},
		Categories:  []CometCategory{{Name: "technology", Count: 4}},
		Spiked:      []CometTrend{{Topic: "Docker", Count: 5, Delta: "↑ 5 searches"}},
		Faded:       []CometTrend{{Topic: "Kubernetes", Count: 3, Since: "quiet since Aug 14"}},
		Stars:       []CometStar{{ID: 1, Title: "Uses Go", Category: "software engineering"}},
		StarTotal:   3,
		GhostCount:  2,
	}
	if err := s.UpsertCometEdition(period, "monthly", "A busy month.", data, 0.004); err != nil {
		t.Fatalf("UpsertCometEdition: %v", err)
	}
	if err := s.SetCometLastGenerated("2026-10-01 06:00:00"); err != nil {
		t.Fatalf("SetCometLastGenerated: %v", err)
	}

	got, err := s.GetCometEdition(period)
	if err != nil {
		t.Fatalf("GetCometEdition: %v", err)
	}
	if got.Hook != "A busy month." || got.Data.Stats.Turns != 12 || got.Data.GhostCount != 2 {
		t.Errorf("edition round-trip = %+v", got)
	}
	if len(got.Data.Categories) != 1 || got.Data.Categories[0].Name != "technology" {
		t.Errorf("categories = %+v", got.Data.Categories)
	}

	latest, err := s.LatestCometEdition()
	if err != nil || latest.Period != period {
		t.Errorf("LatestCometEdition = %+v, %v", latest, err)
	}
	periods, err := s.ListCometPeriods()
	if err != nil || len(periods) != 1 || periods[0] != period {
		t.Errorf("ListCometPeriods = %v, %v", periods, err)
	}

	cfg, _ := s.GetCometConfig()
	if cfg.LastGeneratedAt == nil {
		t.Error("SetCometLastGenerated didn't persist")
	}

	// Category breakdown groups completed trails (not the edition blob).
	for i := 0; i < 3; i++ {
		id := "c" + string(rune('0'+i)) + "111111-1111-1111-1111-111111111111"
		if err := s.CreateThread(id, "t", "deepseek", "web"); err != nil {
			t.Fatal(err)
		}
		tid, err := s.StartTrail(id, period)
		if err != nil {
			t.Fatal(err)
		}
		if err := s.FinishTrail(tid, "technology", "curious", "x", "yes", 0); err != nil {
			t.Fatal(err)
		}
	}
	cats, err := s.CometPeriodCategoryBreakdown(period, 5)
	if err != nil {
		t.Fatal(err)
	}
	if len(cats) != 1 || cats[0].Name != "technology" || cats[0].Count != 3 {
		t.Errorf("category breakdown = %+v, want technology:3", cats)
	}
}

func TestCometPeriodCost_IncludesCometSpend(t *testing.T) {
	s := openTestStore(t)

	threadID := "d1111111-1111-1111-1111-111111111111"
	if err := s.CreateThread(threadID, "t", "deepseek", "comet_approach"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.AddMessage(threadID, "assistant", "classified", "[]", "[]", 0.02, "t1"); err != nil {
		t.Fatal(err)
	}
	if err := s.RecordAuxCost("comet_synthesis", 0.005); err != nil {
		t.Fatal(err)
	}

	period := "2026-09"
	start, end := period+"-01 00:00:00", "2026-10-01 00:00:00"
	// Backdate both ledgers into the period.
	if _, err := s.db.Exec(`UPDATE messages SET created_at = ? WHERE thread_id = ?`, period+"-15 12:00:00", threadID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(`UPDATE aux_usage SET created_at = ? WHERE kind = 'comet_synthesis'`, period+"-15 12:00:00"); err != nil {
		t.Fatal(err)
	}

	cost, err := s.CometPeriodCost(start, end)
	if err != nil {
		t.Fatalf("CometPeriodCost: %v", err)
	}
	if cost < 0.0249 || cost > 0.0251 {
		t.Errorf("CometPeriodCost = %v, want ~0.025 (0.02 message + 0.005 aux)", cost)
	}

	// GetStats must route both into the Comet bucket, not Polaris.
	stats, err := s.GetStats(30)
	if err != nil {
		t.Fatalf("GetStats: %v", err)
	}
	if stats.CostBySource.Comet.TotalCostUSD < 0.0249 {
		t.Errorf("CostBySource.Comet.TotalCostUSD = %v, want ~0.025", stats.CostBySource.Comet.TotalCostUSD)
	}
}
