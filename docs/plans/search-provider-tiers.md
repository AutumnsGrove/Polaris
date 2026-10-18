# Search provider tiers: retiring SearXNG as production primary

**Added: 2026-10-18.**

**Status: proposed — research complete, nothing built.** This doc records the decision to stop
treating the self-hosted SearXNG instance as the production primary, replace it with a
quota-aware ladder over the paid providers we already integrate, and add a very cheap last-mile
provider (Ceramic). SearXNG stays as the default in local dev (free, no API keys). Tracked in
[issue #179](https://github.com/AutumnsGrove/Polaris/issues/179).

Nothing here is urgent — this is a "when there's appetite" piece, captured so the research isn't
lost. The one contained cleanup worth doing earlier is the stale Brave pricing comments (see
"Stale code to refresh").

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
| Tavily | **1,000 credits/mo** | $8 / 1k basic; $16 advanced | Credits: 1 basic search = 1, 2 for advanced/extract. |
| Ceramic | **1,000 credits one-time** | $0.25 / 1k PAYG; $0.20 / 1k Pro (+$99/mo) | Not recurring. 20 qps (50 Pro). |

Recurring free capacity of the three good providers: **~7,000 queries/month**. Our usage is
**~660–890/month**, i.e. ~13% of free capacity.

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
  for `image_search`.

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

Rather than a single hardcoded primary, iterate providers in quality order and skip any whose
monthly cap is reached (the existing fallbacks already check caps before calling). The ladder is:

```
web_search:   Brave (free 1,000)
                → Parallel (free ~5,000, then $1/1k)     ← highest-quality spill
                → Tavily (free 1,000)                    ← last free stop
                → Ceramic ($0.25/1k)                     ← cheap last mile, ~never bills much

image_search: Brave (free 1,000)
                → (Tavily include_images is not "find photos"; Ceramic has no images)
                → degrade honestly if Brave is capped
```

Why Brave stays primary rather than Parallel: it is the only one with an image endpoint, it returns
real listings for Atlas, and it has the best citation precision. Why Parallel is the *spill* rather
than paid Brave overage: it is 5x cheaper per call with 5x the free depth, and at least as good on
hard research.

### Open decision: spill vs. pay overage

Two viable behaviors when Brave's 1,000 free runs out:

1. **Spill to Parallel** (strictly free, but quality slides down the ladder mid-month).
2. **Pay Brave overage** (~$2.50 for 500 calls) to keep quality flat.

For a personal tool, (2) is defensible; (1) matches the "free quotas only" goal. This choice
should be explicit in the setting, not implied.

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
- `tools/image_search.go` — SearXNG → Brave
- `gateway/search.go` — Atlas's own Brave fallback with virtual sub-pagination

A fourth provider plus a switch should not become a fourth copy. Extract one shared
provider-ladder helper (ordered providers, per-provider cap/usage closures, `(formatted,
providerKey, ok)` return) and have all three call sites use it. Atlas's virtual-pagination
behavior is genuinely different and may stay a thin wrapper over the Brave step.

## Code touchpoints

- `tools/web_search.go` — `handleWebSearch`, `braveFallback`, `parallelFallback`,
  `tavilyFallback`; the ladder lands here first.
- `tools/image_search.go` — `handleImageSearch`, `braveImageFallback`.
- `gateway/search.go` — `handleSearch` / `braveFallbackSearch` (Atlas), currently separate.
- `tools/context.go` — `PinnedProvider`, `BraveUsageThisMonth`/`IncrementBraveUsage`, and the
  Parallel/Tavily equivalents; the shared ladder consumes these.
- `store/settings.go` + `gateway/server.go` settings handlers + `web/src/lib/settings.svelte.ts`
  and the settings panel component.
- `config/config.go` — add a `Ceramic` block alongside `SearXNG`/`Brave`/`Parallel`/`Tavily`.
- `cmd/search.go` — CLI one-shot path; per CLAUDE.md this has historically lagged the web app in
  wiring Brave/Parallel/Tavily + usage closures. Verify it has all five pieces.
- `cmd/benchmark.go` — replace the hardcoded `PinnedProvider: "brave"` with a `--provider` flag.
- `gateway/turn_context.go`, `gateway/pulsar_daily.go`, `agent/subagent.go` — every place that
  builds a `tools.Context` must pass the full provider set, not just SearXNG.

## Stale code to refresh (small, independent)

Brave's pricing changed: the `$5` is a **recurring monthly credit** (1,000 free queries/month), not
a one-time signup credit. The cap *value* (`brave.MonthlyCap = 1000`) is correct; only the
reasoning is wrong. Fix:

- `brave/brave.go:8–9` (package doc) and `:41–43` (`MonthlyCap` comment)
- `tools/context.go:48` ("no ongoing free tier at all...")
- `SETUP.md:25` ("one-time $5/mo credit")
- `CLAUDE.md:185–186` (same claim)

This is a comment-only change and can ship on its own day.

## Phasing

1. **Stale comment refresh** (above) — tiny, independent.
2. **Ceramic client** — new `ceramic/` package mirroring `brave/` (config block, `Search`,
   `SearchRecent`; no images), with tests against a fake server.
3. **Setting + resolver** — `search.primary_provider`, dev default `searxng`, production default
   `brave`/`auto`; generalize `PinnedProvider`.
4. **Shared ladder refactor** — one helper, three call sites, cap checks preserved, dedup intact.
5. **Benchmark** — `cmd/benchmark.go --provider` and a real quality comparison across
   searxng/brave/parallel/tavily/ceramic on our own sample.
6. **Flip production** — only after the benchmark and a live potato test; keep SearXNG for dev.

## Verification plan

- Extend `cmd/benchmark.go` with `--provider` and run the same sample through each provider
  (each against an isolated usage DB, as the harness already does). Compare answer correctness,
  citation rate, and calls/question.
- Live-test on the potato: flip the setting, run real `/api/ask` turns, watch
  `docker compose logs polaris` for provider selection and `search_provider_counts` in
  `/api/stats`.
- Explicitly test the cap-hit path: force a provider's usage to its cap and confirm the ladder
  skips it rather than erroring or overspending.
- Confirm `image_search` still works on Brave and degrades honestly when Brave is capped.
- Confirm dev (`go run .`, no API keys) still works on SearXNG.

## Open questions

1. **Spill vs. pay-overage** at Brave's cap (see above).
2. **Brave or Parallel as `web_search` primary** — Brave wins on listings/images/citations,
   Parallel on multi-hop quality and cost. Leaning Brave, to be settled by the benchmark.
3. **Is Ceramic worth adding before we have quality data?** It is cheap and trivial to integrate,
   but unproven; it can also wait until the free quotas actually look at risk.
4. **Atlas unification** — fold `gateway/search.go` into the shared ladder, or keep its
   virtual-pagination Brave path separate?
5. **Privacy** — every retired SearXNG call now leaves the box to a commercial provider. The
   assistant is already not hermetic (`web_read` fetches directly, the LLM goes to OpenRouter),
   so this is probably acceptable, but it should be a stated tradeoff rather than an accident.
