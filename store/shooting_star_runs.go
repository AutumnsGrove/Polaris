package store

import (
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// ShootingStarRun is one row per shooting star — one agent.Run over one
// thread.
type ShootingStarRun struct {
	ID                int64      `json:"id"`
	ThreadID          string     `json:"thread_id"`
	LastMessageIDSeen int64      `json:"last_message_id_seen"`
	StartedAt         time.Time  `json:"started_at"`
	FinishedAt        *time.Time `json:"finished_at"`
	Summary           string     `json:"summary"`
	Error             string     `json:"error"`
	NeedsRetry        bool       `json:"needs_retry"`
	CostUSD           float64    `json:"cost_usd"`
}

// StartShootingStarRun opens a new run row.
func (s *Store) StartShootingStarRun(threadID string, lastMessageIDSeen int64) (int64, error) {
	res, err := s.db.Exec(
		`INSERT INTO shooting_star_runs (thread_id, last_message_id_seen) VALUES (?, ?)`,
		threadID, lastMessageIDSeen,
	)
	if err != nil {
		return 0, fmt.Errorf("start shooting star run: %w", err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return 0, fmt.Errorf("start shooting star run: %w", err)
	}
	return id, nil
}

// FinishShootingStarRun closes out a run: sets finished_at, the closing
// summary/error, needs_retry, and rolls up cost_usd from whatever
// shooting_star_events rows this run logged along the way.
func (s *Store) FinishShootingStarRun(runID int64, summary, errText string, needsRetry bool) error {
	_, err := s.db.Exec(
		`UPDATE shooting_star_runs
		 SET finished_at = CURRENT_TIMESTAMP, summary = ?, error = ?, needs_retry = ?,
		     cost_usd = (SELECT COALESCE(SUM(cost_usd), 0) FROM shooting_star_events WHERE run_id = ?)
		 WHERE id = ?`,
		summary, errText, needsRetry, runID, runID,
	)
	if err != nil {
		return fmt.Errorf("finish shooting star run: %w", err)
	}
	return nil
}

// LastShootingStarRun returns the most recent run for a thread, or nil, nil
// if none exists yet — "no prior run" is the normal, expected state for a
// thread's first pass (see the plan doc's "Thread eligibility").
func (s *Store) LastShootingStarRun(threadID string) (*ShootingStarRun, error) {
	var run ShootingStarRun
	var needsRetry int
	err := s.db.QueryRow(
		`SELECT id, thread_id, last_message_id_seen, started_at, finished_at, summary, error, needs_retry, cost_usd
		 FROM shooting_star_runs WHERE thread_id = ? ORDER BY id DESC LIMIT 1`, threadID,
	).Scan(&run.ID, &run.ThreadID, &run.LastMessageIDSeen, &run.StartedAt, &run.FinishedAt, &run.Summary, &run.Error, &needsRetry, &run.CostUSD)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("last shooting star run: %w", err)
	}
	run.NeedsRetry = needsRetry != 0
	return &run, nil
}

// LastSuccessfulShootingStarRun returns the most recent run that actually
// finished cleanly (needs_retry = 0, finished_at set), or nil, nil if none
// exists — deliberately distinct from LastShootingStarRun, which returns
// the literal last row regardless of outcome and is what callers wanting
// "did the last attempt fail" (tests, admin views) still want.
//
// weaverTaskText's revisit-delta baseline needs this narrower query
// instead: StartShootingStarRun records last_message_id_seen unconditionally
// the moment a run *starts*, success or failure, so a failed run's own row
// already "covers" every message that existed at that point. Live-verified
// bug (2026-09-19): retrying a failed shooting star with no new messages
// since the failure computed an empty delta against that failed run's own
// last_message_id_seen and silently no-opped ("nothing new since the last
// pass") instead of actually retrying the analysis — the retry never
// re-ran Weaver's reasoning at all. Basing the delta on the last
// *successful* run instead means a failed run correctly falls back to
// nil (weaverTaskText's own "first-ever pass" branch), which re-feeds the
// thread's full content rather than a hollow empty delta.
func (s *Store) LastSuccessfulShootingStarRun(threadID string) (*ShootingStarRun, error) {
	var run ShootingStarRun
	var needsRetry int
	err := s.db.QueryRow(
		`SELECT id, thread_id, last_message_id_seen, started_at, finished_at, summary, error, needs_retry, cost_usd
		 FROM shooting_star_runs
		 WHERE thread_id = ? AND needs_retry = 0 AND finished_at IS NOT NULL
		 ORDER BY id DESC LIMIT 1`, threadID,
	).Scan(&run.ID, &run.ThreadID, &run.LastMessageIDSeen, &run.StartedAt, &run.FinishedAt, &run.Summary, &run.Error, &needsRetry, &run.CostUSD)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("last successful shooting star run: %w", err)
	}
	run.NeedsRetry = needsRetry != 0
	return &run, nil
}

// HasInFlightShootingStarRun reports whether any shooting_star_run is
// currently mid-flight (started_at set, finished_at still NULL) — the
// signal the Docker update watcher needs to avoid recreating the container
// out from under a running Weaver pass (see issue #57: a live update once
// landed exactly mid-batch, caught only because the batch happened to
// finish just before the container actually got recreated).
func (s *Store) HasInFlightShootingStarRun() (bool, error) {
	var busy bool
	err := s.db.QueryRow(`SELECT EXISTS(SELECT 1 FROM shooting_star_runs WHERE finished_at IS NULL)`).Scan(&busy)
	if err != nil {
		return false, fmt.Errorf("has in-flight shooting star run: %w", err)
	}
	return busy, nil
}

// MarkStaleShootingStarRunsFailed closes out every shooting_star_runs row
// that's been "in flight" (finished_at IS NULL) for longer than staleAfter
// — the self-healing counterpart to RunShootingStar's own normal
// FinishShootingStarRun call, for the one case that call can never reach:
// the whole process dying mid-run (a crash, OOM, or SIGKILL that skips
// even RunShootingStarRecovered's panic recovery, since that only catches
// a panic within the one goroutine, not the process disappearing out from
// under it). Without this, such a row sits at finished_at IS NULL forever:
// EligibleConstellationThreads' delta gate never re-offers that thread
// (last_message_id_seen already matches the pre-crash high-water mark,
// and needs_retry is still 0) unless new messages happen to arrive later,
// and HasInFlightShootingStarRun (the Docker update watcher's own busy
// check, see its own doc comment referencing issue #57) reports busy=true
// forever too. Called once per scheduler tick and at the start of every
// backfill — cheap (a single bounded UPDATE, no new table), and safe to
// call as often as needed since a run that's actually still healthy never
// matches the staleAfter cutoff.
func (s *Store) MarkStaleShootingStarRunsFailed(staleAfter time.Duration) (int, error) {
	res, err := s.db.Exec(
		`UPDATE shooting_star_runs
		 SET finished_at = CURRENT_TIMESTAMP, error = 'stale: run never finished (process likely crashed or was killed)', needs_retry = 1
		 WHERE finished_at IS NULL AND started_at <= datetime('now', printf('-%d seconds', ?))`,
		int(staleAfter.Seconds()),
	)
	if err != nil {
		return 0, fmt.Errorf("mark stale shooting star runs failed: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("mark stale shooting star runs failed: %w", err)
	}
	return int(n), nil
}
