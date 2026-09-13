package gateway

import (
	"context"
	"testing"

	"polaris/llm"
	"polaris/llm/llmtest"
)

func TestBackfillConstellation_ProcessesEligibleThreadsUpToLimit(t *testing.T) {
	db := openTestStoreForConstellation(t)
	seedWeaverThread(t, db, "thread one")
	seedWeaverThread(t, db, "thread two")
	seedWeaverThread(t, db, "thread three")

	// Each thread's first pass makes exactly one call to the mock (a
	// plain-text answer, no tool call) — three threads means three queued
	// responses if -n is unlimited, or fewer if capped.
	mock := &llmtest.MockClient{Responses: []llmtest.Response{
		{Resp: &llm.ChatResponse{Content: "Noted."}},
		{Resp: &llm.ChatResponse{Content: "Noted."}},
	}}

	processed, err := BackfillConstellation(context.Background(), db, mock, 2, NoopTurnGate())
	if err != nil {
		t.Fatalf("BackfillConstellation: %v", err)
	}
	if processed != 2 {
		t.Errorf("processed = %d, want 2 (capped by limit)", processed)
	}
}

func TestBackfillConstellation_ZeroLimitProcessesEverything(t *testing.T) {
	db := openTestStoreForConstellation(t)
	seedWeaverThread(t, db, "thread one")
	seedWeaverThread(t, db, "thread two")

	mock := &llmtest.MockClient{Responses: []llmtest.Response{
		{Resp: &llm.ChatResponse{Content: "Noted."}},
		{Resp: &llm.ChatResponse{Content: "Noted."}},
	}}

	processed, err := BackfillConstellation(context.Background(), db, mock, 0, NoopTurnGate())
	if err != nil {
		t.Fatalf("BackfillConstellation: %v", err)
	}
	if processed != 2 {
		t.Errorf("processed = %d, want 2 (every eligible thread)", processed)
	}
}

// TestBackfillConstellation_StopsWhenGateRefuses covers the shutdown-drain
// integration: a gate that starts refusing partway through (simulating a
// `polaris restart` beginning mid-backfill) must stop the loop rather than
// silently continuing to fire shooting stars a restart is already trying
// to drain around.
func TestBackfillConstellation_StopsWhenGateRefuses(t *testing.T) {
	db := openTestStoreForConstellation(t)
	seedWeaverThread(t, db, "thread one")
	seedWeaverThread(t, db, "thread two")
	seedWeaverThread(t, db, "thread three")

	mock := &llmtest.MockClient{Responses: []llmtest.Response{
		{Resp: &llm.ChatResponse{Content: "Noted."}},
	}}

	// Allows exactly one shooting star through, then refuses every
	// subsequent one — same shape TryStartTurn takes once shuttingDown
	// flips true.
	allowed := 0
	gate := turnGate{
		tryStart: func() bool {
			if allowed >= 1 {
				return false
			}
			allowed++
			return true
		},
		finish: func() {},
	}

	processed, err := BackfillConstellation(context.Background(), db, mock, 0, gate)
	if err != nil {
		t.Fatalf("BackfillConstellation: %v", err)
	}
	if processed != 1 {
		t.Errorf("processed = %d, want 1 (stopped after the gate refused the second thread)", processed)
	}
}

// TestBackfillConstellation_SweepsStaleRunsBeforeBusyCheck covers why the
// sweep has to run before HasInFlightShootingStarRun's check: a run
// orphaned by an earlier crashed/killed process would otherwise wedge that
// check at busy=true forever, refusing every future backfill attempt for
// no real reason. Sweeping it also puts the thread's retry gate back in
// play immediately (needs_retry=1), so this same backfill call goes on to
// pick it back up — since nothing changed since the original (never
// actually processed) attempt, that reprocessing is a harmless no-op that
// clears needs_retry again, not a fresh LLM call.
func TestBackfillConstellation_SweepsStaleRunsBeforeBusyCheck(t *testing.T) {
	db := openTestStoreForConstellation(t)
	threadID := seedWeaverThread(t, db, "an old thread")

	if _, err := db.StartShootingStarRun(threadID, 1); err != nil {
		t.Fatalf("StartShootingStarRun: %v", err)
	}
	oldStaleAfter := shootingStarStaleAfter
	shootingStarStaleAfter = 0
	defer func() { shootingStarStaleAfter = oldStaleAfter }()

	mock := &llmtest.MockClient{}
	processed, err := BackfillConstellation(context.Background(), db, mock, 0, NoopTurnGate())
	if err != nil {
		t.Fatalf("BackfillConstellation: %v (a stale run should have been swept before the busy check, not blocked it)", err)
	}
	if processed != 1 {
		t.Errorf("processed = %d, want 1 (the swept thread's retry gate makes it eligible again, and this call picks it back up as a no-op)", processed)
	}

	run, err := db.LastShootingStarRun(threadID)
	if err != nil {
		t.Fatalf("LastShootingStarRun: %v", err)
	}
	if run.FinishedAt == nil {
		t.Fatalf("run wasn't finished: %+v", run)
	}
	if run.NeedsRetry {
		t.Error("run.NeedsRetry = true, want false (the reprocessing succeeded and should have cleared it)")
	}
}
