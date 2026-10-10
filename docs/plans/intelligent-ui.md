# Intelligent UI: streamed, declarative `ui` blocks inside the answer

**Added: 2026-10-08. Rewritten as a build plan: 2026-10-09.**

**Status: P0, P1 and P2 built (2026-10-09). P3 group (a) built (`timeline`, `checklist`, `procon`,
`choose`, `facts`; see "Spike results: group (a)" below) and group (b) (`flow`, `tabs`, `disclose`; see
"Group (b)" below) and group (c) (`claim`, `quote`, verification wiring; see "As built" under "Sourcing
and verification" and "Group (c)" below). **P3 is complete.** Tracking issue #159.
P2 = the Oracle `ui` check (compare/steps/none; Low bar 0.85, Normal 0.70; holds back `format`; held back by
`emotional`; skipped under Brief/Safari including a mode Oracle picks itself), the margin-note clause, the
sheet's "Visual block" row, "Rerun as plain text" (`no_visuals`), and `dev/ui_spike` (results below).
Pulsar pulses are in too (decision 3, amended). P1 = `parse.ts`,
`components/ui/` (callout, stat, compare, steps), the `visuals` setting + `prompts.yaml` `ui:` section,
and the flatteners (`flatten.ts`, `uiblocks`) wired into copy, read-aloud, `read_thread`, Weaver
and claim extraction. **Deliberately not done in P1:** `search_chats` indexing still indexes the raw JSON
(`messages_fts` is an external-content index whose delete triggers must replay the exact indexed text, so
changing it needs new triggers plus a reindex migration; a block's JSON keys can match a search and show
in a snippet). **Found while building:** blocks are taught to live WebSocket turns and to `/api/ask`
(so the API can exercise the real behaviour; `polaris search` flattens the fence on print), but not to
Pulsar pulses or voice calls (`ClientMessage.OffersVisuals`, `!VoiceMode`); Oracle's existing `format` nudge
("give numbered steps") competes with a `steps` block until P2's `Suppresses: ["format"]` lands. P0 = `uiBlocks/split.ts`,
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
   would be its own follow-up. **Amended 2026-10-09 (operator): Pulsar pulses are in.** The premise was
   wrong for Pulsar: a pulse is a real thread opened in the normal chat view, and the Pulsar pages only
   link to it, so blocks render with no new code. A pulse also already runs Oracle, so the `ui` check
   applies. Still out: Pulsar Daily (its blocks are separate mini-generations), Atlas's Quick Answer (a
   plain-text card; also `/api/ask` with `quick_mode`), voice calls (read aloud). `/api/ask` is in, so
   the API exercises the real behaviour.
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
  "unrenderable line" / "unknown component" rows). Never throws, never blanks the answer. **As
  built:** a line whose only fault is a missing or misplaced bracket (a dropped `]` before a row's
  closing `}`, seen live) is repaired by counting brackets outside strings and inserting the one the
  closer displaced (both `parse.ts` and `uiblocks.go`'s `closeBrackets`), then re-parsed — so a row
  the model almost finished renders instead of dumping its JSON. Anything worse stays a raw row.
- The compiler: split the fence body on `\n`; every line before the last `\n` is complete and
  parseable; the trailing partial line is held back (shown as a shimmer at most) until its newline.
  Re-parsing the whole body on every content update is fine (blocks are small; the cost is bounded by
  the caps below), as long as rendering is keyed so existing components update in place.
- **Cancelled or errored turn:** complete lines stay rendered, the partial line is dropped. (Proposed
  default; the mockup assumes it.)
- **Caps** (enforced by the parser, beyond them a line degrades to a raw row): 40 lines per fence,
  400 characters per text field. There is deliberately **no cap on the number of `ui` fences per
  answer**: an answer that walks the whole block catalog is legitimate, and the old 8-fence cap
  printed the overflow as raw JSON, which read as a rendering failure. Each fence is still bounded
  by the line and field caps, and the count by the model's own output length.
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
| `compare` | `cols` (2–6), `pick` (index, optional) | `{"row","v":[...per col],"src"}` ≤ 12 | cards on phones with a Pick ribbon; table (sticky first column, side scroll) wider. Wrong-length `v` is padded/truncated; out-of-range `pick` ignored | option |
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
   (`uiBlocks/flatten.ts`) and Go (`uiblocks`), turning each block into readable text
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

> **As built (2026-10-09), and it differs from the design below in one decision: block links are
> addressed by locator, not by occurrence number.** Items 1-3 below (flatten feeds `extractClaims`, schema-
> order link counting on both sides, a client counter threaded through `UiText`) were the original plan.
> Building it showed why that is the fragile choice for blocks: `compare` draws every cell twice (cards and
> table), flattens column by column but draws row by row, and any ordering drift puts a tick on the wrong
> chip, which is the one failure verification must not have. What shipped instead:
>
> - A link inside a block is named by where it sits, `<fence>.<block>.<item>.<field>#<n>` (e.g.
>   `0.2.1.src#0`): `uiblocks/sites.go` enumerates them with the sentence Jev should check, and
>   `VerificationMark` carries an optional `locator`. The client builds the same string per field (a `loc`
>   prop through `components/ui/`, `UiText`, `UiSources`) and `renderInlineCitations` ticks a link when its
>   `<loc>#<n>` has a supported mark. `n` counts tracked links only, as the client chips only those.
> - **Prose is untouched**: still `extractClaims` over the fence-stripped answer and the nth-occurrence
>   rule; a locator mark never ticks a prose chip and a prose mark never ticks a block link.
> - **A disagreement can only lose a tick, never misplace one.** A contract test
>   (`TestSites_FieldNamesMatchTheComponents`) fails if a Go field name stops matching its Svelte `loc`.
> - Each evidence line of a `claim` block is checked against its own line (not the headline claim); a
>   `quote`'s sources are checked for the passage (exact match first, no Jev call, then Jev with a
>   "contains this passage, a paraphrase does NOT count" question); block claims are capped at 20 a turn
>   and run after the prose claims.
> - Not wired: `compare`'s phone-card source line (merged and de-duplicated across rows, so no per-row
>   address; the wide table's per-row sources do tick), `stat`'s big value (plain text on the client), and
>   anything inside a `<td>` (existing `renderInlineCitations` rule: table-cell links stay plain links).
> - **Live check** (real model, real pages, real Jev, `/api/ask` with `wait_verification`): a fact-check
>   answer's three supporting lines each got their own locator mark and tick (two of them citing the same
>   NIH page, ticked independently), the disputing line scored 0.71 and correctly got none, and a `quote`
>   matched its NASA page verbatim at confidence 1.0 with no Jev call. A `facts` card got no ticks because
>   the model wrote no per-row `src` (a one-sentence prompt nudge did not change that on re-run).

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

