# show: a big, inline artifact viewer — one step above highlight

**Status: designed, not built.** Filed against issue #44 (code-generated chart rendering), which
this supersedes the rendering-path recommendation of — that issue originally suggested riding the
existing attachment-image path with no new mechanism; the handoff doc after `code_exec` shipped
proposed reusing `highlight`'s card machinery instead; this doc supersedes *that* in favor of a
dedicated tool, for the reasons below. Depends on nothing unbuilt — `code_exec`'s workspace and
`view_image`'s `path`-resolution pattern (`docs/plans/view-image.md`) are both shipped and
live-verified already; `show` reuses the same workspace-file contract, not new plumbing.

## The gap

Confirmed directly in the frontend code (`web/src/lib/components/ChatTurnView.svelte`): every
existing visual output — `image_search`'s gallery, `highlight`'s grid, `visualize`'s chart — lands
in `turn.cards`, bucketed by `Kind` and rendered as fixed blocks **after** the message's prose,
regardless of when in the tool-call sequence they were produced. That's a deliberate, working
design for *galleries of comparable things you scan together* (highlight's own doc comment: "no
idea what a product is," always has a `url` — it exists for Shopper mode's link-out grid).

It's the wrong shape for a different, newer need: a single artifact — today a `code_exec`-generated
chart, later a fetched image, eventually a full interactive HTML document — that IS the point of
the specific call that produced it, and should render large, right where that call happened in the
conversation, not queued into an end-of-turn bucket alongside unrelated cards.

Two ways to close this gap were considered and rejected in favor of a new tool:

- **Ride the existing attachment-image path** (issue #44's original suggestion) — moot: confirmed
  live that an attachment today only ever renders as a filename chip, the bytes are never re-served
  or shown inline at all. There's no existing path to ride.
- **Rework `highlight`** so a single item renders bigger and inline instead of in the end-of-message
  grid — rejected. `highlight` and this need aren't the same concept: `highlight` is *N comparable
  things*, `show` is *one artifact, shown where it was made*. Making placement conditional on
  `len(items) == 1` would bolt an unrelated UI mode onto a tool whose schema, prompt, and only real
  caller (Shopper mode, live in production) all assume the gallery shape — a magic-threshold branch
  in a shipped tool, not a clean extension. It also doesn't survive the stated future direction:
  `highlight` is semantically "a link-out card with a price"; stretching that into "also maybe an
  embedded live HTML document" contorts what it means for no reason a dedicated tool doesn't share.

## The tool: `show`, path-only, display-only, uncapped

Deliberately the smallest tool that closes the gap for the actual case at hand (an image today),
with a scoped, honest growth path instead of speculative generality:

- **Source: a workspace path only** (`<CodeExecWorkspaceDir>/<ThreadID>/<path>`, the exact same
  resolution `view_image`'s `path` parameter already implements — `readWorkspaceImageBytes`'s
  `filepath.Rel` traversal-check pattern is directly reusable). No `card_index` alternative — an
  `image_search` result already has a home (the gallery); `show` is scoped to "something this
  thread produced," matching the motivating case and keeping the tool's job crisp. Revisit only if
  real usage shows a genuine want to promote a single search result to inline display.
- **Purely a display action — no effect on the model's own context.** `show` never touches
  `ChatMessage`/`ImageURLs` the way `view_image`'s `see` mode does. It only ever emits a
  `tool_call`/`tool_result` pair whose data carries a URL for the frontend to render — the same
  "the model gets a URL/snippet, not raw pixels" shape `image_search` already uses today. If the
  model needs to actually judge the image itself, it calls `view_image` on the same path separately
  — two tools, two jobs, no coupling. This also means none of `view-image.md`'s "resent on every
  subsequent turn" token-cost concern applies here at all: nothing about a `show` call grows what
  gets replayed to the model later.
- **No cap on calls per turn.** Unlike `highlight`'s top-N-of-interchangeable-candidates framing
  (which is exactly why it rejects over 5 rather than silently truncating), each `show` call is a
  distinct, deliberate artifact — there's no natural "pick your best 3" framing to cap against.
  Revisit only if real usage shows a turn rendering an unwieldy wall of large embeds.

Tool schema, mirroring `view_image`'s existing shape:

```
show(path: string, caption?: string)
```

`caption` is optional, passable model-supplied context (e.g. "regression fit for the sales data")
— the frontend can render it as a label under the artifact, and it also becomes the accessible
alt-text description, since nothing else in the transcript describes a `show`n image's actual pixel
content the way `view_image`'s `describe` mode does for a card. Optional rather than required
because it's often genuinely redundant with the surrounding prose that led up to the call — but
worth keeping available rather than dropping it entirely, since the opposite case is real too: a
chart or diagram that's visually self-contained (axis labels and a title baked into the image
itself, nothing else to say) still benefits from a one-line caption purely as self-description when
nothing else in the turn's text names what the artifact actually is.

## Mechanism: reuse the generic tool-timeline, not the Card/bucket system

The actual placement fix comes from a fact confirmed directly in `web/src/lib/types.ts` and
`web/src/lib/state.svelte.ts`: `TimelineItem`'s `{kind: 'tool', tool, args, result, callId}` shape
is already fully generic, and `buildTimelineFromEvents` already reconstructs it from persisted
`tool.<name>` events (`tool call started` / `tool call finished`) on every page reload — the exact
same mechanism every other tool's compact chip already uses. `show` needs **zero new persistence or
reconstruction machinery**: it's a normal `Register("show", handleShow)` tool emitting the same
`ctx.Emit("tool_call"/"tool_result", ...)` pairs every tool in this package already emits.

The only real new pieces:

1. **`tools/show.go`** — resolves the path via the same traversal-checked join `view_image.go`
   already implements (worth factoring the shared logic out of `readWorkspaceImageBytes` at that
   point, rather than duplicating it), and emits a `tool_result` whose `data` carries a URL (see
   #2) and the caption, instead of returning raw bytes to the model at all.
2. **A small new HTTP route** (something like `GET /api/workspace/{thread_id}/{filename}`,
   `gateway/workspace.go`) that serves a workspace file's bytes by thread+filename — genuinely new;
   confirmed nothing today serves an attachment or workspace file back over HTTP at all. Needs the
   same path-traversal defense as the Go tool handler (don't trust the frontend to only ever request
   what a real `show` call produced), and should validate/sniff content-type the same way
   `view_image`'s `fetchImageBytes`/`readWorkspaceImageBytes` already do rather than trusting a file
   extension.
3. **`ToolEvent.svelte`** learns a `item.tool === 'show'` branch that renders a real inline embed
   (an `<img>` pointed at the new route, sized deliberately larger than `HighlightGrid`'s or
   `ImageGallery`'s grid tiles — this is explicitly the "one step up" from highlight in scale, not
   just placement) instead of falling through to the default compact text-label chip every other
   tool gets.
4. **Reuse `ImageLightbox.svelte` as-is** for tap-to-expand — `ImageGallery.svelte` already wires
   exactly this interaction (tap a tile, open a full-screen zoomable preview) against the same
   `Card`-shaped `{title, imageUrl, ...}` data `show`'s tool-result payload can trivially match, so
   the expand-on-tap behavior the two of you already want costs nothing new to build. This is also
   where a future HTML artifact's "open as a proper interactive site" idea slots in later: today the
   lightbox always renders an `<img>`; a future artifact `kind` on the same payload would let it
   branch to a sandboxed `<iframe>` instead, without changing how `show` calls or timeline placement
   work at all — the lightbox becomes the one place that needs to know about a new artifact type.

## Open items, deliberately not resolved here

- Exact route/path naming and auth posture for the new workspace-serving route — this is a
  single-operator, no-user-auth deployment today, but the route should still validate thread
  ownership shape (a well-formed thread ID, a file that actually exists under that thread's
  workspace) the same defensive way `view_image`'s path resolution already does, not trust the
  frontend's URL construction blindly.
- Sandboxed-iframe security posture for future HTML artifacts (`allow-scripts` without
  `allow-same-origin`, no top-navigation, size/resource ceilings) — explicitly deferred until that
  artifact type is actually being built, matching this codebase's stated preference for not
  designing speculatively ahead of real need.
- Whether `code_exec`'s own tool description should proactively suggest calling `show` after
  `savefig`-ing a chart (teaching the model the two-call pattern), versus leaving it purely
  opportunistic. Worth revisiting once there's real usage to look at.
