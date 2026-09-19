# Tides — monthly retrospective

**Status: mockup in review, not yet designed as a build.** Named and scoped in a live brainstorm
2026-09-19 — see `docs/plans/crazy-ideas.md` for the naming history (rejected: Perihelion, Almanac,
Long Exposure, Logbook, Field Notes) and the operator's scoping verdict this doc builds on. A first
visual mockup lives at `mockups/tides.html` (monthly cadence, all four blocks below — the "full
page" density option the operator picked over a single stat card or a stats+chart-only cut).
Nothing here is implementation-ready yet; this is the shape to react to before any schema/tool
work starts.

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

## Open questions before this is buildable

- **Generation cadence/scheduling.** Almost certainly reuses the existing once-a-minute scheduler
  shape (`gateway/pulsar_scheduler.go`/`gateway/pulsar_daily.go`'s `isDailyDue`-style check) with a
  month-boundary check instead of a day-boundary one — not a new scheduling primitive.
- **Cost control per run.** Needs to stay an aggregation pass over `search_history`/`stars`/
  `api_usage`-derived `Stats`, not a raw re-read of the period's threads/messages — that's both the
  cheap path and the one that structurally can't balloon into a prose wall.
- **Yearly cadence.** Explicitly deferred per the operator's own call — monthly is the harder
  minimalism test and ships first; yearly should reuse the same layout once monthly is proven, not
  get designed in parallel.

## Next step

React to `mockups/tides.html` — does the block order, the spiked/faded framing, and the capped-star
showcase feel right, or does something need to move/go before this gets a real schema pass.
