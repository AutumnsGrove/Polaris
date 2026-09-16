# Artifacts: a universal name and viewer for anything `show` surfaces

**Status: shipped and live-verified (2026-09-15, `03e86ca`).** Reframes `show` (shipped
2026-09-14, `docs/plans/show.md`) from an images-only inline viewer into the universal "here's
something I made" surface, covering code_exec-generated charts, generated reports, and — as a
documented but explicitly deferred future phase — runnable HTML/JS/CSS mini-apps. Filed as the
design pass behind issue #41 (report generator) and the workspace-downloads gap found while
reviewing it: there was no way to download what `show` already renders, and no attachment
affordance at all on an assistant message. Mockup: `mockups/artifacts.html`.

**What actually shipped vs. this doc's original plan:** the "Report generator (#41), concretely"
section below describes a dedicated `generate_report` Go tool — that direction was reconsidered
and rejected before implementation. No new tool exists. Instead, `code_exec`'s and `show`'s own
tool descriptions were updated to state directly that `code_exec` is the only way to create a
file (including a report/document) and `show` is how to surface it — the model is trusted to
chain `spawn_researchers` (if it wants real research) → `code_exec` (write the file) → `show`
(display it) on its own, with no orchestration code in between. Live-verified against a real
running dev backend (not `fakeopenrouter`) with two real requests, including a deliberate
ASCII-art stress test (backslashes/pipes/repeated quote-like characters inside the Python string
embedding the report content, to check for tool-call-argument escaping corruption) — both
round-tripped correctly on the first attempt, no retry needed. Sample size is small (n=2); treat
"the model reliably self-chains this" as promising, not proven, until more real usage accumulates.

The viewer itself shipped as a fixed-position overlay (reusing `.modal-backdrop`'s blur/scrim
convention), not the true side-by-side split the reference screenshots showed — see issue #73 for
that follow-up; not done here.

## Why now

`docs/plans/show.md` already flagged this exact extension point and deliberately deferred it:
_"a future artifact `kind` on the same payload would let \[the lightbox\] branch to a sandboxed
`<iframe>` instead... the lightbox becomes the one place that needs to know about a new artifact
type."_ Two things make this the moment to actually design it:

