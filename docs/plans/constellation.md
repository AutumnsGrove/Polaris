# Constellation — very early brainstorm, not scoped yet

**Status: full v1 design — schema, Weaver's own architecture and prompts, the review UI, and every
mockup screen settled. Implementation still not started.** This came out of a live brainstorm with
Polaris itself (see the "Polaris Usage Trends and Recent Queries" thread on the potato,
2026-09-09/10 — ask to search past chats for it if this doc needs the full transcript again) after
using the newly-shipped `search_chats` tool to ask "what do I actually use you for?" Fifteen passes
now, in order below; the short version: passes 1-5 worked out the core shape, 6 picked the UI
direction (`mockups/vault.html`), 7-8 settled naming and the Edit-star flow, 9 sketched the schema
and dropped a category-gated rollout for a global on/off, 10 designed the Inbox review flow, 11-13
designed and wrote Weaver's actual architecture (one real agentic loop, not staged calls) and its
tools/prompts, 14 pulled the "you" layer into v1 as a single `is_personal` flag, and 15 scoped the
weekly digest banner down to a plain query for v1 (real synthesized prose is v2). "Resolved in a
brainstorm" and "a schema sketch" still aren't the same as "designed and ready to build" — the next
real step is turning this into real migrations and code, running it against real data, and watching
`shooting_star_events`/`star_reviews` to see whether the prompting actually holds up.

## Naming (settled — seventh pass, issue #45)

This doc's early passes below were written under working names ("vault," "chapter," "the DOT
agent," "a job") before the real names existed. Left as-is for brainstorm-log fidelity, but the
real vocabulary going forward is:

- **Constellation** — the feature itself. Was "Knowledge vault" / "Vault" earlier in this doc.
- **Star** — a single topic/chapter (what earlier passes below call a "chapter"). Also the literal
  visual metaphor the chosen UI direction (Option D, sixth pass) already uses on its Map screen:
  topics plotted as star nodes, clustered by category, with reflection-layer cross-links drawn as
  connecting lines between them.
- **Shooting star** — one background processing run (earlier passes' working name "a job," itself
  mirroring Pulsar's own "pulse"): one isolated, fresh-context run through a single new thread.
- **Weaver** — the agent that does the reading/inferring and writes/merges stars (earlier passes'
  placeholder "the DOT agent"). Draws the connecting lines between stars (the reflection layer) and
  weaves chat *threads* into the constellation. Settled after a naming brainstorm alongside
  "Cartographer" (map-the-scattered-points framing) and a few astronomy-proper-noun options (Argus,
  Almagest) — Weaver won on the thread/weaving pun and reads well next to Polaris/Atlas/Pulsar.

When reading "vault"/"chapter"/"the DOT agent"/"a job" below, mentally substitute
Constellation/star/Weaver/shooting star — the doc's history isn't being rewritten term-by-term,
just annotated here so the vocabulary shift is explicit.

## The core idea

Polaris auto-generates and maintains a personal knowledge library built out of everything asked
across every thread, organized by **topic, not by chat**. Ask about Cloudflare Workers four times
across four unrelated threads, and that collapses into *one* living chapter (a **star**) that grows
over time, not four scattered notes. Inspired by DOT (New Computer) — specifically the "it
remembers you and reflects it back" feel, not just a notes dump or a passive transcript log.

**"Vault" and "Obsidian" are inspiration/reference points, not the literal target.** This is its
own thing, native to SQLite — chapters (stars) live in the database (frontmatter-equivalent fields
as real columns: title, status, confidence, sources, tags, etc., not literal YAML) so the agents
doing the writing/merging can update them the way any other Polaris data gets updated, not by
parsing and rewriting Markdown files on every change. Obsidian-style Markdown-with-frontmatter is a
target *export format* — "assemble into `.md` on demand" — for whenever the user actually wants a
real, portable vault on disk, not the system's own storage. Same underlying principles (one note
per topic, linkable, frontmatter-carrying), different substrate. This also resolves the earlier
"how does this touch a vault on disk" question from the first pass — there's no vault-on-disk to
touch in v1 at all, just database reads/writes, same shape as every other Polaris subsystem.

## Shape that came out of the brainstorm (not decided, just sketched)

- **Hybrid save model**: obvious/factual/self-contained stuff (how Cloudflare Workers work, what
  Surveyor 1 was) auto-saves with no friction. Anything that's an inference *about the user*
  (reading tastes, location plans, mood) or otherwise low-confidence gets proposed instead —
  never auto-written, always a yes/no.
- **Structure sketch** (now a DB shape, not literal folders — see "SQLite, not a real vault"
  above; folder names below are really just status/category groupings a chapters (stars) table
  would filter on, and become real folders only at export time): chapters are either **auto**
  (written directly) or **proposed** (awaiting approval, the DB equivalent of an "Inbox"), plus
  some higher-level table-of-contents grouping ("Atlas"-equivalent) over the chapters themselves.
  Each chapter carries frontmatter-equivalent columns: `title`, `type`, `created`/`updated`,
  `status: auto|proposed`, `confidence`, `sources` (links back to the originating thread(s)),
  `tags`.
