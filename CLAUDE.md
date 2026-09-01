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
`ssh potato-remote` (a Tailscale-IP SSH config alias; `potato` is the LAN-IP alias). It's been
Docker-only since 2026-08-15 — bare-metal is fully supported by the codebase and still how local
dev typically runs, but the actual production host runs the Docker Compose stack. Install lives at
`~/Polaris` on that host either way (same git checkout path, just a different runtime).

Useful live-diagnostic commands once SSH'd in (Docker):
- `docker compose ps` — container status
- `docker compose logs polaris` / `-f` — logs (equivalent of `journalctl -u polaris` on bare-metal)
- `curl http://127.0.0.1:8899/api/version` — `{"deployment":"docker","version":"rNNN.hash"}`
- `docker run --rm -v polaris_polaris-data:/data alpine sh` — direct access to `polaris.db`/logs/
  attachments, which live in a named Docker volume, not plain host files
- `systemctl status polaris-update.path polaris-update.timer` — the update watcher's systemd units

## Updating the production deployment

**Prefer `polaris update` / `polaris restart` over SSH, or the settings panel's buttons — both
auto-detect bare-metal vs. Docker and just work correctly either way, no flag needed.** This is a
deliberate design point: as of the Docker-CLI-parity work, every update/restart path (settings
panel and CLI, both install methods) goes through the exact same underlying logic, so there's
nothing to remember about which mode a given host is in.

- **Bare-metal**: `polaris update` does `git pull && go build && restart` locally.
- **Docker**: `polaris update` resolves the latest published image's digest from GHCR (waiting out
  an in-progress CI build first, so a click right after `git push` can't silently grab the
  *previous* build), then hands off to a host-side systemd watcher (`compose/watcher/update.sh`)
  that first `git fetch`/`merge --ff-only`s the host checkout (so `docker-compose.yml`'s env
  passthrough list and the bind-mounted config templates land *with* the new image, not days
  later on whoever next remembers to `git pull` by hand — a real gap found live: an image update
  alone left a newly-added secret's passthrough line missing from `docker-compose.yml` until the
  checkout was synced manually), then does `docker compose pull && up --force-recreate` — the
  container itself never gets any control over Docker. The `--ff-only` refuses rather than
  clobbering if the host checkout has diverged (uncommitted edits, a stray local commit).
- If you rebuild the *host-side* `polaris` binary on the potato (needed after a CLI-only code
  change — `cd ~/Polaris && go build -o polaris .`) note that's separate from the running
  container's own image; the CLI binary is just a thin client hitting the container's REST API
  under Docker, it doesn't need to match the container's version.
- Images publish automatically to `ghcr.io/autumnsgrove/polaris` (multi-arch) via
  `.github/workflows/docker-publish.yml` on every push to `main`.

## This project ships two deployment models — most new features touch both

Polaris runs bare-metal (a plain `go build` binary + systemd/launchd) **and** via Docker Compose
(a container pulling prebuilt images from GHCR, updated by a host-side watcher). Both are real,
supported install paths — not "Docker as a dev convenience" — see README's "Docker install"
section for the full picture. Before this dual-support existed, plenty of code only had to think
about one runtime shape; a lot of the actual bugs found while building the Docker path came from
code that quietly assumed bare-metal and nobody noticed until it ran somewhere else.

**When adding a new feature, run through this checklist — most items are "does this even apply
under Docker," not "go build Docker support for X":**

1. **Does it read a file relative to CWD?** `prompt.md`, `prompts.yaml`, `blocked_sources.txt` are
   all loaded this way, hot-reloaded, and hand-editable — and Docker's runtime CWD is `/app` inside
   the container, not the repo root. Each one is baked into the image via `Dockerfile`'s `COPY` line
   *and* bind-mounted over top in `docker-compose.yml` so host edits still take effect without a
   rebuild (see the Dockerfile's comment on why both). **A new hot-editable resource directory
   needs the same two-sided treatment** — a `COPY` in the Dockerfile, a bind mount in
   docker-compose.yml, matching what already exists for the three files above. (Keep an eye on
   `docker-compose.yml`'s bind-mount list and `Dockerfile`'s `COPY` lines staying in sync with each
   other — nothing enforces that automatically today.)

2. **Does it add a CLI command, or touch an existing one?** Every `cmd/*.go` command needs to
   either (a) work correctly under both deployment models, or (b) explicitly refuse under one with
   a clear message — never silently do the wrong thing. The established pattern:
   `isDockerComposeInstall(repoPath)` (checks for `docker-compose.yml` in the working directory —
   **not** `gateway.deploymentMode()`, which reads an env var only ever set *inside* the container,
   never in a host-side SSH session) gates a branch to a thin HTTP client hitting the running
   container's own REST API (`cmd/docker_client.go`'s `runDockerModeCall`, or the ad hoc
   equivalents in `cmd/search.go`/`cmd/stats.go`). Reuse the real API endpoint's request/response
   types (`gateway.AskRequest`, `store.Stats`, ...) instead of redefining them — see those two
   files for the exact shape. `cmd/install.go` is the "explicitly refuse" case: there's no
   systemd/launchd unit to write under Docker, so it says so instead of doing something misleading.

