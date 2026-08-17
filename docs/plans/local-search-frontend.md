# Atlas — v1 plan

Atlas is a self-hosted "local Kagi" search results page for Polaris, built on the existing
SearXNG instance. This is a new, visually distinct product living inside the same Go
binary/SvelteKit app as the chat assistant, not a reskin of it — connected by a mode toggle in
the header and a shared sidebar shell (see "Shared chrome" below). Named in the same
navigation-instrument register as Polaris (the star you steer by) — Atlas is the reference you
consult, a companion product rather than a clone.

Design reference: `design/mockups/search-results.html` (open directly in a browser, works
offline, no build step — the header wordmark there now reads "Atlas"). That mockup is the source
of truth for visual decisions below; this document is the source of truth for scope, data model,
and architecture.

## Why this exists

Polaris already has search — but only as a tool the LLM agent calls (`tools/web_search.go` →
`search.SearXNGClient`) on the user's behalf. There is no page where the user can see raw ranked
results themselves, adjust which sources they trust, or get a fast synthesized answer without
starting a full chat thread. Kagi Search is the reference point: search-as-a-product, not
search-as-an-LLM-tool.

## v1 scope

**In scope:**
- Web results only. No images/news/video verticals yet — SearXNG already supports those
  categories (confirmed live), so they're additive later, not a rework.
- Domain ranking: a 5-state system (Block / Lower / Default / Raise / Pin), replacing the
  existing binary blocklist.
- A floating popover (not a slide-over, not inline) for adjusting a result's domain ranking,
  anchored to a small "tune" icon next to each result's title.
- An inline Quick Answer card, `?`-triggered, above the results — powered by Polaris's existing
  agent, not a new LLM integration.
- Autocomplete/prefill in the omnibox, via SearXNG's built-in `/autocompleter` endpoint.
- A mode toggle in the header to switch to the existing chat assistant.
- Light and dark themes, both real supported states (not just a dark-mode default).
- Mobile-responsive layout, including a bottom-sheet fallback for the ranking popover.

**Explicitly out of scope for v1** (revisit later, not forgotten):
- Kagi Lenses (bounded include/exclude domain+keyword filter presets) — good v2 candidate, maps
  cleanly onto SearXNG's existing engine/category params.
- Images, news, video result verticals.
- A numeric/continuous ranking score — the 5-state model is deliberately simpler than Kagi's own
  actual mechanism turned out to require.

## Domain ranking

**States:** `block`, `lower`, `default`, `raise`, `pin` (matches Kagi's real UI — confirmed via
research, not a numeric slider). `block` excludes a domain entirely; `pin` forces it to the top;
`lower`/`raise`/`default` are relative weight adjustments applied during result ordering.

**Storage:** a hand-editable, hot-reloaded YAML file — matching the existing convention set by
`blocked_sources.txt` and `prompts.yaml` — that the settings-panel ranking UI also writes to
directly. Proposed path: `domain_rankings.yaml`, loaded the same way `LoadBlocklist` currently
loads `blocked_sources.txt`.

```yaml
# domain_rankings.yaml — hand-editable or written by the ranking popover UI.
# State: block | lower | default | raise | pin. Omitted domains are implicitly "default".
reddit.com: raise
pinterest.com: lower
content-farm.example: block
```

**Where it plugs in:** `search/blocklist.go` currently backs a binary allow/deny check inside
`search/searxng.go`'s `Search()`. This becomes a `DomainRankings` type (rename/extend, not a
parallel system) that:
1. Drops `block`-state results before they count toward `maxResults` (same early-filter behavior
   `Blocklist.Blocked` has today).
2. Applies a weight multiplier to `lower`/`raise`/`pin` domains when sorting results.
3. Is shared by both the new search frontend's Go handler *and* `tools/web_search.go`'s existing
   agent tool — this is what makes ranking changes "affect Polaris too," per the original ask.

