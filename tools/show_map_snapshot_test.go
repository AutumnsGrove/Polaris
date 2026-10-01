package tools

import (
	"bytes"
	"fmt"
	"image"
	"image/png"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"polaris/mapsnap"
)

// snapshotCtx is a context with a working snapshot renderer pointed at a local
// fake tile server (plain client: the real constructor's SSRF-safe dialer would,
// correctly, refuse a loopback address) and a temp workspace.
func snapshotCtx(t *testing.T) (*Context, string) {
	t.Helper()
	var tile bytes.Buffer
	png.Encode(&tile, image.NewRGBA(image.Rect(0, 0, 256, 256)))
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write(tile.Bytes()) }))
	t.Cleanup(srv.Close)

	ws := t.TempDir()
	ctx := newTestContext()
	ctx.CodeExecWorkspaceDir = ws
	ctx.ThreadID = "thread1"
	ctx.MapSnapshot = &MapSnapshot{
		Source: &mapsnap.TileSource{
			URLTemplate: srv.URL + "/{z}/{x}/{y}.png?k={api_key}", APIKey: "SECRETKEY", UserAgent: "t",
			Cache: &mapsnap.TileCache{Dir: t.TempDir()},
		},
		Attribution: "© test",
	}
	return ctx, filepath.Join(ws, "thread1")
}

const snapshotArgs = `{"kind":"map","center":"47.62, -122.35","markers":[{"id":"a","lat":47.62,"lon":-122.35,"label":"A"}]}`

func TestShowMapSnapshot_WritesFileAndNamesItInResult(t *testing.T) {
	ctx, dir := snapshotCtx(t)
	result, _, res := showMapCapture(t, ctx, snapshotArgs)
	p := mapOf(t, res)

	if p.Snapshot != p.ID+".png" {
		t.Fatalf("payload.Snapshot = %q, want %q", p.Snapshot, p.ID+".png")
	}
	data, err := os.ReadFile(filepath.Join(dir, p.Snapshot))
	if err != nil {
		t.Fatalf("snapshot not written: %v", err)
	}
	if img, err := png.Decode(bytes.NewReader(data)); err != nil || img.Bounds().Dx() != mapsnap.Width {
		t.Errorf("snapshot isn't a valid %dpx-wide PNG: %v", mapsnap.Width, err)
	}
	for _, want := range []string{p.Snapshot, "view_image"} {
		if !strings.Contains(result, want) {
			t.Errorf("result = %q, want it to mention %q", result, want)
		}
	}
	// The directory must be world-writable for the sandbox container's uid.
	if info, _ := os.Stat(dir); info.Mode().Perm() != 0o777 {
		t.Errorf("workspace dir mode = %v, want 0777", info.Mode().Perm())
	}
	// And view_image's own resolver must accept the name we handed the model.
	if _, err := resolveWorkspaceFilePath(ctx, p.Snapshot); err != nil {
		t.Errorf("view_image couldn't resolve the snapshot: %v", err)
	}
}

func TestShowMapSnapshot_UpdateWritesANewFilenameAndKeepsTheOld(t *testing.T) {
	ctx, dir := snapshotCtx(t)
	_, _, res := showMapCapture(t, ctx, snapshotArgs)
	first := mapOf(t, res)

	_, _, res = showMapCapture(t, ctx, fmt.Sprintf(`{"update":%q,"markers":[{"id":"b","lat":47.621,"lon":-122.351,"label":"B"}]}`, first.ID))
	second := mapOf(t, res)

	if second.Snapshot != first.ID+"-v2.png" {
		t.Errorf("v2 snapshot = %q, want %q so view_image never reads a stale file", second.Snapshot, first.ID+"-v2.png")
	}
	for _, name := range []string{first.Snapshot, second.Snapshot} {
		if _, err := os.Stat(filepath.Join(dir, name)); err != nil {
			t.Errorf("%s missing: %v", name, err)
		}
	}
}

func TestShowMapSnapshot_NotConfiguredStillShowsTheCard(t *testing.T) {
	ctx := newTestContext() // MapSnapshot nil
	result, _, res := showMapCapture(t, ctx, snapshotArgs)
	p := mapOf(t, res)
	if p.Snapshot != "" || !strings.Contains(result, "snapshots aren't configured") {
		t.Errorf("snapshot=%q result=%q, want no file and an explanation", p.Snapshot, result)
	}
}

