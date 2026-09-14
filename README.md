# Polaris

A private, self-hosted, search-augmented AI assistant — the "Kagi Assistant" / Perplexity idea,
but pointed at your own [SearXNG](https://github.com/searxng/searxng) instance instead of a paid
search API, running as a single Go binary with the web UI embedded inside it.

You're lost at sea with no way to know the answer yourself. Polaris is the fixed point you
triangulate against — it doesn't know things, it knows how to go find out.

**Get started:** [SETUP.md](SETUP.md) has install instructions (one-liner, bare-metal, or Docker)
and configuration. **Contributing or hacking on it?** See [DEVELOPMENT.md](DEVELOPMENT.md) for
architecture, frontend dev, the CLI, and deployment internals.

## What it does

Ask it something. It decides for itself whether it needs to search the web, read a specific page,
look up a nearby place, check the weather, or just answer directly — then streams the answer back
with citations.

## Features

- **Web search** via your own SearXNG instance — no API key, no per-query cost. Falls back through
  Brave → Parallel → Tavily (all optional) if SearXNG's engines are rate-limited or down, tagging
  results `[via <provider>]` so a fallback firing is visible. Shared by `web_search`,
  `polaris search`, and Atlas — see [SETUP.md](SETUP.md#requirements) for what each fallback needs.
- **Atlas** (`/search`) — a Kagi-style search-results page alongside the chat assistant: browse
  ranked SearXNG results with your own per-domain Block/Lower/Raise/Pin rankings, or end a query
  with `?` for a fast sourced answer instead of full results. Same fallback chain as web search.
- **Page reading** — fetches a URL and extracts clean text, optionally focused by an instruction
  ("just the prices"). Handles PDFs directly; falls back to archive.org, then Tavily's Extract API,
  for dead links and JS-rendered pages.
- **YouTube transcripts** — reads a video's captions straight from its watch page via `yt-dlp`, so
  a shared link is as researchable as any article.
- **Weather** — current conditions and a short forecast via Open-Meteo, no API key.
- **Wikipedia / arXiv lookup** — an encyclopedia summary or paper abstract pulled directly from
  source, for a cleaner citation than a general web search.
- **GitHub repo stats** — stars, forks, license, commit history, and open issue/PR counts from
  GitHub's API, plus the README. No token required (one just raises the rate limit).
- **GitHub activity** — recent releases and their notes, a specific pull request's detail, recent
  issue activity, or commits since a date, alongside the repo-stats lookup above.
- **Dictionary** — definitions, part of speech, and an example sentence from Wiktionary, with a
  second source as fallback.
- **Music recommendations** — "find me songs/albums like this" grounded in Last.fm's similarity
  data, shown as a cover-art carousel. Requires a free Last.fm API key.
- **Book recommendations** — grounded in Hardcover.app's curated reader lists, falling back to
  Open Library's shared-subject data when Hardcover isn't configured. Same carousel as music.
- **Movie & TV recommendations** — grounded in TMDB's audience-recommendation data. Requires a
  free TMDB API key.
- **Nearby places** — restaurant/pharmacy/etc. search via Foursquare, with distance/category/map
  links, falling back to a plain web search if Foursquare isn't configured. Uses browser
  geolocation for "near me" questions when available (see [SETUP.md](SETUP.md#configuration)).
- **Voice** — hold a button to record a memo (transcribed via Voxtral), and hear replies read
  aloud in a real voice (Kokoro-82M), streamed sentence by sentence as they're ready.
- **Code execution** — runs model-written Python in a locked-down, network-less Docker sandbox
  (numpy/pandas/matplotlib/scipy/scikit-learn preinstalled) for real calculations, data analysis,
  and file processing, and can pull in a URL you've already been shown as a citation to work with
  real data. Docker installs only — see [SETUP.md](SETUP.md#requirements).
- **Calculator** — evaluates arithmetic exactly (ratios, percentages, date-interval math) instead
  of doing the math in free text, removing a whole class of LLM arithmetic mistakes.
- **Charts** — renders data it's synthesized (search results, calculations) as a line/bar/timeline/
  meter chart instead of a wall of prose or a table.
- **Image search & viewing** — finds real photos for a query as a gallery, and can look directly
  at a specific image (a result, or a chart it just generated) when a text description isn't enough.
- **Highlight cards** — turns a handful of items actually found this turn (products, places,
  repos) into cards instead of a paragraph.
- **PDF paging** — search or page through an attached PDF beyond its initial preview, with an
  optional instruction to extract just what's needed from a page.
- **Search your own chats** — finds and rereads a past conversation ("what did we decide about
  X") instead of starting over from scratch.
- **Memory** — remembers durable facts and preferences across threads, saved unprompted from an
  explicit correction or stated preference. The Settings Memory panel lists, edits, or forgets
  anything, including via a plain-English instruction ("forget the one about my old job").
- **Clarifying questions** — asks a single focused question with tappable options when a
  genuinely necessary detail is missing, instead of guessing or interrogating you at once.
- **Retry & edit, with branching** — regenerate a reply or fix a typo and re-run from that point;
  old versions stay reachable behind a `‹ 2/3 ›` switcher on the reply.
- **Persistent threads** with per-thread and per-turn cost tracking.
- **Illustrated sources** — citations carry a thumbnail when one's genuinely available, instead
  of a bare text chip.
- **Settings panel** — theme, default model, the Memory list above, and a one-click
  Update/Restart button — see [Self-update](SETUP.md#self-update).
- **Automatic backups** — a daily, pruned-automatically database snapshot, with a CLI to
  list/create/restore, optionally mirrored to Cloudflare R2 — see [Backups](SETUP.md#backups).
- **CLI mode** — `polaris search "..."` answers straight from the terminal, no browser needed.
- **Installable** — a web manifest and iOS meta tags let you add Polaris to your phone's
  homescreen as a standalone app, since [mobile is the primary surface](PRODUCT.md).

## Beyond chat: Pulsar, Pulsar Daily, and Constellation

Three background systems that go beyond "ask a question, get an answer" — each runs on its own
schedule and turns into something you read rather than something you type into.

**Pulsar** (`/pulsar`) — a saved prompt that fires on a schedule (daily/weekly/monthly) instead of
on demand, running through the same turn pipeline as a normal message. Each firing reports only
what's new since last time, not a repeat of settled facts. A "help me write this" wizard turns a
vague idea into a tuned prompt through a short interview. Catches up on any fire it missed while
Polaris was offline.

**Pulsar Daily** (`/daily`) — one daily "morning newspaper" edition: a week's weather range, word
of the day, on-this-day, a quote, picture of the day, headlines, trending/local/sports news, plus
any custom blocks you define. Each section generates independently, and a second pass drops
anything unchanged since yesterday so a quiet news day makes a shorter page. A Top Story gets
deeper elaboration. Generates on schedule or on demand via Settings.

**Constellation** (`/constellation`) — an auto-generated personal library, not a chat feature: a
background agent ("Weaver") reads your recent threads and extracts durable facts about *you* into
short, evergreen "stars," collapsing repeated mentions into one growing card instead of scattered
notes. Browsable by category, plus a star-map view of how stars connect to each other. Runs
cheaply on an interval, never live-per-message.

---

## Why not just use \[existing tool\]?

Perplexica, Morphic, and Open WebUI all do something adjacent, but they're Next.js/Python
platforms with real resource footprints and are built to plug into a paid search API by default.
This is built specifically to sit on top of a self-hosted SearXNG instance, run on genuinely
low-power hardware (a single-board computer, not a server), and stay small enough that "the whole
app" is one file you can scp around if you ever needed to.

## License

MIT — see [LICENSE](LICENSE).
