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

// CreateMemory inserts a brand-new memory, failing with ErrMemoryExists
// if name is already taken rather than overwriting it — see the memory
// tool's "write" action, which is deliberately create-only.
func (s *Store) CreateMemory(name, memType, description, content string) error {
	_, err := s.db.Exec(
		`INSERT INTO memories (name, type, description, content) VALUES (?, ?, ?, ?)`,
		name, memType, description, content,
	)
	if err != nil {
		if isUniqueConstraintErr(err) {
			return ErrMemoryExists
		}
		return fmt.Errorf("create memory: %w", err)
	}
	return nil
}

// UpdateMemory overwrites an existing memory's type/description/content in
// place — name is the stable identity and never changes. Returns
// ErrMemoryNotFound if name doesn't exist, so the memory tool's "edit"
// action never silently no-ops.
func (s *Store) UpdateMemory(name, memType, description, content string) error {
	res, err := s.db.Exec(
		`UPDATE memories SET type = ?, description = ?, content = ?, updated_at = CURRENT_TIMESTAMP WHERE name = ?`,
		memType, description, content, name,
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

// GetMemory returns one memory's full content, or ErrMemoryNotFound.
func (s *Store) GetMemory(name string) (*Memory, error) {
	var m Memory
	err := s.db.QueryRow(
		`SELECT name, type, description, content, created_at, updated_at FROM memories WHERE name = ?`,
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
	rows, err := s.db.Query(`SELECT name, type, description FROM memories ORDER BY type, name`)
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

// DeleteMemory removes a memory outright — the memory tool's "forget"
// action, for a memory the model has confirmed is wrong, stale, or the
// user explicitly asked to have forgotten. Returns ErrMemoryNotFound if
// name doesn't exist.
func (s *Store) DeleteMemory(name string) error {
	res, err := s.db.Exec(`DELETE FROM memories WHERE name = ?`, name)
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
