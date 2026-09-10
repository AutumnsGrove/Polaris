// Package store persists threads and messages to SQLite so past
// sessions can be revisited, restarted, or continued with a follow-up
// question — and so per-thread cost can be shown in the UI.
package store

import (
	"database/sql"
	"encoding/base64"
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
	-- no_research: the composer's "Research" toggle switched off (chat
	-- mode) — the fourth sticky field, added after the original three
	-- (model/focus_mode/deep_research) shipped with Pulsar's prerequisite
	-- work. It was left composer-local at the time (see gateway/protocol.
	-- go's ClientMessage.NoResearch doc comment), which meant leaving a
	-- thread in chat mode and reopening it silently dropped back to
	-- research-on — a real, reported gap, not an intentional exclusion.
	no_research INTEGER NOT NULL DEFAULT 0,
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
	-- chart: JSON-encoded tools.ChartSpec, set via SetMessageChart once the
	-- assistant message's ID exists, same post-hoc-UPDATE shape as cards
	-- above. Unlike cards this is a single object, not an array — a turn
	-- produces at most one chart. '' for user messages and for any
	-- assistant message no tool call produced a chart for.
	chart TEXT NOT NULL DEFAULT '',
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
	-- occurred_at: an optional "YYYY-MM-DD" the model sets when a fact has
	-- a meaningful date distinct from row bookkeeping (created_at/
	-- updated_at record when Polaris touched the row, not when the fact
	-- itself became true) — a decision date, a deadline, a "true as of"
	-- marker. Plain TEXT, not DATETIME: it's a model-supplied date with no
	-- time component, never compared against CURRENT_TIMESTAMP. Empty
	-- string (not NULL) for memories with no meaningful date, matching
	-- every other optional TEXT column in this schema, so callers never
	-- have to sql.NullString-unwrap it.
	occurred_at TEXT NOT NULL DEFAULT '',
	-- disabled: soft-delete flag, same shape as threads.disabled above —
	-- "forgetting" a memory sets this rather than issuing a real DELETE,
	-- so the record survives but is excluded from every read path
	-- (ListMemories, ListMemoriesFull, GetMemory). See store/memory.go's
	-- DeleteMemory/CreateMemory doc comments for the full reasoning,
	-- including why a forgotten name can be reused (CreateMemory revives
	-- a disabled row instead of failing on it).
	disabled INTEGER NOT NULL DEFAULT 0
);

