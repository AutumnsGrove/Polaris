package store

import (
	"testing"
	"time"
)

func TestShootingStarRun_LifecycleAndRetry(t *testing.T) {
	s := openTestStore(t)
	threadID := seedThread(t, s)

	runID, err := s.StartShootingStarRun(threadID, 5)
	if err != nil {
		t.Fatalf("StartShootingStarRun: %v", err)
	}

	last, err := s.LastShootingStarRun(threadID)
	if err != nil {
		t.Fatalf("LastShootingStarRun: %v", err)
	}
	if last.ID != runID || last.LastMessageIDSeen != 5 {
		t.Errorf("LastShootingStarRun = %+v, want the run just started", last)
	}
	if last.NeedsRetry {
		t.Error("a fresh run should not need retry before it's finished")
	}

	if err := s.FinishShootingStarRun(runID, "Noted a new interest in Cloudflare Workers.", "", false); err != nil {
		t.Fatalf("FinishShootingStarRun: %v", err)
	}
	last, err = s.LastShootingStarRun(threadID)
	if err != nil {
		t.Fatalf("LastShootingStarRun (after finish): %v", err)
	}
	if last.Summary == "" || last.NeedsRetry {
		t.Errorf("LastShootingStarRun after success = %+v, want summary set and needs_retry false", last)
	}

	// A failing run sets needs_retry and error.
	runID2, err := s.StartShootingStarRun(threadID, 9)
	if err != nil {
		t.Fatalf("StartShootingStarRun (2nd): %v", err)
	}
	if err := s.FinishShootingStarRun(runID2, "", "max_turns_exceeded", true); err != nil {
		t.Fatalf("FinishShootingStarRun (failure): %v", err)
	}
	last, err = s.LastShootingStarRun(threadID)
	if err != nil {
		t.Fatalf("LastShootingStarRun (after failure): %v", err)
	}
	if !last.NeedsRetry || last.Error != "max_turns_exceeded" {
		t.Errorf("LastShootingStarRun after failure = %+v, want needs_retry=true, error=max_turns_exceeded", last)
	}
}

// TestGetConstellationWeekFeed_IncludesStarID covers a real bug: none of
// the three feed queries (new/updated/linked) ever selected a star's id,
// only its title — the "This week" page's rows had no way to navigate
// anywhere at all when tapped, since ConstellationWeekItem carried nothing
// to link to.
func TestHasInFlightShootingStarRun(t *testing.T) {
	s := openTestStore(t)

	busy, err := s.HasInFlightShootingStarRun()
	if err != nil {
		t.Fatalf("HasInFlightShootingStarRun: %v", err)
	}
	if busy {
		t.Error("busy = true with no runs at all, want false")
	}

	threadID := seedThread(t, s)
	runID, err := s.StartShootingStarRun(threadID, 1)
	if err != nil {
		t.Fatalf("StartShootingStarRun: %v", err)
	}

	busy, err = s.HasInFlightShootingStarRun()
	if err != nil {
		t.Fatalf("HasInFlightShootingStarRun: %v", err)
	}
	if !busy {
		t.Error("busy = false with a started, unfinished run, want true")
	}

	if err := s.FinishShootingStarRun(runID, "done", "", false); err != nil {
		t.Fatalf("FinishShootingStarRun: %v", err)
	}

	busy, err = s.HasInFlightShootingStarRun()
	if err != nil {
		t.Fatalf("HasInFlightShootingStarRun: %v", err)
	}
	if busy {
		t.Error("busy = true after the only run finished, want false")
	}
}

// TestMarkStaleShootingStarRunsFailed covers the self-healing sweep for a
// run whose process died mid-flight (crash/OOM/SIGKILL) before it ever
// reached its own FinishShootingStarRun call — without this, such a row
// sits at finished_at IS NULL forever, permanently wedging both
// HasInFlightShootingStarRun (reports busy=true forever) and
// EligibleConstellationThreads (the thread never becomes eligible again
// unless new messages happen to arrive).
func TestMarkStaleShootingStarRunsFailed(t *testing.T) {
	s := openTestStore(t)

	freshThread := seedThread(t, s)
	freshRunID, err := s.StartShootingStarRun(freshThread, 1)
	if err != nil {
		t.Fatalf("StartShootingStarRun (fresh): %v", err)
	}

	staleThread := seedThread(t, s)
	staleRunID, err := s.StartShootingStarRun(staleThread, 1)
	if err != nil {
		t.Fatalf("StartShootingStarRun (stale): %v", err)
	}
	if _, err := s.db.Exec(`UPDATE shooting_star_runs SET started_at = datetime('now', '-2 hours') WHERE id = ?`, staleRunID); err != nil {
		t.Fatalf("backdating started_at: %v", err)
	}

	n, err := s.MarkStaleShootingStarRunsFailed(time.Hour)
	if err != nil {
		t.Fatalf("MarkStaleShootingStarRunsFailed: %v", err)
	}
	if n != 1 {
		t.Errorf("MarkStaleShootingStarRunsFailed count = %d, want 1 (only the 2-hour-old run)", n)
	}

	staleRun, err := s.LastShootingStarRun(staleThread)
	if err != nil {
		t.Fatalf("LastShootingStarRun (stale): %v", err)
	}
	if staleRun.FinishedAt == nil {
		t.Error("stale run FinishedAt is nil, want it closed out")
	}
	if !staleRun.NeedsRetry {
		t.Error("stale run NeedsRetry = false, want true (so the thread becomes eligible again)")
	}
	if staleRun.Error == "" {
		t.Error("stale run Error is empty, want a human-readable reason")
	}

	freshRun, err := s.LastShootingStarRun(freshThread)
	if err != nil {
		t.Fatalf("LastShootingStarRun (fresh): %v", err)
	}
	if freshRun.FinishedAt != nil {
		t.Error("fresh (recently-started) run was closed out, want it left alone")
	}

	busy, err := s.HasInFlightShootingStarRun()
	if err != nil {
		t.Fatalf("HasInFlightShootingStarRun: %v", err)
	}
	if !busy {
		t.Error("busy = false, want true (the fresh run is still legitimately in flight)")
	}

	// Finish the fresh run too, then confirm a second sweep is a no-op —
	// nothing left to mark.
	if err := s.FinishShootingStarRun(freshRunID, "done", "", false); err != nil {
		t.Fatalf("FinishShootingStarRun: %v", err)
	}
	n, err = s.MarkStaleShootingStarRunsFailed(time.Hour)
	if err != nil {
		t.Fatalf("MarkStaleShootingStarRunsFailed (second sweep): %v", err)
	}
	if n != 0 {
		t.Errorf("second sweep count = %d, want 0", n)
	}
}
