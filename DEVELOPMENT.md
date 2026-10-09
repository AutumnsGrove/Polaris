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

### Answer rendering: segments, `ui` blocks, streaming mermaid

An answer is not one `{@html}` string. `web/src/lib/uiBlocks/split.ts` cuts it around column-0
` ```ui ` and ` ```mermaid ` fences; `renderAnswer.ts` runs the Markdown pieces through the usual
marked → DOMPurify → citation-chip pipeline (threading one per-URL occurrence counter across pieces
so verification ticks land on the right chip) and `ChatTurnView` renders each piece by kind, keyed by
index so a growing block updates in place. A `ui` fence is one JSON object per line, parsed by
`parse.ts` (total: bad lines become muted raw rows, a trailing partial line is held back) and drawn by
`components/ui/`. `mermaid.ts`'s `mountMermaidStream` re-renders a diagram on each new complete line
and keeps the last good render when a prefix doesn't parse.

Anything that is not the chat renderer must not see the raw JSON: `flatten.ts` (TS) and
`gateway/uiblocks` (Go) turn a block into readable text, and both are tested against
`testdata/ui_flatten.json`. Blocks are taught to live WebSocket chat turns, Pulsar pulses (real threads
in the chat view) and `/api/ask` (`ClientMessage.OffersVisuals`, so the API exercises the real
behaviour; its raw `Answer` keeps the fence and `polaris search` flattens it on print), never to voice
calls or Atlas's plain-text Quick Answer. Oracle's `ui` check follows the same gate. Design and phases:
`docs/plans/intelligent-ui.md`. To watch a stream fill in, run `dev/fakeopenrouter` with `-chunk-delay`.

"Found in source" ticks inside a block use a different rule from prose: a block link is named by where it
sits (`<fence>.<block>.<item>.<field>#<n>`), not by "the nth time this URL is cited". The server lists them
(`gateway/uiblocks/sites.go`, `Sites`), verifies each against its own sentence (`gateway/verification.go`; a
`quote` is exact-match first, Jev only on a miss) and sends marks carrying a `locator`; the components
pass the same address down as a `loc` prop and `renderInlineCitations` ticks the matching link. Adding a
sourced field means touching both `collect()` and the component, and
`TestSites_FieldNamesMatchTheComponents` fails if they disagree.

### Start-screen night sky

`web/src/lib/components/NightSky.svelte` paints the canvas behind the empty-state heading; the logic
lives in `web/src/lib/nightSky/`. Every timing, size, count and spacing dial is in `config.ts`
(seconds between constellations, how many at once, comet gap, how far apart they must sit).
To add a constellation, append an entry to the pool in `constellations.ts` (points plus edge order,
nothing else). Placement (`placement.ts`) and scheduling (`director.ts`) are pure and unit-tested;
`renderer.ts` is the only file that touches the canvas. Design mockup: `mockups/polaris-living-sky.html`.

### One-command dev stack

`dev/stack.sh` starts (or cleanly restarts) the whole bare-metal dev inner loop in one shot —
vite, the Go backend (`go run . run --dev`), the code_exec watcher loop (skipped automatically if
`config.yaml` has no `code_exec:` block), and the local SearXNG container:

```bash
dev/stack.sh            # restart everything (default): stop, then start fresh
dev/stack.sh status     # which pieces are up, on which ports
dev/stack.sh stop
```

Each process is launched via `setsid`, detached from the invoking shell/terminal, so the stack
keeps running even after the shell that launched it exits — logs land in `dev/.stack/*.log`, pids
in `dev/.stack/*.pid`. `stop`/`restart` kill each process's whole group (not just the recorded
pid), so `pnpm run dev`'s real Node child doesn't survive as an orphan holding :45173. The
existing `searxng-dev` container is reused (`docker start`) rather than recreated on every run —
see below for how it's created the first time.

`start`/`restart` refuse to run (before stopping anything) if a process outside the stack — say a
stale `polaris run` from an earlier session — has `polaris.db` open, and `status` flags it too.
Every Polaris process runs the Pulsar scheduler, so a leftover older binary on the same database
silently fires routines with its old code. Pass `--force` to override.

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
polaris stats --days 30    # cost, tool-call counts/error rates, research-loop tuning signals, paid-API monthly cap usage (also `api_caps` in GET /api/stats)
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

The container itself never gets Docker/systemd control — `compose/watcher/`'s host-side scripts
run unprivileged, with one narrow, hash-gated exception for re-syncing their own unit files. See
[SECURITY.md](SECURITY.md) for the full trust model before changing anything under
`compose/watcher/` or `tools/code_exec.go`.
