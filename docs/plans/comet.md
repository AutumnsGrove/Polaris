# Comet — monthly retrospective

**Status: mockup reviewed and liked (operator: "just right... quite brief"), backend mechanics for
category breakdown + spiked/faded now settled — still entirely in planning, implementation
explicitly deferred.** Named and scoped in a live brainstorm 2026-09-19 — see
`docs/plans/crazy-ideas.md` for the naming history. A first visual mockup lives at
`mockups/comet.html` (monthly cadence, all four blocks below — the "full page" density option the
operator picked over a single stat card or a stats+chart-only cut). Nothing here is
implementation-ready yet — no schema written, no code started — this is still the shape to react
to and refine before any of that begins.

## Naming — settled 2026-09-19

Working name through most of this doc's early drafts was **Tides** (nautical: `wave`/`crashing
wave`/`the tide`), reached after **Perihelion** (the very first idea, rejected — real astronomy but
arbitrary-sounding orbital-mechanics jargon to anyone who isn't an astronomer) and a first round of
alternatives (Almanac, Long Exposure, Logbook, Field Notes) that didn't land either. Revisited once
more on reflection: the operator likes astronomy naming specifically (it's what Polaris/Atlas/
Pulsar/Constellation/Weaver are all already doing), and Tides broke from that into nautical
language instead. Renamed to **Comet**, replacing the whole Tides/wave/crashing-wave family:

- **Comet** — the feature itself. A comet's defining trait is *periodic return* (Halley's Comet,
  etc.) — "look what's come back around" — which is exactly what a monthly/yearly retrospective is.
- **Approach** — the per-thread classification run (was "crashing wave"), mirroring Weaver's own
  "shooting star" run naming.
- **Trail** — the resulting stored record for one thread, one period (was "wave"), mirroring
  Weaver's "star" artifact — what a comet leaves visible behind it as it passes.

**Icon: `majesticons:comet`, vendored.** No Lucide icon exists for comet/meteor/shooting-star at all
(checked directly against every plausible name in 1.46.0 — all 404, including `lucide-lab`, Lucide's
own staging repo for icons without a merged use-case yet). Researched broadly outside Lucide/Tabler
too (Phosphor, Iconoir, Remix Icon, Solar) — most "astronomy icon packs" turned out to be
inconsistent-style marketplace bundles, not real maintained icon libraries, and a couple of
plausible-looking hits (Phosphor's `meteor`, Remix's `meteor-line`) turned out to be false positives
on inspection (a JS-framework logo and a ringed-planet glyph, respectively) rather than real comet
glyphs. `majesticons:comet` — a radiating diagonal burst with a solid nucleus — is a genuine,
MIT-licensed comet glyph built on the exact same conventions Lucide uses (24×24, stroke-width 2,
round linecap/linejoin), so it drops in as one small vendored component
(`web/src/lib/components/icons/`, same pattern as `ShootingStar.svelte`, already shipped for the
`stars` tool icon) rather than a new npm dependency. See `mockups/comet-icon-options.html` for the
full comparison against Tabler's and Hugeicons' comet glyphs, and against Majesticons' catalog for
Pulsar/Daily/Constellation (none beat the existing Lucide icons there — kept as-is).

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
brainstorm — Comet is a Polaris/assistant feature, explicitly not an Atlas one).

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

**Decision: a new monthly classification job, upstream of Comet generation, not a Comet-time read.**
This doesn't violate the "no raw transcript re-reads" cost guardrail above — that guardrail is
about the *retrospective render step* specifically. This job is a separate, once-a-month pass that
reads each thread exactly once and writes a small structured row; Comet generation itself still
only ever aggregates over stored output, same as it does for `Stats`. Scope, picked after weighing
per-thread-incremental (needs a new thread-idle trigger, doesn't exist), batch-of-queries-only
(no mood/topic-per-conversation at all), and sampled/capped (accepts incompleteness): **per-thread,
once, run as a month-end batch** — simplest trigger shape, full coverage, no new "thread just went
idle" detection to build.

- **New table (tentative), `trails`**: one row per thread per period — `thread_id`, `period`,
  `topic` (reusing the *same* category taxonomy `stars.category` already uses, so this doesn't
  invent a second palette/taxonomy for Comet to reconcile against Constellation's), `mood`,
  `intent`, `resolved`, `classified_at`. Forced through a tool-call schema for structured output,
  not prose — same shape `tools/finalize_daily_items.go`/`finalize_pulsar_prompt.go` already use
  elsewhere in this codebase.
- **Trigger**: reuse `isDailyDue`'s exact shape (`gateway/pulsar_daily.go:616`) — a month-boundary
  check on the existing once-a-minute scheduler tick (`gateway/pulsar_scheduler.go`), not a new
  scheduling primitive. At month-end: find threads in the period with no Trail yet, run an Approach
  over each, write the row, then Comet generation reads this table instead of any transcript.
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
  `polaris comet classify -n N` against a new `/api/comet/classify?limit=N` gets the same
  try-before-you-commit workflow for free, no new CLI pattern needed.

**Status: mechanics settled, nothing implemented.** Explicitly flagged by the operator as needing
real testing against real threads (quality of topic/mood output, actual per-call cost, whether 500
chars of assistant reply is enough signal) before any schema is finalized or real implementation
starts — implementation is "not for another day or few" as of 2026-09-19. The `-n`-limited classify
path above is the first concrete thing to build, specifically so that testing can happen before the
`trails` schema is locked in.

## Cross-month behavior and remaining mechanics — settled 2026-09-19

**Schema correction: a Trail is keyed by `(thread_id, period)`, not `thread_id` alone.** Walking
through a concrete case surfaced this: a thread about fishing boats gets a Trail in September (at
turn 10). The same thread stays open and picks back up in October (now at turn 30, "I bought one").
That's a *second* Trail for the same thread, not an update to the first — each period a thread has
new activity in gets its own Trail. **Eligible for a Trail this period = thread has message activity
within the period AND has no Trail yet for `(thread_id, this period)`.** A thread quiet for months
then revived only gets a new Trail for the period it was actually active in, not a retroactive
rewrite of the old one.

**The classifier gets a `search_trails` tool.** Same shape as `tools/search_stars.go`'s existing
`stars_fts`-backed lookup, but over prior Trails instead of stars. Two jobs: (1) when a thread gets
a second Trail in a later period, the Approach run for it surfaces the thread's own prior
Trail(s) as context, so the new Trail can note "picked back up from last period's result: X, Y, Z"
instead of re-deriving the whole thread's history cold; (2) keeps topic/mood phrasing consistent
run over run generally, the same reason `search_stars` existing for Weaver's internal use matters —
without it, Trail-writing style would drift thread to thread with no shared reference point.
Toolbelt is now `{view_image, search_trails, record_classification}` — still forced to end via
`record_classification`, the other two are optional exploration steps first.

**Fields recorded per Trail: four, not two.** `topic` (reusing `stars.category`'s taxonomy as the
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
there); Weaver/shooting-star runs (`source = 'weaver'`, shipped via issue #90 — real thread rows
now, so this needs an explicit filter, not "they don't exist as threads"); star-edit runs (confirmed
to be one-off DB mutations from the Constellation UI, no `threads` row at all — no filter needed);
and ghost-mode turns, confirmed via `gateway/turn.go`'s `Anonymous` handling and `ghost_usage`'s own
schema comment ("a ghost turn has no event log to fall back on... the very thing ghost mode exists
to avoid") to never persist as an ordinary thread either. **Ghost mode gets exactly one number
surfaced in Comet: a plain count of that period's `ghost_usage` rows** — how many times ghost mode
was used, nothing about content — mirroring how `GetStats` already folds ghost spend into Polaris's
totals via that same genuinely-anonymous table, not new tracking machinery.

**Comet has a hard dependency on that period's Trails being complete — no partial-month tolerance
in production.** The whole premise (aggregating real signal instead of re-reading transcripts) needs
the full period's Trails present before Comet can generate anything worthwhile. Failed per-thread
Approach retries reuse Constellation's existing pattern exactly (`constellation_scheduler.go`'s
stale-run sweep / `needs_retry` marking) — failed threads get swept and retried before Comet
assembles that month's edition, not silently skipped.

**Approaches run concurrently, in small batches (~5 at a time), unlike Weaver.** Weaver
processes threads one at a time deliberately — a later shooting star run may need to see what an
earlier one already wrote to a star, since stars evolve as new developments occur across runs.
Approaches have no such cross-thread dependency: one thread produces exactly one Trail,
independent of every other thread's Trail that period, so there's no correctness reason to
serialize them. Batches of ~5 balance real parallelism against not hammering the LLM provider with
the full month's thread count at once.

**This concurrency choice has a direct testing-infra consequence.** `dev/fakeopenrouter`'s plain
FIFO queueing only works for genuinely sequential request order — Pulsar Daily's Stage A already
needed the `match`-substring targeting (pin a queued response to whichever request body actually
contains that text, ahead of FIFO and independent of queue position — see the package doc comment
in `dev/fakeopenrouter/main.go`) specifically because its own concurrent block-firing breaks FIFO
assumptions the same way. Since Approach batches are concurrent by design, **testing them through
`fakeopenrouter` needs `match`-based targeting from the start**, not as a later add-on — each batch
member's scripted response should be pinned to something identifying in its request (e.g. the
thread's own content/topic), not queue position.

## Throwaway test environment — settled 2026-09-19

**The test workflow deliberately stays close to how this codebase already verifies everything
else — real `/api/ask` calls against a real, throwaway server, not synthetic fixtures.** No new
seeding mechanism needed: `polaris run --config <path>` already loads any config file
(`cmd/run.go:61`), and that config's `database.path` field already controls where the DB lives —
the exact same override `gateway/testutil_test.go`'s `newTestHarness` already uses for its own
tempdir-isolated tests. So a throwaway Comet environment is just a second `config.yaml` pointing
`database.path` somewhere disposable (never the real `polaris.db`), same spirit as
`polaris benchmark --db <path>`'s isolation, just via the ordinary config mechanism instead of a
dedicated flag.

The actual workflow, agreed 2026-09-19 (and already exercised once, live, for issue #90's
verification):

1. Stand up `polaris run` against that throwaway config.
2. Claude has a real, varied set of conversations against its `/api/ask` endpoint — genuinely
   realistic thread content (covering different topics/moods, at least one thread with an image to
   exercise `view_image`, at least one pair of threads spanning a fake month boundary to exercise
   cross-month Trail continuation), not hand-authored fixture rows bypassing the real chat path.
   This is the same "verify against the real thing, not a mock" discipline this codebase already
   applies everywhere else (spiking APIs with `curl`, testing tools via `/api/ask`, live-verifying
   installers on real hardware) — applied here to seeding instead of to a finished feature.
3. Run `polaris constellation backfill` against that same throwaway DB to populate `stars` from
   those seeded threads, so the environment has real Constellation data too, not just raw threads.
4. Run the Comet classify pass (Approaches) against that DB, then Comet generation itself, and
   inspect the results — including opening an Approach's own hidden thread directly at its own
   `/t/<uuid>` to confirm the hidden-thread visibility actually works, something no Go unit test
   can show. (Weaver's own equivalent — a shooting star's hidden thread at `/t/<uuid>` — is exactly
   what issue #90 shipped and live-verified this same way, real bugs found and fixed included.)

Go-level unit tests (mirroring `pulsar_daily_pipeline_test.go`'s `newTestHarness` +
`sequencedSSEServer` scripted-response pattern) remain available as a faster, CI-friendly
complement once the mechanics are locked in enough to write assertions against — but the live
seeded-throwaway-server workflow above is the primary, agreed testing plan, not a fallback.

## Yearly cadence — settled 2026-09-19

**A year-end edition aggregates over that year's twelve already-computed monthly editions, not a
fresh classification pass.** The classification work (Approaches) has already happened twelve times
over by the time a year boundary is reached — re-running anything more expensive than reading back
that year's monthly output + underlying Trails would waste data that's already sitting there.
Cheap by construction once monthly is real, same "aggregate stored output, never re-derive"
discipline as everything else here.

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
the Approach classifier's own prompt the same way, with the same "extend rather than fragment,
check what's in use first" escape hatch and the same live currently-in-use interpolation. Left at
that until real Trail output over real months actually shows a gap — no attempt to enumerate the 50
now; that's prompt-writing work for implementation, not a planning decision.

## Open questions before this is buildable

Every open question from the original planning session — generation cadence/scheduling,
cost-control-per-run, yearly cadence, the test environment, star-edit runs' thread status, and
`mood`'s vocabulary — is settled. What's left, surfaced by the naming pass:

- **`trails` table naming/schema is still tentative** — chosen for consistency with the new
  Comet/Approach/Trail naming, not yet validated against a real schema pass.
- **`threads.source` value for an Approach's hidden thread** — likely `'comet_approach'` or
  `'comet'`, not yet picked; needs the same `ListThreads`/`ListThreadsPage` exclusion treatment
  `'weaver'` already got in issue #90.

## Next step

Planning is done — mockup reaction, classification mechanics, naming (including the icon), eligibility,
concurrency, and the test environment are all settled. Before any Comet implementation starts:

1. [Issue #90](https://github.com/AutumnsGrove/Polaris/issues/90) (Weaver/shooting-star transcript
   persistence) — **shipped** 2026-09-19, live-verified, two real bugs found and fixed along the
   way. Comet's classification eligibility filter depends on this pattern, now proven.
2. Stand up the throwaway test environment (see above), seed it via real `/api/ask` conversations
   plus a `constellation backfill` run, and build the classify pass against it before any `trails`
   schema is locked in.

Full implementation is intentionally not starting yet.
