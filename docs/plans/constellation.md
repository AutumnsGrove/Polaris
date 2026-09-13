# Constellation

**Status: implemented and shipped.** Schema, Weaver's five tools, the scheduler, backfill CLI,
review UI, personal-star handling, and cost auditability are all real code — see `store/constellation.go`,
`gateway/constellation_*.go`, `tools/{search,read,create,update,link}_star*.go`,
`cmd/constellation_backfill.go`, and `web/src/lib/constellation*`/`web/src/routes/constellation`.
All Constellation/Weaver/star tests pass (`go test ./gateway/... ./store/... ./tools/...`), the
scheduler follows the same drain-channel shutdown shape as `RunPulsarScheduler` with no CWD-relative
resource reads, and the backfill CLI's `isDockerComposeInstall` gate (proxying to the container's
`/api/constellation/backfill` under Docker) already matches this repo's established dual-deployment
pattern (see CLAUDE.md). This doc is kept as the design record; treat the code as authoritative
where the two disagree. Remaining/deferred work lives in separate issues — see #56 (a `stars` tool
for the *main* assistant, not just Weaver's own internal `search_stars`) and "Deferred to v2" below.

This came out of a live brainstorm with Polaris itself (see the "Polaris Usage Trends and Recent
Queries" thread on the potato, 2026-09-09/10) after using `search_chats` to ask "what do I
actually use you for?" Inspired by DOT (New Computer) — specifically the "it remembers you and
reflects it back" feel, not just a notes dump or a passive transcript log.

## What it is

Polaris auto-generates and maintains a personal knowledge library built out of everything asked
across every thread, organized by **topic, not by chat**. Ask about Cloudflare Workers four times
across four unrelated threads, and that collapses into *one* living **star** that grows over time,
not four scattered notes.

**Naming:**
- **Constellation** — the feature: a browsable library of topics, plus a literal star-map view of
  how they connect.
- **Star** — a single topic. Shown as a card in the Library, a node on the Map.
- **Shooting star** — one background processing run: Weaver reading a single thread and deciding
  what belongs in Constellation.
- **Weaver** — the agent that reads threads, writes/merges stars, and draws the connections
  between them. Runs in its own isolated context with its own narrow tool set — never Polaris's
  main chat agent gaining a new tool, and never given `web_search`/`calculator`/anything from the
  main catalog, since its whole job is reading and inferring from already-written chat content,
  not researching or computing anything new.

**"Vault" and "Obsidian" are inspiration/reference points, not the literal target.** Constellation
is native to SQLite — stars live in the database (frontmatter-equivalent fields as real columns:
title, status, confidence, sources, tags) so Weaver can update them the way any other Polaris data
gets updated, not by parsing and rewriting Markdown files. Obsidian-style Markdown-with-frontmatter
is a target *export format* — assembled on demand — for whenever the user wants a real, portable
vault on disk, not the system's own storage. There's no vault-on-disk to touch in v1 at all, just
database reads/writes, same shape as every other Polaris subsystem. Export itself is pure
assembly: frontmatter is a mechanical rendering of already-structured columns into a `---` block,
and the star body is already well-formatted Markdown the moment Weaver writes it (real headers,
written to be read once and referenced later) — concatenate stored fields and body, done.

## Design principles

- **Not MCP, not the main agent.** A totally separate agent (Weaver), totally separate isolated
  environment, totally different narrow tool set — never Polaris's own `agent.Run` loop gaining a
  tool.
- **An interval-based background poller, not live-per-message and not an unconditional nightly
  job.** Check periodically: any new thread content since last check? If yes, Weaver processes it.
  If no — **zero AI calls, full stop, silently skipped.** This is a hard requirement: it directly
  guards against a real failure mode from a past project (`her-go`, a Go program simulating
  "Samantha" from *Her*) whose nightly "dream sequence" ran unconditionally every night regardless
  of whether anything new had happened — burning tokens for zero informational gain, forever.
- **A new, dedicated UI surface** — not the existing thread/chat UI. Card-based, meant to feel like
  an actual second brain, not "a chat you had."
- **No category gate.** Constellation doesn't restrict Weaver to writing about one topic area at a
  time. Gating by category would mean the model classifying against the gate before deciding
  whether to write anything at all — exactly the kind of forcing this system avoids. Topics emerge
  from what's actually being talked about; whatever mix of books/music/technology/other shows up
  is the real signal. The trace tables (below) are the safety net for watching that unfold, not a
  content filter — restricting scope doesn't reduce risk here, it just hides information worth
  having.
- **Full observability from day one, not an add-later concern.** Every run, every candidate
  decision, every tool call, every review action, every dollar spent is logged and reconstructable.
  Modeled on `gateway/pulsar_daily.go`'s `pulsar_daily_trace`, which exists *because* a real
  silent-drop bug (content generated then discarded with nothing but an ephemeral log line) was
  hard to debug without it — Weaver gets the same treatment up front.
- **Lives entirely inside Polaris** — same repo, same binary, same `polaris.db`. Not a separate
  project or sidecar process. Not expected to make the main chat agent itself smarter at
  anything — this is a user-facing feature (a browsable library), not a capability upgrade for
  `agent.Run`.

## Database schema

New tables, matching `store/store.go`'s existing conventions (the FTS5 external-content-plus-
triggers pattern from `messages_fts`, the singleton-row pattern from `pulsar_daily_config`, the
observability-trace pattern from `pulsar_daily_trace`).

