# Mermaid rendering — investigation + plan

Rendering ` ```mermaid ` code fences as live diagrams in assistant replies. **No tool call
involved** — the model just writes a mermaid fence and the frontend renders it, the same way
GitHub/Obsidian render mermaid in markdown. This doc records why that's the right shape versus a
`mermaid` tool, and the concrete slice of frontend work it needs.

## Why not a `mermaid` tool

The natural "pattern-match" is to copy `visualize`: a tool the model calls, a `tools.Context`
field, wire-through, a renderer component. That's the wrong shape for this feature, for three
reasons that line up with how `visualize` was scoped in the first place:

1. **`visualize` exists to *extract data out of prose* into structured form.** The whole point
   (see `docs/plans/visualize-and-image-search.md`: "Structured, already-fetched data gets
   flattened into text and thrown away") is that the model's answer was carrying table-shaped
   data that only became readable as a chart once pulled out of the text and typed. A mermaid
   diagram has no equivalent structure to extract — **the diagram *is* the text.** The model
   writes a flowchart directly; there is nothing between prose and artifact to pull apart, and no
   reason for it to take a detour through a tool result.
2. **Tool cost.** Every tool in the catalog costs tokens in every turn's tool list and a full
   model round-trip each time it's used. `visualize` "has to be rare" per its own plan; a mermaid
   tool would have to justify the same slot while offering *less* — its only output is a string
   of code that would render identically if it just appeared in the answer body.
3. **Plumbing is strictly heavier.** `visualize` fits the existing at-most-one-chart-per-turn
   constraint (`Context.Chart`, `SetMessageChart`). Diagrams don't — a reply can legitimately
   contain several, so a tool-based approach needs *new* multi-diagram plumbing (`Cards`-style
   append, a new column, a new renderer path). Auto-rendering inline text gives arbitrary
   multiple diagrams per turn for free, in the already-existing message content, and works the
   moment a mermaid fence appears in **any** message — no new column, no new event, no parser.

The one real thing a tool buys is *"the model explicitly decides a diagram is needed"* — and
that's a prompt instruction, not a mechanism (see "Prompt guidance" below).

## Where rendering happens

The app's markdown pipeline is already one shared choke point: `web/src/lib/markdown.ts` owns
the single `marked` instance (ChatTurnView and ToolEvent both import `marked` from there), and
`ChatTurnView.svelte` renders assistant answers as
`renderInlineCitations(DOMPurify.sanitize(marked.parse(turn.content)))`.

Two small frontend changes cover the whole feature:

### 1. `web/src/lib/markdown.ts` — special-case the `mermaid` fence

The `code` renderer currently treats every fence as code to syntax-highlight; `mermaid` falls
into its "unrecognized language → plaintext" branch (`hljs.getLanguage('mermaid')` is undefined),
so it renders as a plain colored-less code block. Add a branch *before* the hljs lookup:

```ts
code({ text, lang }) {
    if (lang && lang.toLowerCase() === 'mermaid') {
        // Escape ourselves — marked hands `text` to the renderer unescaped
        // (hljs normally does the escaping; here we must). DOMPurify keeps
        // the text safe as raw text nodes regardless.
        const escaped = text.replace(/&/g, '&amp;').replace(/</g, '&lt;').replace(/>/g, '&gt;');
        return `<pre class="mermaid-source" data-mermaid><code class="language-mermaid">${escaped}</code></pre>`;
    }
    // ... existing hljs path unchanged ...
}
```

`data-mermaid` is the discovery marker for the post-pass, and is allowed through by DOMPurify
(data-* attributes are in its default allowed set; the existing test bundle will catch it if not).

### 2. `ChatTurnView.svelte` — a post-render DOM pass, not a component

Replacing DOM inside `{@html}` content doesn't compose with Svelte components, so the rendering
is a plain async DOM pass in the same file as the prose, run via a `$effect` keyed on
`renderedHtml` and gated on `!turn.streaming` (see "Streaming" below):

```ts
let proseEl = $state<HTMLElement>();
$effect(() => {
    if (proseEl) void renderMermaidIn(proseEl);   // re-runs whenever renderedHtml changes
});
```

`web/src/lib/mermaid.ts` (new, half lib / half module cache):

- `renderMermaidIn(container)` — `container.querySelectorAll('pre[data-mermaid]')`; for each
  block, read `code.textContent` (the browser has already HTML-unescaped the source for us),
  call `mermaid.render(id, code)` and `replaceWith` the returned SVG. Blocks that fail parse/
  render are left as their original `<pre>` **and given a small "couldn't render this one" note**
  — a syntax error degrades to exactly today's behavior (a readable code block), never a broken
  hole or a silently-missing diagram.
- Lazy-loads mermaid with a module-level `import('mermaid')` promise so the lib (~a few hundred
  KB gzipped once bundled) is only ever fetched by a client that actually displays a mermaid block — the potato
  serves the static assets, so this cost should stay off the critical path of every other reply.
  Its themes are set at render time, and language defaults are initialized to `securityLevel:
  'strict'` — untrusted content (a page's fetched text echoed into the diagram by the model) is
  the native risk here, and strict mode strips HTML from the rendered SVG.
- Theme-aware: reads `document.documentElement.dataset.theme` ('dark' | 'light', see
  `settings.svelte.ts:169`) at render time and picks a matching mermaid theme. The app is
  dark-by-default ("night-sky-not-tech-neon"); the light theme's diagram needs to not pin a
  white background / punchy colors into a dark thread.

No new component file is strictly needed; the pass is ~40 lines in the helper. If it grows
(theme toggling mid-render, copy-source affordance) a `MermaidBlock.svelte` is a later tidy, not
v1 scope.

## Semantics worth calling out

- **Streaming.** While a reply streams, the fence is briefly un-closed, and marked renders an
  unclosed fence as a *full* code block the moment it opens — if the pass ran live it would try
  to parse a half-diagram and show a render-failure note that vanishes once the block closes.
  Gate the pass on the turn not being `streaming`, and the pass only runs once on the final
  content. (Retry/regenerate and variant switching both replace `turn.content` → a fresh
  `renderedHtml` → the `$effect` re-runs on the fresh DOM, so old replacements are simply
  discarded with the old content — no manual cleanup.)
- **Multiple diagrams.** Each `pre[data-mermaid]` is replaced independently, so several blocks
  in one answer all render. The at-most-one constraint that shapes ChartSpec plumbing just
  doesn't exist here — the inline path has nothing to enforce it against.
- **Voice read-aloud.** `appState.readAloud` speaks `turn.content`, so a mermaid fence gets read
  as raw source text. That's the same as any code block today — not a regression, and stripping
  the source from the TTS payload is a possible v2 nicety, not v1 (keep scope small).
- **User messages / ToolEvent.** User bubbles render `turn.content` directly (no markdown) and
  `ToolEvent` commentary uses `marked` but has no post-pass — a mermaid fence in either just
  shows as a plain code block. That's fine for v1; it's the assistant answer that's the
  interesting surface. (Pasting mermaid into your *own* message and having it render is a natural
  v2; it needs the post-pass in the ChatTurnView user-bubble branch, which today is not markdown
  at all.)

## Prompt guidance — one small edit, three copies

The model needs to know the fence language renders and **when to use it** (this is the entire
"explicit decision" a mermaid tool would have provided, for one paragraph of prompt instead of a
tool):

> To include a diagram — a flowchart, sequence diagram, architecture, state machine, and so on —
> write a ```mermaid fenced code block and it renders inline. Use it only when a real diagram
> clarifies what you've said; a simple list or table is still better as prose.

