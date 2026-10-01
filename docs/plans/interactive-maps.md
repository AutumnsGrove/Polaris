# Interactive maps: pins, drawn-on annotations, and annotated images

**Added: 2026-09-30.**

**Status: proposed — design mockups approved, nothing built.** Mockups (four phone screens, clickable
pins/toggles) are saved at `mockups/interactive-maps.html` (standalone, open in a browser); the
original canvas is also a private Claude artifact: <https://claude.ai/artifact/FwCFagzugkgKf1uBVmQJyj>.
Tracked in issue #143.

## The idea

Ask "where can I get coffee near the Space Needle?" or "circle the meta event area on this Guild Wars 2
region map" and get an **interactive** map/image card in the answer: pan and zoom with a thumb, tap a pin
for details, toggle the model's drawings on and off. Kagi/Claude-style "pinpoint this for me" — but
built on what Polaris already has, not a bolt-on.

Two surfaces, one primitive:

1. **Geographic map** — real tiles, markers, radius circles, routes, labels.
2. **Image canvas** — any image (a wiki region map, a screenshot, a chart) as a zoomable base with
   pixel-space annotations. This is the general-purpose answer to the original GW2 request: GW2 stops
   being a special case and becomes "an image with a circle on it."

## Mockups (what "done" looks like)

| # | Screen | Shows |
|---|--------|-------|
| 1 | Map card in chat | Inline card under the answer; numbered pins; tapping a pin selects it and updates the footer (name, walk time, Directions); expand button; OSM attribution |
| 2 | Expanded map | Full-screen map, search pill, zoom/recenter, bottom sheet listing places; pins and rows select each other; sheet collapses/expands |
| 3 | Drawn-on annotations | Radius ring ("10 min walk"), dashed route with time label, an out-of-range pin greyed; per-layer toggles (Radius / Route / Labels) |
| 4 | Any image | Region map with a hand-drawn circle, arrow and label; "Annotations" toggle; caption states it's an annotated copy, original untouched |

Visual constraints already baked in (see `PRODUCT.md`): warm dark neutrals, gold accent for pins and
"current state", pale blue-white for the landmark/route (`--color-accent-2`), no neon, no gradient
chrome, touch targets ≥44px, phone-first.

## Why interactive, not a static PNG

An earlier sketch had the server (or `code_exec`) stitch tiles and draw with Pillow, then
`show`/`view_image` the PNG. That works and stays valuable as a fallback (below), but it's the wrong
primary experience: no pan/zoom, no tapping, and every tweak ("also show the second one") is a full
re-render. Pixel placement also isn't the model's job — it should emit **coordinates as data** and let
the frontend draw.

## Design

### Tool: `show_map`

One tool, structured arguments, no image generation on the model's side.

```
show_map({
  kind: "map" | "image",
  // kind = "map"
  center?: "place name" | "lat, lon",      // geocoded via places/geocode.go (Nominatim)
  bounds?: [[lat,lon],[lat,lon]],
  zoom?: int,
  // kind = "image"
  path?: "workspace/relative.png" | url?: "https://...",   // same sources as show.go
  // both kinds
  markers?:  [{ id, lat|x, lon|y, label, kind?: "place"|"landmark"|"muted", detail?, source_index? }],
  shapes?:   [{ id, type: "circle"|"polyline"|"polygon"|"arrow", ... , label?, layer }],
  layers?:   [{ id, label, default_on }],
  title?: string
})
```

- Geographic shapes use lat/lon (+ `radius_m` for circles); image shapes use pixel coordinates in the
  image's own space, so zoom/pan can't misalign them.
- `layers` is what the toggle chips in screen 3/4 bind to; every shape names one.
- Like `show`, this is a **display action**: it emits a `tool_call`/`tool_result` pair whose data the
  frontend renders, and never touches the model's context by itself.
- Marker `id`s let a follow-up call **update** an existing card ("add the fourth one") instead of
  stacking duplicates.
- Reuse, don't redefine: geocoding = `places/geocode.go`; nearby results = `nearby_search`/Foursquare
  lat/lon; image sources and traversal-safe path resolution = `resolveWorkspaceFilePath`
  (`tools/view_image.go`); image blocklist check = same as `show.go`'s `url` mode.

### Frontend: `MapCard.svelte`

- New component under `web/src/lib/components/`, rendered by a `ToolEvent.svelte` branch (same pattern
  `show` used) and reloaded from persisted events (`buildTimelineFromEvents` must thread the new
  fields through — this exact gap was caught live for `show`).
