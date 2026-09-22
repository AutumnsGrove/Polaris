# Per-claim "found in source" badge

**Status: fully speced, live-spiked, not built. Ships after
[source-verification-compare-tool.md](source-verification-compare-tool.md)** — bigger surface
area: evidence map, claim extraction, a new WS event, a migration, async cost tracking, two
frontend spots.

See [source-verification.md](source-verification.md) for shared context: what Jev is, the API
shape (including the corrected `instructions`/`criteria` request format), and general skepticism
notes.

## The idea

After the answer finishes, check each inline citation's claim↔source pair against the text
actually fetched from that URL, and mark the chip when the source really says what the sentence
claims. Today a chip only proves the model *visited* a page, not that the page backs the claim.

**Wording matters.** "Verified" suggests the claim is *true*. What this actually checks is "the
cited page says this," so something like "found in source" is more honest, and fits "calm over
clever."

## Live spike results relevant to this feature (2026-09-22)

Single-source support/contradiction testing, against real fetched text (a live Anthropic
announcement page, a live raw Wikipedia fetch — genuinely messy real extraction, citation brackets
and all, not a cleaned-up fixture):

- Plain true/false claims: correct every time, confidence 1.0.
- An off-by-one-day date claim: caught, `contradicted`.
- "Sonnet 5 outperforms Opus 4.8" against source text saying "close to Opus 4.8, but at lower
  prices": correctly `contradicted` — not just pattern-matching on "Opus 4.8" being present.
- "Webb's mirror is 2.7x Hubble's diameter, giving it 2.7x the resolution" against source text
  explaining resolution is actually *comparable* despite the size difference: correctly
  `contradicted` — real reasoning over an explained relationship, not keyword matching.
- A negated claim ("Sonnet 5 is *not* safer than Sonnet 4.6") against source text saying it *is*
  safer: correctly flipped to `contradicted`.
