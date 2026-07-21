# Polaris

A private, self-hosted, search-augmented AI assistant — the "Kagi Assistant" / Perplexity idea,
but pointed at your own [SearXNG](https://github.com/searxng/searxng) instance instead of a paid
search API, running as a single Go binary with the web UI embedded inside it.

You're lost at sea with no way to know the answer yourself. Polaris is the fixed point you
triangulate against — it doesn't know things, it knows how to go find out.

## What it does

Ask it something. It decides for itself whether it needs to search the web, read a specific page,
look up a nearby place, or just answer directly — then streams the answer back with citations.

- **Web search** via your own SearXNG instance (no API key, no per-query cost, no rate limits)
- **Page reading** — fetches a URL and extracts clean text for free; optionally give it an
  instruction ("just the prices") and it runs a small second LLM pass to pull out only that
- **Nearby places** — real-world search (restaurants, pharmacies, etc.) via Foursquare, with
  distance/category/map links, falling back to a plain web search if Foursquare isn't configured
- **Voice** — hold a button to record a memo (transcribed via Voxtral), and read any reply aloud
  in a real voice (Kokoro-82M), not the browser's default robotic TTS
- **Retry & edit** — redo a failed turn, or fix a typo and re-run from that point, without losing
  the rest of the thread
- **Persistent threads** with per-thread and per-turn cost tracking, visible or hideable
- **Settings panel** — dark/light theme, default model, price visibility, and a one-click
  "push update now" button that pulls, rebuilds, and restarts the service — no SSH required
- **CLI mode** — `polaris search "..."` answers straight from the terminal, no browser needed

## Architecture

```
Browser (SvelteKit SPA, embedded in the Go binary via go:embed)
  ↕ WebSocket (/ws) + REST (/api/*)
Go backend
  ├── agent    — tool-use loop: think / web_search / web_read / nearby_search, or just answer
  ├── llm      — OpenRouter client, provider-pinned per model for consistent prompt-cache pricing
  ├── search   — SearXNG client
  ├── places   — Foursquare + Nominatim geocoding
  ├── voice    — Voxtral (speech-to-text) + Kokoro-82M (text-to-speech), both via OpenRouter
  ├── store    — SQLite: threads, messages, settings, running cost
  └── updater  — git pull + rebuild, shared by the CLI and the settings panel's update button
```

One binary, no Node.js at runtime. The SvelteKit frontend is built ahead of time and its static
output is committed to the repo and embedded directly into the Go binary, so the machine running
this only ever needs the Go toolchain — nothing else to install, nothing else to keep running.

## Why not just use \[existing tool\]?

Perplexica, Morphic, and Open WebUI all do something adjacent, but they're Next.js/Python
platforms with real resource footprints and are built to plug into a paid search API by default.
This is built specifically to sit on top of a self-hosted SearXNG instance, run on genuinely
low-power hardware (a single-board computer, not a server), and stay small enough that "the whole
app" is one file you can scp around if you ever needed to.

## Requirements

- Go 1.24+
- A running [SearXNG](https://github.com/searxng/searxng) instance with JSON output enabled
  (disabled by default upstream — see below)
- An [OpenRouter](https://openrouter.ai) API key
- Optional: a [Foursquare](https://foursquare.com/developers) Service API Key for structured
  nearby-place search (free tier: 10k calls/month) — without it, `nearby_search` falls back to
  plain web search

### SearXNG's JSON API

SearXNG disables its JSON output by default as an anti-scraping measure. Add this to your
instance's `settings.yml`:

```yaml
search:
  formats:
    - html
    - json
```

## Quick start

```bash
git clone https://github.com/AutumnsGrove/Polaris.git
cd Polaris

cp config.yaml.example config.yaml
# edit config.yaml: OpenRouter API key, your SearXNG URL, model choices

cd web && pnpm install && pnpm run build && cd ..
go build -o polaris .
./polaris run
```

Open `http://localhost:8899`.

### Local dev SearXNG (Docker)

```bash
docker run -d --name searxng-dev -p 18888:8080 \
  -v "$(pwd)/dev/searxng/settings.yml:/etc/searxng/settings.yml:ro" \
  searxng/searxng:latest
```

### Frontend development

The Go binary embeds the frontend's built static output (`web/build/`) via `go:embed` — it's a
build artifact, not committed to the repo (Vite's output isn't byte-reproducible across runs, so
committing it just churns the tree). Both a fresh clone and the potato's self-update flow need
`pnpm run build` to run before `go build`.

```bash
cd web
pnpm install
pnpm run dev          # hot-reload dev server, proxies /api and /ws to the Go backend on :8899
pnpm run build        # rebuild the static output that go:embed picks up
```

## Configuration

Everything behavior-affecting lives in `config.yaml` (gitignored — copy `config.yaml.example`)
or the in-app settings panel:

- **config.yaml** — API keys, the model catalog (each entry pins a specific OpenRouter provider
  for consistent prompt-cache pricing), SearXNG/Foursquare URLs, logging, voice model choices.
  Meant to be hand-edited; changes require a restart.
- **Settings panel** (gear icon in the sidebar) — theme, default model, price visibility, and
  the update button. Changes apply instantly, no restart, no file editing.
- **prompt.md** — the system prompt, read fresh on every turn. Edit it, see the change on your
  very next message.

## Self-update

No scp'd binaries, no manual redeploy steps:

```bash
polaris update    # git pull, rebuild, restart — from the CLI over SSH
```

or click **Push update now** in the settings panel to do the same thing from the browser.

## CLI usage

```bash
polaris search "what's the current stable version of Go?"
polaris search --model deepseek "find a coffee shop near the Space Needle"
```

## Deployment

Runs as a systemd service (Linux) or launchd agent (macOS) via the bundled `procmgr` package —
`Restart=always`, logs rotate daily with 90-day retention. Designed to run on genuinely
resource-constrained hardware (this was built to run on a Le Potato SBC); see
`config.yaml.example` for the full set of tunables.

## License

MIT — see [LICENSE](LICENSE).
