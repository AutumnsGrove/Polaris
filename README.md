# Polaris

A private, self-hosted, search-augmented AI assistant — the "Kagi Assistant" / Perplexity idea,
but pointed at your own [SearXNG](https://github.com/searxng/searxng) instance instead of a paid
search API, running as a single Go binary with the web UI embedded inside it.

You're lost at sea with no way to know the answer yourself. Polaris is the fixed point you
triangulate against — it doesn't know things, it knows how to go find out.

**Get started:** [SETUP.md](SETUP.md) has install instructions (one-liner or manual, both Docker)
and configuration. **Contributing or hacking on it?** See [DEVELOPMENT.md](DEVELOPMENT.md) for
architecture, frontend dev, the CLI, and deployment internals. **Curious about the trust
boundaries** (container isolation, the code_exec sandbox, the update watcher's own privilege
model)? See [SECURITY.md](SECURITY.md).

## What it does

Ask it something. It decides for itself whether it needs to search the web, read a specific page,
look up a nearby place, check the weather, or just answer directly — then streams the answer back
with citations.

**New here and the names (Pulsar, Constellation, Fields...) are unfamiliar?** Tap the **?** button
next to Settings at the bottom of the sidebar — it explains each one in plain English.

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
  for dead links and JS-rendered pages. Can also skip straight to a real, JS-rendering read as a
  deliberate last resort when a plain read looks stale (e.g. a live-updating page), within Tavily's
  own monthly cap.
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
  aloud in a real voice (Kokoro-82M), streamed sentence by sentence as they're ready. Pick from
  four voices (American/British, female/male) in Settings.
- **Transponder** — a full-screen, hands-free call mode: push-to-talk to speak, get a spoken reply
  back (with sources still visible on screen), and keep going without touching the keyboard.
- **Code execution** — runs model-written Python in a locked-down, network-less Docker sandbox
  (numpy/pandas/matplotlib/scipy/scikit-learn preinstalled) for real calculations, data analysis,
  file processing, and charts styled to match the app's own theme, and can pull in a URL you've
  already been shown as a citation to work with real data. It's also how any generated document
  (a report, a CSV export, anything file-shaped) gets created — there's no separate file-writing
  tool.
- **Artifacts** — anything the model writes to its workspace can be surfaced inline: an image (a
  generated chart, a fetched photo) renders large and inline; anything else (a report, a data
  export) renders as a tappable card that opens a viewer with a rendered/raw toggle, copy, and
  download.
- **Calculator** — evaluates arithmetic exactly (ratios, percentages, date-interval math) instead
  of doing the math in free text, removing a whole class of LLM arithmetic mistakes.
- **Current time** — the assistant always knows today's date and checks the exact time of day
  only when an answer depends on it.
- **Image search & viewing** — finds real photos for a query, looks at the candidates itself (a
  description, or directly if the model is multimodal), and shows you only the good ones — as a
  gallery or cards — instead of dumping every result.
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
- **Oracle mode** (opt-in) — reads each message first and quietly adjusts how it's answered
  (extra care with sources on a health question, a table for a comparison, fresher sources for
  breaking news), with every change shown and undoable right on the reply. It can also offer to
  turn a broad topic into an interactive Safari. Off by default in ghost conversations, with a
  switch to opt those in too (still without the offer to set up a Pulsar, add to Daily, or file
  the chat into a Field). See [docs/oracle.md](docs/oracle.md) for every check and what it does.
- **Night-sky start screen** — twinkling stars, a slow comet, and constellations that draw
  themselves in at random clear spots and fade back out behind "Ask Polaris anything."
- **Retry & edit, with branching** — regenerate a reply or fix a typo and re-run from that point;
  old versions stay reachable behind a `‹ 2/3 ›` switcher on the reply.
- **Persistent threads** with per-thread and per-turn cost tracking.
- **Follow-ups that remember the research** — later turns see the sources behind earlier answers;
  an opt-in setting also replays every earlier search result and page read, so follow-ups don't
  re-search (costs more per turn, and raises the auto-compaction limit to 200K tokens).
- **Ghost threads** — an incognito mode, hidden from the sidebar/search and without memory/chat-
  search tool access, that's deleted the moment it ends unless you promote it into a regular
  thread. Its real cost still counts toward your regular usage totals, so spend never goes
  unaccounted for. Available over the WebSocket chat client and `POST /api/ask`/`/api/ask/stream`.
- **Fields** (`/fields`) — a named group of conversations that share custom instructions (a "help
  me write this" interview can draft them for you) and a pool of reference files, plus per-Field defaults for model, focus mode, and memory, and opt-outs
  from Constellation and chat search. A Field's memory can be global (as usual), its own private
  store, both (its own plus your regular ones, read-only), or off. Shared files are read-only to every conversation in the
  Field (an edit becomes a private copy); pin a Field to keep it in the sidebar, or file a conversation under one from the composer's More menu. Shared files
  need the `code_exec` sandbox's workspace configured.
- **Illustrated sources** — citations carry a thumbnail when one's genuinely available, instead
  of a bare text chip.
- **Help (?) button** — next to Settings in the sidebar; a plain-English glossary of the app's
  named features (Fields = projects, Pulsar = routine searches, and so on).
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
cheaply on an interval, never live-per-message. Its settings panel takes an optional name and
pronouns so Weaver has real guidance instead of guessing when writing personal stars about you.

---

## Why not just use \[existing tool\]?

Perplexica, Morphic, and Open WebUI all do something adjacent, but they're Next.js/Python
platforms with real resource footprints and are built to plug into a paid search API by default.
This is built specifically to sit on top of a self-hosted SearXNG instance, run on genuinely
low-power hardware (a single-board computer, not a server), and stay small enough that "the whole
app" is one file you can scp around if you ever needed to.

## License

MIT — see [LICENSE](LICENSE).
