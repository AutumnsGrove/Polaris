// constellation_scheduler.go is Constellation's poller — an interval-based
// background check, not live-per-message and not an unconditional nightly
// job (see docs/plans/constellation.md's design principles). Same
// once-a-minute ticker shape as gateway/pulsar_scheduler.go.
package gateway

import (
	"context"
	"time"

	"polaris/config"
	"polaris/llm"
	"polaris/store"
)

// constellationSchedulerInterval is the ticker's own cadence — separate
// from constellation_config.poll_interval_minutes (the per-install
// idle-timing gate checked every tick, see store.EligibleConstellationThreads).
// A short, fixed tick lets a newly-idle thread get picked up promptly once
// it clears the idle gate, without needing a matching per-install ticker
// interval — same relationship pulsarSchedulerInterval has to each
// routine's own schedule.
const constellationSchedulerInterval = time.Minute

// backfillStaleAfter bounds how long constellation_config.backfill_started_at
// is honored before runConstellationTick starts ignoring it — see that
// check's own comment for why this needs a self-healing timeout at all.
const backfillStaleAfter = 4 * time.Hour

// shootingStarStaleAfter bounds how long a shooting_star_runs row can sit
// with finished_at IS NULL before the sweep below (store.Store.
// MarkStaleShootingStarRunsFailed) treats it as abandoned rather than
// genuinely still running — comfortably longer than any real shooting star
// should ever take (the 25-turn cap plus real completion-call latency), so
// this never fires on a merely slow run. A var, not a const, so a test can
// shrink it (save/restore around the call) to exercise the sweep without
// needing to backdate a row's started_at from outside the store package.
var shootingStarStaleAfter = 30 * time.Minute

// RunConstellationScheduler runs until done is closed — see
// RunPulsarScheduler's doc comment for the same shutdown-drain shape.
func (s *Server) RunConstellationScheduler(done <-chan struct{}) {
	runOnce := func() {
		runConstellationTick(context.Background(), s.db, s.liveConfig(), s.shootingStarTurnGate())
	}

	runOnce()
	ticker := time.NewTicker(constellationSchedulerInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			runOnce()
		case <-done:
			return
		}
	}
}

// WeaverClient builds the LLM client Weaver runs on — modelID empty means
// "use whatever config.DefaultModel currently resolves to" (see
// store.ConstellationConfig.Model's doc comment), which cfg.ModelByID
// already falls back to for an empty/unknown id. Same construction shape
// as gateway/pulsar_daily.go's dailyClient.
func WeaverClient(cfg *config.Config, modelID string) llm.ChatClient {
	modelCfg := cfg.ModelByID(modelID)
	return llm.NewClient(cfg.OpenRouter.BaseURL, cfg.OpenRouter.APIKey, modelCfg.Model, modelCfg.Temperature, modelCfg.MaxTokens).
		WithProvider(&llm.ProviderRouting{Order: modelCfg.Provider, AllowFallbacks: boolPtr(true)})
}

// runConstellationTick is RunConstellationScheduler's per-tick body, split
// out as a free function (db/cfg passed explicitly) so it's testable
// without spinning up a full *Server. If Constellation is disabled, or
// nothing is eligible this tick, this makes zero AI calls — a hard
// requirement (see the plan doc's design principles, specifically the
// her-go "dream sequence" failure mode this guards against).
func runConstellationTick(reqCtx context.Context, db *store.Store, cfg *config.Config, gate turnGate) {
	// Ahead of the Enabled check below — a run can be orphaned (crash, OOM,
	// SIGKILL) whether or not Constellation is still enabled by the time
	// the next tick runs, and self-healing it costs nothing (a single
	// bounded UPDATE, no AI calls), so there's no reason to gate it behind
	// the same "zero AI calls when disabled" principle that governs
	// everything below.
	if n, err := db.MarkStaleShootingStarRunsFailed(shootingStarStaleAfter); err != nil {
		log.Warn("constellation: sweeping stale shooting star runs failed", "err", err)
	} else if n > 0 {
		log.Warn("constellation: marked stale shooting star run(s) as needing retry", "count", n)
	}

	cfgRow, err := db.GetConstellationConfig()
	if err != nil {
		log.Warn("constellation: loading config failed", "err", err)
		return
	}
	if !cfgRow.Enabled {
		return
	}
	// A manual `constellation backfill` run is independently computing its
	// own "which threads are eligible" list right now — skip this whole
	// tick rather than race it (see BackfillConstellation's doc comment for
	// the real duplicate-processing bug this fixes). backfillStaleAfter
	// bounds how long a crashed/killed backfill (which never reaches its
	// own defer) can wedge the scheduler off — comfortably past
	// cmd/docker_client.go's own 2-hour timeout on a full-backlog run, so a
	// backfill that's still genuinely running never gets treated as stale.
	if cfgRow.BackfillStartedAt != nil && time.Since(*cfgRow.BackfillStartedAt) < backfillStaleAfter {
		return
	}

	threadIDs, err := db.EligibleConstellationThreads(cfgRow.PollIntervalMinutes)
	if err != nil {
		log.Warn("constellation: listing eligible threads failed", "err", err)
		return
	}

	if len(threadIDs) > 0 {
		client := WeaverClient(cfg, cfgRow.Model)
		// Sequential, never concurrent — each run's writes commit before
		// the next starts, so run N's own search_stars retrieval sees
		// whatever run N-1 just wrote. This is what prevents two threads
		// in the same backlog from independently creating duplicate stars
		// for the same emerging topic — see the plan doc's "Sequential,
		// never concurrent."
		for _, threadID := range threadIDs {
			// Re-checked before every thread, not just once at the top of
			// the tick: a tick can process several threads back-to-back
			// (each a real, possibly-slow agent.Run), and a backfill could
			// start partway through that loop. Bailing out here narrows
			// that race window from "the whole tick" down to "the thread
			// currently in flight" — see BackfillConstellation's own doc
			// comment for the live-observed bug this whole flag exists to
			// prevent.
			if cur, err := db.GetConstellationConfig(); err != nil {
				log.Warn("constellation: re-checking backfill state mid-tick failed", "err", err)
			} else if cur.BackfillStartedAt != nil && time.Since(*cur.BackfillStartedAt) < backfillStaleAfter {
				log.Warn("constellation: backfill started mid-tick, stopping early", "threads_remaining", len(threadIDs))
				break
			}
			// Registers this shooting star with the server's shutdown-drain
			// tracking (see turnGate's doc comment) before it starts, and
			// stops the tick outright — rather than skipping just this one
			// thread and trying the next — the moment a restart is
			// underway: shuttingDown never goes back to false, so every
			// later thread in this same slice would fail the same check
			// anyway.
			if !gate.tryStart() {
				log.Warn("constellation: server is restarting, stopping tick early", "threads_remaining", len(threadIDs))
				break
			}
			err := RunShootingStarRecovered(reqCtx, db, client, threadID)
			gate.finish()
			if err != nil {
				log.Warn("constellation: shooting star failed", "thread_id", threadID, "err", err)
			}
		}
	}

	if err := db.SetConstellationLastChecked(time.Now().UTC().Format("2006-01-02 15:04:05")); err != nil {
		log.Warn("constellation: recording last checked time failed", "err", err)
	}
}
