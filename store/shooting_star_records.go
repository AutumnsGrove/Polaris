package store

import (
	"database/sql"
	"fmt"
)

// RecordShootingStarCandidate logs one topic candidate Weaver proposed
// within a run — called as a side effect of create_star/update_star, not a
// separate logging step.
func (s *Store) RecordShootingStarCandidate(runID int64, title, confidenceClass, decision, reasoning string, resultingStarID *int64) error {
	_, err := s.db.Exec(
		`INSERT INTO shooting_star_candidates (run_id, title, confidence_class, decision, reasoning, resulting_star_id)
		 VALUES (?, ?, ?, ?, ?, ?)`,
		runID, title, confidenceClass, decision, reasoning, resultingStarID,
	)
	if err != nil {
		return fmt.Errorf("record shooting star candidate: %w", err)
	}
	return nil
}

// LatestCandidateReasoning returns the most recent
// shooting_star_candidates.reasoning recorded for a star — the "why" a
// human reviewing it in the Inbox needs (see the plan doc's "Reviewing a
// proposed star": the Review screen surfaces this as a "Why this needs a
// look" block, the same sentence Weaver already logged for the trace
// tables, not a separately-authored summary). Returns "" if the star has
// no candidate row at all (shouldn't normally happen — create_star/
// update_star always log one — but a star reached some other way
// shouldn't 500 over it).
func (s *Store) LatestCandidateReasoning(starID int64) (string, error) {
	var reasoning string
	err := s.db.QueryRow(
		// id DESC, not created_at DESC — created_at only has second
		// resolution, so two candidates recorded within the same second
		// would otherwise tie arbitrarily. See LatestCandidateReasoningBulk's
		// identical, already-fixed concern.
		`SELECT reasoning FROM shooting_star_candidates
		 WHERE resulting_star_id = ? ORDER BY id DESC LIMIT 1`,
		starID,
	).Scan(&reasoning)
	if err == sql.ErrNoRows {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("latest candidate reasoning: %w", err)
	}
	return reasoning, nil
}

// LatestCandidateReasoningBulk is LatestCandidateReasoning for a whole set
// of stars in one query — the Inbox list (handleListConstellationStars'
// "inbox" section) needs every proposed star's own "why" for its card,
// same reasoning text the Review screen's "Why this needs a look" block
// already surfaces, and doing that as one N-star query instead of N
// separate round trips. Stars with no candidate row (shouldn't normally
// happen) are simply absent from the returned map.
func (s *Store) LatestCandidateReasoningBulk(starIDs []int64) (map[int64]string, error) {
	out := map[int64]string{}
	if len(starIDs) == 0 {
		return out, nil
	}
	args := make([]any, len(starIDs))
	for i, id := range starIDs {
		args[i] = id
	}
	rows, err := s.db.Query(
		`SELECT resulting_star_id, reasoning FROM shooting_star_candidates
		 WHERE resulting_star_id IN (`+placeholders(len(starIDs))+`)
		 ORDER BY id ASC`,
		args...,
	)
	if err != nil {
		return nil, fmt.Errorf("latest candidate reasoning bulk: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var id int64
		var reasoning string
		if err := rows.Scan(&id, &reasoning); err != nil {
			return nil, fmt.Errorf("latest candidate reasoning bulk: %w", err)
		}
		// ORDER BY id ASC (not created_at, which only has second
		// resolution — two candidates for the same star recorded within
		// the same second would otherwise tie) + overwrite-on-scan means
		// the last write wins per id, i.e. the most recently inserted row.
		out[id] = reasoning
	}
	return out, rows.Err()
}

// RecordStarReconcileCost logs the real, billed cost of one Refine/Edit
// LLM call (see gateway/constellation_routes.go's reconcileAndSaveStar) —
// these calls aren't part of any shooting_star_runs row, so they can't use
// RecordShootingStarEvent (whose run_id column is NOT NULL). Recorded
// regardless of whether the correction turned out to be a flat denial
// (invalidated=true) — the model call itself was made and billed either
// way, so the cost is real even when its content is discarded.
func (s *Store) RecordStarReconcileCost(starID int64, costUSD float64) error {
	_, err := s.db.Exec(`INSERT INTO star_reconcile_events (star_id, cost_usd) VALUES (?, ?)`, starID, costUSD)
	if err != nil {
		return fmt.Errorf("record star reconcile cost: %w", err)
	}
	return nil
}

// SetStarStatusAndRecordReview atomically updates a star's status and logs
// the review action that caused it — approve/discard/refine each write
// both rows together, so a crash between the two writes (server restart,
// process kill) can never leave a star's status changed with no matching
// star_reviews row, or vice versa. Previously these were two independent
// Execs in the HTTP handler.
func (s *Store) SetStarStatusAndRecordReview(starID int64, status, action, correction string) error {
	tx, err := s.db.Begin()
	if err != nil {
		return fmt.Errorf("set star status and record review: %w", err)
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`UPDATE stars SET status = ?, updated_at = CURRENT_TIMESTAMP WHERE id = ?`, status, starID); err != nil {
		return fmt.Errorf("set star status and record review: %w", err)
	}
	if _, err := tx.Exec(`INSERT INTO star_reviews (star_id, action, correction) VALUES (?, ?, ?)`, starID, action, correction); err != nil {
		return fmt.Errorf("set star status and record review: %w", err)
	}
	return tx.Commit()
}

// RecordShootingStarEvent logs one completion call in Weaver's loop —
// every turn produces a row, whether it called a tool or ended the run in
// plain text ('final_answer'), plus the double-RAG filter pass on a
// revisit ('filter_pass').
func (s *Store) RecordShootingStarEvent(runID int64, tool, args, result string, costUSD float64) error {
	_, err := s.db.Exec(
		`INSERT INTO shooting_star_events (run_id, tool, args, result, cost_usd) VALUES (?, ?, ?, ?, ?)`,
		runID, tool, args, result, costUSD,
	)
	if err != nil {
		return fmt.Errorf("record shooting star event: %w", err)
	}
	return nil
}

// RecordStarReview logs one Inbox review action (approved/refined/
// discarded) — scoped to Inbox review only, see the plan doc's
// star_reviews section for why an ordinary Edit-star correction doesn't
// write here.
func (s *Store) RecordStarReview(starID int64, action, correction string) error {
	_, err := s.db.Exec(
		`INSERT INTO star_reviews (star_id, action, correction) VALUES (?, ?, ?)`,
		starID, action, correction,
	)
	if err != nil {
		return fmt.Errorf("record star review: %w", err)
	}
	return nil
}
