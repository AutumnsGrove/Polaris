package store

import (
	"fmt"
)

// ConstellationStats is Constellation's own cost/observability summary —
// a wholly separate surface from Stats/CostBySource (see the plan doc's
// "Cost tracking and observability" for why Weaver's spend isn't folded
// into the main Polaris/Pulsar/Daily breakdown).
type ConstellationStats struct {
	PeriodDays int `json:"period_days"`

	TotalCostUSD  float64 `json:"total_cost_usd"`
	PeriodCostUSD float64 `json:"period_cost_usd"`

	ShootingStarCount int `json:"shooting_star_count"`

	StarCountsByStatus map[string]int `json:"star_counts_by_status"`

	// ToolCallCounts is scoped to Weaver's real tools only — the cost sum
	// above includes filter_pass/final_answer too, but this breakdown
	// doesn't, same distinction Stats.SearchProviderCounts' doc comment
	// draws between "what actually answered" and "what was billed".
	ToolCallCounts map[string]int `json:"tool_call_counts"`

	ReviewActionCounts map[string]int `json:"review_action_counts"`

	LinksCreatedCount int `json:"links_created_count"`

	MaxTurnsCount   int `json:"max_turns_count"`
	NeedsRetryCount int `json:"needs_retry_count"`
}

// constellationRealTools is Weaver's own five tools plus search_chats — the
// one main-catalog tool Weaver also gets (see catalog.go's WeaverRun
// exclusion, added 2026-09-18) — so its usage shows up in this same
// breakdown instead of being silently invisible here.
var constellationRealTools = []string{"search_stars", "read_star", "create_star", "update_star", "link_stars", "search_chats"}

