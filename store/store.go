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
	-- "atlas" for one started as an Atlas Quick Answer (see gateway/ask.go),
	-- or a caller-supplied label (e.g. "her-go") for threads created via
	-- POST /api/ask. Mostly informational, with one behavioral exception:
	-- see continued_in_assistant below.
	source TEXT NOT NULL DEFAULT 'web',
	-- continued_in_assistant: an "atlas"-sourced thread starts out hidden
	-- from ListThreads (the Assistant sidebar) until this flips to 1 —
	-- set by handleGetThread the first time the thread is actually opened
	-- there (e.g. via Quick Answer's "Continue in Assistant" link).
	-- Without this, every one-off Quick Answer query — including repeat
	-- searches for the same thing, each its own thread — permanently
	-- cluttered the sidebar whether or not anyone ever followed up on it.
	-- Meaningless for any other source, which ListThreads' filter never
	-- even checks this column for.
	continued_in_assistant INTEGER NOT NULL DEFAULT 0,
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
	active_variant_id TEXT NOT NULL DEFAULT '',
	-- focus_mode/deep_research, alongside model above, are a thread's
	-- sticky turn config — read back into the composer on open, written
	-- through on every change, instead of resetting to composer-local
	-- defaults on every reload/thread switch. model was already a
	-- persisted column before this triple existed, but only as a
	-- historical "what this thread last answered with" record nothing
	-- read back; it's repurposed here rather than duplicated. See
	-- docs/plans/pulsar-routines.md's "Prerequisite" section.
	focus_mode TEXT NOT NULL DEFAULT '',
	deep_research INTEGER NOT NULL DEFAULT 0,
	-- pulsar_routine_id: set on a thread created by a Pulsar routine firing
	-- (source = 'pulsar') — lets a routine's pulse history be a plain
	-- WHERE query instead of inferring it from title text. Empty for every
	-- other thread.
	pulsar_routine_id INTEGER,
	-- seen: whether a pulsar-sourced thread's pulse has actually been
	-- opened yet — flipped by the same open path continued_in_assistant
	-- uses for Atlas threads. Drives the amber unread indicator; meaningless
	-- for any non-pulsar thread.
	seen INTEGER NOT NULL DEFAULT 0
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
	-- cards: structured rich-result items (see tools.Card) a tool wants
	-- rendered as their own visual block — e.g. music's recommendations
	-- carousel — set via SetMessageCards once the assistant message's ID
	-- exists, same post-hoc-UPDATE shape as suggestions/duration_ms above.
	-- '[]' for user messages and for any assistant message no tool call
	-- populated cards for.
	cards TEXT NOT NULL DEFAULT '[]',
	-- pending_question: JSON-encoded tools.PendingQuestion, set only on an
	-- assistant message that ended its turn by calling ask_user_question
	-- instead of finishing normally — see SetMessagePendingQuestion.
	-- Answering it is just the next ordinary message in the thread, so
	-- there's no separate "answered" flag: any message after this one
	-- already implies it's resolved. '' for every other message.
	pending_question TEXT NOT NULL DEFAULT '',
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

-- api_usage tracks calendar-month call counts for paid, card-on-file
-- fallback APIs (currently just Parallel's Search API — see
-- tools/web_search.go's fallback chain) whose free tier has a hard cap
-- worth enforcing ourselves rather than trusting the provider not to
-- silently bill overage. One row per (provider, month); IncrementAPIUsage
-- upserts rather than requiring a row to already exist, so a brand-new
-- month just starts a fresh row at 1 the first time it's called.
CREATE TABLE IF NOT EXISTS api_usage (
	provider TEXT NOT NULL,
	-- month: "YYYY-MM", from SQLite's own strftime('%Y-%m', 'now') rather
	-- than a Go-computed timestamp — same reasoning as every other
	-- SQL-side time function in this schema, avoids any host clock/
	-- timezone mismatch between the Go process and what's stored.
	month TEXT NOT NULL,
	count INTEGER NOT NULL DEFAULT 0,
	PRIMARY KEY (provider, month)
);

-- search_history backs Atlas's sidebar "Recent searches"/Favorites
-- sections — the same shape as threads' recency+favorite model, but for
-- one-shot queries rather than conversations, so it's its own table
-- rather than shoehorned into threads. One row per distinct query
-- (exact-match, case-sensitive — see RecordSearch): re-running the same
-- search bumps updated_at instead of creating a duplicate entry, same
-- "recency without clutter" idea as a browser's own history.
CREATE TABLE IF NOT EXISTS search_history (
	id INTEGER PRIMARY KEY AUTOINCREMENT,
	query TEXT NOT NULL,
	favorite INTEGER NOT NULL DEFAULT 0,
	-- Millisecond precision, matching RecordSearch's ON CONFLICT bump
	-- (strftime('%Y-%m-%d %H:%M:%f', 'now')) exactly — CURRENT_TIMESTAMP
	-- only has second precision, so a fresh row and a bumped row could
	-- otherwise get identical updated_at strings within the same second
	-- and sort nondeterministically in ListSearchHistory's ORDER BY
	-- updated_at DESC.
	created_at DATETIME NOT NULL DEFAULT (strftime('%Y-%m-%d %H:%M:%f', 'now')),
	updated_at DATETIME NOT NULL DEFAULT (strftime('%Y-%m-%d %H:%M:%f', 'now'))
);

CREATE UNIQUE INDEX IF NOT EXISTS idx_search_history_query ON search_history(query);

-- search_cache/search_cache_results back Atlas's (and web_search's Brave
-- fallback's) virtual pagination: one row here is one *real* page fetched
-- from a provider (SearXNG's own pageno, or Brave's offset), cached so
-- that (a) re-running the same search, paging back, or reopening a
-- browser tab doesn't re-hit a rate-limited SearXNG or a billed Brave
-- call for data already fetched this same day, and (b) a real page can be
-- sliced into several smaller "virtual" pages on the way out (see
-- gateway/search.go's resolveVirtualPage) without a second real fetch for
-- each slice. provider distinguishes SearXNG's cache from Brave's since
-- their real-page shapes (variable result count vs. Brave's fixed
-- 20-per-request) are entirely different and must never collide on the
-- same key. Query matching is exact/case-sensitive, same convention as
-- search_history.
CREATE TABLE IF NOT EXISTS search_cache (
	id INTEGER PRIMARY KEY AUTOINCREMENT,
	provider TEXT NOT NULL,
	query TEXT NOT NULL,
	category TEXT NOT NULL DEFAULT '',
	-- real_page is 1-indexed for SearXNG (matches SearXNG's own pageno)
	-- and for Brave too (converted from Brave's 0-indexed offset at the
	-- call site) — one consistent numbering scheme across both providers
	-- so the accumulate-until-enough walk in resolveVirtualPage doesn't
	-- need provider-specific indexing logic.
	real_page INTEGER NOT NULL,
	-- max_results is part of the cache key, not just metadata: a page 1
	-- fetched with max_results=20 and one fetched with max_results=40
	-- are different real requests with different result sets, not
	-- interchangeable.
	max_results INTEGER NOT NULL,
	has_more INTEGER NOT NULL DEFAULT 0,
	degraded INTEGER NOT NULL DEFAULT 0,
	fetched_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
	UNIQUE(provider, query, category, real_page, max_results)
);

-- messages_fts is an external-content FTS5 index over messages.content —
-- "external content" (content='messages', content_rowid='id') so the
-- indexed text isn't duplicated on disk, just tokenized; the triggers
-- below are what keep it in sync, since an external-content table doesn't
-- auto-update itself the way a normal one would. Backs full-text search
-- over past chat threads (the sidebar's search box) — the same "find that
-- thing I asked last month" gap search_history already closes for Atlas's
-- one-shot queries, but for actual conversation content.
CREATE VIRTUAL TABLE IF NOT EXISTS messages_fts USING fts5(
	content,
	content='messages',
	content_rowid='id'
);

CREATE TRIGGER IF NOT EXISTS messages_fts_ai AFTER INSERT ON messages BEGIN
	INSERT INTO messages_fts(rowid, content) VALUES (new.id, new.content);
END;

CREATE TRIGGER IF NOT EXISTS messages_fts_ad AFTER DELETE ON messages BEGIN
	INSERT INTO messages_fts(messages_fts, rowid, content) VALUES ('delete', old.id, old.content);
END;

CREATE TRIGGER IF NOT EXISTS messages_fts_au AFTER UPDATE ON messages BEGIN
	INSERT INTO messages_fts(messages_fts, rowid, content) VALUES ('delete', old.id, old.content);
	INSERT INTO messages_fts(rowid, content) VALUES (new.id, new.content);
END;

CREATE TABLE IF NOT EXISTS search_cache_results (
	id INTEGER PRIMARY KEY AUTOINCREMENT,
	cache_id INTEGER NOT NULL REFERENCES search_cache(id) ON DELETE CASCADE,
	-- position preserves this real page's own ranked order — SQLite makes
	-- no ordering guarantee on plain SELECT without ORDER BY, and this
	-- table's insert order isn't reliably its read order once rows have
	-- been through SQLite's own storage/vacuum churn.
	position INTEGER NOT NULL,
	title TEXT NOT NULL,
	url TEXT NOT NULL,
	content TEXT NOT NULL DEFAULT '',
	score REAL NOT NULL DEFAULT 0,
	thumbnail TEXT NOT NULL DEFAULT '',
	engine TEXT NOT NULL DEFAULT '',
	-- engines is a JSON-encoded []string (search.SearchResult.Engines) —
	-- not worth a child table for what's always a handful of short engine
	-- names read back as a single unit, never queried individually.
	engines TEXT NOT NULL DEFAULT '[]',
	rank_state TEXT NOT NULL DEFAULT '',
	pinned INTEGER NOT NULL DEFAULT 0
);

CREATE INDEX IF NOT EXISTS idx_search_cache_results_cache ON search_cache_results(cache_id);

-- memories backs the memory tool (see tools/memory.go): durable facts
-- Polaris's own model chooses to persist about the user/ongoing work
-- across threads, the same idea as Claude Code's own file-based memory
-- system this was deliberately modeled on, just stored as rows instead of
-- markdown files since Polaris already has a per-install SQLite database
-- and no equivalent of a hand-editable, git-tracked memory directory.
-- name is a model-chosen kebab-case slug, not a surrogate id, so the model
-- can address a memory it already knows about (edit/forget) without a
-- prior lookup round trip.
CREATE TABLE IF NOT EXISTS memories (
	name TEXT PRIMARY KEY,
	-- type: "user" | "feedback" | "project" | "reference" — same four-way
	-- split as the source system, see tools/memory.go's api_description for
	-- what each is for.
	type TEXT NOT NULL,
	-- description: one line, always sent to the model as part of the
	-- always-on {memories} index (see agent/driver.go's applyMemoriesPlaceholder)
	-- so it must stay short — the full content is only fetched on demand via
	-- memory(action=view, name=...).
	description TEXT NOT NULL,
	content TEXT NOT NULL,
	created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
	updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
	-- disabled: soft-delete flag, same shape as threads.disabled above —
	-- "forgetting" a memory sets this rather than issuing a real DELETE,
	-- so the record survives but is excluded from every read path
	-- (ListMemories, ListMemoriesFull, GetMemory). See store/memory.go's
	-- DeleteMemory/CreateMemory doc comments for the full reasoning,
	-- including why a forgotten name can be reused (CreateMemory revives
	-- a disabled row instead of failing on it).
	disabled INTEGER NOT NULL DEFAULT 0
);
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
	`ALTER TABLE messages ADD COLUMN cards TEXT NOT NULL DEFAULT '[]'`,
	`ALTER TABLE threads ADD COLUMN continued_in_assistant INTEGER NOT NULL DEFAULT 0`,
	// messages_fts (schema, above) only indexes rows inserted/updated after
	// it existed — a database with messages predating this migration needs
	// a one-time backfill. Plain INSERT, not a "not already indexed" guard:
	// confirmed live that a bare `SELECT rowid FROM messages_fts` on an
	// external-content FTS5 table doesn't read the index at all, it
	// transparently proxies through to the content table (messages) — so
	// that check always found every row "already indexed" and silently
	// backfilled nothing. Safe as a plain INSERT because, like every other
	// entry in this list, it only ever runs once per database via
	// user_version tracking (see applyMigrations); it is not safe to
	// replay by hand.
	`INSERT INTO messages_fts(rowid, content) SELECT id, content FROM messages`,
	`ALTER TABLE messages ADD COLUMN pending_question TEXT NOT NULL DEFAULT ''`,
	// memories shipped before the disabled column existed — an install
	// that already ran CREATE TABLE IF NOT EXISTS memories (above) without
	// it needs this added explicitly, same as every other column here.
	`ALTER TABLE memories ADD COLUMN disabled INTEGER NOT NULL DEFAULT 0`,
	// Pulsar's turn-config-persistence prerequisite — see the schema
	// comment above focus_mode. An existing thread gets focus off/research
	// off by default (the same values a fresh composer already starts
	// with), not whatever that thread's last turn actually used, since
	// nothing recorded that until now.
	`ALTER TABLE threads ADD COLUMN focus_mode TEXT NOT NULL DEFAULT ''`,
	`ALTER TABLE threads ADD COLUMN deep_research INTEGER NOT NULL DEFAULT 0`,
	`ALTER TABLE threads ADD COLUMN pulsar_routine_id INTEGER`,
	`ALTER TABLE threads ADD COLUMN seen INTEGER NOT NULL DEFAULT 0`,
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
	// database/sql pools multiple physical connections by default, and two
	// of them writing at once can still surface as an immediate
	// SQLITE_BUSY/"database is locked" error rather than actually waiting
	// out _busy_timeout above — that pragma governs how long SQLite's own
	// busy handler retries within one connection, not how Go's pool
	// arbitrates between several. Capping the pool to one connection
	// forces every write to queue behind Go's own mutex instead, so two
	// goroutines writing around the same moment (e.g. a turn's detached
	// follow-up-suggestions save landing while the next turn's fork
	// transaction runs — see handleTurn) simply wait their turn rather
	// than erroring. This app's write volume is low enough that
	// serializing all of it through one connection costs nothing
	// noticeable.
	db.SetMaxOpenConns(1)
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
	Favorite bool `json:"favorite"`
	// FocusMode/DeepResearch are this thread's sticky turn config,
	// alongside Model above — see the schema comment on focus_mode.
	FocusMode    string `json:"focus_mode"`
	DeepResearch bool   `json:"deep_research"`
	CreatedAt    time.Time `json:"created_at"`
	UpdatedAt    time.Time `json:"updated_at"`
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
	AttachmentFilename    string `json:"attachment_filename,omitempty"`
	AttachmentContentType string `json:"attachment_content_type,omitempty"`
	// Cards is JSON-encoded []tools.Card — see SetMessageCards.
	Cards string `json:"cards"`
	// PendingQuestion is JSON-encoded *tools.PendingQuestion, set only on
	// an assistant message that ended its turn via ask_user_question —
	// see SetMessagePendingQuestion. "" for every other message.
	PendingQuestion string    `json:"pending_question,omitempty"`
	CreatedAt       time.Time `json:"created_at"`
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

// SetThreadConfig writes through a thread's sticky turn config (model,
// focus mode, deep research) — called on every turn (see handleTurn) so
// reopening a thread later restores exactly what it was last configured
// with, and from handleUpdateThread when the composer's selectors are
// changed directly without sending a message. Deliberately does not touch
// updated_at, matching SetThreadFavorite's reasoning: applying a sticky
// config isn't "activity" on the thread and shouldn't reorder it in the
// sidebar.
func (s *Store) SetThreadConfig(id, model, focusMode string, deepResearch bool) error {
	return execOne(s.db.Exec(
		`UPDATE threads SET model = ?, focus_mode = ?, deep_research = ? WHERE id = ?`,
		model, focusMode, deepResearch, id,
	))
}

// execOne runs a write that's expected to touch exactly one existing row
// (an UPDATE targeting a single id), translating "the id didn't match
// anything" into sql.ErrNoRows so callers — and, following the same
// errors.Is(err, sql.ErrNoRows) -> 404 convention handleGetThread/
// handleRegenerateTitle already use, their HTTP handlers — can tell a
// missing id apart from a real database error instead of both silently
// reporting success.
func execOne(res sql.Result, err error) error {
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return sql.ErrNoRows
	}
	return nil
}

// SetThreadTitle updates a thread's title — used both for the one-time
// LLM-generated title after a new thread's first turn finishes, and for
// a user-initiated rename from the sidebar. Either one replaces
// whatever title was there before; there's no separate "locked" flag,
// since a rename happening at all is itself the signal that the title
// is no longer just the auto-generated placeholder.
func (s *Store) SetThreadTitle(id, title string) error {
	return execOne(s.db.Exec(
		`UPDATE threads SET title = ?, updated_at = strftime('%Y-%m-%d %H:%M:%f', 'now') WHERE id = ?`,
		title, id,
	))
}

// SetThreadFavorite pins/unpins a thread to the sidebar's Favorites
// section. Deliberately does not touch updated_at — favoriting isn't
// "activity" on a thread the way a rename or a new message is, and
// shouldn't reorder it within its section.
func (s *Store) SetThreadFavorite(id string, favorite bool) error {
	return execOne(s.db.Exec(`UPDATE threads SET favorite = ? WHERE id = ?`, favorite, id))
}

// MarkThreadContinued flips continued_in_assistant to 1 — see that
// column's schema comment. Called by handleGetThread the first time an
// "atlas"-sourced thread is actually opened in the Assistant, which is
// what makes it start showing up in ListThreads from then on. A no-op
// (not an error) once it's already 1, and safe to call on a non-"atlas"
// thread too — ListThreads never consults this column for those, so
// setting it there just does nothing observable.
func (s *Store) MarkThreadContinued(id string) error {
	_, err := s.db.Exec(`UPDATE threads SET continued_in_assistant = 1 WHERE id = ?`, id)
	return err
}

// TouchUpdatedAt bumps rootID's own updated_at to now, independent of
// whichever thread is actually being written to. Needed because AddMessage/
// CompactThread bump updated_at on storageThreadID — the effective variant a
// turn is writing into (see EffectiveThreadID), which is a hidden,
// forked thread (fork_root_id set) once anything's ever been edited or
// regenerated in rootID's conversation. ListThreads only ever returns
// root threads (fork_root_id = ”), so without this, a thread with even
// one edit/retry in its past silently stops advancing in the sidebar's
// recency order the moment that happens — every later message keeps
// bumping the hidden variant's own updated_at instead, which nothing
// user-visible ever reads. rootID is always safe to call this with even
// when it has no active variant (storageThreadID == rootID): the two
// bumps just land on the same row a moment apart, which is harmless.
func (s *Store) TouchUpdatedAt(rootID string) error {
	_, err := s.db.Exec(
		`UPDATE threads SET updated_at = strftime('%Y-%m-%d %H:%M:%f', 'now') WHERE id = ?`,
		rootID,
	)
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
		`INSERT INTO messages (thread_id, role, content, citations, suggestions, cost_usd, turn_id, duration_ms, attachment_filename, attachment_content_type, cards, pending_question, created_at)
		 SELECT ?, role, content, citations, suggestions, cost_usd, turn_id, duration_ms, attachment_filename, attachment_content_type, cards, pending_question, created_at
		 FROM messages WHERE thread_id = ? ORDER BY id ASC LIMIT ?`,
		forkID, srcID, atIndex,
	); err != nil {
		return "", err
	}

	// Events (reasoning bursts, tool calls/results) aren't attached to a
	// message row — they're their own rows in a separate table, joined
	// back to a turn only by turn_id (see events.turn_id's doc comment).
	// Copying messages alone would leave the shared prefix's own
	// reasoning/tool-call history stranded under srcID: ListEvents filters
	// by thread_id, so querying by forkID would come back empty even
	// though the messages themselves came through fine. Copying only the
	// turn_ids that actually made it into forkID (not srcID's whole
	// event history) keeps this exact to the prefix, not everything srcID
	// ever did.
	if _, err := tx.Exec(
		`INSERT INTO events (thread_id, level, source, message, data, turn_id, created_at)
		 SELECT ?, level, source, message, data, turn_id, created_at
		 FROM events
		 WHERE thread_id = ? AND turn_id != '' AND turn_id IN (
			 SELECT DISTINCT turn_id FROM messages WHERE thread_id = ? AND turn_id != ''
		 )`,
		forkID, srcID, forkID,
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
		`SELECT id, title, model, cost_usd, context_tokens, compacted_summary, compacted_through_id, source, favorite, focus_mode, deep_research, created_at, updated_at
		 FROM threads WHERE id = ? AND disabled = 0 AND fork_root_id = ''`, id,
	).Scan(&t.ID, &t.Title, &t.Model, &t.CostUSD, &t.ContextTokens, &t.CompactedSummary, &t.CompactedThroughID, &t.Source, &t.Favorite, &t.FocusMode, &t.DeepResearch, &t.CreatedAt, &t.UpdatedAt)
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
		`SELECT id, title, model, cost_usd, context_tokens, compacted_summary, compacted_through_id, source, favorite, focus_mode, deep_research, created_at, updated_at
		 FROM threads WHERE id = ?`, id,
	).Scan(&t.ID, &t.Title, &t.Model, &t.CostUSD, &t.ContextTokens, &t.CompactedSummary, &t.CompactedThroughID, &t.Source, &t.Favorite, &t.FocusMode, &t.DeepResearch, &t.CreatedAt, &t.UpdatedAt)
	if err != nil {
		return nil, err
	}
	return &t, nil
}

// ListThreads returns non-disabled, non-variant threads newest-first, for
// the sidebar/history view. Favorite/non-favorite are interleaved here in
// one recency order — the frontend splits them into the pinned Favorites
// section and the rest, each keeping this same relative ordering.
//
// source = 'atlas' AND continued_in_assistant = 0 is excluded — a Quick
// Answer creates a real thread on every query (see gateway/ask.go), and
// without this, every one-off search (repeated ones most of all)
// permanently cluttered this list whether or not anyone ever actually
// followed up on it in the Assistant. See continued_in_assistant's schema
// comment for how it flips to 1.
func (s *Store) ListThreads(limit int) ([]Thread, error) {
	if limit <= 0 {
		limit = 100
	}
	rows, err := s.db.Query(
		`SELECT id, title, model, cost_usd, context_tokens, source, favorite, focus_mode, deep_research, created_at, updated_at
		 FROM threads
		 WHERE disabled = 0 AND fork_root_id = '' AND (source != 'atlas' OR continued_in_assistant = 1)
		 ORDER BY updated_at DESC LIMIT ?`,
		limit,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var threads []Thread
	for rows.Next() {
		var t Thread
		if err := rows.Scan(&t.ID, &t.Title, &t.Model, &t.CostUSD, &t.ContextTokens, &t.Source, &t.Favorite, &t.FocusMode, &t.DeepResearch, &t.CreatedAt, &t.UpdatedAt); err != nil {
			return nil, err
		}
		threads = append(threads, t)
	}
	return threads, rows.Err()
}

// MessageSearchResult is one matching message from SearchMessages, not a
// deduped-by-thread summary — a user hunting for "that thing I asked"
// wants to see where each hit actually landed (a thread can match more
// than once), not just which threads contain a match somewhere.
type MessageSearchResult struct {
	ThreadID    string    `json:"thread_id"`
	ThreadTitle string    `json:"thread_title"`
	Role        string    `json:"role"`
	// Snippet wraps each matched term in \x02...\x03 (ASCII STX/ETX,
	// never legitimate message content) instead of literal HTML — the
	// frontend splits on these markers and renders highlights as real
	// text nodes, so there's no {@html} injection surface from search
	// results built out of past user/assistant text.
	Snippet   string    `json:"snippet"`
	CreatedAt time.Time `json:"created_at"`
}

// buildFTSQuery turns free-text search-box input into an FTS5 MATCH
// expression: each whitespace-separated token is individually quoted
// (doubling any embedded quote) with a trailing * for prefix matching,
// ANDed together. Quoting is what makes this safe against a token that
// happens to contain an FTS5 operator character (AND, OR, NOT, -, (, ")
// being parsed as query syntax instead of literal text typed by the user;
// the prefix match is what makes it feel "natural" while still typing,
// rather than requiring a whole word before anything matches.
func buildFTSQuery(query string) string {
	fields := strings.Fields(query)
	if len(fields) == 0 {
		return ""
	}
	parts := make([]string, 0, len(fields))
	for _, f := range fields {
		parts = append(parts, `"`+strings.ReplaceAll(f, `"`, `""`)+`"*`)
	}
	return strings.Join(parts, " AND ")
}

// SearchMessages does a full-text search over every message's content via
// messages_fts (kept in sync by triggers — see the schema comment),
// ranked by FTS5's own bm25-based relevance.
//
// Deliberately not just ListThreads' filter copy-pasted: a message row
// can live in a hidden variant thread (fork_root_id set — see
// ForkThread's doc comment) that's currently the *effective* content of
// its root, per EffectiveThreadID/active_variant_id. Filtering out every
// fork_root_id-set thread (what ListThreads does, since it's only ever
// listing roots) would make an edited/regenerated message's own current
// content unsearchable while its now-superseded predecessor in the root
// thread stayed findable — the opposite of what "search my chats" should
// mean. The join against `root` below instead includes a message iff its
// owning thread is exactly the one EffectiveThreadID(root) would resolve
// to: the root itself when active_variant_id is unset, or that specific
// variant when it's been swapped to. Every other check (disabled, Atlas
// visibility) reads from `root`, not `t` — a forked thread's own
// disabled/source/continued_in_assistant columns are just copied
// defaults from ForkThread and never independently updated (DeleteThread
// only ever flips the root's own row), so root's values are the ones
// that are actually authoritative. ThreadID/ThreadTitle are root's too:
// a forked thread's title is always '' (ForkThread never sets one) and
// its own id isn't independently addressable by GetThread — only a root
// id is, which is what clicking a result needs to open.
func (s *Store) SearchMessages(query string, limit int) ([]MessageSearchResult, error) {
	if limit <= 0 {
		limit = 30
	}
	ftsQuery := buildFTSQuery(query)
	if ftsQuery == "" {
		return []MessageSearchResult{}, nil
	}

	rows, err := s.db.Query(
		`SELECT root.id, root.title, m.role, m.created_at,
			snippet(messages_fts, 0, char(2), char(3), '…', 12)
		 FROM messages_fts
		 JOIN messages m ON m.id = messages_fts.rowid
		 JOIN threads t ON t.id = m.thread_id
		 JOIN threads root ON root.id = COALESCE(NULLIF(t.fork_root_id, ''), t.id)
		 WHERE messages_fts MATCH ?
		   AND root.disabled = 0
		   AND (root.active_variant_id = t.id OR (root.active_variant_id = '' AND t.id = root.id))
		   AND (root.source != 'atlas' OR root.continued_in_assistant = 1)
		 ORDER BY rank LIMIT ?`,
		ftsQuery, limit,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	results := []MessageSearchResult{}
	for rows.Next() {
		var r MessageSearchResult
		if err := rows.Scan(&r.ThreadID, &r.ThreadTitle, &r.Role, &r.CreatedAt, &r.Snippet); err != nil {
			return nil, err
		}
		results = append(results, r)
	}
	return results, rows.Err()
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

type SearchHistoryEntry struct {
	ID        int64     `json:"id"`
	Query     string    `json:"query"`
	Favorite  bool      `json:"favorite"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// RecordSearch logs a completed Atlas search — called once per query from
// handleSearch, not per keystroke. An exact repeat of an existing query
// (case-sensitive, trimmed by the caller) bumps its updated_at instead of
// inserting a duplicate row, same recency-without-clutter idea as
// TouchUpdatedAt for threads.
func (s *Store) RecordSearch(query string) error {
	_, err := s.db.Exec(
		`INSERT INTO search_history (query) VALUES (?)
		 ON CONFLICT(query) DO UPDATE SET updated_at = strftime('%Y-%m-%d %H:%M:%f', 'now')`,
		query,
	)
	return err
}

// ListSearchHistory returns searches newest-first, for Atlas's sidebar —
// same shape as ListThreads: favorite/non-favorite interleaved in one
// recency order, split into sections by the frontend.
func (s *Store) ListSearchHistory(limit int) ([]SearchHistoryEntry, error) {
	if limit <= 0 {
		limit = 100
	}
	rows, err := s.db.Query(
		`SELECT id, query, favorite, created_at, updated_at
		 FROM search_history ORDER BY updated_at DESC LIMIT ?`,
		limit,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var entries []SearchHistoryEntry
	for rows.Next() {
		var e SearchHistoryEntry
		if err := rows.Scan(&e.ID, &e.Query, &e.Favorite, &e.CreatedAt, &e.UpdatedAt); err != nil {
			return nil, err
		}
		entries = append(entries, e)
	}
	return entries, rows.Err()
}

// SetSearchHistoryFavorite pins/unpins a search to the sidebar's Favorites
// section — deliberately doesn't touch updated_at, same reasoning as
// SetThreadFavorite. Returns sql.ErrNoRows for an id that doesn't exist,
// same convention as SetThreadFavorite/SetThreadTitle.
func (s *Store) SetSearchHistoryFavorite(id int64, favorite bool) error {
	return execOne(s.db.Exec(`UPDATE search_history SET favorite = ? WHERE id = ?`, favorite, id))
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
			attachment_filename, attachment_content_type, cards, pending_question, created_at
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
			&m.AttachmentFilename, &m.AttachmentContentType, &m.Cards, &m.PendingQuestion, &m.CreatedAt); err != nil {
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

// SetMessageCards records a tool's structured rich-result items (see
// tools.Card) after the assistant message was already persisted — same
// post-hoc-UPDATE shape as SetMessageDuration above, since cards (like
// citations) are only known once agent.Run has already returned.
func (s *Store) SetMessageCards(messageID int64, cardsJSON string) error {
	_, err := s.db.Exec(`UPDATE messages SET cards = ? WHERE id = ?`, cardsJSON, messageID)
	return err
}

// SetMessagePendingQuestion records a turn-ending ask_user_question call
// (see tools.PendingQuestion) after the assistant message was already
// persisted — same post-hoc-UPDATE shape as SetMessageCards above.
func (s *Store) SetMessagePendingQuestion(messageID int64, pendingQuestionJSON string) error {
	_, err := s.db.Exec(`UPDATE messages SET pending_question = ? WHERE id = ?`, pendingQuestionJSON, messageID)
	return err
}

// SetMessageSuggestions records follow-up suggestions generated after the
// assistant message was already persisted — generateSuggestions now runs
// after handleTurn sends "done" (so the turn footer doesn't wait on it),
// so this is a post-hoc UPDATE rather than part of the original AddMessage
// insert, same shape as SetMessageDuration above.
func (s *Store) SetMessageSuggestions(messageID int64, suggestionsJSON string) error {
	_, err := s.db.Exec(`UPDATE messages SET suggestions = ? WHERE id = ?`, suggestionsJSON, messageID)
	return err
}

// AddThreadCost adds delta to a thread's running cost total — used for
// costs incurred after AddMessage's own cost_usd bump already ran, e.g.
// follow-up suggestions generated post-"done" (see SetMessageSuggestions).
func (s *Store) AddThreadCost(threadID string, delta float64) error {
	_, err := s.db.Exec(`UPDATE threads SET cost_usd = cost_usd + ? WHERE id = ?`, delta, threadID)
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

// IncrementAPIUsage bumps provider's call count for the current calendar
// month by one (creating the row at 1 if this is the first call this
// month) and returns the new total — see api_usage's schema comment.
// Callers that need to check the cap before spending a call should use
// GetAPIUsage first; this only records that a call was actually made.
func (s *Store) IncrementAPIUsage(provider string) (int, error) {
	if _, err := s.db.Exec(
		`INSERT INTO api_usage (provider, month, count) VALUES (?, strftime('%Y-%m', 'now'), 1)
		 ON CONFLICT(provider, month) DO UPDATE SET count = count + 1`,
		provider,
	); err != nil {
		return 0, err
	}
	return s.GetAPIUsage(provider)
}

// GetAPIUsage returns provider's call count for the current calendar
// month — 0 if nothing's been recorded yet (a brand-new month, or a
// provider that's never been used).
func (s *Store) GetAPIUsage(provider string) (int, error) {
	var count int
	err := s.db.QueryRow(
		`SELECT count FROM api_usage WHERE provider = ? AND month = strftime('%Y-%m', 'now')`,
		provider,
	).Scan(&count)
	if err == sql.ErrNoRows {
		return 0, nil
	}
	return count, err
}
