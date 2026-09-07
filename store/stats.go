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

	// CostBySource splits TotalCostUSD/PeriodCostUSD three ways — Polaris
	// (regular chat, every threads.source other than "pulsar"), Pulsar
	// (routine pulses, threads.source = "pulsar"), and Daily (Pulsar
	// Daily editions). Daily is a wholly separate cost path
	// (pulsar_daily_editions.cost_usd) — it's never a thread at all, so
	// it was previously invisible in both totals above; this is the first
	// place its cost is surfaced anywhere in Stats.
	CostBySource CostBySource `json:"cost_by_source"`

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

	// SearchProviderCounts is how many web_search calls each provider key
	// ("searxng"/"brave"/"parallel"/"tavily") actually answered — not the
	// same thing as api_usage's monthly billing-cap counts (store.go's
	// IncrementAPIUsage), which count every billed request regardless of
	// whether it returned anything useful, and which Tavily's fallback
	// doesn't participate in at all since it has no cap. This is scoped
	// to genuine successes only (a tool_result actually returned to the
	// model), so it answers "how often does each fallback actually save
	// the day" rather than "how much have I spent".
	SearchProviderCounts map[string]int `json:"search_provider_counts"`

	// ChartKindCounts is how many times the model called visualize with
	// each kind ("line"/"bar"/"timeline"/"meter") — the evidence for
	// whether the tool's v1 kind set (see tools/visualize.go) actually
	// matches what the model reaches for in practice. Scoped to visualize
	// specifically, not weather's Tier-1 auto-attached chart: that one's
	// kind is always "line" and never a model decision, so counting it
	// here would answer a different question ("how often is weather
	// asked for multiple days") than "which chart kinds does the model
	// choose".
	ChartKindCounts map[string]int `json:"chart_kind_counts"`
}

// SourceCost is one bucket's period/all-time cost — see
// Stats.CostBySource.
type SourceCost struct {
	PeriodCostUSD float64 `json:"period_cost_usd"`
	TotalCostUSD  float64 `json:"total_cost_usd"`
}

// CostBySource is Stats.TotalCostUSD/PeriodCostUSD broken down by where
// the cost actually came from.
type CostBySource struct {
	Polaris SourceCost `json:"polaris"`
	Pulsar  SourceCost `json:"pulsar"`
	Daily   SourceCost `json:"daily"`
}

