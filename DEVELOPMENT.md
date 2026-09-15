# Development

Architecture, frontend development, the CLI, and deployment internals. For install and
configuration, see [SETUP.md](SETUP.md). Agent-specific conventions (Go/SvelteKit build commands,
etc.) live in `CLAUDE.md`.

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
  └── r2       — hand-rolled SigV4 client mirroring backups off-device to Cloudflare R2
```

One binary, no Node.js at runtime — the SvelteKit frontend is built ahead of time (`web/build/`,
`go:embed`) into the Go binary itself.

Runs as a Docker Compose stack, which bundles a SearXNG instance alongside it — see
[SETUP.md](SETUP.md). `polaris update`/`polaris restart` (CLI or settings panel) resolve the
latest image from GHCR and hand off to a host-side watcher (`gateway/docker_update.go`,
`compose/watcher/`) — no in-process git pull/rebuild.

## Frontend development

The Go binary embeds the frontend's built static output (`web/build/`) via `go:embed`. It's not
committed to git — Docker's image build always runs `pnpm run build` fresh from `web/src/` (see
`Dockerfile`'s frontend-build stage), so there's nothing to keep in sync. For bare-metal dev/
testing, build it locally whenever the frontend changes:

```bash
cd web
pnpm install
pnpm run dev          # hot-reload dev server, proxies /api and /ws to the Go backend on :8899
pnpm run build        # produces web/build/ for `go build`/`go run .` to embed
```

### Local dev SearXNG (Docker)

```bash
docker run -d --name searxng-dev -p 18888:8080 \
  -v "$(pwd)/dev/searxng/settings.yml:/etc/searxng/settings.yml:ro" \
  searxng/searxng:latest
```

### Local dev code_exec (Docker sandbox)

`code_exec` needs a reachable Docker daemon and the host-side watcher script running — nothing
about Polaris itself needs to be containerized. Uncomment `config.yaml`'s `code_exec:` block
(see `config.yaml.example`), then run the watcher in a spare terminal alongside `go run .`:

```bash
while true; do ./compose/watcher/codeexec.sh; sleep 1; done
```

The script itself is single-shot (processes at most one pending request, then exits) — in
production `polaris-codeexec.path` (a systemd path unit) re-triggers it the instant a new
request file appears; the loop above is the dev-friendly equivalent. Each iteration is a no-op
if nothing's pending, and shells out to `docker run` against
`ghcr.io/autumnsgrove/polaris-sandbox:latest` (pulled automatically on first use) once a
request does show up.

## CLI usage

```bash
polaris search "what's the current stable version of Go?"
polaris search --model deepseek "find a coffee shop near the Space Needle"
polaris stats --days 30    # cost, tool-call counts/error rates, research-loop tuning signals
polaris backup list        # see SETUP.md's Backups
polaris benchmark --dataset browse_comp_test_set.csv --n 20   # run a BrowseComp sample, graded by an LLM judge
```

`search`/`stats`/`update`/`restart`/`backup create`/`backup list`/`atlas search`/`constellation
backfill` are all thin HTTP clients hitting the running container's own REST API
(`cmd/docker_client.go`) — none of them assume a local `config.yaml`/git checkout. `backup
restore`/`restore-remote` are the exception: there's no safe way to swap a live database file over
HTTP, so those two always run directly against a local config/database path — under Docker that
means `docker compose run --rm --no-deps polaris backup restore ...` (see their `--help`).

## Deployment

Runs as a Docker container with `restart: unless-stopped` in `docker-compose.yml`. Designed to run
on genuinely resource-constrained hardware (this was built to run on a Le Potato SBC — 64MB image,
no local Go/Node toolchain needed on-device at all); see `compose/polaris/config.yaml.example` for
the full set of tunables.

`GET /healthz` is an unauthenticated liveness check (confirms the process is up and the SQLite
connection is actually reachable) for `Restart=always` or any external uptime monitor to poll.
