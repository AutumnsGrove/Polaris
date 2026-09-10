# Highlight tool expansions — v1 plan

Follow-up to #49 ("Expand highlight tool to more callers") and to `docs/plans/shopping-mode.md`,
which built `highlight` deliberately domain-agnostic and left a list of candidate callers
undesigned. This plan picks up four of them (media spotlighting, GitHub repos, image curation) plus
one unrelated model-roster addition bundled in at the same time. Places (Foursquare) get the same
treatment as media; a Researcher-mode source grid is still deferred, same as shopping-mode.md left
it. A fifth initiative, added after live-spiking a couple of real pages, gives `web_read` best-effort
schema.org/JSON-LD extraction — recipes and job postings are its first two callers, sharing the same
small infrastructure.

Ideation notes below record the forks actually considered and which way each one went, since two of
the four initiatives ended up *not* touching `highlight.go` at all — worth keeping visible so the
"why" isn't lost the next time this file is read.

## 1. Spotlighting a favorite from books/music/movies/places — no `highlight` schema change

**The fork:** should `highlight` grow a `type` field (`book`/`movie`/`tv`/`music`) that live-resolves
a title via the same resolver functions `books.go`/`movies.go`/`music.go` already have
(`resolveHardcoverBook`, `resolveTMDBTitle`, `fetchDeezerCoverArt`, ...), or should the recommendation
tools' own text output just start including what `highlight` already needs?

**Decided: the second one.** Checked live: `formatBooksResult`/`formatMoviesResult` (and music's
equivalents) only ever put title/author/description/genre in the text the model reads back — never
the URL or cover art, even though `addBookCards`/movies'/music's own Card population already has both.
That's *why* the model can't call `highlight` to spotlight a pick today — it has literally never read
a url or image_url for any recommended item, so `highlight`'s "must come from something you read this
turn" invariant blocks it outright, correctly.

Fix: extend each per-item line in `formatBooksResult`, `formatMoviesResult`, and music's per-track/
per-album formatters (`tools/books.go`, `tools/movies.go`, `tools/music.go`) to also print that item's
URL and cover/poster URL — the exact same values already going into `addBookCards`/`Card.ImageURL`
elsewhere in the same function, just also surfaced as text. `nearby_search` (`tools/nearby_search.go`)
gets the identical treatment — Foursquare already returns a photo and a Maps link per place, just not
echoed into the result text; `price` on the eventual `highlight` call carries distance or a `$$` range
instead of a dollar amount, same "it's just free text" property that made this work for shopping.

`highlight.go` itself needs zero changes. The only other piece is a one-line addition to each of
`books.yaml`/`movies.yaml`/`music.yaml`/`nearby_search.yaml`'s `api_description`: "each item's url and
cover/photo are included above — if asked to narrow this down to a favorite or a top few, call
`highlight` with those, not by describing them again in prose." No new focus mode needed — this is
general "the user asked me to pick" behavior, not commercial-intent steering the way Shopper mode is.

Rejected the live-resolve `type` field because it duplicates four resolver call paths inside a tool
whose entire design point was *not fetching anything*, and reopens "was this actually verified" for a
tool that currently can't lie by construction (it only ever renders what's already been read). If a
real case shows up where the model wants to highlight something it never ran through
`books`/`music`/`movies`/`nearby_search` in the first place, that's worth a fresh design conversation,
not a default assumption now.

## 2. GitHub repos — a carousel via the existing Card mechanism, not `highlight`

**The fork:** the issue's own fallback (`price: "★ 4.2k"` via `highlight`) works today with zero code
changes. The richer ask — stats, a GitHub-style language-composition bar — needs structured data
`highlight`'s generic `{title,url,price,image_url}` shape can't carry (an array of per-language
percentages), which would mean either bloating `highlight`'s schema for one caller or building a
second bespoke tool.

**Real problem raised in discussion: comparing several repos in one turn would flood the chat with
individual stat blocks unless they're grouped.** That reframes this entirely — `github_repo` should
behave like `books`/`music`/`movies` already do: call `ctx.AddCard` itself, directly, no `highlight`
involved. `ChatTurnView.svelte`'s card partition already buckets everything that isn't `kind ===
'image'` or `kind === 'highlight'` into the existing `RecommendationsCarousel` — a bare default
`Kind: ""` Card gets the carousel for free, with zero new frontend component and zero new Card field.
Comparing three repos in one turn means three `github_repo` calls, three Cards, one carousel — same
shape as asking for three book recommendations.

