# Intelligent UI: streamed, declarative `ui` blocks inside the answer

**Added: 2026-10-08.**

**Status: draft, mid-design — nothing built.** Prompted by OpenAI's "GPT-6 and Intelligent UI
for everyone" launch (2026-10-07). Open questions are listed at the bottom; resolve them before
treating any section as final. No code lands from this doc until the plan is refined and an issue
exists.

**Update 2026-10-09:** mockups done (`mockups/intelligent-ui.html` — every block rendered at phone
width next to the fence lines that produce it, plus a replayable streaming demo). Two new sections
below came out of that round: "Block catalog" (the concrete vocabulary) and "Oracle integration"
(how Oracle decides *whether* and *which block*, rather than relying on the prompt alone).

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

callout · stat · comparison · tabs · stepper/checklist · timeline · flow · claim · quote ·
disclose · choose · facts · map (reusing the interactive-maps work). See "Block catalog" for the
current list; **charts and action chips were cut on 2026-10-09** (Decisions).

Any component may carry citation refs that bind to the existing numbered sources
(`web/src/lib/citations.ts`). This is the differentiator against a chatbot: components are
*sourced*, not decorative — consistent with PRODUCT.md's "sourcing is the product."

## Block catalog (proposed, from the mockups)

Concrete form of the "starter vocabulary" above. Syntax is JSONL, one line per completed piece. A
**container line** (`"c":...`) opens a block; the **child lines** that follow (`"row"`, `"i"`,
`"when"`, `"tab"`, `"n"`/`"e"`, `"+"`/`"-"`) append to it until the next container line or the
closing fence. That is what lets a table fill row by row as it streams. Flat blocks (`callout`,
`stat`, `quote`) are a single line. Free-text fields accept `**bold**` and `` `code` `` only. Any
block may carry `"cite":[n]`, bound to the existing numbered sources.

| Block | Shape | Phone layout (mockup pick) | Oracle option? | Phase |
|---|---|---|---|---|
| `callout` | 1 line | **B** icon chip on a card (decided); tones `note`/`warn`/`ok` and `answer` (bottom line up front, always with an `asof` date) | no — model's own accent | 1 |
| `stat` | 1 line | one big number. The 3-up strip is superseded by `facts` | no | 1 |
| `compare` | container + `row` lines | **B** stacked option cards, pick ribbon; table (A) when ≥4 attributes and ≤3 options | yes | 1 |
| `steps` | container + `i` lines (`d` detail, `t` duration) | **A** numbered rail; `t` upgrades to **B** chips only when durations exist | yes | 1 |
| `checklist` | container + `i` lines | ticks are local state, progress bar | yes | 2 |
| `timeline` | container + `when`/`i` lines | vertical rail, date above the text (decided; the ledger is dropped, and the rail is the one that copes with long dates like "14 Mar – 2 Apr 2005") | yes | 2 |
| `tabs` | container + `tab` lines | segmented bar, local selection | yes | 2 |
| `procon` | container + `+`/`-` lines | two columns, +/– symbols so it isn't colour-only | yes | 2 |
| `flow` | container + `n` (node) / `e` (edge) lines | vertical-first cards, tap to expand, decision branches side by side | yes | 3 |
| `claim` | container + `+`/`-` lines (`verdict`: true/mixed/misleading/false/unverified) | verdict pill, quoted claim, Supports / Disputes sections with sources | no — rides `claim_check` | 2 |
| `quote` | 1 line | serif pull-quote; "found in source" badge supplied by verification, never by the model | no | 2 |
| `disclose` | container + `p` lines | native `<details>` rows, title + read-time hint | no — rides `depth`=thorough | 2 |
| `choose` | container + `if`/`then` lines | "If …" rows with a → pick pill: decision rules instead of a facts table | no — rides `task`=decide | 2 |
| `facts` | container + `k`/`v` lines | titled at-a-glance card, label/value rows with sources | no — rides `intent` | 2 |
| `map` | — | reuse interactive-maps; not mocked | later | 3 |

Layout is the client's decision, never the model's: the model supplies data and (for `compare`) a
`pick`; the renderer chooses cards vs table by viewport and shape. This keeps the model's job small
and lets phone layouts improve without changing any prompt.

**Cut: `chips`.** Polaris already shows LLM-generated follow-up suggestions under every reply, so
chips inside the answer would duplicate them. **Cut: `chart`.** `code_exec` produces better charts
than any renderer we'd hand-build and tune (an earlier custom visualization tool became a waste of
time once it landed), so numbers-as-a-picture stays a code-execution job. Keep the vocabulary to
blocks that present *structure*.

