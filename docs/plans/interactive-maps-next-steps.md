# Interactive maps: where we stopped, and what's next

**Written: 2026-10-01.** Companion to `interactive-maps.md` (the design) and issue #143.

**Status: deliberately paused.** The operator decided this is further out of scope than they care to
take it right now, possibly ever. Everything below is the state of the work so a later session (or
nobody) can pick it up cold. It lives in a **draft PR that should not be merged as-is** — see "Before
anyone merges this".

## What exists

All of it is on the draft PR's branch, in order:

| Piece | Where | State |
|---|---|---|
| `show_map` tool (validation, caps, geocoding, same-turn update-by-id, event payload) | `tools/show_map.go`, `tools/descriptions/show_map.yaml` | Done, unit-tested |
| Snapshot renderer (Web Mercator, tile stitch, pins/rings/routes/labels, attribution) | `mapsnap/` | Done, unit-tested |
| Shared tile cache + daily sweeper | `mapsnap/cache.go`, `cmd/run.go` | Done, unit-tested |
| Snapshot wiring into `show_map` (writes `map-xxxxxx.png`, `…-v2.png` on update) | `tools/show_map_snapshot.go` | Done, unit-tested |
| Gateway: carries the card on the WebSocket event and in the persisted event | `gateway/protocol.go`, `turn_emit.go` | Done, tested |
| `maps:` config + `GEOAPIFY_API_KEY` in the example configs/compose | `config/config.go`, `*.example`, `docker-compose.yml` | Done |
| Frontend: `MapCard.svelte` (Leaflet), selection state, `toolResultFields` helper | `web/src/lib/` | Works, **looks mediocre** |
| Plan, spike findings, mockups | `docs/plans/interactive-maps.md`, `mockups/` | Done |

## What was verified live

- A real model (DeepSeek via OpenRouter) called `show_map` unprompted, got the snapshot filename back,
  and then called `view_image` on its own snapshot — the intended review loop works end to end.
- The snapshot rendered 12 real Geoapify `osm-bright` tiles with 0 missing; pins and an 800 m ring
  landed correctly on the Space Needle / Seattle Center. The operator opened the PNG and liked it.
- A reopened thread rebuilt the card from its persisted event (the "fine live, bare chip on reload"
  bug that `show` once hit did not recur).
- A live run caught a bug no unit test could: `ServerEvent` copies tool-payload fields by name, so the
  `map` field was silently dropped at the gateway until added in three places
  (`gateway/turn_emit_map_test.go` now pins that).

**Not verified:** the card streaming live in a browser during a turn (only unit-tested), a Playwright
pass, a phone, and a real-model pass on the potato.

## Why it's paused: the card doesn't look good

Operator's first reaction, compared side by side with Claude's own map card: "not bad, not great."
Specifically:

1. **Pins are buried.** Every pin has a permanent name label sitting on top of it; with close pins,
   labels hide the numbered discs. The ring's label lands on top of pins too. A drawn route would be
   hidden the same way (Leaflet draws tooltips above vector layers).
2. **The basemap is muddy.** The live card inverts a light OSM raster with a CSS filter. Raster tiles
   are pre-drawn pictures, so recolouring them means a pixel filter; the result is low-contrast and
   washed out.
3. **Heavy chrome.** Large chips, a "Tap a pin" footer, and a long stacked list under the map. Claude's
   card uses compact rating-badge pins and a floating side panel with thumbnails.
4. **The card appears before the model has looked at it.** `show_map` renders the snapshot and emits the
   card in one call, so the user sees a first draft, and an update-by-id adds a *second* card under it
   instead of replacing it.

## The open decision: which basemap

Gathered 2026-10-01. Pricing is from Google's pricing page; the Google licensing points come from a
search summary because the primary terms pages would not load, so treat them as likely, not certain.

| | Google Maps JS API | MapLibre (vector) | Keep Leaflet + OSM raster |
|---|---|---|---|
| Looks like Claude's | Most closely | Close if styled well; can match Polaris's palette | The current "just OK" |
| Cost | 10,000 free map loads/month, then $7 per 1,000 | Free | Free |
| Privacy | Every view goes to Google | Stays with us | Tiles from OSM |
| Needs | A Google Cloud billing account; key lives in the browser (restrict to your hosts, set a quota cap) | A vector tile source whose terms allow it (Geoapify styles on the existing key, or OpenFreeMap) — **neither verified** | Nothing |
| Effort | Least | Most (different library from Leaflet) | None |

