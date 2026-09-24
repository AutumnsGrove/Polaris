// stats.go aggregates usage/tuning numbers on demand from the
// threads/messages/events tables — no running counters kept anywhere
// else, since a single-user install's data volume (see the package doc's
// single-user framing) makes a full scan per request cheap enough not to
// bother with a second source of truth to keep in sync.
package store

import (
	"encoding/json"
	"fmt"
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

	// CostBySource splits TotalCostUSD/PeriodCostUSD four ways — Polaris
	// (regular chat, every threads.source other than "pulsar", plus
	// ghost-mode turns' spend from ghost_usage — see GetStats — since a
	// ghost thread is just an incognito regular chat, not a distinct
	// subsystem the way Pulsar/Daily/Constellation are), Pulsar (routine
	// pulses, threads.source = "pulsar"), Daily (Pulsar Daily editions),
	// and Constellation (Weaver runs plus Refine/Edit's one-off calls —
	// see store.ConstellationStats, the same two tables). Daily and
	// Constellation are both wholly separate cost paths that never touch
	// threads/messages at all, so every field here — including
	// TotalCostUSD/PeriodCostUSD themselves — is the real, complete sum
	// across all four; previously Daily/Constellation were silently
	// excluded from the grand total while still appearing as their own
	// breakdown rows in the settings panel, which looked like the
	// breakdown didn't add up to the headline figure (a real, reported
	// point of confusion) even though every number involved was correct.
	CostBySource CostBySource `json:"cost_by_source"`

	// VerificationCostUSD is how much of the above (already counted once,
	// inside CostBySource.Polaris/.Pulsar via ctx.AddCost -> messages.
	// cost_usd) was specifically Jev verification spend — a transparency
	// breakout, not a fourth additive bucket. Deliberately NOT summed into
	// TotalCostUSD/PeriodCostUSD or CostBySource a second time; see
	// jev_usage's schema comment (store.go) and
	// docs/plans/source-verification-compare-tool.md's cost-tracking
	// section. Sourced from jev_usage, a per-call ledger, so period
	// filtering is exact (trailing N days), unlike a calendar-month-only
	// aggregate would allow.
	VerificationCostUSD SourceCost `json:"verification_cost_usd"`

	ThreadCount int `json:"thread_count"`
	TurnCount   int `json:"turn_count"`

	// TransponderCallCount is the number of distinct threads that have
	// ever had a voice_mode (Transponder) turn — see
	// docs/plans/transponder.md's "one call" definition. Always all-time.
	TransponderCallCount int `json:"transponder_call_count"`

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

	// CodeExecWallTimeMS is total wall-clock time code_exec has actually
	// spent running sandboxed scripts (sum of each call's "tool call
	// finished" timestamp minus its "tool call started" timestamp) —
	// replaces the removed visualize-chart-kind-counts stat (see issue
	// #44) as the settings panel's "how much is this actually being
	// used" number for the tool that took its place.
	CodeExecWallTimeMS int64 `json:"code_exec_wall_time_ms"`

	// CacheUsage is the deployment-wide prompt-cache health stat — how
	// much of every turn's input the provider served from its prompt
	// cache (issue #107's per-thread hit %, rolled up across everything).
	// Raw token sums rather than a percentage, so the frontend can show
	// "—" instead of a misleading 0% when nothing's been recorded yet.
	CacheUsage CacheUsage `json:"cache_usage"`
}

// CacheUsage pairs summed prompt tokens with how many of them were
// prompt-cache reads, for the trailing period and all time. See
// Stats.CacheUsage.
type CacheUsage struct {
	PeriodPromptTokens    int `json:"period_prompt_tokens"`
	PeriodCacheReadTokens int `json:"period_cache_read_tokens"`
	TotalPromptTokens     int `json:"total_prompt_tokens"`
	TotalCacheReadTokens  int `json:"total_cache_read_tokens"`
}

// SourceCost is one bucket's period/all-time cost — see
// Stats.CostBySource.
type SourceCost struct {
	PeriodCostUSD float64 `json:"period_cost_usd"`
	TotalCostUSD  float64 `json:"total_cost_usd"`
}

