# Artifacts: a universal name and viewer for anything `show` surfaces

**Status: designed, not yet implemented.** Reframes `show` (shipped 2026-09-14,
`docs/plans/show.md`) from an images-only inline viewer into the universal "here's something I
made" surface, covering code_exec-generated charts, generated reports, and — as a documented but
explicitly deferred future phase — runnable HTML/JS/CSS mini-apps. Filed as the design pass behind
issue #41 (report generator) and the workspace-downloads gap found while reviewing it: there was no
way to download what `show` already renders, and no attachment affordance at all on an assistant
message. Mockup: `mockups/artifacts.html`.

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
  chrome — nothing in the app currently does a persistent split-pane layout.
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

1. **`ToolEvent.svelte`'s `show` branch** learns to check `item.result`'s content type (or simplest:
   attempt the `<img>`, `onerror` falls back to the card — no backend metadata plumbing needed,
   matching `show`'s existing "purely a display action" minimalism) and render the document card
   instead of a broken image icon for a non-image path.
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
4. **Report generator (#41), concretely, once this lands**: no new Go tool file beyond what
   `docs/plans/report-generator.md` already describes for the *research* half (reusing
   `spawn_researchers`); the *output* half changes to "the synthesis step's own `code_exec` call
   writes `report.md` into the thread workspace, then the orchestrator calls `show("report.md")`" —
   deleting that doc's now-stale `SetMessageAttachment`/attachments-dir section entirely rather than
   leaving two competing descriptions of how the file gets persisted.

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

## Open items for implementation

- Exact card visual spec (icon-per-filetype vs. one generic document icon; whether the "Download"
  button's chevron-dropdown from the reference — implying format alternatives — is worth copying
  when v1 only ever has one format per artifact).
- `ArtifactViewer`'s wide/narrow breakpoint — reuse whatever breakpoint the rest of the app already
  treats as "phone vs. desktop" rather than picking a new one.
- Whether `show`'s tool description (`tools/descriptions/show.yaml`) needs updating to actively
  suggest itself for "write this to a file and show it to me"-shaped requests, now that it's not
  just for images.
- Update `docs/plans/report-generator.md`'s "Output shape" section to match this doc once
  implementation actually starts, rather than leaving two design docs disagreeing about how a
  report gets persisted.
