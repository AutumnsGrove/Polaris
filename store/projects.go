// projects.go persists Projects — see docs/plans/projects.md (issue #119). A
// project is a named container of threads carrying its own custom
// instructions and a handful of per-project turn defaults; its shared file
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

// ErrProjectNotFound is returned when a project id matches no row — kept
// distinct from raw SQL errors so the HTTP layer can answer 404 rather than
// 500, same reasoning as ErrMemoryNotFound.
var ErrProjectNotFound = errors.New("project not found")

// Project memory modes. "project_scoped" (a real isolated per-project memory
// store) is the plan's v2 — deliberately absent here so nothing can persist
// a mode that would silently behave like "default".
const (
	ProjectMemoryDefault = "default"
	ProjectMemoryNone    = "none"
)

// ValidProjectMemoryMode reports whether m is a memory_mode this build
// actually implements.
func ValidProjectMemoryMode(m string) bool {
	return m == ProjectMemoryDefault || m == ProjectMemoryNone
}

// Project is one row of the projects table (see its schema comment for what
// each field means). ThreadCount is not a column — it's filled in by
// GetProject/ListProjects for the hub's cards and counts only live threads,
// the same set ListProjectThreads returns.
type Project struct {
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

// ProjectUpdate is a partial update: a nil field is left untouched. Pointers
// (not zero-value checks) because "" and false are meaningful values here —
// an empty default_model means "inherit the global default", and clearing
// it must be expressible.
type ProjectUpdate struct {
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
// carry a project_id in the first place).
const liveThreadFilter = `disabled = 0 AND fork_root_id = '' AND ghost = 0`

// notInProjectWhere builds a "this thread's project has NOT opted out" SQL
// condition — the shared shape of the chat-search exclusion and the
// Constellation-visibility gate, both of which are an unconditional skip
// joined through project_id at query time rather than a flag mirrored onto
// every thread row (a mirrored copy would go stale the moment a project's
// setting or a thread's membership changed). A NULL project_id has no
// matching project row, so NOT EXISTS is true and an ungrouped thread is
// unaffected. projectIDExpr and optOut are compile-time SQL fragments from
// this package's own call sites, never user input.
func notInProjectWhere(projectIDExpr, optOut string) string {
	return `NOT EXISTS (SELECT 1 FROM projects px WHERE px.id = ` + projectIDExpr + ` AND px.` + optOut + `)`
}

// projectColumns/scanProject keep GetProject and ListProjects reading the
// same column list in the same order — a drifted pair of hand-written
// SELECTs is exactly the kind of bug a positional Scan hides until runtime.
const projectColumns = `id, name, description, custom_instructions, favorite, default_focus_mode, default_model, memory_mode, constellation_visible, exclude_from_chat_search, color,
	(SELECT COUNT(*) FROM threads t WHERE t.project_id = projects.id AND t.disabled = 0 AND t.fork_root_id = '' AND t.ghost = 0),
	created_at, updated_at`

func scanProject(r rowScanner) (*Project, error) {
	var p Project
	err := r.Scan(&p.ID, &p.Name, &p.Description, &p.CustomInstructions, &p.Favorite, &p.DefaultFocusMode, &p.DefaultModel, &p.MemoryMode,
		&p.ConstellationVisible, &p.ExcludeFromChatSearch, &p.Color, &p.ThreadCount, &p.CreatedAt, &p.UpdatedAt)
	if err != nil {
		return nil, err
	}
	return &p, nil
}

// CreateProject inserts a new project with a fresh id. Only the user-facing
// fields are taken from p; id/timestamps/thread count are the store's own.
// MemoryMode falls back to "default" when empty, so a caller building a
// Project literal without it doesn't trip ValidProjectMemoryMode.
func (s *Store) CreateProject(p Project) (*Project, error) {
	p.Name = strings.TrimSpace(p.Name)
	if p.Name == "" {
		return nil, errors.New("project name is required")
	}
	if p.MemoryMode == "" {
		p.MemoryMode = ProjectMemoryDefault
	}
	if !ValidProjectMemoryMode(p.MemoryMode) {
		return nil, fmt.Errorf("invalid memory_mode %q", p.MemoryMode)
	}
	id := uuid.NewString()
	if _, err := s.db.Exec(
		`INSERT INTO projects (id, name, description, custom_instructions, favorite, default_focus_mode, default_model, memory_mode, constellation_visible, exclude_from_chat_search, color)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		id, p.Name, p.Description, p.CustomInstructions, p.Favorite, p.DefaultFocusMode, p.DefaultModel, p.MemoryMode,
		p.ConstellationVisible, p.ExcludeFromChatSearch, p.Color,
	); err != nil {
		return nil, fmt.Errorf("create project: %w", err)
	}
	return s.GetProject(id)
}

// GetProject returns ErrProjectNotFound for an unknown id.
func (s *Store) GetProject(id string) (*Project, error) {
	p, err := scanProject(s.db.QueryRow(`SELECT `+projectColumns+` FROM projects WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrProjectNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("get project: %w", err)
	}
	return p, nil
}

// ListProjects returns every project, most recently touched first — the
// hub's order. Favorited ones are not split out here; the frontend filters
// for the sidebar the same way it does for threads.favorite.
func (s *Store) ListProjects() ([]Project, error) {
	rows, err := s.db.Query(`SELECT ` + projectColumns + ` FROM projects ORDER BY updated_at DESC, name`)
	if err != nil {
		return nil, fmt.Errorf("list projects: %w", err)
	}
	defer rows.Close()
	var out []Project
	for rows.Next() {
		p, err := scanProject(rows)
		if err != nil {
			return nil, fmt.Errorf("list projects: %w", err)
		}
		out = append(out, *p)
	}
	return out, rows.Err()
}

// UpdateProject applies the non-nil fields of u and bumps updated_at,
// returning the fresh row. Built as a dynamic SET list rather than one fixed
// UPDATE so an untouched field is never rewritten with a stale value read a
// moment earlier — two settings toggles landing back to back can't clobber
// each other.
func (s *Store) UpdateProject(id string, u ProjectUpdate) (*Project, error) {
	var sets []string
	var args []any
	add := func(col string, v any) {
		sets = append(sets, col+" = ?")
		args = append(args, v)
	}
	if u.Name != nil {
		name := strings.TrimSpace(*u.Name)
		if name == "" {
			return nil, errors.New("project name is required")
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
		if !ValidProjectMemoryMode(*u.MemoryMode) {
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
		return s.GetProject(id)
	}
	// Same millisecond-precision stamp threads use for updated_at (see
	// TouchUpdatedAt), so ListProjects' recency order can tell two edits in
	// the same second apart.
	sets = append(sets, "updated_at = strftime('%Y-%m-%d %H:%M:%f', 'now')")
	args = append(args, id)
	res, err := s.db.Exec(`UPDATE projects SET `+strings.Join(sets, ", ")+` WHERE id = ?`, args...)
	if err != nil {
		return nil, fmt.Errorf("update project: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return nil, ErrProjectNotFound
	}
	return s.GetProject(id)
}

// DeleteProject removes the row and orphans its threads back to ungrouped,
// in one transaction — threads.project_id is a real FK (foreign_keys=on), so
// deleting the row first would fail, and orphaning first then crashing
// before the DELETE would leave a project that silently lost its threads.
// Hidden fork variants never carry a project_id (see ForkThread), so the one
// UPDATE covers every row that could reference it. The caller owns removing
// the shared directory on disk, after this succeeds — a leftover directory
// is harmless, a deleted directory under a surviving project is not.
func (s *Store) DeleteProject(id string) error {
	tx, err := s.db.Begin()
	if err != nil {
		return fmt.Errorf("delete project: %w", err)
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`UPDATE threads SET project_id = NULL WHERE project_id = ?`, id); err != nil {
		return fmt.Errorf("delete project: orphaning threads: %w", err)
	}
	res, err := tx.Exec(`DELETE FROM projects WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("delete project: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrProjectNotFound
	}
	return tx.Commit()
}

// SetThreadProject moves a thread into a project, or out of any project when
// projectID is nil. Verifies the project exists first so a stale picker
// gets ErrProjectNotFound (a 404) instead of a raw FK-constraint error.
// Deliberately does not touch updated_at: reorganizing a thread shouldn't
// bump it to the top of Recents as if it had just been chatted in.
func (s *Store) SetThreadProject(threadID string, projectID *string) error {
	if projectID != nil {
		var one int
		if err := s.db.QueryRow(`SELECT 1 FROM projects WHERE id = ?`, *projectID).Scan(&one); errors.Is(err, sql.ErrNoRows) {
			return ErrProjectNotFound
		} else if err != nil {
			return fmt.Errorf("set thread project: %w", err)
		}
	}
	// Not liveThreadFilter: a brand-new thread is bound right after creation
	// and must not depend on ghost/variant state, and a hidden fork variant
	// is never a legitimate target anyway (fork_root_id = '' keeps that).
	res, err := s.db.Exec(`UPDATE threads SET project_id = ? WHERE id = ? AND disabled = 0 AND fork_root_id = ''`, projectID, threadID)
	if err != nil {
		return fmt.Errorf("set thread project: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return sql.ErrNoRows
	}
	return nil
}

// ListProjectThreads returns a project's live threads newest-first, for the
// detail view. Same shape/columns as ListThreads so the frontend can reuse
// its thread-row component unchanged.
func (s *Store) ListProjectThreads(projectID string) ([]Thread, error) {
	rows, err := s.db.Query(
		`SELECT id, title, model, cost_usd, context_tokens, source, favorite, focus_mode, deep_research, pulsar_routine_id, project_id, created_at, updated_at
		 FROM threads WHERE project_id = ? AND `+liveThreadFilter+`
		 ORDER BY updated_at DESC`,
		projectID,
	)
	if err != nil {
		return nil, fmt.Errorf("list project threads: %w", err)
	}
	defer rows.Close()
	var threads []Thread
	for rows.Next() {
		var t Thread
		if err := rows.Scan(&t.ID, &t.Title, &t.Model, &t.CostUSD, &t.ContextTokens, &t.Source, &t.Favorite, &t.FocusMode, &t.DeepResearch, &t.PulsarRoutineID, &t.ProjectID, &t.CreatedAt, &t.UpdatedAt); err != nil {
			return nil, fmt.Errorf("list project threads: %w", err)
		}
		threads = append(threads, t)
	}
	return threads, rows.Err()
}
