// fields.go persists Fields — see docs/plans/fields.md (issue #119). A
// field is a named container of threads carrying its own custom
// instructions and a handful of per-field turn defaults; its shared file
// pool lives on disk (<CodeExecWorkspaceDir>/<id>/), not in this package.
package store

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
)

// ErrFieldNotFound is returned when a field id matches no row — kept
// distinct from raw SQL errors so the HTTP layer can answer 404 rather than
// 500, same reasoning as ErrMemoryNotFound.
var ErrFieldNotFound = errors.New("field not found")

// Field memory modes (issue #133). What a turn in the field reads/writes:
//
//	default    global memory only (reads and writes), as outside any field
//	field_only the field's own store only (reads and writes)
//	both       reads the field's store first, then global; writes go to the
//	           field's store only — a field thread can never write global
//	none       no memory at all
const (
	FieldMemoryDefault   = "default"
	FieldMemoryFieldOnly = "field_only"
	FieldMemoryBoth      = "both"
	FieldMemoryNone      = "none"
)

// ValidFieldMemoryMode reports whether m is a memory_mode this build
// actually implements.
func ValidFieldMemoryMode(m string) bool {
	switch m {
	case FieldMemoryDefault, FieldMemoryFieldOnly, FieldMemoryBoth, FieldMemoryNone:
		return true
	}
	return false
}

// Field is one row of the fields table (see its schema comment for what
// each field means). ThreadCount is not a column — it's filled in by
// GetField/ListFields for the hub's cards and counts only live threads,
// the same set ListFieldThreads returns.
type Field struct {
	ID                    string    `json:"id"`
	Name                  string    `json:"name"`
	Description           string    `json:"description"`
	CustomInstructions    string    `json:"custom_instructions"`
	Favorite              bool      `json:"favorite"`
	DefaultFocusMode      string    `json:"default_focus_mode"`
	DefaultModel          string    `json:"default_model"`
	MemoryMode            string    `json:"memory_mode"`
	ConstellationVisible  bool      `json:"constellation_visible"`
	ExcludeFromChatSearch bool      `json:"exclude_from_chat_search"`
	Color                 string    `json:"color"`
	ThreadCount           int       `json:"thread_count"`
	CreatedAt             time.Time `json:"created_at"`
	UpdatedAt             time.Time `json:"updated_at"`
}

// FieldUpdate is a partial update: a nil field is left untouched. Pointers
// (not zero-value checks) because "" and false are meaningful values here —
// an empty default_model means "inherit the global default", and clearing
// it must be expressible.
type FieldUpdate struct {
	Name                  *string
	Description           *string
	CustomInstructions    *string
	Favorite              *bool
	DefaultFocusMode      *string
	DefaultModel          *string
	MemoryMode            *string
	ConstellationVisible  *bool
	ExcludeFromChatSearch *bool
	Color                 *string
}

// liveThreadFilter is the ordinary "a real, visible thread" predicate —
// the same conditions ListThreads applies, minus the source exclusions that
// only exist to keep pulses/Weaver runs out of the sidebar (those never
// carry a field_id in the first place).
const liveThreadFilter = `disabled = 0 AND fork_root_id = '' AND ghost = 0`

// notInFieldWhere builds a "this thread's field has NOT opted out" SQL
// condition — the shared shape of the chat-search exclusion and the
// Constellation-visibility gate, both of which are an unconditional skip
// joined through field_id at query time rather than a flag mirrored onto
// every thread row (a mirrored copy would go stale the moment a field's
// setting or a thread's membership changed). A NULL field_id has no
// matching field row, so NOT EXISTS is true and an ungrouped thread is
// unaffected. fieldIDExpr and optOut are compile-time SQL fragments from
// this package's own call sites, never user input.
func notInFieldWhere(fieldIDExpr, optOut string) string {
	return `NOT EXISTS (SELECT 1 FROM fields px WHERE px.id = ` + fieldIDExpr + ` AND px.` + optOut + `)`
}

// fieldColumns/scanField keep GetField and ListFields reading the
// same column list in the same order — a drifted pair of hand-written
// SELECTs is exactly the kind of bug a positional Scan hides until runtime.
const fieldColumns = `id, name, description, custom_instructions, favorite, default_focus_mode, default_model, memory_mode, constellation_visible, exclude_from_chat_search, color,
	(SELECT COUNT(*) FROM threads t WHERE t.field_id = fields.id AND t.disabled = 0 AND t.fork_root_id = '' AND t.ghost = 0),
	created_at, updated_at`

