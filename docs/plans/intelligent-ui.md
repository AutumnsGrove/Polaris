# Intelligent UI: streamed, declarative `ui` blocks inside the answer

**Added: 2026-10-08. Rewritten as a build plan: 2026-10-09.**

**Status: P0 built (2026-10-09), P1 onward not started.** Tracking issue #159. P0 = `uiBlocks/split.ts`,
`renderAnswer.ts`, `MermaidBlock.svelte`, `mountMermaidStream` in `mermaid.ts`. Live-verified in
Chromium against real mermaid via `dev/fakeopenrouter` (new `-chunk-delay` flag): a warm diagram grows
0 -> 3 -> 4 -> 5 nodes as its fence streams, a broken fence falls back to source + note, a list-nested
fence still renders through the old DOM pass. Findings worth keeping: (a) a *cold* `import('mermaid')`
takes about as long as a short fence takes to stream, so the first diagram of a session jumps straight
to its final form (latest-wins) and only later ones visibly grow; (b) `fakeopenrouter` plain-FIFO is
consumed by title/follow-up calls too, so pin the scripted answer with `"match":"Today's date"`;
(c) phone render cost is still unmeasured. Prompted by OpenAI's "GPT-6 and Intelligent UI for
everyone" launch (2026-10-07). Mockups: `mockups/intelligent-ui.html` (every block rendered at phone
width next to the fence lines that produce it, replayable streaming demos for the line compiler and
for mermaid). Operator review of those mockups is recorded in "Decisions". No code lands until an
issue exists; "Phases" below is the slicing for it.

## The idea

Polaris answers are Markdown prose plus a few tool-driven rich surfaces (`show`, `highlight`, cards,
weather's chart). The model has no way to say "this answer is really a comparison / a procedure / a
decision chain" and have the client lay it out as one. Add a fenced ```` ```ui ```` block the model
writes **inside its answer text**, interleaved with prose, in a line-oriented format the frontend
renders **as it streams**. Same trick `mermaid` already uses, generalized from one diagram language
to a small component vocabulary. No new tool, no new protocol, no new storage.

The headline property is progressive rendering: each completed line becomes a component the moment
its newline arrives, so a comparison fills row by row and a flow chart grows node by node. That is
what makes it feel native rather than bolted on, and it is why the format is line-oriented (below).

What we take from the reference product: a component library, progressive compile, "plain text is a
valid answer", and a user dial. **Not taken:** "build me a tool" (calculators, games, bill
splitters). Out of scope, and consistent with `docs/plans/artifacts.md`'s rejection of runnable app
artifacts.

## Decisions

2026-10-08 (operator Q&A):

1. **Interactivity: static + local toggles only.** Tabs, checklist ticks, flow node expand, disclose.
   No computed/rescaling values, no expression language: that keeps it clear of "build a tool".
2. **Visuals dial defaults to Low:** a block only when it clearly beats prose.
3. **Surfaces: chat only.** Pulsar, Pulsar Daily and Atlas have separate layouts and LLM paths; each
   would be its own follow-up.
4. **Images: tool-sourced only**, referenced by index like `show` does; never a model-supplied URL.

2026-10-09 (review of the mockups):

5. **Chips: cut.** The automatic follow-up suggestions under every reply already cover it.
6. **Charts: cut.** `code_exec` exists to make charts and they come out far better than a hand-built
   renderer; an earlier custom visualization tool became a waste once it landed. Numbers-as-a-picture
   stays a code-execution job. No `chart` block, no Oracle option for it.
7. **Timeline: the vertical rail**, date above the text. The ledger overlapped it and is dropped. (The
   rail is also the one that copes with long dates such as "14 Mar – 2 Apr 2005"; the first mockup note
   had this backwards.)
8. **Callout: option B** (icon chip on a card). Option A read like a generic markdown renderer.
9. **`flow`: in.** More interactive than mermaid, and it sits naturally in prose.
10. **Streaming render is the headline feature**, and it extends to mermaid (below): keep the last
    good render visible while newer ones are computed, so a bad prefix never blanks the diagram.
