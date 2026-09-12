# Themed/special daily editions

**Status: designed, not yet implemented.** Answers issue #37's design questions (how a
day-specific template is configured, scheduled, and rendered) with concrete decisions against the
real Stage A-D pipeline in `gateway/pulsar_daily.go`. A mockup of the alternate layout lives at
`mockups/daily-special-edition.html`. Confirmed in a live discussion: single special day for v1
(not a list), and a hybrid topic-source model — see "Topic source: hybrid" below.

## Scope for v1: one special day, not a general templating system

The issue's own framing ("e.g. a Sunday special") is one alternate shape, not N per-weekday
templates. Building a generic "configure any subset of weekdays with their own block layout"
system on day one is scope this feature hasn't earned yet — a single optional special-day slot
(which weekday, on/off, one deep-dive topic) covers the actual ask and is trivially extensible to
more slots later if it turns out to matter. Don't build the general case speculatively; this
follows the same restraint CLAUDE.md's own conventions favor elsewhere in this codebase.

## Configured

Five new fields on `store.PulsarDailyConfig` (`store/pulsar_daily.go:20`), same shape as the
existing `WeatherLocation`/`CustomBlocks` fields:

```go
SpecialEditionEnabled bool          `json:"special_edition_enabled"`
SpecialEditionWeekday time.Weekday  `json:"special_edition_weekday"` // 0=Sunday..6=Saturday; meaningless if !Enabled
SpecialEditionTopic   string        `json:"special_edition_topic"`   // free-text deep-dive steer, same "no fixed key" shape as PulsarDailyCustomBlock.Instructions
// StaleNote/StaleSuggestion are set by flag_topic_staleness (see "Topic
// source: hybrid" below) — a Weaver-review-style suggestion sitting
// beside the topic, not inside it. Both nil/empty in the normal case.
SpecialEditionStaleNote       *string `json:"special_edition_stale_note,omitempty"`
SpecialEditionStaleSuggestion *string `json:"special_edition_stale_suggestion,omitempty"`
```

Settings surface: one new section in `PulsarDailyConfigModal.svelte`, alongside the existing
enabled-blocks/custom-blocks editor — a toggle, a weekday picker, and a text area for the topic
steer. When `StaleNote` is set, the same section shows a dismissible suggestion banner (the note
plus the suggested replacement, with "Use this topic" / "Keep current topic" actions) rather than
a second, separate UI surface. No new route or standalone settings page.

## Topic source: hybrid — fixed by default, model can flag staleness

Considered three shapes for where the weekly topic comes from: fully fixed (you write it once,
never changes), fully model-chosen (you just enable the feature, the model picks something new
each week), or a rotating list you maintain. Landed on a fourth, hybrid option instead: **the
topic stays fixed and under your control** (predictable, and there's somewhere to steer it if the
model picks something dull), **but the model can flag when it's run dry** rather than silently
producing a thinner and thinner deep-dive on an exhausted topic — direction stays yours, the model
just surfaces when it's worth reconsidering.

