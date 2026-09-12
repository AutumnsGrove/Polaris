# Shopping Mode — v1 plan

A shopping-flavored surface built entirely out of pieces that already exist: `web_search`/
`web_read` for discovery (no new fetcher, no scraper), a new focus mode for steering, and one new,
deliberately generic tool — `highlight` — whose only job is to turn links the model already
verified into a masonry grid of cards. Shopping is `highlight`'s first caller, not its only reason
to exist: the tool itself knows nothing about products, prices, or retailers, so any future
"here are the best few things I found" use case (places, repos, articles) can reuse it for free.
No Amazon-specific code anywhere in this plan.

## Why this exists, and what's already been tested live

The obvious first idea — a dedicated Amazon-scraping tool, mirroring `web_read`'s
`fetchAndExtract` — was tested against the real site before any code was written, per this repo's
"verify on real hardware" culture, and the result rules it out on two independent grounds:

- **Technical**: Amazon's own search endpoint (`/s?k=...`) is behind Akamai Bot Manager. A plain
  GET with `web_read`'s exact User-Agent gets HTTP 200 back, but the body is a ~1.3KB interstitial
  shell — a JS proof-of-work challenge that POSTs to `/_sec/verify` before showing anything. No
  headless browser is run locally today (see `web_read.go`'s doc comment on why — staying light on
  the potato), so this endpoint is a dead end regardless of policy. Individual product pages
  (`/dp/{ASIN}`) are *not* behind this wall — same request, full page, real price/title markup —
  but that only matters if a URL to one is already in hand, which is exactly what `web_search`
  already gets the model for free.
- **Policy**: robots.txt doesn't blanket-disallow `/s` or `/dp`, but Amazon's Conditions of Use
  separately prohibit automated data collection regardless of whether a technical wall happens to
  catch it. That's a real constraint independent of what Akamai does or doesn't block, and the plan
  below is scoped to avoid it entirely — Polaris never talks to Amazon's (or any retailer's) search
  or listing endpoints directly, only individual pages the model reaches via its own `web_search`
  results, exactly like every other citation it already produces.

Two API lookups came out of the same conversation, worth recording so they aren't re-litigated
later:

- **SearXNG has no shopping category.** Confirmed against the current engine docs — categories are
  general/images/videos/news/maps/music/IT/science/files/social media, full stop. `web_search`'s
  `category` argument is a raw pass-through to SearXNG's `&categories=` param (`search/searxng.go`),
  but only `general`/`news` are exposed to the model today (`tools/web_search.go`'s enum) because
  those are the only two SearXNG categories this deployment actually has engines for. Adding a
  `"shopping"` value to that enum in v1 would be a no-op at best (no backing engines) and misleading
  at worst — **not done here**.
- **Brave's Web Search API does carry a real signal, but it's unverified.** Its response schema
  documents a `product`/`product_cluster` object described as "products and reviews found on the
  web search result page." That's potentially real structured price/rating data for free on top of
  the existing SearXNG→Brave fallback chain — but Brave's public docs don't enumerate the object's
  actual fields, and confirming them costs a real, billed call against the capped `brave` monthly
  quota (`brave.MonthlyCap`, shared with `web_search`/`image_search`'s existing usage). Not spent
  speculatively here — see "Explicitly out of scope" below. **v1 does not depend on this**; it's a
  clean v1.5 addition if the spike test confirms something worth wiring up.

Net effect on this plan: discovery stays exactly what it already is (`web_search`, optionally
`web_read` on a promising candidate), and the only new thing is how the *result* of that discovery
gets promoted into a masonry grid instead of prose.

## The actual question: cards without a dedicated tool

Not possible, and worth stating plainly since it's the crux of the design: every `Card` on screen
today comes from `ctx.AddCard()`, called inside a tool handler (`tools/registry.go`'s `Card` type,
populated by `music.go`/`books.go`/`movies.go`/`image_search.go`). The model's plain reply text has
no path to that struct. So a tool is required no matter what — the only real design freedom is how
thin (and how narrowly scoped) it is. Given the Amazon/SearXNG/Brave findings above, it can be about
as thin as a tool gets — zero network calls of its own, a pure "promote these already-fetched links"
shim — and, per the naming decision below, there's no reason to scope it to shopping at all.

## `highlight` — a generic, reusable card tool, not a shopping tool

Named for what it does, not what it's used for today: shopping is the first real caller, but the
tool has no idea what a "product" is. It takes generic `{title, url, price?, image_url?}` items and
turns them into cards — useful anywhere the model wants to say "here are the best few things I
found" instead of a paragraph, which is a shape that shows up well beyond shopping (comparing
places, repos, articles — anything `web_search`/`web_read` already surfaces real links for). Keeping
it generic now avoids the alternative of a `highlight_products`-shaped tool needing a near-identical
sibling (`highlight_places`? `highlight_repos`?) the first time a second use case actually shows up —
one tool, one schema, reused.

### Shape

```go
// tools/highlight.go (new)
var highlightDef = llm.ToolDef{
	Type: "function",
	Function: llm.ToolFunctionDef{
		Name: "highlight",
		Parameters: map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"items": map[string]interface{}{
					"type":     "array",
					"minItems": 1,
					// [{ "title": string, "url": string, "price": string?, "image_url": string? }]
					// price is optional and free text ("$129.99", "£45", "~$40, limited stock") —
					// not a structured amount+currency pair, since nothing downstream sorts or
					// computes on it. It's the one shopping-flavored field on an otherwise generic
					// item shape; a future non-shopping caller just leaves it empty. image_url is
					// also optional — a missing one renders the same placeholder tile
					// RecommendationsCarousel already falls back to (Card.ImageURL empty).
				},
			},
			"required": []string{"items"},
		},
	},
}
```

**Every `url`/`title`/`price` must come from something the model actually read this turn** —
`web_search` results or a `web_read` page — never recalled from training data. That's a prompt-level
instruction (in the tool's `api_description`, loaded from `tools/descriptions/highlight.yaml` same
as every other tool), not something the schema can enforce, but it's the one thing that keeps this
tool from turning into a generator of confident-looking but made-up cards. It's a real, acknowledged
limitation of a zero-fetch tool — see "Explicitly out of scope" for why v1 accepts it rather than
adding verification.

### Handler

No HTTP client, no fetch — the entire handler is validation plus `ctx.AddCard`:

- Reject with a tool error if `items` is empty or exceeds a hard cap of **5** (`"error: too many
  items (N) — highlight supports at most 5. Pick the strongest candidates and call again with
  fewer."`, matching `visualize`'s reject-don't-truncate pattern) — five is the actual ask ("top 3
  or 5"), not a soft target with headroom above it.
- Each item with an empty `title` or `url` is a tool error, same "fail the whole call, not silently
  drop one entry" posture as `web_read`'s required-field checks.
- `ctx.Blocklist.Blocked(url)` is checked per item, same guard `web_read` already applies before
  fetching anything — cheap, and there's no reason a known-bad source should get promoted to a
  visually prominent card just because this tool never fetches it itself.
- For each surviving item: `ctx.AddCard(Card{Title: i.Title, Price: i.Price, ImageURL: i.ImageURL,
  URL: i.URL, Kind: "highlight"})`. `AddCard`'s existing de-dupe-by-URL (`registry.go`) applies for
  free if the model calls this more than once in a turn.

### Availability — deliberately not `visualize`'s pattern

`visualize`'s doc (`docs/plans/visualize-and-image-search.md`) gives it no `category`, reasoning
that a tool which only renders data *already in the conversation* is safe in chat mode
(`NoResearch`) even with fetching tools off. `highlight` looks like the same shape but isn't: every
item it renders implicitly claims "this is something real I just found," which is only trustworthy
immediately downstream of a real `web_search`/`web_read` call. Giving it `category: research` in
`tools/descriptions/highlight.yaml` means `NoResearch` drops it together with the tools it depends
on, rather than leaving it on the menu as a way to produce a confident-looking card with no live
source behind it. `catalog.go`'s `offered()` needs no code change for this — it's a one-line YAML
field, same mechanism `image_search.yaml` already uses.

## `Card` gets a `Price` field and a third `Kind`

```go
// tools/registry.go, extending the existing Card struct
type Card struct {
	Title    string `json:"title"`
	Subtitle string `json:"subtitle,omitempty"`
	ImageURL string `json:"image_url,omitempty"`
	URL      string `json:"url"`
	Kind         string `json:"kind,omitempty"` // "" (media) | "image" | "highlight"
	FullImageURL string `json:"full_image_url,omitempty"`
	// Price is set only by highlight (Kind "highlight") — optional free text,
	// not a structured amount, per highlight.go's doc comment on why. Empty
	// for every existing caller and for any non-shopping highlight item,
	// unchanged default.
	Price string `json:"price,omitempty"`
}
```

Additive and default-preserving, same posture `Kind`/`FullImageURL` were added with for
`image_search` — the four existing recommendation tools and `image_search` need zero changes.

## Frontend — `HighlightGrid.svelte`, masonry like `image_search`, link-out like the carousel

`ChatTurnView.svelte`'s card partition (currently two-way: `mediaCards`/`imageCards`,
ChatTurnView.svelte:47-48) becomes three-way:

```ts
let mediaCards = $derived((turn.cards ?? []).filter((c) => c.kind !== 'image' && c.kind !== 'highlight'));
let imageCards = $derived((turn.cards ?? []).filter((c) => c.kind === 'image'));
let highlightCards = $derived((turn.cards ?? []).filter((c) => c.kind === 'highlight'));
```

...with a third conditional block rendering `<HighlightGrid cards={highlightCards} />` alongside the
existing `RecommendationsCarousel`/`ImageGallery` ones — same "whichever groups are actually
present" posture already documented at ChatTurnView.svelte:42-46.

`HighlightGrid.svelte` (new) is `ImageGallery.svelte`'s masonry (`columns: 2 180px` CSS
multi-column, not a uniform grid — real photos vary in aspect ratio same as search-result photos
do) with two changes from a straight copy:

