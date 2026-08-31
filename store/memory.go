// memory.go persists the memory tool's entries (see tools/memory.go) —
// durable facts Polaris's model chooses to remember about the user or
// ongoing work, addressed by a model-chosen name rather than a surrogate
// id. Modeled directly on Claude Code's own memory system: a short index
// (name/type/description) that's cheap enough to always be in context,
// backing full content fetched only on demand.
package store

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"
)

// ErrMemoryNotFound is returned by GetMemory/UpdateMemory/DeleteMemory
// when name doesn't match any row — distinguished from other errors so
// the memory tool can give the model a clear "no such memory" result
// instead of a raw SQL error string.
var ErrMemoryNotFound = errors.New("memory not found")

// ErrMemoryExists is returned by CreateMemory when name is already taken —
// forces the model through UpdateMemory to change an existing memory
// rather than silently overwriting it with a second write call.
var ErrMemoryExists = errors.New("memory already exists")

// Memory is one full memory entry, as returned by GetMemory.
type Memory struct {
	Name        string `json:"name"`
	Type        string `json:"type"`
	Description string `json:"description"`
	Content     string `json:"content"`
	CreatedAt   string `json:"created_at"`
	UpdatedAt   string `json:"updated_at"`
}

// MemoryIndexEntry is the compact, always-in-context form of a memory —
// name and description only, no content — as listed by ListMemories.
type MemoryIndexEntry struct {
	Name        string `json:"name"`
	Type        string `json:"type"`
	Description string `json:"description"`
}

// CreateMemory inserts a brand-new memory, failing with ErrMemoryExists if
// an active memory already has that name — see the memory tool's "write"
// action, which is deliberately create-only for anything currently live.
//
// name is a human/model-chosen slug, not a surrogate id (unlike threads'
// UUIDs), so it can collide with a name that was forgotten (soft-deleted,
// see DeleteMemory) in the past — that name is free again from the user's
// perspective, and permanently blocking its reuse just because a disabled
// row still occupies the primary key would be a confusing, unexplained
// dead end. So a name matching an existing but disabled row revives it
// (overwriting it with the new type/description/content) instead of
// failing; a name matching an active row still fails with ErrMemoryExists,
// same as before.
func (s *Store) CreateMemory(name, memType, description, content string) error {
	var disabled int
	err := s.db.QueryRow(`SELECT disabled FROM memories WHERE name = ?`, name).Scan(&disabled)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		if _, err := s.db.Exec(
			`INSERT INTO memories (name, type, description, content) VALUES (?, ?, ?, ?)`,
			name, memType, description, content,
		); err != nil {
			if isUniqueConstraintErr(err) {
				// A concurrent insert of the same brand-new name won this
				// race between the SELECT above and this INSERT — same
				// "someone else already has this name" outcome either way.
				return ErrMemoryExists
			}
			return fmt.Errorf("create memory: %w", err)
		}
		return nil
	case err != nil:
		return fmt.Errorf("create memory: %w", err)
	case disabled == 0:
		return ErrMemoryExists
	default:
		if _, err := s.db.Exec(
			`UPDATE memories SET type = ?, description = ?, content = ?, disabled = 0, updated_at = CURRENT_TIMESTAMP WHERE name = ?`,
			memType, description, content, name,
		); err != nil {
			return fmt.Errorf("create memory (reviving forgotten name): %w", err)
		}
		return nil
	}
}

