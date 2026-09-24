// pulsar.go persists Pulsar routines — see docs/plans/pulsar-routines.md.
// A routine is a saved prompt plus a schedule; each firing (a "pulse") is a
// normal thread tagged with source = 'pulsar' and pulsar_routine_id (see
// store.go's threads schema comment), not a record in this table itself.
package store

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

// ErrPulsarRoutineNotFound is returned by GetPulsarRoutine/
// UpdatePulsarRoutine/ArchivePulsarRoutine/SetPulsarRoutineLastRun when id
// doesn't match any row.
var ErrPulsarRoutineNotFound = errors.New("pulsar routine not found")

// PulsarRoutine is one saved routine, as returned by every read method
// below. LastRunAt/ArchivedAt are *time.Time (nil, not sql.NullTime) —
// both are genuinely absent for a brand-new/still-active routine, and a
// bare pointer marshals to clean JSON null instead of sql.NullTime's raw
// {Time, Valid} struct, which the frontend would otherwise have to
// special-case.
type PulsarRoutine struct {
	ID             int64  `json:"id"`
	Name           string `json:"name"`
	Prompt         string `json:"prompt"`
	Model          string `json:"model"`
	FocusMode      string `json:"focus_mode"`
	DeepResearch   bool   `json:"deep_research"`
	ScheduleType   string `json:"schedule_type"`
	ScheduleParams string `json:"schedule_params"`
	TimeOfDay      string `json:"time_of_day"`
	// CreatedAt doubles as the due-time baseline for a routine that has
	// never fired yet (LastRunAt nil) — see gateway's isRoutineDue, which
	// needs real time.Time arithmetic on it, unlike
	// PulsarPulseSummary.CreatedAt below (display-only, plain string).
	CreatedAt  time.Time  `json:"created_at"`
	LastRunAt  *time.Time `json:"last_run_at"`
	ArchivedAt *time.Time `json:"archived_at"`
}

// CreatePulsarRoutine inserts a new active routine, returning its id.
func (s *Store) CreatePulsarRoutine(name, prompt, model, focusMode string, deepResearch bool, scheduleType, scheduleParams, timeOfDay string) (int64, error) {
	res, err := s.db.Exec(
		`INSERT INTO pulsar_routines (name, prompt, model, focus_mode, deep_research, schedule_type, schedule_params, time_of_day)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		name, prompt, model, focusMode, deepResearch, scheduleType, scheduleParams, timeOfDay,
	)
	if err != nil {
		return 0, fmt.Errorf("create pulsar routine: %w", err)
	}
	return res.LastInsertId()
}

// UpdatePulsarRoutine overwrites an existing routine's editable fields in
// place — the "edit" half of the create/edit form's shared UI (see the
// plan doc's "Routine lifecycle"). Does not touch archived_at/last_run_at;
// editing an archived routine is allowed (its form is reachable from the
// archive section) but doesn't itself unarchive it.
func (s *Store) UpdatePulsarRoutine(id int64, name, prompt, model, focusMode string, deepResearch bool, scheduleType, scheduleParams, timeOfDay string) error {
	res, err := s.db.Exec(
		`UPDATE pulsar_routines SET
			name = ?, prompt = ?, model = ?, focus_mode = ?, deep_research = ?,
			schedule_type = ?, schedule_params = ?, time_of_day = ?
		 WHERE id = ?`,
		name, prompt, model, focusMode, deepResearch, scheduleType, scheduleParams, timeOfDay, id,
	)
	if err != nil {
		return fmt.Errorf("update pulsar routine: %w", err)
	}
	return rowsAffectedOrNotFound(res, ErrPulsarRoutineNotFound)
}

// ArchivePulsarRoutine soft-deletes a routine — see the plan doc's "Delete
// is always soft" — moving it out of the active list and the scheduler's
// consideration without touching its row or any pulse (thread) it ever
// produced. A no-op (not an error) if the routine is already archived,
// since archiving twice isn't meaningfully different from archiving once.
func (s *Store) ArchivePulsarRoutine(id int64) error {
	res, err := s.db.Exec(`UPDATE pulsar_routines SET archived_at = CURRENT_TIMESTAMP WHERE id = ? AND archived_at IS NULL`, id)
	if err != nil {
		return fmt.Errorf("archive pulsar routine: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("archive pulsar routine: %w", err)
	}
	if n == 0 {
		// Distinguish "doesn't exist" from "already archived" so the
		// gateway handler can 404 correctly rather than treat both as
		// silent success.
		if _, err := s.GetPulsarRoutine(id); err != nil {
			return err
		}
	}
	return nil
}

// UnarchivePulsarRoutine reactivates a previously archived routine —
// "archive, then unarchive when you want it back" is v1's stand-in for a
// separate pause state, per the plan doc's "Routine lifecycle".
func (s *Store) UnarchivePulsarRoutine(id int64) error {
	res, err := s.db.Exec(`UPDATE pulsar_routines SET archived_at = NULL WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("unarchive pulsar routine: %w", err)
	}
	return rowsAffectedOrNotFound(res, ErrPulsarRoutineNotFound)
}