11. **Oracle gets one new check, `ui`, with every shape block as an option.** Not one check per block,
    and not blocks riding other checks (see "Oracle").

2026-10-09 (planning Q&A, second round):

12. **`compare`: cards on phones (under ~600px), table when wide.**
13. **`steps`: the numbered rail.** A duration chip appears only when the model supplies `t`.
14. **History stays verbatim.** Older turns' `ui` blocks are *not* flattened when the thread is sent
    back to the model: editing history would break prompt caching (`cache_control` is sent for
    Anthropic models), which costs more than the tokens saved. The flattener is for display-side and
    non-model consumers only (copy, read-aloud, search_chats, Weaver, titles, verification).
15. **Cut-off turn: keep complete lines, drop the partial.** A comparison cut off after row 3 shows
    rows 1-3 as a normal block.
16. **Margin note gets a `ui` clause** ("Read as comparison · shown as a compare block"); wording to be
    judged in real use like the rest of Oracle's notes.
17. **`claim` verdict pill: pill only, no "Polaris's read" caption.** Accepted trade-off: the pill is
    the model's read, not a verified result, and a reader could take it as one. The mitigation is that
    only the evidence lines carry "found in source" ticks, so the pill never has one.
18. **`quote` verification: exact-match first, Jev on a miss** (see "Sourcing and verification").

## Why fenced blocks, not tools

Verified against the code:

- `llm/client.go` buffers a tool call's `arguments` completely before dispatch. A "UI tool" can
  therefore never fill in progressively without first building argument streaming end to end.
- Answer text already streams token by token (`turn.content += e.content`), is saved as the message,
  and survives reload and history for free. A fence inherits all of that.
- The renderer already has the pattern: `markdown.ts` emits a `data-mermaid` marker for a `mermaid`
  fence and `mermaid.ts` does a DOM pass. `ui` is the same idea with a richer renderer.
- Works with every model on OpenRouter; no tool-calling capability required.

Tools stay what they are: data-fetching and artifact-producing. A `ui` block *presents*; it never
fetches.

## Grammar

A fence whose info string is exactly `ui`, starting at column 0 (an indented fence inside a list item
stays an ordinary code block; the model is told to put `ui` fences at top level). Inside it, **one
JSON object per line**; blank lines ignored.

- A line with a `"c"` key is a **container line**: it opens a block and closes the previous one.
  Flat blocks (`callout`, `stat`, `quote`) are a container line with no children.
- A line with no `"c"` key is a **child line** of the current container, interpreted by that
  container's schema. A child line that fits no schema, or arrives before any container, becomes a
  muted raw row; it never closes the container and never affects its neighbours.
- A line that isn't valid JSON, or whose `"c"` is unknown, becomes a muted raw row (the mockup's
  "unrenderable line" / "unknown component" rows). Never throws, never blanks the answer.
- The compiler: split the fence body on `\n`; every line before the last `\n` is complete and
  parseable; the trailing partial line is held back (shown as a shimmer at most) until its newline.
  Re-parsing the whole body on every content update is fine (blocks are small; the cost is bounded by
  the caps below), as long as rendering is keyed so existing components update in place.
- **Cancelled or errored turn:** complete lines stay rendered, the partial line is dropped. (Proposed
  default; the mockup assumes it.)
- **Caps** (tentative, enforced by the parser, beyond them a line degrades to a raw row): 8 `ui`
  fences per answer, 40 lines per fence, 400 characters per text field.
- **Text fields** (`text`, `d`, `i`, `v`, ...) accept the same inline Markdown subset as prose:
  `**bold**`, `` `code` ``, and `[Title](URL)` links. Never HTML; DOMPurify still runs.

### Citations

Polaris citations are not numbered refs: the model writes inline `[Title](URL)` and
`renderInlineCitations` (`web/src/lib/citations.ts`) turns a link whose URL is one of the turn's
tracked citations into a named source chip. Blocks reuse exactly that:

- A link inside any text field renders as the same chip (same lookup, same unknown-URL fallback to an
  ordinary link).