**Ranking algorithm (resolved):** `search/searxng.go` currently normalizes each engine's raw
`score` by dividing by 10 and clamping to 1.0 (`search/searxng.go:115-118`) — a rough heuristic,
not a real cross-engine signal, since SearXNG merges results from engines (DuckDuckGo, Brave,
Bing News, etc.) whose scores aren't on the same scale and some engines don't return one at all.

Fix: **Reciprocal Rank Fusion (RRF)**, the standard IR technique for combining ranked lists
without comparable scores. For each engine's own result list, a result at 1-indexed position
`rank` contributes `1 / (k + rank)` to its fused score (`k = 60`, the standard damping constant
from the original RRF paper). A result's total fused score is the sum of its contributions across
every engine that returned it — so a result ranked #1 by two engines outscores one ranked #1 by
only a single engine, entirely from position data, no score-magnitude comparison needed. This
also directly produces the "agreed on by N other engines" detail already shown in the mockup's
ranking popover — it falls out of the fusion math rather than needing separate bookkeeping.

Domain-ranking states apply as a second pass, on top of the fused score:
- `block` — excluded before fusion (same early-filter behavior as today's `Blocklist.Blocked`).
- `pin` — forced to the top of the final list, ahead of fused ordering.
- `raise` / `lower` — multiply the fused score (exact multipliers TBD during implementation —
  something like 1.5–2x / 0.4–0.6x as a starting point, tuned by feel once it's running against
  real queries).
- `default` — fused score used as-is.

## Autocomplete

SearXNG has a built-in `/autocompleter?q=...&format=json` endpoint, gated by a `search.autocomplete`
setting that was never set. Fixed and verified live in `dev/searxng/settings.yml`
(`search.autocomplete: "duckduckgo"`), confirmed working via curl.

**Still needed:** the same line in `compose/searxng/settings.yml` (production). Not applied yet —
deliberately deferred until this feature is actually being shipped, since it's a live-deployment
config change, not a dev sandbox one.

## Quick Answer

Triggered by appending `?` to the query (matching Kagi's actual mechanism). Calls Polaris's
existing agent/LLM pipeline — no new model integration — and renders as a card above the results
list, with numbered citations linking down to the specific sources used. No redirect into the
existing `/t/[id]` chat thread UI; this stays inline on the results page.

## Shared chrome (sidebar)

Atlas and the chat assistant share one sidebar shell — same component structure, same visual
tokens, same collapse/expand and mobile-overlay behavior as `web/src/lib/components/Sidebar.svelte`
— so toggling between modes doesn't feel like switching apps. What's shared is the *shell*, not
the *content*:

- **Shell (shared):** brand row + collapse button, primary action button, favorites/recents
  thread-style list with the same row styling (leading dot, active-state accent-soft background,
  inset separators), status footer with connection dot + settings button. Same collapse-to-zero
  desktop behavior and fixed-overlay-with-backdrop mobile behavior as today's `Sidebar.svelte`.
- **Content (mode-scoped, not shared):** the wordmark and list entries change per mode. In
  Assistant mode it reads "Polaris" and lists chat threads (today's behavior, unchanged). In
  Search mode it reads "Atlas" and lists past *searches*, not chat threads — these are separate
  histories, not a merged one. The primary action button is "New thread" in Assistant mode,
  "New search" in Atlas mode.
- **Token strategy:** the sidebar uses Polaris's real `--color-*` tokens from `web/src/app.css`
  (not Atlas's own `--paper`/`--ink` palette), so it renders identically regardless of which
  mode's content area is showing — confirmed in the mockup by literally copying the light/dark
  values rather than approximating them. Both palettes are driven by the same light/dark toggle;
  Atlas's `data-theme` attribute values were renamed from an initial `paper`/`night` guess to
  `light`/`dark` specifically to match Polaris's real attribute values one-for-one, so a single
  theme switch drives both token systems without a translation layer.

Practically, this means Atlas needs its own search-history store (query text, timestamp,
favorite flag) separate from `Thread`/`Message` — mirroring the shape, not reusing the table.

## Frontend architecture

- New route tree in `web/src/routes` (e.g. `/search`), separate from `/t/[id]`'s chat UI, with
  its own layout and components in the content area — deliberately not sharing chat's content
  component tree, since the interaction models are fundamentally different (persistent
  conversation thread vs. one-shot query → ranked list). The sidebar shell is the one exception,
  per "Shared chrome" above.
- Same Go binary, same `go:embed` bundling, same backend — only the frontend route/component tree
  and (per above) the domain-ranking data model are new.
- New Go handler(s) for: serving search results (wrapping `search.SearXNGClient.Search`), the
  ranking popover's read/write endpoint (reads/writes `domain_rankings.yaml`), and the Quick
  Answer endpoint (thin wrapper around the existing agent).
- Docker two-sided sync checklist (per `CLAUDE.md`) applies to `domain_rankings.yaml` exactly like
  `blocked_sources.txt` — needs the same `COPY` line in `Dockerfile` and bind mount in
  `docker-compose.yml` once this is built.

## Visual design (see mockup for the actual pixels)

- **Distinct content-area identity, shared chrome** — Polaris's own `PRODUCT.md` calls for a
  dark, night-sky, editorial calm; Atlas's content area instead defaults to a warm-paper light
  theme (with a full dark mode available via a header toggle, defaulting to
  `prefers-color-scheme`). The reasoning: dense link-scanning in daylight/outdoor mobile use (the
  primary use case per Polaris's own "mobile is the primary surface" principle) favors a light,
  high-contrast reading surface over a dark one. The sidebar is the deliberate exception — see
  "Shared chrome" above.
- Serif titles (`ui-serif, Georgia...` stack) paired with a system-sans UI chrome — signals "a
  considered reading surface," not a generic search-engine clone. No web font dependency, fully
  offline/self-hosted-appropriate.
- Restrained warm-neutral palette + one terracotta/clay accent; the 5 ranking states get their
  own small bounded color set, scoped only to the ranking popover, not leaking into the rest of
  the page.
- Results render as a flowing list (title/url/snippet + colored favicon monogram), not a card
  grid — matches how real search engines actually lay out results and avoids the "identical card
  grid" AI-slop pattern.
- Domain ranking control is a **floating popover anchored to a small tune icon** next to each
  result's title (not a full-height slide-over drawer, not an inline expansion under the result)
  — positioned via the trigger icon's bounding box, with a caret connecting it visually, flipping
  above the icon when it would overflow the viewport bottom. Falls back to a bottom sheet on
  mobile, since anchored popovers don't work well on small touch screens.
- Quick Answer renders as a full-width tinted card (not a left-border-accented callout), citations
  as small numbered chips, sources listed below as a chip row.

## Sample/reference data

The mockup's sample results ("rust async runtime") are drawn from a real query against the local
dev SearXNG instance (port 18888 via Docker), not fabricated — see the mockup's result set for
titles/URLs/snippets that genuinely came back from DuckDuckGo/Brave.

## Next steps

1. Implement RRF-based re-ranking in `search/searxng.go`, replacing the current `score / 10`
   heuristic — requires each engine's results to carry their own within-engine rank, which means
   grouping SearXNG's merged response back out by `engine` before fusing.
2. Build the Go-side `DomainRankings` type + `domain_rankings.yaml` loader (extends
   `search/blocklist.go`), applying block/pin/raise/lower on top of the fused score.
3. Build the `/search` route tree in `web/src/routes`, porting the mockup's HTML/CSS into Svelte
   components.
4. Extract the shared sidebar shell out of `Sidebar.svelte` into a mode-agnostic component that
   takes brand text + list entries + primary-action label as inputs, so Assistant and Atlas each
   feed it their own content rather than forking the component.
5. Add a search-history store (query text, timestamp, favorite flag) for Atlas's sidebar list,
   separate from `Thread`/`Message`.
6. Wire the Quick Answer endpoint to the existing agent.
7. Add `search.autocomplete` to `compose/searxng/settings.yml` and the Dockerfile/compose
   COPY+bind-mount pair for `domain_rankings.yaml`, once the feature is ready to ship rather than
   still in design.
