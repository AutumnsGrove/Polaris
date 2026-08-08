// Package store persists threads and messages to SQLite so past
// sessions can be revisited, restarted, or continued with a follow-up
// question — and so per-thread cost can be shown in the UI.
package store

import (
	"database/sql"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	_ "modernc.org/sqlite"
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
	compacted_through_id INTEGER NOT NULL DEFAULT 0,
	-- source: who started this thread — "web" for the normal chat UI,
	-- or a caller-supplied label (e.g. "her-go") for threads created via
	-- POST /api/ask. Purely informational: never changes how a thread
	-- behaves, just lets future tooling/queries tell them apart.
	source TEXT NOT NULL DEFAULT 'web',
	-- disabled: soft-delete flag. "Deleting" a thread from the UI just
	-- sets this rather than issuing a real DELETE — the row (and its
	-- messages/events) stay in the database as a durable record, they're
	-- just excluded from ListThreads/GetThread so a disabled thread is
	-- indistinguishable from a genuinely absent one to every API caller.
	disabled INTEGER NOT NULL DEFAULT 0,
	-- favorite: user-pinned via the thread menu's Favorite toggle — drives
	-- the sidebar's pinned Favorites section (see ListThreads). Purely a
	-- display flag, unrelated to disabled/soft-delete.
	favorite INTEGER NOT NULL DEFAULT 0,
	-- fork_root_id/fork_at_index/active_variant_id implement message
	-- variants (editing or regenerating a reply no longer destroys the
	-- old one — see ForkThread's doc comment for the full model). A
	-- thread with fork_root_id set is a hidden variant, never surfaced by
	-- ListThreads/GetThread directly: it only exists to be pointed at by
	-- its root's active_variant_id or listed by VariantsAt.
	fork_root_id TEXT NOT NULL DEFAULT '',
	-- fork_at_index: the 0-based position in the message list where this
	-- variant's content starts differing from its siblings — the anchor
	-- VariantsAt groups by to find every alternative at the same spot.
	fork_at_index INTEGER NOT NULL DEFAULT 0,
	-- active_variant_id: only meaningful on a root thread (fork_root_id
	-- ''). Empty means the root's own messages are what's shown; otherwise
	-- it's the id of whichever variant (a thread ForkThread created) is
	-- currently the effective content — see EffectiveThreadID.
	active_variant_id TEXT NOT NULL DEFAULT ''
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
	-- turn_id: shared by the user message and assistant message that make
	-- up one turn, and by every event (see events.turn_id below) logged
	-- while that turn ran — the join key that lets a reopened thread
	-- reconstruct which tool calls/thinking steps belong to which answer.
	turn_id TEXT NOT NULL DEFAULT '',
	-- duration_ms: wall-clock time agent.Run took to produce this answer
	-- (assistant messages only, 0 for user messages) — set via
	-- SetMessageDuration once the ID exists, not at insert time, same
	-- reason context_tokens is a separate post-hoc UPDATE: the timer
	-- can't stop until agent.Run has already returned the finished answer.
	duration_ms INTEGER NOT NULL DEFAULT 0,
	-- attachment_filename/attachment_content_type: set only on a user
	-- message that carried an upload from the composer's "+" menu — the
	-- original filename and its detected content type, for display
	-- ("📎 report.pdf") when the thread is reopened. The actual file
	-- lives on disk under config.Attachments.Dir, named by an opaque ID
	-- from the upload response, not by this filename — this column is
	-- purely cosmetic. '' on every other message.
	attachment_filename TEXT NOT NULL DEFAULT '',
	attachment_content_type TEXT NOT NULL DEFAULT '',
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
	-- turn_id: "" for events with no single turn to attach to (startup,
	-- self-update, thread rename, voice TTS/STT) — see messages.turn_id
	-- above for the shared join key within one turn.
	turn_id   TEXT NOT NULL DEFAULT '',
	created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE INDEX IF NOT EXISTS idx_events_thread ON events(thread_id);
CREATE INDEX IF NOT EXISTS idx_events_created ON events(created_at);
`

// migrations adds columns to a threads table created before they existed.
// CREATE TABLE IF NOT EXISTS above only helps brand-new databases — an
// existing polaris.db needs these added explicitly. Applied in order,
// tracked via PRAGMA user_version (see applyMigrations) rather than by
// probing each one's error — append new entries here; never edit or
// reorder existing ones, since a database's recorded version is just an
// index into this slice.
var migrations = []string{
	`ALTER TABLE threads ADD COLUMN context_tokens INTEGER NOT NULL DEFAULT 0`,
	`ALTER TABLE threads ADD COLUMN compacted_summary TEXT NOT NULL DEFAULT ''`,
	`ALTER TABLE threads ADD COLUMN compacted_through_id INTEGER NOT NULL DEFAULT 0`,
	`ALTER TABLE messages ADD COLUMN suggestions TEXT NOT NULL DEFAULT '[]'`,
	`ALTER TABLE threads ADD COLUMN source TEXT NOT NULL DEFAULT 'web'`,
	`ALTER TABLE messages ADD COLUMN turn_id TEXT NOT NULL DEFAULT ''`,
	`ALTER TABLE events ADD COLUMN turn_id TEXT NOT NULL DEFAULT ''`,
	`ALTER TABLE messages ADD COLUMN duration_ms INTEGER NOT NULL DEFAULT 0`,
	`ALTER TABLE messages ADD COLUMN attachment_filename TEXT NOT NULL DEFAULT ''`,
	`ALTER TABLE messages ADD COLUMN attachment_content_type TEXT NOT NULL DEFAULT ''`,
	`ALTER TABLE threads ADD COLUMN disabled INTEGER NOT NULL DEFAULT 0`,
	`ALTER TABLE threads ADD COLUMN favorite INTEGER NOT NULL DEFAULT 0`,
	`ALTER TABLE threads ADD COLUMN fork_root_id TEXT NOT NULL DEFAULT ''`,
	`ALTER TABLE threads ADD COLUMN fork_at_index INTEGER NOT NULL DEFAULT 0`,
	`ALTER TABLE threads ADD COLUMN active_variant_id TEXT NOT NULL DEFAULT ''`,
}

func Open(path string) (*Store, error) {
	// _busy_timeout: SQLite allows only one writer at a time; without this,
	// a second concurrent writer (routine now, since every turn does
	// several writes — the message, context tokens, and multiple event-log
	// inserts — across goroutines) gets an immediate SQLITE_BUSY error
	// instead of waiting its turn. _journal_mode=WAL lets readers proceed
	// without blocking on a writer at all, which is what actually makes
	// the busy_timeout the common case rather than the exception.
	db, err := sql.Open("sqlite", path+"?_foreign_keys=on&_journal_mode=WAL&_busy_timeout=5000")
	if err != nil {
		return nil, fmt.Errorf("opening database: %w", err)
	}
	if _, err := db.Exec(schema); err != nil {
		return nil, fmt.Errorf("applying schema: %w", err)
	}
	if err := applyMigrations(db); err != nil {
		return nil, err
	}
	return &Store{db: db}, nil
}

// applyMigrations runs whichever entries in `migrations` haven't been
// applied yet, tracked via SQLite's built-in PRAGMA user_version — no
// extra table needed, it's exactly the "how far have we gotten" counter
// this needs. Previously "already applied" was detected only by
// string-matching each ALTER TABLE's error against "duplicate column",
// which broke silently if that exact wording ever changed across a
// go-sqlite3/SQLite version bump, and couldn't detect completion for any
// migration shape other than ADD COLUMN.
//
// A fresh user_version of 0 covers two cases identically, and both are
// handled correctly by just running the full list: a brand-new database
// (the `schema` constant above already creates every column any migration
// would add, so each one below is a harmless no-op there) and a
// pre-versioning database from before this counter existed (every
// migration actually needs to run there). The "duplicate column" check is
// kept as a tolerance for exactly that transitional case — going forward,
// user_version itself is what prevents a migration from ever being
// re-attempted, not error-message sniffing.
//
// Each migration's success (real or tolerated-duplicate) is recorded
// immediately, one at a time — so a real failure partway through the list
// leaves user_version accurately at the last one that actually landed,
// and the next Open() resumes from exactly there instead of re-running
// (and re-risking) everything from the start.
func applyMigrations(db *sql.DB) error {
	var version int
	if err := db.QueryRow(`PRAGMA user_version`).Scan(&version); err != nil {
		return fmt.Errorf("reading schema version: %w", err)
	}
	for i := version; i < len(migrations); i++ {
		if _, err := db.Exec(migrations[i]); err != nil && !strings.Contains(err.Error(), "duplicate column") {
			return fmt.Errorf("applying migration %d %q: %w", i, migrations[i], err)
		}
		if _, err := db.Exec(fmt.Sprintf(`PRAGMA user_version = %d`, i+1)); err != nil {
			return fmt.Errorf("recording schema version %d: %w", i+1, err)
		}
	}
	return nil
}

func (s *Store) Close() error { return s.db.Close() }

// Ping verifies the database connection is alive, for /healthz — a
// dropped connection or a locked/corrupt file surfaces here rather than
// only on the next real request.
func (s *Store) Ping() error { return s.db.Ping() }

type Thread struct {
	ID      string  `json:"id"`
	Title   string  `json:"title"`
	Model   string  `json:"model"`
	CostUSD float64 `json:"cost_usd"`
	// ContextTokens is exposed to the frontend for the context-usage %
	// display. CompactedSummary/CompactedThroughID are internal —
	// history-building only, never sent to the frontend.
	ContextTokens      int    `json:"context_tokens"`
	CompactedSummary   string `json:"-"`
	CompactedThroughID int64  `json:"-"`
	// Source is informational only (see schema comment in Open) — "web"
	// for the normal chat UI, or a caller-supplied label for threads
	// created via POST /api/ask.
	Source string `json:"source"`
	// Favorite drives the sidebar's pinned Favorites section — see the
	// schema comment in Open.
	Favorite  bool      `json:"favorite"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

type Message struct {
	ID          int64   `json:"id"`
	ThreadID    string  `json:"thread_id"`
	Role        string  `json:"role"`
	Content     string  `json:"content"`
	Citations   string  `json:"citations"`   // JSON-encoded []tools.Citation
	Suggestions string  `json:"suggestions"` // JSON-encoded []string
	CostUSD     float64 `json:"cost_usd"`
	// TurnID joins this message to the events (see store.Event.TurnID)
	// logged while the turn that produced it ran, so a reopened thread
	// can reconstruct that turn's tool calls/thinking steps.
	TurnID string `json:"turn_id"`
	// DurationMs is how long agent.Run took to produce this answer —
	// 0 for user messages, and for assistant messages until
	// SetMessageDuration runs (see its doc comment for why that's a
	// separate post-hoc update rather than part of AddMessage itself).
	DurationMs int64 `json:"duration_ms"`
	// AttachmentFilename/AttachmentContentType are set only on a user
	// message that carried an upload — see SetMessageAttachment.
	AttachmentFilename    string    `json:"attachment_filename,omitempty"`
	AttachmentContentType string    `json:"attachment_content_type,omitempty"`
	CreatedAt             time.Time `json:"created_at"`
}

// CreateThread inserts a new thread. title is typically derived from the
// first user message (truncated) and can be renamed later. source tags
// where the thread came from (see schema comment above) — pass "web" for
// the normal chat UI.
func (s *Store) CreateThread(id, title, model, source string) error {
	_, err := s.db.Exec(
		`INSERT INTO threads (id, title, model, source) VALUES (?, ?, ?, ?)`,
		id, title, model, source,
	)
	return err
}

// SetThreadTitle updates a thread's title — used both for the one-time
// LLM-generated title after a new thread's first turn finishes, and for
// a user-initiated rename from the sidebar. Either one replaces
// whatever title was there before; there's no separate "locked" flag,
// since a rename happening at all is itself the signal that the title
// is no longer just the auto-generated placeholder.
func (s *Store) SetThreadTitle(id, title string) error {
	_, err := s.db.Exec(
		`UPDATE threads SET title = ?, updated_at = strftime('%Y-%m-%d %H:%M:%f', 'now') WHERE id = ?`,
		title, id,
	)
	return err
}

// SetThreadFavorite pins/unpins a thread to the sidebar's Favorites
// section. Deliberately does not touch updated_at — favoriting isn't
// "activity" on a thread the way a rename or a new message is, and
// shouldn't reorder it within its section.
func (s *Store) SetThreadFavorite(id string, favorite bool) error {
	_, err := s.db.Exec(`UPDATE threads SET favorite = ? WHERE id = ?`, favorite, id)
	return err
}

// EffectiveThreadID resolves which thread's messages are actually shown
// for rootID right now — rootID's own, unless SetActiveVariant last
// pointed it at a different variant (a thread ForkThread previously
// created). Every read (GetMessages, GetThreadEvents, loadHistory) and
// every new message (a plain send, or the shared prefix an edit/retry
// branches from) goes through this first, so continuing a conversation
// after browsing to an older variant just keeps building on that variant
// — no special-casing needed anywhere else.
func (s *Store) EffectiveThreadID(rootID string) (string, error) {
	var active string
	if err := s.db.QueryRow(`SELECT active_variant_id FROM threads WHERE id = ?`, rootID).Scan(&active); err != nil {
		return "", err
	}
	if active == "" {
		return rootID, nil
	}
	return active, nil
}

// ForkThread is what makes editing/regenerating non-destructive: instead
// of deleting whatever's being replaced (the old DeleteMessagesFromAndAdd
// Message behavior), the turn about to overwrite srcID's content first
// gets a permanent home of its own. This creates that new hidden thread —
// fork_root_id=rootID, fork_at_index=atIndex — and copies srcID's first
// atIndex messages into it (the shared prefix both branches have in
// common). srcID's own messages are never touched here; the caller is
// expected to write the new content into the returned thread, and
// srcID's row stays exactly as reachable afterward (via VariantsAt) as
// it was before this ran.
//
// atIndex is a position (0-based index into the message list ordered by
// id), not a message id — it's what lets VariantsAt group multiple
// threads as "alternatives at the same spot" even though each fork's own
// copied messages get entirely new autoincrement ids.
func (s *Store) ForkThread(rootID, srcID string, atIndex int) (string, error) {
	forkID := uuid.NewString()

	tx, err := s.db.Begin()
	if err != nil {
		return "", err
	}
	defer tx.Rollback()

	var model, source string
	if err := tx.QueryRow(`SELECT model, source FROM threads WHERE id = ?`, srcID).Scan(&model, &source); err != nil {
		return "", err
	}

	if _, err := tx.Exec(
		`INSERT INTO threads (id, title, model, source, fork_root_id, fork_at_index) VALUES (?, '', ?, ?, ?, ?)`,
		forkID, model, source, rootID, atIndex,
	); err != nil {
		return "", err
	}

	if _, err := tx.Exec(
		`INSERT INTO messages (thread_id, role, content, citations, suggestions, cost_usd, turn_id, duration_ms, attachment_filename, attachment_content_type, created_at)
		 SELECT ?, role, content, citations, suggestions, cost_usd, turn_id, duration_ms, attachment_filename, attachment_content_type, created_at
		 FROM messages WHERE thread_id = ? ORDER BY id ASC LIMIT ?`,
		forkID, srcID, atIndex,
	); err != nil {
		return "", err
	}

	if err := tx.Commit(); err != nil {
		return "", err
	}
	return forkID, nil
}

// SetActiveVariant points rootID at a different variant of its own
// conversation. targetID is either rootID itself (show its own original
// content) or a thread ForkThread previously returned. Swapping is O(1)
// regardless of conversation length — it never moves or copies data,
// just repoints which existing thread EffectiveThreadID resolves to.
func (s *Store) SetActiveVariant(rootID, targetID string) error {
	active := targetID
	if targetID == rootID {
		active = ""
	}
	_, err := s.db.Exec(`UPDATE threads SET active_variant_id = ? WHERE id = ?`, active, rootID)
	return err
}

// VariantsAt lists every variant available at message index atIndex for
// rootID's conversation, oldest-created first: rootID's own original
// content (if its own history reaches that far — it's the implicit
// "slot 0" that predates any forking) followed by every fork branching at
// that exact index. A single-element result means nothing's actually
// been edited/regenerated at this position, so the caller shouldn't show
// a switcher for it at all.
func (s *Store) VariantsAt(rootID string, atIndex int) ([]string, error) {
	var count int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM messages WHERE thread_id = ?`, rootID).Scan(&count); err != nil {
		return nil, err
	}

	var ids []string
	if count > atIndex {
		ids = append(ids, rootID)
	}

	rows, err := s.db.Query(
		`SELECT id FROM threads WHERE fork_root_id = ? AND fork_at_index = ? ORDER BY created_at ASC`,
		rootID, atIndex,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

// VariantIndices returns every position rootID has ever been
// edited/regenerated at, so the caller can build the full variants map
// for a GetThread response with one query per position instead of
// probing every possible index.
func (s *Store) VariantIndices(rootID string) ([]int, error) {
	rows, err := s.db.Query(
		`SELECT DISTINCT fork_at_index FROM threads WHERE fork_root_id = ? ORDER BY fork_at_index`,
		rootID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var indices []int
	for rows.Next() {
		var i int
		if err := rows.Scan(&i); err != nil {
			return nil, err
		}
		indices = append(indices, i)
	}
	return indices, rows.Err()
}

// GetThread looks up a thread by id. A disabled (soft-deleted) thread or a
// hidden variant (fork_root_id set — see ForkThread) is excluded — same
// sql.ErrNoRows a caller gets for an id that never existed at all, so a
// stale tab/bookmark pointed at a deleted thread (or a variant id, which
// was never meant to be addressable on its own) fails the same way as a
// bad id. Internal callers that need to read a variant thread directly
// use GetThreadRaw instead.
func (s *Store) GetThread(id string) (*Thread, error) {
	var t Thread
	err := s.db.QueryRow(
		`SELECT id, title, model, cost_usd, context_tokens, compacted_summary, compacted_through_id, source, favorite, created_at, updated_at
		 FROM threads WHERE id = ? AND disabled = 0 AND fork_root_id = ''`, id,
	).Scan(&t.ID, &t.Title, &t.Model, &t.CostUSD, &t.ContextTokens, &t.CompactedSummary, &t.CompactedThroughID, &t.Source, &t.Favorite, &t.CreatedAt, &t.UpdatedAt)
	if err != nil {
		return nil, err
	}
	return &t, nil
}

// GetThreadRaw looks up any thread by id, including a hidden variant or a
// disabled one — for internal use (loadHistory, ForkThread) where the id
// in hand is known to be legitimate (resolved via EffectiveThreadID, not
// taken from an untrusted request), not the public GetThread's job of
// rejecting ids that shouldn't be individually addressable.
func (s *Store) GetThreadRaw(id string) (*Thread, error) {
	var t Thread
	err := s.db.QueryRow(
		`SELECT id, title, model, cost_usd, context_tokens, compacted_summary, compacted_through_id, source, favorite, created_at, updated_at
		 FROM threads WHERE id = ?`, id,
	).Scan(&t.ID, &t.Title, &t.Model, &t.CostUSD, &t.ContextTokens, &t.CompactedSummary, &t.CompactedThroughID, &t.Source, &t.Favorite, &t.CreatedAt, &t.UpdatedAt)
	if err != nil {
		return nil, err
	}
	return &t, nil
}

// ListThreads returns non-disabled, non-variant threads newest-first, for
// the sidebar/history view. Favorite/non-favorite are interleaved here in
// one recency order — the frontend splits them into the pinned Favorites
// section and the rest, each keeping this same relative ordering.
func (s *Store) ListThreads(limit int) ([]Thread, error) {
	if limit <= 0 {
		limit = 100
	}
	rows, err := s.db.Query(
		`SELECT id, title, model, cost_usd, context_tokens, source, favorite, created_at, updated_at
		 FROM threads WHERE disabled = 0 AND fork_root_id = '' ORDER BY updated_at DESC LIMIT ?`,
		limit,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var threads []Thread
	for rows.Next() {
		var t Thread
		if err := rows.Scan(&t.ID, &t.Title, &t.Model, &t.CostUSD, &t.ContextTokens, &t.Source, &t.Favorite, &t.CreatedAt, &t.UpdatedAt); err != nil {
			return nil, err
		}
		threads = append(threads, t)
	}
	return threads, rows.Err()
}

// DeleteThread soft-deletes a thread: it flips disabled rather than
// issuing a real DELETE, so the row and every message/event still
// referencing it survive as a durable record — ListThreads/GetThread
// simply stop returning it, which is indistinguishable from a real
// deletion to every existing API caller.
func (s *Store) DeleteThread(id string) error {
	_, err := s.db.Exec(
		`UPDATE threads SET disabled = 1, updated_at = strftime('%Y-%m-%d %H:%M:%f', 'now') WHERE id = ?`,
		id,
	)
	return err
}

// AddCost bumps a thread's running cost without inserting a message row —
// for spend that isn't itself a stored turn, like a read-aloud TTS call
// against an existing assistant message.
func (s *Store) AddCost(threadID string, costUSD float64) error {
	_, err := s.db.Exec(
		`UPDATE threads SET cost_usd = cost_usd + ?, updated_at = strftime('%Y-%m-%d %H:%M:%f', 'now') WHERE id = ?`,
		costUSD, threadID,
	)
	return err
}

// AddMessage inserts a message and bumps the thread's running cost and
// updated_at in one transaction, so ListThreads' ordering and the
// header's cost display stay consistent. Returns the new message's ID,
// which the frontend needs later to retry/edit from this point.
func (s *Store) AddMessage(threadID, role, content, citationsJSON, suggestionsJSON string, costUSD float64, turnID string) (int64, error) {
	tx, err := s.db.Begin()
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()

	res, err := tx.Exec(
		`INSERT INTO messages (thread_id, role, content, citations, suggestions, cost_usd, turn_id) VALUES (?, ?, ?, ?, ?, ?, ?)`,
		threadID, role, content, citationsJSON, suggestionsJSON, costUSD, turnID,
	)
	if err != nil {
		return 0, err
	}
	if _, err := tx.Exec(
		`UPDATE threads SET cost_usd = cost_usd + ?, updated_at = strftime('%Y-%m-%d %H:%M:%f', 'now') WHERE id = ?`,
		costUSD, threadID,
	); err != nil {
		return 0, err
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

// IsFirstMessage reports whether id is the earliest message in threadID —
// used by handleTurn to tell an edit/retry of the thread's opening
// question (which should regenerate the title, since the question the
// old title was based on no longer exists) apart from an edit/retry
// further into the conversation (which shouldn't: the title already
// describes an established thread, not just this one turn).
func (s *Store) IsFirstMessage(threadID string, id int64) (bool, error) {
	var minID int64
	err := s.db.QueryRow(`SELECT MIN(id) FROM messages WHERE thread_id = ?`, threadID).Scan(&minID)
	if err != nil {
		return false, err
	}
	return minID == id, nil
}

// MessageIndex returns the 0-based position of message id within
// threadID's own message list — the atIndex ForkThread needs to know how
// much of the shared prefix to copy for an edit/retry landing on this
// message.
func (s *Store) MessageIndex(threadID string, id int64) (int, error) {
	var index int
	err := s.db.QueryRow(`SELECT COUNT(*) FROM messages WHERE thread_id = ? AND id < ?`, threadID, id).Scan(&index)
	return index, err
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
		 context_tokens = ?, updated_at = strftime('%Y-%m-%d %H:%M:%f', 'now') WHERE id = ?`,
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
		`SELECT id, thread_id, role, content, citations, suggestions, cost_usd, turn_id, duration_ms,
			attachment_filename, attachment_content_type, created_at
		FROM messages WHERE thread_id = ? ORDER BY id ASC`,
		threadID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var msgs []Message
	for rows.Next() {
		var m Message
		if err := rows.Scan(&m.ID, &m.ThreadID, &m.Role, &m.Content, &m.Citations, &m.Suggestions, &m.CostUSD, &m.TurnID, &m.DurationMs,
			&m.AttachmentFilename, &m.AttachmentContentType, &m.CreatedAt); err != nil {
			return nil, err
		}
		msgs = append(msgs, m)
	}
	return msgs, rows.Err()
}

// SetMessageDuration records how long agent.Run took to produce a given
// assistant message — a separate post-hoc UPDATE rather than a column
// set at AddMessage time, since the duration isn't known until agent.Run
// has already returned (and thus after the message's ID exists to attach
// it to). Mirrors SetContextTokens' same shape for the same reason.
func (s *Store) SetMessageDuration(messageID int64, durationMs int64) error {
	_, err := s.db.Exec(`UPDATE messages SET duration_ms = ? WHERE id = ?`, durationMs, messageID)
	return err
}

// SetMessageAttachment records the display filename/content-type for a
// user message that carried an upload — same post-hoc-UPDATE shape as
// SetMessageDuration, since AddMessage needs to run first for the
// message's ID to exist.
func (s *Store) SetMessageAttachment(messageID int64, filename, contentType string) error {
	_, err := s.db.Exec(`UPDATE messages SET attachment_filename = ?, attachment_content_type = ? WHERE id = ?`,
		filename, contentType, messageID)
	return err
}