- **A thin "reflection layer"** — cross-links between chapters that surface a connection the user
  didn't ask for (Ender's Game → child psychology → ethics), and/or a weekly digest note
  ("this week you learned about X, Y, Z; you keep circling back to space and to where you might
  live"). This is the most DOT-flavored, most speculative part — explicitly optional/deferrable.
- **A private "you" layer** (deferred) — the loose personal inferences (reads literary sci-fi,
  part of Atlanta's queer community, weighing a move) stay local and are always proposed, never
  auto-written, given how sensitive that layer is.

## Decided so far (second brainstorm pass)

These firmed up enough to move out of "open questions" — still nothing built, but the shape is no
longer up for grabs on these specific points:

- **Not MCP. A totally separate agent, in a totally separate environment, with a totally
  different, narrow tool set.** This isn't Polaris's own `agent.Run` loop gaining a new tool —
  it's a distinct agent (Weaver) that never needs `web_search`, `calculator`, `visualize`, or
  anything else in the main catalog, because its whole job is reading/inferring from already-
  written chat content, not researching or computing anything new. Its tool set is closer to "read
  a thread, read/write the constellation, maybe search the constellation for an existing related
  star" — nothing else.
- **Trigger model: an interval-based background poller, not live-per-message and not an
  unconditional nightly job.** Something like "check every hour: any new thread(s) since last
  check?" If yes, Weaver runs a shooting star through them. If no — **zero AI calls, full stop,
  silently skipped.** This is a hard requirement, not a nice-to-have: it directly guards against a
  real failure mode from a past project (`her-go`, a Go program simulating "Samantha" from *Her*)
  whose nightly "dream sequence" ran unconditionally every single night regardless of whether
  anything new had actually happened that day — burning real tokens for zero informational gain,
  every night, forever. This system is explicitly designed to never do that: no new input since
  last check means no LLM call is made at all, not even a cheap one.
- **A new, dedicated UI surface — not the existing thread/chat UI at all.** Described as wanting
  something "nice, slick, card-like," entirely fresh for this project, explicitly not reusing the
  thread-list/chat-transcript visual language. Framed as wanting this to feel like an actual
  second brain, a different kind of surface from "a chat you had."
- **A second input source beyond chat threads: manually saved links.** Not just things inferred
  from conversations — a way to directly hand the system a URL ("save this for later") that gets
  fetched and run through the same inference pipeline as "a thing the user is into," independent
  of whether it ever came up in a chat at all. This means the poller's "anything new to process?"
  check needs to cover saved-links-since-last-check too, not just new threads. **Explicitly held
  for v2** (thirteenth pass) — real, wanted, not being designed as part of this v1 pass.

## Decided so far (third pass — dedup/merge)

- **Dedup/merge pipeline**: (1) one LLM call reads a new thread's effective content and proposes
  0–N candidate topics (title + short summary + confidence: obvious/factual vs. fuzzy/personal —
  zero candidates is a valid, common outcome, not an error); (2) for each candidate, a cheap FTS5
  retrieval prefilter over existing chapters' (stars') title/tags/summary (the same mechanism
  `search_chats` already built, applied to a `chapters`/`stars` table instead of `messages`) pulls
  back the top handful of plausibly-related existing chapters; (3) one LLM call per candidate,
  given the new candidate plus those few existing chapter summaries, both decides *and* produces
  the merged result in the same call ("no match → new chapter" or "matches chapter X → here's X's
  updated content"), rather than a separate decide-then-merge round trip. Deliberately FTS5, not
  embeddings, for the retrieval step — same reasoning `search_chats`' own v2 (semantic search)
  was deferred for: one person's own vocabulary doesn't drift enough from itself to need it, and
  it avoids a new table/indexer/Ollama dependency for the MVP. Revisit only if real usage
  surfaces FTS5 actually missing real matches, not preemptively.
- **Ambiguous matches always route to the proposed/Inbox queue**, never an auto-merge or
  auto-new-chapter guess — "not confidently a match" is treated the same as "fuzzy/personal,"
  since a wrong auto-merge (polluting an unrelated chapter) or a silent duplicate chapter is worse
  than asking once.
- **No per-poll cap on backlog size.** Considered capping how many candidate topics a single poll
  processes (deferring the rest to the next interval) to bound worst-case cost after a long gap,
  but rejected: deferring doesn't reduce total cost, it only delays it, so there's no real benefit
  for a system with no shared/contended resource to protect — it would just elongate the process.
  A poll processes everything it finds, every time.
- **One isolated, fresh-context processing run per new thread (one shooting star), run
  sequentially — not one continuous session walking the whole backlog, and not concurrent runs
  either.** Mirrors `agent.SpawnResearchers`' own reasoning for isolated sub-agent contexts
  (narrower context per unit of work beats one shared blob accumulating unrelated topics), but
  sequential rather than concurrent: each run's writes commit to the DB before the next run
  starts, so run N's own FTS5 retrieval step naturally sees whatever run N-1 just wrote — which is
  what actually prevents two threads in the same backlog from independently creating duplicate
  chapters for the same emerging topic, without needing a shared context or any extra coordination
  to get that self-correction. Running concurrently (Deep Research sub-agent style) would
  reintroduce exactly that race, for no benefit here since nothing needs cross-thread reasoning
  within one poll.

## Decided so far (fourth pass — where it lives, on/off, tone, reflection layer)

- **Working name for the execution unit: a "job."** (Now named for real: a **shooting star** — see
  Naming above.) Mirrors Pulsar's own naming (a routine fires a "pulse").
- **Lives entirely inside Polaris — same repo, same binary, same database.** Not a separate
  project or sidecar process; an extension of the search agent side of the app, using the existing
  SQLite store. Explicitly **not** expected to make the main chat agent itself any better at
  anything — this is a user-facing feature (a browsable library), not a capability upgrade for
  `agent.Run`. Worth remembering when judging whether a piece of it is "worth the engineering,"
  since the payoff is entirely in what the user gets to look at, not in smarter answers.
  Context for scale: the potato's real instance already has 160 real (non-disabled, opened)
  threads as of 2026-09-10 — a year of usage at anything like that pace is a genuinely large
  dataset for this to organize, which is the whole point of building it.
- **On/off: a dedicated settings panel section**, same shape as Pulsar's and Pulsar Daily's own
  settings sections — a plain global switch, not a per-tool toggle buried elsewhere.
- **Tone: factual, not a journal-with-a-take.** This is a library, and libraries state facts.
  Deliberately not verbose either — glanceable, not something to sit and read for a while, since
  the user already read the original thread once; a chapter should always link back to that
  original thread for anyone who wants the full context again.
- **A concrete new interaction, worth designing for even this early**: a "Continue in chat" button
  on a chapter (star) card. Reading a Cloudflare Workers chapter and suddenly have a follow-up
  ("how do Durable Objects work")? One tap starts a fresh chat pre-loaded with a reference back to
  that chapter (an attachment-ID-style reference dropped into the omnibox, not a fully retyped
  question) so the question can just be typed and sent immediately, without re-establishing
  context by hand.
- **The reflection layer is fully wanted, not a maybe.** Explicitly not being scoped down to "just
  a clean library" — the proactive, DOT-flavored cross-linking and weekly-digest behavior is
  something to actually build, not a stretch goal to quietly drop. Doesn't have to land in the
  very first narrow slice (see below), but it's a real target, not speculative padding.

## Decided so far (fifth pass — export)

- **Export turned out not to be a real design problem.** Frontmatter is a direct, mechanical
  rendering of already-structured DB columns (title, tags, chapter/type, etc. straight into a
  `---` block) — nothing to design there. The chapter *body* is already well-formatted Markdown
  the moment it's written, since the model authors it with real headers and wikilinks as part of
  producing the chapter content in the first place, not as a separate post-processing pass over
  plain text. Export is therefore pure assembly: take the stored structured fields, take the
  stored body, concatenate, done. No remaining open questions from this doc's brainstorm passes —
  the next real step is sketching a concrete schema/first-slice, not more open-ended ideation.

## Decided so far (sixth pass — UI direction)

- **Three initial directions were mocked up and compared** as phone-width UI mockups: "A — The
  Index" (an editorial table of contents, grouped by category, closest to Polaris's existing
  thread-list visual language), "B — The Constellation" (chapters as stars on a literal
  night-sky map, topic clusters, reflection-layer cross-links drawn as connecting lines), and
  "C — The Digest" (a DOT-style scrolling card stream, a factual "This week" digest pinned on
  top, swipe-style Inbox approval cards for proposed chapters).
- **Chosen: a fourth, combined direction ("Option D").** Mockup committed at
  `mockups/vault.html` (three screens: Library, Chapter detail, Map, in that file's own
  `#screen-*` sections) — this is the actual target for the eventual UI, not just one more idea
  still up for grabs. The mockup file itself hasn't been renamed to match the Constellation/star
  vocabulary yet — treat "vault"/"chapter" inside it as "constellation"/"star" until it is.
  - **Library** (default/home view): A's grouped-by-category structure (Technology, Books &
    Ideas, Science & History, ...) rendered with C's card polish — an elevated `chapter-card`
    row per chapter (icon tile, title, one-line summary, tags, chevron) instead of A's flatter
    list rows. A slim "This week" digest banner and an Inbox ("N chapters proposed") banner sit
    above the grouped sections.
  - **Chapter detail**: title/tags/confidence/status frontmatter fields, reading-first body
    prose (serif headings only, matching the rest of the app's typography convention), a
    **"Linked articles"** block listing the originating thread(s) (the chapter's `sources`
    field), and — new in this direction — a **"Nearby in the constellation"** sub-section: a
    compact, bounded revival of Option B's spatial reflection-layer visualization (a small
    starfield-textured panel with 2-3 connected nodes) in place of a flat "related chapters"
    chip list, without needing its own screen. "Continue in chat" stays a pinned bottom button.
  - **Map**: Option B's full star-map view (topic clusters as star nodes, reflection-layer links
    as connecting lines) survives as a real, full screen — not a bottom sheet, not the default
    view. It's reached via a **Library / Map tab bar** at the bottom, shared between the two
    top-level screens, so the constellation is a deliberate, opt-in exploration mode rather than
    competing with the list for the home view. Chapter detail is a drill-in from Library and
    does not carry the tab bar.
  - This settles the "structure sketch" section's open UI question above: the higher-level
    TOC/"Atlas"-equivalent grouping is literally the category sections in the Library view, and
    auto-vs-proposed chapters are visually distinguished (a status badge on the chapter page, a
    dedicated Inbox banner in the Library view) rather than shown inline, undifferentiated, in
    the same list.

