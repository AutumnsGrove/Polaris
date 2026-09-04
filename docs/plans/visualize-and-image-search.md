# Visualize & Image Search — v1 plan

Two small, related additions: `visualize`, a tool that renders structured data as a chart instead
of prose, and `image_search`, a tool that returns real photos for a query instead of describing
them. Both came out of the same conversation (see the six concept renders reviewed before this
doc — a real Portland, OR forecast plus five illustrative chart kinds) and both are scoped
deliberately small: one tool each, not a family of chart-type-specific tools, matching the
project's existing tool-count discipline (nothing here should make tool selection harder than it
is today).

Atlas is out of scope for both — this is chat/Pulsar-surface work only.

## Why this exists

Today a tool's answer is always prose (or a `tools.Card` carousel for the four recommendation
tools). Two real gaps fall out of that:

- **Structured, already-fetched data gets flattened into text and thrown away.** `tools/weather.go`
  is the clearest case: `fetchWeather`'s `openMeteoResponse.Daily` already returns parallel arrays
  (`Time`, `TempMax`, `TempMin`, `PrecipProbMax`, `WeatherCode`) — a real time series — and
  `formatWeather` (weather.go:191) does nothing with it but write a bulleted list. The data was
  never the problem; only the rendering was.
- **There's no way to ask for a photo.** Every tool result is text-first. "Show me curtain-bang
  shag cuts" or "what does the new terrain look like" has no answer today beyond a text
  description with a citation link the user has to click through.

Both plug into two things already built: the follow-up-suggestion generator (today it can already
suggest a related question; it should be able to suggest "visualize this" the same way, once a
chartable answer is on screen) and Pulsar Daily's future slot system, which this doc deliberately
sizes for even though Pulsar Daily itself isn't being designed here.

## Part 1 — `visualize`

### Two tiers, not one

**Tier 1 — auto-attached, no tool call.** Whenever a tool's own response is already a time series,
the tool attaches a chart to its own result deterministically. No LLM decision, no extra
round-trip, always consistent. `weather` is the only Tier-1 source in v1 — see "Weather wiring"
below. A future structured tool (e.g. a stock/finance lookup, if that ever gets built) would do
the same thing for free once it exists.

**Tier 2 — model-invoked, for everything that isn't already structured.** A single `visualize`
tool, reached for when the model has synthesized chartable data itself (comparing figures across
several search results, building a timeline out of a research thread) rather than pulled it from
one structured source. This is the tool that has to earn its place in every turn's tool list, so
it needs to actually be rare — Tier 1 existing for the obvious cases is what keeps it rare.

### Tool shape

One tool, one nested schema — not one tool per chart kind, and not a flat pile of
kind-specific scalar params. `spawn_researchers` is the existing precedent that nested array
arguments work fine in this codebase's tool-calling (`tools/spawn_researchers.go`); this follows
the same shape.

```go
// tools/visualize.go (new)
var visualizeDef = llm.ToolDef{
	Type: "function",
	Function: llm.ToolFunctionDef{
		Name: "visualize",
		Parameters: map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"kind":     map[string]interface{}{"type": "string", "enum": []string{"line", "bar", "timeline", "meter"}},
				"title":    map[string]interface{}{"type": "string"},
				"x_label":  map[string]interface{}{"type": "string"},
				"y_label":  map[string]interface{}{"type": "string"},
				"series": map[string]interface{}{
					"type": "array",
					// [{ "label": string, "points": [{ "x": string|number, "y": number }] }]
					// used by line/bar only. bar accepts exactly one series in v1 — see
					// "v1 kind set" below.
				},
				"events": map[string]interface{}{
					"type": "array",
					// [{ "date": string, "label": string }] — "timeline" only. Not a
					// series/points chart at all (no axis, nothing plotted), so it gets
					// its own field rather than being wedged into series/points — see
					// "v1 kind set" below for why that was the wrong shape at first.
				},
				"value": map[string]interface{}{
					// { "current": number, "min": number, "max": number, "label": string } — "meter" only
				},
			},
			"required": []string{"kind", "title"},
		},
	},
}
```