**`constellation_config`** — singleton row, same shape as `pulsar_daily_config`:
```sql
CREATE TABLE constellation_config (
    id                    INTEGER PRIMARY KEY CHECK (id = 1),
    enabled               INTEGER NOT NULL DEFAULT 0,
    poll_interval_minutes INTEGER NOT NULL DEFAULT 60,
    last_checked_at       DATETIME,
    -- model: empty means "use whatever config.DefaultModel currently resolves
    -- to" -- same empty-means-inherit pattern pulsar_daily_config.weather_location
    -- already uses, resolved live at run time so it stays in sync if the
    -- app-wide default model ever changes. Settings panel gets a model
    -- dropdown, "Same as chat (default)" mapping to empty, any other entry an
    -- explicit override -- same picker every other model-select surface uses.
    model                 TEXT NOT NULL DEFAULT '',
    created_at            DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);
```

**`stars`** — the main table, one row per topic:
```sql
CREATE TABLE stars (
    id           INTEGER PRIMARY KEY AUTOINCREMENT,
    title        TEXT NOT NULL,
    -- category: the model's own free-text label (e.g. "books", "technology")
    -- -- no fixed list. Orthogonal to is_personal below.
    category     TEXT NOT NULL,
    summary      TEXT NOT NULL DEFAULT '',   -- Library card's one-liner
    body         TEXT NOT NULL DEFAULT '',   -- the Markdown star body
    tags         TEXT NOT NULL DEFAULT '[]', -- JSON array, shown as chips
    -- status: 'auto' (Weaver wrote it directly, high confidence) | 'proposed'
    -- (awaiting review) | 'confirmed' (a formerly-proposed star a human
    -- approved -- kept distinct from 'auto' so "how often is Weaver's own
    -- confidence judgment right" stays answerable) | 'rejected' (a human said
    -- no -- stays in stars_fts so Weaver's own search_stars can still find it
    -- and treat it as a closed matter, but drops out of the normal Library/
    -- Inbox/Map reads into its own dedicated "Rejected" section instead).
    status       TEXT NOT NULL DEFAULT 'proposed',
    confidence   TEXT NOT NULL DEFAULT '',  -- human-readable, shown on Star detail
    -- is_personal: true for a star that characterizes the person themselves
    -- (an inference about who they are), not a topic they discussed -- see
    -- "Personal stars" below for the full routing rules this drives.
    is_personal  INTEGER NOT NULL DEFAULT 0,
    -- disabled: manual delete/hide, same soft-delete shape as threads.disabled
    -- -- independent of status. A confirmed star the person just doesn't want
    -- in their library anymore isn't "rejected" (that means Weaver got it
    -- wrong); it's disabled, via the star's own overflow menu (see "Reviewing
    -- and editing a star").
    disabled     INTEGER NOT NULL DEFAULT 0,
    created_at   DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at   DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE VIRTUAL TABLE stars_fts USING fts5(
    title, summary,
    content='stars', content_rowid='id'
);
-- plus the standard external-content sync triggers (ai/ad/au), same shape as
-- messages_fts in store/store.go.
```
Columns beyond what the UI shows (Library card, Star detail) aren't invented ahead of need — a
genuinely backend-only field gets added once Weaver's pipeline actually needs somewhere to put it.

**`star_sources`** — child table backing the "Linked articles" list:
```sql
CREATE TABLE star_sources (
    id        INTEGER PRIMARY KEY AUTOINCREMENT,
    star_id   INTEGER NOT NULL REFERENCES stars(id) ON DELETE CASCADE,
    thread_id INTEGER NOT NULL REFERENCES threads(id),
    linked_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    UNIQUE (star_id, thread_id)
);
```
A real table, not a JSON blob, because a thread can contribute to the same star more than once
over time (see "Revisiting a thread" below) — each contribution **upserts** (refreshes
`linked_at`) rather than duplicating.

**`star_edges`** — the reflection layer, star-to-star connections shown on the Map and "Nearby in
the constellation":
```sql
CREATE TABLE star_edges (
    id         INTEGER PRIMARY KEY AUTOINCREMENT,
    star_a_id  INTEGER NOT NULL REFERENCES stars(id) ON DELETE CASCADE,
    star_b_id  INTEGER NOT NULL REFERENCES stars(id) ON DELETE CASCADE,
    reasoning  TEXT NOT NULL DEFAULT '',  -- why -- see link_stars below
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    CHECK (star_a_id < star_b_id),
    UNIQUE (star_a_id, star_b_id)
);
```

**`shooting_star_runs`** — one row per shooting star (one `agent.Run` over one thread):
```sql
CREATE TABLE shooting_star_runs (
    id                    INTEGER PRIMARY KEY AUTOINCREMENT,
    thread_id             INTEGER NOT NULL REFERENCES threads(id),
    -- last_message_id_seen: messages.id is a global autoincrement, so this is
    -- a clean per-thread high-water mark -- see "Thread eligibility" below.
    last_message_id_seen  INTEGER NOT NULL,
    started_at            DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    finished_at           DATETIME,
    summary               TEXT NOT NULL DEFAULT '',  -- Weaver's own closing wrap-up
    -- error: a human-readable failure reason, or the literal 'max_turns_exceeded'
    -- when the 25-turn cap (see "Weaver's tools") was hit -- that specific value
    -- is what ConstellationStats' MaxTurnsCount counts, separate from other
    -- errors.
    error                 TEXT NOT NULL DEFAULT '',
    -- needs_retry: set on any failure (including max_turns_exceeded). The
    -- next poll tick retries this thread unconditionally via the retry gate
    -- in "Thread eligibility", regardless of the delta gate -- no backoff, no
    -- retry limit. Cleared the moment a run for this thread succeeds.
    needs_retry           INTEGER NOT NULL DEFAULT 0,
    -- cost_usd: a cached rollup, SUM(shooting_star_events.cost_usd) for this
    -- run_id -- never its own source of truth, always reconcilable against
    -- the itemized events below.
    cost_usd              REAL NOT NULL DEFAULT 0
);
```

