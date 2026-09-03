// pulsar.go persists Pulsar routines — see docs/plans/pulsar-routines.md.
// A routine is a saved prompt plus a schedule; each firing (a "pulse") is a
// normal thread tagged with source = 'pulsar' and pulsar_routine_id (see
// store.go's threads schema comment), not a record in this table itself.
package store

import (
	"database/sql"
	"errors"
	"fmt"
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
