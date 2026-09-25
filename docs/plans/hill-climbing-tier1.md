# Hill-climbing Tier 1 — handoff for review + implementation

Status: **nothing implemented yet.** This is a handoff doc, written after a status-check
found that `polaris eval` (issues #110/#111, `eval/`) only ever covered Tier 2 of
`docs/plans/hill-climbing.md`'s two-tier split — Tier 1 (deterministic code, offline
fixtures, zero model calls) is completely untouched. Read `docs/plans/hill-climbing.md`'s
"Tier 1" section first; this doc is the concrete build plan against it, not a restatement.

## Why this is a separate track from `polaris eval`

`eval/` (Tier 2) scores single LLM/Jev calls — titles, suggestions, compaction, citation
support, tool selection, injection resistance. Every one of those needs a real (if cheap)
API call. Tier 1 is different in kind, not just degree: every item is a **pure function**
(HTML → extracted text, text → paywall/empty classification, raw result list → ranked list,
mermaid source → parses-or-not, answer text → speech chunks) that can be scored in a tight
loop with **zero network calls and zero cost**, once a fixture corpus exists. That's a
meaningfully faster inner loop than even Tier 2's pennies-per-run, and it's the tier
`docs/plans/hill-climbing.md` calls "highest priority" for its #1 item (web_read
extraction) specifically because that text is in the model's context on nearly every
research turn.

Where Tier 1 code should live is a real open question — see each item's "Where to score
it" note below. The common thread: `fetchAndExtract`, `looksLikePaywall`, `looksEmpty`,
`collapseWhitespace` (tools/web_read.go) are all unexported, so a scorer needs either to
live inside package `tools` (as a `_test.go` file, which can call them directly and is the
path of least resistance) or those functions need exporting — a call worth making
deliberately, not by accident of where the scorer ended up.

## What already exists to build on

