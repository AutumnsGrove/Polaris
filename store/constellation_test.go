package store

import (
	"testing"
	"time"

	"github.com/google/uuid"
)

func boolPtr(b bool) *bool { return &b }

func TestConstellationConfig_DefaultsThenUpdate(t *testing.T) {
	s := openTestStore(t)

	c, err := s.GetConstellationConfig()
	if err != nil {
		t.Fatalf("GetConstellationConfig: %v", err)
	}
	if c.Enabled {
		t.Error("Enabled should default to false")
	}
	if c.PollIntervalMinutes != 60 {
		t.Errorf("PollIntervalMinutes = %d, want 60", c.PollIntervalMinutes)
	}
	if c.Model != "" {
		t.Errorf("Model = %q, want empty (inherit default)", c.Model)
	}
	if c.LastCheckedAt != nil {
		t.Errorf("LastCheckedAt should be unset initially, got %+v", c.LastCheckedAt)
	}
	if err := s.UpdateConstellationConfig(true, 30, "deepseek-pro"); err != nil {
		t.Fatalf("UpdateConstellationConfig: %v", err)
	}
	c, err = s.GetConstellationConfig()
	if err != nil {
		t.Fatalf("GetConstellationConfig (after update): %v", err)
	}
	if !c.Enabled || c.PollIntervalMinutes != 30 || c.Model != "deepseek-pro" {
		t.Errorf("GetConstellationConfig after update = %+v, want the values just written", c)
	}

	if err := s.SetConstellationLastChecked("2026-09-10 12:00:00"); err != nil {
		t.Fatalf("SetConstellationLastChecked: %v", err)
	}
	c, err = s.GetConstellationConfig()
	if err != nil {
		t.Fatalf("GetConstellationConfig (after SetConstellationLastChecked): %v", err)
	}
	if c.LastCheckedAt == nil {
		t.Error("LastCheckedAt should be set after SetConstellationLastChecked")
	}
}

func TestConstellationConfig_BackfillStartedRoundTrips(t *testing.T) {
	s := openTestStore(t)

	c, err := s.GetConstellationConfig()
	if err != nil {
		t.Fatalf("GetConstellationConfig: %v", err)
	}
	if c.BackfillStartedAt != nil {
		t.Errorf("BackfillStartedAt should default to nil, got %+v", c.BackfillStartedAt)
	}

	if err := s.SetConstellationBackfillStarted(time.Hour); err != nil {
		t.Fatalf("SetConstellationBackfillStarted: %v", err)
	}
	c, err = s.GetConstellationConfig()
	if err != nil {
		t.Fatalf("GetConstellationConfig (after SetConstellationBackfillStarted): %v", err)
	}
	if c.BackfillStartedAt == nil {
		t.Fatal("BackfillStartedAt should be set after SetConstellationBackfillStarted")
	}

	// A second concurrent call must not silently "win" too — this is the
	// compare-and-set that prevents two overlapping backfills from
	// independently computing the eligible-threads list and racing each
	// other (see the function's own doc comment).
	if err := s.SetConstellationBackfillStarted(time.Hour); err != ErrBackfillAlreadyRunning {
		t.Errorf("second SetConstellationBackfillStarted while already running = %v, want ErrBackfillAlreadyRunning", err)
	}

	// A stale flag (older than staleAfter) must not block forever — a
	// crashed backfill that never reached its own defer shouldn't wedge
	// every future attempt.
	if _, err := s.db.Exec(`UPDATE constellation_config SET backfill_started_at = datetime('now', '-2 hours') WHERE id = 1`); err != nil {
		t.Fatalf("backdating backfill_started_at: %v", err)
	}
	if err := s.SetConstellationBackfillStarted(time.Hour); err != nil {
		t.Errorf("SetConstellationBackfillStarted with a stale flag = %v, want nil (stale flags must be reclaimable)", err)
	}

	if err := s.ClearConstellationBackfillStarted(); err != nil {
		t.Fatalf("ClearConstellationBackfillStarted: %v", err)
	}
	c, err = s.GetConstellationConfig()
	if err != nil {
		t.Fatalf("GetConstellationConfig (after ClearConstellationBackfillStarted): %v", err)
	}
	if c.BackfillStartedAt != nil {
		t.Errorf("BackfillStartedAt should be nil after ClearConstellationBackfillStarted, got %+v", c.BackfillStartedAt)
	}
}

// seedThread inserts a minimal thread row plus one message, so
// foreign-key-referencing tests (star_sources, shooting_star_runs) have a
// valid thread_id to point at, and eligibility-gate tests
// (EligibleConstellationThreads, keyed off messages' created_at/id) have
// something to gate on.
func seedThread(t *testing.T, s *Store) string {
	t.Helper()
	id := uuid.NewString()
	if err := s.CreateThread(id, "Test thread", "deepseek", "web"); err != nil {
		t.Fatalf("CreateThread: %v", err)
	}
	if _, err := s.AddMessage(id, "user", "hello", "[]", "[]", 0, ""); err != nil {
		t.Fatalf("AddMessage: %v", err)
	}
	return id
}
