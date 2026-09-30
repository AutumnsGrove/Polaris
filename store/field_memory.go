// field_memory.go is a Field's own memory store (issue #133) — the same
// create/edit/view/forget shape as memory.go, keyed by (field_id, name)
// instead of name alone. Kept as parallel methods rather than threading an
// optional scope through the global ones: every global call site (the
// settings panel, import/export, the benchmark harness) stays exactly as
// it was.
package store

import (
	"database/sql"
	"errors"
	"fmt"
)

// ErrMemoryNameInGlobal is returned (by the gateway's merged "both"-mode
// write closure, not the store itself) when a field memory's name is
// already a live global memory. Rejecting it up front means the two stores
// never hold a same-named pair that one bare view would silently shadow.
var ErrMemoryNameInGlobal = errors.New("memory name already exists in global memory")

// ErrGlobalMemoryReadOnly is returned when a field turn tries to edit or
// forget a name that only exists in global memory — field turns read global
// memory in "both" mode but never write it.
var ErrGlobalMemoryReadOnly = errors.New("global memory is read-only from a field")

// CreateFieldMemory mirrors CreateMemory: a name matching a forgotten
// (disabled) row revives it; a name matching a live row is ErrMemoryExists.
// It deliberately doesn't know about the global store — rejecting a name
// that already exists globally is the gateway's job, since only it knows
// whether the turn can see global memory at all.
func (s *Store) CreateFieldMemory(fieldID, name, memType, description, content, occurredAt string) error {
	var disabled int
	err := s.db.QueryRow(`SELECT disabled FROM field_memories WHERE field_id = ? AND name = ?`, fieldID, name).Scan(&disabled)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		if _, err := s.db.Exec(
			`INSERT INTO field_memories (field_id, name, type, description, content, occurred_at) VALUES (?, ?, ?, ?, ?, ?)`,
			fieldID, name, memType, description, content, occurredAt,
		); err != nil {
			if isUniqueConstraintErr(err) {
				return ErrMemoryExists
			}
			return fmt.Errorf("create field memory: %w", err)
		}
		return nil
	case err != nil:
		return fmt.Errorf("create field memory: %w", err)
	case disabled == 0:
		return ErrMemoryExists
	default:
		if _, err := s.db.Exec(
			`UPDATE field_memories SET type = ?, description = ?, content = ?, occurred_at = ?, disabled = 0, updated_at = CURRENT_TIMESTAMP WHERE field_id = ? AND name = ?`,
			memType, description, content, occurredAt, fieldID, name,
		); err != nil {
			return fmt.Errorf("create field memory (reviving forgotten name): %w", err)
		}
		return nil
	}
}

// UpdateFieldMemory mirrors UpdateMemory, including its single-statement
// CASE-WHEN update (concurrent edits in one tool batch must not clobber
// each other) and "empty means unchanged" convention.
func (s *Store) UpdateFieldMemory(fieldID, name, memType, description, content, occurredAt string) error {
	res, err := s.db.Exec(
		`UPDATE field_memories SET
			type = CASE WHEN ? = '' THEN type ELSE ? END,
			description = CASE WHEN ? = '' THEN description ELSE ? END,
			content = CASE WHEN ? = '' THEN content ELSE ? END,
			occurred_at = CASE WHEN ? = '' THEN occurred_at ELSE ? END,
			updated_at = CURRENT_TIMESTAMP
		WHERE field_id = ? AND name = ? AND disabled = 0`,
		memType, memType, description, description, content, content, occurredAt, occurredAt, fieldID, name,
	)
	if err != nil {
		return fmt.Errorf("update field memory: %w", err)
	}
	return rowsAffectedOrNotFound(res, ErrMemoryNotFound)
}

// GetFieldMemory returns one live field memory, or ErrMemoryNotFound.
func (s *Store) GetFieldMemory(fieldID, name string) (*Memory, error) {
	var m Memory
	err := s.db.QueryRow(
		`SELECT name, type, description, content, occurred_at, created_at, updated_at FROM field_memories WHERE field_id = ? AND name = ? AND disabled = 0`,
		fieldID, name,
	).Scan(&m.Name, &m.Type, &m.Description, &m.Content, &m.OccurredAt, &m.CreatedAt, &m.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrMemoryNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("get field memory: %w", err)
	}
	return &m, nil
}

// ListFieldMemories is the index form (no content), ordered like
// ListMemories so related entries group together in the prompt block.
func (s *Store) ListFieldMemories(fieldID string) ([]MemoryIndexEntry, error) {
	rows, err := s.db.Query(`SELECT name, type, description, occurred_at FROM field_memories WHERE field_id = ? AND disabled = 0 ORDER BY type, name`, fieldID)
	if err != nil {
		return nil, fmt.Errorf("list field memories: %w", err)
	}
	defer rows.Close()

	entries := []MemoryIndexEntry{}
	for rows.Next() {
		var e MemoryIndexEntry
		if err := rows.Scan(&e.Name, &e.Type, &e.Description, &e.OccurredAt); err != nil {
			return nil, fmt.Errorf("list field memories: %w", err)
		}
		entries = append(entries, e)
	}
	return entries, rows.Err()
}

// ListFieldMemoriesFull is the full-row form, for a field's settings page.
func (s *Store) ListFieldMemoriesFull(fieldID string) ([]Memory, error) {
	rows, err := s.db.Query(`SELECT name, type, description, content, occurred_at, created_at, updated_at FROM field_memories WHERE field_id = ? AND disabled = 0 ORDER BY type, name`, fieldID)
	if err != nil {
		return nil, fmt.Errorf("list field memories: %w", err)
	}
	defer rows.Close()

	memories := []Memory{}
	for rows.Next() {
		var m Memory
		if err := rows.Scan(&m.Name, &m.Type, &m.Description, &m.Content, &m.OccurredAt, &m.CreatedAt, &m.UpdatedAt); err != nil {
			return nil, fmt.Errorf("list field memories: %w", err)
		}
		memories = append(memories, m)
	}
	return memories, rows.Err()
}

// DeleteFieldMemory soft-deletes (forgets) one field memory, same as
// DeleteMemory; CreateFieldMemory's revival path frees the name again.
func (s *Store) DeleteFieldMemory(fieldID, name string) error {
	res, err := s.db.Exec(`UPDATE field_memories SET disabled = 1, updated_at = CURRENT_TIMESTAMP WHERE field_id = ? AND name = ? AND disabled = 0`, fieldID, name)
	if err != nil {
		return fmt.Errorf("delete field memory: %w", err)
	}
	return rowsAffectedOrNotFound(res, ErrMemoryNotFound)
}
