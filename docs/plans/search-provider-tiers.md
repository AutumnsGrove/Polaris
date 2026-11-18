# Search provider tiers: retiring SearXNG as production primary

**Added: 2026-10-18. Revised: 2026-11-18.**

**Status: rescoped — the image half is built, the web half is still only a plan.** Read
"Scope change (2026-11-18)" first; everything below it is the original research, with the numbers
it got wrong corrected in place. Tracked in
[issue #179](https://github.com/AutumnsGrove/Polaris/issues/179).

## Scope change (2026-11-18): images now, web search later

The original framing was "retire SearXNG", full stop. Live measurement says that's the wrong
cut: **SearXNG's web results were never the problem — its image path is.**

- `web_search` is not visibly degraded by quality. In the 30 days to 2026-11-18, SearXNG answered
  347 of 421 provider-attributed answers (82%), Brave 74, Parallel 2 — the fallbacks fired on
  *outages*, exactly as designed, and there is no evidence that the answers were worse for it.
- The image path is a different story, and it is worse than "uneven":
  - SearXNG's image engines are mostly **suspended** under ordinary use. A fresh query against the
    dev instance returned `brave.images: too many requests`, `startpage images: CAPTCHA`,
    `duckduckgo images: parsing error`, plus `artic` access-denied and `unsplash` parsing errors —
    5–8 dead engines on every query tried, out of ~12.
  - Thumbnails are hotlinked from the source page's CDN, and **roughly a third to a half don't load**
    (measured 8/20, 8/20, 3/20 on three queries, then 16/45 across a wider sample). A broken tile
    still costs the model a `view_image` call to discover it's broken.
  - Titles are frequently empty or junk — one haircut query's top hit was a `lucide-static` SVG
    icon titled "blinds".
  - **And it could never escalate.** `search/searxng.go` only computes `Degraded` for
    `category == ""` (it compares against `generalCategoryEngineCount`), so the `images` category
    can never report itself degraded on its own. `tools/image_search.go`'s Brave fallback was
    therefore reachable only if a *web* search had already tripped the shared cooldown — which is
    why the good provider was effectively dead code for the exact tool that needed it most.
- Brave Images, same queries, same minute: 10/10 results, every thumbnail served from
  `imgs.search.brave.com` (Brave's own CDN, so no hotlink 403s and no ad-blocker breakage from
  `*.explicit.bing.net`), descriptive titles.

So the first shipped change is the image flip alone, with **no new vendor and no web-search
changes**: Brave first, SearXNG's images category only as a fallback, dead thumbnails probed out
before the model ever sees them, and 100 calls of Brave's monthly cap reserved so image search
can't be starved by `web_search` spending its share. See "Built 2026-11-18" at the end of this doc.

The web-search ladder below stays a plan. It is still the right shape *if* the web results ever
start disappointing the way the image results did — but nothing measured so far justifies
reordering them today.

## Why now

SearXNG is a metasearch proxy over scraped engines (Brave, Google, DuckDuckGo, Startpage). It's
free and self-hosted, which is why it was the right first provider, but under real use it is the
weakest link in the search path:

- **It degrades fast.** A handful of queries in a burst can trip engine-level rate limits and
  put the whole instance into the hour-long cooldown (`search/searxng.go`), at which point every
  call falls through to Brave anyway.
- **Quality is uneven.** The underlying engines are being scraped, not queried through a real
  index, so result quality is whatever survives the scrape — the "not terrible, not great"
  experience that prompted this doc.
- **The fallbacks already carry most of the weight.** In the last 90 days SearXNG answered 1,171
  `web_search` calls and Brave answered 280 *as a fallback* — the safety net is not rare.
- **Images and raw listings are better elsewhere.** Atlas's browsing UI wants real, multi-result
  listings; Brave's Search and Image endpoints are a better fit than SearXNG's aggregated output.

The paid providers we already have wired are, for our purposes, better than SearXNG on quality,
and their **recurring free quotas alone exceed our usage by a wide margin** (see "Capacity math").

## How we measured

Live `/api/stats` on the production deployment (`ssh potato-remote`, 2026-10-18), which reports
`search_provider_counts` scoped by `?days=` plus `api_caps` for the current calendar month.

| Window | `web_search` calls | `image_search` calls | searxng | brave | parallel | tavily |
|---|---:|---:|---:|---:|---:|---:|
| last 30 days | 892 | 34 | 657 | 205 | 5 | 0 |
| last 60 days | 1,508 | 37 | 1,167 | 280 | 5 | 6 |
| last 90 days | 1,973 | 37 | 1,171 | 280 | 5 | 6 |

Current calendar month (2026-10-01 → 10-18), from `api_caps`:

| Provider | Used | Cap | Remaining |
|---|---:|---:|---:|
| Brave | 192 | 1,000 | 808 |
| Parallel | 3 | 4,900 | 4,897 |
| Tavily | 41 | 500 | 459 |

Caveats on the numbers:

- `search_provider_counts` counts **successful answers only** (a provider actually returned
  results to the model), not billed calls — it is not the same as `api_usage`'s cap counters.
- The last-30-day figure (892) is well above the 90-day average (~658/mo). Treat 892 as a busy
  peak, not the steady state.
- These counts cover the `web_search`/`image_search` tools only. They **exclude** Atlas's own
  Brave fallback (`gateway/search.go`), `polaris search` on the CLI, and any other call site that
  reaches a provider directly.

## Capacity math

Recurring free allowances (confirmed on each vendor's pricing page, October 2026):

| Provider | Recurring free | Paid after | Notes |
|---|---|---|---|
| Brave | **$5 credit/mo = 1,000 queries** | $5 / 1k | Card required at signup for identity; free usage not charged. 50 qps. |
| Parallel | **$5 credit/mo ≈ 5,000** Turbo/Fast | $1 / 1k (Turbo/Fast); $5 / 1k (Basic/Advanced) | 600 req/min. |
| Tavily | **1,000 credits/mo** (code caps at **500 calls** — `tavilyMonthlyCap` in `tools/web_search.go`, because credits are shared with `web_read`'s extract/`force_tavily` path, which spends 2 per advanced call) | $8 / 1k basic; $16 advanced | Credits: 1 basic search = 1, 2 for advanced/extract. |
| Ceramic | **1,000 credits one-time** | $0.25 / 1k PAYG; $0.20 / 1k Pro (+$99/mo) | Not recurring. 20 qps (50 Pro). |

Recurring free capacity of the three good providers: **~6,900 calls/month** once Tavily's real
500-call ceiling is used rather than its 1,000 credits. Our usage is **~450–890/month**, i.e.
~7–13% of free capacity.

Refreshed 2026-11-18 from the same endpoint (last 30 days): `web_search` 442, `image_search` 17,
providers searxng 347 / brave 74 / parallel 2. Current month: Brave 99/1,000, Parallel 1/4,900,
Tavily 56/500.

If SearXNG is retired in production, essentially all `web_search` + `image_search` volume moves to
Brave first: **~926 calls in a peak 30-day window (~93% of Brave's 1,000 free)** and **~670/mo on
the 90-day average (~67%)**. The peak figure is the one that matters — it is close enough to the
cap that a heavier month spills into paid or into the next provider, and it does **not** include
Atlas/CLI calls.

Conclusion: capacity is not the problem. **Ordering and spill behavior are.**

## Provider landscape (research)

### Brave Search API

- Independent index (30B+ pages, 100M+ updates/day), 50 qps, 20 results/request, up to 200 via
  offset. Separate Image Search endpoint.
- Pricing: **$5/1k**, **$5 free credit every month** (= 1,000 free queries), card required at
  signup for anti-fraud but free usage is not charged.
- Quality: strongest general web index in Exa's head-to-head; leads citation precision; highest
  agent score and lowest latency (669 ms) in AIMultiple's eight-provider benchmark.
- Best fit here: `web_search` primary, `image_search` primary, Atlas browsing (real listings).

### Parallel Search API

- Modes by latency/depth: Turbo/Fast (~200–700 ms) at **$1/1k**; Basic/Advanced at $5/1k.
  600 req/min, 20 results/request.
- Free: **$5/mo credit ≈ 5,000 Turbo/Fast searches** (matches `parallelMonthlyCap = 4900`).
- Quality: top of the field on multi-hop/agentic benchmarks (BrowseComp, HLE, WebWalker in
  Parallel's own runs; Artificial Analysis Search Index Aug-2026: Parallel basic 73 vs Tavily 66).
  Returns agent-shaped compressed excerpts, **not** browsing listings; no image endpoint.
- Best fit here: high-quality overflow (`web_search`), research sub-agents.

### Tavily

- Search + extract/map/crawl; 1,000 free credits/mo; $8/1k basic, $16 advanced.
- Quality: solid, but the weakest and most expensive of the three on agentic benchmarks.
- Best fit here: `web_read`'s extract fallback (already wired), `force_tavily`, and the last-paid
  tier before Ceramic.

### Ceramic (new)

- Independent lexical index (40B+ pages), founded by Dr. Anna Patterson (ex-Google VP Eng,
  TeraGoogle) and Tom Costello (Cuil). Launched Q2 2026, backed by NEA/IBM/Samsung Next.
- **Very cheap:** $0.25/1k PAYG; $0.20/1k on Pro ($99/mo incl. ~495k). ~60 ms median latency.
  20 qps (50 Pro). 10–20 results.
- API: `POST https://api.ceramic.ai/search`, `Authorization: Bearer <key>`, body
  `{"query": "..."}`; response `result.results[].{title, url, description}`.
- **Caveats:** free credits are **one-time (1,000)**, not monthly; **no image search**, no
  extract/crawl, no JS rendering; **lexical, English-only** (needs keyword-shaped queries);
  **no public quality benchmarks or customer list** — Cloudflare's own comparison recommends
  "test Exa for quality". Pricing has appeared as both `$0.05` and `$0.25`/1k in different
  sources — **confirm at signup**.
- Best fit here: the cheap last-mile for `web_search` when every free quota is spent. Not usable
  for `image_search`. **Deferred (2026-11-18)** — see "Open questions": it is unproven, has a
  one-time (not monthly) allowance, and SearXNG is already a free last rung.

### Dead ends (checked 2026-11-18, recorded so nobody re-proposes them)

These come up every time image search is discussed and none of them is available:

- **Google Custom Search JSON API** (`searchType=image`, 100 free queries/day) — **closed to new
  customers**, and existing customers must transition by **2027-01-01**. Not an option for a new
  integration.
- **Bing Image Search API** — **retired 2025-08-11**, new signups closed.
- **SerpApi / DataForSEO / Bright Data** — live, but $25/1k, $0.60/1k standard-queue and $1.50/1k
  records respectively, and the Google-results ones carry their own legal grey area. Not worth it
  at our volume
- **Serper** — Google Images in JSON, 2,500 free queries (one-off), ~$1/1k. The only serious
  *paid* image alternative if Brave's image side ever disappoints too.
- **Pexels (20,000 req/mo free) / Unsplash (50 req/hour demo)** — high quality, but they are photo
  *libraries*: right for "show me a cozy reading nook", wrong for a real person, event, product or
  place. Worth remembering only if a query-shape heuristic ever seems worth it. (SearXNG's own
  `pexels`/`unsplash` engines are wired but return "timeout"/"parsing error" — nominally present,
  effectively useless.)

### SearXNG

- Free, self-hosted, no API keys, queries never leave the box. Worth keeping for dev.
- Rate-limits quickly, uneven scraped-index quality, and the hour-long cooldown is a blunt
  instrument. Already the thing the whole fallback chain exists to paper over.

### Quality comparison caveat

Almost every published benchmark is vendor-authored (Parallel's page favors Parallel, Exa's
favors Exa). The reliable signal is directional: **Brave ≈ Parallel > Tavily >> Ceramic (unproven)**,
with Brave ahead for general browsing/citations/images and Parallel ahead for multi-hop research.
The codebase already has a benchmark harness (below) — use it to settle this with our own query
mix rather than trust vendor blogs.

## Proposed strategy: a quota-aware ladder

**Shipped for images on 2026-11-18; still proposed for web search.** Iterate providers in quality
order and skip any whose monthly cap is reached (the existing fallbacks already check caps before
calling). The web ladder below is the deferred half:

```
web_search:   Brave (free 1,000, stopping at brave.WebSearchCeiling)
                → Parallel (free ~4,900, then $1/1k)     ← highest-quality spill
                → Tavily (free 500 calls, then $8/1k)    ← last paid stop before free-only
                → SearXNG                                ← free last rung, already deployed

image_search: Brave (the last 100 calls of its 1,000, see brave.ImageReserveCalls)
                → SearXNG's "images" category            ← free fallback, ~third of tiles dead
                → degrade honestly
```

Why Brave stays primary rather than Parallel: it is the only one with an image endpoint, it returns
real listings for Atlas, and it has the best citation precision. Why Parallel is the *spill* rather
than paid Brave overage: it is 5x cheaper per call with 5x the free depth, and at least as good on
hard research. Why SearXNG is the last rung rather than Ceramic: it is free, already deployed, and
needs no new secret — strictly better than paying an unproven provider for the same last-mile slot.

### Decided (2026-11-18): spill to Parallel

Two viable behaviors when Brave's 1,000 free runs out:

1. **Spill to Parallel** (strictly free, but quality slides down the ladder mid-month).
2. **Pay Brave overage** (~$2.50 for 500 calls) to keep quality flat.

**Decided: (1), spill.** The ladder stays inside free allowances, and the spill is deep
(4,900 more calls) and good on exactly the kinds of queries that would have pushed Brave over.
Overage stays a one-line change if it ever turns out to be the wrong call; a setting for it is not
worth the surface area until someone actually wants the other behavior.

## The switch

Add a persisted setting (the `settings` table + `GET/PUT /api/settings` already exist) exposing
the primary provider:

- `search.primary_provider` ∈ `auto | searxng | brave | parallel | tavily | ceramic`.
- `auto` = the ladder above (production default). `searxng` remains the **dev default**, so a
  bare-metal `go run .` needs no API keys.
- Surface it in the settings panel with a short explanation of the current tier and remaining
  free quota (the `api_caps` numbers already exist).

`tools.Context.PinnedProvider` already exists but is **benchmark-only** (`cmd/benchmark.go`
hardcodes `"brave"`). Generalize it into a real primary-provider preference that the resolver
consults, instead of adding a parallel mechanism.

### The refactor behind it

The fallback cascade is currently **triplicated**:

- `tools/web_search.go` — SearXNG → Brave → Parallel → Tavily
- `tools/image_search.go` — Brave → SearXNG (flipped 2026-11-18; no ladder helper needed, since
  it is one preferred provider plus one free fallback)
- `gateway/atlas_search.go` — Atlas's own Brave fallback with virtual sub-pagination

A switch plus a reordered ladder should not become a fourth copy. Extract one shared
provider-ladder helper (ordered providers, per-provider cap/usage closures, `(formatted,
providerKey, ok)` return) and have the web-shaped call sites use it. Atlas's virtual-pagination
behavior is genuinely different and may stay a thin wrapper over the Brave step. The image path is
deliberately *not* part of that helper — it is two named attempts with different result shapes, and
folding it in would be the abstraction earning its keep backwards.

## Code touchpoints

- `tools/web_search.go` — `handleWebSearch`, `braveFallback`, `parallelFallback`,
  `tavilyFallback`; the ladder lands here first.
- `tools/image_search.go` — `braveImageSearch` (primary), `searxngImageSearch` (fallback),
  `filterDeadThumbnails`, `emitImageDegraded`. Already restructured for this.
- `gateway/atlas_search.go` — `ResolveAtlasSearch` / `resolveAtlasBraveFallback` / `bravePageFetcher`.
  (An earlier draft of this doc named `gateway/search.go` and `braveFallbackSearch`; that code moved.)
- `tools/nearby_search.go` — falls back to a plain SearXNG search for its venue-less case too, so
  it is another call site that would notice a SearXNG retirement.
- `tools/context.go` — `PinnedProvider`, `ProbeThumbnail`, `BraveUsageThisMonth`/
  `IncrementBraveUsage`, and the Parallel/Tavily equivalents; the shared ladder consumes these.
- `store/settings.go` + `gateway/server.go` settings handlers + `web/src/lib/settings.svelte.ts`
  and the settings panel component.
- `config/config.go` — add a `Ceramic` block alongside `SearXNG`/`Brave`/`Parallel`/`Tavily`
  (only once Ceramic is actually pursued).
- `cmd/search.go` — CLI one-shot path; per CLAUDE.md this has historically lagged the web app in
  wiring Brave/Parallel/Tavily + usage closures. Verify it has all five pieces.
- `cmd/benchmark.go` — replace the hardcoded `PinnedProvider: "brave"` with a `--provider` flag.
- `gateway/turn_context.go`, `gateway/pulsar_daily.go`, `agent/subagent.go` — every place that
  builds a `tools.Context` must pass the full provider set, not just SearXNG. Note Pulsar Daily's
  Picture of the Day dispatches `image_search` directly (`gateway/pulsar_daily.go`), so the image
  order flip changed its provider too, for free.

## Stale code to refresh (small, independent)

Brave's pricing changed: the `$5` is a **recurring monthly credit** (1,000 free queries/month), not
a one-time signup credit. The cap *value* (`brave.MonthlyCap = 1000`) is correct; only the
reasoning was wrong.

**Done** (comment-only, refreshed alongside closing #159/#163) — corrected in `brave/brave.go`
(package doc + `MonthlyCap`), `tools/context.go`, `SETUP.md`, `CLAUDE.md`, and
`config/config.go` (the same claim, not listed in the original TODO).

## Phasing

**Phase 0 — images (shipped 2026-11-18).** See "Built 2026-11-18" at the end.

Everything below is the deferred web-search half:

1. **Stale comment refresh** — done (see the section above).
2. **Benchmark first, not last** — a deterministic provider A/B (no model, no judge): same query
   set through each provider, measuring results returned, domain diversity, duplicate rate, and
   for images the broken-thumbnail rate. `eval/` and `benchmark/` both exist now and neither needs
   the other's machinery; a `polaris search-quality` style command over a fixed query list would
   do. **Do not flip web search before this produces a number that contradicts "SearXNG's web
   results are fine".**
3. **Setting + resolver** — `search.primary_provider`, dev default `searxng`, production default
   `auto`; generalize `PinnedProvider`.
4. **Shared ladder refactor** — one helper, web-shaped call sites, cap checks preserved, dedup
   intact.
5. **Flip production** — only after (2) and a live potato test; keep SearXNG for dev.
6. **Ceramic** — only if free quotas ever look at risk (they are at ~10%), and only after (2) has
   an image-free baseline to compare against.

## Verification plan

- **Shipped for images** (2026-11-18): live `tools.Dispatch` against the real Brave API and the dev
  SearXNG returned 10/10 Brave candidates all on `imgs.search.brave.com`, and on the fallback path
dropped 3 of 9 SearXNG thumbnails as unreachable before the model saw them. `go test ./tools/
./gateway/` covers the order, the reserve, the cap, the dead-tile filter, and the degraded message.
- For the web half when it happens: run the same sample through each provider against an isolated
  usage DB (as `cmd/benchmark.go` already does) and compare answer correctness, citation rate and
  calls/question.
- Live-test on the potato: flip the setting, run real `/api/ask` turns, watch
  `docker compose logs polaris` for provider selection and `search_provider_counts` in
  `/api/stats`.
- Explicitly test the cap-hit path: force a provider's usage to its cap and confirm the ladder
  skips it rather than erroring or overspending. For images specifically, force Brave's usage to
  `brave.MonthlyCap` and confirm the SearXNG fallback answers rather than the turn erroring.
- Confirm dev (`go run .`, no API keys) still works on SearXNG.

## Open questions

All five answered 2026-11-18:

1. **Spill vs. pay-overage** — spill. Decided above.
2. **Brave or Parallel as `web_search` primary** — Brave, if the flip ever happens. Settle with the
   provider A/B in phasing (2), not with vendor blogs.
3. **Is Ceramic worth adding before we have quality data?** — No. Deferred entirely; SearXNG is the
   free last rung, and it is already deployed.
4. **Atlas unification** — stays separate for now. Its virtual pagination is real logic, and it is
   not what is broken.
5. **Privacy** — now stated, not implied: image queries leave the box for Brave. They already did
   whenever SearXNG was degraded, and `web_read`/the LLM were never hermetic, so this is a
   confirmation of the status quo rather than a new exposure.

New, opened by the image work:

6. **Do SearXNG's *web* results ever actually disappoint?** The complaint that started this issue was
   about images; nothing measured so far shows a web-results problem. If that changes, it needs the
   same kind of measurement (broken/dead links, duplicate domains, snippet usefulness) before a
   flip — not another "quality is uneven" hunch.
7. **Pulsar Daily's Picture of the Day** now uses Brave as well. That is the right provider, but it
   means the reserve is shared with a nightly job — worth watching if picture editions ever start
   reporting "no images found" late in a month.

## Built 2026-11-18

- `tools/image_search.go`: Brave first (cap-checked, usage-recorded only on success), SearXNG only
  as fallback, one shared `emitImageDegraded` for every "no provider" path, and
  `filterDeadThumbnails` probing SearXNG thumbnails (8 concurrent, 3s each, one byte each) before
  they reach the model.
- `brave/brave.go`: `ImageReserveCalls` (100) and `WebSearchCeiling` (`MonthlyCap - 100`).
- `tools/web_search.go`, `gateway/atlas_search.go`: both web-shaped Brave checks now stop at
  `brave.WebSearchCeiling` instead of `brave.MonthlyCap`, so the reserve is real.
- `tools/context.go`: `ProbeThumbnail` (nil = the real probe; set by tests only).
- `gateway/pulsar_daily_picture_test.go`: sets `ProbeThumbnail` — the fixture thumbnails are
  `cdn.example` URLs, which the real filter would otherwise drop before the judge saw one.
- Docs: `CLAUDE.md`'s fallback-chain section now describes the two orders and the reserve.