- Any container or child line may also carry `"src":["https://..."]` for a source with no natural
  place in the text; it renders as chips on that row or block. Unknown URL: plain domain link.
- **The mockup's numbered pills are stand-ins** for those chips. Do not build `"cite":[n]`.

## Block catalog

Container line fields, child line fields, phone layout, limits. "Oracle" says whether the block is an
option of the `ui` check (primary shapes only; accents are the model's own call under the base prompt).

| Block | Container line | Child lines | Layout (mockup) | Oracle |
|---|---|---|---|---|
| `callout` | `tone` note/warn/ok/answer, `text`, `asof` (`YYYY-MM`, answer only), `src` | none | **B** icon chip on a card; `answer` is a bottom-line card that always shows its as-of date | accent |
| `stat` | `label`, `value`, `note`, `src` | none | one big number (the 3-up strip is superseded by `facts`) | accent |
| `compare` | `cols` (2–4), `pick` (index, optional) | `{"row","v":[...per col],"src"}` ≤ 12 | cards on phones with a Pick ribbon; table (sticky first column, side scroll) wider. Wrong-length `v` is padded/truncated; out-of-range `pick` ignored | option |
| `choose` | `title` | `{"if","then","src"}` | "If …" rows with a → pick pill; the follow-up to `compare` when the honest answer is "it depends" | option |
| `steps` | `title` | `{"i","d","t"}` ≤ 15 (`d` detail, `t` duration) | numbered rail; duration chips only when `t` is present | option |
| `checklist` | `title` | `{"i"}` ≤ 20 | ticks are local state, progress bar | option |
| `timeline` | none | `{"when","i","src"}` ≤ 15 | vertical rail, date above text | option |
| `flow` | none | `{"n":id,"t","d","kind":"decision","src"}` and `{"e":[from,to],"l"}` ≤ 10 nodes | vertical-first cards, tap to expand, branches side by side; see below | option |
| `procon` | `pro_h`, `con_h` | `{"+"}` / `{"-"}` | two columns with +/– symbols (never colour alone) | option |
| `tabs` | none | `{"tab","text"}` ≤ 6 | segmented bar, local selection | option |
| `claim` | `text`, `verdict` true/mixed/misleading/false/unverified | `{"+","src"}` / `{"-","src"}` | verdict pill, serif quoted claim, Supports / Disputes sections | option |
| `facts` | `title`, `sub` | `{"k","v","src"}` ≤ 12 | titled at-a-glance card; replaces the stat strip | option |
| `disclose` | `title`, `hint` | `{"p"}` | native `<details>` row, no JS | accent |
| `quote` | `text`, `by`, `src` | none | serif pull-quote | accent |

`map` stays a later idea (reuse the interactive-maps work); not mocked.

**Layout is the client's decision, never the model's.** The model supplies data and (for `compare`) a
`pick`; the renderer chooses cards vs table by viewport. That keeps the model's job small and lets
phone layouts improve without touching a prompt.

### `flow` (the "Mermaid Plus" block)

Mermaid stays (sequence, ER, gantt, anything graph-shaped it already does). The gap is the commonest
case, a process or decision chain, where mermaid's output is a desktop-shaped graph shrunk to ~5px
text on a phone, nodes can't hold a sentence of detail, and a source can't attach to a node. `flow`
fixes that. Concrete rules from the mockup's renderer:

- Layout is BFS from the first node: each layer of one node is a card, a layer of several is a side-by-
  side branch row, edge labels sit above the branch they lead to.
- Nodes render the moment their line arrives. A node with no incoming edge yet is shown dotted as
  "waiting for its edge" below the chain, then slots in when its edge arrives.
- A **back-edge** (an edge to an already-placed node, like "Not yet → back to the check") is not drawn
  as a line; the source node shows a small "↩ back to <title>" note. Cycles are legal, never recursed.
- Scope guard: ≤ ~8 nodes in practice (hard cap 10); bigger or any other graph type stays mermaid.

## Architecture (frontend)

Today the answer is one string: `marked` → DOMPurify → `renderInlineCitations`, injected as a single
`{@html renderedHtml}` into `div.prose` (`ChatTurnView.svelte`), re-set on every token, with mermaid
as a post-render DOM pass gated on `!turn.streaming`. Svelte components can't live inside `{@html}`
(`mermaid.ts` says so itself), and re-setting the prose HTML each token would wipe a block's local
state (ticked boxes, expanded nodes). So:

**Split the answer into segments; render each kind with the right tool.**

1. `web/src/lib/uiBlocks/split.ts`: `splitContent(content, streaming) → Segment[]` where a segment is
   `{kind:'md', text}`, `{kind:'ui', src, closed}` or `{kind:'mermaid', src, closed}`. Pure, no DOM.
   Recognizes column-0 fences only; `closed` is true once a closing fence line exists. Also gives the
   mermaid work the closed-vs-open signal the DOM can't (marked emits both as a code block).
2. `web/src/lib/uiBlocks/parse.ts`: `parseUi(src) → UiBlock[]`, a pure function returning a
   discriminated union (types in `types.ts`). All grammar and caps live here, none in components.
3. `web/src/lib/components/ui/`: one small Svelte component per block, plus `UiBlocks.svelte` which
   `{#each}`es the parsed blocks **keyed by index** so streaming updates in place, and `UiText.svelte`
   (inline markdown subset → DOMPurify → chips via the existing citation lookup).
4. `ChatTurnView.svelte`: replace the single `{@html}` with `{#each segments}`: `md` → today's pipeline
   unchanged (sanitize, citations), `ui` → `<UiBlocks>`, `mermaid` → `<MermaidBlock>`. Side benefit:
   finished segments stop re-parsing every token; only the last one changes.
5. **Gotcha: verification marks.** `renderInlineCitations` matches a "found in source" mark by
   *(url, nth occurrence in document order)* across the whole answer, and the server computes the same
   index in `extractClaims` (`gateway/verification.go`). Per-segment rendering must thread a running
   per-URL counter through the `md` segments, and **the client and server must agree on whether `ui`
   links count**, or a URL cited in a block and again in prose gets its ticks on the wrong chips. P1:
   neither counts (the server strips `ui` fences before extraction, the client counter skips `ui`
   links). The "Sourcing and verification" section turns both on together.
6. **Consumers of message text** must not show raw JSON lines. Add a flattener, TS
   (`uiBlocks/flatten.ts`) and Go (`gateway/uiblocks`), turning each block into readable text
   (compare → "Moka pot: …" lines, steps → numbered lines, ...). Use it for: copy buttons
   (`ChatTurnView` copies `turn.content` at two sites), read-aloud (`/api/speak`), `search_chats`
   indexing (`store/message_search.go`), Weaver, and thread titles. One shared fixture file
   (`testdata/ui_flatten.json`) is read by both the TS and Go tests so the two can't drift. Whether
   *history sent back to the model* stays verbatim (Decision 14: prompt caching).

### Streaming mermaid (spiked 2026-10-09)

Today a diagram renders only once the turn ends. The gate's own comment gives the real reason: an
*unclosed* fence is briefly a half-diagram, and parsing it flashes a failure note. That is a reason
not to render an **open** fence, not to wait for the whole turn.

Spike (real mermaid 11.12 in headless Chromium, `parse` + `render` at each complete-line prefix):
flowchart 9/9 prefixes valid, sequence 8/8, gantt 8/8, ER 5/8 (invalid only inside an open `{ … }`
attribute block). Render median ~29 ms, max ~52 ms (flowchart, desktop-class). **Phone speed is
unmeasured**; measure on the potato's client device before shipping.

Design (the demo in section 6 of the mockups runs exactly this):

1. A `mermaid` segment from `splitContent`, rendered by a new `MermaidBlock.svelte` that owns a
   stable DOM node (so a render isn't wiped by the next token). `mermaid.ts`'s per-diagram mount
   (toolbar, source toggle, lightbox, error note) is extracted from `renderMermaidIn` so the component
   can call it; the standalone DOM pass stays for any non-streamed path.
2. While streaming, on each **new complete line**: `mermaid.parse(prefix)`. Valid → render, latest-wins
   with at most one render in flight, swap in with a short fade (none under reduced motion).
   Invalid → **keep the last good render**, wait for the next line. A bad prefix never blanks the diagram.
3. `autoQuoteLabels` and `ensureStyleContrast` (existing repairs) run on every prefix, not just the final.
4. Throttle by cost: wait at least ~3× the last render's duration between renders, so a slow phone
   renders fewer intermediate frames instead of falling behind.
5. On the closing fence: final render exactly as today.

Known cost: nodes move as dagre re-lays the graph out; the fade softens it, nothing more. A plain
mermaid graph is also tall on a phone, which is a further reason `flow` exists.

## Sourcing and verification (how `quote`'s badge works)

Found in the code, and it removes the open item from the last draft: Polaris already verifies
claims against their sources, with Jev (the same backend model as Oracle), in
`gateway/verification.go`. How it works today:

- **After the turn**, in a detached goroutine (`gateway/turn_followups.go`), `runVerification` takes
  the raw answer markdown, finds every `[text](url)` link whose URL is a tracked citation, and takes
  the enclosing sentence plus two lead-up sentences as that link's **claim** (links in table rows are
  skipped: there the link text is the data).
- Evidence is `tools.Context.EvidenceForURL(url)`: the **raw extracted text of every page `web_read`
  fetched this turn**, deliberately not the LLM-filtered summary (checking a claim against a summary
  would be circular). A source the model only saw as a search snippet has no stored evidence and gets
  no badge.
- One `AskChoice` per source (chunked and relevance-selected when over Jev's context budget): "Does
  the source support the claim: ...", options `supported` / `partially_supported` / `contradicted` /
  `not_addressed`, each with a confidence. Only **supported at confidence >= 0.85** (and not also
  contradicted) becomes a mark; a missed badge costs nothing, a wrong one costs trust.
- Marks `{url, claim_index, choice, confidence}` are persisted (`SetMessageVerification`) and sent as
  a `verification` WS event; `claim_index` is the nth occurrence of that URL among the answer's
  citation links, which the frontend matches onto the nth chip. Cost lands in the turn's
  `verification` cost tier and counts against the shared $0.01/turn and $5/month Jev caps.
- `compare_sources` is the mid-turn sibling (does the model's own set of sources agree on a fact),
  using the same Jev + evidence machinery.

So blocks get "found in source" by feeding this same system, not by inventing another:

1. **Make the server see block text as claims.** A Go flattener (`gateway/uiblocks.Flatten`, the same
   one the other consumers use) turns a `ui` fence into plain sentences **with its `[Title](URL)`
   links preserved**, and `runVerification` runs `extractClaims` over the flattened answer instead of
   the raw one. Every sourced line then becomes a claim with no change to extraction itself: a
   `quote` becomes `"<text>" [by](url).`, each `claim` `+`/`-` line a sentence with its link, each
   `facts` row `Key: value [Title](url).`
2. **Canonical link order.** Flatten and the renderer must emit links in the same order or
   `claim_index` points at the wrong chip. JSON key order is the model's whim, so both go by the
   **schema order in the catalog table** (container fields, then child lines in arrival order, fields
   within a line in schema order), never by key order. The shared fixture (`testdata/ui_flatten.json`)
   asserts the expected link order too, read by both the Go and TS tests.
3. **Client counter.** The running per-URL counter threads through `md` and `ui` segments in order;
   `UiText` links participate in it exactly as prose links do, so a tick on a chip inside a block is
   the same mark mechanism as one in prose.
4. **`quote` gets a stricter question.** A quote is a stronger promise than "supports this claim", so
   `claim` gains a `kind` and quote claims ask "Does the source contain this passage, verbatim or
   near-verbatim?" instead of "support the claim". **Decided (18): exact match first.** A
   normalized (case, whitespace, punctuation, ellipsis) substring match against the evidence marks the
   quote supported at confidence 1.0 with no Jev call, and an exact match can't be hallucinated; only
   a miss falls through to Jev for near-verbatim cases.
5. **What the model writes vs what is verified.** `quote`'s badge and every tick on a `claim` block's
   Supports/Disputes lines come from this pipeline; a `claim` block's own `verdict` pill is the
   model's read of the evidence, **not** verified, and never carries a tick (Decision 17: no caption).
   Trust marks are never model-written.
6. **Cost.** Blocks add claims (a `facts` card with ten sourced rows is ten claims). Same budget caps
   apply and already fail safe (no badge once a cap is hit), but cap claims per turn (tentatively 20)
   so one block-heavy answer can't spend the whole turn budget before prose gets checked.
7. **Timing.** Marks arrive after the turn, so badges appear a moment after the block does, exactly
   as chip ticks do today. Nothing about streaming changes.

## Oracle

**One new check, `ui`, in the same single Jev call, with every primary shape as an option.** Jev
answers a whole question map in parallel against the same state, so adding a question costs
tokens, not latency.

This reverses a version of this section from earlier today that had `claim`/`choose`/`facts`/`disclose`
"ride" `claim_check`, `task`, `intent` and `depth` so they'd add no question. Rejected on reflection:
it spreads dial gating over four checks, makes nudges stack (`task`=decide plus `ui`=compare would
both fire), and still needs engine changes to append a syntax line conditionally. One check has one
winner, one dial gate, one suppression rule, one place to tune.

```yaml
# prompts.yaml, oracle.checks (wording only; thresholds live in config.yaml).
# The same text goes in prompts/defaults_oracle.go; the drift test enforces it.
ui:
  instructions: >-
    Would a structured visual block serve this message clearly better than ordinary prose?
    Pick "none" unless the answer is really one of these shapes and prose would be harder to scan.
  options:
    none: Prose, a short list, or code serves this best.
    compare: Choosing between specific options across shared attributes.
    choose: The right pick depends on the person's situation; decision rules help more than a table.
    steps: A procedure where order matters.
    checklist: Things to prepare or tick off.
    timeline: Events over time, a history, or a schedule.
    flow: A process or decision chain with branches.
    procon: One thing weighed for and against.
    tabs: Parallel versions of one answer (per OS, per option).
    claim: Checking whether a specific claim holds up, with evidence on both sides.
    facts: An at-a-glance summary of one named thing (a product, place, person, organization).
  inject:
    compare: >-
      The user is choosing between options. A `compare` block fits: write one ui fence, e.g.
      {"c":"compare","cols":["A","B"],"pick":0} then {"row":"Price","v":["$9","$12"]} lines, then
      say which to pick and what would change that. Keep the prose short.
    flow: >-
      This is a process with a decision in it. A `flow` block fits: {"c":"flow"}, then
      {"n":"a","t":"Step"} node lines and {"e":["a","b"],"l":"Yes"} edge lines (at most 8 nodes).
    claim: >-
      The user is testing a claim. A `claim` block fits: {"c":"claim","text":"…","verdict":"misleading"}
      then {"+":"what supports it"} and {"-":"what disputes it"} lines, each ending in a [Title](URL).
    # ...one entry per option, each a one-line description plus one exemplar line.
```

```go
// config/oracle.go DefaultOracle() — policy (mirror in config.yaml.example; TestExampleOracleMatchesDefaults)
"ui": {Threshold: 0.70, SkipForFocus: []string{"safari", "brief"}, Suppresses: []string{"format"}, VisualsLowOffset: 0.15},
// and add "ui" to emotional's Suppresses list
```

Concrete engine changes (small, in `gateway/oracle.go` and friends):

- `OracleInput.Visuals` ("off" | "low" | "normal"), read from the new setting at the call site in
  `gateway/turn_oracle.go`. In `RunOracle`, skip the `ui` question when `off` (special-cased by key,
  like the `field` chip already is) and raise its bar by `VisualsLowOffset` when `low`
  (new `OracleCheckRules` field, zero for every other check). Normal uses `Threshold` as-is.
- `Suppresses: ["format"]` so `format`'s Markdown-shape nudge doesn't stack with the block nudge. A
  held-back `format` already shows as "held back" in the ⓘ sheet. With the dial Off `ui` isn't asked
  and `format` behaves exactly as today.
- Skips: Brief (a few sentences; a block defeats it), Safari (own pacing). `emotional` suppresses `ui`:
  someone distressed gets acknowledgment, not a comparison widget. `high_stakes` does **not** suppress
  it, but its caveat must survive: a compare block is not a licence to drop the caveat that changes
  what the user should do. Check this in the live run.
- One block per answer is the default (Jev returns one winner; the nudge says "one fence"). The model
  may still add accents (`callout`, `stat`, `quote`, `disclose`) under the base prompt.
- **If the 11-way pick proves poorly calibrated**, split into two questions in the same call: `ui`
  (yes/no gate, the dial sets its bar) and `ui_block` (which one, no `none`), firing only when both
  clear. That needs one new `requires` rule in `OracleCheckRules`. Not built until the spike says so.
- No schema change: `OracleResult.Checks` is generic. Frontend: a label entry in `oracleLabels.ts` for
  the margin note ("Read as **comparison** · shown as a **compare** block"), a star in the
  constellation (it already scales with check count), and a "Rerun as plain text" button on the ⓘ card
  (visuals analogue of the existing rerun; a wrong firing costs one tap).

Two prompt layers, so Oracle failing never breaks the feature:

1. **Base fragment** (new `ui:` section in `prompts.yaml` + `prompts/ui.go` defaults + drift test;
   present whenever the dial isn't Off): compact grammar for every block, "plain text is a valid
   answer; use a block only when it clearly beats prose", "`ui` fences at top level only". This is the
   floor: Oracle off, Jev timed out (`oracleTimeout` 2.5s; the original spike saw a 14% failure rate),
   or `ui` didn't fire.
2. **Oracle nudge** (only when `ui` fires): names the block and gives its exemplar line. Just-in-time
   few-shot, in `## Oracle` and re-injected by `modeReinforcement` for the same recency reason.

Once the nudge carries the exemplar the base fragment could shrink to names plus one-liners; don't do
that until the Oracle-off fallback rate is measured, since a smaller floor means a worse fallback.

## Settings, prompts, docs

- **Setting `visuals`** (off/low/normal, default low). Mirror `oracle_ghost_enabled`'s plumbing:
  `gateway/settings.go` (store key const, validation set, GET/POST field, `...FromStore` helper),
  `gateway/settings_test.go` (including a test that the validation set matches the dial's values),
  `web/src/lib/settings.svelte.ts`, `SettingsPanel.svelte` (segmented control, copy as in the mockup).
- `prompts.yaml` + `prompts/` defaults for the `ui:` fragment and the Oracle `ui` check; hot-reloaded.
- `config/oracle.go` + `config.yaml.example` for the `ui` rule.
- `HelpModal.svelte` `TERMS`: entries for "Visuals" and "UI blocks" (CLAUDE.md requires it for any new
  named feature). `docs/FEATURES.md`: one or two lines. `DEVELOPMENT.md`: a short note on the
  segment/parse/component architecture.
- `dev/fakeopenrouter` scripted responses with `ui` fences (progressive, plus malformed/unknown
  lines) for the Playwright runs.

## Phases

Each phase is one or more PRs; each ships usable on its own.

**P0: segmentation + streaming mermaid** (no `ui` blocks yet; worth shipping alone).
`splitContent`, `ChatTurnView` rendering by segment, `MermaidBlock`, `mermaid.ts` extraction, the
verification-occurrence counter threaded across segments. Acceptance: a thread with prose + two
mermaid fences renders identically to today when finished; mid-stream the diagram builds up and a
deliberately broken prefix keeps the last good render; no regression in verification ticks (compare
chip marks before/after on a fixture turn); phone render cost measured.

**P1: infrastructure + the four simplest blocks.**
`parseUi`, `UiBlocks`, `UiText`, `callout`, `stat`, `compare`, `steps`; base prompt fragment; `visuals`
setting + control; flatteners (TS + Go) wired into copy, read-aloud, search_chats, Weaver, titles;
HelpModal/FEATURES entries. **No Oracle yet**, so the floor can be measured on its own. Acceptance:
`runVerification` is fed the stripped answer, so ticks on prose chips are unchanged on a fixture turn;
fakeopenrouter run fills a compare table row by row in the real app; truncated, malformed and unknown
streams degrade per the grammar; copy and read-aloud give clean text; Off removes the fragment.

**P2: Oracle `ui` check**, limited to the options P1 can render (`compare`, `steps`, `none`), plus
margin-note wording, ⓘ card, "Rerun as plain text". Run the classification spike first (below).

**P3: remaining blocks, in groups**, each group also extending the Oracle option list and exemplars:
(a) `timeline`, `checklist`, `procon`, `choose`, `facts`; (b) `flow`, `tabs`, `disclose`;
(c) `claim`, `quote`, plus the verification wiring from "Sourcing and verification": flatten feeds
`extractClaims`, schema-order link counting on both sides, quote-specific Jev question, claim cap.

## Verification

- Unit: `splitContent` (open/closed fences, nested lists, many fences), `parseUi` (truncated at every
  byte offset of each block's golden fence, malformed, unknown, over-cap), flatteners against the
  shared fixture, `resolveFocus`-style table tests for the `ui` rules (dial off/low/normal, Brief,
  Safari, emotional, high_stakes).
- **Property-style streaming test:** for every golden fence, feed every prefix to `parseUi` and assert
  it never throws and the block count is monotonic. This is the guarantee the whole design leans on.
- Playwright against `dev/stack.sh --fake-llm`: scripted chunked fences; assert the DOM after each
  chunk (rows appear one at a time), mobile viewport (390px) no horizontal overflow, ticked
  checkbox/expanded node state survives subsequent tokens.
- Oracle spike, `dev/oracle_spike`-style: ~50 real thread-openers plus a hand-written set of true
  positives (comparisons, how-tos, timelines, decision chains, claims) and true negatives (chatty,
  opinion, one-fact lookups). The number that matters is **false-positive rate**, not accuracy: an
  unneeded block is worse than a missed one. Also check the 11-way pick's calibration (see the
  two-question fallback).
- Real-model runs through `/api/ask`, with and without the nudge, same prompts; confirm via
  `dev/fakeopenrouter`'s `/_control/calls` that the nudge text reaches the request body.

## Risks

- **Over-eager UI** is the likeliest failure: OpenAI trained theirs, we prompt ours. Mitigations: tiny
  vocabulary, Low default, Oracle's bar, "plain text is valid", the one-tap rerun as plain text.
- **Token cost and latency** on a phone over Tailscale: terse JSON keys; measure the base fragment and
  each exemplar; blocks also ride along verbatim in history on later turns (Decision 14), so the base fragment and exemplars should stay terse.
- **Mermaid jitter and phone render cost** (above). Mitigated by throttling, not eliminated.
- **Verification and claims.** Server-side claim extraction runs over the raw answer; JSON lines
  must not reach it unflattened. P1 strips `ui` fences before extraction and the client counter
  skips `ui` links (both or neither, never one); "Sourcing and verification" turns both on together.
  Evidence only exists for pages `web_read` fetched this turn, so a block citing a snippet-only source
  never gets a badge. Principle: trust marks are never model-written.
- **Mobile layout** is the primary target (CLAUDE.md); desktop is secondary.

## Open questions

All three are gated on measurement, not on a decision:

1. Does the nudge-with-exemplar let the base fragment shrink, and by how much? Needs the Oracle-off
   fallback rate measured first.
2. Single `ui` question vs the gate + kind split: decided by the spike's calibration numbers
   (false-positive rate first).
3. Mermaid render cost on a real phone, which sets the throttle (P0).