`kind` is a closed enum, not open text — the frontend renderer switches on it, so an
unrecognized value has to fail tool validation, not fail silently at render time.

**Caps, enforced in the handler before `ctx.SetChart`/`AddCard` is ever called, not just at
render time:** `line`/`bar` series cap at 50 points each (a deliberately round starting number —
worth stress-testing against real turns before deciding whether 100 is a better ceiling; unlikely
to need to go higher than that), and `bar` additionally caps at 12 bars total across its one
series, tighter than the point cap because each bar needs real horizontal room in a fixed-width
card in a way a plotted line point doesn't. `timeline` caps at 20 events for the same
horizontal-room reason. A chart that exceeds any cap is a tool error, not a silent truncation —
`error: too many points (N) — visualize supports at most 50 per series. Reduce the count and
call again with fewer numbers.` — so the model can actually correct itself (aggregate, pick the
most important N) instead of a chart quietly rendering with data missing and no explanation.

### `ChartSpec` — the type both tiers produce

```go
// tools/registry.go, alongside Card (registry.go:509)
type ChartSpec struct {
	Kind   string        `json:"kind"` // "line" | "bar" | "timeline" | "meter"
	Title  string        `json:"title"`
	XLabel string        `json:"x_label,omitempty"`
	YLabel string        `json:"y_label,omitempty"`
	Series []ChartSeries `json:"series,omitempty"` // line, bar
	Events []ChartEvent  `json:"events,omitempty"` // timeline
	Value  *ChartValue   `json:"value,omitempty"`  // meter
}

type ChartSeries struct {
	Label  string       `json:"label"`
	Points []ChartPoint `json:"points"`
}

type ChartPoint struct {
	X interface{} `json:"x"` // string (date/category) or number
	Y float64     `json:"y"`
}

// ChartEvent is "timeline"'s own shape — deliberately not a ChartPoint.
// A timeline (see card 04 of the concept renders: a vertical list of
// dated entries, no axis, nothing plotted) has no numeric value to put
// in Y; the first draft of this doc had timeline sharing ChartPoint's
// shape with a mandatory-but-meaningless Y, which was wrong the moment
// it was checked against what actually got approved.
type ChartEvent struct {
	Date  string `json:"date"`
	Label string `json:"label"`
}

type ChartValue struct {
	Current float64 `json:"current"`
	Min     float64 `json:"min"`
	Max     float64 `json:"max"`
	Label   string  `json:"label"`
}
```

A turn produces at most one `ChartSpec` — unlike `Card`, which is a deduped-append list (multiple
recommendation cards make sense in one answer; multiple competing charts in one reply don't).

### Wire format — a new sibling to `Cards`, not a stretch of it

`tools.Card` (registry.go:509: `Title`/`Subtitle`/`ImageURL`/`URL`) is purpose-built for the
cover-art carousel and already reused as-is by `image_search` below — but it has no way to carry a
point series, and stretching it to do so would be the same mistake the Atlas plan's own
`DomainRankings` doc explicitly called out avoiding: *"kept as a separate type/file rather than
folding into `Blocklist`... a genuinely different concern... merging them would have meant
scope-creeping for no reason"* (`docs/plans/local-search-frontend.md`). `ChartSpec` gets the same
treatment `Cards` already has, one level over:

- `tools.Context` gets a `Chart *ChartSpec` field (`registry.go`, alongside `Cards []Card`) and a
  `SetChart(spec ChartSpec)` setter — last-write-wins, not append, per "at most one chart" above.
- `gateway/protocol.go`'s `ServerEvent` (protocol.go:161) gets `Chart *tools.ChartSpec
  \`json:"chart,omitempty"\`` next to the existing `Cards []tools.Card` field (protocol.go:179).
- `gateway/turn.go`'s tool-event payload switch (turn.go:290-307, the same block that already
  does `if v, ok := payload["cards"].([]tools.Card); ok { evt.Cards = v }`) gets one more case for
  `"chart"`.
