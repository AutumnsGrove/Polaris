# Interactive maps: pins, drawn-on annotations, and annotated images

**Added: 2026-09-30.**

**Status: proposed — design mockups approved, nothing built.** Mockups (four phone screens, clickable
pins/toggles) live in a private Claude artifact: <https://claude.ai/artifact/FwCFagzugkgKf1uBVmQJyj>.
Tracked in the GitHub issue filed alongside this doc.

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
- **Geographic:** MapLibre GL JS vs Leaflet is an open decision (below). Layers are real map layers,
  which is what makes toggles instant.
- **Image:** a small pan/zoom viewer with an SVG annotation overlay in image pixel space; extends or
  sits beside `ImageLightbox.svelte` rather than duplicating it.
- Expand button opens the full-screen state (screen 2). Marker ↔ list selection state lives in a
  `*.svelte.ts` class per `docs/STANDARDS.md`'s Svelte conventions.
- UI must use the `app.css` tokens (`--z-*`, `--radius-*`, `--space-*`, existing color vars), and a
  glossary entry in `HelpModal.svelte`'s `TERMS` (e.g. "Map card = an interactive map the assistant
  draws on").
- Theme: map style switches with the UI theme (dark default), same idea as `CodeExecThemePrompt`.

### The model can't see a live map

`view_image` reviews pixels; an interactive map has none server-side. Options, in order of preference:

1. **Trust the data.** Coordinates from geocoding/Foursquare are exact; the model's job is choosing
   what to draw, not where pixels land. Good enough for markers/rings/routes.
2. **Snapshot for self-review (later).** A server-side static render (tile stitch + the same shapes
   via Pillow, or a headless render) written to the workspace so `view_image` `see` can check "does
   this look right" — also doubles as the fallback for non-browser surfaces (CLI `polaris search`,
   Pulsar Daily). Deliberately deferred: build it only if step 1 proves insufficient.

### Tiles and the network

- The frontend fetches tiles **from the user's browser**, so there is no server-side SSRF surface for
  tile fetching, but the tile host must be allowed by whatever CSP/proxy setup the app has — check
  before building.
- Public `tile.openstreetmap.org` is for light use with attribution; verify its **current** usage
  policy before shipping. Make the tile URL template a config field (`config.yaml`) so a keyed provider
  or a self-hosted tile server can be swapped in without a code change. Always render the attribution
  string.
- If a server-side snapshot is later built, it needs its own tile cache, a per-call tile cap, a real
  User-Agent, and `SafeDialContext` like the other fetchers.
- Tailscale-only phone use means tile requests go over the phone's normal connection, not through the
  Polaris host — nothing to proxy unless CSP forces it.

### Image kind specifics (the GW2 case)

- Sources: workspace file (from `fetch_url`, which already lands images there) or remote `url`.
- The **original is never modified.** The annotated result is an overlay rendered client-side; "Save to
  Field" / download writes a flattened copy into the workspace.
- GW2's own map is a tile pyramid (`tiles.guildwars2.com/{continent}/{floor}/{zoom}/{x}/{y}.jpg`) with
  coordinates from `api.guildwars2.com/v2/continents`/`maps`/`pois`. Wiki region maps are ordinary
  images. v1 handles ordinary images only; a GW2 tile source is a later, optional extension, not a
  requirement of this design.

## Implementation phases

1. **Spike (live, per repo culture).** `curl` a few OSM tiles and confirm marker/circle placement math
   against known coordinates; prototype `MapCard` with hardcoded data in the real app; decide
   Leaflet vs MapLibre from an actual phone.
2. **Tool + plumbing.** `tools/show_map.go` (+ `descriptions/show_map.yaml`, catalog entry, tests),
   event schema, persisted-event threading, `web/src/lib/types.ts`.
3. **Frontend.** `MapCard` inline + expanded; marker/list selection; layer toggles; theming.
4. **Image kind.** Pan/zoom viewer + SVG overlay; flatten/save.
5. **Live verification.** Drive the real app with `dev/fakeopenrouter` queuing scripted `show_map`
   calls (including an update-by-id turn and a hard reload) via Playwright, then a real-model pass on
   the potato. Confirm a disabled tool isn't offered (`/_control/calls`).
6. **Docs.** `README.md` Features line, `HelpModal` `TERMS` entry, `SETUP.md` if a tile key/URL config is
   added, `DEVELOPMENT.md` only if architecture notes change.
7. **Optional:** static snapshot for model self-review / non-browser surfaces; GW2 tile source.

## Open decisions

- **Leaflet or MapLibre GL JS.** Leaflet: small, simple, raster tiles, fine on phones. MapLibre:
  smoother, vector tiles/rotation, noticeably heavier. Decide in the spike on real hardware.
- **Tile provider default.** Public OSM tiles vs a keyed provider vs self-hosted. Ship with a
  configurable URL either way.
- **Is the static snapshot worth building** for model self-review, or is trusting the data enough?
- **Pins from `nearby_search`.** Should `nearby_search` results auto-offer a map card, or only when
  the model calls `show_map` explicitly? (Leaning explicit, to keep answers calm.)
- **Per-turn cap** on `show_map` calls. `show` has none; a map is heavier than an image, so a small cap
  may be reasonable.
- **Persistence size.** Marker/shape payloads are stored in the transcript; cap counts to keep
  events small.

## Non-goals

- Turn-by-turn navigation, live traffic, or a full Maps replacement — "Directions" hands off to an
  external maps link like `nearby_search` already does.
- User-drawn annotations (v1 is model-drawn, viewer toggles only).
- Any server-side image editing by the model as the primary path (`code_exec` + Pillow still works for
  one-off custom drawing on a workspace image).