`prompt.md` (the live system prompt) is the primary edit. `prompts.yaml`'s
`agent.fallback_system_prompt` and `prompts/prompts.go`'s `buildDefaults` Go-literal mirror
`prompt.md`'s text and stay in sync with it by convention — all three get the same paragraph.

## CLAUDE.md deployment checklist

n/a for this slice, checked item by item:

1. **Hot-editable CWD-relative resource files** — none added (this is a frontend build dep, not
   a runtime config file). No Dockerfile `COPY` / bind-mount two-sided sync needed.
2. **CLI command** — none. No `cmd/*.go` change.
3. **Settings-panel server-mutating action** — none.
4. **Update/restart polling logic** — untouched.
5. **Image build** — the new npm dependency flows through the frontend build; the existing
   `frontend-build-sync.yml` CI gate and the pre-commit hook (`git config core.hooksPath
   .githooks` auto-rebuilds + stages `web/build/` on any `web/src/` / `package.json` /
   `pnpm-lock.yaml` change) both already cover exactly this change. `pnpm-lock.yaml` must be
   committed. Docker builds fresh from source, so nothing per-image is needed.

The one-for-a-future-note: the potato has to serve the larger frontend bundle the first time a
client renders a mermaid block — fine over Tailscale, and only to clients that actually saw one.

