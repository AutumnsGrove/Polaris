# Tides — monthly retrospective

**Status: mockup reviewed and liked (operator: "just right... quite brief"), backend mechanics for
category breakdown + spiked/faded now settled — still entirely in planning, implementation
explicitly deferred.** Named and scoped in a live brainstorm 2026-09-19 — see
`docs/plans/crazy-ideas.md` for the naming history (rejected: Perihelion, Almanac, Long Exposure,
Logbook, Field Notes) and the operator's scoping verdict this doc builds on. A first visual mockup
lives at `mockups/tides.html` (monthly cadence, all four blocks below — the "full page" density
option the operator picked over a single stat card or a stats+chart-only cut). Nothing here is
implementation-ready yet — no schema written, no code started — this is still the shape to react
to and refine before any of that begins.

## What it is

A page generated on a monthly (and eventually yearly) cadence — same "runs on a schedule, produces
something you read rather than type into" shape as Pulsar Daily, just a different cadence and a
retrospective lens instead of a forward-looking one. Built entirely from data Polaris already
has timestamped, not a new synthesis-from-transcripts pass:

- **Stat row** — turns asked, new stars formed, searches run, period cost. Straight aggregation,
  same shape as `store.Stats`/`GetStats` already does for the Settings panel — no new counting
  logic, just a different time window and a different surface.
- **Category breakdown** — new stars this period grouped by `stars.category`, rendered with the
  *same* category color tokens Constellation's star map already uses (`app.css`'s `--color-cat-*`),
  not a new palette invented for this feature.
- **Spiked / faded** — topics with a surge or a stop in `search_history` this period vs. the
  trailing baseline. Deliberately *not* sourced from `stars` — stars are a durable-fact layer that
  moves too slowly to show a real month-to-month spike; `search_history` is the actual "what were
  you circling" signal.
- **New stars this period** — a capped showcase (mockup uses 4 of 8, "+N more → Constellation"),
  not the full list. The point is a taste of what got captured, not a duplicate of the Library.

## Explicit scope discipline (the operator's own stated risk)

The real risk flagged going in wasn't build cost, it was this turning into a wall of generated
prose — the opposite of "calm over clever." The mockup enforces the ceiling structurally, not just
by prompt instruction: four fixed blocks, each with a hard cap (5 category rows, 3 spiked + 3
faded items, a capped star grid), no open-ended text block anywhere on the page. If a future
revision wants a free-text editorial line back (the mockup currently has exactly one, in the
masthead), it should stay capped at one sentence, not become a paragraph.

## Spiked/faded — settled design (2026-09-19)

**Data source is `events`, not `search_history`.** `search_history` looked right at first but is
wrong on two independent counts: `RecordSearch` (`store/store.go:1831`) upserts on exact query
text (`ON CONFLICT(query) DO UPDATE SET updated_at = ...`), so a repeat search bumps a timestamp
instead of creating a countable row — there's no occurrence counter anywhere in that table. And
it's only ever written from one place, `gateway/search.go:64` — Atlas's own search box. It never
sees a single `web_search` tool call the assistant makes during ordinary chat, which is the actual
"assistant-based search engine" signal this feature is supposed to reflect (confirmed live in the
brainstorm — Tides is a Polaris/assistant feature, explicitly not an Atlas one).

The real source: every `web_search` call the agent makes gets logged to `events` regardless of
which thread it happened in — `gateway/turn.go:1019`, `source = "tool."+evt.Tool` (i.e.
`"tool.web_search"`), `message = "tool call started"`, with the raw call arguments (including the
query string, `args.Query` — `tools/web_search.go:128`) sitting in `data.args.query`. One row per
real call, not deduped, so it actually supports counting. Query: `events` filtered to
`source = 'tool.web_search'` and `message = 'tool call started'`, reading `data->>'$.args.query'`
and `created_at`.

**Clustering needs a model, not a formula.** Raw query strings from that log are still free text —
"docker compose networking issue" and "docker compose network config" are the same topic to a
person and two unrelated rows to a `GROUP BY`. At single-operator volume (the stat row's own
sample is 96 searches/month — genuinely too little data for a statistical spike detector, z-scores
included, to mean anything), a bounded, tool-call-forced LLM pass over the period's distinct
queries + counts is the right tool, same shape as `tools/finalize_daily_items.go`'s "did this
actually change since yesterday" pass: narrow input (query strings + counts only, never raw
threads), narrow output (a hard cap of 3 spiked + 3 faded clusters, forced through a tool schema,
not free prose). This is also what keeps the per-run cost small regardless of how busy a given
month was — the input size is bounded by distinct-query count, not message/token volume.