func TestShowMapSnapshot_NoWorkspaceStillShowsTheCard(t *testing.T) {
	ctx, _ := snapshotCtx(t)
	ctx.CodeExecWorkspaceDir = ""
	result, _, res := showMapCapture(t, ctx, snapshotArgs)
	if mapOf(t, res).Snapshot != "" || !strings.Contains(result, "no workspace") {
		t.Errorf("result = %q, want no file and a no-workspace note", result)
	}
}

func TestShowMapSnapshot_RenderFailureNeverFailsTheToolOrLeaksTheKey(t *testing.T) {
	ctx, dir := snapshotCtx(t)
	dead := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	dead.Close()
	ctx.MapSnapshot.Source.URLTemplate = dead.URL + "/{z}/{x}/{y}?k={api_key}"

	result, _, res := showMapCapture(t, ctx, snapshotArgs)
	if strings.HasPrefix(result, "error:") {
		t.Fatalf("a snapshot failure failed the whole tool: %q", result)
	}
	if mapOf(t, res).Snapshot != "" || !strings.Contains(result, "snapshot failed") {
		t.Errorf("result = %q, want the card shown and a failure note", result)
	}
	if strings.Contains(result, "SECRETKEY") {
		t.Errorf("result leaks the API key: %q", result)
	}
	if entries, _ := os.ReadDir(dir); len(entries) != 0 {
		t.Errorf("workspace has %d files after a failed render, want none", len(entries))
	}
}

func TestShowMapSnapshot_ImageKindIsSkippedForNow(t *testing.T) {
	ctx, _ := snapshotCtx(t)
	result, _, res := showMapCapture(t, ctx, `{"kind":"image","url":"https://example.com/a.png"}`)
	if mapOf(t, res).Snapshot != "" || strings.Contains(result, "snapshot") {
		t.Errorf("result = %q, want no snapshot talk for an image card", result)
	}
}

func TestSnapshotRequest_DrawsOnlyDefaultOnLayers(t *testing.T) {
	lat, lon := 47.6, -122.3
	p := &MapPayload{
		Kind:    "map",
		Markers: []MapMarker{{ID: "a", Lat: &lat, Lon: &lon, Label: "A", Kind: "muted"}},
		Shapes: []MapShape{
			{ID: "ring", Type: "circle", Lat: &lat, Lon: &lon, RadiusM: 500, Layer: "radius"},
			{ID: "route", Type: "polyline", Points: [][]float64{{1, 2}, {3, 4}}, Layer: "route"},
		},
		Layers: []MapLayer{{ID: "radius", DefaultOn: true}, {ID: "route", DefaultOn: false}},
	}
	req := snapshotRequest(p, "© x")
	if len(req.Shapes) != 1 || req.Shapes[0].Type != "circle" || req.Shapes[0].RadiusM != 500 {
		t.Errorf("shapes = %+v, want only the default-on circle", req.Shapes)
	}
	if len(req.Markers) != 1 || req.Markers[0].Kind != "muted" || req.Attribution != "© x" {
		t.Errorf("markers/attribution = %+v / %q", req.Markers, req.Attribution)
	}
}

func TestNewMapSnapshot_RefusesUnusableConfigs(t *testing.T) {
	good := "https://maps.example.com/{z}/{x}/{y}.png?apiKey={api_key}"
	if NewMapSnapshot(good, "key", t.TempDir(), "©") == nil {
		t.Error("a valid config produced no renderer")
	}
	cases := map[string]struct{ url, key string }{
		"no url":              {"", "key"},
		"url needs key":       {good, ""},
		"public OSM":          {"https://tile.openstreetmap.org/{z}/{x}/{y}.png", ""},
		"OSM subdomain":       {"https://a.tile.openstreetmap.org/{z}/{x}/{y}.png", ""},
		"OSM even with a key": {"https://tile.openstreetmap.org/{z}/{x}/{y}.png?k={api_key}", "key"},
	}
	for name, tc := range cases {
		if NewMapSnapshot(tc.url, tc.key, t.TempDir(), "©") != nil {
			t.Errorf("%s: want snapshots disabled", name)
		}
	}
	// A key-free URL (self-hosted server) is fine without a key.
	if NewMapSnapshot("https://tiles.home.lan/{z}/{x}/{y}.png", "", t.TempDir(), "©") == nil {
		t.Error("a key-free self-hosted URL should be accepted")
	}
}
