# Hill-climbing opportunities — plan

Status: **planning only, nothing implemented.** Written from a code survey on 2026-09-25; every
"today it does X" claim below cites the file it came from, but none of the proposed metrics have
been run yet — no baseline numbers exist for any of them.

## Why this exists

The one scored feedback loop Polaris has today is `benchmark/` (BrowseComp / SimpleQA /
LiveNewsBench via `polaris benchmark`). It's the right tool for the end-to-end question "did the
research turn get better," but it's expensive: every sample is a full multi-turn `agent.Run` plus
paid search plus a grader call. That makes it a poor inner loop for iterating on anything smaller
than the whole agent.

Every other quality lever in the codebase has been tuned the way `ebd68fb` tuned
`compaction_system`: run it live, read the output, fix what looks wrong. That catches real bugs,
but it leaves no number behind, so the next edit can't tell whether it regressed anything.

This plan collects the places where a **cheap, fast, repeatable** score is possible — most of them
deterministic Go/TS code that needs no model call at all — so each one can be improved by
iteration (change → score → keep or revert) rather than by eyeballing.

## The data we already have

Most of the fixture corpora below don't need to be invented; production already records them in
`polaris.db` (the `polaris_polaris-data` Docker volume on the potato — see CLAUDE.md's "Production
access"):

- `messages.content` / `messages.transcript` — every answer, and the verbatim turn transcript
  (tool calls and results) behind it.
- `messages.citations` — which URLs the model actually cited.
- `events.data` (source `tool.web_search`, `tool.web_read`, …) — tool args, including every URL
  `web_read` fetched.
- `search_cache` / `search_cache_results` — stored result lists with position, engines, and
  rank_state.
- `messages.prompt_tokens` / `messages.cache_read_tokens` — per-turn cache accounting (already
  aggregated in `store/stats.go`).
- `pulsar_daily_trace` — Pulsar Daily's per-stage records.

**Prerequisite for most items:** a one-off export script that pulls a snapshot of these into a
checked-out-but-gitignored fixtures directory (e.g. `dev/fixtures/`, never committed — it's the
operator's personal history). Pages fetched by `web_read` need to be re-fetched and saved as raw
HTML once, since the DB only stores extracted text.

## Tier 1 — free: deterministic code, offline fixtures, no model calls

Each of these is a pure function (or close to it) that can be scored by a `go test` / vitest run
over a saved corpus in seconds.

### 1. `web_read` HTML extraction — highest priority

**Today** (`tools/web_read.go`'s `fetchAndExtract`): removes a fixed tag list (`script, style,
nav, footer, header, noscript, iframe, svg, form, aside`), takes the first `<article>`, else
`<main>`, else `<body>`, and flattens it with goquery's `.Text()` + `collapseWhitespace`. No
content-block scoring, no link-density check, no cookie-banner/"related articles" handling, and
tables/lists lose all structure (a spec table becomes one run-on line).

**Why it matters:** this text is in the model's context on nearly every research turn — it's both
an answer-quality lever and a per-turn token-cost lever.

**Corpus:** a few hundred real pages, saved as raw HTML, drawn from `web_read` URLs in `events`.
For each page, hand-mark one or more "answer-bearing" sentences (or reuse the claim the model cited
it for).

**Metrics:**
- answer-sentence recall (did the fact survive extraction),
- boilerplate ratio (share of output lines matching nav/cookie/related-links patterns, or not
  present in a hand-trimmed reference),
- output characters per page (lower is cheaper, as long as recall holds),
- table fidelity on a small table-heavy subset (row/column structure preserved).

**Candidate moves:** readability-style block scoring, link-density pruning, rendering tables and
lists to markdown instead of flattening them, stripping common consent/banner containers.

### 2. Paywall / empty-page heuristics

**Today:** `looksLikePaywall` is an exact-substring list (`paywallMarkers`); `looksEmpty` is a
character-count threshold (`minViableExtractedChars`). Together they decide when `web_read` falls
back to Wayback or spends a capped, paid Tavily credit (see CLAUDE.md's "Web search fallback
chain").

**Corpus:** the same saved pages as #1, each labelled `paywalled` / `js-only` / `fine`.

**Metrics:** precision and recall per class. False positives cost a Tavily credit each; false
negatives hand the model a paywall stub as if it were the article.

### 3. Search result ranking

**Today** (`search/searxng.go`): SearXNG results are fused with Reciprocal Rank Fusion
(`rrfScore`, constant `rrfK`), then adjusted by `domain_rankings.yaml` states
(block/lower/raise/pin). `prompt.md` spends a full paragraph asking the model to avoid citing
homepage-shaped URLs — a ranking problem pushed onto the prompt.

**Labels, for free:** for each stored result list, the results the model went on to `web_read`
or cite are the relevant ones. That's an implicit click-through signal already sitting in the DB.

**Metrics:** MRR / NDCG@k of used results over the stored `search_cache_results` lists.

**Candidate moves:** tune `rrfK`, per-engine weights, how strongly raise/lower shift a result, and
a homepage-shaped-URL demotion (bare domain, `/news`, section index) — which, if it works, lets
that `prompt.md` paragraph shrink.

**Caveat:** the labels are biased toward whatever ranking was live when they were collected
(position bias). Fine for relative comparisons between nearby variants; don't read absolute
numbers too literally.

### 4. Mermaid render success

**Today** (`web/src/lib/mermaid.ts`): on a parse failure, `autoQuoteLabels` retries once,
repairing only unquoted `ID[label]` nodes. Other shapes (`(…)`, `{…}`, `((…))`, edge labels
`|…|`) aren't repaired. `prompt.md` carries a long paragraph of quoting rules to compensate.

**Corpus:** every ```` ```mermaid ```` block extracted from `messages.content`.

**Metric:** share of blocks that parse (mermaid's own `parse()` in vitest, no browser needed).

**Payoff:** higher render rate directly, and possibly deleting most of `prompt.md`'s Mermaid
paragraph, which is sent on every turn.

### 5. Voice chunking

**Today** (`voice/chunk.go`): `sentenceEndRe` splits on any `[.!?]+` followed by whitespace, so
"Dr. Smith", "U.S. rules", "e.g. this" all get a mid-sentence break. `StripMarkdown` handles
links/bold/italic/headers/inline code, but not list bullets or tables. `maxChunkChars` is 300.

**Corpus:** stored answers run through `SplitIntoSpeechChunks`, plus a hand-written edge-case
table (abbreviations, decimals, lists, tables, URLs).

**Metrics:** false sentence breaks per 1k sentences; leftover markdown characters that would be
spoken; size of the first chunk (a proxy for time to first audio).

### 6. Prompt token footprint

**Today:** each tool has two description channels (`description` → `{tools}` in the system
prompt, `api_description` → the function schema), about 37 KB across `tools/descriptions/*.yaml`,
with heavy overlap (e.g. `web_read.yaml`). `prompt.md` is ~8.8 KB.

**Metric (pure arithmetic):** total system-prompt + tool-schema bytes/tokens per composer mode
(default, chat-only, Deep Research, each focus mode, voice).

**Guardrail (cheap, Tier 2):** a small fixed set of tool-choice probes — single model calls
asserting the right tool is picked first — so trimming doesn't silently break tool selection.

### 7. Prompt-cache hit rate

**Today:** `prompt.md` puts the parts that change first — `{tools}` (varies with composer
toggles), then `{memories}`, `{person}`, `{custom_instructions}` — ahead of ~8 KB of fixed rules.
Providers cache on an exact prefix, so any change to a memory or toggle bills everything after it
at full price again. (The date line was already fixed for exactly this reason — see
`agent/driver.go`'s `currentContextPreamble` doc comment.)

**Metric:** `cache_read_tokens / prompt_tokens` from `messages`, grouped by week — already
recorded, just not yet trended against prompt changes.

**Candidate move:** reorder `prompt.md` so fixed rules come before the changing sections; measure
the hit rate over a week before and after.

### 8. Server overhead per turn, measured with `dev/fakeopenrouter`

A real `polaris run` pointed at `dev/fakeopenrouter` costs nothing to run.

**Metrics:** time from WebSocket message to the first upstream request; number of model calls per
turn (main loop + title + suggestions + compaction); bytes per upstream request (via
`/_control/calls`).

**Also:** there are currently no `func Benchmark` tests anywhere in the Go code. Adding a few for
the hot pure functions (extraction, `collapseWhitespace`, ranking, prompt assembly) gives this tier
a baseline.

## Tier 2 — pennies: one cheap model call per case, no agent loop

These score a single prompt in isolation, so a case costs a fraction of a cent rather than a full
research turn. Needs a small harness (a sibling to `benchmark/`, or a `--suite` that bypasses
`agent.Run`) that loads cases, calls one prompt, and applies a checker.

| Prompt (`prompts.yaml`) | Cases from | Checker |
|---|---|---|
| `turn.title_system` / `title_regenerate_*` | first user messages / whole threads | code: 3–6 words, no trailing punctuation; small LLM judge: names the topic, doesn't answer it |
| `turn.suggestions_task` | stored answers | code: exactly 3 lines, each ends in `?`, "Visualize this as a chart?" only when chartable (hand-labelled) |
| `turn.compaction_system` | long real threads | facts/URLs retained (auto-extracted list from the original), summary size across 1, 2, 3 rounds |
| `tools.web_read_filter_system` | saved pages + instructions | extracted target present; injected-instruction pages don't change behaviour |
| `turn.memory_import_system` | sample export dumps | memories created vs. hand "should keep" set; instructions never bundled |
| Pulsar Daily unchanged-judge / top-story (`gateway/pulsar_daily.go`) | `pulsar_daily_trace` pairs | agreement with hand labels |

**Grader note:** use a different model from the one under test where an LLM judge is needed. The
existing benchmark grades with the same model it tests (`cmd/benchmark.go` uses `client` for both),
which flatters scores.

## Small fixes found along the way

Not hill-climbing, but surfaced by the survey and cheap to act on:

- **`agent.empty_answer_retry` leaks BrowseComp's answer format into production.** It tells the
  model to answer "starting with 'Explanation:'" (`prompts.yaml`, default at
  `prompts/prompts.go:372`), which is BrowseComp's template (`benchmark/templates.go:17`), but the
  nudge fires in normal chats too (`agent/driver.go:656`). Not yet observed live.
- **`prompts.yaml` has drifted from the Go defaults.** A throwaway diff test found 4 mismatches:
  `Agent.VoiceModeInstruction` (the YAML's show/highlight sentence is missing from the default),
  `Agent.FallbackSystemPrompt`, and the two Pulsar Daily wizard prompts (whitespace-level). A
  permanent drift test in `prompts/prompts_test.go` would keep them in sync.
- **Pulsar Daily prompts are inline Go strings** (`gateway/pulsar_daily.go:245, 417, 494, 579`),
  against the "every prompt in `prompts.yaml`" convention — they can't be hot-edited, which makes
  them the hardest prompts to iterate on. Moving them is a prerequisite for scoring them.
- **`deep_research_instruction` and `focus_modes.researcher` are near-duplicate paragraphs.**
- **Frontend tests never run in CI.** `.github/workflows/go-ci.yml` builds the frontend but never
  runs vitest or svelte-check, and only triggers on Go paths — a frontend-only change gets no CI.
- **`TestCrossProcessCloseRace` flaked once** (`store/crossprocess_test.go:270`: `SQLITE_BUSY`,
  then a write reported as successful was missing around `oldProc.Close()`); it passed on three
  reruns. The scenario is the container-recreate handoff during `polaris update`, so it's worth
  root-causing rather than retrying.

## Checked and ruled out (for now)

- **Frontend bundle size:** the entry page preloads ~76 KB gzipped JS across 29 files; Mermaid (the
  ~660 KB chunk) is already a lazy `import('mermaid')`, and highlight.js is imported per-language
  from `highlight.js/lib/core`.
- **Title generation latency:** `generateTitle` runs after `agent.Run` in `gateway/turn.go`, so it
  doesn't delay the first answer token.
- **Tool catalog loading:** `tools/catalog.go`'s `loadCatalog` is mtime-cached, not re-parsed per
  call.

## Suggested order

1. Fix the `empty_answer_retry` leak and add the prompts drift test (small, independent).
2. Write the fixture export script (unblocks everything below).
3. **#1 extraction + #2 paywall/empty heuristics** — one saved-page corpus, fully offline, touches
   both answer quality and Tavily spend.
4. **#3 search ranking** — labels already exist.
5. **#7 cache-hit reorder** — one edit, measured from data already being collected.
6. Tier 2 harness, starting with titles/suggestions/compaction.
7. **#4 Mermaid, #5 voice, #6 token footprint** as time allows.

Throughout, the full `polaris benchmark` run stays the end-to-end check before trusting a stack
of small wins — run it occasionally, with a fixed `--seed`, rather than as the inner loop.

## Open questions

- Where fixtures live and how they're kept out of git (they're personal history). A gitignored
  `dev/fixtures/` is the working assumption.
- Whether the Tier 2 harness should be a new `polaris eval` command or new `benchmark` suites. The
  `benchmark.Suite` interface is built around `agent.Run`, so a separate command is probably cleaner.
- How much hand-labelling is acceptable for #1/#2 — a few hundred pages is a couple of hours; a
  smaller seed set (50) is enough to start climbing.