- **Geographic:** Leaflet (decided; see Decisions). Layers are real map layers, which is what makes
  toggles instant.
- **Image:** a small pan/zoom viewer with an SVG annotation overlay in image pixel space; extends or
  sits beside `ImageLightbox.svelte` rather than duplicating it.
- Expand button opens the full-screen state (screen 2). Marker ↔ list selection state lives in a
  `*.svelte.ts` class per `docs/STANDARDS.md`'s Svelte conventions.
- UI must use the `app.css` tokens (`--z-*`, `--radius-*`, `--space-*`, existing color vars), and a
  glossary entry in `HelpModal.svelte`'s `TERMS` (e.g. "Map card = an interactive map the assistant
  draws on").
- Theme: map style switches with the UI theme (dark default), same idea as `CodeExecThemePrompt`.

### Event schema and persistence (read from the code 2026-10-01)

`show` needed no new persistence machinery, and neither does this, but the threading has to be exact:

- `tool_call` carries the model's raw `args`; `tool_result` carries a **server-resolved `map` object**
  (new field, alongside `url`/`caption`/`images`). The card renders from `map`, never from `args`:
  geocoding (`center: "place name"` -> lat/lon), update-by-id merging, and defaults all happen in the
  handler, so the stored result is the full, final state of the card.
- `map` shape: `{ kind, title, view: {center, zoom} | {bounds}, markers[], shapes[], layers[],
  image?: {url, width, height}, snapshot?: "map-1.png", attribution }`.
- Persisting and replaying `tool.show_map` events means adding `map` in **four** places or reload
  silently renders a bare chip (the exact gap `show` hit): `ServerEvent`'s `tool_result` and
  `TimelineItem` in `web/src/lib/types.ts`, `applyStreamingEvent` in `web/src/lib/turnEvents.ts`, and
  *both* match branches (call_id and name fallback) in `buildTimelineFromEvents`
  (`web/src/lib/stateHelpers.ts`), which duplicate the field copy: extract it rather than add a fifth
  copy.
- **Size:** `store.truncateEventStrings` (20,000-byte `maxEventDataBytes`) only trims *top-level
  string* values. A nested `map` object is stored untruncated, so the 50-marker / 30-shape cap in the
  handler is the only thing bounding it. Because `args` is also persisted ("tool call started"), a
  call stores roughly 2x its payload; the caps have to be sized with that in mind.
- **Update-by-id** re-emits a *complete* merged `map` in the new result (the card for that call updates
  in place; it does not diff on the client), so a card is always reconstructible from its own event.
- **Catalog:** append `show_map` at the *end* of `catalogOrder` (`tools/catalog.go`), like
  `save_to_field`: the order is the wire-format tool list that prompt-prefix caching depends on, so a
  mid-list insert shifts every later tool.
- **Per-turn cap (3):** there is no existing per-turn call counter to reuse, so `tools.Context` gets a
  small mutex-guarded counter (same pattern as `SetShow`); over-cap returns an error result through
  the existing `showError`-style helper (result only, no second `tool_call`).

### Snapshot: every `show_map` call also saves a static image

The model can't see a live interactive map, and `view_image` reviews pixels. Rather than build a
separate review path, **`show_map` always renders a static snapshot server-side, saves it to the
thread's workspace, and names the file in its tool result** (e.g. `snapshot: map-1.png`). Then
`view_image` `path: "map-1.png"` (`see` mode) works with no extra plumbing, and `show`/Save to Field can
use the same file.

- **Rendered in the handler, synchronously**, so the file exists before the model's next step. (A
  browser-side canvas export was rejected: it only exists once the card loads, and non-browser surfaces
  like the CLI's `polaris search` or Pulsar Daily would get nothing.)
- **Geographic:** fetch the tiles covering the bounds, stitch, draw markers/shapes/labels in Go using
  the same Web Mercator math the spike validates. Pillow is not an option here: it lives only in the
  network-less sandbox, and this runs in the Polaris process. Library choice (stdlib `image` +
  `golang.org/x/image`, or a small 2D drawing lib) is settled in the spike.
- **Image kind:** base image plus shapes flattened in pixel space; also what "Save to Field" writes, and
  the original file is untouched.
- The snapshot is a **flat render of all default-on layers**, not of the viewer's current toggle state.
- Cost of this choice: a second renderer to keep visually consistent with the Leaflet card, and a
  server-side tile fetch (below). Both accepted for the value of one uniform review/fallback path.