func scanField(r rowScanner) (*Field, error) {
	var p Field
	err := r.Scan(&p.ID, &p.Name, &p.Description, &p.CustomInstructions, &p.Favorite, &p.DefaultFocusMode, &p.DefaultModel, &p.MemoryMode,
		&p.ConstellationVisible, &p.ExcludeFromChatSearch, &p.Color, &p.ThreadCount, &p.CreatedAt, &p.UpdatedAt)
	if err != nil {
		return nil, err
	}
	return &p, nil
}

// CreateField inserts a new field with a fresh id. Only the user-facing
// fields are taken from p; id/timestamps/thread count are the store's own.
// MemoryMode falls back to "default" when empty, so a caller building a
// Field literal without it doesn't trip ValidFieldMemoryMode.
func (s *Store) CreateField(p Field) (*Field, error) {
	p.Name = strings.TrimSpace(p.Name)
	if p.Name == "" {
		return nil, errors.New("field name is required")
	}
	if p.MemoryMode == "" {
		p.MemoryMode = FieldMemoryDefault
	}
	if !ValidFieldMemoryMode(p.MemoryMode) {
		return nil, fmt.Errorf("invalid memory_mode %q", p.MemoryMode)
	}
	id := uuid.NewString()
	if _, err := s.db.Exec(
		`INSERT INTO fields (id, name, description, custom_instructions, favorite, default_focus_mode, default_model, memory_mode, constellation_visible, exclude_from_chat_search, color)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		id, p.Name, p.Description, p.CustomInstructions, p.Favorite, p.DefaultFocusMode, p.DefaultModel, p.MemoryMode,
		p.ConstellationVisible, p.ExcludeFromChatSearch, p.Color,
	); err != nil {
		return nil, fmt.Errorf("create field: %w", err)
	}
	return s.GetField(id)
}

// GetField returns ErrFieldNotFound for an unknown id.
func (s *Store) GetField(id string) (*Field, error) {
	p, err := scanField(s.db.QueryRow(`SELECT `+fieldColumns+` FROM fields WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrFieldNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("get field: %w", err)
	}
	return p, nil
}

// ListFields returns every field, most recently touched first — the
// hub's order. Favorited ones are not split out here; the frontend filters
// for the sidebar the same way it does for threads.favorite.
func (s *Store) ListFields() ([]Field, error) {
	rows, err := s.db.Query(`SELECT ` + fieldColumns + ` FROM fields ORDER BY updated_at DESC, name`)
	if err != nil {
		return nil, fmt.Errorf("list fields: %w", err)
	}
	defer rows.Close()
	var out []Field
	for rows.Next() {
		p, err := scanField(rows)
		if err != nil {
			return nil, fmt.Errorf("list fields: %w", err)
		}
		out = append(out, *p)
	}
	return out, rows.Err()
}

