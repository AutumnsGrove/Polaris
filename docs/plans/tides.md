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

## Open questions before this is buildable

- **Spike/fade threshold.** "What counts as a spike" needs an actual definition (raw count
  over some window vs. count relative to a personal baseline) — this is the one place the page
  does more than aggregate, and it's the one place a bad heuristic would show up as obviously
  wrong ("spiked" topics that are just noise, or missing an obvious real spike).
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
