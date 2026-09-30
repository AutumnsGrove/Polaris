package store

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
	-- ghost: issue #67's ghost/incognito mode. A ghost thread is a fully
	-- real thread from its very first turn — real messages/events/title/
	-- cost, nothing special-cased in the persistence path — the only
	-- differences while this is 1 are: (a) excluded from ListThreads/
	-- ListThreadsPage/SearchMessages/GetThread/ReadThread, so it doesn't
	-- look persisted from the UI or a tool's perspective, and (b) memory/
	-- chat_search/stars/custom-instructions tool access is withheld for
	-- the turn (see gateway/turn.go's ghost-gated tools.Context wiring).
	-- POST /api/threads/{id}/promote (PromoteGhostThread) clears this with
	-- one UPDATE — nothing else needs to change, since every gate above
	-- re-checks this column fresh each turn rather than trusting anything
	-- the client says past the thread's creation turn. If a WebSocket
	-- session ends (disconnect or server crash) while a thread is still
	-- tagged ghost, the whole row — and its messages/events, via the
	-- ON DELETE CASCADE FKs below — is hard-deleted instead of soft-
	-- deleted like disabled above; see DeleteThreadPermanently,
	-- DeleteAllGhostThreads, and gateway/ws.go's disconnect cleanup.
	ghost INTEGER NOT NULL DEFAULT 0,
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
	-- field_id: the Field (see the fields table below and
	-- docs/plans/fields.md) this thread belongs to, or NULL for an
	-- ordinary ungrouped thread. Nullable with no default, so every
	-- pre-Fields thread is simply "not in a field" — the same "add a
	-- column, only matters going forward" shape as pulsar_routine_id above.
	-- A real FK, not a bare TEXT: DeleteField must NULL these out in the
	-- same transaction that removes the field row (foreign_keys=on, see
	-- Open), never leave a dangling id.
	field_id TEXT REFERENCES fields(id),
	-- seen: whether a pulsar-sourced thread's pulse has actually been
	-- opened yet — flipped by the same open path continued_in_assistant
	-- uses for Atlas threads. Drives the amber unread indicator; meaningless
	-- for any non-pulsar thread.
	seen INTEGER NOT NULL DEFAULT 0,
	-- compacted_pending_notice/compacted_pending_cost: a one-shot handoff
	-- between the turn that compacted this thread and the next turn that
	-- runs on it. Compaction is detached from the triggering turn's own
	-- "done" (it used to run synchronously before "done" shipped, making
	-- the user wait out a whole extra LLM round-trip for a summary they
	-- never asked to watch), so its cost can no longer ride along on that
	-- turn's cost_usd — and there is no live client listening at the moment
	-- it finishes for a bare POST /api/ask turn or a pulse. Instead the
	-- completed compaction marks itself here, and the NEXT turn on this
	-- thread takes the marker, announces the summary to whoever is
	-- listening then, and clears it. See gateway/turn.go's compaction
	-- block and TakeCompactionNotice.
	--
	-- The summary itself is NOT duplicated here — it's already
	-- compacted_summary above, which is the current effective one. Only the
	-- cost (which has no other home: threads.cost_usd and the through
	-- message's cost_usd have both already absorbed it by the time anyone
	-- reads this) and the "unannounced" flag need storing.
	compacted_pending_notice INTEGER NOT NULL DEFAULT 0,
	-- Accumulated with +=, not overwritten: if two compactions somehow land
	-- before a turn takes the notice (a detached compaction that was still
	-- in flight when a turn read the flag as clear, then finished after),
	-- both costs still reach the session total exactly once. See the
	-- compaction in-flight guard in gateway/server.go for the case that
	-- actually keeps this rare.
	compacted_pending_cost REAL NOT NULL DEFAULT 0
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
	-- ("📎 report.pdf") when the thread is reopened. '' on every other
	-- message.
	attachment_filename TEXT NOT NULL DEFAULT '',
	attachment_content_type TEXT NOT NULL DEFAULT '',
	-- workspace_file_id: the exact addressable filename (a short generated
	-- id plus its extension) an uploaded file was given inside this
	-- thread's persistent code_exec workspace directory (see
	-- gateway/attachments.go's resolveAttachment) — what a model tool call
	-- actually addresses the file by, and the literal last path segment
	-- GET /api/workspace/:thread_id/:filename serves. attachment_filename
	-- above stays the human-readable display name; this is the on-disk
	-- addressing identity. '' on every message with no attachment, and on
	-- attachment rows predating this column (that upload was already
	-- deleted after one read under the old single-read-then-deleted
	-- lifecycle, so there's nothing on disk to point at).
	workspace_file_id TEXT NOT NULL DEFAULT '',
	-- attachments: JSON-encoded []Attachment, one entry per file included
	-- with a user message (issue #71 — multiple attachments per turn).
	-- Supersedes attachment_filename/attachment_content_type/
	-- workspace_file_id above, which are now frozen: nothing writes them
	-- after this column shipped, they're kept only so GetMessages can
	-- synthesize a one-element attachments array for rows written before
	-- this column existed. '[]' for a message with no upload.
	attachments TEXT NOT NULL DEFAULT '[]',
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
	-- tts_audio_file_id: the addressable filename (short generated id plus
	-- ".wav") of a persisted read-aloud synthesis for this assistant
	-- message, inside the same per-thread code_exec workspace directory
	-- workspace_file_id above uses -- the same
	-- GET /api/workspace/:thread_id/:filename route serves it back. A
	-- dedicated column rather than reusing workspace_file_id/attachments:
	-- those are upload-specific and frozen (see the attachments comment
	-- above), and a message could plausibly carry both a user upload and a
	-- TTS synthesis. '' for every message with no persisted read-aloud audio.
	tts_audio_file_id TEXT NOT NULL DEFAULT '',
	-- verification: JSON-encoded []gateway.VerificationMark — per-claim
	-- "found in source" results from checking this assistant message's
	-- own inline citations against the text actually fetched for them
	-- (see docs/plans/source-verification-badge.md), set via
	-- SetMessageVerification once the assistant message's ID exists, same
	-- post-hoc-UPDATE shape as suggestions/cards/chart above — the
	-- verification pass runs in a detached goroutine after "done" ships,
	-- so it's never known at AddMessage time. '[]' for user messages and
	-- for any assistant message with no citations, an unconfigured Jev
	-- client, or nothing found supported at/above the confidence
	-- threshold.
	verification TEXT NOT NULL DEFAULT '[]',
	-- prompt_tokens/cache_read_tokens: the turn's input tokens summed
	-- across every LLM call agent.Run made, and how many of those the
	-- provider served from its prompt cache (issue #107). Assistant
	-- messages only, 0 elsewhere and on every turn from before this
	-- existed. Per-message rather than a running thread counter, same as
	-- cost_usd's own audit trail: the thread-level hit % is summed on read
	-- (see ThreadCacheUsage), so a fork's copied prefix counts too.
	prompt_tokens INTEGER NOT NULL DEFAULT 0,
	cache_read_tokens INTEGER NOT NULL DEFAULT 0,
	-- transcript: JSON-encoded exact wire messages this turn sent (see
	-- llm.EncodeTranscript and agent.Result.Transcript) — the model-facing
	-- user message through the final answer, tool calls/results
	-- untruncated. Assistant messages only. loadHistory replays it
	-- verbatim so later turns share a byte-identical, cacheable prefix
	-- (docs/plans/verbatim-turn-transcripts.md); '' for every turn from
	-- before this existed, which falls back to the older reconstruction.
	transcript TEXT NOT NULL DEFAULT '',
	-- oracle_result: JSON-encoded gateway.OracleResult — which checks ran,
	-- each winner/probability, and which injections fired (docs/plans/
	-- oracle-mode.md, issue #122), set via SetMessageOracleResult once the
	-- assistant message's ID exists, same post-hoc-UPDATE shape as
	-- verification above. '' for a message with Oracle off or unconfigured.
	oracle_result TEXT NOT NULL DEFAULT '',
	-- focus_mode_source: "manual" (picked in the composer for this
	-- message), "default" (Settings' standing default), or "oracle" (this
	-- turn's Oracle focus check fired) — see oracle-mode.md's "Manual
	-- always wins" section for why the server needs to tell these apart.
	-- '' for a message with no focus mode in play at all.
	focus_mode_source TEXT NOT NULL DEFAULT '',
	-- applied_focus_mode: this turn's actual resolved agent.FocusMode
	-- ("" for none) — distinct from threads.focus_mode (the thread's
	-- current *sticky* config, overwritten every turn) and from
	-- oracle_result's own FocusMode field (Oracle's pick even when
	-- overridden by manual — see gateway.OracleResult's doc comment).
	-- This is "what this specific turn actually ran with", captured once
	-- per assistant message so the frontend can compare a turn's own
	-- applied mode against the nearest earlier turn's to render "kept
	-- your X" vs. "Switched X -> Y" in the Oracle margin note
	-- (docs/plans/oracle-mode.md) — threads.focus_mode alone can't
	-- answer that per-turn, since it's just whatever the client last
	-- sent, never Oracle's own picks (see SetMessageAppliedFocusMode).
	applied_focus_mode TEXT NOT NULL DEFAULT '',
	-- applied_model: this turn's own requested model id (config.yaml's
	-- model id, e.g. "deepseek-v4.1"), alongside applied_focus_mode above
	-- for the same reason — threads.model is the thread's current sticky
	-- default, overwritten every turn, so it can't answer "what did THIS
	-- past turn use" once a later turn switches models. Doesn't account
	-- for llm.Client's own runtime provider fallback within that model id
	-- (e.g. the DeepSeek 429 pool-exhaustion retry) — this is what was
	-- requested, which is what the turn-info sheet's "Model" stat means to
	-- show.
	applied_model TEXT NOT NULL DEFAULT '',
	-- completion_tokens: this turn's summed output tokens, the other half
	-- of prompt_tokens above (issue #107 only exposed the input side) —
	-- added for the turn-info sheet's "tokens out" stat (docs/plans/
	-- oracle-mode.md).
	completion_tokens INTEGER NOT NULL DEFAULT 0,
	-- last_prompt_tokens/llm_calls: the turn's LAST model call's input
	-- size and how many calls the turn made. prompt_tokens above is a
	-- sum over every call (needed for the cache-hit ratio), and a tool
	-- turn re-sends the whole prefix per call, so it reads as a multiple
	-- of the real context size — last_prompt_tokens is that real size,
	-- the number to watch when judging prompt bloat. 0 on rows from
	-- before this existed (no way to reconstruct it in hindsight).
	last_prompt_tokens INTEGER NOT NULL DEFAULT 0,
	llm_calls INTEGER NOT NULL DEFAULT 0,
	-- cost_answer_usd/cost_verification_usd/cost_oracle_usd: the same
	-- total cost_usd above, split by what spent it (docs/plans/
	-- oracle-mode.md's "three-tier cost") — the main answer (plus
	-- follow-up suggestions and title regen, both part of producing it),
	-- the per-claim "found in source" pass, and Oracle's own Jev call.
	-- cost_usd stays their sum, read by every existing cost display; these
	-- three are additive detail, not a replacement.
	cost_answer_usd REAL NOT NULL DEFAULT 0,
	cost_verification_usd REAL NOT NULL DEFAULT 0,
	cost_oracle_usd REAL NOT NULL DEFAULT 0,
	-- ttft_ms/tokens_per_second: time to the first streamed token and the
	-- answer's overall generation rate — assistant messages only, 0 for
	-- user messages and for any turn from before these were recorded.
	-- Set post-hoc once agent.Run returns, same shape as duration_ms.
	ttft_ms INTEGER NOT NULL DEFAULT 0,
	tokens_per_second REAL NOT NULL DEFAULT 0,
	-- tool_call_count: how many tool calls this turn made in total —
	-- assistant messages only, 0 elsewhere. Set post-hoc alongside
	-- ttft_ms/tokens_per_second.
	tool_call_count INTEGER NOT NULL DEFAULT 0,
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

-- image_candidates is image_search's numbered result pool, persisted per
-- thread (see image_candidates.go). The model's history keeps the numbered
-- candidate list from earlier turns, but tools.Context is rebuilt every
-- turn — without this, "show images 2 and 3" on a follow-up turn cites
-- numbers the server no longer knows. Its own table rather than reading
-- events back: events are pruned after 90 days, threads never are.
CREATE TABLE IF NOT EXISTS image_candidates (
	thread_id TEXT NOT NULL REFERENCES threads(id) ON DELETE CASCADE,
	num       INTEGER NOT NULL, -- 1-based, the number the model was told
	card      TEXT NOT NULL,    -- JSON-encoded tools.Card
	PRIMARY KEY (thread_id, num)
);

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

-- ghost_usage is historical-only, as of the full-fidelity ghost-thread
-- redesign (see the ghost column's schema comment above). It used to be
-- the one thing a ghost-mode (issue #67) turn ever left behind, back when
-- handleTurn skipped every other store.Store write for such a turn — no
-- thread/message/event rows — which meant a ghost turn's real, billed LLM
-- spend had nowhere else to land. A ghost thread now bills cost through
-- the normal AddMessage/AddTurnCost path like any other thread, so
-- nothing writes new rows here anymore; old rows are kept and still
-- folded into Polaris's regular cost totals by GetStats (a ghost thread
-- was always an incognito regular chat, not a separate subsystem, so its
-- spend belongs in the same bucket a persisted thread's would have) — see
-- RecordGhostCost and stats.go's GetStats.
CREATE TABLE IF NOT EXISTS ghost_usage (
	id INTEGER PRIMARY KEY AUTOINCREMENT,
	cost_usd REAL NOT NULL,
	created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);

-- jev_usage is a per-call ledger of real Jev (TypeSafe AI) spend — one row
-- per API call, cost_usd read directly off that call's own response (never
-- estimated), same shape as ghost_usage above (a per-row ledger with
-- created_at, not a monthly aggregate) so it can back both a monthly cap
-- check (SUM WHERE created_at falls in the current calendar month, see
-- JevCostThisMonth) and Stats.VerificationCostUSD's trailing-N-day breakout
-- (SUM WHERE created_at >= since, matching every other period-filtered stat
-- in stats.go). That breakout is a transparency slice into money already
-- counted once via tools.Context.AddCost -> messages.cost_usd — GetStats
-- must never add this into TotalCostUSD/PeriodCostUSD a second time. See
-- docs/plans/source-verification-compare-tool.md's cost-tracking section.
CREATE TABLE IF NOT EXISTS jev_usage (
	id INTEGER PRIMARY KEY AUTOINCREMENT,
	cost_usd REAL NOT NULL,
	-- source: "" for verification/compare_sources spend (every row from
	-- before this column existed), "oracle" for Oracle mode's pre-read
	-- (issue #125) — lets Stats break the two out separately while the
	-- monthly cap (JevCostThisMonth) still sums every row.
	source TEXT NOT NULL DEFAULT '',
	created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);

-- aux_usage is a per-call ledger of real, billed LLM spend the assistant
-- incurred on a chat's behalf that belongs to no single turn's own cost —
-- currently gateway/pulsar_suggest.go's one-shot "derive a routine prompt
-- from this conversation" call made when the operator taps an offer chip,
-- and gateway/wizard.go's ephemeral interview turns (which by design
-- persist no threads/messages rows to bill, so each turn records its own
-- cost here).
-- Folded straight into CostBySource.Polaris by GetStats — the same place
-- the spend would have landed had it happened inside a turn — so it
-- reaches the settings panel's grand total without needing a bucket of
-- its own. kind names the caller (e.g. "pulsar_suggest"/"wizard:field_instructions")
-- purely so a future one can be told apart in the table; GetStats sums
-- across all kinds, since the split it reports is by subsystem, not by
-- call site.
CREATE TABLE IF NOT EXISTS aux_usage (
	id INTEGER PRIMARY KEY AUTOINCREMENT,
	kind TEXT NOT NULL,
	cost_usd REAL NOT NULL,
	created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
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

-- fields backs Fields (see docs/plans/fields.md, issue #119): a named
-- container of threads with its own custom instructions, a shared read-only
-- file pool (<CodeExecWorkspaceDir>/<id>/, never this table's concern), and a
-- handful of per-field turn defaults. Threads join via threads.field_id.
-- A deleted field is a real DELETE (no disabled column) — see
-- store/fields.go's DeleteField for the orphan-the-threads sequence.
CREATE TABLE IF NOT EXISTS fields (
	id TEXT PRIMARY KEY,
	name TEXT NOT NULL,
	-- description: one line, shown on the hub's card grid.
	description TEXT NOT NULL DEFAULT '',
	-- custom_instructions: same free-text shape as the global operator-wide
	-- field (gateway/settings.go's maxCustomInstructionsChars cap applies),
	-- appended after it — never instead of it — at turn-context build.
	custom_instructions TEXT NOT NULL DEFAULT '',
	-- favorite: 1 = pinned into the sidebar's Fields section, the same
	-- mechanism threads.favorite is. An unfavorited field is reachable
	-- only via /fields.
	favorite INTEGER NOT NULL DEFAULT 0,
	-- default_focus_mode/default_model: '' = inherit the global standing
	-- default (settings.defaultFocusMode / settings.defaultModel), not
	-- "force off" — 'off' is itself a valid focus mode id.
	default_focus_mode TEXT NOT NULL DEFAULT '',
	default_model TEXT NOT NULL DEFAULT '',
	-- memory_mode: 'default' | 'none'. 'field_scoped' is a reserved,
	-- not-yet-functional value (the plan's v2) — accepted nowhere yet, see
	-- ValidFieldMemoryMode.
	memory_mode TEXT NOT NULL DEFAULT 'default',
	-- constellation_visible: 0 = Weaver skips this field's threads.
	constellation_visible INTEGER NOT NULL DEFAULT 1,
	-- exclude_from_chat_search: 1 = SearchMessages skips this field's
	-- threads entirely.
	exclude_from_chat_search INTEGER NOT NULL DEFAULT 0,
	-- color: '' = no tag, else one of app.css's --color-cat-* suffixes.
	color TEXT NOT NULL DEFAULT '',
	created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
	updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
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
	-- use_oracle: whether this routine's pulses may run through Oracle mode
	-- (issue #141). Subordinate to the global oracle_enabled setting — it can
	-- only opt a routine *out* (gateway sets NoOracle from it), never force
	-- Oracle on when the operator has it off. Defaults to 1 so every routine
	-- keeps the behavior it had before this column existed.
	use_oracle INTEGER NOT NULL DEFAULT 1,
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
	architect_model TEXT NOT NULL DEFAULT 'deepseek',
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
	-- backfill_started_at: NULL means no backfill is running. Set at the
	-- start of BackfillConstellation, cleared when it finishes -- the live
	-- per-minute scheduler (runConstellationTick) checks this and skips its
	-- own tick entirely while it's set, so a manual "constellation backfill"
	-- run and the ordinary poller can't both pick up the same thread. A DB
	-- column, not an in-process flag/mutex, specifically because backfill
	-- and the scheduler are NOT always the same OS process: under Docker
	-- both run inside the container's own server, but bare-metal's
	-- "constellation backfill" is a separate short-lived CLI process
	-- against the same SQLite file (see CLAUDE.md's dual-deployment
	-- section) -- only something both processes can see by reading the
	-- database itself actually closes the race. Treated as stale (ignored)
	-- past backfillStaleAfter so a crashed/killed backfill can't wedge the
	-- scheduler off forever -- see runConstellationTick's own check.
	backfill_started_at   DATETIME,
	-- person_name/person_pronouns: optional operator-supplied guidance
	-- about themselves (set in the Constellation settings panel), prepended
	-- to Weaver's system prompt on every shooting star (see
	-- gateway/constellation_weaver.go's newWeaverToolContext and
	-- agent/driver.go's loadSystemPrompt) -- without it Weaver has no
	-- signal for either and has to guess, which is exactly what motivated
	-- adding this: Weaver defaulting to he/him with no basis for the guess.
	person_name           TEXT NOT NULL DEFAULT '',
	person_pronouns       TEXT NOT NULL DEFAULT '',
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
	updated_at   DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
	-- content_updated_at: bumped only by a real content merge (Store.UpdateStar),
	-- unlike updated_at (bumped by every mutator -- rename, disable, status
	-- change, content merge). GetConstellationWeekFeed's "updated" bucket
	-- needs this distinction: keying off updated_at meant approving/renaming/
	-- disabling a star made it show up as "updated" in "This week", which the
	-- plan doc's "The weekly digest" explicitly says review actions must not
	-- do ("resolutions of something already made, not new material").
	content_updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
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

-- LatestCandidateReasoningBulk (called once per Inbox page load, for every
-- proposed star) filters/IN's on resulting_star_id with no supporting index
-- otherwise -- a full table scan that only grows as every create_star/
-- update_star call ever made adds another row here.
CREATE INDEX IF NOT EXISTS idx_shooting_star_candidates_star ON shooting_star_candidates(resulting_star_id);

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

-- star_reconcile_events tracks the cost of Refine/Edit-star LLM calls --
-- reconcileStarContent (gateway/constellation_routes.go) is a one-off
-- completion outside any shooting_star_runs row (no thread pass, no
-- agent.Run), so it can't log into shooting_star_events the way Weaver's
-- own turns do (run_id there is NOT NULL). Previously this cost was
-- silently dropped entirely -- live-observed as the Usage modal's totals
-- undercounting real spend by every Refine/Edit call made.
CREATE TABLE IF NOT EXISTS star_reconcile_events (
	id         INTEGER PRIMARY KEY AUTOINCREMENT,
	star_id    INTEGER NOT NULL REFERENCES stars(id) ON DELETE CASCADE,
	cost_usd   REAL NOT NULL DEFAULT 0,
	created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);

-- star_versions snapshots a star's content-bearing columns right before
-- every real content merge (Store.UpdateStar) overwrites them -- version
-- N is "what the star looked like going into update N", so reverting to a
-- version just replays it through UpdateStar again (source 'revert')
-- rather than deleting rows, keeping this table honestly append-only, the
-- same way git revert never rewrites history. Plain rename/disable
-- (RenameStar/SetStarDisabled) don't snapshot here -- scoped to real
-- content merges only, matching content_updated_at's own scope on stars.
CREATE TABLE IF NOT EXISTS star_versions (
	id               INTEGER PRIMARY KEY AUTOINCREMENT,
	star_id          INTEGER NOT NULL REFERENCES stars(id) ON DELETE CASCADE,
	version_number   INTEGER NOT NULL,
	title            TEXT NOT NULL,
	summary          TEXT NOT NULL,
	body             TEXT NOT NULL,
	tags             TEXT NOT NULL,
	confidence       TEXT NOT NULL,
	source           TEXT NOT NULL,
	created_at       DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);
CREATE INDEX IF NOT EXISTS idx_star_versions_star_id ON star_versions(star_id, version_number);
`