Notes that apply to every option:

- **The snapshot cannot use Google imagery.** The Map Tiles terms reportedly prohibit image analysis
  and machine interpretation; the snapshot exists so a model can read it. It stays on Geoapify
  (which explicitly allows caching) whatever the live card uses. Only the model sees it, so the two
  looks needn't match.
- **Public OSM is for the live card only** (a user's own browser, viewport tiles, with a Referer —
  a `file://` page gets "Access blocked" tiles). The server must never fetch from it; this is
  enforced in `tools.NewMapSnapshot`.
- The plan chose Leaflet over MapLibre "unless it feels bad on a real phone". It looks bad enough
  that this is worth revisiting. Recommendation at pause time was **MapLibre**, because the private,
  self-hosted pitch is the product; Google is a legitimate choice if speed to polish matters more.

## If this is picked up again: suggested order

1. **Fix the pins and labels first** (cheap, biggest visible win, independent of the basemap): no
   permanent name labels (numbers on pins; names on select and in the list); shape labels only at
   readable zoom or in the legend; keep routes above labels.
2. **Add `preview: true`** to `show_map` (render the snapshot only, show no card) and make a later
   version of the same `map.id` replace the earlier card in the timeline instead of stacking.
3. **Decide the basemap** (table above), then swap it. If MapLibre: verify the vector tile source's
   terms first; a server-side tile proxy through the shared cache is possible but only for a provider
   that allows caching.
4. **Richer pins and list:** optional marker fields (`badge` such as a rating, `image_url`, `url`),
   which `nearby_search` already has the data for; a floating side panel on wide screens and a bottom
   sheet on phones.
5. **The rest of the original plan:** `kind: "image"` pan/zoom viewer + SVG overlay (phase 4; image cards
   currently render a plain `<img>` plus the pin list and get no snapshot); label collision avoidance;
   a `config.yaml` tile URL for the live card (it is a hardcoded constant in `MapCard.svelte`);
   `source_index` (pin -> citation link, dropped from v1); cross-turn update-by-id (cards only update
   within the turn they were shown in).
6. **Docs and verification:** `HelpModal` `TERMS` entry, `docs/FEATURES.md`, `SETUP.md` (the optional
   Geoapify key), a Playwright pass with `dev/fakeopenrouter`, a phone check, and a real-model pass on
   the potato. None of these were done, deliberately: the feature isn't shipping.

## Before anyone merges this

- **`show_map` is offered to the model by default** once merged (it's in `catalogOrder` and not
  `Requires`-gated). With the current card quality, either keep it off by default (e.g. a `Requires`
  gate on the maps config) or finish step 1 first. It is toggleable per user in Settings > tools.
- Snapshots only work where `code_exec`'s workspace is configured (`view_image` reads the same
  directory); elsewhere the card still shows and the tool result says no snapshot was made.
- `go.mod` gained `golang.org/x/image` (the embedded font), and `web/package.json` gained `leaflet`
  + `@types/leaflet`.

## How to live-test it again

The first live test used an isolated second instance so nothing touched real data. Recipe:

1. Write a **fresh minimal** config (do not copy `config.yaml` wholesale — see the incident below) with
   its own port, `database.path`, `attachments.dir`, `logging.dir`, `code_exec.workspace_dir` /
   `host_workspace_dir` / `signal_dir` under a temp directory, a `maps:` block, and `${OPENROUTER_API_KEY}`
   / `${GEOAPIFY_API_KEY}` placeholders, with **no `r2:` block**.
2. Export the two keys into the process environment only, build (`go build`; the binary embeds
   `web/build`, so run `cd web && pnpm run build` first for the UI) and run `polaris run --config <it>`
   without `--dev`.
3. Drive a turn over the WebSocket (`/ws`, `{"type":"message","content":...,"model":...}`) or the UI. Give
   the model the pin coordinates in the prompt so it doesn't depend on search, and tell it to check the
   snapshot.
4. Needs a shell that can reach `maps.geoapify.com`. (Claude Code's sandbox could not; the operator ran
   the server from their own terminal.)

**Incident worth remembering:** the first isolated instance was started from a *copy of the real
`config.yaml`*, which included real Cloudflare R2 credentials, so it uploaded a backup of its empty
throwaway database to the operator's real bucket (`polaris-20261001-170527.db`). Nothing was deleted,
but that object should be removed manually (a `restore-remote` would otherwise pick it as the newest
backup). Build test configs from scratch with `${VAR}` placeholders instead.