// GetConstellationStats aggregates on demand from Constellation's own
// tables — same "no running counters, no second source of truth" approach
// GetStats already uses. periodDays of 0 means "all time" for
// PeriodCostUSD too (mirrors Stats' own PeriodDays=0 convention).
func (s *Store) GetConstellationStats(periodDays int) (*ConstellationStats, error) {
	stats := &ConstellationStats{
		PeriodDays:         periodDays,
		StarCountsByStatus: map[string]int{},
		ToolCallCounts:     map[string]int{},
		ReviewActionCounts: map[string]int{},
	}

	// Total/period cost is the sum of shooting_star_events (Weaver's own
	// runs) AND star_reconcile_events (Refine/Edit's one-off calls) — two
	// tables because the latter has no shooting_star_runs row to hang off
	// of (see RecordStarReconcileCost's doc comment), but both are real
	// billed spend and belong in the same total.
	if err := s.db.QueryRow(`SELECT COALESCE(SUM(cost_usd), 0) FROM shooting_star_events`).Scan(&stats.TotalCostUSD); err != nil {
		return nil, fmt.Errorf("constellation stats: total cost: %w", err)
	}
	var reconcileTotal float64
	if err := s.db.QueryRow(`SELECT COALESCE(SUM(cost_usd), 0) FROM star_reconcile_events`).Scan(&reconcileTotal); err != nil {
		return nil, fmt.Errorf("constellation stats: total reconcile cost: %w", err)
	}
	stats.TotalCostUSD += reconcileTotal

	// periodDays is a Go int, never attacker-shaped, so the old
	// fmt.Sprintf-built WHERE clause wasn't actually exploitable — but
	// binding it as a real parameter (via printf's own '-%d days' inside
	// SQLite, since datetime()'s modifier can't itself be a bound string
	// built from a numeric arg without one more layer) matches every other
	// query in this file's fully-parameterized convention instead of being
	// the one exception a future edit could copy-paste into an actually
	// unsafe shape.
	periodFilter := "1=1"
	periodArgs := []any{}
	if periodDays > 0 {
		periodFilter = "created_at >= datetime('now', printf('-%d days', ?))"
		periodArgs = []any{periodDays}
	}

	if err := s.db.QueryRow(`SELECT COALESCE(SUM(cost_usd), 0) FROM shooting_star_events WHERE `+periodFilter, periodArgs...).Scan(&stats.PeriodCostUSD); err != nil {
		return nil, fmt.Errorf("constellation stats: period cost: %w", err)
	}
	var reconcilePeriod float64
	if err := s.db.QueryRow(`SELECT COALESCE(SUM(cost_usd), 0) FROM star_reconcile_events WHERE `+periodFilter, periodArgs...).Scan(&reconcilePeriod); err != nil {
		return nil, fmt.Errorf("constellation stats: period reconcile cost: %w", err)
	}
	stats.PeriodCostUSD += reconcilePeriod

	if err := s.db.QueryRow(`SELECT COUNT(*) FROM shooting_star_runs`).Scan(&stats.ShootingStarCount); err != nil {
		return nil, fmt.Errorf("constellation stats: shooting star count: %w", err)
	}

	statusRows, err := s.db.Query(`SELECT status, COUNT(*) FROM stars WHERE disabled = 0 GROUP BY status`)
	if err != nil {
		return nil, fmt.Errorf("constellation stats: star counts by status: %w", err)
	}
	for statusRows.Next() {
		var status string
		var count int
		if err := statusRows.Scan(&status, &count); err != nil {
			statusRows.Close()
			return nil, fmt.Errorf("constellation stats: star counts by status: %w", err)
		}
		stats.StarCountsByStatus[status] = count
	}
	statusRows.Close()
	if err := statusRows.Err(); err != nil {
		return nil, fmt.Errorf("constellation stats: star counts by status: %w", err)
	}

	toolRows, err := s.db.Query(
		`SELECT tool, COUNT(*) FROM shooting_star_events WHERE tool IN (`+placeholders(len(constellationRealTools))+`) GROUP BY tool`,
		toAnySlice(constellationRealTools)...,
	)
	if err != nil {
		return nil, fmt.Errorf("constellation stats: tool call counts: %w", err)
	}
	for toolRows.Next() {
		var tool string
		var count int
		if err := toolRows.Scan(&tool, &count); err != nil {
			toolRows.Close()
			return nil, fmt.Errorf("constellation stats: tool call counts: %w", err)
		}
		stats.ToolCallCounts[tool] = count
	}
	toolRows.Close()
	if err := toolRows.Err(); err != nil {
		return nil, fmt.Errorf("constellation stats: tool call counts: %w", err)
	}

	reviewRows, err := s.db.Query(`SELECT action, COUNT(*) FROM star_reviews GROUP BY action`)
	if err != nil {
		return nil, fmt.Errorf("constellation stats: review action counts: %w", err)
	}
	for reviewRows.Next() {
		var action string
		var count int
		if err := reviewRows.Scan(&action, &count); err != nil {
			reviewRows.Close()
			return nil, fmt.Errorf("constellation stats: review action counts: %w", err)
		}
		stats.ReviewActionCounts[action] = count
	}
	reviewRows.Close()
	if err := reviewRows.Err(); err != nil {
		return nil, fmt.Errorf("constellation stats: review action counts: %w", err)
	}

	if err := s.db.QueryRow(`SELECT COUNT(*) FROM star_edges`).Scan(&stats.LinksCreatedCount); err != nil {
		return nil, fmt.Errorf("constellation stats: links created count: %w", err)
	}

	if err := s.db.QueryRow(`SELECT COUNT(*) FROM shooting_star_runs WHERE error = 'max_turns_exceeded'`).Scan(&stats.MaxTurnsCount); err != nil {
		return nil, fmt.Errorf("constellation stats: max turns count: %w", err)
	}

	// Distinct threads whose MOST RECENT run still needs a retry — not a
	// raw COUNT(*) over every needs_retry=1 row ever written. needs_retry
	// only ever gets updated on the run row it was set on; a thread that
	// failed once and later succeeded leaves that old row's flag sitting
	// at 1 forever, so a plain COUNT(*) silently double-counts history
	// instead of reporting what's actually stuck right now (confirmed
	// live: stayed at 10 even after every one of those 10 threads had
	// already been retried successfully). Mirrors the exact "most recent
	// run per thread" logic EligibleConstellationThreads' own retry gate
	// already uses, so this reports the same set the scheduler will
	// actually reattempt.
	if err := s.db.QueryRow(`
		SELECT COUNT(*) FROM (
			SELECT thread_id FROM shooting_star_runs r
			WHERE r.id = (SELECT r2.id FROM shooting_star_runs r2 WHERE r2.thread_id = r.thread_id ORDER BY r2.id DESC LIMIT 1)
			  AND r.needs_retry = 1
		)
	`).Scan(&stats.NeedsRetryCount); err != nil {
		return nil, fmt.Errorf("constellation stats: needs retry count: %w", err)
	}

	return stats, nil
}