3. **Does it add a settings-panel action that mutates server state?** Bare-metal and Docker
   diverge hard here — see `gateway/update.go`'s `deploymentMode() == "docker"` branch pattern and
   `gateway/docker_update.go`. Under Docker, the container itself is deliberately never given
   control over Docker (no socket mount) — anything that needs to affect the running deployment
   writes a signal file the host-side watcher (`compose/watcher/update.sh`, a real systemd
   path-unit + oneshot service on Linux) picks up instead.

4. **Does it touch the frontend's update/restart polling logic?** `waitForServerAndReload` in
   `web/src/lib/settings.svelte.ts` has real, non-obvious constraints — e.g. a plain restart never
   changes the reported version by design, so "did the version change" can't be the success signal
   for that case. Read its doc comments before changing the polling condition.

5. **Does it change what image gets built?** `Dockerfile`'s frontend and Go build stages are both
   pinned to `--platform=$BUILDPLATFORM` deliberately (native cross-compilation, no QEMU for the
   slow steps) — don't remove that pin without understanding why it's there. `main.Version` /
   `version.go` **must** stay a bare string-literal initializer, never a computed one — Go's
   `-ldflags -X` silently can't override a computed initializer, which was a real, previously
   undetected bug (bare-metal's own build never exercised `-X` at all until Docker's CI pipeline
   did).

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
  to `main`; `.github/workflows/go-ci.yml` — build/vet/test on Go changes;
  `.github/workflows/frontend-build-sync.yml` — fails a PR if `web/build/` drifts from `web/src/`
- `install.sh` — `POLARIS_INSTALL_MODE=docker` is the Docker install path; default is bare-metal
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
  package doc comment in `dev/fakeopenrouter/main.go` for the full usage example.
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
`ParallelUsageThisMonth`/`IncrementParallelUsage`) — `cmd/search.go` originally had none of these
wired for the CLI's one-shot `polaris search` path even after the web UI/assistant got them, a real
gap only found by running `polaris search` live and checking what it actually had access to, not by
code review. Any new CLI command or server entry point that can trigger `web_search` needs the same
four pieces (SearXNG, Brave, Parallel, Tavily + DB-backed usage closures), not just the LLM client.

## Conventions worth knowing before editing Go here

- `uv`/Python-specific instructions some global CLAUDE.md files carry do **not** apply — this is a
  Go + SvelteKit project. Use `go build`, `go test ./...`, `go vet ./...` directly.
- Frontend changes require `web/build/` to be rebuilt and committed for bare-metal (the potato
  can't run `pnpm install`/`vite build` itself) — `git config core.hooksPath .githooks` (once)
  makes this automatic on commit. Docker doesn't have this constraint (builds fresh from source in
  CI/on `docker compose up --build`), but the committed `web/build/` still needs to stay in sync
  for bare-metal, and `frontend-build-sync.yml` enforces it in CI either way.
- Comment style in this codebase explains *why*, not *what* — non-obvious constraints, prior
  incidents, races being guarded against. Match that density when adding new code; a lot of the
  Docker-path bugs were specifically caught because a doc comment recorded the exact reasoning a
  reviewer needed to spot the gap.
- UI work (`web/src`) must use the shared CSS custom properties in `app.css`'s `:root` —
  `--z-*` for stacking, `--radius-*` for corner rounding, `--space-*` for padding/margin/gap —
  instead of a new raw px literal. Pick the nearest existing step rather than inventing a value;
  only add a new token when nothing on the scale actually fits.