**`shooting_star_candidates`** — one row per topic candidate Weaver proposes within a run:
```sql
CREATE TABLE shooting_star_candidates (
    id                 INTEGER PRIMARY KEY AUTOINCREMENT,
    run_id             INTEGER NOT NULL REFERENCES shooting_star_runs(id) ON DELETE CASCADE,
    title              TEXT NOT NULL,
    confidence_class   TEXT NOT NULL DEFAULT '',  -- 'obvious' | 'fuzzy'
    decision           TEXT NOT NULL DEFAULT '',  -- 'new_star' | 'merged'
    reasoning          TEXT NOT NULL DEFAULT '',  -- why -- same role as pulsar_daily_trace.diff_reasoning
    resulting_star_id  INTEGER REFERENCES stars(id),
    created_at         DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);
```
Populated as a side effect of `create_star`/`update_star` tool calls (see "Weaver's tools" below),
not by a separate logging step. No `cost_usd` here — cost isn't attributable per candidate once
Weaver is one continuous agentic loop, only per completion call (see `shooting_star_events`).

**`shooting_star_events`** — a generic trace of *every* tool call and completion turn in a run,
not just the writes. Answers "is Weaver over-linking or under-linking in practice," the same
observability instinct `star_reviews` (below) applies to the confidence gate, and is also the real
cost-auditability record: one row per LLM completion call in the loop, whether that turn called a
tool or ended the run in plain text:
```sql
CREATE TABLE shooting_star_events (
    id         INTEGER PRIMARY KEY AUTOINCREMENT,
    run_id     INTEGER NOT NULL REFERENCES shooting_star_runs(id) ON DELETE CASCADE,
    -- tool: the five real tool names below, plus 'filter_pass' (the double-RAG
    -- pre-pass on a revisit) and 'final_answer' (the turn that ends the run).
    tool       TEXT NOT NULL,
    args       TEXT NOT NULL DEFAULT '{}',
    result     TEXT NOT NULL DEFAULT '',
    cost_usd   REAL NOT NULL DEFAULT 0,
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);
```

**`star_reviews`** — the human side of the observability story, sibling to
`shooting_star_runs`/`shooting_star_candidates` on the machine side:
```sql
CREATE TABLE star_reviews (
    id         INTEGER PRIMARY KEY AUTOINCREMENT,
    star_id    INTEGER NOT NULL REFERENCES stars(id) ON DELETE CASCADE,
    action     TEXT NOT NULL,             -- 'approved' | 'refined' | 'discarded'
    correction TEXT NOT NULL DEFAULT '',  -- the free-text typed, for 'refined'; '' otherwise
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);
```
Scoped to Inbox review only — an Edit-star correction on an already-*confirmed* star (see below)
does not write here. The confidence-gate tuning question ("is Weaver flagging too much or too
little, and once something's in the Inbox is the person mostly just confirming it, or routinely
reaching for Refine/Discard") is fully answered by `star_reviews`' `action` distribution alongside
`shooting_star_candidates`' `confidence_class`/`decision` — an ordinary maintenance edit to a star
nothing was ever unsure about doesn't add signal to that question.

## Weaver

One `agent.Run` per shooting star, with its own narrow tool set. Go code does exactly one thing
before the loop starts — assemble the task text — and everything after that is Weaver's own
reasoning and tool calls: deciding what's worth noting, whether it matches something existing,
and whether it's worth linking. No forced Go-orchestrated stages.

### Thread eligibility

A thread isn't "new or already processed" — it's "does it have content Weaver hasn't seen,"
because threads get revisited weeks later with no archive/close concept to lean on. Gates:

- **Idle-timing gate**: a thread is only a poll candidate once it's been quiet for at least
  `poll_interval_minutes` — unrelated to whether it's ever been seen before, just "don't grab a
  conversation mid-thought."
- **Delta gate**: has *this specific* thread produced messages Weaver hasn't seen — current
  `MAX(messages.id) WHERE thread_id = ?` compared against the most recent
  `shooting_star_runs.last_message_id_seen` for that thread (or no prior run at all). A thread
  reopened two weeks later behaves identically to a brand-new thread the first time it goes idle
  again — no special case, `shooting_star_runs` just accumulates another row.
- **Retry gate**: if that thread's most recent run has `needs_retry = 1` (see "Failure and retry"
  below), it's eligible regardless of the delta gate — a failed attempt doesn't get to hide behind
  "no new messages."
- **`threads.disabled = 1` is excluded outright** — a disabled thread is never fed to Weaver, full
  stop, not considered at all. If a thread gets disabled *after* Weaver already processed it,
  nothing retroactively happens to whatever stars it already contributed to — that content already
  exists independently in Constellation.

