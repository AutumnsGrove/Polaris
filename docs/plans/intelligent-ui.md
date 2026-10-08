# Intelligent UI: streamed, declarative `ui` blocks inside the answer

**Added: 2026-10-08.**

**Status: draft, mid-design — nothing built.** Prompted by OpenAI's "GPT-6 and Intelligent UI
for everyone" launch (2026-10-07). Open questions are listed at the bottom; resolve them before
treating any section as final. No code lands from this doc until the plan is refined and an issue
exists.

## The idea, in one paragraph

Polaris answers are Markdown prose plus a few tool-driven rich surfaces (`show`, `highlight`,
cards, weather's chart). The model has no way to say "this answer is really a comparison / a
stepper / a timeline" and have the client lay it out as one. Add a fenced ```` ```ui ```` block the
model writes **inside its answer text**, interleaved with prose, in a line-oriented format the
frontend renders as it streams. Same trick `mermaid` already uses (`web/src/lib/markdown.ts`),
generalized from "one diagram language" to "a small component vocabulary." No new tool, no new
protocol, no new storage.

## What the reference product does (and what we take from it)

OpenAI describes: a library of native, streamable components; a compiler that processes the
interface *as the model generates it* so it appears progressively; and a model trained to decide
when a visual helps and when plain text is enough. They also let users dial visuals down. They
ship interactive diagrams, side-by-side comparisons, checklists, maps, charts, and a separate
"build me a tool" feature.

Taken: the component library + progressive compile + "plain text is a valid answer" + a user dial.
**Explicitly not taken: "build me a tool"** (calculators, bill splitters, games). Out of scope, per
the operator, and independently consistent with `docs/plans/artifacts.md`'s rejection of runnable
app artifacts.

Also separable and cheap: their "interleaved thinking and answering" (first answer, keep working,
refine). Polaris already has a `commentary` timeline event (`web/src/lib/turnEvents.ts`) that
positions early prose among tool calls — a prompt-level nudge, independent of the UI work.

## Why fenced blocks, not tools

Verified against the code:

- `llm/client.go` buffers a tool call's `arguments` completely before dispatch. A "UI tool" can
  therefore never fill in progressively without first building argument streaming end to end.
- Answer text already streams token-by-token (`turn.content += e.content`), is saved as the
  message, and survives reload/history for free. A fence inherits all of that.
- The renderer already has the pattern: `markdown.ts` emits a `data-mermaid` marker for a
  `mermaid` fence and `mermaid.ts` does a DOM pass. `ui` is the same shape with a richer renderer.
- Works with every model on OpenRouter; no tool-calling capability required.

Tools stay what they are: data-fetching and artifact-producing (`show`, `highlight`,
`nearby_search`, weather, `code_exec`). A `ui` block *presents*; it does not fetch.

## Format: line-oriented, so every prefix is valid

One complete component per line (JSONL, or a tiny indented DSL — decide in refinement). Half-
streamed JSON is hard to render; a stream of whole lines is trivial: each completed line becomes a
component, and the trailing partial line is held back until its newline arrives. That is the whole
"compiler." Unknown component or invalid line → that line (or the whole block) degrades to a plain
code block. Never throws, never blanks the answer.

Sketch only (not a final grammar):

```ui
{"c":"callout","tone":"note","text":"Ripe in Sept–Oct","cite":[2]}
{"c":"compare","cols":["A","B"],"rows":[["Price","$9","$12","cite:3"]]}
{"c":"steps","items":["Rest the lamb","Crisp the potatoes"]}
```

## Component vocabulary (starter, grow only when earned)

callout · stat · comparison table · tabs · stepper/checklist · timeline · simple line/bar chart
from inline numbers · map (reusing the interactive-maps work) · action chips.

Any component may carry citation refs that bind to the existing numbered sources
(`web/src/lib/citations.ts`). This is the differentiator against a chatbot: components are
*sourced*, not decorative — consistent with PRODUCT.md's "sourcing is the product."

## Interaction model (no code execution, no `eval`)

- **Local, declarative state only:** tab selection, checklist ticks, hotspot selection. Never
  persisted server-side, never model-driven.
- **Action chips** send text as the user's next ordinary message — the same pattern as
  `ask_user_question` (`tools/ask_user_question.go`), no live round trip.
- Computed/rescaling widgets (slider drives a derived value) are **out of scope** — see
  Decisions; they're the nearest neighbour to the excluded "build a tool" feature.

## Safety and failure

- DOMPurify still runs over everything (`ChatTurnView.svelte`); component props are plain data,
  rendered by Svelte components — model output never becomes markup or script.
- Images come only from this turn's tool results, referenced by index (see Decisions), so the
  blocklist check `show` already does applies and the model never supplies a URL.
- Plain text must remain a first-class answer; the prompt says so explicitly.

## Settings and prompts

A "visuals" dial (off / low / normal) in Settings, injected via `prompts.yaml` (hot-reloaded, with
`buildDefaults()` kept in sync — a drift test enforces it). Default: Low.

## Risks

- **Design judgment.** OpenAI trained theirs; we prompt ours. Mitigation: tiny vocabulary,
  few-shot examples, a clear "when text is better" rule. Over-eager UI is the likeliest failure.
- **Token cost and latency** on a phone over Tailscale; keep component syntax terse.
- **Stream edge cases:** a fence cut off by a cancelled/errored turn must still render sanely.
- **Mobile layout** is the primary target (CLAUDE.md); desktop is secondary.

## Verification plan (per this repo's culture)

Script streamed `ui` blocks through `dev/fakeopenrouter` and watch progressive fill in the real
SvelteKit app with Playwright; then run a handful of real prompts through `/api/ask` on a dev
backend. Unit-test the line parser against truncated, malformed, and unknown-component streams.

## Phasing (tentative)

1. Parser + renderer + 3–4 components (callout, comparison, stepper, stat), prompt fragment,
   Settings dial.
2. Tabs, timeline, chart, action chips.
3. Citations bound into components; map component; commentary-style early answers.

Shipping also requires: a `HelpModal.svelte` `TERMS` entry and a `docs/FEATURES.md` line
(CLAUDE.md).

## Decisions (2026-10-08, operator Q&A)

1. **Interactivity: static + local toggles only.** Tabs, checklist ticks, hotspot selection, action
   chips. No computed/rescaling values and no expression language — that keeps this clear of the
   excluded "build a tool" territory. Revisit only if real usage shows a concrete need.
2. **Visuals dial defaults to Low** at launch: a component only when it clearly beats prose.
3. **Surfaces: chat only.** Pulsar, Pulsar Daily and Atlas have separate layouts and LLM paths;
   each would be its own follow-up.
4. **Images: tool-sourced only.** Components reference `image_search`/`highlight` results by index
   (as `show` does); no arbitrary model-supplied URLs.

## Open questions

1. JSONL vs. an indented DSL for the line format (token cost vs. robustness to model typos).
2. Exact component vocabulary and field names for phase 1.
3. Whether a cancelled/errored turn leaves a half-rendered block or collapses it to a code block.