// SetPulsarRoutineLastRun records that a pulse just fired — called by the
// scheduler right before (not after) running the pulse's turn, so a crash
// mid-turn doesn't leave last_run_at stale and cause the very next
// scheduler tick to immediately re-fire the same routine.
func (s *Store) SetPulsarRoutineLastRun(id int64, when string) error {
	res, err := s.db.Exec(`UPDATE pulsar_routines SET last_run_at = ? WHERE id = ?`, when, id)
	if err != nil {
		return fmt.Errorf("set pulsar routine last run: %w", err)
	}
	return rowsAffectedOrNotFound(res, ErrPulsarRoutineNotFound)
}

// GetPulsarRoutine returns one routine by id, active or archived.
func (s *Store) GetPulsarRoutine(id int64) (*PulsarRoutine, error) {
	var r PulsarRoutine
	err := s.db.QueryRow(
		`SELECT id, name, prompt, model, focus_mode, deep_research, schedule_type, schedule_params, time_of_day, created_at, last_run_at, archived_at
		 FROM pulsar_routines WHERE id = ?`, id,
	).Scan(&r.ID, &r.Name, &r.Prompt, &r.Model, &r.FocusMode, &r.DeepResearch, &r.ScheduleType, &r.ScheduleParams, &r.TimeOfDay, &r.CreatedAt, &r.LastRunAt, &r.ArchivedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrPulsarRoutineNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("get pulsar routine: %w", err)
	}
	return &r, nil
}

// ListActivePulsarRoutines returns every non-archived routine, newest
// first — for both the /pulsar active-routines list and the scheduler's
// own due-check pass.
func (s *Store) ListActivePulsarRoutines() ([]PulsarRoutine, error) {
	return s.queryPulsarRoutines(`WHERE archived_at IS NULL ORDER BY created_at DESC`)
}

// ListArchivedPulsarRoutines returns every archived routine, most
// recently archived first — for /pulsar's archive section.
func (s *Store) ListArchivedPulsarRoutines() ([]PulsarRoutine, error) {
	return s.queryPulsarRoutines(`WHERE archived_at IS NOT NULL ORDER BY archived_at DESC`)
}

func (s *Store) queryPulsarRoutines(whereOrderBy string) ([]PulsarRoutine, error) {
	rows, err := s.db.Query(
		`SELECT id, name, prompt, model, focus_mode, deep_research, schedule_type, schedule_params, time_of_day, created_at, last_run_at, archived_at
		 FROM pulsar_routines ` + whereOrderBy,
	)
	if err != nil {
		return nil, fmt.Errorf("list pulsar routines: %w", err)
	}
	defer rows.Close()

	routines := []PulsarRoutine{}
	for rows.Next() {
		var r PulsarRoutine
		if err := rows.Scan(&r.ID, &r.Name, &r.Prompt, &r.Model, &r.FocusMode, &r.DeepResearch, &r.ScheduleType, &r.ScheduleParams, &r.TimeOfDay, &r.CreatedAt, &r.LastRunAt, &r.ArchivedAt); err != nil {
			return nil, fmt.Errorf("list pulsar routines: %w", err)
		}
		routines = append(routines, r)
	}
	return routines, rows.Err()
}

// PulsarPulseSummary is one row of a routine's pulse history — the
// thread-row-style list the plan doc's "routine detail" screen shows,
// trimmed to what that list actually renders rather than a full Thread.
type PulsarPulseSummary struct {
	ThreadID  string `json:"thread_id"`
	Title     string `json:"title"`
	Seen      bool   `json:"seen"`
	CreatedAt string `json:"created_at"`
}

