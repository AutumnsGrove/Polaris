// stats.go aggregates usage/tuning numbers on demand from the
// threads/messages/events tables — no running counters kept anywhere
// else, since a single-user install's data volume (see the package doc's
// single-user framing) makes a full scan per request cheap enough not to
// bother with a second source of truth to keep in sync.
package store

import (
	"encoding/json"
	"strings"
	"time"
)

// Stats is a lightweight usage/tuning summary — surfaced via the CLI's
// `polaris stats`, GET /api/stats, and a small settings-panel section.
// Deliberately plain counts and percentages, not a time series: this is
// meant to answer "is the research loop well-tuned" and "what's this
// costing me lately" at a glance, not to be a dashboard.
type Stats struct {
	// PeriodDays is 0 for "all time", otherwise how many trailing days
	// PeriodCostUSD/TurnCount/ToolCallCounts/nudge counts below cover —
	// TotalCostUSD alone is always all-time regardless.
	PeriodDays int `json:"period_days"`

	TotalCostUSD  float64 `json:"total_cost_usd"`
	PeriodCostUSD float64 `json:"period_cost_usd"`

	ThreadCount int `json:"thread_count"`
	TurnCount   int `json:"turn_count"`

	AvgTurnDurationMs int64 `json:"avg_turn_duration_ms"`

	// ToolCallCounts/ToolErrorCounts are keyed by tool name (e.g.
	// "web_search") — a tool absent from ToolErrorCounts had zero errors
	// in the period, not "unknown".
	ToolCallCounts  map[string]int `json:"tool_call_counts"`
	ToolErrorCounts map[string]int `json:"tool_error_counts"`

	// CheckInCount/StaleStreakCount/MaxTurnsWrapupCount are how often
	// each research-steering signal fired (see agent/driver.go's
	// emitNudge) — the actual evidence researchCheckInInterval/
	// staleStreakThreshold/config.MaxAgentTurns should be tuned against,
	// rather than guesswork from reading the code alone.
	CheckInCount        int `json:"check_in_count"`
	StaleStreakCount    int `json:"stale_streak_count"`
	MaxTurnsWrapupCount int `json:"max_turns_wrapup_count"`

	CompactionCount int `json:"compaction_count"`
}

