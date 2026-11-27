# Source triage ("Bearing"): Oracle, in motion

**Added: 2026-11-14.**

**Status: proposed — research and design only, nothing built.** Tracked in issue #214. "Bearing"
is a working name (a bearing is the direction you steer by; the chip says how true a source is to
the question's heading). Rename freely before anything ships.

## The idea

Oracle mode ([oracle-mode.md](oracle-mode.md)) reads the user's message once, before the turn, and
steers it. This is the same trick applied **while the turn is running**: every time the agent
fetches a page, a decision model reads that page against the request and answers "is this useful
for what was asked?" The answer does three jobs:

1. **A chip on the source** — a small, calm mark in the sources list and the `web_read` tool row
   saying whether the page answers the question, only touches it, is off-topic, is behind a wall,
   or is stale. The operator sees *why* a citation is or isn't trustworthy before reading it.
2. **A nudge to the model** — one line appended to the tool result ("this page doesn't address
   the question; the answer is probably elsewhere"), the same shape as the `[via brave]` and
   `[Page image: …]` lines `web_search`/`web_read` already add. It points the model at the better
   link instead of letting it keep mining a bad one.
3. **A cost lever** — a page judged unusable doesn't need its full text re-sent on every later
   loop iteration, and steering the model off a dead end sooner saves a loop iteration. Phased in
   only after measurement, and only for reads without `instructions` (see "Cost").

Why this fits the product: PRODUCT.md's "sourcing is the product." Oracle already made Polaris's
*decisions* about a question visible; this makes its *judgment of each source* visible. It was
also already on the shelf: [source-verification.md](source-verification.md) lists "scoring search
results for junk/SEO spam before they reach the model" and "`web_read`'s paywall/looks-empty
heuristic chain → a Noul over the extracted text" under "Other places Jev might fit (unexplored)."

## What Perplexity actually did (and where this differs)

From [the Decider v1.3 post](https://www.perplexity.ai/hub/blog/perplexity-decider):

- Decider is a **decision model**: shared state (a document) plus questions, answers come back as
  probabilities per option, many questions in one forward pass. It's the same API shape as the
  `decisions/` client here; v1.3 and Flash are in our Oracle bench already.
- Their page-quality example is **crawl-time**: the crawled page is the shared state and several
  questions (usefulness, audience, harmful content, PII) are answered in one pass, flagging anti-bot
  walls, error pages, spam, and navigation-only pages. They call it much faster and cheaper than an
  LLM judge.
- **The post does not describe a request-relative judge** ("is this page useful *for this
  question*"). The nearest precedent it gives is pointwise reranking (query–document pairs scored
  independently). Putting the request alongside the page is our extension, not their documented
  use, so it needs its own evaluation (below).
- **The post gives no latency numbers**; it says there was no matched latency comparison. The
  low-latency claim for this project rests on our own bench, not on the article.

## Why this is cheap to build: it's Oracle's sibling

Reused as-is, so the new surface is small:

| Piece | Reuse |
|---|---|
| Client | `decisions.Client` (`AskChoice`), already on `tools.Context.Decisions`; multi-source `state` arrays already proven by `compare_sources` |
| Seam for tests | a one-method `AskChoice` interface like `gateway/oracle.go`'s `decisionsAskChoicer` |
| Own timeout, fail-open | exactly Oracle's rule: errors/timeouts/no client → no chip, no hint, turn unaffected |
| Money | `decisions_usage` ledger with a new `source` value, summed by the existing `decisionsMonthlyCapUSD` ($5/mo) check; cap hit → behaves as off |
| Stats | the per-source breakout pattern from issue #151 |
| Opt-in | a Settings toggle off by default, like `oracle_enabled`; ghost threads off unless opted in, like `oracle_ghost_enabled` |
| Model pin | `cfg.Decisions.Model` via `ForModel`, so a per-turn re-read stays race-free |

New: a `Bearing` field on `tools.Citation`, the request text on `tools.Context` (it has no field
for the user's message today), the judge call, the chip, and an eval track.

## Where it hooks in

| Hook | What the decider reads | Verdict | Cost of being wrong |
|---|---|---|---|
| **A. Search-result triage** | title + URL + snippet of each `web_search` hit | weak: snippets are the thing the model already reads | low, annotation only |
| **B. Post-fetch page judge** (`web_read`) | the extracted page text (≤12k chars ≈ 3k tokens) | strong: this is what decision models are good at, and it is evidence the chip can honestly show | low in v1, real in v2 trim |
| **C. Speculative prefetch** | fetch + judge the top-N hits *before* the model asks | would genuinely "point the model at a link" | fetch load on an SBC, wasted bandwidth, paywalled hits, more SSRF/blocklist surface |

**Build B first.** It is the only one with real signal and it is what the chip needs. A is a
cheap follow-on once B proves out. C is the thing the original idea reaches for ("point the model
even more toward a certain link over another") but it is a different cost profile; park it until
A and B have numbers.

## The questions (hook B)

One `AskChoice` call per fetched page. `state` is a two-entry source array — `request` (the
latest user message, the prior one for follow-ups, plus `web_read`'s own `instructions` argument
when set) and `page` (title + extracted text). Keeping them as separate sources avoids
delimiter-concatenation bleed, which the compare-tool spike confirmed.

| Check | Options | Used for |
|---|---|---|
| `usefulness` | `answers` / `partial` / `background` / `off_topic` / `unusable` | the chip, the nudge, and (v2) trimming |
| `wall` | `none` / `paywall` / `bot_wall` / `js_empty` / `error_page` | chip; later, a better trigger for the Tavily Extract fallback than the `looksLikePaywall`/`looksEmpty` regexes |
| `stale` | `yes` / `no` — is the page dated relative to what's being asked | chip; only meaningful for current-facts questions |

Three questions, not twenty: page-sized state is ~3k tokens, so the call is dominated by the page,
not the question count, and a small set is easy to label and evaluate. Option descriptions live in
`prompts.yaml` under a new block (hot-reloaded, compiled-in defaults, drift test), thresholds in
`config.yaml`, same split Oracle used.

Skip the call entirely when extraction produced nothing (the verification plan found empty state
degrades gracefully but wastes the call) and for PDFs' later pages.

## What the model sees

One short line appended after the page text, behind a preamble that frames it as a hint from a
classifier, not the user — the same defense Oracle's `## Oracle` section uses against a wrong
guess being obeyed:

> `[Bearing: this page covers the topic but doesn't answer the specific question (0.88). The
> answer is probably in another source.]`

Only emitted when the verdict clears its threshold. `answers` emits nothing (no noise on the happy
path). Never emitted for a read that passes `instructions` in v1 (the judge saw less text than the
model did; see Risks). Same hard rule as Oracle: **nudge only, never remove tools, never block a domain.**
`domain_rankings.yaml` and the blocklist stay operator-owned; verdicts never write to them. (A
later "block this domain?" offer chip is fine, since offers are never actions.)

## Cost

The honest framing: the decider call is a rounding error; the savings are the point, and they are
unmeasured. At Jev's $0.042/MTok input, a 3k-token page is about $0.00013. But model choice can
move that 5-20x on page-sized state (HeyGen bills 10.4k tokens for a prompt Perplexity bills at
3.9k; Reflex bills ~19x Jev's tokens), so cost per 1,000 pages is measured per candidate, not
inferred from the Oracle bench's per-turn figures.

Where savings would come from, in rough order:

1. **Tool results are re-sent on every later loop iteration.** A 3k-token junk page costs 3k
   tokens × the remaining iterations. Trimming a page the judge calls unusable is the biggest lever
   and the biggest risk.
2. **Skipping `web_read`'s filter pass.** When `instructions` is set (and not Quick Answer mode),
   `web_read` makes an extra LLM round-trip over up to 100k chars (`maxFilterInputChars`). Judging
   first and skipping that pass for an unusable page saves a whole call, not just tokens. **But the
   judge only sees the 12k-char display window, while that filter pass reads up to 100k chars**, so
   an `unusable` verdict would be skipping a pass over text the judge never read. The filter pass
   only ever runs when `instructions` is set, so there is no other read it could apply to:
   **skip-filter is dropped from v2 entirely** as designed. It comes back only if the judge is fed
   the same 100k slice the filter gets, which has its own cost and latency and is decided from
   phase 0/1 numbers, not assumed. Note that trimming (item 1) and the nudge (item 3) are also
   withheld for `instructions` reads in this design, so the savings from items 1 and 3 accrue only
   on reads without `instructions`; the phase-1/2 savings estimate must be computed on that
   subset, not on all `web_read` calls.
3. **Fewer wasted reads.** If the nudge steers the model off a dead end one iteration sooner, that
   is a saved loop iteration. Deep Research sub-agents read the most pages, so they would gain the
   most (see the dependency on issue #208 below).

**Phasing, so a false negative can never be the first thing we learn about:**

- **Shadow mode first.** Judge every page, log the verdict, show nothing, change nothing, and
  record what v2 *would* have trimmed and how many tokens that is. This is the dataset and the
  savings estimate.
- **v1 annotates** (chip + nudge). Nothing is removed. For reads that pass `instructions`, v1
  shows the chip but sends **no nudge** (see "Long pages" under Risks).
- **v2 trims**, only for `unusable`/`off_topic` at a high threshold, only when the shadow data
  shows a false-negative rate on genuinely useful pages low enough to accept. A trimmed result
  keeps the head of the page and says it was trimmed and why, so the model can ask for the rest.
  How it asks (a `full: true` argument, or re-reading with an `offset`) is an open question.
  Reads that pass `instructions` are excluded from v2 trimming too (and skip-filter is dropped, see
  item 2).

## Latency

Oracle's bench: Jev median 152ms, p95 189ms, every call inside Oracle's 2.5s timeout. **That is 23
short questions on a prompt, not three questions on a 3k-token page.** And Oracle's own earlier
live spike (`oracle-mode.md`, 2026-09-28; 50 messages sampled from the potato's thread history, run
location not recorded) measured p50 1.9s / p95 10s before the bench measured 152ms, and
`oracle-mode.md` still lists a measurement from the potato itself as outstanding. So the number
that matters here — the real potato, real page-sized state — is not known. Phase 0 measures it.

Design assumptions to check, not facts:

- The judge runs after the fetch and before the tool result is emitted, so it adds to each
  `web_read`'s latency. If Nova's tool registry dispatches calls concurrently the cost overlaps
  across parallel reads; verify rather than assume.
- Own short timeout (start near 1.5s, tune from data), far under `decisions.Client`'s 20s default.
- The judge never replaces the filter pass (skip-filter is dropped, see Cost item 2), so on a read
  with `instructions` it only ever adds a call in front of the filter pass. Phase 0 should measure
  that serial cost specifically, since those reads already pay for a fetch plus an LLM round-trip.

The alternative of emitting the tool result immediately and lighting the chip up when the verdict
lands avoids the added latency, but then the model never sees the nudge, which is half the value.
Inline with a tight timeout is the starting point.

## The model question: don't assume Oracle's ranking carries over

The Oracle bench ranks models on prompt classification. Page usefulness is a different task over
a different input size. Jev is the pinned incumbent and is the likeliest answer (speed), but the
candidates that mattered in the Oracle bench are worth a real comparison here:
`typesafe/jev-1.14`, `liquid/d1-ultra` (speed tie), `perplexity/pplx-decider-v1.3:free` (best free
build, 89.5 on Oracle; the article's own page-quality use is the closest to this task),
`nace-ai/drex-max` (accuracy). Perplexity Flash v1.3 scored lowest on `premise`, the family
nearest to "does this text support that", so it is not a first candidate.

This needs a new bench track, built the way `oraclebench/` already works: hand-labeled cases,
`expect`/`quiet`/`disputed_label`, a rationale per case, scoring weights. Cases are real pages from
saved threads on the potato (request, `web_read` text) labeled by hand for `usefulness`/`wall`/
`stale`, **including the hard negatives that decide whether trimming is safe**: a page that looks
off-topic but contains the one needed paragraph, a long page whose answer is past the first 3k
tokens, a forum thread with the answer in reply 14. Whether this lives as a new track under
`oraclebench/` or as a sibling package is an open question.

## Risks

- **False negative on a useful page** is the failure that matters, and the one v2 trim could turn
  from a wrong chip into a worse answer. Mitigations: asymmetric thresholds (a much higher bar to
  say "unusable" than to say "answers"), annotate-before-trim, shadow-mode evidence, and trimming
  that keeps the head and is reversible.
- **Prompt injection through the page.** Page text is untrusted input to the decider. Jev resisted
  embedded injection in every spike case (source-verification.md). Worst case for a hostile page in
  v1 is a misleading "answers" chip; it cannot gain anything from being *trimmed*. Still part of
  the eval set.
- **Long pages, and the `instructions` path.** State is bounded to the display window (≤12k chars)
  in v1, so an answer past the window is judged on the wrong text. For a read with no `instructions`
  the judge reads what the model reads (that window), so `usefulness` means "useful in this
  window" and the chip should say so for a paginated read. That does **not** hold when
  `instructions` is set: the model then gets filter output over up to 100k chars, which the judge
  never sees. The chip is still honest there (it says what the window covers), but the verdict must
  not be used to trim, and **must not be sent as a nudge**: the tool result in that case is filter
  output that may already contain the answer the judge couldn't see, and "this page doesn't address
  the question" appended to it would tell the model to distrust text it was just handed. That is
  the highest-impact false negative in v1 because it needs no shadow data to fire. Two ways to lift
  the restriction later, both phase 1 questions: judge the same 100k slice the filter reads, or
  judge the filter *output* itself (small, and exactly what the model reads; it gives up any
  skip-filter saving, which is already dropped).
- **Calibration shifts per model and per task.** Thresholds start as guesses and are set from the
  bench, never from the Oracle values.
- **Stats double-counting.** As with verification and Oracle, the judge's spend is counted once via
  `AddCost` into the turn and the ledger row is a breakout, never added to totals a second time.

## UI

Not designed yet; this is the constraint set. PRODUCT.md says calm over clever and night sky, not
tech neon, so no red/amber/green traffic light.

- A small star-like glyph on each source in `SourcesList` and on the `web_read` row in `ToolEvent`
  (filled = answers, half = partial/background, hollow = off-topic/unusable), with a plain-words
  label on tap ("Covers the topic, not the question"). A wall or stale page reads as a quiet
  secondary mark, not a failure state.
- The detail (probabilities, the exact nudge sent to the model) belongs in the existing ⓘ turn-info
  sheet's Oracle area, as another section.
- Tokens only: `--z-*`, `--radius-*`, `--space-*`. Reduced motion: no animation.
- Same-change obligations from CLAUDE.md: a `TERMS` entry in `HelpModal.svelte`, a line in
  `docs/FEATURES.md`, and `SETUP.md` if it adds a requirement (it shouldn't; it reuses the
  OpenRouter key).

## Dependencies and sequencing

- **#207** (move the Decisions client onto Nova's `decide` module) is in flight. Build against the
  small `AskChoice` interface so that migration doesn't have to touch this feature.
- **#208** (Deep Research sub-agents bypass provider routing; `newSubAgentContext` doesn't copy
  `Router`, `ModelID`, `Complete`) is the same class of bug this feature would hit: sub-agents read
  the most pages and need `Decisions` plus the request text copied across. Check that constructor
  when wiring, and don't ship the judge for sub-agents until it's covered by a test.
- **#190** (port tools to `tool.Result` and Nova's tool events): the nudge line is appended to a
  tool's string result today; if tools move to `tool.Result` first, the verdict becomes metadata on
  the result instead. Worth doing in whichever order lands second.
- **#179** (retire SearXNG as primary) changes result quality and therefore the base rate of junk
  pages; re-measure the shadow-mode junk rate after it lands.

## Phases

0. **Spike (no production code).** `curl` the Decisions API with 20-30 real pages and real
   requests through the potato, per this repo's live-verify culture. Measure p50/p95 on page-sized
   state, tokens billed per model, and whether the three questions behave. Exit: a latency and
   cost-per-1k-pages table for the four candidate models.
1. **Bench track.** Labeled pages including the hard negatives; run the four candidates; pick the
   model and set thresholds. Exit: a recommendation with intervals, same format as the Oracle
   bench, and a measured false-negative rate on `answers`.
2. **Shadow mode.** Judge in production behind a flag, log only, record would-have-trimmed tokens.
   Exit: a savings estimate from real turns and a false-negative rate on real pages.
3. **Chips + nudge (v1).** Citation field, prompts block, tool-result line, chip, ledger source,
   stats breakout, settings toggle, docs. Verify live through `dev/stack.sh --fake-llm` and
   Playwright, then on the potato. A/B answer quality with the pinned-provider benchmark harness
   (`cmd/benchmark.go`) so "does it help" isn't only a feeling.
4. **Trim (v2).** Only if phase 2's numbers justify it; reads with `instructions` excluded, and
   skip-filter dropped.
5. **Search-result triage (hook A)**, and revisit speculative prefetch (hook C) with real numbers.

## Open questions

- Name: "Bearing", or something that fits the existing night-sky vocabulary better?
- Does the judge run for Pulsar routines and Pulsar Daily blocks? Those are narrow, scheduled,
  and unattended, so a wrong trim hurts more and a chip helps nobody. Default off there, with a
  per-routine toggle later like #141 did for Oracle, is the conservative answer.
- How does the model ask for a trimmed page's full text (`full: true`, or an `offset` re-read)?
- Does the turn-info sheet's cost split get a fourth tier ("Sourcing"), or fold into Oracle's?
- Should `wall` replace the `looksLikePaywall`/`looksEmpty` regexes as the trigger for the
  Tavily Extract fallback? That spends a capped paid credit on a model's say-so, so it needs its
  own bench numbers and is deliberately not in v1.
- Bench home: new `oraclebench/` track, or a sibling package with its own case schema?
