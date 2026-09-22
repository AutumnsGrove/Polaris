# Source verification with Jev — investigation (not yet designed)

**Status: investigation only, nothing built.** No TypeSafe API key was available when this was
written, so every claim about Jev below comes from TypeSafe's own docs and launch post, not a live
spike. Per CLAUDE.md, spike-test it with `curl` before writing any code against it.

## The idea

Every inline citation chip (`web/src/lib/citations.ts`'s `renderInlineCitations`) is already a
claim↔source pair: the sentence the chip rides in, plus the URL it points at. After the answer
finishes, check each pair against the text we actually fetched from that URL, and mark the chip
when the source really says what the sentence claims. That fits PRODUCT.md's "sourcing is the
product": today a chip only proves the model *visited* a page, not that the page backs the claim.

## What Jev is

TypeSafe AI came out of stealth 2026-09-15. Jev is a "System One" model: not an LLM, it never
generates text. You send a `state` (text or JSON) plus a set of typed questions; it returns typed
answers with probabilities in a single parallel pass.

- `POST https://api.typesafe.ai/v1/systemone`, `Authorization: Bearer $TYPESAFE_API_KEY`, model
  `jev-latest`. Also on Cloudflare Workers AI as `typesafe/jev`. Plain JSON over HTTP, so no Go
  SDK is needed (TypeSafe only ships Python/JS SDKs).
- Three question types: **Noul** (probability that yes/no is yes), **Choice** (one of up to 255
  named options, with per-option probabilities + confidence), **Score** (ordered levels).
- Questions in one request are "evaluated in parallel and in isolation against the same state."
  That's the right shape for this: one source's text as the state, one question per claim that
  cites it.
- Claimed: 70–500 ms latency, $0.042/MTok input, output free. Cloudflare lists a **32k-token
  context window**. Text only. Early access.

**Skepticism warranted:**
- The headline "cannot hallucinate" only means *type-safe*: the answer is always one of the
  options you defined. Their own docs say "calibration is measured across groups of predictions;
  it does not guarantee that an individual answer is correct." Jev can still be confidently wrong
  about whether a page supports a claim.
- The benchmarks are self-reported, and some use an average of two frontier LLMs as the reference
  answer. TypeSafe concedes that its "193.6x faster, 444.6x cheaper" figures are "on the higher
  end."
- The company is days old. Treat it like the paid search tiers: optional, nil-client-safe, and a
  missing key or an outage just means no badges.

## Rough shape

1. **Keep the evidence.** Nothing keeps fetched page text past the tool call today;
   `tools.Citation` holds only title/URL/site/image. Add a per-turn `URL → evidence text` map on
   `tools.Context`, filled by `web_read` with the *raw* extracted text. Use the raw text, not
   `FilterExtractedText`'s LLM-filtered output: checking a claim against an LLM's own summary is
   circular. `web_search`-only citations have just a snippet. Either skip them or label them
   "snippet-checked" so the two don't look equally strong.
2. **Extract pairs** after `agent.Run` returns: walk the answer's markdown for `[text](url)` links
   whose URL is a tracked citation, and take the enclosing sentence as the claim. Do this in Go,
   mirroring the frontend's matching rule (tracked-URL links only, skip table cells).
3. **One Jev call per source, all sources in parallel.** Use the source text as the state and one
   **Choice** question per claim: `supported` / `partially_supported` / `contradicted` /
   `not_addressed`. Choice beats Noul here because "the page doesn't mention it" and "the page says
   the opposite" are different failures. Pages over ~32k tokens need chunking: pick the top chunks
   per claim with the existing `tools.Context.Embed` client, or fan out one question per chunk and
   take the max.
4. **Emit a `verification` WS event** after the answer, keyed by URL + claim offset, and persist it
   next to `messages.citations`. The frontend adds the mark to already-rendered chips, so the
   answer itself is never delayed.
5. **Gate on confidence.** Show the mark only when `choice == supported` and confidence is at or
   above a threshold tuned live. Everything else shows nothing. A `contradicted` result at high
   confidence might deserve a quiet warning, but only after live tuning, since a false "your
   source disagrees" is worse than no badge.

**Wording matters.** "Verified" suggests the claim is *true*. What this actually checks is "the
cited page says this," so something like "found in source" is more honest, and fits "calm over
clever."

## Cost / latency back-of-envelope

For a typical answer (about 8 chips across about 5 sources, ~4k tokens of evidence per source),
that's ~20k input tokens, roughly **$0.001 per answer** at the list price, running
post-answer in parallel. Small next to the answer's own LLM cost.

## Baseline to compare against

The same pipeline works today without Jev, using a cheap OpenRouter model as an LLM judge with a
forced tool-call verdict (the pattern `tools/finalize_daily_items.go` already uses). It's slower,
costs more, and has no calibrated probability. Build the plumbing (steps 1, 2, 4) provider-
agnostic, then A/B Jev vs. the LLM judge on real saved threads before deciding. The plumbing is
most of the work and is worth having either way.

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
