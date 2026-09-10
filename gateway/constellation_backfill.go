// constellation_backfill.go is the one-time backlog processor behind
// `polaris constellation backfill` (see docs/plans/constellation.md's
// "Backfill") — every eligible thread run through the exact same Weaver
// pipeline the poller uses, just triggered manually and back-to-back
// instead of gated by idle-timing.
package gateway

import (
	"context"

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
func BackfillConstellation(reqCtx context.Context, db *store.Store, client llm.ChatClient, limit int) (processed int, err error) {
	ids, err := db.EligibleConstellationThreadsForBackfill(limit)
	if err != nil {
		return 0, err
	}
	for _, threadID := range ids {
		if err := RunShootingStar(reqCtx, db, client, threadID); err != nil {
			log.Warn("constellation backfill: shooting star failed", "thread_id", threadID, "err", err)
			continue
		}
		processed++
	}
	return processed, nil
}
