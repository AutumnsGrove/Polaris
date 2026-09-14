# Development

Architecture, frontend development, the CLI, and deployment internals. For install and
configuration, see [SETUP.md](SETUP.md). Agent-specific conventions (Go/SvelteKit build commands,
the Docker-vs-bare-metal checklist for new features, etc.) live in `CLAUDE.md`.

## Architecture

```
Browser (SvelteKit SPA, embedded in the Go binary via go:embed)
  ↕ WebSocket (/ws) + REST (/api/*)
Go backend
  ├── agent    — tool-use loop: think / web_search / web_read / nearby_search / youtube_transcript /
  │              weather / reference_lookup / github_repo / dictionary / music / books / movies /
  │              memory / ask_user_question, or just answer — independent tool calls in the same
  │              turn run concurrently
  ├── llm      — OpenRouter client, provider-pinned per model for consistent prompt-cache pricing
  ├── search   — SearXNG client; detects a full engine outage and enters a cooldown
  ├── places   — Foursquare + Nominatim geocoding
  ├── brave, parallel, tavily — the paid search-fallback chain (Brave → Parallel → Tavily), plus
  │              Tavily's separate Extract API for web_read's JS-rendering fallback — see
  │              [SETUP.md's Requirements](SETUP.md#requirements)
  ├── voice    — Voxtral (speech-to-text) + Kokoro-82M (text-to-speech), both via OpenRouter
  ├── store    — SQLite: threads, messages, memories, settings, running cost, per-provider API
  │              usage counts
  ├── backup   — daily VACUUM INTO snapshots of the database, rotation, and restore — see
  │              [SETUP.md's Backups](SETUP.md#backups)
  ├── r2       — hand-rolled SigV4 client mirroring backups off-device to Cloudflare R2
  └── updater  — git pull + rebuild, shared by the CLI and the settings panel's update button
```

One binary, no Node.js at runtime. The SvelteKit frontend is built ahead of time and its static
output is committed to the repo and embedded directly into the Go binary, so the machine running
this only ever needs the Go toolchain — nothing else to install, nothing else to keep running.

Two ways to run it: bare-metal (the binary directly) or Docker Compose, which bundles a SearXNG
instance alongside it — see [SETUP.md](SETUP.md). Both are full deployments, not a dev-only
convenience.

## Frontend development

The Go binary embeds the frontend's built static output (`web/build/`), which is committed to
this repo — the potato is a Le Potato SBC, too weak to run `pnpm install` + `vite build` in any
reasonable time on every self-update, so that cost stays on a real dev machine instead.

`git config core.hooksPath .githooks` (once) enables a pre-commit hook that rebuilds `web/build/`
automatically and stages it whenever a commit touches `web/src/` or the frontend's dependency
manifests — so it's structurally impossible to commit a stale build. You don't need to remember to
run `pnpm run build` yourself; the hook does it for you.

```bash
cd web
pnpm install
pnpm run dev          # hot-reload dev server, proxies /api and /ws to the Go backend on :8899
pnpm run build        # manual rebuild, if you ever need one outside of committing
```

### Local dev SearXNG (Docker)

```bash
docker run -d --name searxng-dev -p 18888:8080 \
  -v "$(pwd)/dev/searxng/settings.yml:/etc/searxng/settings.yml:ro" \
  searxng/searxng:latest
```

## CLI usage

```bash
polaris search "what's the current stable version of Go?"
polaris search --model deepseek "find a coffee shop near the Space Needle"
polaris stats --days 30    # cost, tool-call counts/error rates, research-loop tuning signals
polaris backup list        # see SETUP.md's Backups
polaris benchmark --dataset browse_comp_test_set.csv --n 20   # run a BrowseComp sample, graded by an LLM judge
```

Every command auto-detects Docker vs. bare-metal from `docker-compose.yml`'s presence, no flag
needed — `search`/`stats`/`update`/`restart`/`backup create`/`backup list` all hit the running
container's own REST API under Docker instead of assuming a local `config.yaml`/git checkout.
`install` and `backup restore` are the exceptions: both explicitly refuse under Docker (there's no
systemd/launchd unit to write; there's no safe way to swap a live database file from the host)
rather than doing something misleading — `docker compose up -d` and the printed restore sequence
are those steps instead.

## Deployment

Bare-metal: runs as a systemd service (Linux) or launchd agent (macOS) via the bundled `procmgr`
package — `Restart=always`, logs rotate daily with 90-day retention. Docker: `restart:
unless-stopped` in `docker-compose.yml` plays the same role. Designed to run on genuinely
resource-constrained hardware (this was built to run on a Le Potato SBC, and runs there via Docker
today — 64MB image, no local Go/Node toolchain needed on-device at all); see
`config.yaml.example` (bare-metal) or `compose/polaris/config.yaml.example` (Docker) for the full
set of tunables.

`GET /healthz` is an unauthenticated liveness check (confirms the process is up and the SQLite
connection is actually reachable) for `Restart=always` or any external uptime monitor to poll.