**New blocks and Oracle.** `claim`, `choose`, `facts` and `disclose` deliberately add **no new Jev
question**: each rides an existing check (`claim_check`, `task`, `intent`, `depth`), whose nudge
simply gains the block's syntax line. Only the "this answer is really a ___" shapes need the new
`ui` check. `quote` is the model's own accent. **Trust marks are never model-written:** the "found in
source" badge on `quote` and the verdict's evidence come from the verification Polaris already runs,
so a block can't claim a check it didn't get.

### Streaming is the point

The mockup's replayable demo (`mockups/intelligent-ui.html`, section 5) is the most important
result: components build up as the model writes them, one completed line at a time, with the partial
line held back. That is the whole compiler. In the flow scenario nodes appear as their lines arrive
and edges attach once both ends exist; a node whose edge hasn't arrived yet is shown as a dotted
"waiting for its edge" card rather than hidden.

### "Mermaid Plus": `flow`, not a mermaid replacement

Mermaid stays — sequence, ER, gantt and anything graph-shaped it already does. The real gap is the
commonest case, a **process or decision chain**, where mermaid's output is a desktop-shaped graph
shrunk until its text is ~5px on a phone, nodes can't hold a sentence of detail, and a source can't
attach to a node. `flow` fixes exactly that: vertical-first, each node a tappable card that expands
to detail and can carry a citation, branches rendered side by side. Scope guard: ≤ ~8 nodes; anything
bigger or any other graph type stays mermaid. Not an attempt to re-implement mermaid's layout engine.

### Streaming mermaid too (spiked 2026-10-09)

Today a diagram renders only once the turn ends: `ChatTurnView.svelte` gates `renderMermaidIn` on
`!turn.streaming`. Its own comment says why: an *unclosed* fence is briefly a half-diagram, and parsing
that would flash a failure note. That is a reason to avoid rendering an unclosed fence, not to wait
for the whole turn, and it doesn't need anything from mermaid itself.

Spike (real mermaid 11.12 in headless Chromium, `mermaid.parse` + `mermaid.render` at each
complete-line prefix): flowchart 9/9 prefixes valid, sequence 8/8, gantt 8/8, ER 5/8 (invalid only
inside an open `{ … }` attribute block). Render time median ~29 ms, max ~52 ms for the flowchart on a
desktop-class machine; **phone speed is unmeasured.** So the approach that needs no library change:

1. While streaming, on each *new complete line* of a mermaid fence, `mermaid.parse` the prefix.
2. If it parses, render it (latest-wins, one render in flight) and swap it in with a short fade.
3. If it doesn't, keep the last good render and wait for the next line.
4. On the closing fence, do the final render exactly as today (including `autoQuoteLabels` /
   `ensureStyleContrast`, which must also run on every prefix).