**Baseline window (operator call): trailing 60–90 days.** A topic needs real presence somewhere in
that window, excluding the current period, to be eligible to "fade" — long enough that something
you dug into in July and went quiet on in August doesn't already read as faded in September's
edition, short enough that the fade list doesn't fill up with years-old one-off mentions once
real history accumulates.

**Spike threshold (operator call): low bar, trust the clustering.** A real cluster (3+ related
queries this period) with little-to-no presence in the baseline window counts as a spike — no
requirement that it beat some historical rate, no fixed top-N cutoff. Favors surfacing a genuine
new interest even on a quiet month, trusting the LLM's judgment about what's actually a coherent
cluster over a hard numeric formula that a data volume this small can't really support anyway.

**Deferred, not decided against:** extending past `web_search` to other tools (`nearby_search`,
`fetch_url`) as additional topic signal. `web_search` alone is the core "what were you researching"
signal and enough for v1; folding in more tool types is a natural v2 lever once the single-source
version is proven, not a blocker now.

## Category breakdown — settled design (2026-09-19): needs a new classification job, not `stars`

The original assumption was that the stat row and category breakdown could both lean on `stars`
data almost for free (`stars.category` already has color tokens, already exists). Checked live
against the real potato deployment (`ssh potato-remote`, direct sqlite query against
`polaris_polaris-data`): **127 stars total after ~2 months of real use**, skewed hard toward a
handful of categories (technology 29, video games 13, ai & machine learning 12, ...) and reading
as durable personal facts ("works in Midtown Atlanta," "daily browser is Zen"), not per-conversation
topics. Stars move far too slowly and are too personal/sparse to answer "what did I actually talk
about this month" — the same problem already flagged for spiked/faded and `search_history` above,
just for a different table. Real thread volume from the same live check: **Aug 2026 — 132 threads,
366 user messages, 571 `tool.web_search` events; Sept 2026 (partial) — 79 threads, 226 user
messages, 497 events.** That's the actual scale a classification job needs to handle.

