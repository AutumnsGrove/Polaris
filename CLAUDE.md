# CLAUDE.md

Guidance for Claude Code (or any agent) working in this repo. This file is checked in and
takes precedence over any generic/global instructions for work done here.

## What this is

Polaris is a private, self-hosted, search-augmented AI assistant — a single Go binary (SvelteKit
frontend embedded via `go:embed`) that answers questions by actually searching the web (via a
self-hosted SearXNG instance) and citing sources, closer to Kagi Assistant/Perplexity than a
chatbot. Single-operator tool, primarily used from a phone over Tailscale. See `README.md` for the
full feature list and `PRODUCT.md` for the design philosophy ("sourcing is the product," calm
over clever, night-sky-not-tech-neon).

## Production access

The real, live deployment runs on a Le Potato SBC (aarch64/Armbian), reachable via
`ssh potato-remote` (a Tailscale-IP SSH config alias; `potato` is the LAN-IP alias). Install and
production have been Docker-only since 2026-08-15 (further cemented by a 2026-09 migration that
deleted the bare-metal install/production code paths outright — see
`docs/plans/docker-only.md`). Install lives at `~/Polaris` on that host.

Local dev of the Go backend still runs bare-metal (`go run .`) — that's a deliberate, separate
choice from the install/production question, not a leftover: see "Install and production are
Docker-only; local dev is bare-metal Go" below.

Useful live-diagnostic commands once SSH'd in:
- `docker compose ps` — container status
- `docker compose logs polaris` / `-f` — logs
- `curl http://127.0.0.1:8899/api/version` — `{"deployment":"docker","version":"rNNN.hash"}`
- `docker run --rm -v polaris_polaris-data:/data alpine sh` — direct access to `polaris.db`/logs/
  attachments, which live in a named Docker volume, not plain host files
- `systemctl status polaris-update.path polaris-update.timer` — the update watcher's systemd units

## Updating the production deployment

**Use `polaris update` / `polaris restart` over SSH, or the settings panel's buttons — both are
thin clients hitting the running container's own REST API (`cmd/docker_client.go`'s
`runDockerModeCall`), never anything host-side beyond that HTTP call.**

`polaris update` resolves the latest published image's digest from GHCR (waiting out an
in-progress CI build first, so a click right after `git push` can't silently grab the *previous*
build), then hands off to a host-side systemd watcher (`compose/watcher/update.sh`) that first
`git fetch`/`merge --ff-only`s the host checkout (so `docker-compose.yml`'s env passthrough list
and the bind-mounted config templates land *with* the new image, not days later on whoever next
remembers to `git pull` by hand — a real gap found live: an image update alone left a
newly-added secret's passthrough line missing from `docker-compose.yml` until the checkout was
synced manually), then does `docker compose pull && up --force-recreate` — the container itself
never gets any control over Docker. The `--ff-only` refuses rather than clobbering if the host
checkout has diverged (uncommitted edits, a stray local commit).
- If you rebuild the *host-side* `polaris` binary on the potato (needed after a CLI-only code
  change — `cd ~/Polaris && go build -o polaris .`) note that's separate from the running
  container's own image; the CLI binary is just a thin client hitting the container's REST API
  under Docker, it doesn't need to match the container's version.
- Images publish automatically to `ghcr.io/autumnsgrove/polaris` (multi-arch) via
  `.github/workflows/docker-publish.yml` on every push to `main`.

## Install and production are Docker-only; local dev is bare-metal Go

These are two separate questions with two separate, deliberate answers — don't conflate them:

