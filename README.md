# Polaris

A private, self-hosted, search-augmented AI assistant — the "Kagi Assistant" / Perplexity idea,
but pointed at your own [SearXNG](https://github.com/searxng/searxng) instance instead of a paid
search API, running as a single Go binary with the web UI embedded inside it.

You're lost at sea with no way to know the answer yourself. Polaris is the fixed point you
triangulate against — it doesn't know things, it knows how to go find out.

## What it does

Ask it something. It decides for itself whether it needs to search the web, read a specific page,
look up a nearby place, check the weather, or just answer directly — then streams the answer back
with citations.

- **Web search** via your own SearXNG instance (no API key, no per-query cost, no rate limits)
- **Page reading** — fetches a URL and extracts clean text for free; optionally give it an
  instruction ("just the prices") and it runs a small second LLM pass to pull out only that.
  Handles PDFs directly (no extra setup), and falls back to archive.org for dead links/paywalls,
  then to Tavily's Extract API (optional, paid) for JS-rendered pages the free path can't see
- **YouTube transcripts** — reads a video's captions directly from its watch page (no API key, no
  headless browser) so a shared YouTube link is as researchable as any article
- **Weather** — current conditions and a short forecast via Open-Meteo (no API key), geocoded the
  same way as `nearby_search`
- **Wikipedia / arXiv lookup** — queries an encyclopedia summary or a paper's abstract directly from
  its source instead of through a general web search, for a cleaner, more precise citation
- **Nearby places** — real-world search (restaurants, pharmacies, etc.) via Foursquare, with
  distance/category/map links, falling back to a plain web search if Foursquare isn't configured.
  Uses the browser's own geolocation for "near me" questions when it's reachable over HTTPS (a
  plain Tailscale IP isn't — see [Configuration](#configuration)), with a manual location fallback
  in Settings otherwise
- **Voice** — hold a button to record a memo (transcribed via Voxtral), and read any reply aloud
  in a real voice (Kokoro-82M), not the browser's default robotic TTS. Replies are synthesized and
  played back sentence by sentence as they're ready, not as one call that waits for the whole
  answer — audio starts noticeably sooner on a long reply
- **Retry & edit** — redo a failed turn, or fix a typo and re-run from that point, without losing
  the rest of the thread
- **Persistent threads** with per-thread and per-turn cost tracking, visible or hideable
- **Settings panel** — dark/light theme, default model, price visibility, and a one-click
  "push update now" button that pulls, rebuilds, and restarts the service — no SSH required
- **CLI mode** — `polaris search "..."` answers straight from the terminal, no browser needed
- **Installable** — a web manifest and iOS meta tags let you add Polaris to your phone's homescreen
  as a standalone app (no browser chrome), since [mobile is the primary surface](PRODUCT.md)

## Architecture

```
Browser (SvelteKit SPA, embedded in the Go binary via go:embed)
  ↕ WebSocket (/ws) + REST (/api/*)
Go backend
  ├── agent    — tool-use loop: think / web_search / web_read / nearby_search / youtube_transcript /
  │              weather / reference_lookup, or just answer — independent tool calls in the same
  │              turn run concurrently
  ├── llm      — OpenRouter client, provider-pinned per model for consistent prompt-cache pricing
  ├── search   — SearXNG client
  ├── places   — Foursquare + Nominatim geocoding
  ├── tavily   — Extract API client, web_read's paid fallback for JS-rendered pages
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
- Optional: a [Tavily](https://tavily.com) API key so `web_read` can fall back to Tavily's Extract
  API (which actually renders JS) for JS-rendered pages the free goquery-based fetch can't see —
  without it, those pages just fail after the free archive.org fallback also comes up empty

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

git config core.hooksPath .githooks   # once — auto-rebuilds web/build/ on commit, see below
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

The Go binary embeds the frontend's built static output (`web/build/`), which is committed to
this repo — the potato is a Le Potato SBC, too weak to run `pnpm install` + `vite build` in any
reasonable time on every self-update, so that cost stays on a real dev machine instead.

`git config core.hooksPath .githooks` (once, see Quick start) enables a pre-commit hook that
rebuilds `web/build/` automatically and stages it whenever a commit touches `web/src/` or the
frontend's dependency manifests — so it's structurally impossible to commit a stale build. You
don't need to remember to run `pnpm run build` yourself; the hook does it for you.

```bash
cd web
pnpm install
pnpm run dev          # hot-reload dev server, proxies /api and /ws to the Go backend on :8899
pnpm run build        # manual rebuild, if you ever need one outside of committing
```

## Configuration

Everything behavior-affecting lives in `config.yaml` (gitignored — copy `config.yaml.example`)
or the in-app settings panel:

- **config.yaml** — API keys (OpenRouter, Foursquare, Tavily), the model catalog (each entry pins
  a specific OpenRouter provider for consistent prompt-cache pricing), SearXNG's URL, logging,
  voice model choices. Meant to be hand-edited; changes require a restart.
- **Settings panel** (gear icon in the sidebar) — theme, default model, price visibility, a manual
  location fallback for `nearby_search`, and the update button. Changes apply instantly, no
  restart, no file editing.
- **prompt.md** — the system prompt, read fresh on every turn. Edit it, see the change on your
  very next message.

Browser geolocation (used automatically for "near me" questions, before falling back to the
manual location above or `config.yaml`'s `default_location`) needs a secure context — it won't
work over Polaris's default plain-HTTP Tailscale IP. [Tailscale
Serve](https://tailscale.com/docs/features/tailscale-serve) (`tailscale serve --bg --https=8899
http://localhost:8899`) gives it a real, tailnet-only HTTPS URL with zero cert management.

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

`GET /healthz` is an unauthenticated liveness check (confirms the process is up and the SQLite
connection is actually reachable) for `Restart=always` or any external uptime monitor to poll.

## License

MIT — see [LICENSE](LICENSE).
