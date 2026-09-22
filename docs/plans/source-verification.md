# Source verification with Jev — overview

**Status: investigation + open questions resolved, live-spiked twice (2026-09-22). Not built.**
This is now an umbrella doc: shared API context and general findings live here; the two concrete
features each have their own focused plan, since they ship independently:

- **[source-verification-compare-tool.md](source-verification-compare-tool.md)** — `compare_sources`,
  a mid-turn tool letting the model check whether two of its own cited sources actually agree.
  Small, self-contained, **builds first**: no schema/WS/frontend changes, reuses the existing
  `ctx.AddCost` mechanism.
- **[source-verification-badge.md](source-verification-badge.md)** — a post-answer "found in
  source" mark on citation chips. Bigger: evidence map, claim extraction, a new WS event, a
  migration, async cost tracking, two frontend spots. Builds second.

## The idea

Every inline citation chip (`web/src/lib/citations.ts`'s `renderInlineCitations`) is already a
claim↔source pair: the sentence the chip rides in, plus the URL it points at. Checking that pair
against the text actually fetched from that URL — rather than just proving the model *visited* a
page — fits PRODUCT.md's "sourcing is the product."

## What Jev is

TypeSafe AI came out of stealth 2026-09-15. Jev is a "System One" model: not an LLM, it never
generates text. You send a `state` (text or JSON) plus a set of typed questions; it returns typed
answers with probabilities in a single parallel pass.

- `POST https://api.typesafe.ai/v1/systemone`, `Authorization: Bearer $TYPESAFE_API_KEY`, model
  `jev-latest`. Also on Cloudflare Workers AI as `typesafe/jev`. Plain JSON over HTTP, so no Go
  SDK is needed (TypeSafe only ships Python/JS SDKs).
- **It's also on OpenRouter, in beta** (`typesafe/jev-1.13`, `typesafe/jev-latest`) — this is the
  path that matters for Polaris. `compose/polaris/config.yaml.example` already wires an
  `openrouter.api_key`/`base_url` for the chat model, so this reuses that exact same key instead
  of adding a second secret/env var/Docker passthrough line. **Confirmed live**: `POST
  https://openrouter.ai/api/v1/systemone` with `Authorization: Bearer $OPENROUTER_API_KEY` works,
  and accepts `model: "jev-latest"` (also `"jev-1.13"`, `"typesafe/jev-1.13"` — but not
  `"typesafe/jev-latest"` or bare `"jev"`, both 400 "does not exist"). Requests are a System
  One-shaped JSON call, not `/chat/completions` — this needs its own small HTTP client, not
  `llm.ChatClient`.
- Three question types: **Noul** (probability that yes/no is yes), **Choice** (one of up to 255
  named options, with per-option probabilities + confidence), **Score** (ordered levels).
- Questions in one request are "evaluated in parallel and in isolation against the same state."
- Claimed: 70–500 ms latency, $0.042/MTok input, output free. Cloudflare lists a **32k-token
  context window**. Text only. Early access.

### Corrected request shape for a `choice` question (found in follow-up spike, 2026-09-22)

Earlier prose in this doc described a `choice` question loosely as `question` + `options: [...]`.
That shape 400s. The real schema, confirmed live:

```json
{
  "model": "jev-latest",
  "state": "<source text>",
  "questions": {
    "q1": {
      "type": "choice",
      "instructions": "Does the source support the claim that X?",
      "criteria": {
        "supported": "The source confirms this claim",
        "partially_supported": "The source partially confirms this",
        "contradicted": "The source contradicts this claim",
        "not_addressed": "The source does not address this at all"
      }
    }
  }
}
```

`criteria`'s keys *are* the option set — there's no separate `options` array. Any implementation
(both features below) must use `instructions` + `criteria`, not `question` + `options`.

## Live spike results (2026-09-22, two rounds)

**Round 1** (original investigation) — correctness, injection resistance, and failure-mode testing
against real fetched text. Full detail lives in each feature doc's own "what was tested" section,
since the two features' correctness cases differ (single-source support vs. cross-source
agreement). Headline findings, true for both:
- Confidence genuinely varies with difficulty (0.35–1.0 observed), not pinned to 1.0 — makes
  confidence-gating a real filter, not a no-op.