**Decision: a new monthly classification job, upstream of Tides generation, not a Tides-time read.**
This doesn't violate the "no raw transcript re-reads" cost guardrail above — that guardrail is
about the *retrospective render step* specifically. This job is a separate, once-a-month pass that
reads each thread exactly once and writes a small structured row; Tides generation itself still
only ever aggregates over stored output, same as it does for `Stats`. Scope, picked after weighing
per-thread-incremental (needs a new thread-idle trigger, doesn't exist), batch-of-queries-only
(no mood/topic-per-conversation at all), and sampled/capped (accepts incompleteness): **per-thread,
once, run as a month-end batch** — simplest trigger shape, full coverage, no new "thread just went
idle" detection to build.

- **New table (tentative), `thread_classifications`**: one row per thread — `thread_id`, `topic`
  (reusing the *same* category taxonomy `stars.category` already uses, so this doesn't invent a
  second palette/taxonomy for Tides to reconcile against Constellation's), a mood/tone tag,
  `classified_at`. Forced through a tool-call schema for structured output, not prose — same shape
  `tools/finalize_daily_items.go`/`finalize_pulsar_prompt.go` already use elsewhere in this
  codebase.
- **Trigger**: reuse `isDailyDue`'s exact shape (`gateway/pulsar_daily.go:616`) — a month-boundary
  check on the existing once-a-minute scheduler tick (`gateway/pulsar_scheduler.go`), not a new
  scheduling primitive. At month-end: find threads in the period with no classification row yet,
  classify each, write the row, then Tides generation reads this table instead of any transcript.
- **Classifier input, bounded per call regardless of thread length**: strip tool calls and internal
  reasoning entirely — just the conversational back-and-forth. User messages in full (per the
  operator's own estimate, user messages run roughly 10x shorter than assistant replies in
  practice, so this is cheap). Assistant messages capped to their first ~500 characters — enough to
  catch the gist/tone of a reply without paying for the full response. Preserve chronological turn
  order so the classifier sees real back-and-forth shape, not a bag of messages.
- **Needs a real agentic loop, not a single forced-tool call.** Unlike `finalize_daily_items.go`'s
  narrow-input/single-tool-out shape, this classifier may need to actually look at an image a
  thread references to know what it's about — so it gets a small toolbelt,
  `{view_image, record_classification}`, and is allowed to call `view_image` zero or more times
  before being forced to end the turn via `record_classification` (mirrors
  `finalize_pulsar_prompt.go`'s "final output must go through a tool call" pattern, just with an
  optional exploration step in front). This reuses `tools/view_image.go` completely as-is — its
  existing `describe` (always available)/`see` (only when `ctx.Multimodal`) gate already handles
  "does this thread's model support real vision" per-thread, so the classification job doesn't
  need its own parallel multimodal-detection logic.
- **Cheap sampling for testing, before committing to a full run**: `cmd/constellation_backfill.go`
  already has exactly this shape — an `-n` flag that hits `/api/constellation/backfill?limit=N`
  and processes only the N most-recently-active eligible threads, specifically so a backlog run
  can be eyeballed on a handful of threads before running the full thing. A matching
  `polaris tides classify -n N` against a new `/api/tides/classify?limit=N` gets the same
  try-before-you-commit workflow for free, no new CLI pattern needed.

**Status: mechanics settled, nothing implemented.** Explicitly flagged by the operator as needing
real testing against real threads (quality of topic/mood output, actual per-call cost, whether 500
chars of assistant reply is enough signal) before any schema is finalized or real implementation
starts — implementation is "not for another day or few" as of 2026-09-19. The `-n`-limited classify
path above is the first concrete thing to build, specifically so that testing can happen before the
`thread_classifications` schema is locked in.

## Naming, cross-month behavior, and remaining mechanics — settled 2026-09-19

**Naming, mirroring Constellation's run/artifact split.** Weaver's "shooting star" is the *run*
that produces a *star* (the stored artifact). Tides gets the same two-name split: **"crashing
wave"** is the per-thread classification run, **"wave"** is the resulting stored record (one
thread's topic/mood/intent for one period). **"The tide"** is the existing monthly Tides
generation pass itself — it comes in and assembles from that period's accumulated waves, same
nautical image as the feature name, not a separate coinage.

**Schema correction: a wave is keyed by `(thread_id, period)`, not `thread_id` alone.** Walking
through a concrete case surfaced this: a thread about fishing boats gets a wave in September (at
turn 10). The same thread stays open and picks back up in October (now at turn 30, "I bought one").
That's a *second* wave for the same thread, not an update to the first — each period a thread has
new activity in gets its own wave. **Eligible for a wave this period = thread has message activity
within the period AND has no wave yet for `(thread_id, this period)`.** A thread quiet for months
then revived only gets a new wave for the period it was actually active in, not a retroactive
rewrite of the old one.

**The classifier gets a `search_waves` tool.** Same shape as `tools/search_stars.go`'s existing
`stars_fts`-backed lookup, but over prior waves instead of stars. Two jobs: (1) when a thread gets
a second wave in a later period, the crashing-wave run for it surfaces the thread's own prior
wave(s) as context, so the new wave can note "picked back up from last period's result: X, Y, Z"
instead of re-deriving the whole thread's history cold; (2) keeps topic/mood phrasing consistent
run over run generally, the same reason `search_stars` existing for Weaver's internal use matters —
without it, wave-writing style would drift thread to thread with no shared reference point.
Toolbelt is now `{view_image, search_waves, record_classification}` — still forced to end via
`record_classification`, the other two are optional exploration steps first.

**Fields recorded per wave: four, not two.** `topic` (reusing `stars.category`'s taxonomy as the
rough starting vocabulary, with a freeform escape hatch when a thread genuinely doesn't fit any
existing category — not a hard closed enum), `mood`, **`intent`** ("what were you trying to do," one
line), and **`resolved`** ("did you get your answer" — yes/no/partial). `mood` has no ready-made
taxonomy the way `topic` does — same "lean on the vibe of `stars.category` (short, plain-word tags)
as a rough starting point, freestyle if nothing fits" treatment, not a hard enum forced by the tool
schema.

**Eligibility filter, grounded in the real `threads.source` values** (`store/store.go:47`,
`source TEXT NOT NULL DEFAULT 'web'`): ordinary chat threads (`source = 'web'`, the default) are
eligible. Excluded: `source = 'pulsar'` (routine pulses); Pulsar Daily editions (already confirmed
in `store/stats.go`'s own comment to never be a `threads` row at all —
`pulsar_daily_editions.cost_usd` is a wholly separate table/cost path, so no filter is even needed
there); Weaver/shooting-star runs (`gateway/constellation_weaver.go`'s `RunShootingStar` calls
`agent.Run` directly as an internal task, not through the normal thread-creation path); star-edit
runs (assumed to be a direct DB mutation from the Constellation UI, not a chat thread — worth
confirming when this is actually built); and ghost-mode turns, confirmed via `gateway/turn.go`'s
`Anonymous` handling and `ghost_usage`'s own schema comment ("a ghost turn has no event log to fall
back on... the very thing ghost mode exists to avoid") to never persist as an ordinary thread
either. **Ghost mode gets exactly one number surfaced in Tides: a plain count of that period's
`ghost_usage` rows** — how many times ghost mode was used, nothing about content — mirroring how
`GetStats` already folds ghost spend into Polaris's totals via that same genuinely-anonymous table,
not new tracking machinery.

**The tide has a hard dependency on that period's waves being complete — no partial-month
tolerance in production.** The whole premise (aggregating real signal instead of re-reading
transcripts) needs the full period's waves present before the tide can generate anything
worthwhile. Failed per-thread classification retries reuse Constellation's existing pattern exactly
(`constellation_scheduler.go`'s stale-run sweep / `needs_retry` marking) — failed threads get
swept and retried before the tide crashes, not silently skipped for the month.

**Still open: dedicated testing infrastructure.** The `-n`-limited `polaris tides classify` path
(above) covers trying the classifier against a handful of *real* threads before trusting it at
scale. But the operator separately flagged needing a throwaway/synthetic environment — something
closer to the `polaris benchmark` command's isolated-DB-plus-pinned-search approach — to let a full
tide run "cook" against seeded test questions without waiting on a real month's worth of real data,
or touching production `polaris.db`. This has no design yet; it's the next thing to scope before
implementation starts.

## Yearly cadence — settled 2026-09-19

**A year-end tide aggregates over that year's twelve already-computed monthly tides, not a fresh
classification pass.** The classification work (crashing waves) has already happened twelve times
over by the time a year boundary is reached — re-running anything more expensive than reading back
that year's monthly tide output + underlying waves would waste data that's already sitting there.
No longer deferred-and-undesigned the way it was earlier in this doc; it's cheap by construction
once monthly is real, same "aggregate stored output, never re-derive" discipline as everything else
here.

## Testing infrastructure and hidden-thread visibility — settled 2026-09-19

**Shooting star (Weaver) persistence is a separate prerequisite, not part of Tides' own scope.**
Filed as its own issue — [#90](https://github.com/AutumnsGrove/Polaris/issues/90) — since
`RunShootingStar` currently creates no `threads` row and persists no transcript at all (`agent.Run`
never persists messages itself; that's the caller's job, and Weaver's caller never does it). This
was originally going to be a Tides-driven nice-to-have ("I want to open the hood on Weaver"), but
it turns out to be a real prerequisite: Tides' classification eligibility filter currently excludes
shooting-star runs *because they don't exist as threads*, and that assumption breaks the moment
they do. Handle #90 first, right after this planning session, ahead of any Tides implementation.

**Crashing-wave runs get the same "real thread, hidden from sidebar" treatment**, once #90's
pattern exists to copy: `threads.source = 'tide_wave'`, added to `ListThreads`/`ListThreadsPage`'s
exclusion list (`store/store.go:1441` already excludes `source = 'pulsar'` the same way) — free
visibility at `/t/<uuid>` via the existing filter-less `GetThreadRaw`, no frontend work.

**Classification runs concurrently, in small batches (~5 at a time), unlike Weaver.** Weaver
processes threads one at a time deliberately — a later shooting star run may need to see what an
earlier one already wrote to a star, since stars evolve as new developments occur across runs.
Crashing waves have no such cross-thread dependency: one thread produces exactly one wave,
independent of every other thread's wave that period, so there's no correctness reason to serialize
them. Batches of ~5 balance real parallelism against not hammering the LLM provider with the full
month's thread count at once.

**This concurrency choice has a direct testing-infra consequence.** `dev/fakeopenrouter`'s plain
FIFO queueing only works for genuinely sequential request order — Pulsar Daily's Stage A already
needed the `match`-substring targeting (pin a queued response to whichever request body actually
contains that text, ahead of FIFO and independent of queue position — see the package doc comment
in `dev/fakeopenrouter/main.go`) specifically because its own concurrent block-firing breaks FIFO
assumptions the same way. Since crashing-wave batches are concurrent by design, **testing them
through `fakeopenrouter` needs `match`-based targeting from the start**, not as a later add-on —
each batch member's scripted response should be pinned to something identifying in its request
(e.g. the thread's own content/topic), not queue position.

## Throwaway test environment — settled 2026-09-19

**Star-edit runs confirmed: one-off DB mutations from the Constellation UI, no `threads` row, not
recorded anywhere durable.** No exclusion-filter work needed for these at all.

**The test workflow deliberately stays close to how this codebase already verifies everything
else — real `/api/ask` calls against a real, throwaway server, not synthetic fixtures.** No new
seeding mechanism needed: `polaris run --config <path>` already loads any config file
(`cmd/run.go:61`), and that config's `database.path` field already controls where the DB lives —
the exact same override `gateway/testutil_test.go`'s `newTestHarness` already uses for its own
tempdir-isolated tests. So a throwaway Tides environment is just a second `config.yaml` pointing
`database.path` somewhere disposable (never the real `polaris.db`), same spirit as
`polaris benchmark --db <path>`'s isolation, just via the ordinary config mechanism instead of a
dedicated flag.

The actual workflow, agreed 2026-09-19:

1. Stand up `polaris run` against that throwaway config.
2. Claude has a real, varied set of conversations against its `/api/ask` endpoint — genuinely
   realistic thread content (covering different topics/moods, at least one thread with an image to
   exercise `view_image`, at least one pair of threads spanning a fake month boundary to exercise
   cross-month wave continuation), not hand-authored fixture rows bypassing the real chat path.
   This is the same "verify against the real thing, not a mock" discipline this codebase already
   applies everywhere else (spiking APIs with `curl`, testing tools via `/api/ask`, live-verifying
   installers on real hardware) — applied here to seeding instead of to a finished feature.
3. Run `polaris constellation backfill` against that same throwaway DB to populate `stars` from
   those seeded threads, so the environment has real Constellation data too, not just raw threads.
4. Run the Tides classify pass (crashing waves) against that DB, then tide generation itself, and
   inspect the results — including opening a crashing-wave thread directly at its own `/t/<uuid>`
   to confirm the hidden-thread visibility actually works, something no Go unit test can show.

Go-level unit tests (mirroring `pulsar_daily_pipeline_test.go`'s `newTestHarness` +
`sequencedSSEServer` scripted-response pattern) remain available as a faster, CI-friendly
complement once the mechanics are locked in enough to write assertions against — but the live
seeded-throwaway-server workflow above is the primary, agreed testing plan, not a fallback.

## `mood` vocabulary — settled 2026-09-19

**Same mechanism `stars.category` already uses, not a new one.** `prompts.yaml` (~line 670)
hardcodes `category must be one of: technology, software engineering, ... education & learning`
(32 values) directly in Weaver's prompt, with an explicit escape hatch ("only invent a category
outside this list if the star's subject genuinely fits none of them... check what's already in use
first so you extend the library rather than fragment it") and a live `%s` interpolation listing
"categories currently in use beyond the fixed list" — so the taxonomy is anchored to a hardcoded
starting list but can grow, and the prompt always shows the model what's already been invented so
it reuses rather than duplicates.

`mood` gets the identical treatment: **a hardcoded starting list of roughly 50 values**, baked into
the crashing-wave classifier's own prompt the same way, with the same "extend rather than
fragment, check what's in use first" escape hatch and the same live currently-in-use interpolation.
Left at that until real wave output over real months actually shows a gap — no attempt to
enumerate the 50 now; that's prompt-writing work for implementation, not a planning decision.

## Open questions before this is buildable

Every open question from this planning session — generation cadence/scheduling, cost-control-per-
run, yearly cadence, the test environment, star-edit runs' thread status, and `mood`'s vocabulary —
is now settled. Nothing outstanding remains before a schema pass and implementation can start,
beyond the still-open, separately-tracked prerequisite in
[issue #90](https://github.com/AutumnsGrove/Polaris/issues/90).

## Next step

Planning is done — mockup reaction, classification mechanics, naming, eligibility, concurrency, and
the test environment are all settled. Before any Tides implementation starts:

1. [Issue #90](https://github.com/AutumnsGrove/Polaris/issues/90) (Weaver/shooting-star transcript
   persistence) ships first — Tides' classification eligibility filter depends on it existing.
2. Then: stand up the throwaway test environment (see above), seed it via real `/api/ask`
   conversations plus a `constellation backfill` run, and build the classify pass against it before
   any `thread_classifications` schema is locked in.

Full implementation is intentionally not starting yet.
