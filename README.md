# Polaris

A private, self-hosted, search-augmented AI assistant — the "Kagi Assistant" / Perplexity idea,
but pointed at your own [SearXNG](https://github.com/searxng/searxng) instance instead of a paid
search API, running as a single Go binary with the web UI embedded inside it.

You're lost at sea with no way to know the answer yourself. Polaris is the fixed point you
triangulate against — it doesn't know things, it knows how to go find out.

<p align="center"><img src="docs/screenshots/chat-desktop-split.webp" alt="Polaris answering a question with visible searches and citations, shown in dark and light themes" width="860"></p>

Built phone-first, and it fills out a desktop window too. **[More screenshots →](docs/SHOWCASE.md)**

**Get started:** [SETUP.md](SETUP.md) has install instructions (one-liner or manual, both Docker)
and configuration. **Contributing or hacking on it?** See [DEVELOPMENT.md](DEVELOPMENT.md) for
architecture, frontend dev, the CLI, and deployment internals. **Curious about the trust
boundaries** (container isolation, the code_exec sandbox, the update watcher's own privilege
model)? See [SECURITY.md](SECURITY.md).

## What it does

Ask it something. It decides for itself whether it needs to search the web, read a specific page,
look up a nearby place, check the weather, or just answer directly — then streams the answer back
with citations, showing every search and page read along the way.

**New here and the names (Pulsar, Constellation, Fields...) are unfamiliar?** Tap the **?** button
next to Settings at the bottom of the sidebar — it explains each one in plain English.

<table>
<tr>
<td width="25%"><img src="docs/screenshots/start-phone.webp" alt="The night-sky start screen"></td>
<td width="25%"><img src="docs/screenshots/recommendations-phone.webp" alt="Book recommendations as a cover carousel"></td>
<td width="25%"><img src="docs/screenshots/code-exec-phone.webp" alt="A chart and a CSV file from the code sandbox"></td>
<td width="25%"><img src="docs/screenshots/constellation-phone.webp" alt="The Constellation library of learned facts"></td>
</tr>
</table>

## Highlights

- **Sourced answers** from your own SearXNG instance — no API key, no per-query cost — with optional
  paid fallbacks if its engines get rate-limited. Reads pages and PDFs, and YouTube transcripts.
- **Results, not paragraphs:** cover-art carousels for music, books, and film; weather range cards;
  image galleries; charts and downloadable files from a locked-down code sandbox.
- **A dozen built-in lookups:** weather, Wikipedia, arXiv, GitHub, a dictionary, nearby places, a
  calculator, and more.
- **[Atlas](docs/SHOWCASE.md#atlas-a-search-results-page-when-youd-rather-read-the-results-yourself)** —
  a Kagi-style results page, with your own per-domain Block / Lower / Raise / Pin rankings.
- **Voice:** record a memo, hear replies read aloud, or go hands-free in full-screen **Transponder** mode.
- **Memory you can see and edit**, plus **Fields** (folders of conversations that share instructions
  and files).
- **[Oracle mode](docs/oracle.md)** (opt-in) reads each question first and quietly adjusts how it's
  answered, showing — and letting you undo — every change.
- **Pulsar** runs a saved question on a schedule and reports only what's new. **The Daily** assembles a
  morning newspaper. **Constellation** keeps a library of durable facts learned from your chats.
- **Ghost threads** (incognito), retry and edit with branching, per-turn cost tracking, automatic
  daily backups, and a `polaris search` CLI.
- **Installable** as a standalone app on your phone's homescreen.

The complete list, with how each piece works, is in **[docs/FEATURES.md](docs/FEATURES.md)**.

---

## Why not just use \[existing tool\]?

Perplexica, Morphic, and Open WebUI all do something adjacent, but they're Next.js/Python
platforms with real resource footprints and are built to plug into a paid search API by default.
This is built specifically to sit on top of a self-hosted SearXNG instance, run on genuinely
low-power hardware (a single-board computer, not a server), and stay small enough that "the whole
app" is one file you can scp around if you ever needed to.

## License

MIT — see [LICENSE](LICENSE).
