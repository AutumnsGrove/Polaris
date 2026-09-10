# Constellation: backend done, frontend handoff

**Status: backend fully implemented, tested, and live-verified. Frontend not started.**
This doc is for a fresh session picking up exactly where this one left off — read this, then
`docs/plans/constellation.md` (the full spec) and `mockups/constellation.html` /
`mockups/constellation-personal-star-options.html` (the shipped UI direction) before writing any
Svelte.

Branch: `constellation-implementation`, off `main` (includes Shopping Mode). Backend is one commit:
`Implement Constellation's backend end-to-end (schema, Weaver, API, CLI)`.

## What exists and works right now

Everything below is real, tested, and was exercised against a live `polaris run` process + a real
SQLite database + a scripted fake LLM (`dev/fakeopenrouter`) — not just unit tests. Full flow
confirmed working: creating a thread → enabling Constellation → backfill → a real star landing in
the DB → every review/edit/restore/rename/disable action → the digest and week-feed queries.

- **`store/constellation.go`** — every table from the plan doc's "Database schema", plus:
  `GetConstellationStats`, `GetConstellationDigest`, `GetConstellationWeekFeed`,
  `EligibleConstellationThreads`/`EligibleConstellationThreadsForBackfill`, `AllStarEdges`.
- **Weaver's five tools** — `tools/search_stars.go`, `read_star.go`, `create_star.go`,
  `update_star.go`, `link_stars.go` + matching `tools/descriptions/*.yaml`. Gated via
  `requires: weaver_run`; Weaver is *additionally* restricted to only those five tools (see
  `catalog.go`'s `offered()` — a `ctx.WeaverRun` check that excludes everything else, not just an
  allowlist of what's added).
- **`prompts.yaml`'s `weaver:` section** — system prompt, revisit-instruction template,
  reconciliation prompt for Edit star/Refine.
- **`gateway/constellation_weaver.go`** — `RunShootingStar(reqCtx, db, client, threadID)`, the
  actual Weaver agent.Run wrapper. First-pass vs. revisit (double-RAG filter — this is why
  `tools.FilterExtractedText` got exported, it was unexported before). Turn-cap detection via
  `result.TurnCount == maxTurns+1` (agent.Run's forced-wrapup signal).
- **`gateway/constellation_scheduler.go`** — `Server.RunConstellationScheduler`, wired into
  `cmd/run.go` next to `RunPulsarScheduler`. `runConstellationTick` is the testable free-function
  core.
- **`gateway/constellation_routes.go`** — the full REST API (see "API reference" below).
- **`gateway/constellation_backfill.go`** + **`cmd/constellation_backfill.go`** —
  `polaris constellation backfill -n N`, dual-mode (bare-metal opens the DB directly; Docker mode
  proxies through `POST /api/constellation/backfill` on the running container, since the CLI binary
  outside the container can't reach the Docker-volume DB).

Run `go test ./...` from repo root — everything passes. ~50 new tests across `store`/`tools`/
`gateway`/`prompts`.

## What's NOT done — this is the actual next task

**Nothing in `web/src` exists for Constellation yet.** The 10 screens in
`mockups/constellation.html` (Library, Star detail, Map, Edit star, Inbox, Review star, Refine,
This week, Constellation settings, Constellation Usage) need real SvelteKit implementations wired
to the API below. Read the mockup file directly (it's real HTML/CSS, not just a description) —
it's marked "Option D," the already-chosen direction, not one of several to pick from.

## API reference (what the frontend has to work with)

All routes live in `gateway/constellation_routes.go`, registered in `gateway/server.go` right after
the Pulsar Daily routes. `store.Star`/`store.StarSource`/`store.StarEdge`/`store.StarEdgePair`/
`store.ConstellationConfig`/`store.ConstellationStats` all have `json:` tags — read those structs
directly rather than guessing shapes.

```
GET  /api/constellation/config              -> store.ConstellationConfig
PUT  /api/constellation/config               body {enabled, poll_interval_minutes, model} -> same
GET  /api/constellation/stats?period_days=N  -> store.ConstellationStats
GET  /api/constellation/stars?section=X      -> []store.Star
                                                 section is one of: library | about_you | inbox | rejected
GET  /api/constellation/stars/{id}           -> {star, sources: []StarSource, edges: []StarEdge}
PATCH /api/constellation/stars/{id}          body {title?, disabled?} (pointers — only sent fields change)
POST /api/constellation/stars/{id}/restore   -> store.Star (rejected -> confirmed, not proposed)
POST /api/constellation/stars/{id}/review    body {action: approve|discard|refine, correction?} -> store.Star
POST /api/constellation/stars/{id}/edit      body {correction} -> store.Star (real LLM reconciliation call)
GET  /api/constellation/digest               -> {new_count, links_count, highlight, show}
GET  /api/constellation/week                 -> []{kind: new|updated|linked, title, detail?, timestamp}
GET  /api/constellation/map                  -> {stars: []Star, edges: []StarEdgePair}
POST /api/constellation/backfill?limit=N     -> {processed} (Docker-mode target, not for normal UI use)
```

Section semantics, since they're not obvious from the route alone: **`library`** = non-personal
`auto`/`confirmed` stars (this is what the grouped-by-category home view reads, then the frontend
groups by `category` client-side — there's no server-side category grouping). **`about_you`** =
personal `auto`/`confirmed` stars only — a personal star stays in `inbox` until reviewed, same as
any other proposed star, then moves here once confirmed. **`inbox`** = every `proposed` star,
personal or not. **`rejected`** = self-explanatory, each card needs a Restore button hitting the
restore endpoint.

## Settings-panel and Usage-panel wiring

Per the plan doc: Constellation settings gets its own section in the existing settings panel
(`SettingsPanel.svelte`), same shape as Pulsar/Pulsar Daily's sections — on/off toggle, poll
interval, model picker (`"Same as chat (default)"` maps to `model: ""`). Constellation Usage is a
separate panel reached via its own `Info` icon, same `showStats`/`showMemory`-style sibling-panel
pattern `SettingsPanel.svelte` already uses — plus one discoverability link from the *main* Usage
panel ("→ Constellation usage"). Don't merge Constellation's stats into the main `Stats`/
`CostBySource` — it's deliberately a wholly separate surface (see the plan doc's "Cost tracking and
observability").

## Conventions to match (read these before writing Svelte)

- `web/src/lib/pulsarDaily.svelte.ts` — closest existing analog: a `.svelte.ts` state module
  wrapping fetch calls to a sibling feature's REST API. `web/src/lib/components/
  PulsarDailyConfigModal.svelte` is the closest existing settings-modal analog.
- `web/src/routes/daily/` is the closest existing *route* analog if Constellation gets its own
  top-level route (`/constellation` or similar) rather than living inside a modal/sheet stack —
  the mockup's 10 screens (a Library home view, a Map tab, several sheet-style overlays) suggest a
  real route makes more sense than a modal, but that's a call for whoever picks this up to confirm
  against the mockup's actual navigation model before assuming either way.
- CLAUDE.md's UI conventions: use the shared CSS custom properties in `app.css`'s `:root`
  (`--z-*`, `--radius-*`, `--space-*`) instead of raw px literals.
- `web/build/` needs rebuilding and committing before this ships to bare-metal (Docker builds
  fresh from source) — `git config core.hooksPath .githooks` once makes this automatic on commit.
- Personal-star visual treatment is already decided: `--color-personal`, a soft violet — see the
  plan doc's "Personal stars" section and `mockups/constellation-personal-star-options.html` (which
  mocked five options before this one was picked, so don't re-litigate it).

## Testing this live once the frontend exists

`dev/fakeopenrouter` is how this session verified the backend without burning real API calls —
point `config.yaml`'s `openrouter.base_url` at it, queue scripted `create_star`/`update_star`/
`link_stars` tool calls via its `/_control/queue` HTTP API. **Use the `match` field** to pin a
response to a specific request (a plain chat turn makes several LLM calls — main answer, title,
suggestions — that will steal a FIFO-queued response meant for Weaver if you don't pin it). See the
package doc comment in `dev/fakeopenrouter/main.go`.

`polaris constellation backfill -n 1` (bare-metal, run from a directory *without*
`docker-compose.yml` in it — that file's presence is what `isDockerComposeInstall` checks, and the
real repo root has one) is the fastest way to get one real star into a dev database to build UI
against, instead of waiting on the once-a-minute poller.

## Known gaps / deliberate simplifications from this pass

- **`shooting_star_events` cost granularity**: the plan doc asks for one row per LLM *completion
  call* with its own cost. What's actually implemented logs one row per *tool call* (cost 0, since
  tool dispatch itself isn't separately billed) plus the run's `cost_usd` rollup from
  `FinishShootingStarRun`'s own accounting — not a row-per-completion-turn breakdown. Getting true
  per-turn granularity would need a hook into `agent.Run`'s internal loop
  (`agent/driver.go`'s `totalCost += resp.CostUSD`), which wasn't done here. Cost totals are
  correct; the *per-turn* trace the plan doc describes is coarser than specified.
- **Weekly digest** is v1 as specced (pure counts, zero LLM calls) — the v2 "synthesized prose"
  version is explicitly deferred in the plan doc itself, not a gap from this pass.
- **Saved links** (v2 feature) — not touched, per the plan doc's own deferral.
