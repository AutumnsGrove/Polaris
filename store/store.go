// Package store persists threads and messages to SQLite so past
// sessions can be revisited, restarted, or continued with a follow-up
// question — and so per-thread cost can be shown in the UI.
package store

import (
	"database/sql"
	"fmt"
	"time"

	_ "github.com/mattn/go-sqlite3"
)

type Store struct {
	db *sql.DB
}

const schema = `
CREATE TABLE IF NOT EXISTS threads (
	id TEXT PRIMARY KEY,
	title TEXT NOT NULL DEFAULT '',
	model TEXT NOT NULL,
	cost_usd REAL NOT NULL DEFAULT 0,
	created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
	updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE IF NOT EXISTS messages (
	id INTEGER PRIMARY KEY AUTOINCREMENT,
	thread_id TEXT NOT NULL REFERENCES threads(id) ON DELETE CASCADE,
	role TEXT NOT NULL,
	content TEXT NOT NULL,
	citations TEXT NOT NULL DEFAULT '[]',
	cost_usd REAL NOT NULL DEFAULT 0,
	created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE INDEX IF NOT EXISTS idx_messages_thread ON messages(thread_id);
`

func Open(path string) (*Store, error) {
	db, err := sql.Open("sqlite3", path+"?_foreign_keys=on")
	if err != nil {
		return nil, fmt.Errorf("opening database: %w", err)
	}
	if _, err := db.Exec(schema); err != nil {
		return nil, fmt.Errorf("applying schema: %w", err)
	}
	return &Store{db: db}, nil
}

func (s *Store) Close() error { return s.db.Close() }

type Thread struct {
	ID        string    `json:"id"`
	Title     string    `json:"title"`
	Model     string    `json:"model"`
	CostUSD   float64   `json:"cost_usd"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

type Message struct {
	ID        int64     `json:"id"`
	ThreadID  string    `json:"thread_id"`
	Role      string    `json:"role"`
	Content   string    `json:"content"`
	Citations string    `json:"citations"` // JSON-encoded []tools.Citation
	CostUSD   float64   `json:"cost_usd"`
	CreatedAt time.Time `json:"created_at"`
}

// CreateThread inserts a new thread. title is typically derived from the
// first user message (truncated) and can be renamed later.
func (s *Store) CreateThread(id, title, model string) error {
	_, err := s.db.Exec(
		`INSERT INTO threads (id, title, model) VALUES (?, ?, ?)`,
		id, title, model,
	)
	return err
}

func (s *Store) GetThread(id string) (*Thread, error) {
	var t Thread
	err := s.db.QueryRow(
		`SELECT id, title, model, cost_usd, created_at, updated_at FROM threads WHERE id = ?`, id,
	).Scan(&t.ID, &t.Title, &t.Model, &t.CostUSD, &t.CreatedAt, &t.UpdatedAt)
	if err != nil {
		return nil, err
	}
	return &t, nil
}

// ListThreads returns threads newest-first, for the sidebar/history view.
func (s *Store) ListThreads(limit int) ([]Thread, error) {
	if limit <= 0 {
		limit = 100
	}
	rows, err := s.db.Query(
		`SELECT id, title, model, cost_usd, created_at, updated_at FROM threads ORDER BY updated_at DESC LIMIT ?`,
		limit,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var threads []Thread
	for rows.Next() {
		var t Thread
		if err := rows.Scan(&t.ID, &t.Title, &t.Model, &t.CostUSD, &t.CreatedAt, &t.UpdatedAt); err != nil {
			return nil, err
		}
		threads = append(threads, t)
	}
	return threads, rows.Err()
}

func (s *Store) DeleteThread(id string) error {
	_, err := s.db.Exec(`DELETE FROM threads WHERE id = ?`, id)
	return err
}

// AddCost bumps a thread's running cost without inserting a message row —
// for spend that isn't itself a stored turn, like a read-aloud TTS call
// against an existing assistant message.
func (s *Store) AddCost(threadID string, costUSD float64) error {
	_, err := s.db.Exec(
		`UPDATE threads SET cost_usd = cost_usd + ?, updated_at = CURRENT_TIMESTAMP WHERE id = ?`,
		costUSD, threadID,
	)
	return err
}

// AddMessage inserts a message and bumps the thread's running cost and
// updated_at in one transaction, so ListThreads' ordering and the
// header's cost display stay consistent. Returns the new message's ID,
// which the frontend needs later to retry/edit from this point.
func (s *Store) AddMessage(threadID, role, content, citationsJSON string, costUSD float64) (int64, error) {
	tx, err := s.db.Begin()
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()

	res, err := tx.Exec(
		`INSERT INTO messages (thread_id, role, content, citations, cost_usd) VALUES (?, ?, ?, ?, ?)`,
		threadID, role, content, citationsJSON, costUSD,
	)
	if err != nil {
		return 0, err
	}
	if _, err := tx.Exec(
		`UPDATE threads SET cost_usd = cost_usd + ?, updated_at = CURRENT_TIMESTAMP WHERE id = ?`,
		costUSD, threadID,
	); err != nil {
		return 0, err
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

// DeleteMessagesFrom removes every message in threadID with id >= fromID
// (the message being edited/retried, plus everything after it — there's
// no branching history, so anything downstream of an edit is invalidated)
// and recomputes the thread's running cost from what's left, since a
// simple subtraction would drift if this is called more than once.
func (s *Store) DeleteMessagesFrom(threadID string, fromID int64) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	if _, err := tx.Exec(
		`DELETE FROM messages WHERE thread_id = ? AND id >= ?`,
		threadID, fromID,
	); err != nil {
		return err
	}
	if _, err := tx.Exec(
		`UPDATE threads SET cost_usd = (
			SELECT COALESCE(SUM(cost_usd), 0) FROM messages WHERE thread_id = ?
		), updated_at = CURRENT_TIMESTAMP WHERE id = ?`,
		threadID, threadID,
	); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) GetMessages(threadID string) ([]Message, error) {
	rows, err := s.db.Query(
		`SELECT id, thread_id, role, content, citations, cost_usd, created_at FROM messages WHERE thread_id = ? ORDER BY id ASC`,
		threadID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var msgs []Message
	for rows.Next() {
		var m Message
		if err := rows.Scan(&m.ID, &m.ThreadID, &m.Role, &m.Content, &m.Citations, &m.CostUSD, &m.CreatedAt); err != nil {
			return nil, err
		}
		msgs = append(msgs, m)
	}
	return msgs, rows.Err()
}