Scope for this pass, per discussion — **the star-badge version, carouseled, language bar deferred**:

- `githubRepoInfo` (`tools/github_repo.go`) gains an `Owner struct { AvatarURL string
  \`json:"avatar_url"\` } \`json:"owner"\`` field — already in GitHub's REST response, just not parsed
  today.
- `githubRepoStats` gains `AvatarURL string`, populated in `fetchGitHubRepoStats`.
- `handleGitHubRepo` calls `ctx.AddCard(Card{Title: stats.FullName, Subtitle: <short badge, e.g.
  "★ 4.2k · Go · MIT">, ImageURL: stats.AvatarURL, URL: stats.HTMLURL})` right alongside the existing
  `ctx.AddCitation` call — `Kind` left at its default `""`, so it lands in the same carousel as any
  media cards from the same turn (existing dedupe-by-URL applies for free on a repeat lookup).
- The full stats block (stars/forks/commits/issues/PRs/README) still goes back as the tool's text
  result, unchanged — the card is a compact visual summary alongside it, not a replacement.

Language-bar `RepoCard.svelte` with `/repos/{owner}/{repo}/languages` data stays a real, deferred v2 —
listed under "Out of scope" below, not designed here.

## 3. Image curation — `view_image` as a real tool, gated to multimodal models

Today's `describe_image` pipeline (`gateway/attachments.go`'s `resolveAttachment`, image branch) is
already most of the way to a tool: it picks a vision-capable model (the thread's own model if it's
multimodal, else `cfg.MultimodalModel()`), and it already synthesizes `tool_call`/`tool_result` events
named `describe_image` purely so the frontend has something to show during the wait — it just isn't
registered as something the model can *invoke*, only something that runs once, automatically, before
`agent.Run` starts.

