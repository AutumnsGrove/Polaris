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

// RunConstellationScheduler runs until done is closed — see
// RunPulsarScheduler's doc comment for the same shutdown-drain shape.
func (s *Server) RunConstellationScheduler(done <-chan struct{}) {
	runOnce := func() {
		runConstellationTick(context.Background(), s.db, s.liveConfig())
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
func runConstellationTick(reqCtx context.Context, db *store.Store, cfg *config.Config) {
	cfgRow, err := db.GetConstellationConfig()
	if err != nil {
		log.Warn("constellation: loading config failed", "err", err)
		return
	}
	if !cfgRow.Enabled {
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
			if err := RunShootingStar(reqCtx, db, client, threadID); err != nil {
				log.Warn("constellation: shooting star failed", "thread_id", threadID, "err", err)
			}
		}
	}

	if err := db.SetConstellationLastChecked(time.Now().UTC().Format("2006-01-02 15:04:05")); err != nil {
		log.Warn("constellation: recording last checked time failed", "err", err)
	}
}