// ListPulsarPulses returns a routine's pulse history newest-first — a
// plain query against threads.pulsar_routine_id (see its schema comment),
// not inference from title text.
func (s *Store) ListPulsarPulses(routineID int64) ([]PulsarPulseSummary, error) {
	rows, err := s.db.Query(
		`SELECT id, title, seen, created_at FROM threads
		 WHERE pulsar_routine_id = ? AND disabled = 0 AND fork_root_id = ''
		 ORDER BY created_at DESC`,
		routineID,
	)
	if err != nil {
		return nil, fmt.Errorf("list pulsar pulses: %w", err)
	}
	defer rows.Close()

	pulses := []PulsarPulseSummary{}
	for rows.Next() {
		var p PulsarPulseSummary
		if err := rows.Scan(&p.ThreadID, &p.Title, &p.Seen, &p.CreatedAt); err != nil {
			return nil, fmt.Errorf("list pulsar pulses: %w", err)
		}
		pulses = append(pulses, p)
	}
	return pulses, rows.Err()
}

// LatestPulseReport returns the most recent completed pulse's final
// assistant answer for a routine, plus when that pulse ran — the raw
// material firePulse folds into the next pulse's prompt so a recurring
// routine can say "already covered, skip" instead of restating the same
// news every run (a real staleness problem: a weekly digest with no
// memory of its own last report just re-describes whatever's still true
// from before). ok is false if the routine has never produced a pulse
// with an actual answer yet (brand new, or every prior pulse failed
// before completing).
//
// One query, not "find the latest pulse thread, then GetMessages(it)":
// ordering by (thread created_at, message id) DESC and taking the single
// newest row naturally skips a pulse thread that errored out with no
// assistant message at all and falls through to the last one that
// actually completed, without a separate failure branch to write.
func (s *Store) LatestPulseReport(routineID int64) (report string, reportedAt string, ok bool, err error) {
	err = s.db.QueryRow(
		`SELECT m.content, t.created_at
		 FROM threads t
		 JOIN messages m ON m.thread_id = t.id
		 WHERE t.pulsar_routine_id = ? AND t.disabled = 0 AND t.fork_root_id = '' AND m.role = 'assistant'
		 ORDER BY t.created_at DESC, m.id DESC
		 LIMIT 1`,
		routineID,
	).Scan(&report, &reportedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return "", "", false, nil
	}
	if err != nil {
		return "", "", false, fmt.Errorf("latest pulse report: %w", err)
	}
	return report, reportedAt, true, nil
}

// SetThreadPulsarRoutine links a freshly created thread to the routine
// whose scheduled firing produced it — called right after CreateThread in
// handleTurn's isNewThread branch, not folded into CreateThread's own
// signature, since every other CreateThread caller (a plain new chat
// thread) has no routine to link.
func (s *Store) SetThreadPulsarRoutine(threadID string, routineID int64) error {
	_, err := s.db.Exec(`UPDATE threads SET pulsar_routine_id = ? WHERE id = ?`, routineID, threadID)
	if err != nil {
		return fmt.Errorf("set thread pulsar routine: %w", err)
	}
	return nil
}

// MarkPulseSeen flips a pulsar-sourced thread's unread flag off — called
// the first time it's actually opened (see gateway/threads.go's
// handleGetThread, same "flip on first real open" shape as Atlas's
// continued_in_assistant). A no-op for any non-pulsar thread in practice,
// since seen is otherwise never set to 1.
func (s *Store) MarkPulseSeen(threadID string) error {
	_, err := s.db.Exec(`UPDATE threads SET seen = 1 WHERE id = ? AND seen = 0`, threadID)
	if err != nil {
		return fmt.Errorf("mark pulse seen: %w", err)
	}
	return nil
}

// UnreadPulseCounts returns the unread pulse count for every routine that
// has at least one — keyed by pulsar_routine_id, for the amber
// dot/count indicator's per-routine scope in /pulsar. The sidebar's
// global Orbit-icon count is just the sum of these values, computed
// client-side rather than as a second query, since the frontend already
// needs this same per-routine map to render each routine row's own badge.
func (s *Store) UnreadPulseCounts() (map[int64]int, error) {
	rows, err := s.db.Query(
		`SELECT pulsar_routine_id, COUNT(*) FROM threads
		 WHERE pulsar_routine_id IS NOT NULL AND seen = 0 AND disabled = 0 AND fork_root_id = ''
		 GROUP BY pulsar_routine_id`,
	)
	if err != nil {
		return nil, fmt.Errorf("unread pulse counts: %w", err)
	}
	defer rows.Close()

	counts := map[int64]int{}
	for rows.Next() {
		var id int64
		var n int
		if err := rows.Scan(&id, &n); err != nil {
			return nil, fmt.Errorf("unread pulse counts: %w", err)
		}
		counts[id] = n
	}
	return counts, rows.Err()
}