1. **Make the server see block text as claims.** A Go flattener (`uiblocks.Flatten`, the same
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
- **Named "Prism" (2026-10-09):** a plain beam of text split into something with structure. User-facing
  only (Settings row, glossary, FEATURES.md); the `visuals` setting key, `Visuals` state field and Go
  identifiers keep the plain name so no stored value is orphaned. `HelpModal.svelte` `TERMS` has
  "Prism = Immersive UI" (broadened from "Comparisons and step lists", which undersold the block set;
  the entry exists because the themed name needs translating). `docs/FEATURES.md`: one or two lines. `DEVELOPMENT.md`: a short note on the
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
  must not reach it unflattened. Prose claims still run over the fence-stripped answer; block links are
  verified separately and ticked by locator (see "As built" under "Sourcing and verification"), so the two
  can no longer disturb each other's numbering. Evidence only exists for pages `web_read` fetched this turn, so a block citing a snippet-only source
  never gets a badge. Principle: trust marks are never model-written.
- **Mobile layout** is the primary target (CLAUDE.md); desktop is secondary.

## Spike results: the Oracle `ui` check (2026-10-09)

`dev/ui_spike` drives the real `gateway.RunOracle` with the shipped prompts and thresholds over
`dev/ui_spike/corpus.json` (65 hand-written messages: 14 compare, 14 steps, 32 plain, 5 borderline) plus
60 real thread openers from the dev DB (unlabeled; they stay in `/tmp`, not the repo). 125 calls, **$0.022
total**. Jev answered every one (0 failures after at most 2 retries; the 2.5s live `oracleTimeout` is a
separate matter and was not exercised).

| Dial (bar) | False positives | Block hit rate | Wrong block |
|---|---|---|---|
| Normal (0.70) | 1 / 32 (3.1%) | 28 / 28 | 0 |
| Low (0.85) | **0 / 32** | 27 / 28 (96%) | 0 |

- The one Normal false positive was "difference between affect and effect" (`compare` 0.81), which is
  arguably a fair comparison. Low's only miss was a git-rebase how-to (`steps` 0.77).
- On the 60 real openers Normal would fire on 3 (a TV purchase at exactly 0.70, a literal "compare the
  sources" request, and a Safari request) and Low on 1 (the literal compare request).
- **Found and fixed:** the Safari request got a `steps` nudge at 0.84, because Safari was picked by the
  same Oracle call, after the question-time `skip_for_focus` had already looked. `uiSkippedByOwnFocus` now
  re-checks against the mode the turn will actually run under, and a skipped `ui` no longer holds back
  `format`. The older checks (`depth`, `format`, `task`, `clarify`) have the same gap and were left alone.
- **Thresholds kept as designed** (0.70, +0.15 on Low): no tuning was needed.
- Not measured: the 11-way pick (only `none`/`compare`/`steps` exist so far), and latency. Re-run the
  spike after each P3 block group adds options; false-positive rate first.

## Spike results: group (a) (2026-10-09)

Corpus grew to 97 labeled messages (5 new shapes, 12 new negatives) plus the 60 real openers; $0.029 for a
157-message run. Block caps chosen for the shapes the catalog left open: `procon` 8 per side, `choose` 8
rules. Jev answered every message.

| Dial (bar) | False positives | Block hit rate | Wrong block |
|---|---|---|---|
| Normal (0.70) | 2 / 44 (4.5%) | 44 / 48 (91.7%) | 0 |
| Low (0.85) | **1 / 44 (2.3%)** | 44 / 48 (91.7%) | 0 |

- **Single 8-way pick holds up; the gate split is not needed.** No wrong block anywhere, and the false
  positives stay at 1-2 of 44.
- **`choose` first lost to `compare` every time** (0.53-0.69 for `compare`), because Jev reads any named
  options as a comparison. **Fixed in part, same day:** the option now says the person asks which to pick or
  what to do *and the best answer depends on their own situation (usage, budget, location, goals), so
  if-then rules beat a table of attributes*, and the corpus has 10 situational `choose` messages instead
  of 4. On Normal, `choose` now lands 5 of 10 (up from 0 of 4) with `compare` 100% and no wrong block; on
  Low most of the rest are the right label below the 0.85 bar (0.64-0.82), which is Low doing its job.
  What did not work: also rewording `compare` ("named options weighed side by side on attributes") pulled
  `compare` confidence under the bar for Roth-vs-traditional-IRA, so `compare` keeps its original text.
  Takeaway: with 8 options the probability mass splits, so a softer shape like `choose` will always sit
  nearer the bar than a crisp one; do not tune it further on a handful of messages.
- After the `choose` change: Normal 1 / 44 false positives, 49 / 54 hits, 0 wrong block; Low 1 / 44 false
  positives, 44 / 54 hits (the rest sub-bar, not wrong). Two real "what is X?" openers fire `facts` on Normal
  only (0.71, 0.78), both fair uses.
- **The remaining Low false positive is "history of the Roman aqueducts" -> `timeline` 0.97**, which is
  defensible as a fair timeline; the label is arguable, not the pick.
- **Found on real traffic and fixed:** a stock-price request got `timeline` at 0.87 (a trend over time,
  not dated events), above the Low bar. The `timeline` option now says "Not a price, number or trend over
  time"; the re-run no longer fires on it. (Normal also showed `facts` 0.78 on "what is kimi k2.8?", which
  is a fair use, and Low stays quiet.) Affect/effect `compare` at 0.74 is the same Normal-only borderline
  as before.

## Group (b): `flow`, `tabs`, `disclose` (2026-10-09)

Built the same way as group (a). What is worth knowing that the catalog did not say:

- **`flow` layout is a pure function** (`uiBlocks/flowLayout.ts`, unit-tested): BFS from the first node, so
  nodes and edges stay flat lists in arrival order and an edge may name a node that has not streamed in.
  Caps: 10 nodes, 20 edges; a duplicate id, a self-loop or a malformed edge is a raw row. Expand state is
  keyed by node id: a live run moved "Replace the fuse" from layer 2 to layer 1 when a later edge landed
  and it stayed open. The Go flattener does not repeat the layout: it lists nodes, then edges
  (`A -> B (label)`), in arrival order.
- **An edge to a sibling in the same layer is neither a loop nor a new layer**, so it gets its own note
  ("also leads to X"). The first version said "back to X" about a node drawn beside it, which read as wrong.
- **Real model finding: tabs hold code.** Asked how to install Node on three OSes, the model put a fenced
  command block in every tab (newlines as `\n` inside the JSON string). Inline-only rendering collapsed the
  commands onto one line and the 400-char clip cut the Ubuntu tab off mid-command. Now a tab's text has a
  2000-char cap (`MAX_TAB_TEXT_CHARS`) and renders as block Markdown (`UiText block`, same DOMPurify and
  chip pass); a disclosed paragraph gets 1200. Both flatteners carry the same caps, pinned by tests.
- **A refactoring trap, worth a comment in `parse.ts`:** giving `text()` a second `max` argument made
  `cols.map(text)` pass the array index as `max` and clip every compare column. Tests caught it.
- **Spike** (179 messages incl. 5 `flow` + 5 `tabs` positives, 3 `steps` and 3 `none` look-alikes; $0.034):
  Normal 2 / 47 false positives, 57 / 67 hits, 1 wrong block; Low 2 / 47, 52 / 67, 0 wrong. Both new shapes
  win with the right label (`flow` 4 of 5 and `tabs` 5 of 5 on Normal), several sit below the Low bar. The
  two false positives are arguable labels ("what is the process by which a bill becomes law?" -> `steps`
  0.95, the aqueducts `timeline` again). The one wrong block is a `choose` message that read as `compare`.
  As the option count grew, `choose` slipped further under the bar (0.41-0.67): the probability mass splits,
  as predicted. The gate + kind split is still not needed on these numbers; revisit if real use shows noise.

## Group (c): `claim`, `quote`, verification (2026-10-09)

Blocks: `quote` (`text`, `by`, `src`; a flat serif pull-quote) and `claim` (`text`, `verdict`, then `+` / `-`
lines with `src`, at most 6 per side; a verdict pill with a glyph and a word, never colour alone). The
verdict is the model's own read and has no caption and no tick (decision 17); an unknown verdict is
`unverified`. Verification wiring is described under "Sourcing and verification" (it deliberately differs
from the original counter-based design). Oracle gets one new option, `claim` ("checking whether one
specific claim holds up, with evidence on both sides"); `quote` stays an accent under the base prompt.

- **Spike** (188 messages, 6 new `claim` positives, 3 new negatives; $0.036): all six `claim` messages win
  as `claim` on Normal; four sit under Low's 0.85 bar (0.70-0.84). False positives are 3 / 50 on Normal
  (affect/effect `compare` 0.73, the bill-becomes-law `steps`, the aqueducts `timeline`) and 2 / 50 on Low,
  0 wrong blocks on Low and 1 on Normal. With 11 options the probability mass is split further, which is why
  `choose` keeps drifting under the bar; still no case for the gate + kind split.
- **Prompt:** the base grammar now also says where sources go ("where a row, step or line rests on a source
  you read, give that line `src`"). A `facts` card for a single product page still came back with no
  per-row `src` (the model credits the page once, in the subtitle), so such a card gets no ticks.

## Next blocks: `views`, `define`, `analogy`, `readings` approved (2026-10-10)

Brainstormed eight candidates (`mockups/prism-next-blocks.html`): `known`, `views`, `changes`, `rank`, `cmd`,
`agenda`, `define`, `spread`. **Operator approved `views` and `define`, and in a second round `analogy` and `readings`.** The other six
(`known`, `changes`, `rank`, `cmd`, `agenda`, `spread`) are not approved and not planned; revisit only if real
use shows the gap. The inline (tap-a-term) form of `define` was considered and skipped. Mockups for the second
round are in the same file. Tracking issue #163.

- **`views`** (a contested question, by camp). Container `{"c":"views","title"}`; child lines
  `{"who","stance","t","src"}`, cap 6 camps. No verdict and no strength meter (a model-invented weight of
  evidence is unverifiable). Each camp's `t` and `src` are verified per line like `claim`'s evidence lines
  (new `sites.go` case, `loc` props to match). Oracle option wording must separate it from `claim` (one
  specific statement) and `procon` (one thing weighed): "a question where informed people disagree; present
  the main positions". Expect it to compete with `claim`; re-run `dev/ui_spike`, false-positive rate first.
- **`define`** (terms in plain English). Container `{"c":"define"}`; child lines
  `{"term","means","also":[...],"ex"}`, cap 8 terms. An accent under the base prompt, like `quote`: no
  Oracle option, no verification (the model's own definition, never presented as a quote of a source).
- **`analogy`** (X is like Y). Container `{"c":"analogy","x":"On the network","y":"In the post","like":"A postal system"}`;
  child lines `{"x","y"}` (cap 6 pairs) and one optional `{"breaks"}` line, the point of the block (an analogy that
  never says where it fails teaches something false). Accent under the base prompt: no Oracle option, no
  verification (the model's own teaching device).
- **`readings`** (pages to read, in order). Container `{"c":"readings","title"}`; child lines
  `{"lvl":"start|next|deep","t","why","src":["https://..."]}`, cap 6, rendered in fixed level order. It exists so
  the answer sends the reader to the sources, so: **a line's URL must be a page `web_read` fetched this turn**
  (the same evidence set verification uses); any other line is dropped and counted in a muted "left out" note.
  "Opened by Polaris", the reading time and the PDF page count are **measured from the page itself, never
  model-written and never derived from what `web_read` handed the model**: the filter pass can return two lines
  from a 15-minute article, so its output says nothing about length. Measure in `web_read` at the same point
  `AddEvidence` runs (`tools/web_read.go`, raw extracted text, before `FilterExtractedText` and `windowText`):
  words of the full text at ~230 wpm, rounded up. Record it in a new per-URL stats map on `tools.Context`
  (words, PDF `totalPages`, a `reliable` flag) rather than re-deriving it from `EvidenceForURL`'s joined string.
  **Cases that get no number:** a PDF shows "N pages" from `totalPages` (`ExtractPDFPage` returns one page,
  capped at `maxExtractedChars`, so there is no full text to count), and a fetch where `looksLikePaywall` or
  `looksEmpty` held shows nothing (a stub would read as "1 min" for a long article). Citations alone cannot mark
  "opened": `web_search` also calls `AddCitation` for snippet-only hits, so "opened" means the URL has evidence
  (and stats). Match `src` to the evidence key exactly as passed to `web_read`, so normalise trailing slashes
  and redirects the same way on both sides. How the numbers reach the client (extra `Citation` fields riding the
  existing `tool_result` payload vs a separate event) is open; check whether citations persist with the message
  before relying on reload showing them. Ticks are local state only, like `checklist`. Oracle option
  must stay clear of `highlight` ("best few things I found", pick one) and Shopper mode: "the person wants to
  learn a topic; give pages to read, in order". Needs the new server-side measurement above (opened-URL filter
  plus page stats) on top of the usual per-block pieces.
- All four need the usual per-block pieces: `parse.ts` case and caps, a `components/ui/` component, a Go
  flattener case in `uiblocks` plus the shared `testdata/ui_flatten.json` fixture, the base-prompt line in
  `prompts.yaml` and `prompts/ui.go` (drift test), and a `docs/FEATURES.md` mention. Neither needs a
  `HelpModal` `TERMS` entry (no new themed name). No code lands until an issue exists.

## Open questions

Two of the three were gated on measurement; the second is now answered:

1. Does the nudge-with-exemplar let the base fragment shrink, and by how much? Needs the Oracle-off
   fallback rate measured first.
2. ~~Single `ui` question vs the gate + kind split~~ **Single question, for now.** With 3 options it
   was well calibrated (0 wrong blocks, 0-3% false positives). Revisit when P3 grows it toward 11.
3. Mermaid render cost on a real phone, which sets the throttle (P0).
