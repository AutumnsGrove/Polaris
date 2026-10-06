package store

import (
	"database/sql"
	"fmt"
)

// AddTurnCost records spend incurred after a turn's AddMessage already
// ran (follow-up suggestions, the verification pass, a title
// regeneration) on both ledgers at once: the thread's running total (what
// the thread menu shows) and the assistant message's own cost_usd (what
// GetStats sums, since it's the only one with a timestamp for the
// 30-day window). Updating only one of them used to leave the two
// disagreeing: suggestion and title-regeneration spend was invisible to
// the 30-day figure, verification spend to the all-time one.
//
// messageID 0 means "the thread's latest assistant message" — for a
// thread-level action (title regeneration) that has no turn of its own.
// A message that no longer exists just drops the message half; the thread
// total still gets it.
//
// tier is "answer", "verification", or "oracle" (docs/plans/oracle-mode.md's
// "three-tier cost") — it decides which of cost_answer_usd/
// cost_verification_usd/cost_oracle_usd also gets delta, alongside the
// existing cost_usd total both ledgers already tracked. An unrecognized
// tier still updates cost_usd/threads.cost_usd (never silently drops spend)
// but skips the per-tier column — callers should only ever pass one of the
// three known values.
func (s *Store) AddTurnCost(threadID string, messageID int64, tier string, delta float64) error {
	tierColumn := map[string]string{
		"answer":       "cost_answer_usd",
		"verification": "cost_verification_usd",
		"oracle":       "cost_oracle_usd",
	}[tier]

	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	messageQuery := `UPDATE messages SET cost_usd = cost_usd + ?`
	if tierColumn != "" {
		messageQuery += fmt.Sprintf(", %s = %s + ?", tierColumn, tierColumn)
	}
	args := []interface{}{delta}
	if tierColumn != "" {
		args = append(args, delta)
	}
	if messageID == 0 {
		messageQuery += ` WHERE id = (SELECT MAX(id) FROM messages WHERE thread_id = ? AND role = 'assistant')`
		args = append(args, threadID)
	} else {
		messageQuery += ` WHERE id = ?`
		args = append(args, messageID)
	}
	if _, err = tx.Exec(messageQuery, args...); err != nil {
		return err
	}
	if _, err := tx.Exec(`UPDATE threads SET cost_usd = cost_usd + ? WHERE id = ?`, delta, threadID); err != nil {
		return err
	}
	return tx.Commit()
}

// IncrementAPIUsage bumps provider's call count for the current calendar
// month by one (creating the row at 1 if this is the first call this
// month) and returns the new total — see api_usage's schema comment.
// Callers that need to check the cap before spending a call should use
// GetAPIUsage first; this only records that a call was actually made.
func (s *Store) IncrementAPIUsage(provider string) (int, error) {
	if _, err := s.db.Exec(
		`INSERT INTO api_usage (provider, month, count) VALUES (?, strftime('%Y-%m', 'now'), 1)
		 ON CONFLICT(provider, month) DO UPDATE SET count = count + 1`,
		provider,
	); err != nil {
		return 0, err
	}
	return s.GetAPIUsage(provider)
}

// GetAPIUsage returns provider's call count for the current calendar
// month — 0 if nothing's been recorded yet (a brand-new month, or a
// provider that's never been used).
func (s *Store) GetAPIUsage(provider string) (int, error) {
	var count int
	err := s.db.QueryRow(
		`SELECT count FROM api_usage WHERE provider = ? AND month = strftime('%Y-%m', 'now')`,
		provider,
	).Scan(&count)
	if err == sql.ErrNoRows {
		return 0, nil
	}
	return count, err
}

// RecordGhostCost is historical-only as of the full-fidelity ghost-thread
// redesign — a ghost thread now bills cost through the normal AddMessage/
// AddTurnCost path like any other thread (gateway/turn.go no longer calls
// this), since the row/messages themselves are real from turn one. Old
// rows are kept and still folded into GetStats' totals (see ghost_usage's
// schema comment); nothing writes new ones anymore.
func (s *Store) RecordGhostCost(costUSD float64) error {
	_, err := s.db.Exec(`INSERT INTO ghost_usage (cost_usd) VALUES (?)`, costUSD)
	return err
}

// LogJevCost appends one row for a real Jev API call's cost — see
// jev_usage's schema comment. Called after every call that actually went
// through (never for a call that errored before billing), same convention
// as IncrementBraveUsage/IncrementParallelUsage only recording completed
// calls.
func (s *Store) LogJevCost(costUSD float64) error {
	_, err := s.db.Exec(`INSERT INTO jev_usage (cost_usd) VALUES (?)`, costUSD)
	return err
}

// LogOracleJevCost is LogJevCost for Oracle mode's pre-read call — same
// ledger (so JevCostThisMonth's monthly cap counts it, which the plan's
// "Cost and budgets" section requires), tagged so Stats can show it apart
// from verification spend (issue #125).
func (s *Store) LogOracleJevCost(costUSD float64) error {
	_, err := s.db.Exec(`INSERT INTO jev_usage (cost_usd, source) VALUES (?, 'oracle')`, costUSD)
	return err
}

// LogCompareJevCost is LogJevCost for the compare_sources tool — same
// ledger (the monthly cap sums every row regardless of source), tagged so
// Stats can count it apart from per-claim verification badges, which keep
// source = ”. Rows written before this value existed stay lumped under ”
// and read as badges (issue #151).
func (s *Store) LogCompareJevCost(costUSD float64) error {
	_, err := s.db.Exec(`INSERT INTO jev_usage (cost_usd, source) VALUES (?, 'compare')`, costUSD)
	return err
}

// RecordAuxCost appends one row for real assistant-side LLM spend that no
// turn owns — see aux_usage's schema comment. Called only after a call
// actually completed and reported its cost, same convention as
// LogJevCost/IncrementBraveUsage above.
func (s *Store) RecordAuxCost(kind string, costUSD float64) error {
	_, err := s.db.Exec(`INSERT INTO aux_usage (kind, cost_usd) VALUES (?, ?)`, kind, costUSD)
	return err
}

// JevCostThisMonth returns total Jev spend for the current calendar month —
// 0 if nothing's been recorded yet. Checked before firing a new Jev call to
// enforce the monthly dollar cap, same "check before spending, record after
// spending succeeds" shape as GetAPIUsage/IncrementAPIUsage.
func (s *Store) JevCostThisMonth() (float64, error) {
	var total float64
	err := s.db.QueryRow(
		`SELECT COALESCE(SUM(cost_usd), 0) FROM jev_usage WHERE strftime('%Y-%m', created_at) = strftime('%Y-%m', 'now')`,
	).Scan(&total)
	return total, err
}
