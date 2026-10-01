package tools

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"polaris/mapsnap"
)

// snapshotTimeout bounds one snapshot render (up to 12 tile fetches plus
// drawing). The handler renders synchronously so the file exists before the
// model's next step, which is exactly why it needs a hard ceiling.
const snapshotTimeout = 25 * time.Second

// MapSnapshot is show_map's static-image renderer: a tile source (with its shared
// disk cache) plus the attribution stamped on every image.
type MapSnapshot struct {
	Source      *mapsnap.TileSource
	Attribution string
}

// NewMapSnapshot builds the renderer from config, or returns nil when snapshots
// aren't usable: no URL, a URL that wants {api_key} with no key, or a URL
// pointing at the public OSM tile server. The last is refused outright rather
// than merely discouraged: tile.openstreetmap.org's usage policy bans headless
// rendering, and a blocked IP would take the live card's tiles down too.
//
// The HTTP client uses the same SSRF/DNS-rebinding-safe dialer as the other
// fetchers (the URL is operator-configured, but redirects aren't).
func NewMapSnapshot(tileURL, apiKey, cacheDir, attribution string) *MapSnapshot {
	if tileURL == "" {
		return nil
	}
	if strings.Contains(tileURL, "{api_key}") && apiKey == "" {
		return nil
	}
	if u, err := url.Parse(strings.NewReplacer("{z}", "0", "{x}", "0", "{y}", "0", "{api_key}", "x").Replace(tileURL)); err == nil &&
		strings.HasSuffix(u.Hostname(), "openstreetmap.org") {
		log.Warn("maps.snapshot_tile_url points at openstreetmap.org, whose policy forbids server-side rendering — snapshots disabled")
		return nil
	}
	return &MapSnapshot{
		Source: &mapsnap.TileSource{
			URLTemplate: tileURL,
			APIKey:      apiKey,
			UserAgent:   "Polaris/1.0 (personal search assistant)",
			Client:      &http.Client{Timeout: 10 * time.Second, Transport: &http.Transport{DialContext: dialContext}},
			Cache:       &mapsnap.TileCache{Dir: cacheDir},
		},
		Attribution: attribution,
	}
}

// snapshotFilename names a card version's image. The first version is just the
// id; later ones get a suffix so view_image never reads a stale file after an
// update (earlier versions stay in the workspace).
func snapshotFilename(p *MapPayload) string {
	if p.Version <= 1 {
		return p.ID + ".png"
	}
	return fmt.Sprintf("%s-v%d.png", p.ID, p.Version)
}

// snapshotRequest converts a resolved card into the renderer's input. Only
// shapes on default-on layers are drawn: the snapshot is a flat render of the
// card as it opens, not of whatever toggles a viewer later flips.
func snapshotRequest(p *MapPayload, attribution string) mapsnap.Request {
	on := map[string]bool{}
	for _, l := range p.Layers {
		on[l.ID] = l.DefaultOn
	}
	req := mapsnap.Request{
		View:        mapsnap.View{Center: p.View.Center, Zoom: p.View.Zoom, Bounds: p.View.Bounds},
		Attribution: attribution,
	}
	for _, m := range p.Markers {
		if m.Lat == nil || m.Lon == nil {
			continue
		}
		req.Markers = append(req.Markers, mapsnap.Marker{Lat: *m.Lat, Lon: *m.Lon, Label: m.Label, Kind: m.Kind})
	}
	for _, s := range p.Shapes {
		if !on[s.Layer] {
			continue
		}
		shape := mapsnap.Shape{Type: s.Type, RadiusM: s.RadiusM, Points: s.Points, Label: s.Label}
		if s.Lat != nil && s.Lon != nil {
			shape.Lat, shape.Lon = *s.Lat, *s.Lon
		}
		req.Shapes = append(req.Shapes, shape)
	}
	return req
}

// writeWorkspaceFile writes data to dir/name, creating dir world-writable first:
// 0o777 + an explicit Chmod (MkdirAll's mode is masked by the umask), because the
// code_exec sandbox container reads the workspace as a different uid — the same
// reasoning as fetch_url.go's identical dance.
func writeWorkspaceFile(dir, name string, data []byte) error {
	if err := os.MkdirAll(dir, 0o777); err != nil {
		return err
	}
	if err := os.Chmod(dir, 0o777); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, name), data, 0o666)
}

// snapshotMap renders p to a PNG in the thread's workspace and returns its
// filename. note is a model-facing sentence for the tool result either way: where
// to find the file, or why there isn't one. A snapshot failure never fails
// show_map — the interactive card already works without it.
func snapshotMap(ctx *Context, p *MapPayload) (name, note string) {
	if p.Kind != "map" {
		return "", "" // image cards get their flattened snapshot in a later phase
	}
	if ctx.MapSnapshot == nil {
		return "", "No static snapshot was made (map snapshots aren't configured), so you can't view_image this map."
	}
	if ctx.CodeExecWorkspaceDir == "" || ctx.ThreadID == "" {
		return "", "No static snapshot was made (this deployment has no workspace), so you can't view_image this map."
	}

	rctx, cancel := context.WithTimeout(ctx.Ctx, snapshotTimeout)
	defer cancel()
	res, err := mapsnap.Render(rctx, ctx.MapSnapshot.Source, snapshotRequest(p, ctx.MapSnapshot.Attribution))
	if err != nil {
		log.Warn("show_map: snapshot render failed", "id", p.ID, "err", err)
		return "", "The static snapshot failed (" + err.Error() + "), so you can't view_image this map; the interactive card is unaffected."
	}

	name = snapshotFilename(p)
	if err := writeWorkspaceFile(filepath.Join(ctx.CodeExecWorkspaceDir, ctx.ThreadID), name, res.PNG); err != nil {
		log.Warn("show_map: writing snapshot failed", "id", p.ID, "err", err)
		return "", "The static snapshot couldn't be saved (" + err.Error() + "), so you can't view_image this map."
	}
	log.Info("show_map: snapshot saved", "id", p.ID, "file", name, "zoom", res.Zoom, "tiles", res.Tiles, "missing_tiles", res.MissingTiles)

	note = fmt.Sprintf("A static snapshot of how it opens (default layers only) is saved in your workspace as %q — call view_image with that path to check your pins and shapes landed where you meant.", name)
	if res.MissingTiles > 0 {
		note += fmt.Sprintf(" (%d of %d background tiles failed to load, so parts of the base map are blank grey.)", res.MissingTiles, res.Tiles)
	}
	return name, note
}