// GetStats aggregates Stats over the trailing periodDays days (0 or
// negative means all time, and only affects the period-scoped fields —
// TotalCostUSD is always all-time). Read-only; safe to call as often as
// the CLI/API/UI want without any write-side bookkeeping to keep correct.
func (s *Store) GetStats(periodDays int) (*Stats, error) {
	stats := &Stats{
		PeriodDays:           periodDays,
		ToolCallCounts:       map[string]int{},
		ToolErrorCounts:      map[string]int{},
		SearchProviderCounts: map[string]int{},
		ChartKindCounts:      map[string]int{},
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

	// CostBySource's Polaris/Pulsar halves mirror TotalCostUSD/
	// PeriodCostUSD's own two different source columns exactly (threads.
	// cost_usd for all-time, messages.cost_usd joined through threads for
	// the period) — so "Polaris + Pulsar" always sums back to the plain
	// total/period figures above, not a second, subtly different number.
	totalBySourceRows, err := s.db.Query(
		`SELECT source, COALESCE(SUM(cost_usd), 0) FROM threads WHERE disabled = 0 GROUP BY source`,
	)
	if err != nil {
		return nil, err
	}
	for totalBySourceRows.Next() {
		var source string
		var cost float64
		if err := totalBySourceRows.Scan(&source, &cost); err != nil {
			totalBySourceRows.Close()
			return nil, err
		}
		if source == "pulsar" {
			stats.CostBySource.Pulsar.TotalCostUSD += cost
		} else {
			stats.CostBySource.Polaris.TotalCostUSD += cost
		}
	}
	if err := totalBySourceRows.Err(); err != nil {
		return nil, err
	}
	totalBySourceRows.Close()

	periodBySourceQuery := `SELECT threads.source, COALESCE(SUM(messages.cost_usd), 0)
		FROM messages JOIN threads ON messages.thread_id = threads.id`
	periodBySourceArgs := []interface{}{}
	if since != "" {
		periodBySourceQuery += ` WHERE messages.created_at >= ?`
		periodBySourceArgs = append(periodBySourceArgs, since)
	}
	periodBySourceQuery += ` GROUP BY threads.source`
	periodBySourceRows, err := s.db.Query(periodBySourceQuery, periodBySourceArgs...)
	if err != nil {
		return nil, err
	}
	for periodBySourceRows.Next() {
		var source string
		var cost float64
		if err := periodBySourceRows.Scan(&source, &cost); err != nil {
			periodBySourceRows.Close()
			return nil, err
		}
		if source == "pulsar" {
			stats.CostBySource.Pulsar.PeriodCostUSD += cost
		} else {
			stats.CostBySource.Polaris.PeriodCostUSD += cost
		}
	}
	if err := periodBySourceRows.Err(); err != nil {
		return nil, err
	}
	periodBySourceRows.Close()

	// Daily's cost never touches threads/messages at all (see
	// pulsar_daily_editions' own doc comment) — a separate query, not a
	// third bucket folded into the joins above. Period-filtered by
	// edition_date (a calendar date), not created_at: a same-day
	// regenerate keeps the row's original created_at, so filtering by
	// insert time would silently miss a re-run edition a user actually
	// looks at today.
	if err := s.db.QueryRow(
		`SELECT COALESCE(SUM(cost_usd), 0) FROM pulsar_daily_editions`,
	).Scan(&stats.CostBySource.Daily.TotalCostUSD); err != nil {
		return nil, err
	}
	if since == "" {
		stats.CostBySource.Daily.PeriodCostUSD = stats.CostBySource.Daily.TotalCostUSD
	} else {
		sinceDate := time.Now().AddDate(0, 0, -periodDays).Format("2006-01-02")
		if err := s.db.QueryRow(
			`SELECT COALESCE(SUM(cost_usd), 0) FROM pulsar_daily_editions WHERE edition_date >= ?`, sinceDate,
		).Scan(&stats.CostBySource.Daily.PeriodCostUSD); err != nil {
			return nil, err
		}
	}

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

	// provider lives inside the JSON data blob (see gateway/turn.go's
	// logTurnEvent), same "cheap enough to unmarshal per-row" reasoning as
	// the nudge query below — only web_search's own finished-call events
	// ever carry it, and only when a provider genuinely answered (empty
	// string / absent for the "no results"/degraded/error tool_results,
	// which never call formatSearchResults).
	providerQuery := `SELECT data FROM events WHERE source = 'tool.web_search' AND message = 'tool call finished'`
	providerArgs := []interface{}{}
	if since != "" {
		providerQuery += ` AND created_at >= ?`
		providerArgs = append(providerArgs, since)
	}
	providerRows, err := s.db.Query(providerQuery, providerArgs...)
	if err != nil {
		return nil, err
	}
	for providerRows.Next() {
		var dataJSON string
		if err := providerRows.Scan(&dataJSON); err != nil {
			providerRows.Close()
			return nil, err
		}
		var d struct {
			Provider string `json:"provider"`
		}
		if err := json.Unmarshal([]byte(dataJSON), &d); err != nil {
			continue
		}
		if d.Provider != "" {
			stats.SearchProviderCounts[d.Provider]++
		}
	}
	if err := providerRows.Err(); err != nil {
		return nil, err
	}
	providerRows.Close()

	// chart_kind lives inside the JSON data blob, same reasoning as
	// provider above — only visualize's own finished-call events ever
	// carry it, and only on success (a rejected/capped call never reaches
	// ctx.SetChart, so its tool_result has no chart at all — see
	// tools/visualize.go's handleVisualize).
	chartKindQuery := `SELECT data FROM events WHERE source = 'tool.visualize' AND message = 'tool call finished'`
	chartKindArgs := []interface{}{}
	if since != "" {
		chartKindQuery += ` AND created_at >= ?`
		chartKindArgs = append(chartKindArgs, since)
	}
	chartKindRows, err := s.db.Query(chartKindQuery, chartKindArgs...)
	if err != nil {
		return nil, err
	}
	for chartKindRows.Next() {
		var dataJSON string
		if err := chartKindRows.Scan(&dataJSON); err != nil {
			chartKindRows.Close()
			return nil, err
		}
		var d struct {
			ChartKind string `json:"chart_kind"`
		}
		if err := json.Unmarshal([]byte(dataJSON), &d); err != nil {
			continue
		}
		if d.ChartKind != "" {
			stats.ChartKindCounts[d.ChartKind]++
		}
	}
	if err := chartKindRows.Err(); err != nil {
		return nil, err
	}
	chartKindRows.Close()

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