Mechanically: `runSpecialDailyPipeline`'s agent run gets one additional optional tool,
`flag_topic_staleness` (`reason`, `suggested_replacement`) — same "force structured output through
a tool call instead of parseable prose" pattern this codebase already uses for
`finalize_daily_items.go`/`finalize_pulsar_prompt.go`, so a staleness flag never leaks into the
actual rendered feature content the way an inline aside would. Called only when the model judges
the topic genuinely exhausted (e.g. "this is the fourth week straight covering the same narrow
angle with nothing new to add"), not as a routine check-in every week. A call writes
`SpecialEditionStaleNote`/`SpecialEditionStaleSuggestion`, surfaced as the settings-panel banner
above; it does **not** auto-switch the topic — that would hand direction to the model despite the
whole point of keeping this fixed-by-default. The fields clear once the banner's dismissed or a
new topic is saved.

## Scheduled

**No changes to `isDailyDue` or the due-check/locking machinery** — `startDailyGenerationIfIdle`,
`SetDailyLastGenerated`, and `dailyGenerationRunning`'s CAS guard (`gateway/pulsar_daily.go:574-633`)
stay exactly as they are; a special edition is still "one edition per day, at `time_of_day`," not a
second schedule to reconcile against the first. The fork happens inside `runDailyPipeline` itself,
right after `today := time.Now().Format("2006-01-02")` (`gateway/pulsar_daily.go:671`):

```go
if cfgRow.SpecialEditionEnabled && time.Now().Weekday() == cfgRow.SpecialEditionWeekday {
    s.runSpecialDailyPipeline(reqCtx, cfg, cfgRow, today)
    return
}
// ...existing Stage A-D masonry pipeline, unchanged
```

## Generated

`runSpecialDailyPipeline` is a new, separate function — not a variant of the existing Stage A
loop — since a deep-dive is a fundamentally different shape (one long-form piece, not N
independent short blocks racing `dailyBlockSem`). It reuses the same primitive Stage A blocks
already use for research (`dailyBlockResearch`'s narrow-toolset `agent.Run`), just with:

- **One task, not N** — no `dailyBlockSem` fan-out, no per-block concurrency.
- **A wider leash** — more turns/tokens than a normal Stage A block gets, similar in spirit to
  Deep Research's Tier 1 widened leash (`docs/plans/deep-research-two-tier.md`), since "go deep on
  one topic" is explicitly the point, unlike Stage A's deliberately narrow per-block asks.
- **`cfgRow.SpecialEditionTopic` verbatim as the task**, same "no fixed key, entire task is
  user-authored text" shape `dailyBlockCustom` already uses for `CustomBlocks` — no new prompt
  template needed, just a longer-leash agent run over the same free-text-instruction mechanism.
- **`flag_topic_staleness` offered alongside the normal toolset**, restricted to this run the same
  way Weaver's five tools are restricted to `weaver_run` (`tools/registry.go`'s `requires` gating)
  — see "Topic source: hybrid" above.
- **No Stage B/C** (Top Story election/elaboration) — there's only one piece of content; it *is*
  the day's story, nothing to rank it against.
- **No diff-judge against literal "yesterday"** — comparing a Sunday special to Saturday's
  ordinary masonry edition is comparing two different shapes; if repeat-avoidance matters here at
  all, it should compare against the *last special edition* (i.e. last week's, not yesterday's),
  a genuinely separate lookup from `LatestDailyEdition`'s existing "day before" semantics. Left as
  an explicit v2 candidate rather than bolted on incorrectly now — a special edition repeating
  itself week to week is a real but much lower-frequency problem than a daily block repeating
  itself day to day.

## Rendered

**A new `Kind` field on the edition row** (`store.PulsarDailyEdition`, `store/pulsar_daily.go:194`
— `"standard"` default via the existing column-default pattern, `"special"` for a deep-dive),
persisted through `UpsertDailyEdition` alongside the existing `blocks`/`cost_usd` columns. The
frontend (`web/src/routes/daily`) branches on this field: `"standard"` renders the existing
masonry board unchanged; `"special"` renders a single-column, magazine-style feature layout with
no grid — see `mockups/daily-special-edition.html` for the concrete shape (a large title/deck, one
continuous piece, sources at the bottom matching the existing citation convention). This is a real
new Svelte component (`SpecialEditionView.svelte` or similar), not a CSS-only variant of the
existing board component, since the content shape itself (one document vs. N cards) is different,
not just its presentation.

## Open items for implementation

- Real migration: `special_edition_enabled`/`special_edition_weekday`/`special_edition_topic`/
  `special_edition_stale_note`/`special_edition_stale_suggestion` columns on the daily-config
  table, `kind` column (with a backfill default of `"standard"` for every existing row) on
  `pulsar_daily_editions`.
- `runSpecialDailyPipeline`'s own function, plus its prompt tuning (how much steering beyond the
  raw topic text it needs, and how conservative `flag_topic_staleness`' calling criteria should be
  — a tool that fires too eagerly is just noise in the settings panel) — not written yet.
- `tools/flag_topic_staleness.go` + `tools/descriptions/flag_topic_staleness.yaml` — not written.
- The "compare against last special edition, not yesterday" anti-repeat logic — explicitly
  deferred above, not required for a v1 ship.
- `SpecialEditionView.svelte` — new component, not started.
- CLAUDE.md's dual-deployment checklist: no new hot-reloaded resource file, no new CLI command, no
  new settings-panel deployment action — this is pure generation-pipeline + frontend-rendering
  work, so the checklist's Docker-specific items don't apply here.
