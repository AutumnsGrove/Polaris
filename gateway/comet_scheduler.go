// comet_scheduler.go is Comet's monthly trigger. It deliberately does not
// add a new ticker: the existing once-a-minute Pulsar scheduler tick already
// runs for the life of the process, so Comet's due-check piggybacks on it
// (see docs/plans/comet.md's "Trigger", which calls for exactly this). Each
// tick, if the current month's scheduled moment has passed and no edition
// has been produced for it yet, Comet classifies the previous complete
// month and generates its retrospective.
package gateway

import (
	"context"
	"time"

	"polaris/store"
	"polaris/tz"
)

// cometClassifyStaleAfter bounds how long comet_config.classify_started_at
// is honored before a new run ignores it — the self-healing timeout for a
// classify process that died before its own defer could clear the marker.
// Comfortably past a full month's classify run.
const cometClassifyStaleAfter = 4 * time.Hour

// cometRetryBackoff is how long after a failed generation attempt the
// scheduler waits before trying the same period again. Without it, a
// persistent failure (a bad model response, a thread that keeps erroring)
// would re-run the whole pipeline on every once-a-minute tick.
const cometRetryBackoff = time.Hour

// maybeCometScheduled is the once-a-minute entry point, called from
// RunPulsarScheduler's runOnce alongside isDailyDue. It makes zero AI calls
// unless Comet is enabled *and* the month's slot is due (and not already
// being generated) — same hard requirement runConstellationTick honors.
func (s *Server) maybeCometScheduled() {
	cfgRow, err := s.db.GetCometConfig()
	if err != nil {
		log.Warn("comet: loading config failed", "err", err)
		return
	}
	now := tz.Now()
	if !cometDue(cfgRow, now) {
		return
	}
	period := cometPeriodOf(now)
	if !s.startCometGenerationIfIdle(period) {
		// A manual "Generate now" click won this instant's race — same
		// benign skip startDailyGenerationIfIdle's own caller logs.
		log.Info("comet: due, but a generation was already running — skipping this tick")
	}
}

// cometDue reports whether a period is due to be generated. The scheduled
// moment is the most recent 1st-of-month at time_of_day (walking back a
// month when "now" is before this month's own slot) — so on the 1st at
// 06:00 it becomes due and the previous complete month is generated.
//
// last_generated_at is the success baseline; last_attempt_at adds retry
// throttling so a failure waits out cometRetryBackoff rather than
// re-firing on the next tick. A successful run sets both, so the ordinary
// case is simply "baseline < scheduled".
func cometDue(c *store.CometConfig, now time.Time) bool {
	if !c.Enabled {
		return false
	}
	hour, minute, ok := parseTimeOfDay(c.TimeOfDay)
	if !ok {
		return false
	}
	scheduled := time.Date(now.Year(), now.Month(), 1, hour, minute, 0, 0, now.Location())
	if scheduled.After(now) {
		scheduled = scheduled.AddDate(0, -1, 0)
	}
	baseline := c.CreatedAt
	if c.LastGeneratedAt != nil {
		baseline = *c.LastGeneratedAt
	}
	if !baseline.Before(scheduled) {
		return false
	}
	if c.LastAttemptAt != nil && c.LastAttemptAt.After(scheduled) && now.Sub(*c.LastAttemptAt) < cometRetryBackoff {
		return false
	}
	return true
}

// startCometGenerationIfIdle starts a full Comet run for period unless one
// is already in flight, returning false in that case. The single-flight
// guard is shared by the scheduler's due-check and the manual "Generate
// now" button, exactly as startDailyGenerationIfIdle's is — without it a
// click landing in the same instant the scheduler decides the month is due
// would run two pipelines over the same period.
func (s *Server) startCometGenerationIfIdle(period string) bool {
	if !s.cometGenerationRunning.CompareAndSwap(false, true) {
		return false
	}
	go func() {
		defer s.cometGenerationRunning.Store(false)
		s.runCometPipelineRecovered(period)
	}()
	return true
}

// runCometPipelineRecovered wraps runCometPipeline with the shutdown-drain
// registration and panic recovery every detached background turn needs —
// same reasoning as runDailyPipelineRecovered.
func (s *Server) runCometPipelineRecovered(period string) {
	if !s.TryStartTurn() {
		log.Warn("skipping comet generation — server is restarting", "period", period)
		return
	}
	defer s.FinishTurn()
	defer func() {
		if rec := recover(); rec != nil {
			log.Error("panic running comet pipeline", "period", period, "panic", rec)
		}
	}()
	s.runCometPipeline(context.Background(), period)
}

// runCometPipeline is classify-then-generate for one period, in that order
// — the plan doc's hard dependency: the retrospective aggregates Trails, so
// they must exist before it renders. It stamps last_attempt_at up front
// (retry throttling, and crash-safety: a process dying mid-run must not
// look like "never attempted").
func (s *Server) runCometPipeline(reqCtx context.Context, period string) {
	if err := s.db.SetCometLastAttempt(time.Now().UTC().Format("2006-01-02 15:04:05")); err != nil {
		log.Warn("comet: recording attempt time failed", "err", err)
		return
	}

	// An empty month is a normal outcome, not a failure: skip it before any
	// classify/generate work, and stamp last_generated_at so cometDue doesn't
	// re-offer the same empty month every hour until the next 1st.
	startStr, endStr, ok := cometPeriodDB(period)
	if !ok {
		log.Warn("comet: invalid period", "period", period)
		return
	}
	if has, herr := s.db.CometPeriodHasMessages(startStr, endStr); herr != nil {
		log.Warn("comet: checking period for data failed", "period", period, "err", herr)
		return
	} else if !has {
		log.Info("comet: no conversations in period, nothing to generate", "period", period)
		if serr := s.db.SetCometLastGenerated(time.Now().UTC().Format("2006-01-02 15:04:05")); serr != nil {
			log.Warn("comet: recording skipped period failed", "err", serr)
		}
		return
	}

	processed, err := s.RunCometClassify(period, 0)
	if err != nil {
		log.Warn("comet: classify pass failed", "period", period, "err", err)
		return
	}
	log.Info("comet: classify pass finished", "period", period, "processed", processed)

	// "No partial-month tolerance": if any thread in the period still has no
	// completed trail (a failed Approach surviving its retry), generating
	// now would produce a retrospective that silently omits it. Skip this
	// attempt and let the scheduler retry after cometRetryBackoff; a manual
	// `polaris comet classify` can be run to see what's failing.
	if remaining, rerr := s.db.EligibleCometThreads(startStr, endStr, period); rerr != nil {
		log.Warn("comet: re-checking eligible threads failed", "err", rerr)
	} else if len(remaining) > 0 {
		log.Warn("comet: period's trails are incomplete, deferring generation", "period", period, "missing", len(remaining))
		return
	}

	if err := s.runCometGeneration(reqCtx, period); err != nil {
		log.Warn("comet: generation failed", "period", period, "err", err)
	}
}