`dev/fixtures_export` (issue from the earlier hill-climbing session, commit `87e9356`)
pulls real signals out of a `polaris.db` snapshot into `dev/fixtures/` (gitignored — see
its own doc comment for why, and for how to pull a safe production snapshot via the
potato's Docker volume). Today it exports three things:

- `web_read.jsonl` — every web_read'd URL, cited/not, the extracted text production
  actually kept, and (capped, cited-first) a re-fetched raw HTML page under `pages/`.
- `search_ranking.jsonl` — cached search result lists with implicit
  clicked/cited relevance labels.
- `pulsar_daily_trace.jsonl` — Pulsar Daily's per-block trace with keep/drop verdicts.

This covers items #1, #2, and #3 below. It does **not** yet cover #4 (Mermaid) or #5
(voice chunking) — those need their own corpus, described per-item below. #6/#7/#8 need no
corpus at all (pure arithmetic / DB aggregation / a live `fakeopenrouter` run).

## Item-by-item build plan

Numbering matches `docs/plans/hill-climbing.md`'s own Tier 1 list.

### 1. `web_read` HTML extraction — highest priority

**Corpus:** `dev/fixtures_export`'s `pages/*.html` (raw HTML) + `web_read.jsonl`'s
`result_text` (what production's `fetchAndExtract` + `collapseWhitespace` actually
produced from it) — already exists, just needs pulling from a real snapshot and hand-
labelling. **Still needed:** for each page, hand-mark the answer-bearing sentence(s) (or
just reuse whatever claim the model cited it for, cross-referencing `cited`/the message
that triggered the fetch) — this labelling step hasn't started.

**Metrics** (from `hill-climbing.md`): answer-sentence recall, boilerplate ratio, output
chars/page, table fidelity on a table-heavy subset.

**Where to score it:** `tools/web_read.go`'s `fetchAndExtract` takes a URL and does its own
HTTP fetch — it needs a companion that takes raw HTML bytes directly (or a tiny refactor
splitting "fetch" from "extract") so the scorer can run extraction against a saved
`pages/*.html` file without re-fetching. That refactor is probably needed regardless of
where the scorer lives.

**Candidate moves** (from the plan, not yet tried): readability-style block scoring,
link-density pruning, rendering tables/lists to markdown instead of flattening, stripping
consent/banner containers.

### 2. Paywall / empty-page heuristics — real-world coverage currently unknown

**Elevated priority — checked while writing this doc, and the finding changes the framing
significantly.** `tools/web_read_test.go` covers `looksLikePaywall`/`looksEmpty` with
exactly two hand-written strings each (one obviously-paywalled sentence, one obviously-fine
one) — that confirms the matching *logic* runs, not that `paywallMarkers` (11 fixed English
substrings: "subscribe to continue reading", "this content is for subscribers", etc.)
actually matches real paywalled sites' current wording. This has **never been checked
against a single real paywalled page.** It's entirely plausible this heuristic fires near-
never in production — NYT/WSJ/Bloomberg/Medium/The Economist/FT all paywall differently,
templates change over time, and an exact-substring match against 11 phrases is exactly the
kind of thing that silently rots. Nobody would notice it failing, since the fallback
(Wayback, then a capped paid Tavily credit — see CLAUDE.md's "Web search fallback chain")
still produces *an* answer either way, just a worse and more expensive one.

**Corpus:** same `pages/*.html` set as #1, each hand-labelled `paywalled` / `js-only` /
`fine` — but the passive "whatever `web_read` happened to fetch in real usage" sample from
`dev/fixtures_export` may not contain many real paywall hits at all (paywalled sites often
get skipped or fall back before ever reaching a labelled page). **Don't rely on incidental
production traffic alone for this one** — deliberately fetch a set of known-paywalled URLs
from major outlets (NYT, WSJ, Bloomberg, Medium members-only posts, The Economist, FT) as a
dedicated adversarial subset, specifically to get a real answer to "does this fire at all"
before worrying about precision/recall tuning.

**Metrics:** precision/recall per class for `looksLikePaywall`/`looksEmpty`
(tools/web_read.go) — but the *first* number that matters here is simpler: raw recall on a
deliberately-gathered real-paywall set. If that's near zero, tuning precision/recall is
premature; the marker list itself needs rebuilding from real examples first.

**Where to score it:** same file/package as #1 — these three functions
(`fetchAndExtract`, `looksLikePaywall`, `looksEmpty`) are tested together in practice since
paywall/empty detection runs on `fetchAndExtract`'s own output.

**Update — real-world sanity check run (2026-09-25), findings below.** Ran
`tools/paywall_livecheck_test.go` (new, `//go:build livecheck`-gated so it never runs in
`go test ./...`/CI — hits the real network) against 6 real, freshly-published URLs (3
Bloomberg, 3 Medium member-only posts, found live via search rather than guessed). Two
results changed the framing from what this doc originally expected:

1. **Bot-blocking, not a 200-with-paywall-shell, is the dominant real failure mode for
   major outlets.** All 6 URLs 403'd on the direct fetch (confirmed with both Polaris's own
   fetcher and a plain `curl` using a real browser User-Agent — this isn't a UA issue).
   Widening to bare homepages: NYT 403, WSJ 401, FT 403, Economist 403; only
   theverge.com returned a clean 200. This means `looksLikePaywall`'s marker matching is
   **structurally unreachable** for these outlets in practice — the `err != nil` branch
   (web_read.go:171-175) fires before there's ever text to inspect, sending it straight to
   the Wayback→Tavily fallback chain. The precision/recall question this doc originally
   framed matters far less for these outlets than whether that fallback chain itself
   recovers real content.
2. **Ran the full fallback chain for real** (Wayback then Tavily, same order as
   `handleWebRead`, using the dev Tavily key). Wayback had no snapshot for any of the 6
   (all too recent). Tavily recovered "content" for all 6, but two new, concrete bugs
   surfaced in what it recovered:
   - **Tavily sometimes returns site chrome instead of the article, and nothing catches
     it.** 2 of 3 Bloomberg cases got back Bloomberg's own login-wall navigation
     boilerplate ("Skip to content Bloomberg the Company & Its Products... Terminal Demo
     Request...") instead of the article — long enough (5-6k chars) to pass `looksEmpty`,
     and containing none of the paywall markers, so `looksLikePaywall` also says "fine."
     This is silently worse than an honest failure: nothing downstream knows extraction
     didn't actually work. **Not yet fixed — needs its own design pass** (what
     distinguishes chrome from article text isn't obvious; a naive fix would need real
     tuning against more chrome-passthrough examples, not a one-line change like the
     marker list below).
   - **Medium's real paywall label, found live, was missing from `paywallMarkers`.** One
     Medium case's Tavily-recovered text literally contained "Member-only story" — Medium's
     actual current UI label — which wasn't in the 11-phrase list. **Fixed**: added
     `"member-only story"` to `paywallMarkers` (`tools/web_read.go`), 2026-09-25.

**Correction to the marker-fix claim above:** the `"member-only story"` marker addition is a
real, correct fix for the general case (a direct 200 fetch that returns a paywall HTML
shell), but it did **not** actually change behavior for the Medium cases observed live —
`looksLikePaywall` is only ever checked against `fetchAndExtract`'s direct-fetch text
(web_read.go:175) and `looksEmpty`'s result on Tavily's own output; nothing in production
re-checks `looksLikePaywall` against Tavily's *result* once obtained (web_read.go's old
line ~195-199 accepted Tavily's text on `!looksEmpty(tavilyText)` alone). Worth flagging in
case a future page slips a genuine paywall banner through Tavily's rendering too — not
addressed here, scope was the chrome-passthrough bug below.

**Chrome-passthrough bug: investigated and fixed, 2026-09-25.** Extended the livecheck test
to run Tavily's real `Extract` call (dev key) and dump full recovered text to
`dev/fixtures/paywall_livecheck/*.txt` for inspection, plus added two clean baselines
(Wikipedia, a Verge page) for contrast. Findings:

- The Bloomberg "chrome" wasn't 100% noise as first assumed — real article sentences were
  present but heavily diluted: one case was 173 lines/5.3KB, almost entirely 1-3 word nav
  labels ("About", "Careers", "### Products"...) with the same nav block duplicated twice
  (mobile+desktop), containing only 6-7 genuine sentences buried inside.
- Root cause: `fetchAndExtract`'s own goquery path already solves exactly this problem by
  removing `script, style, nav, footer, header, ...` tags before extracting
  `article`/`main`/`body` (web_read.go, near `fetchAndExtract`) — but Tavily's `Extract` API
  returns already-rendered plain text with no HTML structure left to filter by, so that
  same protection never applied to the Tavily fallback path.
- **Fix:** added `stripBoilerplateLines` (`tools/web_read.go`) — drops lines under 80 chars
  (real article sentences in the sample were all 150+ chars; nav lines were nearly all under
  40) from Tavily's result specifically, falling back to the unfiltered text if filtering
  would leave too little (`minViableExtractedChars`) so it can only reduce noise, never turn
  a real extraction into an empty one. Wired into both of `handleWebRead`'s Tavily call
  sites (the ordinary fallback branch and `force_tavily`). Verified against the real live
  Bloomberg cases post-fix: 5328→2175 chars and 6173→2607 chars, with the genuine article
  sentences confirmed still present and the nav noise cut. Two new unit tests
  (`tools/web_read_test.go`) cover this with a synthetic case shaped like the real sample
  (doesn't depend on the gitignored fixture files) plus a guard against gutting a
  legitimately short article.
- **Not fully solved:** the length threshold is a blunt instrument — a few long boilerplate
  paragraphs (Bloomberg's own "Connecting decision makers to a dynamic network..." marketing
  copy, a Terms-of-Service consent blurb) are long enough to survive filtering alongside the
  real sentences. Noise reduction, not perfect isolation — deliberately scoped that way
  rather than reaching for a fragile boilerplate-phrase list (same staleness risk this
  section's original marker-list finding already warned about).

### 3. Search result ranking

**Corpus:** `dev/fixtures_export`'s `search_ranking.jsonl` — already has the implicit
click-through labels (`was_clicked`/`was_cited`), no hand-labelling needed. Ready to score
today, pending a real production snapshot with non-trivial `search_cache` data (the local
dev DB had 0 rows here when last checked — needs a potato snapshot).

**Metrics:** MRR/NDCG@k of used results over the stored ranked lists.

**Where to score it:** `search/searxng.go`'s `rrfScore`/`rrfK` and the
`domain_rankings.yaml` state adjustments are what's actually being tuned — scorer likely
belongs in `search/` as a `_test.go` file for the same unexported-function-access reason as
#1/#2.

**Caveat already flagged in the plan:** labels are biased toward whatever ranking was live
when collected — fine for comparing nearby variants, not for reading absolute numbers.

### 4. Mermaid render success

**Corpus: doesn't exist yet.** Needs every ` ```mermaid ` block extracted from
`messages.content` — `dev/fixtures_export` has no support for this today (it's scoped to
`events`/`search_cache`/`pulsar_daily_trace`, not raw message content). This is probably a
small addition to that tool rather than a new one: walk `messages.content` for fenced
mermaid blocks, dump them to a `mermaid_blocks.jsonl`.

**Metric:** share of blocks that parse — pure JS/TS, via mermaid's own `parse()` in vitest,
no browser needed. `web/src/lib/mermaid.ts`'s `autoQuoteLabels` is what's being scored.

**Where to score it:** a vitest file under `web/`, since this is frontend TS code, not Go —
the newly-added `frontend-ci.yml` (issue from two sessions ago) would need a new script/
step to actually run this scorer's report, since `pnpm run test` alone won't print a
retention-rate summary the way a dedicated report would.

### 5. Voice chunking

**Corpus: doesn't exist yet**, and probably doesn't need the DB at all — any stored answer
text works as input to `SplitIntoSpeechChunks` (voice/chunk.go), so this could be as simple
as sampling `messages.content` for assistant messages generally (no `dev/fixtures_export`
dependency), plus a **hand-written edge-case table** the plan explicitly calls for
(abbreviations like "Dr."/"U.S."/"e.g.", decimals, lists, tables, URLs) — that table has to
be authored by hand regardless of corpus source.

**Metrics:** false sentence breaks per 1k sentences, leftover markdown chars, first-chunk
size (time-to-first-audio proxy).

**Where to score it:** `voice/` package, `_test.go` (chunk.go's `sentenceEndRe`,
`StripMarkdown`, `maxChunkChars` are all unexported).

### 6. Prompt token footprint

**No corpus needed — pure arithmetic.** Sum `prompt.md` + `tools/descriptions/*.yaml`'s
rendered size (via `tools.ToolsPrompt` for a given `*tools.Context`, matching whatever
composer-mode toggles are set) in bytes/tokens, per composer mode (default, chat-only, Deep
Research, each focus mode, voice). This is the cheapest item on the whole list to build —
no fixtures, no labelling, just a small Go program or test that prints the numbers.

**Guardrail mentioned in the plan (Tier 2, not Tier 1):** a handful of tool-choice probes
(single model calls asserting the right tool gets picked) so trimming doesn't silently
break tool selection — `eval/`'s existing `tool_selection` kind already covers exactly this
shape; a few more cases there would be the guardrail, not new Tier 1 code.

### 7. Prompt-cache hit rate

**No corpus needed — pure DB aggregation**, already recorded
(`messages.prompt_tokens`/`cache_read_tokens`, aggregated in `store/stats.go`'s `GetStats`)
but never trended against prompt changes over time. Needs: a small query grouping
`cache_read_tokens / prompt_tokens` by week, run against a real production DB snapshot (the
same kind `dev/fixtures_export` already knows how to safely pull).

### 8. Server overhead per turn (`dev/fakeopenrouter`) + missing `Benchmark` tests

**No corpus needed** — a live `polaris run` pointed at `dev/fakeopenrouter` (already built,
see CLAUDE.md's own description) costs nothing to run. Metrics: time from WS message to
first upstream request, model calls per turn, bytes per upstream request (via
`/_control/calls`).

**Separately:** there are currently zero `func Benchmark` tests anywhere in the Go code.
Adding a few for the hot pure functions this doc's earlier items are about
(`fetchAndExtract`, `collapseWhitespace`, ranking, prompt assembly) gives this tier a speed
baseline alongside the accuracy metrics above — worth doing in the same pass as #1/#2/#3
since the functions are the same ones already being instrumented.

## Suggested order

Mirrors `hill-climbing.md`'s own "suggested order," narrowed to what's left — with #2
pulled ahead of where the original plan had it, given the "does this fire at all in the
real world" finding above.

1. **#2's real-world sanity check, first, before anything else here.** Deliberately fetch
   a handful of known-paywalled pages (NYT/WSJ/Bloomberg/Medium/etc.) and check whether
   `looksLikePaywall` fires on any of them. This is a couple hours of manual work, not a
   corpus-building project, and it answers a binary "does this heuristic do anything in
   practice" question that everything else about tuning it depends on.
2. **Pull a real snapshot and hand-label a seed set for #1/#2.** A `dev/fixtures_export`
   run against the potato already gets the raw pages; labelling ~50 of them (the plan's own
   "enough to start climbing" number) is the actual remaining work, not code.
3. **#1 extraction + #2 paywall/empty** together, since they share a corpus and a package.
   This is the highest-value, most-scoped-out item — start here.
4. **#3 search ranking** — the corpus/labels already exist in the tool, just needs a real
   snapshot with non-empty `search_cache` and a scorer in `search/`.
5. **#6 token footprint + #7 cache-hit rate** — both pure arithmetic/DB queries, no
   fixtures, cheap to knock out once #1-#3 prove out the pattern.
6. **#4 Mermaid, #5 voice chunking** — each needs its own small corpus-building step first
   (extending `dev/fixtures_export` for #4, hand-writing edge cases for #5).
7. **#8 `Benchmark` tests** — fold into whichever of #1/#2/#3's PRs touches those functions,
   rather than a separate pass.

## Open questions to resolve before/while implementing

- **Exported vs. package-internal scorers.** Every candidate function (`fetchAndExtract`,
  `looksLikePaywall`, `looksEmpty`, `rrfScore`, `sentenceEndRe`/`StripMarkdown`) is
  unexported. Decide per-package whether the scorer lives as an internal `_test.go` file
  (no API surface change, but tangles "test" and "scoring report" semantics — running it
  looks like `go test`, not a report) or whether a small number of these get exported
  specifically to support scoring from outside the package.
- **Report format.** Tier 2's `polaris eval` prints pass/fail per case and a summary. Tier
  1's metrics are continuous (recall %, MRR, bytes) rather than pass/fail — worth deciding
  whether these want a similar `polaris` sibling command (e.g. `polaris hillclimb-tier1` or
  folded into `eval` as a non-Jev "metric" case type) or are fine as ad hoc `go test -v`
  output for now, given there's no fixed pass/fail threshold yet to gate on.
- **`dev/fixtures_export` extension for #4.** Does mermaid-block extraction belong in that
  tool (consistent single entry point) or as its own small script? Leaning toward
  extending the existing tool for consistency, but not decided.
- **Where fixtures/labels are stored.** `dev/fixtures/` is gitignored (real chat history).
  A hand-labelled paywall/paywall-class file *derived from* that real data inherits the
  same "don't commit" rule — unlike `eval/cases/`, which is synthetic and committed. Keep
  these two corpora (`eval/cases/`, `dev/fixtures/`) conceptually and physically separate;
  don't let labelled Tier 1 data drift into the committed `eval/` tree.