## Testing

- `markdown.test.ts` (existing): a fence ` ```mermaid ` renders the `pre[data-mermaid]` wrapper
  with escaped content (mermaid source with `<script>`/`<` characters stays escaped), and known
  fences (` ```go `) are untouched by the new branch — the branch must be *before* the hljs path
  but must never fire for any other fence tag.
- New helper test for `web/src/lib/mermaid.ts`, with the `mermaid` module mocked (the real one
  needs a full DOM; happy-dom doesn't have an SVG renderer): source with dozens of blocks
  replaced, source with a syntax error falls back to the code block + note (no throw), and the
  lazy-import promise is cached across calls (second block doesn't re-fetch).
- Component level: not for this v1 — the pass is deliberately DOM-level in `ChatTurnView`; the
  unit tests above cover the logic, and the live render is frontend-build-synced like everything
  else.

## Explicitly out of scope for v1

- **A `mermaid` tool** — see "Why not a tool" above. If it ever becomes necessary *because* of
  something diagrams need that prose can't do (e.g. attaching one to a chart card or a Pulsar
  Daily block, or validating before it's shown to the user), the tool would ride the
  `ChartSpec`/catalog machinery — written down here so the re-litigation is a one-sentence
  refusal, not a re-derivation.
- **User-message mermaid rendering** (pasting a block and seeing it render in your own bubble).
- **Copy-source affordance / "edit the diagram"** on rendered blocks.
- **Stripping mermaid source from TTS** — the readAloud-payload nicety noted above.
- **Server-side rendering** (the Go binary rendering diagrams via a headless browser / a hosted
  mermaid API) — rejected now on principle: sends content to a third party (against the private,
  self-hosted brief) or drags a Node/chromium runtime onto the potato, and serves nothing
  client-side rendering already doesn't for a one-reader, single-user app.
- **SearXNG / research integration** — mermaid is a renderer, not a source; Atlas stays
  untouched.

## Next steps

1. `web/`: `pnpm add mermaid` (committed via lockfile + pre-commit rebuild).
2. `web/src/lib/markdown.ts`: the mermaid fence branch (+ escaping) — before the hljs path.
3. `web/src/lib/mermaid.ts` (new): lazy `import('mermaid')` cache, `renderMermaidIn()` DOM pass,
   strict security mode, theme from `data-theme`, error → restore source + note.
4. `ChatTurnView.svelte`: `proseEl` + `$effect` gated on `!turn.streaming`.
5. `prompt.md` (+ `prompts.yaml` + `prompts.go` mirror) — the mermaid paragraph.
6. Tests: `markdown.test.ts` additions; mocked-module tests for `mermaid.ts`.
7. Live-check on the potato/production: `web/build/` committed via the hook, `git push` →
   end-to-end render test (`/api/ask` a question whose answer includes a mermaid block) plus a
   syntax-error reply to confirm the note fallback.