// GetStats aggregates Stats over the trailing periodDays days (0 or
// negative means all time, and only affects the period-scoped fields —
// TotalCostUSD is always all-time). Read-only; safe to call as often as
// the CLI/API/UI want without any write-side bookkeeping to keep correct.
func (s *Store) GetStats(periodDays int) (*Stats, error) {
	stats := &Stats{
		PeriodDays:      periodDays,
		ToolCallCounts:  map[string]int{},
		ToolErrorCounts: map[string]int{},
	}

	var since string
	if periodDays > 0 {
		since = time.Now().AddDate(0, 0, -periodDays).UTC().Format("2006-01-02 15:04:05")
	}

	if err := s.db.QueryRow(
		`SELECT COALESCE(SUM(cost_usd), 0) FROM threads WHERE disabled = 0`,
	).Scan(&stats.TotalCostUSD); err != nil {
		return nil, err
	}

	// TurnCount counts distinct turn_id among assistant messages, not raw
	// message rows — a turn is one user/assistant pair (plus whatever tool
	// calls happened between them), and only the assistant side carries
	// duration_ms, so counting from there avoids double-counting the pair
	// as two turns.
	messageQuery := `SELECT COALESCE(SUM(cost_usd), 0),
		COUNT(DISTINCT CASE WHEN role = 'assistant' AND turn_id != '' THEN turn_id END),
		COALESCE(AVG(CASE WHEN role = 'assistant' AND duration_ms > 0 THEN duration_ms END), 0)
		FROM messages`
	messageArgs := []interface{}{}
	if since != "" {
		messageQuery += ` WHERE created_at >= ?`
		messageArgs = append(messageArgs, since)
	}
	// avgTurnDurationMs is scanned as a float64, not straight into
	// stats.AvgTurnDurationMs (int64) — SQLite's AVG() always returns a
	// real number even over an all-integer column, so a non-whole-number
	// average (the common case with more than one turn) fails an int64
	// Scan outright rather than just losing precision.
	var avgTurnDurationMs float64
	if err := s.db.QueryRow(messageQuery, messageArgs...).Scan(
		&stats.PeriodCostUSD, &stats.TurnCount, &avgTurnDurationMs,
	); err != nil {
		return nil, err
	}
	stats.AvgTurnDurationMs = int64(avgTurnDurationMs)

	// Same disabled/fork_root_id filter ListThreads uses — a hidden
	// variant fork isn't a thread the user thinks of as "one of theirs".
	threadQuery := `SELECT COUNT(*) FROM threads WHERE disabled = 0 AND fork_root_id = ''`
	threadArgs := []interface{}{}
	if since != "" {
		threadQuery += ` AND created_at >= ?`
		threadArgs = append(threadArgs, since)
	}
	if err := s.db.QueryRow(threadQuery, threadArgs...).Scan(&stats.ThreadCount); err != nil {
		return nil, err
	}

	// message = 'tool call finished' excludes the paired 'tool call
	// started' row logTurnEvent also writes per call (see gateway/turn.go)
	// — counting both would double every tool call.
	toolQuery := `SELECT source, level, COUNT(*) FROM events
		WHERE source LIKE 'tool.%' AND message = 'tool call finished'`
	toolArgs := []interface{}{}
	if since != "" {
		toolQuery += ` AND created_at >= ?`
		toolArgs = append(toolArgs, since)
	}
	toolQuery += ` GROUP BY source, level`
	toolRows, err := s.db.Query(toolQuery, toolArgs...)
	if err != nil {
		return nil, err
	}
	for toolRows.Next() {
		var source, level string
		var count int
		if err := toolRows.Scan(&source, &level, &count); err != nil {
			toolRows.Close()
			return nil, err
		}
		tool := strings.TrimPrefix(source, "tool.")
		stats.ToolCallCounts[tool] += count
		if level == "warn" {
			stats.ToolErrorCounts[tool] += count
		}
	}
	if err := toolRows.Err(); err != nil {
		return nil, err
	}
	toolRows.Close()

	// Nudge kind lives inside the JSON data blob, not a column — cheap
	// enough to unmarshal per-row at this data volume rather than reach
	// for SQLite's JSON1 extension (not guaranteed present in every
	// modernc.org/sqlite build) just to GROUP BY a JSON field.
	nudgeQuery := `SELECT data FROM events WHERE source = 'agent.nudge'`
	nudgeArgs := []interface{}{}
	if since != "" {
		nudgeQuery += ` AND created_at >= ?`
		nudgeArgs = append(nudgeArgs, since)
	}
	nudgeRows, err := s.db.Query(nudgeQuery, nudgeArgs...)
	if err != nil {
		return nil, err
	}
	for nudgeRows.Next() {
		var dataJSON string
		if err := nudgeRows.Scan(&dataJSON); err != nil {
			nudgeRows.Close()
			return nil, err
		}
		var d struct {
			Kind string `json:"kind"`
		}
		if err := json.Unmarshal([]byte(dataJSON), &d); err != nil {
			continue
		}
		switch d.Kind {
		case "check_in":
			stats.CheckInCount++
		case "stale_streak":
			stats.StaleStreakCount++
		case "max_turns_wrapup":
			stats.MaxTurnsWrapupCount++
		}
	}
	if err := nudgeRows.Err(); err != nil {
		return nil, err
	}
	nudgeRows.Close()

	compactionQuery := `SELECT COUNT(*) FROM events WHERE source = 'compaction' AND message = 'thread auto-compacted'`
	compactionArgs := []interface{}{}
	if since != "" {
		compactionQuery += ` AND created_at >= ?`
		compactionArgs = append(compactionArgs, since)
	}
	if err := s.db.QueryRow(compactionQuery, compactionArgs...).Scan(&stats.CompactionCount); err != nil {
		return nil, err
	}

	return stats, nil
}