-- pulsar_routines backs Pulsar (see docs/plans/pulsar-routines.md): a saved
-- prompt that fires on a schedule instead of when typed, each firing (a
-- "pulse") a real thread — source = 'pulsar', pulsar_routine_id set (see
-- threads' schema comment above) — run through the exact same turn
-- pipeline as any other message.
CREATE TABLE IF NOT EXISTS pulsar_routines (
	id INTEGER PRIMARY KEY AUTOINCREMENT,
	name TEXT NOT NULL,
	prompt TEXT NOT NULL,
	-- model/focus_mode/deep_research: the same sticky turn-config triple
	-- threads carry (see threads' schema comment) — a routine stores its
	-- own copy rather than referencing a thread's, since a routine exists
	-- independent of any pulse it's produced yet.
	model TEXT NOT NULL,
	focus_mode TEXT NOT NULL DEFAULT '',
	deep_research INTEGER NOT NULL DEFAULT 0,
	-- schedule_type: 'daily' | 'weekly' | 'monthly'. schedule_params holds
	-- whatever schedule_type needs beyond time_of_day — empty for daily,
	-- a weekday name (e.g. "monday") for weekly, a 1-31 day-of-month
	-- string for monthly. Kept as one flexible string column rather than
	-- separate weekday/day-of-month columns since exactly one of them is
	-- ever meaningful for a given schedule_type, and the v1 schedule model
	-- (see the plan doc) is deliberately just these three shapes.
	schedule_type TEXT NOT NULL,
	schedule_params TEXT NOT NULL DEFAULT '',
	-- time_of_day: "HH:MM", 24-hour, server-local — no timezone handling,
	-- per the plan doc's single-operator framing.
	time_of_day TEXT NOT NULL,
	created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
	-- last_run_at: NULL means never fired yet. Checked by the scheduler on
	-- every tick (including right after process start, for catch-up —
	-- see the plan doc's "Catch-up on restart") against schedule_type/
	-- schedule_params/time_of_day to decide whether a pulse is due.
	last_run_at DATETIME,
	-- archived_at: NULL means active. Delete is always soft in v1 — see
	-- the plan doc's "Routine lifecycle" — archiving sets this instead of
	-- removing the row, so the routine and every pulse it ever produced
	-- stay intact and readable, just excluded from the scheduler and the
	-- active-routines list.
	archived_at DATETIME
);

-- pulsar_daily_config is the Daily feature's singleton settings row (see
-- docs/plans/pulsar-daily.md) — unlike pulsar_routines, Polaris is
-- single-operator and there's only ever one Daily, so this is one row
-- (id fixed to 1) rather than a routines-style table built for arbitrarily
-- many independent schedules.
CREATE TABLE IF NOT EXISTS pulsar_daily_config (
	id INTEGER PRIMARY KEY CHECK (id = 1),
	-- created_at: the due-time baseline for a Daily that has never
	-- generated yet (last_generated_at nil) — same reasoning as
	-- pulsar_routines.created_at's doc comment: without it, configuring
	-- Daily at 2pm with time_of_day 07:00 would treat today's already-
	-- passed 7am slot as missed and generate immediately on save.
	created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
	-- enabled_blocks: JSON array of block keys the user has toggled on —
	-- excludes "top_story", which isn't independently generated content,
	-- it's Stage B's elevation of whichever watch block wins the ranking
	-- pass (see the plan doc's "Top Story: LLM-elected, not a fixed slot").
	enabled_blocks TEXT NOT NULL DEFAULT '["word_of_day","weather","on_this_day","headlines","trending","sports","picture_of_day","quote","local"]',
	-- sports_teams: free-text team/league preference — required once
	-- "sports" is enabled, since no sane default exists for it (see
	-- "Per-block settings UI").
	sports_teams TEXT NOT NULL DEFAULT '',
	-- custom_instructions: JSON object mapping a block key to an
	-- optional free-text steering instruction — e.g. "focus on AI and
	-- climate policy" for headlines, or "space and wildlife photography"
	-- for picture_of_day. Added after real usage showed the original v1
	-- design ("no per-block setting earns its keep besides Sports") was
	-- wrong: a generic "give me the news" prompt with no way to say what
	-- you actually want to see isn't useful even though sane defaults
	-- exist. Unlike sports_teams, every entry here is optional — an
	-- absent/empty key just means "use the plain default framing".
	custom_instructions TEXT NOT NULL DEFAULT '{}',
	-- custom_blocks: JSON array of user-authored "general purpose" blocks
	-- with no fixed registry entry at all — {key, title, instructions}.
	-- Unlike enabled_blocks/custom_instructions above (which only ever
	-- reference the fixed dailyBlockRegistry), presence in this list *is*
	-- enabled — there's no separate on/off toggle for a block the user
	-- typed themselves. Added after a real session asked for exactly
	-- this: a way to add something outside dailyBlockRegistry's fixed set
	-- without a code change. key is generated once client-side at
	-- creation and never changes even if title is edited later, so
	-- renaming a block doesn't look like a brand new one to yesterday's
	-- diff-judge lookup or pulsar_daily_trace.
	custom_blocks TEXT NOT NULL DEFAULT '[]',
	-- weather_location: overrides config.yaml's app-wide default_location
	-- for the Weather block only — empty means "use default_location",
	-- same fallback every other location-aware tool already has. Weather
	-- is a direct tools.Dispatch call with no LLM-authored task text, so
	-- it's the one block custom_instructions' "append a steering sentence"
	-- mechanism can't help at all — it needed its own typed field.
	weather_location TEXT NOT NULL DEFAULT '',
	-- architect_model/writer_model: registry IDs (models/models.go), not
	-- raw OpenRouter model strings — same convention pulsar_routines.model
	-- uses. See the plan doc's "Model tiering" for why these are split:
	-- architect judges (Stage A diff-verdicts, Stage B ranking), writer
	-- generates prose (Stage A block content, Stage C elaboration).
	architect_model TEXT NOT NULL DEFAULT 'deepseek-pro',
	writer_model TEXT NOT NULL DEFAULT 'deepseek',
	-- time_of_day: "HH:MM", 24-hour, server-local — same convention and
	-- same single-operator reasoning as pulsar_routines.time_of_day.
	time_of_day TEXT NOT NULL DEFAULT '07:00',
	-- last_generated_at: NULL means never generated yet. Checked against
	-- time_of_day by the scheduler tick, same isRoutineDue-style due-check
	-- pulsar_routines' last_run_at drives.
	last_generated_at DATETIME,
	-- enabled: the Daily-wide on/off switch — checked by the scheduler
	-- before any due-check runs at all, so turning it off means no
	-- generation happens today, not "generate but hide it". Defaults to 1
	-- (on) — a first-time GetDailyConfig INSERT OR IGNORE row should behave
	-- like the feature always did before this column existed.
	enabled INTEGER NOT NULL DEFAULT 1
);

-- pulsar_daily_editions holds one assembled edition per calendar date — what
-- tomorrow's Stage A diff-judge compares fresh content against, and what
-- makes the "← Yesterday" button real history instead of a dead one (see
-- the plan doc's Stage D). Edition retention (keep forever vs. prune) is a
-- deliberately open question, same as backup.go's snapshot retention for a
-- different table — not resolved here.
CREATE TABLE IF NOT EXISTS pulsar_daily_editions (
	id INTEGER PRIMARY KEY AUTOINCREMENT,
	-- edition_date: "YYYY-MM-DD", server-local, one row per date — a
	-- second Stage D run for the same date (e.g. a manual re-trigger)
	-- overwrites rather than duplicating (see store/pulsar_daily.go).
	edition_date TEXT NOT NULL UNIQUE,
	-- blocks: JSON array of rendered block objects (key, title, content,
	-- gist, is_top_story, ...) — one JSON blob rather than a child table
	-- because an edition is always read/written whole (the full masonry
	-- page, or the full diff-judge comparison), never queried per-block.
	blocks TEXT NOT NULL,
	-- cost_usd: total LLM spend across every stage that produced this
	-- edition (every block's generation call, every Watch block's
	-- diff-judge call, Stage B's ranking call, Stage C's elaboration) —
	-- shown in the frontend so real generation cost isn't invisible.
	cost_usd REAL NOT NULL DEFAULT 0,
	created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);

-- pulsar_daily_trace records what every stage actually produced for every
-- enabled block, every day — not just whatever survived into
-- pulsar_daily_editions. Added after a real session where a "the edition
-- looks empty" question turned out to be unanswerable: an "unchanged"
-- Watch-block verdict or a hard generation failure both silently dropped
-- that block's content with nothing but an ephemeral log.Warn line, and
-- the diff-judge/top-story-election tool schemas didn't even ask the
-- model to explain *why* a verdict or winner was chosen, only what it
-- was. One row per block per date (not one row per stage) since a block's
-- full lifecycle is always read together when auditing "what happened to
-- X today" — never queried per-stage in isolation.
CREATE TABLE IF NOT EXISTS pulsar_daily_trace (
	id INTEGER PRIMARY KEY AUTOINCREMENT,
	edition_date TEXT NOT NULL,
	block_key TEXT NOT NULL,
	title TEXT NOT NULL DEFAULT '',
	-- stage_a_content: the full text Stage A generated, recorded
	-- regardless of whether it survived into the edition — exactly the
	-- data that was previously discarded outright for a dropped block.
	stage_a_content TEXT NOT NULL DEFAULT '',
	-- verdict/gist/diff_reasoning: only set for a Watch block that had a
	-- prior day's edition to diff against (see generateOneDailyBlock's
	-- "First-ever day" branch) — '' otherwise. diff_reasoning is a new
	-- field the model is now asked for alongside verdict/gist (see
	-- dailyVerdictToolDef) — previously the model was never asked to
	-- justify a verdict at all.
	verdict TEXT NOT NULL DEFAULT '',
	gist TEXT NOT NULL DEFAULT '',
	diff_reasoning TEXT NOT NULL DEFAULT '',
	-- included/is_top_story/top_story_reasoning/stage_c_content are set
	-- later, by Stage D, once it's known which blocks actually made the
	-- final edition and which one (if any) got elected and elaborated —
	-- see UpdateDailyBlockTraceOutcome.
	included INTEGER NOT NULL DEFAULT 0,
	is_top_story INTEGER NOT NULL DEFAULT 0,
	top_story_reasoning TEXT NOT NULL DEFAULT '',
	stage_c_content TEXT NOT NULL DEFAULT '',
	-- error: set instead of stage_a_content when generation failed
	-- outright — the exact detail that used to exist only in a transient
	-- log line, gone the moment the dev log rotated or the process
	-- restarted.
	error TEXT NOT NULL DEFAULT '',
	cost_usd REAL NOT NULL DEFAULT 0,
	created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
	UNIQUE(edition_date, block_key)
);

-- constellation_config is Constellation's singleton settings row (see
-- docs/plans/constellation.md) -- same shape as pulsar_daily_config: one
-- row (id fixed to 1), not a per-item table, since there's only ever one
-- Constellation.
CREATE TABLE IF NOT EXISTS constellation_config (
	id                    INTEGER PRIMARY KEY CHECK (id = 1),
	enabled               INTEGER NOT NULL DEFAULT 0,
	poll_interval_minutes INTEGER NOT NULL DEFAULT 60,
	last_checked_at       DATETIME,
	-- model: empty means "use whatever config.DefaultModel currently
	-- resolves to" -- same empty-means-inherit pattern
	-- pulsar_daily_config.weather_location uses.
	model                 TEXT NOT NULL DEFAULT '',
	created_at            DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);

-- stars is Constellation's main table, one row per topic -- see the plan
-- doc's "Database schema" section for the full reasoning behind each
-- column, most notably status/is_personal's routing rules.
CREATE TABLE IF NOT EXISTS stars (
	id           INTEGER PRIMARY KEY AUTOINCREMENT,
	title        TEXT NOT NULL,
	category     TEXT NOT NULL,
	summary      TEXT NOT NULL DEFAULT '',
	body         TEXT NOT NULL DEFAULT '',
	tags         TEXT NOT NULL DEFAULT '[]',
	status       TEXT NOT NULL DEFAULT 'proposed',
	confidence   TEXT NOT NULL DEFAULT '',
	is_personal  INTEGER NOT NULL DEFAULT 0,
	disabled     INTEGER NOT NULL DEFAULT 0,
	created_at   DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
	updated_at   DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);

-- stars_fts is an external-content FTS5 index over stars, same
-- external-content-plus-sync-triggers shape as messages_fts above. Indexes
-- title+summary only (not the full body) -- search_stars is a dedup/
-- link-discovery lead-finder, not a full-body search.
CREATE VIRTUAL TABLE IF NOT EXISTS stars_fts USING fts5(
	title, summary,
	content='stars', content_rowid='id'
);

CREATE TRIGGER IF NOT EXISTS stars_fts_ai AFTER INSERT ON stars BEGIN
	INSERT INTO stars_fts(rowid, title, summary) VALUES (new.id, new.title, new.summary);
END;

CREATE TRIGGER IF NOT EXISTS stars_fts_ad AFTER DELETE ON stars BEGIN
	INSERT INTO stars_fts(stars_fts, rowid, title, summary) VALUES ('delete', old.id, old.title, old.summary);
END;

CREATE TRIGGER IF NOT EXISTS stars_fts_au AFTER UPDATE ON stars BEGIN
	INSERT INTO stars_fts(stars_fts, rowid, title, summary) VALUES ('delete', old.id, old.title, old.summary);
	INSERT INTO stars_fts(rowid, title, summary) VALUES (new.id, new.title, new.summary);
END;

-- star_sources backs the "Linked articles" list on Star detail -- a real
-- table, not a JSON blob, because a thread can contribute to the same star
-- more than once over time (see "Revisiting a thread" in the plan doc);
-- each contribution upserts (refreshes linked_at) rather than duplicating.
CREATE TABLE IF NOT EXISTS star_sources (
	id        INTEGER PRIMARY KEY AUTOINCREMENT,
	star_id   INTEGER NOT NULL REFERENCES stars(id) ON DELETE CASCADE,
	thread_id TEXT NOT NULL REFERENCES threads(id),
	linked_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
	UNIQUE (star_id, thread_id)
);

-- star_edges is the reflection layer -- star-to-star connections shown on
-- the Map and "Nearby in the constellation". star_a_id < star_b_id is
-- enforced so a pair is only ever stored once regardless of which order
-- link_stars was called with -- see Store.LinkStars.
CREATE TABLE IF NOT EXISTS star_edges (
	id         INTEGER PRIMARY KEY AUTOINCREMENT,
	star_a_id  INTEGER NOT NULL REFERENCES stars(id) ON DELETE CASCADE,
	star_b_id  INTEGER NOT NULL REFERENCES stars(id) ON DELETE CASCADE,
	reasoning  TEXT NOT NULL DEFAULT '',
	created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
	CHECK (star_a_id < star_b_id),
	UNIQUE (star_a_id, star_b_id)
);

-- shooting_star_runs is one row per shooting star -- one agent.Run over one
-- thread. needs_retry/error implement unconditional, no-backoff retry (see
-- the plan doc's "Failure and retry"); cost_usd is a cached rollup of
-- shooting_star_events.cost_usd for this run, never its own source of
-- truth.
CREATE TABLE IF NOT EXISTS shooting_star_runs (
	id                    INTEGER PRIMARY KEY AUTOINCREMENT,
	thread_id             TEXT NOT NULL REFERENCES threads(id),
	last_message_id_seen  INTEGER NOT NULL,
	started_at            DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
	finished_at           DATETIME,
	summary               TEXT NOT NULL DEFAULT '',
	error                 TEXT NOT NULL DEFAULT '',
	needs_retry           INTEGER NOT NULL DEFAULT 0,
	cost_usd              REAL NOT NULL DEFAULT 0
);

CREATE INDEX IF NOT EXISTS idx_shooting_star_runs_thread ON shooting_star_runs(thread_id);

-- shooting_star_candidates is one row per topic candidate Weaver proposes
-- within a run -- populated as a side effect of create_star/update_star
-- (see Store.CreateStar/UpdateStar callers in gateway/constellation_weaver.go),
-- not by a separate logging step.
CREATE TABLE IF NOT EXISTS shooting_star_candidates (
	id                 INTEGER PRIMARY KEY AUTOINCREMENT,
	run_id             INTEGER NOT NULL REFERENCES shooting_star_runs(id) ON DELETE CASCADE,
	title              TEXT NOT NULL,
	confidence_class   TEXT NOT NULL DEFAULT '',
	decision           TEXT NOT NULL DEFAULT '',
	reasoning          TEXT NOT NULL DEFAULT '',
	resulting_star_id  INTEGER REFERENCES stars(id),
	created_at         DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);

-- shooting_star_events is a generic trace of every tool call and
-- completion turn in a run, not just the writes -- the cost-auditability
-- record: one row per LLM completion call in the loop, whether that turn
-- called a tool or ended the run in plain text ('final_answer').
CREATE TABLE IF NOT EXISTS shooting_star_events (
	id         INTEGER PRIMARY KEY AUTOINCREMENT,
	run_id     INTEGER NOT NULL REFERENCES shooting_star_runs(id) ON DELETE CASCADE,
	tool       TEXT NOT NULL,
	args       TEXT NOT NULL DEFAULT '{}',
	result     TEXT NOT NULL DEFAULT '',
	cost_usd   REAL NOT NULL DEFAULT 0,
	created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE INDEX IF NOT EXISTS idx_shooting_star_events_run ON shooting_star_events(run_id);

-- star_reviews is the human side of the observability story, sibling to
-- shooting_star_runs/shooting_star_candidates on the machine side --
-- scoped to Inbox review only (see the plan doc's "Reviewing and editing a
-- star" for why an Edit-star correction on an already-confirmed star does
-- not write here).
CREATE TABLE IF NOT EXISTS star_reviews (
	id         INTEGER PRIMARY KEY AUTOINCREMENT,
	star_id    INTEGER NOT NULL REFERENCES stars(id) ON DELETE CASCADE,
	action     TEXT NOT NULL,
	correction TEXT NOT NULL DEFAULT '',
	created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
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
	`ALTER TABLE messages ADD COLUMN chart TEXT NOT NULL DEFAULT ''`,
	`ALTER TABLE pulsar_daily_config ADD COLUMN custom_instructions TEXT NOT NULL DEFAULT '{}'`,
	`ALTER TABLE pulsar_daily_editions ADD COLUMN cost_usd REAL NOT NULL DEFAULT 0`,
	`ALTER TABLE pulsar_daily_config ADD COLUMN custom_blocks TEXT NOT NULL DEFAULT '[]'`,
	`ALTER TABLE pulsar_daily_config ADD COLUMN weather_location TEXT NOT NULL DEFAULT ''`,
	// The fourth sticky field, added later than the model/focus_mode/
	// deep_research trio — see the schema comment on no_research. An
	// existing thread defaults to research-on (0), the same value a fresh
	// composer already starts with. Appended at the end, not inserted
	// alongside the other three above — applyMigrations tracks progress by
	// positional index (PRAGMA user_version), not migration content, so
	// inserting mid-list shifts every later index and leaves entries
	// silently unapplied on a database that already ran past that point.
	// Confirmed live: this was originally inserted mid-list and the
	// potato's existing DB skipped it entirely, surfacing as "no such
	// column: no_research" the moment a thread was reopened.
	`ALTER TABLE threads ADD COLUMN no_research INTEGER NOT NULL DEFAULT 0`,
	// Re-attempt, appended fresh: the entry above ran once already as part
	// of the broken mid-list deploy — on a database that already executed
	// it (silently, as a tolerated "duplicate column" skip against
	// whatever migration actually ended up at that shifted index), its
	// user_version has already moved past that slot, so the entry above
	// alone will never run again there. This one gives it an actually-new
	// slot to run in for real. Safe everywhere else too: a fresh database
	// already has the column from CREATE TABLE, so both this and the
	// entry above just hit the same tolerated duplicate-column skip.
	`ALTER TABLE threads ADD COLUMN no_research INTEGER NOT NULL DEFAULT 0`,
	// occurred_at — see the schema comment above. Appended at the end per
	// this file's own established rule: applyMigrations tracks progress by
	// positional index, so a new column always goes last, never inserted
	// alongside the schema comment it corresponds to.
	`ALTER TABLE memories ADD COLUMN occurred_at TEXT NOT NULL DEFAULT ''`,
	// enabled: the Daily-wide on/off switch — see the schema comment above
	// enabled_blocks. Defaults to 1 (on) so an existing install's edition
	// keeps generating on upgrade exactly as it did before this column
	// existed, rather than silently going dark until someone opens
	// settings and notices.
	`ALTER TABLE pulsar_daily_config ADD COLUMN enabled INTEGER NOT NULL DEFAULT 1`,
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
	// FocusMode/DeepResearch/NoResearch are this thread's sticky turn
	// config, alongside Model above — see the schema comment on
	// focus_mode/no_research.
	FocusMode    string `json:"focus_mode"`
	DeepResearch bool   `json:"deep_research"`
	NoResearch   bool   `json:"no_research"`
	// PulsarRoutineID is set only on a pulse (source = "pulsar") — nil for
	// every other thread. The frontend uses this to show a "back to
	// routine" affordance on a pulse's thread view instead of the normal
	// sidebar-toggle-only header, per the plan doc's "Pulse detail" UI.
	PulsarRoutineID *int64    `json:"pulsar_routine_id,omitempty"`
	CreatedAt       time.Time `json:"created_at"`
	UpdatedAt       time.Time `json:"updated_at"`
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
	// Chart is JSON-encoded *tools.ChartSpec — see SetMessageChart. ""
	// (omitted) for every message no tool call produced a chart for.
	Chart string `json:"chart,omitempty"`
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
// focus mode, deep research, no-research/chat-mode) — called on every turn
// (see handleTurn) so reopening a thread later restores exactly what it was
// last configured with, and from handleUpdateThread when the composer's
// selectors are changed directly without sending a message. Deliberately
// does not touch updated_at, matching SetThreadFavorite's reasoning:
// applying a sticky config isn't "activity" on the thread and shouldn't
// reorder it in the sidebar.
func (s *Store) SetThreadConfig(id, model, focusMode string, deepResearch, noResearch bool) error {
	return execOne(s.db.Exec(
		`UPDATE threads SET model = ?, focus_mode = ?, deep_research = ?, no_research = ? WHERE id = ?`,
		model, focusMode, deepResearch, noResearch, id,
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
		`INSERT INTO messages (thread_id, role, content, citations, suggestions, cost_usd, turn_id, duration_ms, attachment_filename, attachment_content_type, cards, chart, pending_question, created_at)
		 SELECT ?, role, content, citations, suggestions, cost_usd, turn_id, duration_ms, attachment_filename, attachment_content_type, cards, chart, pending_question, created_at
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
		`SELECT id, title, model, cost_usd, context_tokens, compacted_summary, compacted_through_id, source, favorite, focus_mode, deep_research, no_research, pulsar_routine_id, created_at, updated_at
		 FROM threads WHERE id = ? AND disabled = 0 AND fork_root_id = ''`, id,
	).Scan(&t.ID, &t.Title, &t.Model, &t.CostUSD, &t.ContextTokens, &t.CompactedSummary, &t.CompactedThroughID, &t.Source, &t.Favorite, &t.FocusMode, &t.DeepResearch, &t.NoResearch, &t.PulsarRoutineID, &t.CreatedAt, &t.UpdatedAt)
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
		`SELECT id, title, model, cost_usd, context_tokens, compacted_summary, compacted_through_id, source, favorite, focus_mode, deep_research, no_research, created_at, updated_at
		 FROM threads WHERE id = ?`, id,
	).Scan(&t.ID, &t.Title, &t.Model, &t.CostUSD, &t.ContextTokens, &t.CompactedSummary, &t.CompactedThroughID, &t.Source, &t.Favorite, &t.FocusMode, &t.DeepResearch, &t.NoResearch, &t.CreatedAt, &t.UpdatedAt)
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
//
// source = 'pulsar' is excluded unconditionally, with no equivalent
// "graduates into visibility" escape hatch — a pulse is only ever meant
// to be browsed via /pulsar's own routine detail view (see
// docs/plans/pulsar-routines.md's "UI structure"), never the ordinary
// chat sidebar. Confirmed live: without this, every pulse showed up in
// Recents indistinguishable from a normal chat thread.
func (s *Store) ListThreads(limit int) ([]Thread, error) {
	if limit <= 0 {
		limit = 100
	}
	rows, err := s.db.Query(
		`SELECT id, title, model, cost_usd, context_tokens, source, favorite, focus_mode, deep_research, pulsar_routine_id, created_at, updated_at
		 FROM threads
		 WHERE disabled = 0 AND fork_root_id = '' AND source != 'pulsar' AND (source != 'atlas' OR continued_in_assistant = 1)
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
		if err := rows.Scan(&t.ID, &t.Title, &t.Model, &t.CostUSD, &t.ContextTokens, &t.Source, &t.Favorite, &t.FocusMode, &t.DeepResearch, &t.PulsarRoutineID, &t.CreatedAt, &t.UpdatedAt); err != nil {
			return nil, err
		}
		threads = append(threads, t)
	}
	return threads, rows.Err()
}

// HistoryEntry is one reconstructed turn from EffectiveHistory — a plain
// Role/Content pair rather than llm.ChatMessage, since store can't import
// package llm (llm has no dependency on store today, and adding one just
// for this return type isn't worth it) and search_chats' read action wants
// to render this as a flat transcript rather than a chat-message list
// anyway.
type HistoryEntry struct {
	Role    string
	Content string
}

// EffectiveHistory reconstructs a thread's prior turns exactly the way
// gateway/turn.go's loadHistory sends them to the LLM: if the thread has
// been auto-compacted, everything at or below CompactedThroughID collapses
// into one leading summary entry instead of being replayed message-by-
// message. excludeFromID, if nonzero, additionally skips every message
// with id >= excludeFromID — loadHistory's own retry/edit-path need (see
// its doc comment); pass 0 for a plain full-history read (search_chats'
// ReadThread below never needs it).
//
// Shared by loadHistory and ReadThread specifically so the two can't drift
// out of sync — see docs/plans/search-chats.md's "The read action" section,
// which calls out hand-duplicating this loop as the wrong move.
func EffectiveHistory(thread *Thread, msgs []Message, excludeFromID int64) []HistoryEntry {
	history := make([]HistoryEntry, 0, len(msgs)+1)
	if thread.CompactedSummary != "" {
		history = append(history, HistoryEntry{
			Role: "assistant",
			Content: "(Summary of earlier conversation, compacted to save context — the full history " +
				"is no longer available, only this summary)\n\n" + thread.CompactedSummary,
		})
	}
	for _, m := range msgs {
		if m.ID <= thread.CompactedThroughID {
			continue // covered by the summary above
		}
		if excludeFromID != 0 && m.ID >= excludeFromID {
			continue
		}
		history = append(history, HistoryEntry{Role: m.Role, Content: m.Content})
	}
	return history
}

// ThreadReadResult is search_chats' read action's raw material — a past
// thread's full reconstructed transcript, compaction-substituted the same
// way a resumed live thread would be. See ReadThread.
type ThreadReadResult struct {
	ThreadID  string
	Title     string
	CreatedAt time.Time
	UpdatedAt time.Time
	Content   string
}

// ReadThread reconstructs a past thread's full transcript for search_chats'
// read action. Uses GetThreadRaw, not the public GetThread — same reasoning
// as loadHistory: a thread the model wants to read back should still be
// readable via a thread_id a prior search call actually returned, without
// re-litigating GetThread's own disabled/hidden-variant rejection here.
func (s *Store) ReadThread(threadID string) (*ThreadReadResult, error) {
	thread, err := s.GetThreadRaw(threadID)
	if err != nil {
		return nil, err
	}
	msgs, err := s.GetMessages(threadID)
	if err != nil {
		return nil, err
	}
	entries := EffectiveHistory(thread, msgs, 0)

	var sb strings.Builder
	for i, e := range entries {
		if i > 0 {
			sb.WriteString("\n\n")
		}
		label := "User"
		if e.Role == "assistant" {
			label = "Assistant"
		}
		sb.WriteString(label + ": " + e.Content)
	}

	return &ThreadReadResult{
		ThreadID:  thread.ID,
		Title:     thread.Title,
		CreatedAt: thread.CreatedAt,
		UpdatedAt: thread.UpdatedAt,
		Content:   sb.String(),
	}, nil
}

// RecencyPageSize is search_chats' recency-mode ("search" with no query)
// fixed page size — deliberately not the keyword path's tunable limit, see
// docs/plans/search-chats.md's "Recency mode" section: a flat, predictable
// page size is more useful for "page through my recent threads" than a
// model-guessed number.
const RecencyPageSize = 10

// ThreadSummary is one recency-mode listing entry — title/date/short
// preview, deliberately not a full thread body (see ListThreadsPage).
type ThreadSummary struct {
	ID        string    `json:"id"`
	Title     string    `json:"title"`
	UpdatedAt time.Time `json:"updated_at"`
	Preview   string    `json:"preview"`
}

// encodeThreadCursor/decodeThreadCursor turn a (updated_at, id) resume
// point into an opaque page token and back. Deliberately round-trips
// through Go's time.Time (RFC3339Nano) rather than trying to preserve
// SQLite's own on-disk TEXT format verbatim — database/sql's generic
// convertAssign reformats a DATETIME column's value into RFC3339Nano the
// moment it's scanned into anything (a *time.Time destination, or even a
// *string one — confirmed live: scanning the same column into a string
// variable still came back "2026-01-01T00:03:00Z", not the
// "2026-01-01 00:03:00.000" the row actually stored), so there was never a
// "raw stored text" available to round-trip in the first place. The WHERE
// clause below wraps both sides in SQLite's own datetime() function, which
// normalizes either format to the same canonical value for comparison —
// but the cursor's timestamp must be *bound as a plain string*, not a
// time.Time: confirmed live that modernc.org/sqlite's driver hands a
// time.Time query parameter to SQLite in a form datetime() can't parse at
// all (silently returns NULL, which made every comparison against it NULL
// — i.e. false — collapsing every page-2-or-later fetch to zero rows).
// ThreadSummary.UpdatedAt.Format(time.RFC3339Nano) below is exactly the
// plain-string form that works.
func encodeThreadCursor(updatedAt time.Time, id string) string {
	return base64.URLEncoding.EncodeToString([]byte(updatedAt.UTC().Format(time.RFC3339Nano) + "|" + id))
}

func decodeThreadCursor(cursor string) (updatedAtStr, id string, err error) {
	raw, err := base64.URLEncoding.DecodeString(cursor)
	if err != nil {
		return "", "", fmt.Errorf("invalid cursor: %w", err)
	}
	parts := strings.SplitN(string(raw), "|", 2)
	if len(parts) != 2 {
		return "", "", fmt.Errorf("invalid cursor")
	}
	if _, err := time.Parse(time.RFC3339Nano, parts[0]); err != nil {
		return "", "", fmt.Errorf("invalid cursor: %w", err)
	}
	return parts[0], parts[1], nil
}

// ListThreadsPage lists non-disabled, non-variant, non-pulsar/unopened-
// Atlas threads newest-first — the same visibility filter ListThreads
// applies — keyset-paginated by (updated_at, id) rather than OFFSET/LIMIT.
// Keyset pagination is what makes this stable across a page 2 fetch even
// if a new thread was created after page 1 was returned: it always resumes
// strictly after the last row actually handed back, not "the Nth row of
// whatever the table looks like right now" (which an OFFSET page would,
// silently reshowing or skipping a row when the underlying order shifts
// between fetches). id is included as a tiebreaker purely for determinism
// when two threads share the same updated_at to the stored precision, not
// because id itself carries any chronological meaning.
//
// Preview is each thread's most recent message content, truncated — cheap
// per-thread context for a model reasoning over "what have I been asking
// about lately" without a separate query per thread.
func (s *Store) ListThreadsPage(cursor string) (threads []ThreadSummary, nextCursor string, err error) {
	where := "disabled = 0 AND fork_root_id = '' AND source != 'pulsar' AND (source != 'atlas' OR continued_in_assistant = 1)"
	args := []interface{}{}
	if cursor != "" {
		cursorUpdatedAt, cursorID, derr := decodeThreadCursor(cursor)
		if derr != nil {
			return nil, "", derr
		}
		where += " AND (datetime(updated_at) < datetime(?) OR (datetime(updated_at) = datetime(?) AND id < ?))"
		args = append(args, cursorUpdatedAt, cursorUpdatedAt, cursorID)
	}
	rows, err := s.db.Query(
		`SELECT id, title, updated_at,
			COALESCE((SELECT content FROM messages WHERE thread_id = threads.id ORDER BY id DESC LIMIT 1), '')
		 FROM threads
		 WHERE `+where+`
		 ORDER BY datetime(updated_at) DESC, id DESC LIMIT ?`,
		append(args, RecencyPageSize+1)..., // +1 to detect a next page without returning an empty one
	)
	if err != nil {
		return nil, "", err
	}
	defer rows.Close()

	for rows.Next() {
		var t ThreadSummary
		if err := rows.Scan(&t.ID, &t.Title, &t.UpdatedAt, &t.Preview); err != nil {
			return nil, "", err
		}
		t.Preview = truncateThreadPreview(t.Preview)
		threads = append(threads, t)
	}
	if err := rows.Err(); err != nil {
		return nil, "", err
	}

	if len(threads) > RecencyPageSize {
		last := threads[RecencyPageSize-1]
		nextCursor = encodeThreadCursor(last.UpdatedAt, last.ID)
		threads = threads[:RecencyPageSize]
	}
	return threads, nextCursor, nil
}

const threadPreviewMaxChars = 150

func truncateThreadPreview(s string) string {
	s = strings.TrimSpace(s)
	if len(s) <= threadPreviewMaxChars {
		return s
	}
	return strings.TrimSpace(s[:threadPreviewMaxChars]) + "…"
}

// MessageSearchResult is one matching message from SearchMessages, not a
// deduped-by-thread summary — a user hunting for "that thing I asked"
// wants to see where each hit actually landed (a thread can match more
// than once), not just which threads contain a match somewhere.
type MessageSearchResult struct {
	ThreadID    string `json:"thread_id"`
	ThreadTitle string `json:"thread_title"`
	Role        string `json:"role"`
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
// a forked thread's title is always ” (ForkThread never sets one) and
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
		   AND root.source != 'pulsar'
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
			attachment_filename, attachment_content_type, cards, chart, pending_question, created_at
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
			&m.AttachmentFilename, &m.AttachmentContentType, &m.Cards, &m.Chart, &m.PendingQuestion, &m.CreatedAt); err != nil {
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

// SetMessageChart records a turn's chart (see tools.ChartSpec) after the
// assistant message was already persisted — same post-hoc-UPDATE shape as
// SetMessageCards above.
func (s *Store) SetMessageChart(messageID int64, chartJSON string) error {
	_, err := s.db.Exec(`UPDATE messages SET chart = ? WHERE id = ?`, chartJSON, messageID)
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