// UpdateField applies the non-nil fields of u and bumps updated_at,
// returning the fresh row. Built as a dynamic SET list rather than one fixed
// UPDATE so an untouched field is never rewritten with a stale value read a
// moment earlier — two settings toggles landing back to back can't clobber
// each other.
func (s *Store) UpdateField(id string, u FieldUpdate) (*Field, error) {
	var sets []string
	var args []any
	add := func(col string, v any) {
		sets = append(sets, col+" = ?")
		args = append(args, v)
	}
	if u.Name != nil {
		name := strings.TrimSpace(*u.Name)
		if name == "" {
			return nil, errors.New("field name is required")
		}
		add("name", name)
	}
	if u.Description != nil {
		add("description", *u.Description)
	}
	if u.CustomInstructions != nil {
		add("custom_instructions", *u.CustomInstructions)
	}
	if u.Favorite != nil {
		add("favorite", *u.Favorite)
	}
	if u.DefaultFocusMode != nil {
		add("default_focus_mode", *u.DefaultFocusMode)
	}
	if u.DefaultModel != nil {
		add("default_model", *u.DefaultModel)
	}
	if u.MemoryMode != nil {
		if !ValidFieldMemoryMode(*u.MemoryMode) {
			return nil, fmt.Errorf("invalid memory_mode %q", *u.MemoryMode)
		}
		add("memory_mode", *u.MemoryMode)
	}
	if u.ConstellationVisible != nil {
		add("constellation_visible", *u.ConstellationVisible)
	}
	if u.ExcludeFromChatSearch != nil {
		add("exclude_from_chat_search", *u.ExcludeFromChatSearch)
	}
	if u.Color != nil {
		add("color", *u.Color)
	}
	if len(sets) == 0 {
		return s.GetField(id)
	}
	// Same millisecond-precision stamp threads use for updated_at (see
	// TouchUpdatedAt), so ListFields' recency order can tell two edits in
	// the same second apart.
	sets = append(sets, "updated_at = strftime('%Y-%m-%d %H:%M:%f', 'now')")
	args = append(args, id)
	res, err := s.db.Exec(`UPDATE fields SET `+strings.Join(sets, ", ")+` WHERE id = ?`, args...)
	if err != nil {
		return nil, fmt.Errorf("update field: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return nil, ErrFieldNotFound
	}
	return s.GetField(id)
}

// TouchField bumps updated_at without changing anything else — UpdateField
// with no fields is deliberately a no-op, so activity that isn't a settings
// edit (a file added to the shared pool) needs its own way to float the
// field up the hub's recency order. Best-effort: a failure only costs the
// re-ordering, so it's logged nowhere and returns nothing.
func (s *Store) TouchField(id string) {
	s.db.Exec(`UPDATE fields SET updated_at = strftime('%Y-%m-%d %H:%M:%f', 'now') WHERE id = ?`, id)
}

// DeleteField removes the row and orphans its threads back to ungrouped,
// in one transaction — threads.field_id is a real FK (foreign_keys=on), so
// deleting the row first would fail, and orphaning first then crashing
// before the DELETE would leave a field that silently lost its threads.
// Hidden fork variants never carry a field_id (see ForkThread), so the one
// UPDATE covers every row that could reference it. The caller owns removing
// the shared directory on disk, after this succeeds — a leftover directory
// is harmless, a deleted directory under a surviving field is not.
func (s *Store) DeleteField(id string) error {
	tx, err := s.db.Begin()
	if err != nil {
		return fmt.Errorf("delete field: %w", err)
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`UPDATE threads SET field_id = NULL WHERE field_id = ?`, id); err != nil {
		return fmt.Errorf("delete field: orphaning threads: %w", err)
	}
	res, err := tx.Exec(`DELETE FROM fields WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("delete field: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrFieldNotFound
	}
	return tx.Commit()
}

// SetThreadField moves a thread into a field, or out of any field when
// fieldID is nil. Verifies the field exists first so a stale picker
// gets ErrFieldNotFound (a 404) instead of a raw FK-constraint error.
// Deliberately does not touch updated_at: reorganizing a thread shouldn't
// bump it to the top of Recents as if it had just been chatted in.
func (s *Store) SetThreadField(threadID string, fieldID *string) error {
	if fieldID != nil {
		var one int
		if err := s.db.QueryRow(`SELECT 1 FROM fields WHERE id = ?`, *fieldID).Scan(&one); errors.Is(err, sql.ErrNoRows) {
			return ErrFieldNotFound
		} else if err != nil {
			return fmt.Errorf("set thread field: %w", err)
		}
	}
	// Not liveThreadFilter: a brand-new thread is bound right after creation
	// and must not depend on ghost/variant state, and a hidden fork variant
	// is never a legitimate target anyway (fork_root_id = '' keeps that).
	res, err := s.db.Exec(`UPDATE threads SET field_id = ? WHERE id = ? AND disabled = 0 AND fork_root_id = ''`, fieldID, threadID)
	if err != nil {
		return fmt.Errorf("set thread field: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return sql.ErrNoRows
	}
	return nil
}

// ListFieldThreads returns a field's live threads newest-first, for the
// detail view. Same shape/columns as ListThreads so the frontend can reuse
// its thread-row component unchanged.
func (s *Store) ListFieldThreads(fieldID string) ([]Thread, error) {
	rows, err := s.db.Query(
		`SELECT id, title, model, cost_usd, context_tokens, source, favorite, focus_mode, deep_research, pulsar_routine_id, field_id, created_at, updated_at
		 FROM threads WHERE field_id = ? AND `+liveThreadFilter+`
		 ORDER BY updated_at DESC`,
		fieldID,
	)
	if err != nil {
		return nil, fmt.Errorf("list field threads: %w", err)
	}
	defer rows.Close()
	var threads []Thread
	for rows.Next() {
		var t Thread
		if err := rows.Scan(&t.ID, &t.Title, &t.Model, &t.CostUSD, &t.ContextTokens, &t.Source, &t.Favorite, &t.FocusMode, &t.DeepResearch, &t.PulsarRoutineID, &t.FieldID, &t.CreatedAt, &t.UpdatedAt); err != nil {
			return nil, fmt.Errorf("list field threads: %w", err)
		}
		threads = append(threads, t)
	}
	return threads, rows.Err()
}
