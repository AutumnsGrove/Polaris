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

- **Web search** via your own SearXNG instance (no API key, no per-query cost) — but SearXNG's
  underlying engines do rate-limit/CAPTCHA a self-hosted instance under real usage, silently
  returning zero results instead of an error. `web_search` detects that (a full outage, not just an
  ordinary empty result) and falls back through Brave → Parallel → Tavily (all optional, in that
  order), before finally saying plainly that search is degraded rather than reporting a false "no
  results" — see [Requirements](#requirements) for what each fallback needs. Every result set is
  tagged `[via <provider>]` so a fallback firing is visible. `polaris search` (the CLI) and Atlas
  (see below) share this same fallback logic
- **Atlas** (`/search`) — a separate, Kagi-style search-results page alongside the chat assistant:
  type a query, browse ranked SearXNG results directly (with your own per-domain
  Block/Lower/Raise/Pin rankings), or end a query with `?` for a fast, sourced answer instead of
  full results. Same fallback chain as `web_search` above, plus its own Brave-specific virtual
  pagination (one real 20-result Brave fetch covers two 10-result Atlas pages)
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
- **GitHub repo stats** — star/fork counts, license, repo age, first/most recent commit dates,
  total commit count, and open issue/PR counts, straight from GitHub's API, plus the README. No API
  key required (an optional token just raises the rate limit)
- **Dictionary** — definitions, part of speech, and an example sentence when available, straight
  from a dictionary source (Wiktionary-backed) instead of general knowledge or a web search —
  precise and citable, with a second independent source as a fallback if the first is down