### Tiles and the network

- **Browser side:** the Leaflet card fetches tiles from the user's browser; the tile host must be
  allowed by whatever CSP/proxy setup the app has — check before building.
- **Server side (snapshot):** the handler fetches tiles itself, so it needs `SafeDialContext` like the
  other fetchers, a per-call tile cap (~16), an on-disk/in-memory tile cache, and a real `User-Agent`.
  These requests originate from the potato, so the public OSM policy applies to them directly.
- **Two tile sources, not one (spike finding, 2026-10-01).** OSM's policy
  (<https://operations.osmfoundation.org/policies/tiles/>) permits interactive viewing where the client
  requests only the current viewport's tiles, but forbids "headless bot rendering" and bulk/offline use
  on `tile.openstreetmap.org`, with violators "blocked without notice." So:
  - **Live card (browser):** public OSM by default, URL template in `config.yaml`. Compliant as long as
    Leaflet only requests the viewport and the Referer header isn't suppressed (no restrictive
    `Referrer-Policy`). Attribution must be visible, not behind a toggle.
  - **Snapshots (server):** a *separate* configurable template (`maps.snapshot_tile_url`) pointing at a
    keyed free-tier provider or self-hosted server, never public OSM. If unset, the snapshot is skipped
    and the tool result says so rather than silently hitting OSM.
- Always render the attribution string, on the card and stamped onto snapshots (the snapshot provider's
  own attribution, which may differ from OSM's).

### Tile cache

One cache **shared by every chat**, persisted across restarts, so a place looked up once is free next
time.

- **Location: its own directory in the `polaris-data` named volume** (`/data/tile-cache`, config
  `tile_cache.dir`, defaulting to a `tile-cache` folder next to `database.path` the way `backups` does),
  *not* under `workspaces/`. `workspaces/` is the host bind mount (mode 777, cross-uid, see
  `install.sh`) that exists so the sandbox can see per-thread files; the cache is only ever touched by
  the Polaris process, and a top-level `workspaces/tile-cache` would sit in the same namespace as
  thread/field IDs that `resolveWorkspaceFilePath` treats as roots. Snapshots themselves still go in
  the *thread's* workspace, since `view_image`/`show` need to read them.
- **Layout:** one file per tile keyed by source + z/x/y (e.g. `<source-hash>/<z>/<x>/<y>.png`), so
  switching the tile URL template never serves another provider's tiles.
- **Recency:** a cache hit bumps the file's mtime (not atime — `noatime`/`relatime` mounts make atime
  unreliable). A tile idle for **30 days** is deleted. Because every use refreshes the clock, areas you
  keep coming back to stay indefinitely and one-off places age out on their own; no separate
  "important area" logic is needed.
- **Pruning:** a once-a-day sweep in the same no-external-cron style as `backup.go`'s snapshot job (or
  piggy-backing on the Pulsar scheduler's tick), removing files past the 30-day idle cutoff.
- **Size backstop:** an overall size ceiling (default 1 GB, configurable) evicting least-recently-used
  first, since the potato's disk is finite and a busy week of map lookups shouldn't be able to fill it.

### Image kind specifics (the GW2 case)

- Sources: workspace file (from `fetch_url`, which already lands images there) or remote `url`.
- The **original is never modified.** In the app the annotations are an overlay rendered client-side;
  the server-side snapshot (above) is the flattened copy that lands in the workspace and is what "Save
  to Field" uses.
- GW2's own map is a tile pyramid (`tiles.guildwars2.com/{continent}/{floor}/{zoom}/{x}/{y}.jpg`) with
  coordinates from `api.guildwars2.com/v2/continents`/`maps`/`pois`. Wiki region maps are ordinary
  images. v1 handles ordinary images only; a GW2 tile source is a later, optional extension, not a
  requirement of this design.

## Implementation phases

1. **Spike (live, per repo culture).** `curl` a few OSM tiles and confirm marker/circle placement math
   against known coordinates; prototype `MapCard` (Leaflet) with hardcoded data in the real app on a
   real phone; pick the Go drawing approach for snapshots.
2. **Tool + plumbing.** `tools/show_map.go` (+ `descriptions/show_map.yaml`, catalog entry, tests),
   event schema, persisted-event threading, `web/src/lib/types.ts`; server-side snapshot renderer
   (tile fetch + cache, stitch, draw) writing to the workspace and naming the file in the result;
   per-turn and payload caps.
