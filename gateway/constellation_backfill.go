// constellation_backfill.go is the one-time backlog processor behind
// `polaris constellation backfill` (see docs/plans/constellation.md's
// "Backfill") — every eligible thread run through the exact same Weaver
// pipeline the poller uses, just triggered manually and back-to-back
// instead of gated by idle-timing.
package gateway

import (
	"context"
	"fmt"

	"polaris/llm"
	"polaris/store"
)

// BackfillConstellation runs Weaver over every eligible thread
// sequentially (same "never concurrent" reasoning as the scheduler — see
// gateway/constellation_scheduler.go), up to limit threads (0 means every
// eligible thread). A single thread's failure is logged and skipped
// rather than aborting the whole backfill — RunShootingStar already marks
// that thread needs_retry, so a later backfill run or the ordinary
// poller will pick it back up.
//
// Marks constellation_config.backfill_started_at for the duration (cleared
// via defer on every exit path) so the live scheduler's own tick
// (runConstellationTick) skips itself while this runs — a real bug, not
// hypothetical: a live backfill and the ordinary poller independently
// computing "which threads are eligible" every minute raced each other and
// reprocessed the same threads twice, live-observed on the potato
// (2026-09-12, ~74 of ~163 threads double-processed across a ~40 minute
// backfill). A DB column rather than an in-process mutex/flag specifically
// because backfill and the scheduler aren't always the same OS process —
// see the column's schema comment in store.go.
//
// SetConstellationBackfillStarted is a real compare-and-set: a second,
// concurrent BackfillConstellation call (a double-click, a retried request
// after a client-side timeout, or a bare-metal CLI run racing an
// HTTP-triggered one) gets store.ErrBackfillAlreadyRunning back instead of
// silently racing the first one over the same eligible-threads list — the
// same class of bug the flag was originally introduced for, just from a
// different trigger. HasInFlightShootingStarRun is checked too, as a
// best-effort guard against the mirror case (a scheduler tick already
// mid-loop over several threads when a backfill starts) — not a perfect
// lock (there's an inherent gap between this check and the first thread
// actually starting), but it closes the obvious window cheaply.
func BackfillConstellation(reqCtx context.Context, db *store.Store, client llm.ChatClient, limit int) (processed int, err error) {
	if err := db.SetConstellationBackfillStarted(backfillStaleAfter); err != nil {
		return 0, err
	}
	defer func() {
		if clearErr := db.ClearConstellationBackfillStarted(); clearErr != nil {
			log.Warn("constellation backfill: clearing in-progress flag failed", "err", clearErr)
		}
	}()

	if busy, err := db.HasInFlightShootingStarRun(); err != nil {
		log.Warn("constellation backfill: checking in-flight run failed", "err", err)
	} else if busy {
		return 0, fmt.Errorf("constellation backfill: a shooting star is already in flight (a scheduler tick may be mid-run) — try again shortly")
	}

	ids, err := db.EligibleConstellationThreadsForBackfill(limit)
	if err != nil {
		return 0, err
	}
	for _, threadID := range ids {
		if err := RunShootingStarRecovered(reqCtx, db, client, threadID); err != nil {
			log.Warn("constellation backfill: shooting star failed", "thread_id", threadID, "err", err)
			continue
		}
		processed++
	}
	return processed, nil
}