- Prompt-injection embedded in scraped source text was resisted every time it was tried.
- Failure modes are clean structured 400s, not vague 500s. Context limit is a hard `400
  max_tokens_exceeded`, not silent truncation — real chunking logic is required for long pages.

**Round 2** (resolving what round 1 left open):
- **Near-empty/whitespace `state`** (simulating a scanned PDF page with no OCR text extracted) →
  clean `not_addressed` at confidence 1.0. No error, no garbage answer. Same for a fully empty
  string. Safer than assumed — still worth skipping the call entirely when extraction produced
  nothing (saves the cost), but a near-empty page sneaking through degrades gracefully rather than
  misbehaving.
- **Non-English source text**: a Spanish-language announcement, checked against English-language
  claims — correctly `supported` on the claim it backed ($50M Series B by Sequoia) and correctly
  `contradicted` on a claim naming the wrong lead investor (Andreessen Horowitz vs. the source's
  actual Sequoia Capital), both confidence 1.0. Genuine cross-lingual reasoning, not literal
  string matching.
- **Sustained burst**: 40-way concurrent calls (double the original 20-way test), all 200s, 2
  seconds total. Still no throttling observed at this volume. A single test key over a short
  window — a real sustained ceiling over hours/days still isn't known.

**Still open, deliberately unresolved (needs live turns post-build, not more API spiking):**
- Real production latency/cost at Polaris's actual per-turn citation volume — round 1's "5 sources
  × ~8 claims" was simulated, not measured from a real saved thread.
- Whether `/v1/systemone` and `/api/alpha/decisions` are the same backend long-term (they matched
  in one spike; not something worth re-testing until it matters).
- `score` question behavior — unused by either current design, not tested.
- For the compare-tool: whether the model reliably reaches for `compare_sources` on its own — a
  prompting question that only live turns against real research questions can answer.

**Skepticism warranted:**
- The headline "cannot hallucinate" only means *type-safe*: the answer is always one of the
  options you defined. TypeSafe's own docs: "calibration is measured across groups of
  predictions; it does not guarantee that an individual answer is correct."
- Benchmarks are self-reported; TypeSafe concedes its "193.6x faster, 444.6x cheaper" figures are
  "on the higher end."
- The company is days old. Treat it like the paid search tiers: optional, nil-client-safe, and a
  missing key or an outage just means no badges/no comparisons — never a broken turn.

## Baseline to compare against

The same pipeline works today without Jev, using a cheap OpenRouter model as an LLM judge with a
forced tool-call verdict (the pattern `tools/finalize_daily_items.go` already uses). It's slower,
costs more, and has no calibrated probability. Build the plumbing provider-agnostic, then A/B Jev
vs. the LLM judge on real saved threads before deciding. After two rounds of live spiking, Jev is
the one worth building against first: sub-second, well under a tenth of a cent per answer, correct
on every tested case including reasoning-level distinctions a naive keyword-overlap check would
miss. Worth keeping the LLM judge as a fallback for when Jev/OpenRouter is unavailable, not as the
primary path.

## Other places Jev might fit (unexplored)

- `web_read`'s paywall/looks-empty heuristic chain → a Noul over the extracted text.
- Pulsar Daily's "is this Watch block unchanged since yesterday" pass.
- Routing a message to quick mode vs. normal vs. deep research before the turn starts.
- Scoring search results for junk/SEO spam before they reach the model.

## Sources

- https://docs.typesafe.ai/llms.txt (docs index, including quickstart, primitives, confidence and
  system-one pages)
- https://typesafe.ai/blog/introducing-system-one-models-and-jev
- https://developers.cloudflare.com/ai/models/typesafe/jev/
- Live testing against `https://openrouter.ai/api/v1/systemone`, 2026-09-22 (two rounds, two
  different temporary keys, neither stored anywhere) — see each feature doc for its own detailed
  test matrix.