- **Music recommendations** — real "find me songs/albums like this" grounded in Last.fm's actual
  similarity data, not guesswork. Resolve-then-lookup for a single track (exact-title matching is
  surprisingly brittle otherwise), concurrent fan-out across a whole album's tracklist for
  song-level picks, and album-to-album recommendations derived the same way (Last.fm has no direct
  album-similarity endpoint). Shown as a scrollable carousel of cover-art cards, not just a text
  list. Requires a free [Last.fm API key](https://www.last.fm/api/account/create)
- **Book recommendations** — real "find me books like this" grounded in readers' curated lists
  (Hardcover.app), ranked by likes-per-book density so a small list someone actually curated with
  intent outranks a sprawling generic "best books ever" list with more raw likes but a weaker
  signal. Falls back to Open Library's shared-subject data (no key needed) when Hardcover isn't
  configured, its token has expired, or a book has too little curated-list data to trust alone —
  see [Requirements](#requirements). Same cover-art carousel as music
- **Movie & TV recommendations** — real "find me movies/shows like this" grounded in TMDB's own
  audience-recommendation data ("people who watched this also watched"), not guesswork. Falls back
  to TMDB's genre/keyword-based `similar` data when a title is too new or obscure to have much
  recommendation data of its own. Same cover-art carousel as music/books. Requires a free
  [TMDB API key](https://www.themoviedb.org/settings/api)
- **Nearby places** — real-world search (restaurants, pharmacies, etc.) via Foursquare, with
  distance/category/map links, falling back to a plain web search if Foursquare isn't configured.
  Uses the browser's own geolocation for "near me" questions when it's reachable over HTTPS (a
  plain Tailscale IP isn't — see [Configuration](#configuration)), with a manual location fallback
  in Settings otherwise
- **Voice** — hold a button to record a memo (transcribed via Voxtral), and read any reply aloud
  in a real voice (Kokoro-82M), not the browser's default robotic TTS. Replies are synthesized and
  played back sentence by sentence as they're ready, not as one call that waits for the whole
  answer — audio starts noticeably sooner on a long reply
- **Memory** — remembers durable facts about you and your preferences across threads, not just
  within one conversation: an explicit correction ("keep answers short"), a stated preference meant
  to stick, or a significant project fact gets saved unprompted, and a compact index of everything
  it knows rides along in every turn's context so it's actually used, not just stored. A Memory
  section in Settings shows the full list (view, edit, or forget one directly), plus a
  "what would you like Polaris to remember about you" box that turns a plain-English instruction
  ("forget the one about my old job", "change my timezone to Eastern") into the right edit
- **Clarifying questions** — when a genuinely necessary detail is missing (which city, which of two
  same-titled books), it can ask a single focused question with tappable options instead of guessing
  or interrogating you with several questions at once
- **Retry & edit, with branching** — regenerate a reply or fix a typo and re-run from that point;
  the old version is never deleted, just tucked behind a `‹ 2/3 ›` switcher on the reply, so you can
  browse back to it (or keep going from it) without losing whichever branch you're not looking at
- **Persistent threads** with per-thread and per-turn cost tracking
- **Illustrated sources** — a citation carries a thumbnail (a page's own lead image, a Wikipedia
  article's photo) whenever one's genuinely available, instead of just a bare text chip. arXiv
  citations get a recognizable source badge instead — a real per-paper image doesn't exist to show
- **Settings panel** — dark/light theme, default model, the Memory list above, and a one-click
  "Update Polaris" button that updates and restarts the service — no SSH required (see
  [Self-update](#self-update))
- **Automatic backups** — a daily, consistent snapshot of the database, kept for 30 days and
  pruned automatically, with a CLI to list/create/restore on demand, optionally mirrored
  off-device to Cloudflare R2 (see [Backups](#backups))
- **CLI mode** — `polaris search "..."` answers straight from the terminal, no browser needed
- **Installable** — a web manifest and iOS meta tags let you add Polaris to your phone's homescreen
  as a standalone app (no browser chrome), since [mobile is the primary surface](PRODUCT.md)

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
  │              [Requirements](#requirements)
  ├── voice    — Voxtral (speech-to-text) + Kokoro-82M (text-to-speech), both via OpenRouter
  ├── store    — SQLite: threads, messages, memories, settings, running cost, per-provider API
  │              usage counts
  ├── backup   — daily VACUUM INTO snapshots of the database, rotation, and restore — see Backups
  ├── r2       — hand-rolled SigV4 client mirroring backups off-device to Cloudflare R2 — see Backups
  └── updater  — git pull + rebuild, shared by the CLI and the settings panel's update button
```

One binary, no Node.js at runtime. The SvelteKit frontend is built ahead of time and its static
output is committed to the repo and embedded directly into the Go binary, so the machine running
this only ever needs the Go toolchain — nothing else to install, nothing else to keep running.

Two ways to run it: bare-metal (the binary directly, described above) or Docker Compose, which
bundles a SearXNG instance alongside it — see [Docker install](#docker-install). Both are full
deployments, not a dev-only convenience; either one is a real install choice.

## Why not just use \[existing tool\]?

Perplexica, Morphic, and Open WebUI all do something adjacent, but they're Next.js/Python
platforms with real resource footprints and are built to plug into a paid search API by default.
This is built specifically to sit on top of a self-hosted SearXNG instance, run on genuinely
low-power hardware (a single-board computer, not a server), and stay small enough that "the whole
app" is one file you can scp around if you ever needed to.

## Requirements

- Go 1.26+ (only for the bare-metal install path — the Docker install path builds its own
  toolchain and needs no local Go at all)
- A running [SearXNG](https://github.com/searxng/searxng) instance with JSON output enabled
  (disabled by default upstream — see below)
- An [OpenRouter](https://openrouter.ai) API key
- Optional: a [Foursquare](https://foursquare.com/developers) Service API Key for structured
  nearby-place search (free tier: 10k calls/month) — without it, `nearby_search` falls back to
  plain web search
- Optional: one [Tavily](https://tavily.com) key covers two *unrelated* products, don't conflate
  them — **Extract** (JS-rendering page fetch: `web_read`'s fallback, after archive.org) and
  **Search** (the last tier of the web-search fallback chain below). Free tier: 1,000
  credits/month total, no card required
- Optional: a [Brave Search API](https://brave.com/search/api/) key, tried first in the web-search
  fallback chain (SearXNG → Brave → Parallel → Tavily Search) — real result listings, used by both
  `web_search` and Atlas. **No ongoing free tier** (a one-time $5/mo credit, ~1,000 queries), so
  usage is capped at 1,000/mo in the DB (Brave's own dashboard enforces the same limit too)
- Optional: a [Parallel](https://parallel.ai) API key, second in that same fallback chain. Free
  tier: 5,000 requests/month, but the account has a card on file, so usage is capped in the DB a
  little under that limit rather than trusting the free tier not to bill overage
- Optional: a [GitHub personal access token](https://github.com/settings/tokens) so `github_repo`
  can make 5000 requests/hour instead of GitHub's unauthenticated 60/hour cap — it works fine with
  no token at all for occasional lookups
- Required for the `music` tool: a free [Last.fm API key](https://www.last.fm/api/account/create)
  (self-service signup, no approval wait) — unlike the tokens above, there's no unauthenticated
  fallback, so `music` is unavailable without one
- Optional: a free [Hardcover.app](https://hardcover.app) API token (account settings > API) for
  the `books` tool's primary curated-list signal — expires after roughly a year, since it's a
  personal-account JWT rather than a stable service key, not something you set once and forget.
  Without one (or once it expires), `books` degrades to Open Library's shared-subject data instead
  of failing outright — see the books tool's package doc comment in `tools/books.go` for why that
  fallback exists and how it compares to Hardcover's stronger signal
- Required for the `movies` tool: a free [TMDB API key](https://www.themoviedb.org/settings/api)
  (self-service signup, no approval wait) — like `lastfm`, there's no unauthenticated fallback, so
  `movies` is unavailable without one
- Optional: a local [Ollama](https://ollama.com) instance serving `nomic-embed-text`, for a
  research-loop signal that nudges the model when consecutive `web_search` queries embed as
  near-duplicates of each other — catches a rephrasing loop that a plain "found nothing new" check
  can miss. Without it, that one signal is just disabled; everything else works the same. Under
  Docker, this needs Ollama rebound beyond `127.0.0.1` and a `docker-compose.yml` route to the
  host — see `compose/polaris/config.yaml.example`'s `ollama` section for the full tradeoff before
  turning it on

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

Bare-metal one-liner (macOS/Linux): clones the repo, builds the binary, brings up a local SearXNG
via Docker (installing Docker itself if it's missing), and opens `config.yaml` for you to drop in
an OpenRouter key. Doesn't start the server — that's still on you, once the key's in.

```bash
curl -fsSL https://raw.githubusercontent.com/AutumnsGrove/Polaris/main/install.sh | bash
```

Once `config.yaml` has a real OpenRouter key in it, start the server:

```bash
cd ~/Polaris   # or wherever POLARIS_INSTALL_DIR pointed, if you set it
./polaris run
```

Open `http://localhost:8899`.

Docker one-liner instead — same script, no local Go toolchain needed, and it sets up the update
watcher for you (Linux only; see [Docker install](#docker-install)):

```bash
POLARIS_INSTALL_MODE=docker curl -fsSL https://raw.githubusercontent.com/AutumnsGrove/Polaris/main/install.sh | bash
```

Once `.env` has a real OpenRouter key in it:

```bash
cd ~/Polaris && docker compose up -d
```

Open `http://localhost:8899`.

Or by hand:

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

## Docker install

`docker-compose.yml` bundles Polaris with its own SearXNG instance (JSON output already enabled —
none of the manual `settings.yml` edit above is needed under Docker). SearXNG's port is published
loopback-only by default (`SEARXNG_HOST=0.0.0.0` in `.env` to widen that, e.g. for direct browser
access on a private tailnet); Polaris always reaches it over the compose network's built-in DNS
either way, regardless of that setting.

```bash
git clone https://github.com/AutumnsGrove/Polaris.git
cd Polaris

cp .env.example .env
# generate SEARXNG_SECRET: openssl rand -hex 32
# edit .env: OpenRouter API key, any optional tool keys you want

cp compose/polaris/config.yaml.example compose/polaris/config.yaml
# nothing in here needs editing to get started — see Configuration below

docker compose up -d
```

Open `http://localhost:8899`. (`POLARIS_INSTALL_MODE=docker curl -fsSL .../install.sh | bash` —
see Quick start — does all of the above for you, plus the update watcher below.)

Config is split across two files — `.env` (Docker Compose's own secret-interpolation mechanism) and
`compose/polaris/config.yaml` (the actual app config, same shape as bare-metal's `config.yaml` but
with Docker-appropriate paths/URLs and `${VAR}` placeholders instead of real key values).

### Self-update, under Docker

"Update Polaris"/"Restart Polaris" (settings panel or `polaris update`/`polaris restart` over SSH,
both auto-detecting Docker from `docker-compose.yml`) work differently than bare-metal: no git
pull/rebuild inside the container. Instead Polaris resolves the latest published image's digest
from GHCR (waiting out an in-progress CI build first) and hands off to a host-side systemd watcher
(`compose/watcher/`, installed by `install.sh`'s Docker mode on Linux) that does
`docker compose pull && up --force-recreate` — the container itself never gets control over
Docker. A restart just recreates the currently-running image, no GHCR check. Not wired up on
macOS (no systemd); a Docker install there still runs, the update button just has nothing to
trigger.

Images publish to `ghcr.io/autumnsgrove/polaris` (multi-arch) on every push to `main` via
`.github/workflows/docker-publish.yml`.

## Configuration

Everything behavior-affecting lives in `config.yaml` (gitignored — copy `config.yaml.example`)
or the in-app settings panel:

- **config.yaml** — API keys (OpenRouter, Foursquare, Tavily, Brave, Parallel), the model catalog (each entry pins
  a specific OpenRouter provider for consistent prompt-cache pricing), SearXNG's URL, logging,
  voice model choices. Meant to be hand-edited; changes require a restart. (Docker install: this
  is split across `.env` and `compose/polaris/config.yaml` instead — see
  [Docker install](#docker-install).)
- **Settings panel** (gear icon in the sidebar) — theme, default model, price visibility, a manual
  location fallback for `nearby_search`, the update button, and (behind the small info-icon button)
  a usage/tuning stats page — cost, tool-call counts/error rates, research-loop steering signals.
  Changes apply instantly, no restart, no file editing.
- **prompt.md** — the system prompt, read fresh on every turn. Edit it, see the change on your
  very next message.

Browser geolocation (used automatically for "near me" questions, before falling back to the
manual location above or `config.yaml`'s `default_location`) needs a secure context — it won't
work over Polaris's default plain-HTTP Tailscale IP. [Tailscale
Serve](https://tailscale.com/docs/features/tailscale-serve) (`tailscale serve --bg --https=8899
http://localhost:8899`) gives it a real, tailnet-only HTTPS URL with zero cert management.

## Self-update

No scp'd binaries, no manual redeploy steps — and `polaris update`/`polaris restart` work
correctly over SSH on **either** install method, auto-detected, so there's nothing to remember
about which one a given host is running:

```bash
polaris update    # bare-metal: git pull, rebuild, restart. Docker: resolve+pull+recreate.
polaris restart    # clean restart, no pull/rebuild/GHCR check either way
```

or click **Update Polaris** / **Restart Polaris** in the settings panel to do the same thing from
the browser — the CLI is a thin client to the exact same endpoints those buttons hit, so the two
can never drift apart in behavior. Docker's version works meaningfully differently under the hood
(no git pull, resolves a GHCR image digest instead) — see
[Self-update, under Docker](#self-update-under-docker) above for the full mechanism.

## Backups

The database (threads, messages, cost history, search history, settings — everything in
`polaris.db`) is backed up automatically once a day via SQLite's own `VACUUM INTO` (a consistent
snapshot that doesn't block the server while it runs), kept for 30 days by default, and pruned
automatically past that — no cron job, no host-side timer, just a background goroutine in the
Polaris process itself, so it works identically on both install methods with zero extra setup.
Configurable via `backup.dir`/`backup.retention_days` in `config.yaml` — see that file's comments.

```bash
polaris backup list                # newest first: name, size, timestamp
polaris backup create              # take one right now, outside the daily schedule
polaris backup restore <name>      # replace the live database with a backup — see below
```

`restore` always preserves whatever database was live before overwriting it (copied alongside
itself as `polaris.db.pre-restore-<timestamp>` first — a restore is itself always undoable) and
verifies the backup passes SQLite's integrity check before touching anything. It needs the server
stopped first — swapping the database file out from under a live connection risks corrupting
whichever write is in flight. Bare-metal refuses if it detects Polaris still answering on its
configured port; Docker installs can't be checked the same way (the CLI runs on the host, outside
the container), so `polaris backup restore` prints the exact `docker compose stop` /
`docker compose run` / `docker compose up` sequence to run by hand instead of guessing.

Backups live in a `backups/` folder next to `polaris.db` itself — already inside the `polaris-data`
named volume under Docker, so no extra bind mount is needed.

### Off-device mirroring to R2

Local backups protect against a bad database state, but not against the device itself failing —
a dead SD card or a bricked SBC takes `backups/` down with it. Setting `r2.*` in `config.yaml`
(see that file's comments, or `.env.example`'s `R2_ACCOUNT_ID`/`R2_ACCESS_KEY_ID`/
`R2_SECRET_ACCESS_KEY` under Docker) mirrors every backup — scheduled or on-demand — to a
dedicated Cloudflare R2 bucket right after it's taken, and prunes R2 to the same retention window.
It's additive: leaving `r2.*` unset disables mirroring entirely and local backups keep working
exactly as before. Use a bucket dedicated to this and a scoped R2 API token (Object Read & Write
on that bucket only) rather than an account-wide key —
[creating one](https://developers.cloudflare.com/r2/api/tokens/). The client signs requests with
AWS Signature V4 by hand rather than pulling in `aws-sdk-go-v2`, matching the small hand-rolled
HTTP clients this project already uses for Brave/Parallel/Tavily instead of their SDKs.

```bash
polaris backup list --remote               # what's actually recoverable from R2, not local disk
polaris backup restore-remote <name>       # disaster recovery: download from R2, then restore
```

`restore-remote` is the actual "device failed" path: on a fresh install with `r2.*` already
configured, it downloads the named backup from R2 into `backup.dir` and then runs the exact same
verify/preserve/swap sequence `restore` does. Under Docker it can't run directly from the host CLI
either (same reasoning as plain `restore`), but — unlike plain restore — it *can* run for real
inside the one-off container `polaris backup restore-remote` prints instructions for: that
container shares the same bind-mounted `config.yaml` (so it has the R2 credentials) and the same
data volume as the real service.

## CLI usage

```bash
polaris search "what's the current stable version of Go?"
polaris search --model deepseek "find a coffee shop near the Space Needle"
polaris stats --days 30    # cost, tool-call counts/error rates, research-loop tuning signals
polaris backup list        # see Backups above
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

## License

MIT — see [LICENSE](LICENSE).