Two caveats the demo makes visible: nodes **move** as dagre re-lays the graph out (a fade softens it,
nothing more), and a plain mermaid graph is tall on a phone, which is a further reason `flow` exists.
Worth building independently of the `ui` work; it's a change to `markdown.ts` / `mermaid.ts` /
`ChatTurnView.svelte`. Needs a way to tell a closed fence from an open one while streaming (the
rendered DOM can't: marked emits both as a code block).

## Interaction model (no code execution, no `eval`)

- **Local, declarative state only:** tab selection, checklist ticks, hotspot selection. Never
  persisted server-side, never model-driven.
- ~~Action chips~~ — cut 2026-10-09; the follow-up suggestions under every reply already do this.
- Computed/rescaling widgets (slider drives a derived value) are **out of scope** — see
  Decisions; they're the nearest neighbour to the excluded "build a tool" feature.

## Safety and failure

- DOMPurify still runs over everything (`ChatTurnView.svelte`); component props are plain data,
  rendered by Svelte components — model output never becomes markup or script.
- Images come only from this turn's tool results, referenced by index (see Decisions), so the
  blocklist check `show` already does applies and the model never supplies a URL.
- Plain text must remain a first-class answer; the prompt says so explicitly.

## Oracle integration (proposed 2026-10-09)

A prompt-only approach leaves "when is a block worth it" entirely to the main model, which is the
exact weakness the Risks section names (over-eager *or* never-used UI). Oracle already solves the
same shape of problem for answer format, sources and tool choice, in the same single Jev call. So:
**add one more question, `ui`, to `oracle.checks`.** It answers "would a structured block beat prose
here, and which one?" and, when it fires, injects a short nudge into `## Oracle` naming the block
and giving that block's exact syntax as a one-line example.

### Two layers, so Oracle failing never breaks the feature

1. **Base prompt fragment** (`prompts.yaml`, present whenever the visuals dial isn't Off): the
   compact grammar for every block, plus "plain text is a valid answer; use a block only when it
   clearly beats prose." This is the floor. It's what runs when Oracle is off, when Jev times out
   (the 2.5s `oracleTimeout` — 14% hard-failure rate in the original spike), or when `ui` doesn't
   fire.
2. **Oracle nudge** (only when `ui` fires): "This looks like a comparison. A `compare` block fits —
   write one `ui` fence, then say which to pick and why." plus the exemplar line(s). This is the
   "extra nudge" — just-in-time few-shot for the specific block, instead of hoping the model
   remembers the grammar from the top of a long system prompt (the same recency argument that put
   `## Oracle` into `modeReinforcement`).

Open measurement: once the nudge carries the exemplar, the base fragment could shrink to names +
one-liners and save tokens on the ~90% of turns where `ui` won't fire. Don't do that until the
fallback rate with Oracle off has been measured — a smaller floor means a worse Oracle-off case.

### The check

```yaml
# prompts.yaml, oracle.checks (+ the same text in buildDefaults()/defaults_oracle.go —
# the drift test enforces it; wording only here, bars live in config.yaml)
ui:
  instructions: >-
    Would a structured visual block serve this message clearly better than ordinary prose?
    Pick "none" unless the answer is really a comparison, procedure, timeline, or
    similar and prose would be harder to scan.
  options:
    none: Prose, a short list, or code serves this best.
    compare: Choosing between specific options across shared attributes.
    steps: A procedure where order matters.
    checklist: Things to prepare or tick off.
    timeline: Events over time, a history, or a schedule.
    flow: A process or decision chain with branches.
    procon: One thing weighed for and against.
    tabs: Parallel versions of one answer (per OS, per option).
  inject:
    compare: >-
      The user is choosing between options. A `compare` block fits: write one ui fence, e.g.
      {"c":"compare","cols":["A","B"],"pick":0} then {"row":"Price","v":["$9","$12"]} lines, and
      follow it with which to pick and what would change that. Keep the prose short.
    # ...one entry per option, each with a one-line exemplar
```

```go
// config/oracle.go DefaultOracle() — policy
"ui": {Threshold: 0.70, SkipForFocus: []string{"safari", "brief"}, Suppresses: []string{"format"}},
// and add "ui" to emotional's Suppresses list
```

Design calls, each deliberate:

- **Separate from `format`, but it holds `format` back.** `format` already nudges toward
  table/comparison/steps/timeline as *Markdown* shapes and works with visuals Off, so it stays. When
  `ui` fires, `Suppresses: ["format"]` stops both nudges stacking ("use a table" + "write a compare
  block") — the nudge-stacking failure `oracle-checks-expansion.md` already hit and fixed for
  `emotional`. A held-back `format` shows as "held back" in the ⓘ sheet for free. With the dial Off the
  `ui` check isn't asked at all and `format` behaves exactly as today.
- **Oracle picks the block, never a layout.** Options name *what the answer is* (`compare`,
  `steps`...), not how it looks; the client chooses cards vs table by viewport.
- **Callout / stat / quote aren't options**, and neither are `claim`, `choose`, `facts`, `disclose`
  (they ride existing checks, see the catalog). Callout, stat and quote are small accents the model
  may add under the base prompt. The `ui` check only decides the "this answer is really a ___" cases, where a wrong default hurts most.
- **One block per answer by default.** Jev returns a single winner, and the nudge says "one fence".
  The model may still add small accents on its own; multi-block answers are not forbidden, just not
  encouraged.
- **Skips.** Brief (a few sentences — a block defeats it) and Safari (own pacing). `emotional`
  suppresses it: someone distressed gets acknowledgment, not a comparison widget.
  `high_stakes` does **not** suppress it, but the high-stakes nudge ("name the caveat that changes
  what the user should do") must survive: a compare block is not a licence to drop caveats into
  cells. Check this in the live spike.
- **The dial sets Oracle's bar.** Off: fragment removed, `ui` not asked. Low (default): the check
  fires at a high bar (tentatively 0.85). Normal: the config bar (0.70). Config holds the Normal
  bar; Low adds a fixed offset in code. The spike sets the real numbers.
- **Escape hatch.** The ⓘ card for `ui` gets a "Rerun as plain text" button (mocked), the visuals
  analogue of the existing rerun. A wrong firing costs one tap, not a bad answer.
- **No schema change.** `OracleResult.Checks` is generic, so persistence and the WS event carry
  `ui` for free. Frontend work is: a label/wording entry for the margin note, a star in the
  constellation (the animation already scales by check count), and the rerun button.

### Verification (extends the plan below)

Before wiring the nudge text, run `dev/oracle_spike`-style classification of ~50 real thread-openers
plus a hand-written set of true positives (comparisons, how-tos, timelines, number-heavy answers) and
true negatives (chatty, opinion, one-fact lookups). The number that matters is **false-positive
rate**, not accuracy: an unneeded block is worse than a missed one. Then run the live loop through
`dev/fakeopenrouter` with Oracle on to confirm the nudge actually appears in the request body
(`/_control/calls`), and compare real-model output with and without the nudge on the same prompts.

## Settings and prompts

A "visuals" dial (off / low / normal) in Settings, injected via `prompts.yaml` (hot-reloaded, with
`buildDefaults()` kept in sync — a drift test enforces it). Default: Low. The dial also sets the `ui`
check's bar when Oracle is on (see "Oracle integration").

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

1. Parser + renderer + 4 components (callout, compare, steps, stat), base prompt fragment, Settings
   dial. Ship this **without** Oracle first so the floor can be measured on its own.
2. The Oracle `ui` check (limited to the options phase 1 can render), margin-note/ⓘ-sheet wording,
   "rerun as plain text". Then checklist, timeline, tabs, procon, and the check-riding blocks (`claim`, `choose`, `facts`, `disclose`, `quote`).
3. `flow`; streaming mermaid (independent, could ship any time); citations bound into every block; map; commentary-style early answers.

Shipping also requires: a `HelpModal.svelte` `TERMS` entry and a `docs/FEATURES.md` line
(CLAUDE.md).

## Decisions (2026-10-08, operator Q&A)

1. **Interactivity: static + local toggles only.** Tabs, checklist ticks, hotspot selection. No computed/rescaling values and no expression language — that keeps this clear of the
   excluded "build a tool" territory. Revisit only if real usage shows a concrete need.
2. **Visuals dial defaults to Low** at launch: a component only when it clearly beats prose.
3. **Surfaces: chat only.** Pulsar, Pulsar Daily and Atlas have separate layouts and LLM paths;
   each would be its own follow-up.
4. **Images: tool-sourced only.** Components reference `image_search`/`highlight` results by index
   (as `show` does); no arbitrary model-supplied URLs.

## Open questions

1. JSONL vs. an indented DSL for the line format (token cost vs. robustness to model typos).
   **Leaning JSONL**, now with container + child lines (see "Block catalog"): every line parses on
   its own, models rarely mangle JSON, and the mockup's streaming demo shows malformed/unknown lines
   degrade to a muted code row without touching their neighbours. Measure token cost on the real
   exemplars before committing; a DSL only wins if the saving is large.
2. Field names above are a first draft from the mockups; settle them with the phase-1 four blocks.
3. Whether a cancelled/errored turn leaves a half-rendered block or collapses it to a code block.
   The mockup assumes the already-complete lines stay rendered and only the partial line is dropped.
4. Does the nudge-with-exemplar let the base fragment shrink, and by how much? Needs the Oracle-off
   fallback measured first.
5. Does `ui` need its own margin-note clause, or is the existing "Read as ..." note enough? The
   mockup shows the clause version; judge in real use like the rest of Oracle's wording.

## Decisions (2026-10-09, operator review of the mockups)

1. **Chips: cut.** The automatic follow-up suggestions under every thread already cover it.
2. **Charts: cut.** `code_exec` was added to make charts, and they come out far better than a
   hand-built visualization suite; the earlier custom visualization tool was a waste once code
   execution existed. No `chart` block, no Oracle `chart` option.
3. **Timeline: the vertical rail.** The ledger overlapped it and is dropped. (The rail also handles
   long dates better than the ledger did, contrary to what the first mockup note claimed.)
4. **Callout: option B** (icon chip on a card). Option A read like a generic markdown renderer.
5. **`flow`: in.** More interactive than mermaid and sits naturally in prose.
6. **Streaming render is the headline feature.** It also motivates streaming mermaid (spiked above);
   mermaid's own library can't render mid-line, so the plan is render-per-complete-line, not a fork.