**Important scoping note, worth being explicit about:** this is not "the multimodal model looks
directly at image_search's thumbnails in its own context." `llm.ChatMessage.Content` is a plain string
everywhere in the normal tool-use loop (`vision.go`'s own doc comment explains why `DescribeImage`
deliberately didn't widen it: a one-off vision request builds its own content-array request instead).
Actually wiring inline vision content into the main agent loop would mean touching `doRequest`'s
message construction, streaming, and provider routing for every call site — a much bigger, separate
change, not attempted here. What this plan builds instead: `view_image` is architecturally identical
to today's `DescribeImage` one-off call, just reusable mid-turn and — when the thread's own selected
model is multimodal — using that same model/account for the description instead of always deferring to
a separate describer. The model still never "sees" pixels inline; it reads a text description, same as
it does for an uploaded photo today. That's a smaller, safe extension of an existing pattern rather
than a new capability class.

- **Extract a shared helper** out of `resolveAttachment`'s image branch and `llm.Client.DescribeImage`
  — something like `describeImage(ctx, cfg, selectedModel, imageBytes, mimeType) (description string,
  costUSD float64, err error)` — used by both the existing pre-turn auto-invoke path (unchanged
  behavior: still runs automatically before `agent.Run` for an uploaded image, still shows as a
  synthetic `describe_image` tool call) and the new tool below.
- **New tool `view_image(url)`** (`tools/view_image.go`, `category: research` since it fetches
  external content — excluded from `NoResearch` chat mode same as `image_search`): downloads the image
  (reusing `gateway/attachments.go`'s `saveRemoteImageAttachment` download logic, or a lighter
  fetch-only variant of it), calls the shared describe helper, returns the description as plain text.
  Adds nothing to Cards — a pure description tool, same shape as `think`. Always offered, for any
  model — a non-multimodal model gets exactly today's fallback-to-`cfg.MultimodalModel()` behavior,
  generalized from "only fires automatically on upload" to "the model can also ask for it mid-turn on
  any URL it has," which is a small, clean win on its own even before curation mode exists.
- **`Context` gains a `Multimodal bool`** — the thread's selected model's own flag, threaded from
  `gateway/turn.go` at Context-construction time (same wiring pattern as `GitHubToken`/`LastFMAPIKey`).
- **`image_search` gains an opt-in `mode` argument** (`"auto"` default = today's unchanged behavior;
  `"review"` = don't auto-`AddCard` anything, instead return a numbered `{index, title, url,
  thumbnail}` list as text). Gated in the handler on `ctx.Multimodal`: requesting `"review"` on a
  non-multimodal thread is a tool error explaining why, steering back to plain `mode: "auto"` — per
  discussion, curation mode's whole value proposition (near-free vision calls using the model already
  in play) evaporates for a non-multimodal thread, where each candidate would need its own
  fallback-model round trip; better to say so plainly than let it silently run up cost.
- **Finishing a curation pass**: the model calls `view_image` on the promising candidates from a
  review-mode list, then calls the existing, unmodified `highlight` with its picks — `image_url` comes
  from the thumbnail already read in the review-mode list text (satisfies `highlight`'s invariant
  cleanly, no schema change), and `price`'s free-text field doubles as a short caption when the item is
  a photo rather than a product — same "it's just a string, nothing downstream parses it" property
  already established for shopping/repos/places.
- `image_search.yaml`'s `api_description` gets the review-mode explanation; no new focus mode.

## 4. DeepSeek V4.1 Flash — added alongside the existing `deepseek` entry, not replacing it

Confirmed released today (2026-09-10) — DeepSeek's own release notes describe it as a multimodal
(text+image) MoE, sparser/cheaper tier of the V4.1 family, positioned above V4 Pro on
performance/speed despite the "Flash" naming. Genuinely useful for §3 above: it'd be the first model
in `models/models.go` that's both multimodal *and* not the dedicated small describe-only `mimo`
model, meaning image curation gets to run on the same model actually driving the conversation instead
of always paying for a separate describer call.

**Explicitly additive** — the existing `deepseek` entry (`deepseek/deepseek-v4-flash-0731`, five-deep
provider fallback chain, `ResearchWorker: true`) stays exactly as-is. This is a new, separate
`config.ModelConfig` entry, e.g. `ID: "deepseek-v41-flash"`, `Model:
"deepseek/deepseek-v4.1-flash"`, `Multimodal: true`. Per the user's own steer, it's running roughly
3x the existing Flash entry's price — not a drop-in upgrade, a separate option to try.

**Not finalized here, and shouldn't be guessed at**: provider/pricing/quantization selection needs the
same live `GET /api/v1/models/deepseek/deepseek-v4.1-flash/endpoints` survey `deepseek`/`deepseek-pro`'s
own doc comments record doing before picking a `Provider` list — the model released today, and a
same-day WebFetch against OpenRouter's page didn't return real structured pricing (the page appears too
fresh to scrape cleanly, and there's no OpenRouter API key in this environment to hit `/endpoints`
directly). Whoever implements this should run that survey for real before committing a `Provider` list,
exactly per this repo's "verify on real hardware, not just review" culture — a wrong guess here means a
silent 404/misroute the same way `mimo-pro`'s multimodal flag once did.

## 5. `web_read` gains best-effort schema.org/JSON-LD extraction — shared infra, two callers

Came out of a live spike (not just review, per this repo's culture): fetched real pages via curl with
`web_read`'s own User-Agent to see what's actually there before designing anything.

**Recipes** (justapinch.com, a real chocolate-chip-cookie recipe page): clean `schema.org/Recipe`
JSON-LD — `prepTime: "PT5M"`, `cookTime: "PT10M"`, `recipeYield: "10-12"`,
`aggregateRating: {ratingValue: "5", reviewCount: 2}`. This is near-universal on real recipe sites —
Google requires this exact schema for recipe rich snippets — so it's reliable, not a lucky find.
(Food Network's own site 403'd the plain curl request outright — same Akamai-style bot wall
shopping-mode.md already hit on Amazon. Not every source is reachable; `web_search` surfacing several
candidates already covers that, same as everywhere else.)

**Job postings**: genuinely inconsistent, worth recording plainly rather than assuming symmetry with
recipes. `jobs.lever.co` postings carry real `schema.org/JobPosting` JSON-LD (`title`,
`hiringOrganization`, `jobLocation`, `employmentType`, `datePosted`). `job-boards.greenhouse.io` —
one of the most widely used ATS platforms — carries **none**: it's a client-rendered SPA
(`<script type="module">`, zero occurrences of `schema.org` anywhere in the raw HTML), even though the
salary/location text ("$85,000 - $100,000 + equity + benefits", "Hybrid") is sitting right there as
plain server-rendered text. `og:image`/`og:title` (company logo, job title) are present on both.

**Design, informed by that asymmetry**: `web_read.go`'s `fetchAndExtract` currently parses the
document, strips `<script>` tags, then extracts text — which would silently destroy any JSON-LD if it
ever tried to read it, since that's exactly where it lives. Add a small **best-effort, type-registry**
extractor, read *before* the script-stripping step:

- Look for `<script type="application/ld+json">` blocks (there can be more than one; schema.org also
  allows a block to be a JSON array rather than a single object).
- A small `map[string]func(raw json.RawMessage) string` keyed by `@type` — starting with `"Recipe"`
  and `"JobPosting"` — each producing one short labeled line, e.g. `Prep: 5 min · Cook: 10 min ·
  Yield: 10-12 · ★5.0 (2 reviews)` or `Full-time · New York, NY · Posted 2026-08-xx`. Deliberately
  generic infrastructure, not a `parseRecipe`/`parseJob` pair hardcoded into `fetchAndExtract` itself —
  the same registry covers `Event`/`Product` later (the travel/listings candidates from the original
  issue) for free, without touching `fetchAndExtract` again.
- **Best-effort by construction, matching every other fallback in this codebase**: no matching
  `@type`, malformed JSON, or a page with no JSON-LD at all (Greenhouse's case) just means this line is
  absent — `fetchAndExtract` returns exactly what it does today (plain body text, which for the
  Greenhouse case still contains the salary/location as prose, just unstructured). Never a hard
  failure; nothing about `web_read`'s existing contract changes for a page with no JSON-LD.
- Appended to the returned `text`, same channel `og:image`/`og:site_name` already use — no new return
  value, no signature change to `fetchAndExtract`.

`highlight` itself needs no changes for either caller, same as §1: the model reads the structured line
in `web_read`'s result this turn, and can put it straight into `price` (`"⏱ 15 min · ★5.0"` for a
recipe, `"$85k-$100k · Hybrid"` for a job) — satisfies the "must come from something you read this
turn" invariant cleanly, because now it genuinely did.

**No dedicated focus mode for either** — same reasoning as the recipe discussion: both are self-evident
from the user's own message ("find me a recipe for X" / "find me jobs for Y"), and the behavior needed
is tool-usage guidance (search, read a few candidates, notice the structured line if present, call
`highlight`) that belongs in `web_read.yaml`/`highlight.yaml`'s prompt text — the same "narrow it down"
sentence already planned for books/movies/music/places in §1 — not a new entry in the focus-mode
picker. Shopper mode earns its slot by needing a *sustained* behavior change across a whole
conversation; a recipe or job search is one-shot. Generalizing this into a mode-per-content-type would
undermine the exact discipline `highlight` was built to enforce at the tool layer.

## Out of scope / deferred (not designed here)

- **GitHub repo language-composition bar.** Needs `/repos/{owner}/{repo}/languages`, a new `Card`
  field (or a dedicated `RepoCard.svelte` + `Kind: "repo"`), and real frontend work for the bar itself.
  Real, worth doing once the plain carousel version above ships and earns its keep.
- **Live-resolving `highlight` by `type`.** Rejected in §1 above for now — revisit only if a concrete
  case needs highlighting something never read via `books`/`music`/`movies`/`nearby_search` this turn.
- **A Researcher-focus-mode source grid.** Still on shopping-mode.md's original deferred list; not
  picked up in this pass.
- **Inline vision content in the main agent loop** (the model literally seeing image bytes mid-turn,
  not just reading a text description of them). Would require widening `llm.ChatMessage.Content` to a
  content-block array across `doRequest`/streaming/provider routing — a genuinely separate, much
  larger change than anything else in this doc. `view_image`'s describe-and-return-text approach
  covers the actual curation use case without it.
- **Travel (flights/hotels) and other listings** (real estate, event tickets) — still real
  `highlight` candidates per the original issue, same "compare a handful of real options" shape as
  shopping/places/jobs, just not designed in this pass. Job postings moved out of this bucket into
  §5 once the JSON-LD spike showed a concrete, mostly-reliable path.
- **`Event`/`Product` schema.org types** in the same JSON-LD registry §5 builds — natural next entries
  once `Recipe`/`JobPosting` are shipped and the registry pattern is proven; would cover travel/ticket
  listings above for free. Not built now — no live spike done for either type yet.

## Next steps

1. `tools/books.go` / `tools/movies.go` / `tools/music.go` / `tools/nearby_search.go` — extend each
   per-item result line with that item's URL + cover/photo URL, mirroring what's already in the
   parallel `Card` population in the same function.
2. `tools/descriptions/{books,movies,music,nearby_search}.yaml` — one added sentence each pointing the
   model at `highlight` for narrowing down to a favorite/top few, now that the data exists to do so.
3. `tools/github_repo.go` — `Owner.AvatarURL` field on `githubRepoInfo`/`githubRepoStats`;
   `ctx.AddCard` call in `handleGitHubRepo` with a short stats badge as `Subtitle`, default `Kind`.
4. `gateway/attachments.go` / `llm/vision.go` — extract the shared `describeImage` helper used by both
   the existing pre-turn auto-invoke path and the new tool below; behavior unchanged for uploads.
5. `tools/view_image.go` (new) — `view_image(url)` tool: download + describe via the shared helper,
   `category: research`, no Card output. `tools/descriptions/view_image.yaml` + `catalogOrder` entry
   (after `image_search`, before `highlight`).
6. `tools/registry.go`'s `Context` — add `Multimodal bool`; wire from `gateway/turn.go` at
   Context-construction time, keyed off the thread's selected `config.ModelConfig.Multimodal`.
7. `tools/image_search.go` — optional `mode` argument (`"auto"`/`"review"`); review mode returns a
   numbered candidate list instead of auto-adding Cards, gated on `ctx.Multimodal` with a clear tool
   error otherwise. `tools/descriptions/image_search.yaml` gets the review-mode explanation.
8. `models/models.go` — new `deepseek-v41-flash` entry, additive alongside the existing `deepseek`.
   **Blocked on a real live `/endpoints` provider/pricing survey** — do not guess the `Provider` list.
9. `tools/web_read.go` — read `<script type="application/ld+json">` blocks before the existing
   `doc.Find("script, ...").Remove()` step; a small `@type`-keyed registry (`"Recipe"`,
   `"JobPosting"` to start) each rendering one labeled summary line, appended to the returned `text`
   alongside `og:image`/`og:site_name`. No match / parse failure / no JSON-LD at all → today's
   behavior, unchanged, exactly as already true for a page with no `og:image`.
10. `tools/descriptions/web_read.yaml` + `tools/descriptions/highlight.yaml` — one sentence each: when
    a structured summary line is present and the user's asking to compare/narrow down a few real
    options (recipes, jobs, or anything else the registry later covers), call `highlight` with it.
11. Live-verify before calling any of this done, per `CLAUDE.md`'s culture: a real turn asking for a
    book/movie/repo/place recommendation and then "highlight your favorite," a real multi-repo
    comparison turn (does the carousel actually group them), a real image-curation turn on
    `deepseek-v41-flash` once its provider list is confirmed (does `view_image` actually get called,
    does `highlight` render captions correctly), confirming `mode: "review"` is correctly refused on a
    non-multimodal thread, and a real recipe + a real Lever job posting turn (does the JSON-LD line
    show up in `web_read`'s result, does a Greenhouse posting degrade cleanly to plain text).
12. Docker two-sided sync checklist (per `CLAUDE.md`) — n/a for this slice: no new hot-editable
    resource directory, no new CLI command, no new settings-panel server-mutating action. The new
    model entry is compiled into the binary/image like any other Go code change, not a runtime
    resource needing a bind mount.