3. **Frontend.** `MapCard` inline + expanded; marker/list selection; layer toggles; theming.
4. **Image kind.** Pan/zoom viewer + SVG overlay; flatten (shared with the snapshot path) and save.
5. **Live verification.** Drive the real app with `dev/fakeopenrouter` queuing scripted `show_map`
   calls (including an update-by-id turn and a hard reload) via Playwright, then a real-model pass on
   the potato. Confirm a disabled tool isn't offered (`/_control/calls`).
6. **Docs.** `docs/FEATURES.md` entry, `HelpModal` `TERMS` entry, `SETUP.md` if a tile key/URL config is
   added, `DEVELOPMENT.md` only if architecture notes change.
7. **Optional:** GW2 tile source.

## Decisions

Settled 2026-09-30:

- **Leaflet**, not MapLibre GL JS: small, raster tiles, enough for pins/circles/routes/toggles, lower
  risk on a phone browser. Revisit only if it feels bad on a real phone in the spike.
- **Public OSM tiles by default, URL template configurable** in `config.yaml`. Verify current OSM usage
  policy before shipping.
- **Every `show_map` call auto-saves a static snapshot** to the workspace, rendered server-side in the
  handler, with the filename in the tool result so `view_image` can review it (see "Snapshot").
- **`nearby_search` never auto-attaches a map card.** Only an explicit `show_map` call does; prompt
  guidance nudges the model toward `show_map` for location questions.
- **Per-turn cap: 3 `show_map` calls.** Heavier than `show` (tiles + render); an over-cap call returns a
  clear "already showed N maps this turn" error.
- **An update-by-id call writes a new snapshot filename** (`map-1.png` -> `map-1-v2.png`) so
  `view_image` never reads a stale file; earlier versions stay in the workspace.
- **Shared tile cache** across all chats, persisted, entries idle for 30 days pruned, recency refreshed
  on every hit (see "Tile cache"; lives in the `polaris-data` volume, not `workspaces/`).
- **Payload cap: 50 markers and 30 shapes per call**, with a "trim it down" error beyond that, to keep
  persisted transcript events small.

Settled 2026-10-01 (spike):

- **Go snapshot renderer: stdlib `image`/`image/draw` + `golang.org/x/image` only, no 2D drawing lib.**
  Validated against a real tile: Web Mercator projection put a pin for the Space Needle
  (47.6205, -122.3493) at z15 inside tile 5247/11442 at pixel (126.7, 16.4), visually on the landmark;
  `radius_m` circles size via `metres_per_pixel = 156543.03392 * cos(lat) / 2^z`. Rings are per-pixel
  distance-falloff antialiased, pins filled discs, polylines thick Bresenham. Still to pick when building:
  an embedded TTF via `x/image/font/opentype` (`basicfont` 7x13 is too small/ugly for labels).
- **Snapshots never fetch from public OSM** (policy bans headless rendering); they use a separate
  configurable tile source. Live card keeps public OSM as the default.

## Still open

- **Snapshot tile provider: Geoapify is the leading candidate (2026-10-01 research), not yet final.** It
  is the only hosted provider found that explicitly permits caching/storing tiles (its FAQ, not its formal
  Terms, so get that in writing from support before shipping). Free tier 3,000 credits/day at 0.25 credit
  per tile (~4 credits per 16-tile snapshot); `dark-matter` style; 256px XYZ
  `https://maps.geoapify.com/v1/tile/{style}/{z}/{x}/{y}.png?apiKey=KEY`; attribution "Powered by
  Geoapify | © OpenStreetMap contributors". The key rides in the URL query, so the renderer/cache key
  (source hash) and logs must exclude it. Rejected for banning server-side caching/proxying: Stadia,
  MapTiler, CARTO, Thunderforest, HERE (Mapbox unclear). Fallback: self-hosted OSM tile server (heavy on
  the potato).
- Live-card finding: OSM returns "Access blocked" tiles to any page without a Referer, e.g. a `file://`
  mockup, so the real card must be served over http(s) and never set a restrictive `Referrer-Policy`.

## Non-goals

- Turn-by-turn navigation, live traffic, or a full Maps replacement — "Directions" hands off to an
  external maps link like `nearby_search` already does.
- User-drawn annotations (v1 is model-drawn, viewer toggles only).
- Any server-side image editing by the model as the primary path (`code_exec` + Pillow still works for
  one-off custom drawing on a workspace image).