// PulsarStats is Pulsar's own activity/cost/tuning summary — a dedicated
// surface mirroring ConstellationStats (store/constellation.go), not
// folded into the main Stats/CostBySource breakdown. Stats.CostBySource
// .Pulsar already gives the one-line total, but nothing today answers
// "which tools are pulses actually calling" or "how often is a pulse
// failing outright" the way GetStats does for the main chat surface —
// and a pulse runs unsupervised on a schedule, so a stuck or misfiring
// routine is easier to miss than a live turn would be.
type PulsarStats struct {
	PeriodDays int `json:"period_days"`

	TotalCostUSD  float64 `json:"total_cost_usd"`
	PeriodCostUSD float64 `json:"period_cost_usd"`

	ActiveRoutineCount   int `json:"active_routine_count"`
	ArchivedRoutineCount int `json:"archived_routine_count"`

	// PulseCount/FailedPulseCount are both period-scoped (unlike
	// ConstellationStats.ShootingStarCount, which is always all-time) —
	// "how's Pulsar doing lately" is exactly what a trailing-window
	// failure rate is for, and periodDays=0 still answers the all-time
	// question when that's what's asked.
	PulseCount       int `json:"pulse_count"`
	FailedPulseCount int `json:"failed_pulse_count"`

	// ToolCallCounts/ToolErrorCounts mirror Stats' own fields exactly
	// (same events-table shape, same "tool.*" source convention), just
	// scoped to threads.source = 'pulsar' — see GetPulsarStats.
	ToolCallCounts  map[string]int `json:"tool_call_counts"`
	ToolErrorCounts map[string]int `json:"tool_error_counts"`

	// CheckInCount/StaleStreakCount/MaxTurnsWrapupCount mirror Stats' own
	// research-loop steering fields (see agent/driver.go's emitNudge),
	// scoped to pulsar pulses only.
	CheckInCount        int `json:"check_in_count"`
	StaleStreakCount    int `json:"stale_streak_count"`
	MaxTurnsWrapupCount int `json:"max_turns_wrapup_count"`
}