- `store.Store` gets `SetMessageChart(messageID int64, chartJSON string) error`, a direct copy of
  `SetMessageCards` (store.go:1280) against a new `messages.chart` column, called from the same
  place `turn.go:624` already calls `SetMessageCards`.

Every one of these is additive next to an existing, working field — nothing about `Cards`'
plumbing changes.

### Rendering — one component, not one per kind

`ChartCard.svelte` (new, same tier as `RecommendationsCarousel.svelte`), switching on `kind`
internally, drawn as inline SVG — no charting library, matching the hand-rolled-over-SDK instinct
already established elsewhere in this codebase (the R2 client, the calculator's own evaluator).
`line`/`bar` share the same `series[].points[]` iteration in the component; `timeline` reads
`events` instead (a list, not a plot — see the `ChartEvent` note above); `meter` reads `value`.
Respects `prefers-reduced-motion` for free by not animating unless asked to. `meter`'s fill color
reuses the exact ≥90%-of-max "hot" threshold `ThreadMenu.svelte` already applies to its
context-usage readout (`.info-row.hot`, switching to `--color-danger`) rather than inventing a
second warning threshold — "getting close to a cap" should look the same everywhere in the app.

### Weather wiring (Tier 1's only v1 source)

`handleWeather` (weather.go:104) already has every number a chart needs in `forecast.Daily` by
the time it calls `formatWeather`. When `len(forecast.Daily.Time) > 1`, it also calls
`ctx.SetChart(...)` with a `ChartSpec{Kind: "line", ...}` built from the same arrays
`formatWeather` already iterates — genuinely zero new fetch code, one new block in a function that
already has the data in scope.

### v1 kind set — closed on purpose

`line`, `bar`, `timeline`, `meter`. Explicitly **not** in v1: `pie`, `scatter`, `heatmap` — no
current use case justifies them, and each one is a chance for the model to pick the wrong kind
inside the one tool that's supposed to stay simple. Also explicitly not in v1: a `compare` kind
(a structured multi-item table/card-grid — closer to Kagi News' "Perspectives" quote cards and
green "Action items" box than to a plotted chart). Real candidate for v2, flagged specifically
because Kagi News is the stated reference for Pulsar Daily, but it's a different kind of
rendering problem (formatted content, not point data) and doesn't belong in `visualize`'s schema.

`bar` is single-series only in v1 — the schema doesn't stop the model from passing more than one,
but the handler rejects it. Grouped/stacked bars are real rendering complexity `ChartCard.svelte`
doesn't need yet and nothing in the six concept renders showed one; a real v1.5/v2 candidate once
a use case for comparing two series side by side actually shows up, not before.

### Follow-up suggestions

No new mechanism — `SetMessageSuggestions` (store.go:1298) and the suggestion-generation prompt
already produce candidate follow-ups per turn (the same system Atlas's Quick Answer bug-fix
writeup in `docs/plans/local-search-frontend.md` references). The suggestion prompt needs one more
instruction: when the answer just given has chartable structure, one candidate suggestion should
be "Visualize this as a chart." Text-only prompt change, no new plumbing.

## Part 2 — `image_search`

### Why a separate tool, not a `web_search` flag

Different result shape entirely — thumbnail URL, source page, title, source domain — not
`web_search`'s snippet-plus-link shape. Forcing it into `web_search`'s return type would blur two
genuinely different jobs.

### Fallback chain — two tiers, and why

| Provider | Verdict | Reasoning |
|---|---|---|
| SearXNG | primary | `images` category confirmed live already (`docs/plans/local-search-frontend.md`'s Atlas plan notes it was "confirmed working live," just never wired to anything) — free, first try. |
| Brave | fallback | Confirmed real, separate endpoint: `https://api.search.brave.com/res/v1/images/search`, same `X-Subscription-Token` auth `brave/brave.go`'s existing web client already uses. Brave's own marketing page lists Images under the same "Search" $5/1,000-request plan as Web/Videos/News/Suggest/Spellcheck — one product, one price tier, most likely one key. |
| Parallel | **excluded** | Confirmed no image product — their API surface is `/search`, `/extract`, `/crawl`, `/map`, `/research`, text-and-citation only. |
| Tavily | **excluded**, per explicit decision | Does technically support images via `include_images=true`, but only as images already embedded on the pages a *text* search returned — illustrating an article, not "find me photos of X" on its own. Wrong shape for this tool. Skipped entirely, not attempted as a third tier. |

So: `image_search` tries SearXNG's `images` category, and on a degraded SearXNG falls straight to
Brave's image endpoint. No third tier.

### Cap — unified with the existing Brave web-search cap, deliberately conservative

Brave's own docs would not confirm, one way or the other, whether Image Search draws from the same
1,000/mo query pool as Web Search or is tracked separately (their dashboard refers to Web Search
and Image Search as distinct "Service APIs" within one framework, which could mean either). Rather
than build a second counter on an assumption that might be wrong in the *permissive* direction —
i.e. assuming separate pools when they're actually shared, and quietly overspending — `image_search`
increments and checks the exact same `store.Store.IncrementAPIUsage("brave")` /
`GetAPIUsage("brave")` / `brave.MonthlyCap` calls `tools/web_search.go`'s existing Brave fallback
already uses. One shared budget across both tools' Brave usage, checked before every Brave image
call the same way `web_search` already checks it before every Brave web call. If it later turns
out the two really are billed separately, splitting the counter is a strictly safe loosening to
make later; assuming separation now and being wrong would not be.

`image_search` doesn't set Brave's `safesearch` param at all in v1 — takes Brave's own default
rather than deciding a stance on it now. If that default turns out to be wrong for something in
practice, the fix is hardcoding `safesearch=off`, not adding a settings-panel toggle for a
one-person tool to fiddle with.

### Tool shape and response

```go
// tools/image_search.go (new)
var imageSearchDef = llm.ToolDef{
	Type: "function",
	Function: llm.ToolFunctionDef{
		Name: "image_search",
		Parameters: map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"query": map[string]interface{}{"type": "string"},
				"count": map[string]interface{}{"type": "integer"}, // default ~10
			},
			"required": []string{"query"},
		},
	},
}
```

Brave's image endpoint defaults to `count=50` and allows up to 200 — far more than a gallery
should ever show. `image_search` requests a small, fixed `count` (10 is enough headroom above the
~5 tiles actually rendered to survive a little client-side filtering) rather than reusing
`brave.MaxCount` (20, `web_search`'s per-request ceiling, tuned for a different product/response
size).

### Reusing `Card`, with one small addition

Each image result is a `Title` (alt text/caption), `Subtitle` (source domain), `ImageURL`
(thumbnail), `URL` (source page) — that's `tools.Card`'s existing four fields, unchanged. So
`image_search` calls the existing `ctx.AddCard(Card{...})` (registry.go:518), the existing
`Cards`/`cards` plumbing (protocol.go:179, store.go:581, `SetMessageCards`) carries it exactly as
it already does for movies/music/books, and `evt.Cards` shows up in the same wire event.

The one real gap: `RecommendationsCarousel.svelte` renders `Cards` as a horizontal cover-art strip
— right for a curated recommendation, wrong for a raw multi-source image gallery (card 06 of the
concept renders: a grid with a per-tile source chip, not a carousel). `Card` needs one small,
additive, default-preserving field:

```go
type Card struct {
	Title    string `json:"title"`
	Subtitle string `json:"subtitle,omitempty"`
	ImageURL string `json:"image_url,omitempty"`
	URL      string `json:"url"`
	// Kind selects which frontend treatment renders this card. Empty/omitted
	// means "media" — today's carousel behavior, unchanged for the four
	// existing callers (music/movies/books/... never set this field).
	Kind string `json:"kind,omitempty"` // "" (media, default) | "image"
}
```

`image_search` sets `Kind: "image"` on every card it adds. If a turn ever produces both kinds (a
recommendation call and an image search in the same turn), the frontend partitions `Cards` into
two groups by `Kind` rather than trying to preserve call-order interleaving, and renders each
group through its matching component — one `RecommendationsCarousel` block, one `ImageGallery`
block, whichever are actually present. Additive, non-breaking, and the four existing
recommendation tools need zero changes.

### No vision model involved, on purpose

The model's only decision is textual — "this question is about a hairstyle/an object/a place,
photos would help" — the same shape of decision it already makes for every other tool call, and
one DeepSeek V4 Flash (the default model, text-only) handles fine. The gallery is a deterministic
pass-through of whatever SearXNG/Brave ranked, not a curated selection — matching the observed
claude.ai behavior this was modeled on. `config.MultimodalModel` (MiMo Pro) is the only model in
the roster that could actually look at candidate thumbnails and choose the best ones instead of
trusting raw ranking; that's real, explicitly deferred v2 work (see below), not a v1 gap.

## Availability by mode

Both tools are offered in the normal case — real chat threads and Pulsar pulses, which run
through the identical turn pipeline (`docs/plans/pulsar-routines.md`: "same cost tracking, same
citations, same everything"). Two narrower contexts cut that down, using two different existing
mechanisms rather than one new one:

- **Chat mode (`ctx.NoResearch`) — `visualize` stays, `image_search` doesn't.** `catalog.go`'s
  `offered()` (catalog.go:73) already bulk-excludes every tool tagged `category: research` — the
  same mechanism `web_search`/`weather`/the four recommendation tools already use, no per-tool
  exclusion list to maintain. `image_search` gets `category: research` in its own YAML (it reaches
  out to SearXNG/Brave — the textbook case that category exists for) and is excluded from chat
  mode for free. `visualize` gets no category at all: given data already in the conversation, it
  doesn't fetch anything, so there's no reason chat mode should lose it — "chart the numbers I
  just gave you" doesn't need research to be re-enabled.
- **The Pulsar wizard — neither.** The wizard already explicitly disables non-research tools that
  don't fit its ephemeral prompt-writing interview: `gateway/pulsar_wizard.go`'s `disabled` map
  (pulsar_wizard.go:184-190) turns off `calculator`/`memory`/`read_attachment` by name, with a
  comment explaining exactly why `NoResearch` alone isn't enough (`NoResearch already excludes
  every "research"-category tool... but NoResearch alone would still leave calculator/memory/
  read_attachment on the menu, which a prompt-writing interview has no use for`). `visualize` is
  the same case — not research-tagged, not useful mid-interview — so it joins that same map:
  `disabled["visualize"] = true`. `image_search` needs nothing added there; being
  `category: research`, `NoResearch` (which the wizard already sets, pulsar_wizard.go:193)
  excludes it the same way it does in plain chat mode.

Net: one new line in an existing map (`gateway/pulsar_wizard.go`), one YAML field
(`image_search.yaml`'s `category: research`), and no change at all to `catalog.go`'s gating logic
itself — both tools slot into mechanisms that already exist for exactly this purpose.

## Explicitly out of scope for v1

(Revisit later, not forgotten — matches this repo's existing scoping convention.)

- **`compare` chart kind** (structured multi-item table/cards, the Kagi News "Perspectives"/"Action
  items" pattern) — different rendering problem than plotted data; real v2 candidate.
- **Multi-series `bar` (grouped/stacked bars)** — v1 is single-series only, enforced in the
  handler; a real v1.5/v2 candidate once a concrete comparison use case needs it.
- **`pie` / `scatter` / `heatmap` kinds** — no current use case; add only if something built
  actually needs one.
- **Vision-curated image selection** — hand candidate thumbnails to `config.MultimodalModel` and
  let it choose/order the best ones instead of trusting raw search-engine ranking. Real v2 idea,
  deliberately not v1: v1 mirrors the simpler, model-blind reflex pattern on purpose.
- **A separate `brave_images` usage counter** — start unified with the existing `brave` counter
  (see "Cap" above); split it out later only if it's confirmed the two are actually billed
  separately and unifying them is costing real quota unnecessarily.
- **Tavily's `include_images` piggyback path** — real capability, wrong shape for "search for
  photos of X" on its own; not worth a third fallback tier for what it'd actually return.
- **Auto-attached charts from any tool besides `weather`** — a future stock/finance tool would get
  this for free once it exists; nothing to build now with no second data source in hand.
- **Any self-introspection / stats-querying tool** ("how many searches have I done on X this
  month") — came up in the same conversation as a "cool, but not now" idea; needs its own design
  pass on tool shape (a dedicated stats-query tool, not shell access) whenever it's actually
  picked up.

## Next steps

1. `tools.ChartSpec`/`ChartSeries`/`ChartPoint`/`ChartEvent`/`ChartValue` types +
   `Context.Chart`/`SetChart` (`tools/registry.go`, alongside `Card`/`AddCard`).
2. `ServerEvent.Chart` (`gateway/protocol.go`) + the one new case in `turn.go`'s payload switch
   (turn.go:290-307) + `store.SetMessageChart` (mirroring `SetMessageCards`, store.go:1280) +
   `messages.chart` column.
3. `tools/visualize.go` — tool def + handler: `kind` validation against the closed v1 enum, the
   50-point-per-series / 12-bar / 20-event caps (rejected with a clear tool error, not truncated),
   and the single-series check for `bar`. No `category` set (stays available in chat mode).
4. Weather auto-chart: `handleWeather` calls `ctx.SetChart(...)` from `forecast.Daily` when more
   than one day was requested.
5. `ChartCard.svelte` — one component, switches on `kind` (`line`/`bar` read `series`, `timeline`
   reads `events`, `meter` reads `value`), inline SVG, real `--color-*` tokens, meter reuses
   `ThreadMenu.svelte`'s existing ≥90% `.info-row.hot` treatment for its warning state.
6. Suggestion-generation prompt: add the "visualize this" candidate for chartable answers.
7. `tools.Card.Kind` field (default-preserving) + `tools/image_search.go` (SearXNG images →
   Brave images, unified `brave` usage cap, no `safesearch` param set, no Parallel/Tavily) +
   `image_search.yaml`'s `category: research` + `brave/` package gets a sibling image-search
   method alongside its existing `Search`.
8. `ImageGallery.svelte` — grid treatment for `Kind: "image"` cards; frontend partitions a
   message's `Cards` into two groups by `Kind` (not preserving call-order interleaving) and
   renders one `RecommendationsCarousel` block and one `ImageGallery` block, whichever are
   present.
9. `gateway/pulsar_wizard.go`'s existing `disabled` map (pulsar_wizard.go:184-190) gets one more
   line: `disabled["visualize"] = true`, with its neighboring comment updated to name it alongside
   calculator/memory/read_attachment. `image_search` needs no change there — `category: research`
   plus the wizard's existing `NoResearch: true` already excludes it.
10. Docker two-sided sync checklist (per `CLAUDE.md`) — n/a for this slice: no new hot-editable
    resource files, no new CLI command, no new settings-panel server-mutating action. Worth
    re-checking once `image_search`'s Brave wiring lands, in case a new API key needs adding to
    `.env.example`/`compose/polaris/config.yaml.example`'s existing Brave key handling (it likely
    doesn't — same key, per "Cap" above — but confirm once Brave's dashboard behavior is actually
    observed with a real key, not just docs).
