package gateway

import (
	"context"
	"testing"

	"polaris/config"
)

func testConstellationConfig() *config.Config {
	cfg := &config.Config{
		DefaultModel: "test-model",
		Models:       []config.ModelConfig{{ID: "test-model", Name: "Test Model", Model: "test/model"}},
	}
	cfg.OpenRouter.BaseURL = "http://127.0.0.1:0" // never actually dialed when disabled/nothing eligible
	return cfg
}

func TestRunConstellationTick_DisabledMakesNoChanges(t *testing.T) {
	db := openTestStoreForConstellation(t)
	threadID := seedWeaverThread(t, db, "hello")
	// Constellation defaults to disabled (constellation_config.enabled
	// defaults to 0) — never call UpdateConstellationConfig, this tests
	// the true first-read default.

	runConstellationTick(context.Background(), db, testConstellationConfig())

	cfgRow, err := db.GetConstellationConfig()
	if err != nil {
		t.Fatalf("GetConstellationConfig: %v", err)
	}
	if cfgRow.LastCheckedAt != nil {
		t.Error("LastCheckedAt should stay unset when Constellation is disabled — a disabled tick should make zero calls, not even bookkeeping ones")
	}

	run, err := db.LastShootingStarRun(threadID)
	if err != nil {
		t.Fatalf("LastShootingStarRun: %v", err)
	}
	if run != nil {
		t.Errorf("a disabled tick ran a shooting star: %+v", run)
	}
}

func TestRunConstellationTick_NothingEligible_StillRecordsLastChecked(t *testing.T) {
	db := openTestStoreForConstellation(t)
	// A very large poll interval means a just-created thread's message
	// never clears the idle-timing gate — nothing eligible this tick, but
	// the tick itself still ran (Constellation is enabled), so
	// last_checked_at should move regardless.
	seedWeaverThread(t, db, "hello")
	if err := db.UpdateConstellationConfig(true, 999999, ""); err != nil {
		t.Fatalf("UpdateConstellationConfig: %v", err)
	}

	runConstellationTick(context.Background(), db, testConstellationConfig())

	cfgRow, err := db.GetConstellationConfig()
	if err != nil {
		t.Fatalf("GetConstellationConfig: %v", err)
	}
	if cfgRow.LastCheckedAt == nil {
		t.Error("LastCheckedAt should be set once an enabled tick runs, even with nothing eligible")
	}
}

// TestRunConstellationTick_SkipsWhileBackfillInProgress covers the real bug
// this was written to fix: a manual `constellation backfill` run and the
// live per-minute scheduler independently computing "which threads are
// eligible" raced each other and reprocessed the same threads twice
// (live-observed on the potato, 2026-09-12 — see BackfillConstellation's
// doc comment). A tick must make zero calls while backfill_started_at is
// set, exactly like the disabled case, and resume normally once cleared.
func TestRunConstellationTick_SkipsWhileBackfillInProgress(t *testing.T) {
	db := openTestStoreForConstellation(t)
	threadID := seedWeaverThread(t, db, "hello")
	// poll_interval_minutes=0: the seeded thread is eligible immediately,
	// so if the tick doesn't skip, this would otherwise be indistinguishable
	// from "nothing eligible."
	if err := db.UpdateConstellationConfig(true, 0, ""); err != nil {
		t.Fatalf("UpdateConstellationConfig: %v", err)
	}
	if err := db.SetConstellationBackfillStarted(backfillStaleAfter); err != nil {
		t.Fatalf("SetConstellationBackfillStarted: %v", err)
	}

	runConstellationTick(context.Background(), db, testConstellationConfig())

	cfgRow, err := db.GetConstellationConfig()
	if err != nil {
		t.Fatalf("GetConstellationConfig: %v", err)
	}
	if cfgRow.LastCheckedAt != nil {
		t.Error("LastCheckedAt should stay unset while a backfill is in progress — the tick should skip entirely, not even bookkeeping")
	}
	run, err := db.LastShootingStarRun(threadID)
	if err != nil {
		t.Fatalf("LastShootingStarRun: %v", err)
	}
	if run != nil {
		t.Errorf("a tick that should have skipped itself ran a shooting star: %+v", run)
	}

	// Clearing the flag lets the very next tick proceed normally.
	if err := db.ClearConstellationBackfillStarted(); err != nil {
		t.Fatalf("ClearConstellationBackfillStarted: %v", err)
	}
	runConstellationTick(context.Background(), db, testConstellationConfig())
	cfgRow, err = db.GetConstellationConfig()
	if err != nil {
		t.Fatalf("GetConstellationConfig (after clearing): %v", err)
	}
	if cfgRow.LastCheckedAt == nil {
		t.Error("LastCheckedAt should be set once the backfill flag is cleared and a normal tick runs")
	}
}