- **Install/production: Docker-only, full stop.** `install.sh` only sets up a Docker Compose
  stack; there is no bare-metal install path anymore (`docs/plans/docker-only.md` deleted it
  outright in 2026-09 — `cmd/install.go`, `procmgr/`, `updater/`, and every
  `isDockerComposeInstall()` branch across `cmd/` and `gateway/update.go` are gone). Every CLI
  command that talks to a running Polaris (`polaris update`/`restart`/`search`/`stats`/`backup`/
  `atlas search`/`constellation backfill`) is unconditionally a thin HTTP client hitting the
  container's own REST API (`cmd/docker_client.go`'s `runDockerModeCall`, or the ad hoc
  equivalents in `cmd/search.go`/`cmd/stats.go`/`cmd/atlas.go` — reuse the real endpoint's
  request/response types, e.g. `gateway.AskRequest`, `store.Stats`, instead of redefining them).
  `polaris backup restore`/`restore-remote` are the one exception: no live-API-based restore
  exists (swapping the database file under a running connection is unsafe), so those two always
  run directly against a local config/database path — under Docker that means invoking them
  inside a fresh one-off container via `docker compose run --rm --no-deps polaris backup
  restore ...` (see `backupRestoreCmd`'s `--help`), not over HTTP.
- **Local dev of the Go backend: still plain `go run .`/`go build`, deliberately.** The compile-
  and-restart loop is already fast; containerizing it would be a real regression for zero
  benefit, since `install.sh` was never part of the inner dev loop to begin with. `code_exec`
  (the one feature needing a container) is reachable from a bare-metal dev instance too — its
  gate is a pure capability check (`cfg.CodeExec.HostWorkspaceDir`/`SignalDir` configured, see
  `gateway/turn.go`), not a deployment-mode check — see `DEVELOPMENT.md`'s "Local dev code_exec"
  section for the concrete setup (Docker installed locally for the sandbox only, plus running
  `compose/watcher/codeexec.sh` in a loop). `dev/stack.sh` (issue #72) starts/stops/restarts the
  whole bare-metal inner loop — vite, `go run . run --dev`, that codeexec watcher loop, and the
  local SearXNG container — in one command instead of juggling each piece by hand; see
  `DEVELOPMENT.md`'s "One-command dev stack" section.

**When adding a new feature, a couple of things still matter even though there's only one
install/production shape now:**

1. **Does it read a file relative to CWD?** `prompt.md`, `prompts.yaml`, `blocked_sources.txt` are
   all loaded this way, hot-reloaded, and hand-editable — and Docker's runtime CWD is `/app` inside
   the container, not the repo root. Each one is baked into the image via `Dockerfile`'s `COPY` line
   *and* bind-mounted over top in `docker-compose.yml` so host edits still take effect without a
   rebuild (see the Dockerfile's comment on why both). **A new hot-editable resource directory
   needs the same two-sided treatment** — a `COPY` in the Dockerfile, a bind mount in
   docker-compose.yml, matching what already exists for the three files above. (Keep an eye on
   `docker-compose.yml`'s bind-mount list and `Dockerfile`'s `COPY` lines staying in sync with each
   other — nothing enforces that automatically today.)

2. **Does it add a settings-panel action that mutates server state?** The container itself is
   deliberately never given control over Docker (no socket mount, see `gateway/docker_update.go`)
   — anything that needs to affect the running deployment writes a signal file the host-side
   watcher (`compose/watcher/update.sh`, a real systemd path-unit + oneshot service on Linux)
   picks up instead.

3. **Does it touch the frontend's update/restart polling logic?** `waitForServerAndReload` in
   `web/src/lib/settings.svelte.ts` has real, non-obvious constraints — e.g. a plain restart never
   changes the reported version by design, so "did the version change" can't be the success signal
   for that case. Read its doc comments before changing the polling condition.

4. **Does it change what image gets built?** `Dockerfile`'s frontend and Go build stages are both
   pinned to `--platform=$BUILDPLATFORM` deliberately (native cross-compilation, no QEMU for the
   slow steps) — don't remove that pin without understanding why it's there. `main.Version` /
   `version.go` **must** stay a bare string-literal initializer, never a computed one — Go's
   `-ldflags -X` silently can't override a computed initializer, which was a real, previously
   undetected bug.

## Verify on real hardware, not just review or mocked tests

This came up repeatedly building the Docker path: careful code review and passing mocked unit
tests did not catch several real bugs (a script missing its executable bit, a UID/permission
mismatch between the container's non-root user and a host bind mount, `docker compose up -d`
being a silent no-op when nothing about the target changed, the `-ldflags -X` initializer gotcha
above) — only actually running the thing against the real potato deployment did. Prefer:
`ssh potato-remote` and exercise the real path (`docker compose ps`, `curl` the real endpoints,
watch `systemctl status polaris-update.service`) over trusting that review + `go test` is enough,
specifically for anything touching install/update/restart/deployment.

More generally: this codebase has a strong existing culture of live-verifying before calling
something done — spike-testing third-party APIs with `curl` before writing implementation code
against them, running new tools through the real server via `/api/ask` rather than only unit
tests, reverting a bugfix to confirm its regression test actually fails without it. Follow that
pattern for new work here, not just the Docker-specific cases above.

## Where things live

- `Dockerfile` / `docker-compose.yml` / `.dockerignore` — the image and the local compose stack
- `compose/polaris/config.yaml.example`, `.env.example` — Docker's config, split across two files
  (compose-level secrets vs. app-level settings) — see README's "Docker install" for why
- `compose/watcher/` — the host-side systemd units + script that actually pulls/recreates the
  container; never touches Docker from inside Polaris's own container
- `compose/searxng/settings.yml` — SearXNG config for the bundled Docker instance (JSON output
  pre-enabled, unlike the bare-metal default)
- `gateway/docker_update.go`, `gateway/docker_ci_status.go` — the Docker-mode HTTP handlers,
  including the GHCR digest resolution and the "wait out an in-progress CI build" race-window fix
- `cmd/docker_client.go` — the CLI's thin-client pattern for reaching a running container
- `.github/workflows/docker-publish.yml` — multi-arch (`amd64`+`arm64`) GHCR publish on every push
  to `main`; `.github/workflows/go-ci.yml` — build/vet/test on Go changes
- `install.sh` — the only install path; sets up Docker Compose, nothing else
- `dev/stack.sh` — one-command bare-metal dev stack launcher (`start`/`stop`/`restart`/`status`);
  see `DEVELOPMENT.md`'s "One-command dev stack" section
- `dev/fakeopenrouter/` — a scriptable stand-in for OpenRouter's streaming `/chat/completions` API,
  for exercising a real running `polaris run` (gateway, agent loop, tool dispatch, the actual
  SvelteKit frontend over a real WebSocket) against a canned model instead of a paid, non-deterministic
  one — same idea as `llm/llmtest.MockClient` (which Go unit tests use directly), just as an HTTP
  double instead of a Go interface double, since a live server process has no seam to inject a mock
  client into (`gateway/turn.go` always constructs a real `llm.NewClient`). Point `config.yaml`'s
  `openrouter.base_url` at it and queue scripted responses (plain answers or tool calls, including
  multi-turn scenarios) via its `/_control/queue` HTTP API; `/_control/calls` returns every request
  body it actually received, for asserting what the app really sent — e.g. that a disabled tool
  didn't make it into that turn's offered tools list. Built for driving Playwright against the real
  app from a Claude Code remote/cloud session with no real `OPENROUTER_API_KEY` on hand; see the
  package doc comment in `dev/fakeopenrouter/main.go` for the full usage example. Plain FIFO only
  works for sequential turns — Pulsar Daily's Stage A fires N blocks as genuinely concurrent
  `/chat/completions` calls (see `gateway/pulsar_daily.go`), so which physical request lands in
  which queue slot depends on goroutine scheduling, not which logical block asked. A queued
  response's optional `match` substring pins it to whichever request body actually contains that
  text instead, ahead of plain-FIFO entries and independent of queue position — see the same doc
  comment's "Plain FIFO breaks down..." section.
- `prompts.yaml` / `prompts/prompts.go` — every LLM prompt fragment except `prompt.md` itself,
  hot-reloaded with compiled-in defaults as a fallback
- `search/searxng.go` — SearXNG's own engines (Brave, Google, DuckDuckGo, Startpage) do rate-limit
  a self-hosted instance under real usage; `SearXNGClient` detects a full outage (every
  general-category engine unresponsive at once, not just one) and enters an hour-long cooldown
  (raised from an initial 20-minute guess — live observation showed the underlying engines still
  suspended well past 20 minutes) rather than repeatedly hammering an already-rate-limited service
- `brave/`, `parallel/`, `tavily/tavily.go`'s `Search` method — the three-tier paid fallback chain
  `tools/web_search.go` reaches for once SearXNG confirms itself degraded (Brave first, since it's
  the only one returning real multi-result listings rather than an AI-pre-summarized answer — a
  better fit for anything Atlas surfaces, not just the assistant's own citations; then Parallel,
  since its free tier of 5,000/mo is 5x Tavily's 1,000/mo; then Tavily). Brave has no ongoing free
  tier at all (a one-time $5/mo signup credit only) and Parallel's account has a card on file, so
  `store.Store`'s `api_usage` table enforces a hard monthly cap for each (`brave.MonthlyCap`,
  `parallelMonthlyCap` in `tools/web_search.go`) before ever calling them — never raise or bypass
  either cap without confirming real usage with the user first. Every result set is tagged
  `[via <provider>]` so a fallback firing is visible in the transcript, not just server logs. Atlas's
  own results-browsing page (`gateway/search.go`'s `handleSearch`) has a *separate* Brave fallback
  from the assistant's `web_search` tool, with its own virtual sub-pagination: one real Brave fetch
  (`count=20`, Brave's own per-request max) is split into two 10-result Atlas pages
  (`braveFallbackSearch`'s `braveVirtualPageSize`) before a second real request fires — this exists
  because Atlas needs raw, real search-result listings for its browsing UI, not the
  agent-facing/pre-summarized shape Parallel/Tavily return, so it can't just reuse `web_search`'s
  fallback chain wholesale

## Web search fallback chain

`web_search` tries SearXNG first, then Brave, then Parallel, then Tavily, only when SearXNG has
confirmed a full outage (not an ordinary empty result) — see `search.SearXNGClient`'s
cooldown/degraded logic and `tools/web_search.go`'s `handleWebSearch`. Wiring this into a new call
site means giving it `tools.Context.Brave`/`Parallel`/`Tavily` **and** a real `store.Store` for the
usage-cap closures (`BraveUsageThisMonth`/`IncrementBraveUsage`,
`ParallelUsageThisMonth`/`IncrementParallelUsage`,
`TavilyUsageThisMonth`/`IncrementTavilyUsage`) — `cmd/search.go` originally had none of these
wired for the CLI's one-shot `polaris search` path even after the web UI/assistant got them, a real
gap only found by running `polaris search` live and checking what it actually had access to, not by
code review. Any new CLI command or server entry point that can trigger `web_search` needs the same
five pieces (SearXNG, Brave, Parallel, Tavily + DB-backed usage closures), not just the LLM client.

`TavilyUsageThisMonth`/`IncrementTavilyUsage` back a single `tavilyMonthlyCap` (`tools/web_search.go`)
shared across every way a deployment can spend a Tavily credit — this file's Search fallback above,
`web_read`'s own JS-render/paywall Extract fallback, and `web_read`'s explicit `force_tavily`
argument (for when the model already has specific reason to believe a plain read of a URL gave
stale data, e.g. a live-updating page whose numbers are only ever populated by client-side polling —
the ordinary err/paywall/looksEmpty heuristic chain never trips for a page like that, since the free
fetch still comes back a real 200 with real, if stale/sparse, text). Unlike Brave/Parallel, nothing
enforced a real ceiling on Tavily before this — it was only "scarce" by comment/convention — which
mattered less while every Tavily call was an accidental last-resort fallback; `force_tavily` lets the
model spend a credit on purpose, so the cap needed to be real too. Any new place that wires
`tools.Context.Tavily` needs the usage-cap closures alongside it, same as Brave/Parallel.

## Pulsar and Pulsar Daily

"Pulsar" is not a typo for Polaris and not a mismatched term — it's a real, sizeable subsystem
(routines + `tools/`, `store/`, `gateway/` files all named `pulsar_*`), named for the astronomical
object: a saved prompt that fires on a schedule instead of when you type it, each firing ("pulse")
running through the exact same `agent.Run` turn pipeline as a normal message. One scheduler
(`gateway/pulsar_scheduler.go`, a once-a-minute goroutine, same no-external-cron shape as
`backup.go`'s daily snapshot job) drives two distinct surfaces built on that one primitive:

- **Pulsar** (`/pulsar`) — user-defined recurring routines (daily/weekly/monthly), each a real
  thread (`threads.source == "pulsar"`) told what it reported last time so it states only what's
  new rather than restating still-true facts. `gateway/pulsar_wizard.go`'s ephemeral,
  non-persisted interview turns a vague idea into a tuned prompt;
  `tools/finalize_pulsar_prompt.go` forces that final output through a tool call instead of
  parseable prose. Design doc: `docs/plans/pulsar-routines.md`.
- **Pulsar Daily** (`/daily`) — a *different, singleton* surface, not a `routine.kind == 'daily'`
  special case: one "morning newspaper" edition/day assembling ~10 independent mini-generations
  (`gateway/pulsar_daily.go`'s Stage A — weather is a direct tool call, word-of-day/on-this-day/
  quote are single no-tools LLM picks, headlines/trending/local/sports/custom blocks are
  narrow-toolset `agent.Run`s) rather than one big agent turn. A second, tool-call-only pass
  (`tools/finalize_daily_items.go`) diffs each "Watch" block against yesterday's stored content
  and can drop it as unchanged; a ranking pass elects a Top Story for deeper elaboration. Storage
  is a singleton daily config, not routine-shaped. Design doc: `docs/plans/pulsar-daily.md`
  (living/mid-design — check its "Status" line before assuming a section is final).

Relevant code beyond the two `gateway/pulsar_scheduler.go`/`pulsar_wizard.go` files above:
`gateway/pulsar_routes.go`, `gateway/pulsar_daily_routes.go`, `store/pulsar.go`,
`store/pulsar_daily.go`. README's Pulsar/Pulsar Daily bullets are the user-facing description;
this section is only the "where the code lives" pointer.

## Keeping README.md / SETUP.md / DEVELOPMENT.md in sync

Docs are split three ways (as of the 2026-09 restructuring): `README.md` is a short pitch + a
trimmed feature list + license, `SETUP.md` is the all-in-one install/config/backup reference, and
`DEVELOPMENT.md` covers architecture, frontend dev, the CLI, and deployment internals. When a
change adds or meaningfully changes a user-facing tool or feature, update `README.md`'s Features
list — one or two lines, what it does, not why it's built that way (no fallback-chain internals,
no historical bugs, no design-tradeoff narrative; that belongs in code comments or `docs/plans/`).
If the change adds a new requirement (an API key, a Docker-only gate, a config field), add it to
`SETUP.md`'s Requirements section too. If it changes the architecture diagram, the CLI, or how the
app is built/deployed, that's `DEVELOPMENT.md`. Don't let any of the three creep back into a
500-line wall — that's the exact problem this split fixed.

## Conventions worth knowing before editing Go here

See `docs/STANDARDS.md` for the full coding standards doc (error handling, comment style, the
three-strikes reuse rule, Svelte/state-management conventions, testing conventions) — it's the
long-form version of the bullets below, written from this codebase's own established patterns
rather than a generic style guide. Check it before a review pass or when something in a diff feels
inconsistent with the rest of the file.

- `uv`/Python-specific instructions some global CLAUDE.md files carry do **not** apply — this is a
  Go + SvelteKit project. Use `go build`, `go test ./...`, `go vet ./...` directly.
- `web/build/` is not committed — Docker's image build always runs `pnpm run build` fresh from
  `web/src/` (`Dockerfile`'s frontend-build stage). For bare-metal dev/testing, run
  `cd web && pnpm run build` by hand whenever the frontend changes; see `DEVELOPMENT.md`.
- Comment style in this codebase explains *why*, not *what* — non-obvious constraints, prior
  incidents, races being guarded against. Match that density when adding new code; a lot of the
  Docker-path bugs were specifically caught because a doc comment recorded the exact reasoning a
  reviewer needed to spot the gap.
- UI work (`web/src`) must use the shared CSS custom properties in `app.css`'s `:root` —
  `--z-*` for stacking, `--radius-*` for corner rounding, `--space-*` for padding/margin/gap —
  instead of a new raw px literal. Pick the nearest existing step rather than inventing a value;
  only add a new token when nothing on the scale actually fits.