// UpdateMemory overwrites an existing memory's type/description/content in
// place — name is the stable identity and never changes. An empty
// memType/description/content means "leave this field as it is", so the
// memory tool's "edit" action can change just one field without a caller
// ever needing to read the row first. That's done here as a single CASE-WHEN
// UPDATE, not a Go-side read-then-write, specifically because
// agent/driver.go's dispatchToolCallsConcurrently runs every tool call in a
// batch on its own goroutine: two concurrent edits to the same memory (one
// changing description, one changing content) reading a shared snapshot
// before either writes back would let whichever write lands second silently
// clobber the other's change. A single UPDATE statement has no such window —
// SQLite applies it atomically. Returns ErrMemoryNotFound if name doesn't
// exist or is currently disabled (forgotten) — editing something forgotten
// isn't meaningful; CreateMemory's revival path is the way to bring a
// forgotten name back.
func (s *Store) UpdateMemory(name, memType, description, content string) error {
	res, err := s.db.Exec(
		`UPDATE memories SET
			type = CASE WHEN ? = '' THEN type ELSE ? END,
			description = CASE WHEN ? = '' THEN description ELSE ? END,
			content = CASE WHEN ? = '' THEN content ELSE ? END,
			updated_at = CURRENT_TIMESTAMP
		WHERE name = ? AND disabled = 0`,
		memType, memType, description, description, content, content, name,
	)
	if err != nil {
		return fmt.Errorf("update memory: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("update memory: %w", err)
	}
	if n == 0 {
		return ErrMemoryNotFound
	}
	return nil
}

// GetMemory returns one active memory's full content, or ErrMemoryNotFound
// — including for a name that exists but is currently disabled (forgotten),
// so a forgotten memory is unreachable through every read path, not just
// the index.
func (s *Store) GetMemory(name string) (*Memory, error) {
	var m Memory
	err := s.db.QueryRow(
		`SELECT name, type, description, content, created_at, updated_at FROM memories WHERE name = ? AND disabled = 0`,
		name,
	).Scan(&m.Name, &m.Type, &m.Description, &m.Content, &m.CreatedAt, &m.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrMemoryNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("get memory: %w", err)
	}
	return &m, nil
}

// ListMemories returns every memory's index entry (name/type/description,
// no content), ordered by type then name so related memories group
// together in the rendered {memories} prompt block — see
// agent/driver.go's applyMemoriesPlaceholder.
func (s *Store) ListMemories() ([]MemoryIndexEntry, error) {
	rows, err := s.db.Query(`SELECT name, type, description FROM memories WHERE disabled = 0 ORDER BY type, name`)
	if err != nil {
		return nil, fmt.Errorf("list memories: %w", err)
	}
	defer rows.Close()

	entries := []MemoryIndexEntry{}
	for rows.Next() {
		var e MemoryIndexEntry
		if err := rows.Scan(&e.Name, &e.Type, &e.Description); err != nil {
			return nil, fmt.Errorf("list memories: %w", err)
		}
		entries = append(entries, e)
	}
	return entries, rows.Err()
}

// ListMemoriesFull returns every memory's full row (including content and
// timestamps) — for the settings panel's memory list, which shows full
// content inline rather than a name-only index. Deliberately a separate
// method from ListMemories rather than an option/flag on it: ListMemories
// runs on every single agent turn via MemoryIndexPrompt, so keeping it
// narrow (three short columns) matters there in a way it doesn't for a
// settings-panel page load.
func (s *Store) ListMemoriesFull() ([]Memory, error) {
	rows, err := s.db.Query(`SELECT name, type, description, content, created_at, updated_at FROM memories WHERE disabled = 0 ORDER BY type, name`)
	if err != nil {
		return nil, fmt.Errorf("list memories: %w", err)
	}
	defer rows.Close()

	memories := []Memory{}
	for rows.Next() {
		var m Memory
		if err := rows.Scan(&m.Name, &m.Type, &m.Description, &m.Content, &m.CreatedAt, &m.UpdatedAt); err != nil {
			return nil, fmt.Errorf("list memories: %w", err)
		}
		memories = append(memories, m)
	}
	return memories, rows.Err()
}

// DeleteMemory soft-deletes a memory — the memory tool's "forget" action,
// for a memory the model has confirmed is wrong, stale, or the user
// explicitly asked to have forgotten. Sets disabled = 1 rather than
// issuing a real DELETE, same soft-delete shape as threads (see
// store.go's threads.disabled column and DeleteThread): the row (and its
// history) survives, just excluded from every read path (ListMemories,
// ListMemoriesFull, GetMemory) so a forgotten memory behaves as if it were
// gone everywhere that matters, without losing the record outright.
// CreateMemory's revival path is what makes the name reusable again.
// Returns ErrMemoryNotFound if name doesn't exist or is already disabled.
func (s *Store) DeleteMemory(name string) error {
	res, err := s.db.Exec(`UPDATE memories SET disabled = 1, updated_at = CURRENT_TIMESTAMP WHERE name = ? AND disabled = 0`, name)
	if err != nil {
		return fmt.Errorf("delete memory: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("delete memory: %w", err)
	}
	if n == 0 {
		return ErrMemoryNotFound
	}
	return nil
}

// isUniqueConstraintErr reports whether err came from violating a UNIQUE/
// PRIMARY KEY constraint — modernc.org/sqlite (this project's driver, see
// store.go) doesn't export a typed error for this, so matching on the
// message text is the only option; both "UNIQUE constraint failed" and
// "constraint failed: UNIQUE" phrasings have shown up across driver
// versions, hence a substring check rather than an exact match.
func isUniqueConstraintErr(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToUpper(err.Error())
	return strings.Contains(msg, "UNIQUE CONSTRAINT") || strings.Contains(msg, "CONSTRAINT FAILED: UNIQUE")
}
