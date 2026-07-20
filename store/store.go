// Package store persists threads and messages to SQLite so past
// sessions can be revisited, restarted, or continued with a follow-up
// question — and so per-thread cost can be shown in the UI.
package store

import (
	"database/sql"
	"fmt"
	"strings"
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
	updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
	-- context_tokens: last known prompt+completion token count for this
	-- thread, per the LLM's own usage numbers — drives the context-usage %
	-- shown next to thread cost, and the auto-compaction threshold check.
	context_tokens INTEGER NOT NULL DEFAULT 0,
	-- compacted_summary/compacted_through_id: once a thread crosses the
	-- compaction threshold, everything up to compacted_through_id gets
	-- replaced by this summary when rebuilding history for the LLM — the
	-- messages table itself is never touched, so the visible transcript
	-- stays the true, complete record; only what's sent back to the model
	-- shrinks.
	compacted_summary TEXT NOT NULL DEFAULT '',
	compacted_through_id INTEGER NOT NULL DEFAULT 0
);

CREATE TABLE IF NOT EXISTS messages (
	id INTEGER PRIMARY KEY AUTOINCREMENT,
	thread_id TEXT NOT NULL REFERENCES threads(id) ON DELETE CASCADE,
	role TEXT NOT NULL,
	content TEXT NOT NULL,
	citations TEXT NOT NULL DEFAULT '[]',
	-- suggestions: up to 3 follow-up questions generated for this answer
	-- (assistant messages only, '[]' for user messages) — persisted so
	-- reopening a thread still shows them, not just the live turn that
	-- generated them.
	suggestions TEXT NOT NULL DEFAULT '[]',
	cost_usd REAL NOT NULL DEFAULT 0,
	created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE INDEX IF NOT EXISTS idx_messages_thread ON messages(thread_id);

-- User-adjustable UI preferences (theme, default model, price visibility).
-- Deliberately separate from config.yaml: those are operator-level
-- settings (API keys, the model catalog, ports) meant to be edited by
-- hand and version-controlled via .example files; these are day-to-day
-- toggles that should update instantly from the settings panel without
-- touching a file or restarting anything.
CREATE TABLE IF NOT EXISTS settings (
	key   TEXT PRIMARY KEY,
	value TEXT NOT NULL
);

-- events is the structured, queryable audit trail described in events.go:
-- every tool call/result, turn start/finish/failure, compaction, config
-- reload, and self-update, persisted here (not just to the log files) so
-- there's durable evidence of what happened even if the process crashed
-- mid-turn or the log directory was never checked.
CREATE TABLE IF NOT EXISTS events (
	id INTEGER PRIMARY KEY AUTOINCREMENT,
	-- thread_id is NULL for events with no single thread to attach to
	-- (startup, self-update, a config reload failure). NULL passes SQLite's
	-- foreign-key check regardless of the referenced table's contents.
	thread_id TEXT REFERENCES threads(id) ON DELETE CASCADE,
	level     TEXT NOT NULL, -- "info" | "warn" | "error"
	source    TEXT NOT NULL, -- e.g. "turn", "tool.web_search", "compaction", "update"
	message   TEXT NOT NULL,
	data      TEXT NOT NULL DEFAULT '{}', -- JSON-encoded structured detail (args, error, cost, etc.)
	created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE INDEX IF NOT EXISTS idx_events_thread ON events(thread_id);
CREATE INDEX IF NOT EXISTS idx_events_created ON events(created_at);
`

// migrations adds columns to a threads table created before they existed.
// CREATE TABLE IF NOT EXISTS above only helps brand-new databases — an
// existing polaris.db needs these added explicitly. Each is run
// independently and a "duplicate column" error is expected and ignored
// once a given database already has it; any other error is real.
var migrations = []string{
	`ALTER TABLE threads ADD COLUMN context_tokens INTEGER NOT NULL DEFAULT 0`,
	`ALTER TABLE threads ADD COLUMN compacted_summary TEXT NOT NULL DEFAULT ''`,
	`ALTER TABLE threads ADD COLUMN compacted_through_id INTEGER NOT NULL DEFAULT 0`,
	`ALTER TABLE messages ADD COLUMN suggestions TEXT NOT NULL DEFAULT '[]'`,
}

func Open(path string) (*Store, error) {
	db, err := sql.Open("sqlite3", path+"?_foreign_keys=on")
	if err != nil {
		return nil, fmt.Errorf("opening database: %w", err)
	}
	if _, err := db.Exec(schema); err != nil {
		return nil, fmt.Errorf("applying schema: %w", err)
	}
	for _, m := range migrations {
		if _, err := db.Exec(m); err != nil && !strings.Contains(err.Error(), "duplicate column") {
			return nil, fmt.Errorf("applying migration %q: %w", m, err)
		}
	}
	return &Store{db: db}, nil
}

func (s *Store) Close() error { return s.db.Close() }

type Thread struct {
	ID      string  `json:"id"`
	Title   string  `json:"title"`
	Model   string  `json:"model"`
	CostUSD float64 `json:"cost_usd"`
	// ContextTokens is exposed to the frontend for the context-usage %
	// display. CompactedSummary/CompactedThroughID are internal —
	// history-building only, never sent to the frontend.
	ContextTokens      int       `json:"context_tokens"`
	CompactedSummary   string    `json:"-"`
	CompactedThroughID int64     `json:"-"`
	CreatedAt          time.Time `json:"created_at"`
	UpdatedAt          time.Time `json:"updated_at"`
}

type Message struct {
	ID          int64     `json:"id"`
	ThreadID    string    `json:"thread_id"`
	Role        string    `json:"role"`
	Content     string    `json:"content"`
	Citations   string    `json:"citations"`   // JSON-encoded []tools.Citation
	Suggestions string    `json:"suggestions"` // JSON-encoded []string
	CostUSD     float64   `json:"cost_usd"`
	CreatedAt   time.Time `json:"created_at"`
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
		`SELECT id, title, model, cost_usd, context_tokens, compacted_summary, compacted_through_id, created_at, updated_at
		 FROM threads WHERE id = ?`, id,
	).Scan(&t.ID, &t.Title, &t.Model, &t.CostUSD, &t.ContextTokens, &t.CompactedSummary, &t.CompactedThroughID, &t.CreatedAt, &t.UpdatedAt)
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
		`SELECT id, title, model, cost_usd, context_tokens, created_at, updated_at
		 FROM threads ORDER BY updated_at DESC LIMIT ?`,
		limit,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var threads []Thread
	for rows.Next() {
		var t Thread
		if err := rows.Scan(&t.ID, &t.Title, &t.Model, &t.CostUSD, &t.ContextTokens, &t.CreatedAt, &t.UpdatedAt); err != nil {
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
func (s *Store) AddMessage(threadID, role, content, citationsJSON, suggestionsJSON string, costUSD float64) (int64, error) {
	tx, err := s.db.Begin()
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()

	res, err := tx.Exec(
		`INSERT INTO messages (thread_id, role, content, citations, suggestions, cost_usd) VALUES (?, ?, ?, ?, ?, ?)`,
		threadID, role, content, citationsJSON, suggestionsJSON, costUSD,
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

// SetContextTokens records the thread's current context size (prompt +
// completion tokens from the LLM's own usage numbers) — drives the
// context-usage % in the UI and the auto-compaction check.
func (s *Store) SetContextTokens(threadID string, tokens int) error {
	_, err := s.db.Exec(`UPDATE threads SET context_tokens = ? WHERE id = ?`, tokens, threadID)
	return err
}

// CompactThread records a fresh summary of everything up to throughID —
// history built for the LLM from here on substitutes this summary for
// every message at or below throughID, instead of the full raw text.
// Deliberately does NOT touch the messages table: the visible transcript
// stays the complete, true record, only what's sent back to the model
// shrinks. cost is the summarization call's own cost, added to the
// thread's running total like any other LLM call.
func (s *Store) CompactThread(threadID, summary string, throughID int64, cost float64, contextTokensEstimate int) error {
	_, err := s.db.Exec(
		`UPDATE threads SET compacted_summary = ?, compacted_through_id = ?, cost_usd = cost_usd + ?,
		 context_tokens = ?, updated_at = CURRENT_TIMESTAMP WHERE id = ?`,
		summary, throughID, cost, contextTokensEstimate, threadID,
	)
	return err
}

// GetSetting returns the stored value for key, or "" if unset — callers
// fall back to a config.yaml/hardcoded default in that case.
func (s *Store) GetSetting(key string) (string, error) {
	var value string
	err := s.db.QueryRow(`SELECT value FROM settings WHERE key = ?`, key).Scan(&value)
	if err == sql.ErrNoRows {
		return "", nil
	}
	return value, err
}

// AllSettings returns every stored key/value pair, for the settings panel
// to populate in one request instead of one round-trip per field.
func (s *Store) AllSettings() (map[string]string, error) {
	rows, err := s.db.Query(`SELECT key, value FROM settings`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := make(map[string]string)
	for rows.Next() {
		var k, v string
		if err := rows.Scan(&k, &v); err != nil {
			return nil, err
		}
		out[k] = v
	}
	return out, rows.Err()
}

// SetSetting upserts a single key/value pair.
func (s *Store) SetSetting(key, value string) error {
	_, err := s.db.Exec(
		`INSERT INTO settings (key, value) VALUES (?, ?) ON CONFLICT(key) DO UPDATE SET value = excluded.value`,
		key, value,
	)
	return err
}

func (s *Store) GetMessages(threadID string) ([]Message, error) {
	rows, err := s.db.Query(
		`SELECT id, thread_id, role, content, citations, suggestions, cost_usd, created_at FROM messages WHERE thread_id = ? ORDER BY id ASC`,
		threadID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var msgs []Message
	for rows.Next() {
		var m Message
		if err := rows.Scan(&m.ID, &m.ThreadID, &m.Role, &m.Content, &m.Citations, &m.Suggestions, &m.CostUSD, &m.CreatedAt); err != nil {
			return nil, err
		}
		msgs = append(msgs, m)
	}
	return msgs, rows.Err()
}