## Where a start might look like, eventually (superseded — see ninth pass)

Floated during the brainstorm: don't build the whole thing — pick one narrow slice (just books, or
just tech), get auto-save + dedupe working for that slice alone, live with it for a week, then
decide whether to expand. **This category-scoped-rollout idea is dropped as of the ninth pass** —
see below. The reflection layer and the "you" layer staying out of v1 is unaffected by that change
— not because either is in doubt (see above, the reflection layer is a real target), just
sequencing.

## Editing a star (seventh pass — in progress, not settled)

Floated as an important gap this doc hadn't addressed: the auto-save/proposed model above handles
Weaver getting a fact right or wrong at write time, but not the user correcting a star *after* it
exists — the memory-panel precedent (a one-off "actually I like coffee more than tea" correction
that a one-off agent pass reconciles into the stored memory) is the shape to follow here too.
Two entry points sketched, both hung off a star:

- **"Continue in chat"** (already designed above, fourth pass) — the star's context gets injected
  into a fresh chat so the correction/follow-up happens conversationally, same as today's design.
- **"Edit star"** — a new, second option, parallel to the existing memory-panel edit flow: a
  direct, one-off edit/correction to the star's own content without spinning up a new chat thread
  at all.

**Decided (eighth pass):**

- **"Edit star" is free-text only, LLM-reconciled — never a field-level editor.** The user types a
  correction/addition in their own words ("actually I finished this one, wasn't just researching
  it"); Weaver runs a one-off pass that folds it into the star's actual fields. This will never
  grow into a structured form or a Markdown text box — the whole point is that it doesn't feel
  like there's frontmatter sitting under the star at all. It should feel like talking to
  Constellation, not editing a record.
- **Most of a star's fields are backend-only.** Some (title, tags, confidence/status badge, body)
  are shown in the UI per the Chapter-detail design above; the rest exist purely for
  categorization/sorting/retrieval (the FTS5-facing side of the schema) and are never surfaced to
  the user directly, edited or otherwise.
- **An edit is not a special case for the dedup/merge pipeline.** It's just a new signal, folded
  in through the same reconciliation Weaver already does for a new thread touching an existing
  star — no separate "edit path," no versioning/logging beyond whatever the normal write already
  gets. Simpler is correct here: this is corrective, low-frequency, low-stakes input, not something
  that needs an audit trail.

This closes out the open questions this pass started with. See `mockups/vault.html`'s new fourth
frame for a first sketch of the "Edit star" sheet UI (a chat-composer-style free-text box over a
dimmed chapter-detail backdrop, not a form).

### First narrow slice (seventh pass — superseded by the ninth pass, kept for history)

Revisiting "Where a start might look like" above with actual candidates instead of a placeholder
"books, or just tech": leaned toward **books** first (comes up regularly in conversation, and is a
clean, bounded category to prove auto-save + dedupe on), **music** second, then **technology**
third — flagged as likely the broadest/messiest of the three given how much general tech
discussion already happens here, so probably not the best first slice even though it's the richest
source material. **Dropped in the ninth pass**, once the schema conversation made the mechanism
this would have needed (a per-category gate) explicit enough to see the problem with it — see
below.

## Schema sketch (ninth pass)

First real schema pass, built to match `store/store.go`'s existing conventions (the FTS5
external-content-plus-triggers pattern from `messages_fts`, the singleton-row pattern from
`pulsar_daily_config`, and — see below — the observability-trace pattern from
`pulsar_daily_trace`). Lives in the same `polaris.db`, as a handful of new tables — never a
separate database, matching "Lives entirely inside Polaris" from the fourth pass above.