// CostBySource is Stats.TotalCostUSD/PeriodCostUSD broken down by where
// the cost actually came from. Polaris + Pulsar + Daily + Constellation
// always sums back to the plain total/period figures exactly — see
// GetStats. There's no separate "ghost" bucket: ghost-mode spend is
// folded straight into Polaris, the same source a ghost thread would
// have been tagged if it were persisted — see GetStats.
type CostBySource struct {
	Polaris       SourceCost `json:"polaris"`
	Pulsar        SourceCost `json:"pulsar"`
	Daily         SourceCost `json:"daily"`
	Constellation SourceCost `json:"constellation"`
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
	}

	var since string
	if periodDays > 0 {
		since = time.Now().AddDate(0, 0, -periodDays).UTC().Format("2006-01-02 15:04:05")
	}

	// TurnCount counts distinct turn_id among assistant messages, not raw
	// message rows — a turn is one user/assistant pair (plus whatever tool
	// calls happened between them), and only the assistant side carries
	// duration_ms, so counting from there avoids double-counting the pair
	// as two turns.
	messageQuery := `SELECT
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
		&stats.TurnCount, &avgTurnDurationMs,
	); err != nil {
		return nil, err
	}
	stats.AvgTurnDurationMs = int64(avgTurnDurationMs)

	// Polaris/Pulsar spend, all-time and period, both from the same
	// messages query — they used to come from two different ledgers
	// (threads.cost_usd for all-time, messages.cost_usd for the period),
	// which disagreed often enough that the 30-day figure could exceed the
	// all-time one: each ledger missed spend the other had, and ForkThread
	// copies a shared prefix's messages (cost_usd included) into every
	// edit/retry variant, so a plain SUM counted a retried thread's
	// earlier turns once per variant. See AddTurnCost for the first half
	// of that fix.
	//
	// Each real message is counted once: fork copies share role and
	// turn_id (every turn gets a fresh one), and pre-turn_id legacy rows
	// fall back to created_at+content, which a copy also keeps. MAX, since
	// a late cost (suggestions, verification) only ever lands on the
	// original row. Deleted threads count too: that money was really spent.
	if err := s.costBySource("", func(source string, cost float64) {
		if source == "pulsar" {
			stats.CostBySource.Pulsar.TotalCostUSD += cost
		} else {
			stats.CostBySource.Polaris.TotalCostUSD += cost
		}
	}); err != nil {
		return nil, err
	}
	if err := s.costBySource(since, func(source string, cost float64) {
		if source == "pulsar" {
			stats.CostBySource.Pulsar.PeriodCostUSD += cost
		} else {
			stats.CostBySource.Polaris.PeriodCostUSD += cost
		}
	}); err != nil {
		return nil, err
	}

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

	// Constellation's cost never touches threads/messages either — the
	// same two tables GetConstellationStats sums (shooting_star_events
	// for Weaver's own runs, star_reconcile_events for Refine/Edit's
	// one-off calls — see its own doc comment for why two tables).
	// Period-filtered by created_at, same as everything above except
	// Daily.
	if err := s.db.QueryRow(
		`SELECT COALESCE(SUM(cost_usd), 0) FROM shooting_star_events`,
	).Scan(&stats.CostBySource.Constellation.TotalCostUSD); err != nil {
		return nil, err
	}
	var reconcileTotal float64
	if err := s.db.QueryRow(
		`SELECT COALESCE(SUM(cost_usd), 0) FROM star_reconcile_events`,
	).Scan(&reconcileTotal); err != nil {
		return nil, err
	}
	stats.CostBySource.Constellation.TotalCostUSD += reconcileTotal
	if since == "" {
		stats.CostBySource.Constellation.PeriodCostUSD = stats.CostBySource.Constellation.TotalCostUSD
	} else {
		if err := s.db.QueryRow(
			`SELECT COALESCE(SUM(cost_usd), 0) FROM shooting_star_events WHERE created_at >= ?`, since,
		).Scan(&stats.CostBySource.Constellation.PeriodCostUSD); err != nil {
			return nil, err
		}
		var reconcilePeriod float64
		if err := s.db.QueryRow(
			`SELECT COALESCE(SUM(cost_usd), 0) FROM star_reconcile_events WHERE created_at >= ?`, since,
		).Scan(&reconcilePeriod); err != nil {
			return nil, err
		}
		stats.CostBySource.Constellation.PeriodCostUSD += reconcilePeriod
	}

	// Ghost-mode spend never touches threads/messages at all (see
	// ghost_usage's own doc comment) — but unlike Daily/Constellation, it
	// isn't its own subsystem with its own bucket; a ghost thread is just
	// an incognito regular chat, so its cost is folded straight into
	// CostBySource.Polaris, the same place it would have landed had the
	// thread been persisted normally. The grand total below picks it up
	// from there, not as a separate addition.
	var ghostTotal, ghostPeriod float64
	if err := s.db.QueryRow(
		`SELECT COALESCE(SUM(cost_usd), 0) FROM ghost_usage`,
	).Scan(&ghostTotal); err != nil {
		return nil, err
	}
	if since == "" {
		ghostPeriod = ghostTotal
	} else {
		if err := s.db.QueryRow(
			`SELECT COALESCE(SUM(cost_usd), 0) FROM ghost_usage WHERE created_at >= ?`, since,
		).Scan(&ghostPeriod); err != nil {
			return nil, err
		}
	}
	stats.CostBySource.Polaris.TotalCostUSD += ghostTotal
	stats.CostBySource.Polaris.PeriodCostUSD += ghostPeriod

	// TotalCostUSD/PeriodCostUSD: the true, complete grand total across
	// every real spend path — computed last, once every bucket above
	// (including ghost's fold-in) is finalized, so the settings panel's
	// "Cost by source" breakdown always sums back to this number exactly.
	// Previously this was Polaris+Pulsar only, with Daily silently
	// excluded — see CostBySource's own doc comment for why that looked
	// like the breakdown didn't add up.
	stats.TotalCostUSD = stats.CostBySource.Polaris.TotalCostUSD + stats.CostBySource.Pulsar.TotalCostUSD +
		stats.CostBySource.Daily.TotalCostUSD + stats.CostBySource.Constellation.TotalCostUSD
	stats.PeriodCostUSD = stats.CostBySource.Polaris.PeriodCostUSD + stats.CostBySource.Pulsar.PeriodCostUSD +
		stats.CostBySource.Daily.PeriodCostUSD + stats.CostBySource.Constellation.PeriodCostUSD

	// VerificationCostUSD: a breakout, not an addition — this money is
	// already inside TotalCostUSD/CostBySource above (it arrived via
	// tools.Context.AddCost during the turn, same as any other tool's
	// spend), so unlike ghostTotal/ghostPeriod just above, neither figure
	// here is added to stats.TotalCostUSD/PeriodCostUSD or CostBySource.
	if err := s.db.QueryRow(
		`SELECT COALESCE(SUM(cost_usd), 0) FROM jev_usage`,
	).Scan(&stats.VerificationCostUSD.TotalCostUSD); err != nil {
		return nil, err
	}
	if since == "" {
		stats.VerificationCostUSD.PeriodCostUSD = stats.VerificationCostUSD.TotalCostUSD
	} else {
		if err := s.db.QueryRow(
			`SELECT COALESCE(SUM(cost_usd), 0) FROM jev_usage WHERE created_at >= ?`, since,
		).Scan(&stats.VerificationCostUSD.PeriodCostUSD); err != nil {
			return nil, err
		}
	}

	// CacheUsage: one row per turn_id, not per message — ForkThread copies
	// a shared prefix's assistant rows (usage columns included) into every
	// edit/retry fork, so a plain SUM would count each retried thread's
	// earlier turns once per variant. MAX within a turn_id is just "the"
	// value, since every copy carries identical numbers. Deleted threads
	// still count: this is about requests the provider actually served.
	cacheQuery := `SELECT COALESCE(SUM(p), 0), COALESCE(SUM(c), 0) FROM (
		SELECT MAX(prompt_tokens) AS p, MAX(cache_read_tokens) AS c FROM messages
		WHERE role = 'assistant' AND turn_id != '' AND prompt_tokens > 0%s
		GROUP BY turn_id)`
	if err := s.db.QueryRow(fmt.Sprintf(cacheQuery, "")).Scan(
		&stats.CacheUsage.TotalPromptTokens, &stats.CacheUsage.TotalCacheReadTokens,
	); err != nil {
		return nil, err
	}
	if since == "" {
		stats.CacheUsage.PeriodPromptTokens = stats.CacheUsage.TotalPromptTokens
		stats.CacheUsage.PeriodCacheReadTokens = stats.CacheUsage.TotalCacheReadTokens
	} else if err := s.db.QueryRow(fmt.Sprintf(cacheQuery, " AND created_at >= ?"), since).Scan(
		&stats.CacheUsage.PeriodPromptTokens, &stats.CacheUsage.PeriodCacheReadTokens,
	); err != nil {
		return nil, err
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

	// TransponderCallCount counts distinct threads, not raw call turns —
	// always all-time regardless of the period filter above, same as
	// TotalCostUSD, since "how many threads have I ever called into" isn't
	// a trailing-window question the way spend/turn-rate stats are.
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM threads WHERE used_transponder = 1 AND disabled = 0`).Scan(&stats.TransponderCallCount); err != nil {
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
	defer toolRows.Close()
	for toolRows.Next() {
		var source, level string
		var count int
		if err := toolRows.Scan(&source, &level, &count); err != nil {
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
	defer providerRows.Close()
	for providerRows.Next() {
		var dataJSON string
		if err := providerRows.Scan(&dataJSON); err != nil {
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

	// CodeExecWallTimeMS: call_id (inside the JSON data blob, same as
	// provider above) pairs a "tool call started" row with its matching
	// "tool call finished" row (see tools/code_exec.go's handleCodeExec,
	// which emits both under that call_id) — wall time per call is
	// finished.created_at minus started.created_at. An unmatched call_id
	// (a period filter's `since` cutoff splitting a pair across the
	// boundary, or a server restart mid-call) contributes nothing rather
	// than guessing; a small undercount is an acceptable tradeoff for a
	// "how much has this actually run" number, silently wrong data isn't.
	codeExecQuery := `SELECT message, data, created_at FROM events WHERE source = 'tool.code_exec' AND message IN ('tool call started', 'tool call finished')`
	codeExecArgs := []interface{}{}
	if since != "" {
		codeExecQuery += ` AND created_at >= ?`
		codeExecArgs = append(codeExecArgs, since)
	}
	codeExecRows, err := s.db.Query(codeExecQuery, codeExecArgs...)
	if err != nil {
		return nil, err
	}
	defer codeExecRows.Close()
	codeExecStarted := map[string]time.Time{}
	for codeExecRows.Next() {
		var message, dataJSON, createdAtStr string
		if err := codeExecRows.Scan(&message, &dataJSON, &createdAtStr); err != nil {
			return nil, err
		}
		var d struct {
			CallID string `json:"call_id"`
		}
		if err := json.Unmarshal([]byte(dataJSON), &d); err != nil || d.CallID == "" {
			continue
		}
		// created_at is written by SQLite's own CURRENT_TIMESTAMP default —
		// same "YYYY-MM-DD HH:MM:SS" UTC format `since` above is formatted
		// in, so both sides of the subtraction agree.
		createdAt, parseErr := time.Parse("2006-01-02 15:04:05", createdAtStr)
		if parseErr != nil {
			continue
		}
		switch message {
		case "tool call started":
			codeExecStarted[d.CallID] = createdAt
		case "tool call finished":
			if start, ok := codeExecStarted[d.CallID]; ok {
				stats.CodeExecWallTimeMS += createdAt.Sub(start).Milliseconds()
				delete(codeExecStarted, d.CallID)
			}
		}
	}
	if err := codeExecRows.Err(); err != nil {
		return nil, err
	}

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
	defer nudgeRows.Close()
	for nudgeRows.Next() {
		var dataJSON string
		if err := nudgeRows.Scan(&dataJSON); err != nil {
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

// costBySource sums message spend per threads.source, counting every real
// message once (see GetStats' comment on fork copies), optionally limited
// to messages created at or after since. add is called once per source.
func (s *Store) costBySource(since string, add func(source string, cost float64)) error {
	where, args := "", []interface{}{}
	if since != "" {
		where, args = "WHERE m.created_at >= ?", append(args, since)
	}
	rows, err := s.db.Query(fmt.Sprintf(`SELECT source, COALESCE(SUM(cost), 0) FROM (
		SELECT MAX(t.source) AS source, MAX(m.cost_usd) AS cost
		FROM messages m JOIN threads t ON t.id = m.thread_id
		%s
		GROUP BY m.role, m.turn_id, CASE WHEN m.turn_id = '' THEN m.created_at || m.content END)
		GROUP BY source`, where), args...)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var source string
		var cost float64
		if err := rows.Scan(&source, &cost); err != nil {
			return err
		}
		add(source, cost)
	}
	return rows.Err()
}