- **The report generator (#41) needs somewhere to put its output that isn't "dump the whole
  document into the chat reply."** The existing `docs/plans/report-generator.md` predates #68 and
  still describes writing to `store.Store.SetMessageAttachment` (singular) — that method is frozen
  post-#68 (`store/store.go`'s comment: "nothing writes them \[any longer\]"); nothing should call
  it in new code. This doc supersedes that section of `report-generator.md`.
- **There's no file-creation/editing tool to design, because `code_exec` already is one.**
  Confirmed directly: `code_exec` already writes into a persistent, per-thread workspace directory
  (`tools/code_exec.go`'s doc comment; the directory is the same one `show`'s
  `resolveWorkspaceFilePath` reads from). A report, a CSV, a diagram — anything artifact-shaped —
  is just a file `code_exec`'s Python writes with `open(...).write(...)`, followed by a `show` call
  naming the path. No new `create_file`/`write_file` tool, no new workspace-write code path to
  secure — the report generator becomes a prompting/orchestration problem (decompose → fan out via
  `spawn_researchers` → synthesize → have the synthesis step's own code_exec call write the `.md`),
  not a plumbing one.

## The core reframe: two tiers of artifact, not one

`show` today assumes every artifact is an image and always renders a big inline `<img>`
(`web/src/lib/components/ToolEvent.svelte`'s `item.tool === 'show'` branch). That stops being true
the moment a report or (later) an HTML app can be `show`n. Splitting into two tiers, matching what
actually differs perceptually between "a chart" and "a five-page document":

- **Visual tier (images) — unchanged, stays big and inline.** A `code_exec`-generated chart or a
  `fetch_url`-fetched photo IS the point of the message at that moment; shrinking it into a chip the
  user has to tap defeats the reason `show` exists (see `show.md`'s original "one step above
  highlight" framing). No behavior change here.
- **Document tier (everything else — reports today, runnable apps later) — a compact, tappable
  card.** Reference: the attached claude.ai screenshots show exactly this shape — an icon, a title,
  a `Document · MD` type label, and a `Download` button with a chevron for format alternatives,
  sitting inline in the transcript at normal message-flow size. Tapping/clicking the card (not the
  Download button itself) opens a dedicated viewer.

`show`'s existing tool call/schema doesn't change (`path`, optional `caption`) — the branch is
purely presentational, decided by content type the same way the backend route already sniffs it
(`http.DetectContentType` in `gateway/workspace.go`'s `handleGetWorkspaceFile`). An
`image/*` result renders the existing big inline embed; anything else renders the card.

## The viewer: side panel on wide viewports, full-screen sheet on narrow ones

Modeled directly on the reference screenshots (side-by-side chat + document panel on a wide
browser window) with a mobile treatment inferred from this app's existing pattern rather than
shown in the references, since none of the three screenshots were mobile:

- **Wide viewport (desktop-class width): a slide-in side panel**, chat compresses to make room
  (matches the reference exactly — the panel is a real sibling of the chat column, not an overlay
  on top of it). Panel header: artifact title + type label, a raw/rendered toggle (the `</>` / eye
  icons in the reference), Copy, Download, expand-to-fullscreen, close. This is genuinely new UI
  chrome — nothing in the app currently does a persistent split-pane layout. **What shipped instead
  (v1 simplification, see issue #73):** a fixed-position overlay docked to the right edge, reusing
  `.modal-backdrop`'s blur/scrim, not a real flex sibling — the chat column doesn't actually
  narrow. Lower risk to ship first (no changes to `ChatView.svelte`'s layout), but visibly not the
  same as the reference; #73 tracks doing the true split.
- **Narrow viewport (phone-class width, this app's primary real usage per README/PRODUCT.md): a
  full-screen modal**, reusing the app's existing `.modal-backdrop`/`.modal-backdrop-close` scrim
  convention (`app.css`, already used by `ImageLightbox.svelte`) rather than inventing new overlay
  chrome. Same header controls as the panel, minus "expand" (it's already fullscreen). This is the
  configuration that actually matters most for this app — see CLAUDE.md: "primarily used from a
  phone over Tailscale."
- **Rendered vs. raw toggle**: for a Markdown artifact, "rendered" reuses the chat's existing
  Markdown renderer (same one normal assistant prose already goes through — no new renderer to
  write); "raw" shows the literal file content in a `<pre>`, matching the reference's `</>` icon
  toggle. Only Markdown gets this pair for now; a future non-Markdown document tier (CSV, later
  runnable apps) may need its own preview strategy, decided when it's actually built.

## What's new, concretely

1. **`ToolEvent.svelte`'s `show` branch** decides by file extension (`isLikelyImage`, a small fixed
   set — png/jpg/jpeg/gif/webp/svg/bmp/avif) rather than adding backend content-type metadata —
   matches `show`'s existing "purely a display action" minimalism, and an unrecognized/missing
   extension defaults to the document card (safer than guessing "image" and showing a broken-image
   icon).
2. **A new `ArtifactViewer.svelte`** (side panel + full-screen modal, same component branching on
   viewport width via a media query, not two separate components) — the one place, per `show.md`'s
   original prediction, that needs to know about artifact kinds. Markdown-rendered / raw / download
   /copy for v1; a `kind` field on the payload is what a future runnable-app tier would branch on
   here specifically, nowhere else.
3. **No backend route changes.** `/api/workspace/{thread_id}/{filename}` already serves any file
   generically; the existing `download={filename}` HTML attribute pattern
   (`ChatTurnView.svelte`'s upload-chip) already produces a correctly-named download with zero
   `Content-Disposition` work — the viewer's Download button and the card's own Download button both
   just reuse that attribute.
4. **Report generator (#41): no dedicated tool at all — superseded, see `docs/plans/report-generator.md`'s
   status header.** Reconsidered during implementation: a `generate_report` Go tool doing its own
   research fan-out + synthesis was rejected as unnecessary orchestration weight. Instead
   `code_exec`'s and `show`'s tool descriptions (`tools/descriptions/*.yaml`) were updated to state
   directly that `code_exec` is the only way to create a file — including a report — and `show`
   surfaces it; the model chains `spawn_researchers` (optional) → `code_exec` → `show` on its own.
   Live-verified working on the first attempt against a real dev backend, including an ASCII-art
   stress test for string-escaping issues.

## Future direction (not decided): runnable HTML/JS/CSS artifacts

Explicitly out of scope for this pass, but worth recording now since it's the reason the viewer is
designed as "the one place that knows about kinds" rather than Markdown-only forever:

- A `kind: "app"` artifact would need the viewer to render a **sandboxed `<iframe>`** instead of the
  Markdown pane — `show.md`'s own deferred open item already named the real posture question:
  `allow-scripts` without `allow-same-origin`, no top navigation, some resource/size ceiling. None
  of that is designed here.
- `code_exec`'s sandbox can already write arbitrary files (so an `index.html` + `app.js` + `app.css`
  triple is no harder to produce than a single `.md`), but serving a *multi-file* app through
  today's single-file `handleGetWorkspaceFile` route needs a real look — either a small static-file
  sub-server scoped to one artifact's directory, or a bundling step that inlines everything into one
  HTML file before it's ever written. Not decided.
- React/build-tooling inside the sandbox (the "or something like that" from the discussion that
  prompted this doc) is a much bigger ask than plain HTML/JS/CSS — it implies either a JS
  toolchain living inside the Docker sandbox image or the model hand-writing pre-bundled code. Worth
  a dedicated follow-up doc once plain static HTML/JS/CSS artifacts are live and the appetite for
  going further is real, not speculative.
- Whether an "app" artifact even reuses `show`'s calling convention (`path` + `caption`) or needs
  its own tool given the very different trust/security posture — an open question, not assumed
  either way.

## Open items — remaining after v1

- **True side-by-side split** instead of the shipped overlay — issue #73, not done here.
- One generic document icon was used for v1 (no icon-per-filetype); revisit only if real usage
  makes that feel wrong.
- The "Download" button's chevron-dropdown from the reference (implying format alternatives) was
  dropped — v1 only ever has one format per artifact, so there was nothing for it to do.
- `ArtifactViewer`'s wide/narrow breakpoint reuses the app's existing 768px convention (same one
  `app.css`'s other `@media` rules already use), not a new one.
- Sample size on live-testing is small (n=2, both prompts fairly explicit about which tools to
  use) — a vaguer "write me a report" with no mechanism hint hasn't been tested yet.