// GetPulsarStats aggregates on demand from pulsar_routines and the same
// threads/events tables GetStats reads — no running counters, same "cheap
// enough to scan fresh every request" reasoning as stats.go's own doc
// comment. periodDays of 0 means all time.
func (s *Store) GetPulsarStats(periodDays int) (*PulsarStats, error) {
	stats := &PulsarStats{
		PeriodDays:      periodDays,
		ToolCallCounts:  map[string]int{},
		ToolErrorCounts: map[string]int{},
	}

	var since string
	if periodDays > 0 {
		since = time.Now().AddDate(0, 0, -periodDays).UTC().Format("2006-01-02 15:04:05")
	}

	if err := s.db.QueryRow(`SELECT COUNT(*) FROM pulsar_routines WHERE archived_at IS NULL`).Scan(&stats.ActiveRoutineCount); err != nil {
		return nil, fmt.Errorf("pulsar stats: active routine count: %w", err)
	}
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM pulsar_routines WHERE archived_at IS NOT NULL`).Scan(&stats.ArchivedRoutineCount); err != nil {
		return nil, fmt.Errorf("pulsar stats: archived routine count: %w", err)
	}

	// Cost: the same fork-safe "count each real message once" query
	// GetStats uses for CostBySource.Pulsar, reused here rather than a
	// third copy of it.
	if err := s.costBySource("", func(source string, cost float64) {
		if source == "pulsar" {
			stats.TotalCostUSD = cost
		}
	}); err != nil {
		return nil, fmt.Errorf("pulsar stats: total cost: %w", err)
	}
	if err := s.costBySource(since, func(source string, cost float64) {
		if source == "pulsar" {
			stats.PeriodCostUSD = cost
		}
	}); err != nil {
		return nil, fmt.Errorf("pulsar stats: period cost: %w", err)
	}

	pulseQuery := `SELECT COUNT(*) FROM threads WHERE source = 'pulsar' AND disabled = 0 AND fork_root_id = ''`
	pulseArgs := []interface{}{}
	if since != "" {
		pulseQuery += ` AND created_at >= ?`
		pulseArgs = append(pulseArgs, since)
	}
	if err := s.db.QueryRow(pulseQuery, pulseArgs...).Scan(&stats.PulseCount); err != nil {
		return nil, fmt.Errorf("pulsar stats: pulse count: %w", err)
	}

	// A "failed" pulse is any pulsar thread that logged at least one
	// source='turn' level='error' event — the same event firePulse's own
	// turnErr check watches for live (see gateway/pulsar_scheduler.go),
	// just counted after the fact instead of during the fire. Distinct
	// thread_id, not raw COUNT(*): a single turn can log more than one
	// error event on its way to failing, and this answers "how many
	// pulses failed," not "how many error events fired."
	failedQuery := `SELECT COUNT(DISTINCT e.thread_id) FROM events e
		JOIN threads t ON t.id = e.thread_id
		WHERE t.source = 'pulsar' AND e.source = 'turn' AND e.level = 'error'`
	failedArgs := []interface{}{}
	if since != "" {
		failedQuery += ` AND e.created_at >= ?`
		failedArgs = append(failedArgs, since)
	}
	if err := s.db.QueryRow(failedQuery, failedArgs...).Scan(&stats.FailedPulseCount); err != nil {
		return nil, fmt.Errorf("pulsar stats: failed pulse count: %w", err)
	}

	// Tool calls/errors: same shape as GetStats' own toolQuery, joined to
	// threads and scoped to source = 'pulsar' instead of scanning every
	// event in the deployment.
	toolQuery := `SELECT e.source, e.level, COUNT(*) FROM events e
		JOIN threads t ON t.id = e.thread_id
		WHERE t.source = 'pulsar' AND e.source LIKE 'tool.%' AND e.message = 'tool call finished'`
	toolArgs := []interface{}{}
	if since != "" {
		toolQuery += ` AND e.created_at >= ?`
		toolArgs = append(toolArgs, since)
	}
	toolQuery += ` GROUP BY e.source, e.level`
	toolRows, err := s.db.Query(toolQuery, toolArgs...)
	if err != nil {
		return nil, fmt.Errorf("pulsar stats: tool call counts: %w", err)
	}
	defer toolRows.Close()
	for toolRows.Next() {
		var source, level string
		var count int
		if err := toolRows.Scan(&source, &level, &count); err != nil {
			return nil, fmt.Errorf("pulsar stats: tool call counts: %w", err)
		}
		tool := strings.TrimPrefix(source, "tool.")
		stats.ToolCallCounts[tool] += count
		if level == "warn" {
			stats.ToolErrorCounts[tool] += count
		}
	}
	if err := toolRows.Err(); err != nil {
		return nil, fmt.Errorf("pulsar stats: tool call counts: %w", err)
	}

	// Nudge kind lives inside the JSON data blob, same as GetStats' own
	// nudgeQuery — see its doc comment for why this isn't a GROUP BY.
	nudgeQuery := `SELECT e.data FROM events e
		JOIN threads t ON t.id = e.thread_id
		WHERE t.source = 'pulsar' AND e.source = 'agent.nudge'`
	nudgeArgs := []interface{}{}
	if since != "" {
		nudgeQuery += ` AND e.created_at >= ?`
		nudgeArgs = append(nudgeArgs, since)
	}
	nudgeRows, err := s.db.Query(nudgeQuery, nudgeArgs...)
	if err != nil {
		return nil, fmt.Errorf("pulsar stats: nudge counts: %w", err)
	}
	defer nudgeRows.Close()
	for nudgeRows.Next() {
		var dataJSON string
		if err := nudgeRows.Scan(&dataJSON); err != nil {
			return nil, fmt.Errorf("pulsar stats: nudge counts: %w", err)
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
		return nil, fmt.Errorf("pulsar stats: nudge counts: %w", err)
	}

	return stats, nil
}

// rowsAffectedOrNotFound is the shared "did this UPDATE actually match a
// row" check every pulsar_routines write above uses — same pattern as
// store.go's execOne, but returning a caller-supplied not-found error
// instead of sql.ErrNoRows, since ErrPulsarRoutineNotFound is what the
// gateway handlers actually check for (errors.Is-style) to 404 correctly.
func rowsAffectedOrNotFound(res sql.Result, notFound error) error {
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return notFound
	}
	return nil
}