- A compound claim, half-true/half-false: `contradicted` at confidence 0.63, not 1.0.
- A genuinely ambiguous paraphrase ("roughly as capable as" vs. source's "close to"):
  `partially_supported` at confidence **0.35**, probabilities split 48/51 between `supported` and
  `partially_supported` — the one case that should have been uncertain, and was.
- 8 fan-out claims against one source in a single call resolved correctly, including all of the
  above nuance, in 419ms for $0.0000876 total.
- **Near-empty/empty state** (scanned-PDF stand-in — see "PDF handling" below): clean
  `not_addressed` at confidence 1.0, not an error.
- **Non-English source text**: see overview doc's round-2 results — genuine cross-lingual
  reasoning confirmed, not literal matching.

Confidence genuinely varies with difficulty (0.35–1.0 observed), which is what makes
confidence-gating below a real filter rather than a no-op.

## Rough shape

1. **Keep the evidence.** Nothing keeps fetched page text past the tool call today;
   `tools.Citation` holds only title/URL/site/image. Add a per-turn `URL → evidence text` map on
   `tools.Context`, filled by `web_read` with the *raw* extracted text (shared prerequisite with
   `compare_sources` — build once). Use raw text, not `FilterExtractedText`'s LLM-filtered output:
   checking a claim against an LLM's own summary is circular. `web_search`-only citations have just
   a snippet — either skip them or label them "snippet-checked" so the two don't look equally
   strong.
2. **Extract pairs** after `agent.Run` returns: walk the answer's markdown for `[text](url)` links
   whose URL is a tracked citation, and take the enclosing sentence as the claim. Do this in Go,
   mirroring the frontend's matching rule (tracked-URL links only, skip table cells).
3. **One Jev call per source, all sources in parallel.** Source text as `state`, one **Choice**
   question per claim: `supported` / `partially_supported` / `contradicted` / `not_addressed`
   (`instructions`/`criteria` shape — see overview doc's correction). Choice beats Noul here
   because "the page doesn't mention it" and "the page says the opposite" are different failures.
   Pages near/over the 32k-token window get a clean `400 max_tokens_exceeded`, not silent
   truncation — real code needs to pre-check token count and chunk before calling. See "Chunking
   design" and "PDF handling" below.
4. **Emit a `verification` WS event** after the answer, keyed by URL + claim offset, and persist it
   next to `messages.citations`. The frontend adds the mark to already-rendered chips, so the
   answer itself is never delayed. See "Cost tracking" below for how the async cost lands on the
   message row after the fact.
5. **Gate on confidence ≥ 0.85** (decided default — every "easy" case in live testing hit 1.0, and
   the one genuinely-ambiguous case landed at 0.35, so 0.85 comfortably excludes ambiguous/
   compound claims while passing clean matches; tune from real usage once live). Show the mark only
   when `choice == supported` at/above threshold. Everything else shows nothing. A `contradicted`
   result at high confidence might deserve a quiet warning, but only after live tuning — a false
   "your source disagrees" is worse than no badge.

## Chunking design

Most single pages won't actually need this. `web_read`'s own existing caps
(`tools/web_read.go`) put realistic evidence text well under Jev's 32k-token window before any
chunking logic runs at all: `maxExtractedChars` (12,000 chars, ~3k tokens) is what the model sees
per page window, and `maxFilterInputChars` (100,000 chars, ~25k tokens) is the largest raw slice
any existing LLM pass already gets handed. If verification evidence reuses a similarly-sized
bound, chunking only matters for genuinely long pages (long Wikipedia articles, doc sites,
multi-page reads) or PDFs — see below.

The ~18k/~40k-token pass/fail boundary measured live used repetitive filler text (~7 chars/token),
denser than real English (~4 chars/token) — don't reuse that char count as a budget; re-derive it
from a conservative chars-per-token estimate (round down, e.g. 3.5) and target ~24–26k tokens, not
the full 32k, to leave headroom for the fan-out questions' own instructions/criteria overhead
(measured live: ~1.3k tokens added by 8 questions' worth of instructions on top of the source
text).

1. **Size-check before ever calling Jev.** Estimate `(evidence text) + (all pending claims'
   instructions/criteria for that source)` against the budget above.
2. **Below budget → one call, all of that source's claims as parallel Choice questions.** Proven
   live: 8 claims, 419ms, ~$0.00009.
3. **Above budget → split into chunks, not claims.** Jev takes one `state` per call, so a chunk
   means a separate call — but each call still carries *all* of that source's pending claims as
   parallel questions. For a source needing K chunks and M claims, that's K calls (parallelizable —
   40-way concurrent burst showed no throttling), not K×M.
   - **Chunk boundaries: split on paragraph breaks**, not fixed offsets. Target ~6–8k tokens per
     chunk with a few hundred tokens of overlap at each boundary.
   - **Chunk selection: lexical/keyword overlap by default, not embeddings.** `tools.Context.Embed`
     (`embed/embed.go`) is explicitly scoped narrow in its own doc comment and `nil` whenever
     Ollama isn't configured — making chunk selection depend on it would silently degrade
     verification on those deployments. Score each chunk by word/n-gram overlap with the claim
     text instead (no dependency, always available); `Embed`, when configured, is a fine optional
     upgrade, never a requirement.
4. **Reduce per-claim across chunk results.** Most chunks of a long page will legitimately say
   `not_addressed` — that's expected. If any chunk returns `supported` at/above threshold, that's
   the claim's answer. Else if any chunk returns `contradicted` above threshold, use that. Else
   `not_addressed`. If two chunks disagree at high confidence, show no badge at all rather than
   guess which chunk wins.

## PDF handling

PDFs are the easier case, not the harder one — `web_read` already extracts them per-page
(`tools/web_read.go`'s `ExtractPDFPage`/`pdfPageText`) with the same `maxExtractedChars` cap
applied per page — each page is already a natural, pre-made chunk.

- **Verify only the pages the model actually read, not the whole PDF.** Evidence for a citation is
  the union of pages actually read during the turn, stored as `URL → []pageText`. Checking against
  pages the model never consulted would answer a different question ("does *anything* in this PDF
  support the claim") than the one this feature answers ("did the page the model used support what
  it said") — and it bounds cost automatically (a 300-page PDF where only 3 pages were read only
  ever produces 3 chunks' worth of calls).
  - Very long PDFs need no special-case chunking logic — reuse the "one call per chunk, all
    pending claims for that source" pattern above, with "page" standing in for "chunk."
- **Scanned/image-only PDFs.** No OCR anywhere in this codebase (`web_read.go`'s PDF path is pure
  text extraction) — a scanned page returns empty or near-empty text. **Resolved live (round 2):**
  a near-empty or fully-empty `state` returns a clean `not_addressed` at confidence 1.0, not an
  error or garbage answer. Given that, the implementation choice is still to **skip the Jev call
  entirely** when extracted text is empty/near-empty (saves the cost — the answer would be
  `not_addressed` anyway) rather than rely on Jev's graceful handling as the primary safeguard.
- **A PDF citation with no page anchor at all** (bare PDF URL, multiple pages read across the
  turn) — evidence is the concatenated set of pages actually read for that URL this turn, chunked
  like a long HTML page.

## Cost tracking — this needs to be exact, not estimated

`usage.cost` off the real Jev response is exact for every call — no estimation step, unlike
token-based LLM cost math elsewhere in this codebase.

1. **Per-call, log it immediately.** `logEvent(..., "cost_usd": <usage.cost>)` the moment the
   response comes back, before any aggregation — an audit trail even if the goroutine driving
   verification dies partway through a multi-chunk source. Mirrors how `gateway/turn.go` already
   logs `cost_usd` for title generation and compaction.
2. **Land on the message it belongs to.** Verification is deliberately async/post-answer (never
   delays the visible answer), so the message row's `cost_usd` is already written by `AddMessage`
   by the time verification finishes. New store method: `AddMessageCost(msgID int64, usd float64)
   error` doing `UPDATE messages SET cost_usd = cost_usd + ? WHERE id = ?`. No-op, not an error, if
   the message was deleted in the meantime (check rows-affected, don't fail loudly over a race
   that just means nobody's looking at the cost anymore).
3. **Monthly dollar cap, not a call-count cap** — `api_usage` only tracks calendar-month call
   counts (right for Brave/Parallel/Tavily's call-count-based free tiers, wrong for token-billed
   Jev). Shared table with `compare_sources` (one Jev cost ledger):
   ```sql
   CREATE TABLE IF NOT EXISTS api_cost_usage (
       provider TEXT NOT NULL,
       month TEXT NOT NULL,
       cost_usd REAL NOT NULL DEFAULT 0,
       PRIMARY KEY (provider, month)
   );
   ```
   `IncrementAPICostUsage(provider string, usd float64) (float64, error)` (upsert, same
   `strftime('%Y-%m', 'now')` pattern as `IncrementAPIUsage`) and `APICostUsageThisMonth(provider
   string) (float64, error)`, checked before firing calls for a new source. **Append this as a new
   migration at the end of `store.go`'s migration list** — migrations here are positional/
   append-only, not content-matched, so it must not be inserted anywhere else.
4. **Per-turn cap: $0.01/turn** (decided, shared value with `compare_sources` — see that doc's
   cost-tracking section) — roughly 10–100x the ~$0.0001–0.001/turn observed live for a typical
   badge pass. Track a running total across the goroutine driving one turn's verification and stop
   issuing further Jev calls once crossed; sources/claims past that point just get no badge, same
   graceful-degradation story as a missing key or an outage.
5. **Both caps fail closed.** Hitting either cap behaves exactly like `Brave`/`Parallel` being
   `nil` today — skip verification for what's left, log a `warn`, never error the turn or block
   the answer that's already been shown.
6. **Verification cost breakout (decided, 2026-09-22 — user request, shared with
   `compare_sources`).** Bundling Jev spend into Polaris/Pulsar's existing totals (via
   `AddMessageCost` above) is fine, but it should also be visible on its own. This is a
   transparency breakout, not a second additive bucket — the same `Stats.VerificationCostUSD
   SourceCost` field designed in `source-verification-compare-tool.md`'s cost-tracking section
   covers both features. Log each verification call (or each chunk call, for a chunked source) as
   an `events` row — `source: "verification"`, `message: "jev call finished"`, `cost_usd` in the
   JSON data blob — read back in `GetStats` the same way `SearchProviderCounts`/
   `CodeExecWallTimeMS` already unmarshal small per-row JSON blobs. Must not also be added to
   `Stats.TotalCostUSD`/`PeriodCostUSD` — that money is already counted once, via step 2's
   `AddMessageCost` landing on `messages.cost_usd`.

## UI affordances

**What this gets us:** a quiet, factual qualifier on citations that already exist, in the same two
places they already render. Decided icon: **lucide's `check-check`** (`@lucide/svelte`, already a
dependency) — two overlapping checkmarks, distinct from a single-check "sent"/"done" glyph
elsewhere, colored with the existing `--color-accent-2` (already the app's "citation chrome /
informational" hue — reuses an existing semantic rather than introducing a new color). A mockup
(light + dark, real `app.css` token values, badge-on/off toggle for comparison) was published to
compare placement: https://claude.ai/artifact/Nfjf2d1Zaf7T5m9SEK5rqD

**Two places it attaches, both already in `ChatTurnView.svelte`:**
- **The inline citation chip** (`web/src/lib/citations.ts`'s `renderInlineCitations`) — most
  precise placement, tied to one specific claim. `check-check` glyph inline before the chip's
  label text, same `--color-accent-2`, sized to sit inside the existing
  `.prose :global(.citation-chip)` pill (11.5px text) without changing chip height.
- **The source-list chip** (`.source-chip` in the collapsible "N Sources" footer) — aggregate view,
  since one source can back several claims with mixed verdicts. Glyph inline right after
  `.source-title`, same treatment.

**Verdict → visual, deliberately asymmetric:**
- `supported` at/above 0.85 confidence → the one visible positive mark. Ship this first — the safe
  direction (a missed badge costs nothing, a wrongly-shown one costs trust).
- `contradicted`, `partially_supported`, `not_addressed`, or below-threshold `supported` → **no
  mark, indistinguishable from "not yet checked."** Deliberate: a `contradicted` mark is much more
  editorially loaded than a `supported` one (it's telling the user their own model may have gotten
  something wrong). Worth a v2 once the `supported` path has been live for a while and the
  false-positive rate is actually known, not before.
- Because "no mark" covers three different real states, the "Sources" toggle header needs a small
  explanatory affordance — a tooltip or info glyph next to the count — saying roughly "a mark means
  this specific claim was checked against its source," so absence never gets read as "this source
  is bad."

**Motion/timing:** verification finishes after the answer is already shown (0.4–0.8s single-call,
more for a chunked source, never blocking). No loading spinner or pending state on the source chip
while in flight — a badge that quietly appears once ready is calmer than one that visibly ticks
through a "checking..." state for something this fast.

## Cost / latency back-of-envelope

For a typical answer (~8 chips across ~5 sources, ~4k tokens of evidence per source), that's ~20k
input tokens, roughly **$0.001 per answer** at list price, running post-answer in parallel — small
next to the answer's own LLM cost. This tracks the live spike: 8 fan-out claims against one
~2k-token real source cost $0.0000876 and took 419ms; a single call stays comfortably under a
second even with 6–8 questions batched; multiple sources can run concurrently (40-way concurrency
showed no throttling in testing).

## Settings panel

`Stats.VerificationCostUSD` (see compare-tool doc's cost-tracking section) needs a 4th row in
`SettingsPanel.svelte`'s "Cost by source" block (lines ~148-174), same `usage-stat-row` markup as
the existing Polaris/Pulsar/Daily rows, labeled "Verification" — reading
`usage.verification_cost_usd.period_cost_usd`/`.total_cost_usd` (a sibling field on the JSON
response, not nested under `cost_by_source`, matching the Go struct shape). Same shared CSS as
`ConstellationUsageModal.svelte`, so add it there too if that panel should show it.

## Not tested / open until built

- Real production latency/cost at Polaris's actual per-turn citation volume — needs measurement
  from real saved threads once built, not more synthetic spiking.
- Confidence threshold (0.85) is a starting default, not a final value — tune from real usage.