**Sequential, never concurrent.** Shooting stars run one at a time, never in parallel — deliberate,
not a missing optimization. Each run's writes commit to the DB before the next run starts, so run
N's own `search_stars` retrieval naturally sees whatever run N-1 just wrote, which is what actually
prevents two threads in the same backlog from independently creating duplicate stars for the same
emerging topic. Running concurrently would reintroduce exactly that race for no benefit, since
nothing here needs cross-thread reasoning within one poll — and deliberately no "finishing sweep"
pass afterward to reconcile duplicates a parallel run might have created; sequential execution
means that reconciliation step is never needed in the first place.

**Failure and retry.** `shooting_star_runs` gets a `needs_retry INTEGER NOT NULL DEFAULT 0` column.
Any error during a run — a hard failure or hitting the turn cap (below) — sets it, along with a
human-readable `error`. The next poll tick retries that thread via the retry gate above,
unconditionally, for as long as `needs_retry` stays set — there's no backoff or retry limit; a
thread that keeps failing keeps getting attempted every poll until it succeeds or the underlying
problem (a bad prompt, a flaky provider) gets fixed. `needs_retry` clears the moment a run for that
thread finishes successfully.

**Shutdown drain.** Each shooting star registers with the gateway server's own shutdown-drain
tracking (`TryStartTurn`/`FinishTurn` in `gateway/server.go`) exactly the way a Pulsar pulse already
does — see `gateway/constellation_weaver.go`'s `turnGate`. Without this, an ordinary `polaris
restart`/`polaris update` could kill a shooting star mid-write instead of waiting (up to the same
grace period a live chat turn gets) for it to finish cleanly first. This only applies to the two
callers that actually run inside the long-lived `polaris run` process — the scheduler's own tick and
the Docker-mode HTTP backfill handler; the bare-metal CLI's own `polaris constellation backfill`
invocation is a separate, one-shot process with no drain to register against, so it passes a no-op
gate instead.

**Stale-run sweep.** A shooting star that's still "in flight" (`finished_at IS NULL`) 30 minutes
after it started — comfortably longer than any real run should take — is presumed to belong to a
process that crashed, ran out of memory, or was force-killed (bypassing the drain above entirely,
e.g. a `docker compose up --force-recreate` that didn't wait), and gets closed out as a failure
needing retry by `store.Store.MarkStaleShootingStarRunsFailed`. Run once per scheduler tick
(regardless of whether Constellation is currently enabled) and once at the start of every backfill,
so a thread orphaned this way self-heals within a minute or two instead of being permanently stuck
(the delta gate would otherwise never re-offer it, and `HasInFlightShootingStarRun` — the Docker
update watcher's own busy check — would report `busy: true` forever).

### Backfill

Turning Constellation on for the first time doesn't mean the library only starts accumulating from
that moment forward — every thread that predates enabling it is eligible too (none of them have
`shooting_star_runs` rows yet, so the normal eligibility logic already covers them without any
special case). What it needs is a way to run through a real backlog (160+ threads on the potato as
of this writing) without waiting for the once-an-hour poller to trickle through it one tick at a
time. **A dedicated one-time CLI command** (`polaris constellation backfill`, matching the
`cmd/*.go` convention), not a UI affordance — this happens once per install, ever, so it doesn't
earn a permanent place in the settings panel. Processes every eligible thread sequentially, exactly
the same Weaver pipeline the poller uses, just triggered manually and back-to-back instead of
gated by idle-timing (a historical thread is definitionally not "mid-conversation"). No fixed
time estimate — however long a full sequential pass over the real backlog actually takes is the
answer, not a guess made ahead of running it.

### Revisiting a thread

Input prep is genuinely different on a first pass vs. a revisit — a discrete Go-orchestrated step
before Weaver's loop starts, not a tool call:

- **First-ever pass on a thread**: no prior notes exist, so there's nothing to filter *for* —
  "what's worth remembering here" is open-ended. Weaver gets the thread's raw content
  (`store.ReadThread`, `store/store.go:1213` — already reconstructs a thread's full effective
  transcript) directly, no filter pass.
- **A revisit** (prior notes exist — whatever `stars`/`shooting_star_candidates` rows this thread
  already produced): fetch only the delta (`messages WHERE id > last_message_id_seen` — old turns
  never get re-read, no matter how many times the thread gets revisited over its lifetime), then
  run `tools/web_read.go`'s `filterExtractedText` (a small, cheap second LLM call that extracts
  only what an instruction asks for — its own doc comment calls it "the double RAG step," and
  `search_chats`' `read` action already applies it to a past thread) on that delta, with an
  instruction built from the prior notes: *"Check for updates on: [prior star titles + one-line
  summaries]. Flag anything that updates, corrects, or adds to those, plus anything genuinely
  new."* The *condensed result*, not the raw delta, becomes Weaver's starting task text. **This
  mechanism only unlocks starting on the second (or later) pass over a thread** — a first pass
  never gets it, by design.

### Weaver's tools

Own narrow catalog (`tools.Register`-style), gated via `requires: weaver_run` — the same mechanism
`finalize_daily_items` uses to stay invisible outside its own context (`requires:
pulsar_daily_items`) — so these sit in the existing catalog/registry machinery without ever being
offered on a normal chat turn.

- **`search_stars(query)`** — FTS5 over `stars_fts`, returns up to 10 matching `star_id`/
  title/summary. Explicitly keyword, not semantic — matches shared words, not paraphrase. Does
  dedup-checking *and* link-discovery duty, same tool, Weaver decides which question it's asking.
  A result here is a lead, never enough on its own to decide a match or a link.
- **`read_star(star_id)`** — the full card (title, category, tags, status, confidence, summary,
  body). Mandatory before `update_star` or `link_stars`, never optional — a model judging a
  connection or a match from a title+summary snippet alone has no way to be careful; one that can
  actually read the candidate does.
- **`create_star(title, category, summary, body, tags, confidence_class, is_personal)`** — writes
  a new `stars` row and the matching `shooting_star_candidates` row (`decision = 'new_star'`) as a
  side effect of the call itself. `category` is free text — no fixed list; reuse a category
  already in use for the same general area rather than inventing a near-duplicate.
- **`update_star(star_id, summary, body, tags, confidence_class, is_personal)`** — merges into an
  existing star (rewrite to read as one coherent, current entry, never append), same side-effect
  logging (`decision = 'merged'`).
- **`link_stars(star_id_a, star_id_b, reasoning)`** — writes `star_edges`, idempotent (no-ops if
  the pair's already linked). **No cap on how many links a run can create** — quality is a
  prompting problem, backstopped by `read_star` (so a careful decision is *possible*) and by full
  observability making an over-linking prompt visible in the data, not a numeric ceiling.
  `reasoning` is accountability, not documentation — a vague reason ("both mention technology") is
  itself a signal the link probably shouldn't be made. Distinct from a merge: these are two
  *different* topics that relate, not the same topic under two names.

`agent.Run` ends naturally when Weaver stops calling tools and returns plain text — no forced
"finalize" tool, unlike `finalize_daily_items`/`finalize_pulsar_prompt`. The system prompt asks
for one or two plain sentences summarizing what happened when it's done, and that plain text *is*
`shooting_star_runs.summary` — the run's closing wrap-up doubles as both its natural termination
and its human-readable trace entry.

**Turn cap: 25.** A real conversation thread is very unlikely to need Weaver's loop to run longer
than that, so this is a backstop, not an expected limit — same idea as `config.MaxAgentTurns` for
the main chat agent. Hitting it forcibly ends the run with `error = 'max_turns_exceeded'` and sets
`needs_retry` (see "Failure and retry" above), and is counted separately in Constellation's own
usage stats (a `MaxTurnsCount`, mirroring the main Usage panel's existing "Ran out of turn budget"
row) rather than folded into a generic error count — a thread that's genuinely too complex for the
current prompt is a different signal than a transient API failure, worth telling apart at a glance.

**Deciding "is this the same topic as something existing" happens inside Weaver's own reasoning**
(via `search_stars`/`read_star`) before it ever calls `create_star` or `update_star` — there's no
separate "ambiguous match" case. Genuine uncertainty about a match just means reaching for
`confidence_class = 'fuzzy'` on whichever call it makes.

### Calibration

`create_star`/`update_star`'s bar is **not** "default to writing nothing." Stars aren't injected
into every future turn's context the way memories are (`tools/descriptions/memory.yaml`'s own
"default to NOT writing" posture exists *because* of that continuous cost) — a mediocre star just
sits unopened in a browsable library, costing nothing per turn. Two judgment calls actually
matter, different from "should this exist at all":

1. **Is this the same topic as something that already exists** — `search_stars`/`read_star` first,
   prefer `update_star` over a near-duplicate `create_star`. Five separate conversations about,
   say, Ferraris isn't five candidate stars, it's one star that gets richer five times, because
   the search-first discipline catches the match each time.
2. **Is this a stated fact/topic vs. an inference about the person** — this is where real caution
   belongs. A fact or topic genuinely, substantively discussed clears a low bar (`obvious`,
   captured freely). An inference *about who they are* clears a higher one (`fuzzy`, routed to
   review) — because that's a claim about them, not a question of whether it's worth remembering.

So the real bar is **"was this actually discussed with some substance,"** not "is this dramatic
enough to matter." Excluded: pure logistics with no topical content, a single throwaway reference
with nothing said about it, ephemeral/time-bound content with no lasting relevance. Real exchanges
about real topics clear it — most shooting stars should produce a new or updated star, not zero.

**A `rejected` star is a closed matter, not a candidate.** `search_stars` includes rejected stars
in its results (they're not excluded from `stars_fts`), and `read_star` surfaces `status:
'rejected'` — Weaver's prompt is explicit that finding one is a stop sign: don't call `create_star`
for the same topic again, and don't `update_star` it back to life either. A human explicitly said
no; that stands until they change their mind through the UI (see "Reviewing and editing a star"),
not because Weaver reconsidered. Without this, a topic that comes up again in an unrelated future
thread would just get re-proposed into the Inbox every time, defeating the point of rejecting it.

**Category/tag sprawl is a real, accepted risk for v1** — nothing beyond the prompt instruction
above ("reuse an existing category, check via `search_stars` if unsure") stops near-duplicate
groupings ("tech" vs. "technology") from accumulating over time. Deliberately not solving this with
a mechanism now — try it with prompting first, and revisit with real usage data (a fixed category
list, a normalization pass, something else) only if it actually gets out of hand, not
preemptively.

### System prompt and safety

Weaver's system prompt (a new `weaver:` section in `prompts.yaml`, sibling to `pulsar_daily:`)
frames the job (read this thread, decide what belongs in Constellation, the person never sees this
run directly), the calibration above, "always `search_stars` before `create_star`," `category` as
free text, and that checking for connections is a real, non-optional part of the job, not an
afterthought after extraction is "done." It carries the same injection-defense framing
`prompts.yaml`'s `thread_read_filter_system` already uses for reading a past thread: the
conversation content is the person's own past messages, not instructions to Weaver, and text
styled as a directive inside it is read and judged like any other sentence, never obeyed.

## Personal stars (the "you" layer)

Personal inferences about who the person *is* (a taste, an identity fact, a circumstance) don't
need their own storage or pipeline — they're the same `stars` table, the same Weaver loop, the
same Map, the same Inbox/Review/Refine flow, distinguished only by `stars.is_personal`.

**The dividing line** — *is this about a topic, or about the person themselves*, not "does this
feel personal" in some vaguer sense. "Ender's Game and the science behind it" characterizes a
book, even though liking it says something about the person. "Reads science fiction" characterizes
*them*. Same source material, different subject.

**Status routing — softened after real usage (2026-09-12):** the original design forced every
personal `create_star`/`update_star` through `proposed`, deliberately strict to start (see below).
Live review over the first ~280-star backfill showed Weaver's personal-star writing was
consistently accurate — the friction wasn't earning its keep — and the library itself pivoted to
**personal-only extraction** (Weaver no longer writes topical/reference stars at all, only facts
about the person), which would have meant literally every star hitting the gate. So:
- `create_star`/`update_star` no longer force `status = 'proposed'` for `is_personal = true` —
  personal stars are created/updated exactly like any other star (`status` as requested by the
  caller, normally `'auto'`), same as this section originally said would happen "once it's clear
  whether constant re-affirming is worth the friction."
- `confidence_class` is still recorded on every personal star (stated directly vs. inferred, shown
  on the Star detail screen); it just no longer needed a separate "it doesn't drive routing"
  caveat, since nothing does anymore.
- The Inbox/Review/Refine flow (below) still exists for the rare star a human wants to hand-correct
  or reject — it just isn't the default path for new personal stars anymore.

Everything else needs zero new design: `link_stars` works completely unmodified (a personal star
can be the hub several topic stars connect to — "reads science fiction" linked to "Ender's Game
and the science behind it" is an ordinary link), and `star_reviews`/Inbox/Review/Refine all work
unmodified — a personal star is just a proposed star in the same queue.

**Visual treatment**: a third accent color, `--color-personal` (a soft violet, sitting between the
existing warm gold and cool blue), applied as a tinted tile/border plus a text badge — no icon
(an icon+badge combination read as cluttered once compared side by side in
`mockups/constellation-personal-star-options.html`, which mocked up five options before this one
was picked). On the Map, a personal star's node gets the same tint, with no separate cluster
(`is_personal` is orthogonal to `category`, so it sits wherever its topic naturally clusters), and
a violet-tinted connecting line where it links to a topic star.

**In the Library, personal stars get their own collapsible group, separate from the category
sections.** Given this is a single-operator tool, interleaving personal stars into the regular
category sections would have been fine — there's no second person browsing who'd need them kept
apart — but a dedicated group reads better for continuity: an "About you" collapsible section,
alongside the regular category sections (themselves also individually collapsible), rather than
personal stars quietly scattered one-per-category among the rest.

## Reviewing and editing a star

Two related but distinct interactions, both deliberately reusing the same free-text,
LLM-reconciled shape rather than a form or field editor — reviewing or correcting a star should
feel like a conversation with Weaver, not editing a record:

**Edit star** — a direct, one-off correction to an already-*confirmed* star's content, without
spinning up a new chat thread. The user types a correction/addition in their own words ("actually
I finished this one, wasn't just researching it"); Weaver runs a one-off reconciliation pass that
folds it into the star's actual fields. This will never grow into a structured form — the whole
point is that it shouldn't feel like there's frontmatter sitting under the star at all. An edit is
not a special case for the dedup/merge pipeline — it's just a new signal, no separate "edit path,"
no versioning beyond the normal write (see `star_reviews` above for why this stays unlogged).

**Reviewing a proposed star** — three actions, not two. A flat approve/discard binary throws away
exactly the input that makes a low-confidence star interesting to review: Weaver usually gets
*part* of it right.
- **Approve** — confirms the star as-is.
- **Discard** — soft-rejects it (`status = 'rejected'`).
- **Refine** — the middle option, visually the same composer sheet as Edit star, but asking a
  two-sided question ("what did it get right, what was wrong") instead of Edit star's one-sided
  "what's wrong or what to add." Sending a refinement both corrects the star *and* resolves the
  review in one step — there's no separate confirm-after-refine tap, since providing the
  correction already is the human decision point.

The Review screen also surfaces `shooting_star_candidates.reasoning` directly as a **"Why this
needs a look"** block — the same sentence Weaver already logs for the trace tables, put in front
of the person who actually has to make the call, not just kept for debugging.

**Rejected stars get their own collapsible Library section**, same idea as the "About you" group —
collapsed by default, since a discarded topic isn't something to browse day to day, but reachable
without hunting for it. Each card gets a **Restore** button, moving the star back to `confirmed`
(not back to `proposed` — a human deciding "actually I do want this" is a direct decision, not a
new Weaver proposal needing re-review). This is also what makes rejecting something low-stakes:
"no" isn't permanent unless it's left alone.

**A star's own overflow menu** (mirroring the existing per-thread overflow menu) covers manual
housekeeping that isn't a Weaver-mediated correction at all: renaming the title directly (no LLM
involved, unlike Edit star — just a plain text field, the same way a thread gets renamed), and
**Disable** — a star the person no longer wants in their library, distinct from rejecting one
Weaver got wrong (`stars.disabled`, soft-delete, same shape as `threads.disabled`; independent of
`status`, so a `confirmed`, entirely correct star can still be disabled if it's just not wanted
anymore).

**Continue in chat** — a third, older interaction (predates Edit star/Refine): a pinned button on
Star detail that starts a fresh chat pre-loaded with a reference back to that star (an
attachment-ID-style reference dropped into the omnibox, not a fully retyped question), for when
reading a star surfaces a real follow-up question rather than a correction.

## The UI

`mockups/constellation.html` — ten phone-width screens, "Option D": A's grouped-by-category
library structure rendered with C's card polish, B's constellation/reflection-layer visualization
folded into a compact panel rather than competing for the home view. (Full comparison of the three
original directions — A "The Index," B "The Constellation," C "The Digest" — and why D combines
them, lives in mockup review notes; the shipped direction is what's described below.)

- **Library** (default/home view) — grouped by category (Technology, Books & Ideas, Science &
  History, ...), an elevated card per star (icon tile, title, one-line summary, tags, chevron). A
  slim "This week" digest banner and an Inbox ("N stars proposed") banner sit above the grouped
  sections, which are each independently collapsible. An "About you" collapsible group (personal
  stars) and a collapsed-by-default "Rejected" collapsible group (with a Restore button per card)
  sit apart from the regular category sections. Auto-vs-proposed stars are visually distinguished
  (a status badge, a dedicated Inbox banner) rather than shown inline, undifferentiated, in the
  same list.
- **Star detail** — title/tags/confidence/status fields, reading-first body prose (serif headings,
  matching the app's typography convention), a **"Linked articles"** block listing the originating
  thread(s) (`star_sources`), and a **"Nearby in the constellation"** sub-section — a compact,
  bounded reflection-layer visualization (a small starfield panel with 2-3 connected nodes) rather
  than a flat "related stars" chip list. "Continue in chat" and the Edit-star trigger are pinned
  bottom buttons; an overflow menu (rename, disable) sits in the top bar.
- **Map** — the full star-map view: topic clusters as star nodes, reflection-layer links as
  connecting lines. A real, full screen, not a bottom sheet — reached via a Library/Map tab bar so
  it's a deliberate, opt-in exploration mode rather than competing with the list for the home view.
- **Edit star** — the free-text correction sheet, a chat-composer-style box over a dimmed Star
  detail backdrop.
- **Inbox** — the list of proposed stars awaiting review, each card showing a short excerpt of why
  Weaver flagged it rather than a confident summary.
- **Review star** — one proposed star's reading view plus the "Why this needs a look" block and
  the three-way Approve/Refine/Discard action bar.
- **Refine** — the two-sided correction sheet, same visual pattern as Edit star.
- **This week** — the digest banner's tap-through screen.
- **Constellation settings** — its own settings-panel section, same shape as Pulsar's and Pulsar
  Daily's own sections: the on/off toggle, poll interval, and the model picker ("Same as chat
  (default)" plus an explicit override).
- **Constellation Usage** — the dedicated cost/observability panel described below, reached from
  Constellation's own `Info` icon (and via a discoverability link from the main Usage panel).

## The weekly digest

The Library's digest banner ("This week: 4 new, 1 link surfaced — Ender's Game → child
psychology") is, in v1, **just counts plus one concrete example — zero LLM calls**:

- **"N new"** — `COUNT(*) FROM stars WHERE created_at >= now - 7 days`.
- **"N links surfaced"** — `COUNT(*) FROM star_edges WHERE created_at >= now - 7 days`.
- **The highlight line** — the most recent `star_edges` row this week, rendered
  `{star_a.title} → {star_b.title}`; falls back to the most recent new star's title if no links
  happened; **the banner doesn't render at all if both counts are zero** — same "no signal, no
  output" instinct the poller itself runs on.

Pure query against tables Weaver already writes, computed live on Library load — no scheduler, no
cadence-check, no new trace table, no cost.

**Tapping the banner → "This week"**, a reverse-chronological feed of exactly what the banner is
summarizing — everything actually *made* in the last 7 days: new stars, updated (merged) stars,
and new links. Deliberately excludes review actions (approve/discard) — those are resolutions of
something already made, not new material themselves. Reuses the existing star-card visual
language in a flat list, each row tagged "New"/"Updated"/"Linked" with a relative timestamp.

**V2, not designed now**: a real synthesized-prose version ("you keep circling back to space and
to where you might live"). Floated shape: a second, distinct Weaver-style pipeline — given the
week's stars as context, either read tool-by-tool the same way the main Weaver loop reads stars,
or one larger context injection of everything that week at once, plus its own cadence-check
(`last_weekly_digest_at`, same `isDailyDue`-style pattern Pulsar Daily already uses). Which shape
is right is real design work for later.

## Cost tracking and observability

Constellation's spend is explicitly **not** Polaris spend — not a fourth bucket in the existing
`store.Stats.CostBySource` (which splits Polaris/Pulsar/Daily), a wholly separate surface. Pulsar
and Pulsar Daily's costs are folded into that breakdown because they're close in shape to the core
chat system (real threads, or a daily edition of the same kind of generation); Weaver isn't either
of those.

**Backend**: `store.ConstellationStats` (sibling to `Stats`) + `GetConstellationStats(periodDays
int)` — same "aggregate on demand from tables, no running counters, no second source of truth to
keep in sync" philosophy `GetStats` already uses, pointed at Constellation's own tables:
- Cost (period + all-time) — `SUM(shooting_star_events.cost_usd)`.
- Shooting star count, star counts by status (auto/proposed/confirmed/rejected).
- Tool call counts by tool, from `shooting_star_events` `GROUP BY tool` — scoped to the five real
  tools only (the cost sum includes `filter_pass`/`final_answer` too, but the call-count breakdown
  doesn't, same distinction `Stats.SearchProviderCounts`' own doc comment draws between "what
  actually answered" and "what was billed").
- Review action counts (approved/refined/discarded, from `star_reviews`) — the confidence-gate
  tuning signal, finally surfaced somewhere visible instead of "a query someone runs."
- Links created count.
- `MaxTurnsCount` — `COUNT(*) FROM shooting_star_runs WHERE error = 'max_turns_exceeded'`, mirroring
  the main Usage panel's existing "Ran out of turn budget" row. Counted separately from other
  errors: a thread that's genuinely too complex for the current 25-turn cap is a prompt-tuning
  signal, not the same thing as a transient API failure.
- `needs_retry` count — how many shooting stars are currently waiting on a retry, at a glance.

**Route**: `GET /api/constellation/stats` (`gateway/constellation_routes.go`).

**Frontend**: Constellation's own settings section gets its own `Info` icon (same idea as the
existing "Usage stats" icon) opening its own "Constellation Usage" panel — a separate component
instance, separate data fetch, never touching `Stats`/`CostBySource`. One discoverability bridge,
not a data merge: the *main* Usage panel gets a small link ("→ Constellation usage") that
navigates into it, same sibling-panel-state pattern `SettingsPanel.svelte` already uses for
`showStats`/`showMemory`/`showMemoryImport`.

**Auditability**: cost only exists at the level of each individual LLM completion call inside
Weaver's loop (`agent/driver.go` already accumulates this internally — `totalCost += resp.CostUSD`
once per turn — it just never surfaced anything but the final sum before). `shooting_star_events`
logs one row per completion call, not just per tool call — every turn produces a row, whether it
called a tool or ended the run in plain text (`tool = 'final_answer'`), and the double-RAG filter
pass on a revisit gets logged the same way (`tool = 'filter_pass'`). `shooting_star_runs.cost_usd`
is therefore a cached rollup — `SUM(shooting_star_events.cost_usd) WHERE run_id = ?` — never a
number with nothing itemized behind it. Every dollar traces to a specific completion call, in
order, with what it produced.

## Where this would live

- `gateway/constellation_scheduler.go` — the poller, same once-a-minute ticker shape as
  `pulsar_scheduler.go`.
- `gateway/constellation_weaver.go` — task assembly, the `agent.Run` call, the tool handlers.
- `gateway/constellation_routes.go` — API routes, including `GET /api/constellation/stats`.
- `tools/search_stars.go`, `read_star.go`, `create_star.go`, `update_star.go`, `link_stars.go` +
  matching `tools/descriptions/*.yaml` files.
- A new `weaver:` section in `prompts.yaml`, sibling to `pulsar_daily:`.
- `store/constellation.go` — the schema above, plus `ConstellationStats`/`GetConstellationStats`.
- `cmd/constellation_backfill.go` (or similar) — the one-time backfill command.

Same package and naming convention as Pulsar and Pulsar Daily throughout — not a separate
top-level package.

**Scheduler startup**: launched exactly where `RunPulsarScheduler` already is —
`cmd/run.go` starts it as its own goroutine with its own shutdown-drain channel, same pattern as
`go backup.RunScheduler(...)` and `go srv.RunPulsarScheduler(pulsarDone)` a few lines above it:
`go srv.RunConstellationScheduler(constellationDone)`.

## Deferred to v2

- **Saved links** — a second input source beyond chat threads: directly handing the system a URL
  ("save this for later") that gets fetched and run through the same inference pipeline as "a
  thing the user is into," independent of whether it ever came up in a chat. Real and wanted, not
  designed as part of this v1 pass — would also mean the poller's "anything new to process?" check
  covering saved-links-since-last-check, not just new threads.
- **Synthesized-prose weekly digest** — see "The weekly digest" above.

## Open questions for implementation

- Real migrations + Go store methods for the schema above (`store/store.go` conventions).
- The five tool implementations, their `tools/descriptions/*.yaml` files, and the `weaver:`
  `prompts.yaml` section — all written in prose here, none as real files yet.
- A CLAUDE.md Docker/bare-metal dual-support checklist pass, covering both the scheduler (likely
  fine — no-external-cron, same shape as Pulsar) and the new `polaris constellation backfill` CLI
  command specifically (does it need the `isDockerComposeInstall`/thin-HTTP-client treatment
  `cmd/docker_client.go` already establishes for other commands that touch a running install) —
  neither has been explicitly checked.
