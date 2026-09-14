# Setup

Everything needed to get Polaris running: requirements, install (one-liner, bare-metal, or
Docker), configuration, self-update, and backups. For architecture, frontend development, and the
CLI, see [DEVELOPMENT.md](DEVELOPMENT.md).

Two ways to run Polaris — pick one and follow that path below:

- **Bare-metal** — a plain Go binary + systemd (Linux) or launchd (macOS) agent.
- **Docker Compose** — a container pulling prebuilt images from GHCR, updated by a host-side
  watcher.

Both are real, supported install paths, not "Docker as a dev convenience."

## Requirements

- Go 1.26+ (bare-metal only — the Docker path builds its own toolchain and needs no local Go)
- A running [SearXNG](https://github.com/searxng/searxng) instance with JSON output enabled
  (disabled by default upstream — see below). Docker's bundled SearXNG already has this on.
- An [OpenRouter](https://openrouter.ai) API key
- Optional: a [Foursquare](https://foursquare.com/developers) Service API Key for structured
  nearby-place search (free tier: 10k calls/month) — without it, `nearby_search` falls back to
  plain web search
- Optional: one [Tavily](https://tavily.com) key covers two *unrelated* products, don't conflate
  them — **Extract** (JS-rendering page fetch: `web_read`'s fallback, after archive.org) and
  **Search** (the last tier of the web-search fallback chain). Free tier: 1,000 credits/month
  total, no card required
- Optional: a [Brave Search API](https://brave.com/search/api/) key, tried first in the web-search
  fallback chain (SearXNG → Brave → Parallel → Tavily Search). **No ongoing free tier** (a
  one-time $5/mo credit, ~1,000 queries), so usage is capped at 1,000/mo in the DB
- Optional: a [Parallel](https://parallel.ai) API key, second in that fallback chain. Free tier:
  5,000 requests/month, but the account has a card on file, so usage is capped in the DB a little
  under that limit
- Optional: a [GitHub personal access token](https://github.com/settings/tokens) so `github_repo`
  can make 5000 requests/hour instead of GitHub's unauthenticated 60/hour cap — works fine with no
  token for occasional lookups
- Required for the `music` tool: a free [Last.fm API key](https://www.last.fm/api/account/create)
  — unlike the tokens above, there's no unauthenticated fallback
- Optional: a free [Hardcover.app](https://hardcover.app) API token (account settings > API) for
  the `books` tool's primary curated-list signal — expires after roughly a year (a personal-account
  JWT, not a stable service key). Without one, `books` degrades to Open Library's shared-subject
  data instead of failing outright
- Required for the `movies` tool: a free [TMDB API key](https://www.themoviedb.org/settings/api) —
  no unauthenticated fallback
- Required for the `youtube_transcript` tool: [yt-dlp](https://github.com/yt-dlp/yt-dlp) on
  `PATH` (Docker installs already have it). Bare-metal: `pip install yt-dlp` (skip the apt/Alpine
  package — it drags in ffmpeg, +276MB, for functionality this tool doesn't use). Without it,
  `youtube_transcript` returns a clear "yt-dlp is not installed" error; every other tool still works
- Optional: a local [Ollama](https://ollama.com) instance serving `nomic-embed-text`, for a
  research-loop signal that catches consecutive `web_search` queries rephrasing the same thing.
  Without it, that one signal is just disabled. Under Docker this needs Ollama rebound beyond
  `127.0.0.1` and a compose route to the host — see `compose/polaris/config.yaml.example`'s
  `ollama` section

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

The one-liner clones the repo, builds the binary, brings up a local SearXNG via Docker (installing
Docker itself if missing), and opens `config.yaml` for you to drop in an OpenRouter key. It does
not start the server.

```bash
curl -fsSL https://raw.githubusercontent.com/AutumnsGrove/Polaris/main/install.sh | bash
```

Once `config.yaml` has a real OpenRouter key, start the server — `install.sh` puts `polaris` on
your PATH:

```bash
polaris run
```

Open `http://localhost:8899`.

For Docker instead (same script, no local Go toolchain needed, sets up the update watcher on
Linux):

```bash
POLARIS_INSTALL_MODE=docker curl -fsSL https://raw.githubusercontent.com/AutumnsGrove/Polaris/main/install.sh | bash
```

Once `.env` has a real OpenRouter key:

```bash
cd ~/Polaris && docker compose up -d
```

Open `http://localhost:8899`.

## Manual install — bare-metal

```bash
git clone https://github.com/AutumnsGrove/Polaris.git
cd Polaris

cp config.yaml.example config.yaml
# edit config.yaml: OpenRouter API key, your SearXNG URL, model choices

git config core.hooksPath .githooks   # once — auto-rebuilds web/build/ on commit
go build -o polaris .
./polaris run
```

Open `http://localhost:8899`. See [DEVELOPMENT.md](DEVELOPMENT.md#frontend-development) if you're
also changing the frontend.

## Manual install — Docker

`docker-compose.yml` bundles Polaris with its own SearXNG instance (JSON output already enabled —
no manual `settings.yml` edit needed). SearXNG's port is published loopback-only by default
(`SEARXNG_HOST=0.0.0.0` in `.env` to widen that, e.g. for direct browser access on a private
tailnet); Polaris always reaches it over the compose network's built-in DNS either way.

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

Open `http://localhost:8899`.

Config is split across two files — `.env` (Docker Compose's own secret-interpolation mechanism)
and `compose/polaris/config.yaml` (the actual app config, same shape as bare-metal's `config.yaml`
but with Docker-appropriate paths/URLs and `${VAR}` placeholders instead of real key values).

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

- **config.yaml** — API keys (OpenRouter, Foursquare, Tavily, Brave, Parallel), the model catalog
  (each entry pins a specific OpenRouter provider for consistent prompt-cache pricing), SearXNG's
  URL, logging, voice model choices. Meant to be hand-edited; changes require a restart. (Docker
  install: split across `.env` and `compose/polaris/config.yaml` instead — see
  [Manual install — Docker](#manual-install--docker).)
- **Settings panel** (gear icon in the sidebar) — theme, default model, price visibility, a manual
  location fallback for `nearby_search`, the update button, and (behind the small info-icon
  button) a usage/tuning stats page — cost, tool-call counts/error rates, research-loop steering
  signals. Changes apply instantly, no restart, no file editing.
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
the browser — the CLI is a thin client to the exact same endpoints those buttons hit. Docker's
version works meaningfully differently under the hood (no git pull, resolves a GHCR image digest
instead) — see [Self-update, under Docker](#self-update-under-docker) above.

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

Backups live in a `backups/` folder next to `polaris.db` itself — already inside the
`polaris-data` named volume under Docker, so no extra bind mount is needed.

### Off-device mirroring to R2

Local backups protect against a bad database state, but not against the device itself failing —
a dead SD card or a bricked SBC takes `backups/` down with it. Setting `r2.*` in `config.yaml`
(see that file's comments, or `.env.example`'s `R2_ACCOUNT_ID`/`R2_ACCESS_KEY_ID`/
`R2_SECRET_ACCESS_KEY` under Docker) mirrors every backup — scheduled or on-demand — to a
dedicated Cloudflare R2 bucket right after it's taken, and prunes R2 to the same retention window.
It's additive: leaving `r2.*` unset disables mirroring entirely and local backups keep working
exactly as before. Use a bucket dedicated to this and a scoped R2 API token (Object Read & Write
on that bucket only) rather than an account-wide key —
[creating one](https://developers.cloudflare.com/r2/api/tokens/).

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