**No per-category gate — dropped, not deferred.** The original plan (see "First narrow slice"
above) was to restrict Weaver to one category at a time via config, expanding after living with it
a week. Rejected once it got concrete: gating by category means the model would have to *classify
against the gate* before deciding whether to write anything, which is exactly the kind of forcing
this system is supposed to avoid — topics should emerge from what's actually being talked about,
not be pre-approved against a shrinking allowlist. **`constellation_config` is a plain global
on/off, nothing scoped underneath it.** Whatever mix of books/music/technology/other actually shows
up is the real signal; the trace tables below (not a content filter) are the safety net for
watching that unfold, same as "no per-poll cap on backlog size" was rejected for a symmetrical
reason back in the third pass — restricting scope doesn't reduce risk here, it just hides
information you'd rather have.

- **`constellation_config`** — singleton row, same shape as `pulsar_daily_config`:
  ```
  id                      INTEGER PRIMARY KEY CHECK (id = 1)
  enabled                 INTEGER NOT NULL DEFAULT 0
  poll_interval_minutes   INTEGER NOT NULL DEFAULT 60
  last_checked_at         DATETIME
  model                   TEXT NOT NULL DEFAULT ''
  created_at              DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
  ```
  `model` empty means "use whatever `config.DefaultModel` currently resolves to" — the exact same
  empty-means-inherit-the-default pattern `pulsar_daily_config.weather_location` already uses
  ("empty means use `default_location`, same fallback every other location-aware tool already
  has"), resolved live at run time rather than copied in once, so it stays in sync if the app-wide
  default model ever changes. The settings panel gets a model dropdown — same model list and
  picker every other model-select surface in the app uses (Pulsar's per-routine model,
  Daily's architect/writer models) — with "Same as chat (default)" as the first option mapping to
  the empty string, and every other entry an explicit override.
- **`stars`** — the main table, one row per topic:
  ```
  id           INTEGER PRIMARY KEY AUTOINCREMENT
  title        TEXT NOT NULL
  category     TEXT NOT NULL
  summary      TEXT NOT NULL DEFAULT ''    -- the Library card's one-liner
  body         TEXT NOT NULL DEFAULT ''    -- the Markdown chapter body
  tags         TEXT NOT NULL DEFAULT '[]'  -- JSON array, shown as chips
  status       TEXT NOT NULL DEFAULT 'proposed'  -- 'auto' | 'proposed'
  confidence   TEXT NOT NULL DEFAULT ''
  created_at   DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
  updated_at   DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
  ```
  Columns beyond what the UI already shows (Library card, Chapter detail) aren't invented ahead of
  need — a genuinely backend-only field gets added by migration once Weaver's extraction pass
  actually needs somewhere to put it, not speculatively now.
- **`star_sources`** — child table, the "Linked articles" list:
  ```
  id          INTEGER PRIMARY KEY AUTOINCREMENT
  star_id     INTEGER NOT NULL REFERENCES stars(id) ON DELETE CASCADE
  thread_id   INTEGER NOT NULL REFERENCES threads(id)
  linked_at   DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
  ```
  A real table, not a JSON blob (unlike e.g. `pulsar_daily_editions.blocks`), because it's queried
  the other direction too — "has thread X already been folded into a star" is the idempotency
  check the poller needs on every run.
- **`stars_fts`** — FTS5 external-content index over `stars(title, summary)`, same
  external-content-plus-triggers shape as `messages_fts`. Backs the dedup/merge retrieval
  prefilter from the third pass above.
- **`shooting_star_runs`** / **`shooting_star_candidates`** — full observability from day one, not
  an add-later concern. Directly modeled on `pulsar_daily_trace`, which exists *because* a real
  silent-drop bug (a block generated then discarded with nothing but an ephemeral log line) was
  genuinely hard to debug without it — Weaver's pipeline has the same shape (an extraction call,
  then a per-candidate merge-or-new decision), so it gets the same treatment up front instead of
  waiting for its own version of that bug:
  ```
  -- one row per thread Weaver processes
  CREATE TABLE shooting_star_runs (
      id               INTEGER PRIMARY KEY AUTOINCREMENT,
      thread_id        INTEGER NOT NULL REFERENCES threads(id),
      started_at       DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
      finished_at      DATETIME,
      candidate_count  INTEGER NOT NULL DEFAULT 0,  -- 0 is a valid, common outcome, not an error
      extraction_raw   TEXT NOT NULL DEFAULT '',    -- what the extraction call actually said
      error            TEXT NOT NULL DEFAULT '',
      cost_usd         REAL NOT NULL DEFAULT 0
  );

  -- one row per candidate topic proposed within a run
  CREATE TABLE shooting_star_candidates (
      id                 INTEGER PRIMARY KEY AUTOINCREMENT,
      run_id             INTEGER NOT NULL REFERENCES shooting_star_runs(id) ON DELETE CASCADE,
      title              TEXT NOT NULL,
      confidence_class   TEXT NOT NULL DEFAULT '',  -- 'obvious' | 'fuzzy'
      decision           TEXT NOT NULL DEFAULT '',  -- 'new_star' | 'merged' | 'proposed'
      reasoning          TEXT NOT NULL DEFAULT '',  -- why — same role as pulsar_daily_trace.diff_reasoning
      resulting_star_id  INTEGER REFERENCES stars(id),
      cost_usd           REAL NOT NULL DEFAULT 0,
      created_at         DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
  );
  ```
  For any thread, this makes "what did Weaver see, propose, decide, and why" fully reconstructable
  — nothing silently dropped, matching this doc's own "verify on real hardware" culture: you
  shouldn't have to trust that the pipeline did the right thing, you should be able to look.

**Deliberately not in this pass:** the reflection-layer cross-links table (`star_edges` or
similar) — still allowed to land after the reflection layer itself is actually being built (see
the fourth pass above), not guessed at today.

## Reviewing a proposed star (tenth pass)

The Inbox banner in the Library screen (sixth pass) had a destination but no real design — "N
chapters proposed, awaiting your review" led nowhere in particular. This pass gives it one, and
it deliberately reuses the free-text/LLM-reconciled shape from the eighth pass's "Edit star"
rather than inventing a second interaction language: **reviewing a proposed star should feel like
the same conversation as correcting a confirmed one, not a different, more bureaucratic flow.**

**Three actions on a proposed star, not two.** A flat approve/discard binary throws away exactly
the input that makes a low-confidence star interesting to review in the first place — Weaver
usually gets *part* of it right. The middle option is a **Refine** sheet, visually the same
composer pattern as Edit star, but asking a two-sided question ("what did it get right, what was
wrong") instead of Edit star's one-sided "what's wrong or what to add." Sending a refinement both
corrects the star *and* resolves the review in one step — there's no separate confirm-after-refine
tap, since providing the correction already is the human decision point. See `mockups/vault.html`
frames 5-7 (Inbox list, Review star, Refine sheet) for the sketch, including a new **"Why this
needs a look"** block on the Review screen that surfaces `shooting_star_candidates.reasoning`
directly — the same sentence Weaver already logs for the trace tables, now put in front of the
person who actually has to make the call, not just kept for debugging.

**Schema additions this implies:**

- **`stars.status` grows two more values**: `'auto' | 'proposed' | 'confirmed' | 'rejected'`.
  `confirmed` is a formerly-`proposed` star a human approved (as-is, or via Refine) — kept
  distinct from `auto` so "how often does Weaver's own confidence judgment turn out right" stays
  answerable later. `rejected` is a soft state, not a delete — same reasoning as `threads.disabled`
  /`memories.disabled` elsewhere in this schema: a discarded star stays in the trace history
  (useful for "did Weaver keep re-proposing this" debugging) but drops out of Library/Inbox reads.
- **New table `star_reviews`** — the human side of the observability story, sibling to
  `shooting_star_runs`/`shooting_star_candidates` on the machine side. You said full observability
  from day one; a review decision (and, for Refine, exactly what was typed) is as much a part of
  that record as what Weaver proposed:
  ```
  CREATE TABLE star_reviews (
      id          INTEGER PRIMARY KEY AUTOINCREMENT,
      star_id     INTEGER NOT NULL REFERENCES stars(id) ON DELETE CASCADE,
      action      TEXT NOT NULL,             -- 'approved' | 'refined' | 'discarded'
      correction  TEXT NOT NULL DEFAULT '',  -- the free-text typed, for 'refined'; '' otherwise
      created_at  DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
  );
  ```
  Not merged into `shooting_star_candidates`: a star can accumulate several candidate rows over
  time (repeated dedup merges before it's ever reviewed), so a resolution belongs to the *star* at
  review time, not to any one candidate event.

**Settled: Edit-star stays unlogged.** `star_reviews` is scoped to the Inbox-review moment only —
an Edit-star correction on an already-*confirmed* star (eighth pass) does not write to it. The
observability question that actually matters — is Weaver flagging too much or too little, and once
something lands in the Inbox is the person mostly just confirming it (gate too cautious), or
routinely reaching for Refine/Discard (gate too loose, or the extraction itself needs prompt work)
— is fully answered by watching `star_reviews`' `action` distribution alongside
`shooting_star_candidates`' own `confidence_class`/`decision` columns. A separate log of ordinary
maintenance edits to stars nothing was ever unsure about wouldn't add signal to that specific
question, so it's left out. This is the day-one observability answer for whether the confidence
gate (and the extraction prompt behind it) will need tuning — no dashboard needed yet, just a
query someone runs against `star_reviews`/`shooting_star_candidates` once there's real data.

## How Weaver actually works (eleventh pass)

First real design pass on the extraction/merge pipeline itself, not just its schema. Grounded in
three patterns already proven in this codebase rather than invented fresh:
`gateway/pulsar_daily.go`'s `dailyDiffJudge` (a forced-tool-call, given prior state and new state,
returns a structured decision), `store.ReadThread` (`store/store.go:1213` — already reconstructs a
thread's full effective transcript, reused as-is), and `tools/web_read.go`'s `filterExtractedText`
(a second, cheap LLM call that extracts only what an instruction asks for from already-fetched
content — its own doc comment literally calls it "the double RAG step," and `search_chats`' `read`
action already applies it to a past thread via `prompts.Tools.ThreadReadFilterSystem`).

**Eligibility is two independent gates, not one.** The original "not yet processed" framing broke
the moment revisiting an old thread became a real case (you go back to threads from weeks ago and
keep chatting in them — there's no archive/close concept to lean on instead). So:

- **Idle-timing gate**: a thread is only a poll candidate once it's been quiet for at least
  `poll_interval_minutes` — unrelated to whether it's ever been seen before, just "don't grab a
  conversation mid-thought."
- **Delta gate**: has this *specific* thread produced messages Weaver hasn't seen. `messages.id`
  is a global autoincrement, so `MAX(id) WHERE thread_id = ?` is a clean high-water mark.
  `shooting_star_runs` gets one new column: `last_message_id_seen INTEGER NOT NULL` (the thread's
  message high-water mark at the moment that run happened). Eligibility: current max > the most
  recent run's `last_message_id_seen` for that thread (or no prior run at all). A thread reopened
  two weeks later behaves identically to a brand-new thread the first time it goes idle again — no
  special case, `shooting_star_runs` just accumulates another row, which is exactly the trace
  history wanted anyway.
- **Correction to the ninth pass**: `star_sources` was justified partly as the idempotency check
  ("has thread X already contributed"). That reasoning doesn't survive revisits —
  `shooting_star_runs.last_message_id_seen` does that job now. `star_sources` goes back to purely
  backing the "Linked articles" UI list: one row per `(star_id, thread_id)`, **upserted** (refresh
  `linked_at`) rather than duplicated on a repeat contribution — `UNIQUE(star_id, thread_id)`.

**Input prep, before Weaver's own loop starts, genuinely different on a first pass vs. a
revisit** — this part is still a discrete Go-orchestrated step, not a tool call:

- **First-ever pass on a thread**: no prior notes exist, so there's nothing to filter *for* —
  "what's worth remembering here" is an open-ended question. Weaver gets the thread's raw content
  (`store.ReadThread`) directly, no filter pass. **The double-RAG mechanism only unlocks starting
  on the second (or n+1th) pass** — a first pass never gets it, by design, not as a missing
  feature.
- **A revisit** (prior notes exist — whatever `stars`/`shooting_star_candidates` rows this thread
  already produced): fetch only the delta (`messages WHERE id > last_message_id_seen` — the old
  turns never get re-read, ever, no matter how many times the thread gets revisited over its
  lifetime), then run `filterExtractedText` on that delta with an instruction built from the prior
  notes: *"Check for updates on: [prior star titles + one-line summaries]. Flag anything that
  updates, corrects, or adds to those, plus anything genuinely new."* That's the real second model
  run — cheap relative to a full re-read, and it's the existing `filterExtractedText`, not new
  code. The *condensed result*, not the raw delta, becomes Weaver's starting task text.

**Correction, twelfth pass: what happens after that input is assembled is NOT a sequence of
Go-orchestrated forced-tool-call stages — see below.** The paragraph originally here described
"Stage 1 extraction" and "Stage 2 resolve" as two separate one-shot calls the way Pulsar Daily's
`dailyDiffJudge` works. That's wrong for Weaver specifically: it should be one real agentic loop,
Weaver's own tool calls doing the reading, deciding, and writing — see "Weaver's toolset" below.

## Weaver's toolset, and reflection-layer linking pulled into v1 (twelfth pass)

Corrects the eleventh pass's architecture: Weaver isn't a sequence of Go-orchestrated forced-tool-
call stages (that was drifting toward Pulsar Daily's shape). It's **one `agent.Run` per shooting
star**, narrow toolset, Weaver's own tool calls deciding what's worth noting, whether it matches
something existing, and whether it's worth linking — exactly what the second pass originally said
("its tool set is closer to 'read a thread, read/write the vault, maybe search the vault'"). Go
code does exactly one thing before the loop starts: assemble the task text (raw content on a first
pass, the `filterExtractedText`-condensed delta plus prior notes on a revisit — previous section,
unchanged). Everything after that is Weaver's own reasoning and tool calls.

**Also new this pass: the reflection layer (star-to-star linking) is pulled into v1.** Previously
deferred (fourth/ninth passes — "not required in v1," `star_edges` "not guessed at today"). That
call is reversed: linked stars are wanted from day one, not a stretch goal.

**Weaver's toolset** (own narrow catalog, `tools.Register`-style, never the main chat agent's
`web_search`/`calculator`/etc.):

- **`search_stars(query)`** — FTS5 over `stars_fts`, returns matching `star_id`/title/summary.
  Does dedup-checking *and* link-discovery duty — same tool, Weaver decides which question it's
  asking with it.
- **`read_star(star_id)`** — the full card (title, category, tags, summary, body). Look-before-
  you-link, and look-before-you-merge — a model judging a connection or a match from a title+
  summary snippet alone has no way to be careful; one that can actually read the candidate does.
- **`create_star(title, category, summary, body, tags, confidence_class)`** — writes a new `stars`
  row (`status` derived from `confidence_class` exactly as before: `obvious` → `auto`, `fuzzy` →
  `proposed`), and writes the matching `shooting_star_candidates` row (`decision = 'new_star'`) as
  a side effect of the call itself — logging isn't a separate Go-orchestrated step anymore, it's
  built into what the tool handler does.
- **`update_star(star_id, summary, body, tags, confidence_class)`** — merges into an existing
  star, same side-effect logging (`decision = 'merged'`).
- **`link_stars(star_id_a, star_id_b, reasoning)`** — writes `star_edges`, idempotent (no-ops if
  the pair's already linked). **No cap on how many links a run can create.** Quality is a
  prompting problem, backstopped by `read_star` actually existing (so a careful decision is
  *possible*) and by full observability (below) making an over-linking prompt visible in the data,
  not a numeric ceiling.

**This also simplifies the decision model**: there's no separate "ambiguous match" case. Deciding
"is this the same topic as something existing" now happens *inside* Weaver's own reasoning (via
`search_stars`/`read_star`) before it ever calls `create_star` or `update_star` — genuine
uncertainty about a match just means reaching for `confidence_class = fuzzy` on whichever call it
makes, same `proposed`-routing outcome as the ninth pass's "ambiguous routes like fuzzy," no third
decision value needed.

**Schema, corrected for the agentic-loop shape:**

- **`shooting_star_runs`** — one row per shooting star (the whole `agent.Run`, not one stage):
  ```
  CREATE TABLE shooting_star_runs (
      id                    INTEGER PRIMARY KEY AUTOINCREMENT,
      thread_id             INTEGER NOT NULL REFERENCES threads(id),
      last_message_id_seen  INTEGER NOT NULL,
      started_at            DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
      finished_at           DATETIME,
      summary               TEXT NOT NULL DEFAULT '',  -- Weaver's own closing wrap-up of what it did
      error                 TEXT NOT NULL DEFAULT '',
      cost_usd              REAL NOT NULL DEFAULT 0
  );
  ```
- **`shooting_star_candidates`** — unchanged shape from the ninth pass, now gets a `run_id` FK and
  is populated by `create_star`/`update_star`'s side effects rather than a discrete resolve stage:
  ```
  CREATE TABLE shooting_star_candidates (
      id                 INTEGER PRIMARY KEY AUTOINCREMENT,
      run_id             INTEGER NOT NULL REFERENCES shooting_star_runs(id) ON DELETE CASCADE,
      title              TEXT NOT NULL,
      confidence_class   TEXT NOT NULL DEFAULT '',   -- 'obvious' | 'fuzzy'
      decision           TEXT NOT NULL DEFAULT '',   -- 'new_star' | 'merged'
      reasoning          TEXT NOT NULL DEFAULT '',
      resulting_star_id  INTEGER REFERENCES stars(id),
      cost_usd           REAL NOT NULL DEFAULT 0,
      created_at         DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
  );
  ```
- **`star_edges`** — comes out of "deferred," unchanged from the ninth pass's original sketch:
  ```
  CREATE TABLE star_edges (
      id          INTEGER PRIMARY KEY AUTOINCREMENT,
      star_a_id   INTEGER NOT NULL REFERENCES stars(id) ON DELETE CASCADE,
      star_b_id   INTEGER NOT NULL REFERENCES stars(id) ON DELETE CASCADE,
      reasoning   TEXT NOT NULL DEFAULT '',
      created_at  DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
      CHECK (star_a_id < star_b_id),
      UNIQUE (star_a_id, star_b_id)
  );
  ```
- **`shooting_star_events`** — new this pass, replacing an earlier `star_link_passes` idea floated
  and dropped mid-discussion: one continuous loop wants one generic trace, not a bespoke table per
  tool. Captures *every* tool call in a run — `search_stars`/`read_star` included, not just the
  writes — which is what actually answers "is Weaver over-linking or under-linking in practice,"
  the same observability instinct as `star_reviews` answering the confidence-gate question:
  ```
  CREATE TABLE shooting_star_events (
      id          INTEGER PRIMARY KEY AUTOINCREMENT,
      run_id      INTEGER NOT NULL REFERENCES shooting_star_runs(id) ON DELETE CASCADE,
      tool        TEXT NOT NULL,               -- 'search_stars' | 'read_star' | 'create_star' | 'update_star' | 'link_stars'
      args        TEXT NOT NULL DEFAULT '{}',
      result      TEXT NOT NULL DEFAULT '',
      created_at  DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
  );
  ```

**Where this would live**: `gateway/constellation_scheduler.go` (poller, same once-a-minute ticker
shape as `pulsar_scheduler.go`) and `gateway/constellation_weaver.go` (task assembly + the
`agent.Run` call + the tool handlers) — same package and naming convention as Pulsar and Pulsar
Daily, not a separate top-level package.

## Weaver's tools and system prompt (thirteenth pass)

First real prompt-writing pass, grounded in `tools/descriptions/memory.yaml` (the closest existing
analog — a tool that decides what's durable enough to write, unsupervised, with real calibration
language) and `prompts.yaml`'s `thread_read_filter_system` entry (the injection-defense framing
already used for reading a past thread, reused near-verbatim since Weaver reads the same kind of
content). Structurally: each tool gets a `tools/descriptions/*.yaml` file (name/requires/
description/api_description, same shape as every existing tool), gated via `requires: weaver_run`
— the same mechanism `finalize_daily_items` uses to stay invisible to a normal chat turn
(`requires: pulsar_daily_items`) — so these five tools sit in the existing catalog/registry
machinery without ever being offered outside Weaver's own isolated `agent.Run`. The system prompt
itself gets a new `weaver:` section in `prompts.yaml`, sibling to `pulsar_daily:`.

**Correction caught before locking in**: an early draft of `create_star`'s calibration copied
`memory.yaml`'s "default to NOT writing" posture directly. That's wrong here, not just differently
worded — memory's conservatism exists *because* every memory gets injected into *every future
turn's context, forever*, so clutter there has a real, continuous cost. Stars don't work that way:
they live in their own browsable library, never auto-injected into a chat turn. A mediocre star
just sits there unopened, costing nothing per turn — "default to nothing" doesn't earn its keep on
a system built to be a *growing* library. The two judgment calls that actually matter are
different from "should this exist at all":

1. **Is this the same topic as something that already exists** — `search_stars`/`read_star`
   first, prefer `update_star` over a near-duplicate `create_star`. Five separate conversations
   about, say, Ferraris isn't five candidate stars, it's one star that gets richer five times,
   because the search-first discipline catches the match each time.
2. **Is this a stated fact/topic vs. an inference about the person** — this is where real caution
   belongs. A fact or topic genuinely, substantively discussed clears a low bar (`obvious`,
   captured freely). An inference *about who they are* (a taste, a leaning, a pattern read across
   a couple of mentions) clears a higher one (`fuzzy`, routed to review) — because that's a claim
   about them, not a question of whether it's worth remembering at all.

So the real bar for `create_star`/`update_star` is **"was this actually discussed with some
substance," not "is this dramatic enough to matter."** Excluded: pure logistics with no topical
content, a single throwaway reference with nothing said about it ("saw a Ferrari today" and
nothing else), ephemeral/time-bound content with no lasting relevance (today's weather). Real
exchanges about real topics clear it, and most shooting stars should produce a new or updated
star, not zero.

**The five tools:**

- **`search_stars(query)`** — FTS5 over `stars_fts`. Explicitly framed as keyword, not semantic:
  matches shared words, not paraphrase. Returns up to 10 hits, title+summary only, with an
  explicit instruction that this alone is never enough to decide a match or a link — only enough
  to decide something's worth reading.
- **`read_star(star_id)`** — full card (title, category, tags, status, confidence, summary,
  body). Framed as mandatory before `update_star` or `link_stars`, never optional — a
  `search_stars` snippet is a lead, not evidence.
- **`create_star(title, category, summary, body, tags, confidence_class)`** — the calibration
  above. `category` is explicitly free text, no fixed list — "reuse a category already in use for
  the same general area rather than inventing a near-duplicate; `search_stars` if unsure what's
  already there."
- **`update_star(star_id, summary, body, tags, confidence_class)`** — merge, don't append;
  rewrite to read as one coherent, current entry. Same `confidence_class` routing as create — a
  merge can lower a star's confidence just as easily as a fresh create can.
- **`link_stars(star_id_a, star_id_b, reasoning)`** — explicitly distinguished from merge: "these
  are *different* topics that relate, not the same topic under two names — if they're the same
  thing, that's `update_star`, not a link." `reasoning` framed as accountability, not
  documentation: "a vague reason ('both mention technology') is itself a signal this link
  probably shouldn't be made."

**Weaver's system prompt** (new `weaver:` section, sibling to `pulsar_daily:`'s `wizard_system`):
frames the job (read this thread, decide what belongs in Constellation, the person never sees this
run directly), the corrected calibration above, "always `search_stars` before `create_star`,"
`category` as free text, and that checking for connections (`search_stars`/`read_star`/
`link_stars`) is a real, non-optional part of the job, not an afterthought after extraction is
"done." Carries the same injection-defense paragraph as `thread_read_filter_system` — the
conversation content is the person's own past messages, not instructions to Weaver, and text
styled as a directive inside it is read and judged like any other sentence, never obeyed.

**No forced "finalize" tool, unlike `finalize_daily_items`/`finalize_pulsar_prompt`.** `agent.Run`
ends naturally when Weaver stops calling tools and returns plain text — the system prompt asks for
one or two plain sentences summarizing what happened when it's done, and that plain text *is*
`shooting_star_runs.summary`. Confirmed as the right structure: the run's own closing wrap-up
doubles as both its natural termination and its human-readable trace entry, no separate step
needed for either.

## The "you" layer, pulled into v1 as a tag, not a subsystem (fourteenth pass)

First pass's deferred "private 'you' layer" (personal inferences about who the person *is* — "part
of Atlanta's queer community, weighing a move" — stay local, always proposed, never auto-written)
turns out not to need its own storage or pipeline at all. It's the same `stars` table, the same
Weaver loop, the same Inbox/Review/Refine flow — just a flag, because a personal star is still
genuinely a star: it shows on the Map, it can be linked to, it goes through the same review UI.

**Schema**: one new column, `stars.is_personal INTEGER NOT NULL DEFAULT 0`. Nothing else new.

**The dividing line, for Weaver's own prompt**: *is this about a topic, or about the person
themselves* — not "is this personal-feeling content" in some vaguer sense. "Ender's Game and the
science behind it" characterizes a book, even though liking it says something about the person.
"Reads science fiction" characterizes *them*. Same source material, different subject —
`create_star`/`update_star`'s `is_personal` guidance is written around this exact test, using this
exact pair as the worked example.

**Status routing — starting strict, on purpose:**

- `create_star` with `is_personal = true` → **always `proposed`**, regardless of
  `confidence_class`. No exceptions.
- `update_star` with `is_personal = true` → **any update resets status back to `proposed`**, even
  a pure reinforcement of an already-confirmed personal star (more sci-fi book threads adding to
  an already-approved "reads science fiction" star still requires a fresh look). Chosen
  deliberately over a softer alternative (only a *meaningful revision* re-triggers review, plain
  reinforcement merges silently into an already-confirmed star) — the softer version needs the
  model to self-judge "is this the same claim or a different one," exactly the kind of judgment
  easiest to get subtly wrong on identity-level content. **Explicitly a starting point, not a
  permanent one** — expected to soften once it's clear in practice whether constant re-affirming
  is genuinely worth the friction or just annoying; loosening it later is a prompt change, not a
  schema change, so nothing about starting strict forecloses that.
- `confidence_class` is still recorded on a personal star (stated directly vs. inferred — shown on
  the Review screen's confidence line) even though it no longer drives routing once
  `is_personal = true` overrides it. Informational, not decisional, for that case.

**Everything else needs zero new design**, which is the actual payoff of "just a tag, not a
subsystem":

- `link_stars` works completely unmodified. A personal star can be the hub several topic stars
  connect to — "reads science fiction" linked to "Ender's Game and the science behind it" is an
  ordinary link, same tool, same reasoning field. This is the reflection layer doing exactly what
  the first pass originally pitched it for.
- `star_reviews`/Inbox/Review/Refine all work unmodified — a personal star is just a proposed star
  in the same queue.

**Settled: option E from `mockups/vault-personal-star-options.html`, minus its corner icon** — a
tinted tile/border (`--color-personal`, a third accent between the existing warm gold and cool
blue) plus a text badge, nothing else. The icon overlapping the badge read as cluttered once seen
side by side, which the standalone options file was built specifically to catch before it landed
in the main mockup. Folded into `mockups/vault.html`: the Library's new "Reads science fiction"
card (Books & Ideas section) and the Inbox's existing "Might be weighing a move" card (now showing
both a `badge-personal` and its original `badge-proposed` — status and type are different axes,
both worth showing). Map's node treatment landed too, same pass: a small `--color-personal`-tinted
node (no separate cluster — `is_personal` is orthogonal to category, so it sits wherever its topic
naturally clusters) with a violet-tinted connecting line where it links to a topic star — the
"Reads science fiction" ↔ "Ender's Game" example from this pass's own discussion, made real on the
map. Also caught and fixed while comparing screens side by side: the Refine sheet's send button
was blue, inconsistent with every other primary action button (Continue in chat, Edit star's own
send button, Approve) — cards varying by provenance (auto/proposed/personal) is intentional, an
action button varying by which screen it's on wasn't, now gold throughout.

## The "This week" digest, v1 scoped down to a query (fifteenth pass)

The Library's digest banner ("This week: 4 new, 1 link surfaced — Ender's Game → child psychology")
has been sitting in the mockup since the sixth pass with no design behind it. Turns out its actual
copy is far more modest than the fourth pass's original idea (real synthesized prose — "you keep
circling back to space and to where you might live") — it's just counts plus one concrete example,
and that version needs **zero LLM calls**, v1 scope:

- **"N new"** — `COUNT(*) FROM stars WHERE created_at >= now - 7 days`.
- **"N links surfaced"** — `COUNT(*) FROM star_edges WHERE created_at >= now - 7 days`.
- **The highlight line** — the most recent `star_edges` row this week, rendered
  `{star_a.title} → {star_b.title}`; falls back to the most recent new star's title if no links
  happened this week; **the banner doesn't render at all if both counts are zero** — same
  "no signal, no output" instinct the poller itself runs on (the `her-go` lesson from the first
  pass, applied here too).

Pure query against tables Weaver already writes, computed live on Library load — no scheduler, no
cadence-check, no new trace table, no cost. This is core v1, not a stretch addition, since it's
essentially free once the rest of Weaver exists.

**Tapping the banner → a new "This week" screen**, reverse-chronological feed of exactly what the
banner is summarizing — everything actually *made* in the last 7 days: new stars, updated
(merged) stars, and new links. Deliberately excludes review actions (approve/discard) — those are
resolutions of something already made, not new material themselves, and "stuff that was made"
was the actual ask. Reuses the existing card component (same visual language as the Library's
`chapter-card`) in a flat list, each row tagged "New"/"Updated"/"Linked" instead of a category, with
a relative timestamp — not a new timeline/activity-feed visual pattern, since this screen doesn't
carry enough weight to earn one.

**v2, explicitly not now**: a real synthesized-prose version, closer to the fourth pass's original
ambition. Floated shape: a second, distinct Weaver-style pipeline — given the week's stars as
context ("here's what was created/updated this week, summarize trends if you see any"), either
read tool-by-tool the same way the main Weaver loop reads stars, or one larger context injection
of everything that week at once. Which of those two shapes is right, and its own cadence-check
(`last_weekly_digest_at`, same `isDailyDue`-style pattern Pulsar Daily already uses) is real design
work for later, not decided here — v1 stays a query, on purpose.