- Each tile is a real `<a href={card.url} target="_blank" rel="noreferrer">`, not a button that
  opens a lightbox (`RecommendationsCarousel.svelte`'s link-out pattern, not `ImageGallery.svelte`'s
  preview-then-link pattern) — there's nothing to preview full-screen here, the point is to leave
  for the source.
- A badge reuses `ImageGallery.svelte`'s `.tile-source` treatment exactly (same `color-mix(in srgb,
  black 55%, transparent)` pill, same corner) but shows `card.price` instead of `card.subtitle`/
  source domain, and only renders when `card.price` is set — a non-shopping `highlight` call with no
  price just shows a plain tile.
- A tile with no `image_url` gets `RecommendationsCarousel.svelte`'s `.card-image-placeholder`
  treatment (a plain surface-2 box) rather than a broken `<img>`.

`Card` (`web/src/lib/types.ts:18`) gets `price?: string;` alongside the interface's existing
optional fields — mirrors the Go struct change 1:1, same "keep in sync by hand" posture already
used for `FocusMode`'s Go/TS pair (`agent/driver.go:149-152`).

## Shopper focus mode

This is the steering half — how the model is nudged toward commercial-intent `web_search` queries
and toward actually calling the generic `highlight` tool for products specifically, without any new
plumbing. `FocusMode` is already exactly this mechanism (`agent/driver.go:149-161`'s consts,
mirrored in `web/src/lib/types.ts:233`'s union and `web/src/lib/focusModes.ts`'s picker list, each
keyed into `prompts.yaml`'s `agent.focus_modes` map) — adding `"shopper"` is one entry in each of
those four places, the same shape as every existing mode, not new infrastructure. Because
`highlight` itself is domain-agnostic, this mode is where all the shopping-specific instruction
actually lives — it's what tells the model "use that tool, for this":

- `agent/driver.go`: `FocusModeShopper = "shopper"`.
- `prompts/prompts.go`'s `d.Agent.FocusModes["shopper"]`: instructs the model to phrase `web_search`
  queries with commercial intent (price, reviews, "best X for Y") rather than assuming a `category`
  value exists for this (per the SearXNG finding above — there isn't one), to prefer checking a
  couple of *different* retailers rather than only ever landing on one, to `web_read` the most
  promising 1-3 candidates to confirm real price/photo before presenting anything as a pick (exactly
  the `/dp`-page path already confirmed live), and to end by calling `highlight` with its top 3-5
  choices (title, url, price, photo) rather than describing them in prose — explicit that
  `highlight` itself has no idea this is a shopping turn, so the price/photo discipline is on the
  model, not the tool.
- `web/src/lib/types.ts`'s `FocusMode` union: add `'shopper'`.
- `web/src/lib/focusModes.ts`'s `FOCUS_MODES` array: `{ id: 'shopper', label: 'Shopper', description: 'Find and compare real products', icon: ShoppingCart }` (`ShoppingCart` from `@lucide/svelte`, already the icon package in use).

No change to `web_search`'s tool schema, no change to `catalog.go`'s gating logic — the mode is pure
prompt text plus which cards the model chooses to produce at the end, identical in kind to how
`"academic"`/`"news"` already just bias phrasing and category choice today.

## Explicitly out of scope for v1

(Real ideas, deliberately deferred — matches this repo's existing scoping convention.)

- **Brave's `product_cluster` field.** Real, but its field-level shape isn't confirmed and
  confirming it costs a billed call. v1.5 candidate: spend one deliberate spike-test call (with
  sign-off, since it's billed against the shared Brave cap), and if it carries real
  price/rating/image data, wire it as an optional automatic enrichment before `highlight` is even
  called — the tool's schema doesn't need to change either way, since `price`/`image_url` are
  already optional per-item fields.
- **Amazon Product Advertising API.** The clean, ToS-compliant, structured path — but requires an
  approved Amazon Associates account with qualifying sales activity to keep API access, which may
  not fit a single-operator personal tool. Worth a real look if commerce volume through this feature
  ever justifies it; not a v1 blocker since discovery doesn't depend on it.
- **A `"shopping"` SearXNG category.** Would require standing up/enabling real shopping engines in
  the self-hosted instance (`compose/searxng/settings.yml`) — a real, separate infra project, not a
  code change here. Not attempted until there's a concrete engine to point at.
- **Verifying `highlight`'s URLs are reachable/still in stock before rendering.** Would mean giving
  the tool its own HTTP client after all, exactly what this plan avoids. v1 accepts the tradeoff: a
  stale or dead link surfaces as a normal 404 when tapped, no worse than any other citation link
  already can be.
- **A Pulsar Daily "Deals" block.** Same `agent.Run` sub-generation shape as Daily's other blocks
  (`docs/plans/pulsar-daily.md`) could run a shopper-focused pulse and finalize into `highlight`
  cards for a recurring "today's picks" edition — a natural v2 given Daily's existing narrow-toolset
  pattern, not designed here.
- **Structured `price` (amount + currency).** Free text only in v1, per `highlight`'s tool shape
  above; add structure only if something ever actually sorts/filters/aggregates on price, which
  nothing does today.
- **Other `highlight` callers** — the whole point of naming it generically is that these need zero
  tool changes when they show up, just a prompt somewhere telling the model to reach for
  `highlight`. Tracked in issue #49; status per caller:
  - **Places** — done (`tools/descriptions/nearby_search.yaml`'s `api_description`). Correction to
    this doc's original note: Foursquare's base place-search response has **no photo field**
    (confirmed against `places.Place`'s struct — fetching one would need a separate per-place
    `/photos` API call, out of scope for this pass), so the shipped instruction leaves `image_url`
    unset and uses `price` for distance/category only.
  - **GitHub repos** — done (`tools/descriptions/github_repo.yaml`'s `api_description`), `price`
    holds a star count (`"★ 4.2k"`) when comparing more than one repo.
  - **Travel** — flights/hotels found via `web_search`, same "compare a handful of real options"
    shape as shopping. Not yet built.
  - **Listings in general** — job postings, real estate, event tickets: title + link + optional
    short badge + often a photo. Not yet built.
  - **A Researcher-focus-mode source grid** — the strongest few sources on a topic as a visual grid
    instead of citations buried in prose, when the user's comparing options rather than reading a
    synthesized answer. Not yet built.
  - **Pulsar Daily blocks** beyond the already-noted "Deals" idea — e.g. "New releases this week" —
    since it's surfacing search-found items, not computing similarity, it doesn't need
    music/books/movies' recommendation-engine machinery at all. Not yet built.
  Nothing here forces the rest of these to exist; listed so the next time one comes up, it's a
  prompt change reaching for an existing tool, not a "should this be its own tool" conversation
  again.

## Next steps

1. `tools.Card.Price` field (`tools/registry.go`, alongside `Kind`/`FullImageURL`) — additive,
   default-preserving.
2. `tools/highlight.go` — tool def + handler: `minItems`/5-item cap (rejected with a tool error, not
   truncated, mirroring `visualize`'s cap pattern), required `title`/`url` per item,
   `ctx.Blocklist.Blocked` check per item, `ctx.AddCard(Card{..., Kind: "highlight"})`.
3. `tools/descriptions/highlight.yaml` — `category: research` (see "Availability" above for why,
   deliberately not matching `visualize`'s uncategorized default) + a domain-agnostic description
   (no mention of products/shopping — that framing belongs to the shopper focus mode's prompt text,
   not the tool's own description) instructing the model to only pass URLs/prices it actually read
   this turn.
4. `tools/catalog.go`'s `catalogOrder` — add `"highlight"` (after `"image_search"`, before
   `"read_attachment"`, matching the existing research-tool grouping) + a `catalogDefaults` entry.
5. `web/src/lib/types.ts`'s `Card` interface — add `price?: string;`.
6. `HighlightGrid.svelte` (new) — `ImageGallery.svelte`'s masonry CSS, `RecommendationsCarousel.svelte`'s
   direct-link-out `<a>` tiles, a badge (shows `card.price` when set) reusing `.tile-source`'s pill
   styling.
7. `ChatTurnView.svelte` — three-way card partition (`mediaCards`/`imageCards`/`highlightCards`,
   ChatTurnView.svelte:47-48) + the new `<HighlightGrid>` conditional block alongside the existing two.
8. Shopper focus mode: `agent.FocusModeShopper` const (`agent/driver.go:149-161`),
   `prompts/prompts.go`'s `d.Agent.FocusModes["shopper"]` text (the shopping-specific instruction to
   reach for `highlight` — see "Shopper focus mode" above), `web/src/lib/types.ts`'s `FocusMode`
   union, `web/src/lib/focusModes.ts`'s `FOCUS_MODES` entry (`ShoppingCart` icon).
9. Live-verify before calling this done, per `CLAUDE.md`'s "verify on real hardware" culture: a real
   turn in Shopper focus mode, checked against the actual running app — does the model reach for
   commercial-intent `web_search` queries, does `web_read` on a chosen `/dp`-style page still come
   back clean, does `highlight` actually get called with real data instead of prose, does the
   masonry grid render correctly on a phone-width viewport (this is a phone-first, Tailscale-only
   app per `PRODUCT.md`).
10. Docker two-sided sync checklist (per `CLAUDE.md`) — n/a for this slice: no new hot-editable
    resource directory, no new CLI command, no new settings-panel server-mutating action, no new API
    key (reuses whatever `web_search`/`web_read` already have configured).
