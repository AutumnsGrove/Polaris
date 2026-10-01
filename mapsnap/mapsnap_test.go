package mapsnap

import (
	"bytes"
	"context"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// solidTile is a valid 256x256 PNG of one colour.
func solidTile(t testing.TB, c color.RGBA) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, TileSize, TileSize))
	for i := 0; i < len(img.Pix); i += 4 {
		img.Pix[i], img.Pix[i+1], img.Pix[i+2], img.Pix[i+3] = c.R, c.G, c.B, 255
	}
	var b bytes.Buffer
	if err := png.Encode(&b, img); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

// fakeTiles serves a flat grey tile for every z/x/y and counts requests. The
// template carries the key the way Geoapify's does, so key handling is exercised.
func fakeTiles(t *testing.T) (src *TileSource, hits *atomic.Int64) {
	t.Helper()
	hits = new(atomic.Int64)
	body := solidTile(t, color.RGBA{225, 225, 225, 255})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		if r.URL.Query().Get("apiKey") != "SECRETKEY" {
			http.Error(w, "bad key", http.StatusUnauthorized)
			return
		}
		w.Write(body)
	}))
	t.Cleanup(srv.Close)
	return &TileSource{
		URLTemplate: srv.URL + "/{z}/{x}/{y}.png?apiKey={api_key}",
		APIKey:      "SECRETKEY",
		UserAgent:   "polaris-test",
		Cache:       &TileCache{Dir: t.TempDir()},
	}, hits
}

func TestProject_KnownLandmark(t *testing.T) {
	// Space Needle at z15 lands in tile 5247/11442, verified against the real OSM
	// tile during the spike (the pin sat on Seattle Center).
	x, y := Project(47.6205, -122.3493, 15)
	if int(x/TileSize) != 5247 || int(y/TileSize) != 11442 {
		t.Errorf("tile = %d/%d, want 5247/11442", int(x/TileSize), int(y/TileSize))
	}
	if gx, gy := Project(0, 0, 0); gx != 128 || math.Abs(gy-128) > 1e-9 {
		t.Errorf("(0,0) at z0 = %v,%v, want the world's centre 128,128", gx, gy)
	}
	// Poles are clamped, not infinite.
	if _, py := Project(90, 0, 3); math.IsInf(py, 0) || math.IsNaN(py) {
		t.Errorf("north pole projected to %v", py)
	}
}

func TestMetersPerPixel(t *testing.T) {
	// At the equator, z0: the whole 40,075 km circumference over 256 px.
	if got := MetersPerPixel(0, 0); math.Abs(got-156543.03392) > 1e-6 {
		t.Errorf("equator z0 = %v", got)
	}
	// Halves every zoom, and shrinks with cos(lat).
	if a, b := MetersPerPixel(47, 10), MetersPerPixel(47, 11); math.Abs(a/b-2) > 1e-9 {
		t.Errorf("zoom ratio = %v, want 2", a/b)
	}
	if MetersPerPixel(60, 10) >= MetersPerPixel(0, 10) {
		t.Error("a pixel should cover less ground at high latitude")
	}
}

func TestRender_PinRingAndTileBudget(t *testing.T) {
	src, hits := fakeTiles(t)
	res, err := Render(context.Background(), src, Request{
		View: View{Center: []float64{47.6205, -122.3493}, Zoom: 15},
		Markers: []Marker{
			{Lat: 47.6205, Lon: -122.3493, Label: "Space Needle", Kind: "landmark"},
			{Lat: 47.6226, Lon: -122.3517, Label: "Storyville"},
		},
		Shapes:      []Shape{{Type: "circle", Lat: 47.6205, Lon: -122.3493, RadiusM: 400, Label: "5 min walk"}},
		Attribution: "© test",
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.Tiles > 12 || res.Tiles < 1 || int(hits.Load()) != res.Tiles || res.MissingTiles != 0 {
		t.Errorf("tiles=%d hits=%d missing=%d, want ≤12, one request each, none missing", res.Tiles, hits.Load(), res.MissingTiles)
	}
	img, err := png.Decode(bytes.NewReader(res.PNG))
	if err != nil || img.Bounds().Dx() != Width || img.Bounds().Dy() != Height {
		t.Fatalf("decoded %v, %v; want a %dx%d PNG", img.Bounds(), err, Width, Height)
	}

	// Centre pin is the landmark: ink outline ring at pinRadius+1, blue fill inside.
	cx, cy := Width/2, Height/2
	if got := img.At(cx+8, cy+8); got == (color.RGBA{225, 225, 225, 255}) {
		t.Errorf("pixel beside the landmark's star is still bare tile, want pin fill")
	}
	if got := color.RGBAModel.Convert(img.At(cx+pinRadius+1, cy)).(color.RGBA); got.R > 80 {
		t.Errorf("pin outline pixel = %v, want dark ink", got)
	}

	// The 400 m ring's stroke sits at radius 400/mpp from the centre.
	r := 400 / MetersPerPixel(47.6205, 15)
	got := color.RGBAModel.Convert(img.At(cx, cy-int(math.Round(r)))).(color.RGBA)
	if got.B < got.R+40 {
		t.Errorf("pixel at the ring radius = %v, want a blue stroke (r=%.1fpx)", got, r)
	}
	// Well away from ring and pins it's untouched tile grey.
	if got := img.At(40, 40); got != (color.RGBA{225, 225, 225, 255}) {
		t.Errorf("empty pixel = %v, want bare tile", got)
	}
}

func TestRender_SecondRenderIsServedFromCache(t *testing.T) {
	src, hits := fakeTiles(t)
	req := Request{View: View{Center: []float64{47.6, -122.3}, Zoom: 14}}
	if _, err := Render(context.Background(), src, req); err != nil {
		t.Fatal(err)
	}
	first := hits.Load()
	if _, err := Render(context.Background(), src, req); err != nil {
		t.Fatal(err)
	}
	if hits.Load() != first {
		t.Errorf("hits went %d -> %d on an identical render, want all tiles from cache", first, hits.Load())
	}
}

func TestRender_FramesMarkersAndIgnoresMuted(t *testing.T) {
	src, _ := fakeTiles(t)
	near := []Marker{{Lat: 47.620, Lon: -122.349, Label: "a"}, {Lat: 47.622, Lon: -122.352, Label: "b"}}
	res, err := Render(context.Background(), src, Request{Markers: near})
	if err != nil {
		t.Fatal(err)
	}
	if res.Zoom < 14 || res.Zoom > maxFitZoom {
		t.Errorf("zoom = %d for two pins ~300 m apart, want a close-in fit", res.Zoom)
	}
	// A far-away muted pin must not zoom the frame out.
	withMuted := append(append([]Marker{}, near...), Marker{Lat: 40.7, Lon: -74, Label: "far", Kind: "muted"})
	res2, err := Render(context.Background(), src, Request{Markers: withMuted})
	if err != nil {
		t.Fatal(err)
	}
	if res2.Zoom != res.Zoom {
		t.Errorf("zoom with a muted far pin = %d, want unchanged %d", res2.Zoom, res.Zoom)
	}
}

func TestRender_FrameIncludesCircleExtent(t *testing.T) {
	src, _ := fakeTiles(t)
	pin := []Marker{{Lat: 47.6205, Lon: -122.3493, Label: "x"}}
	bare, _ := Render(context.Background(), src, Request{Markers: pin})
	withRing, err := Render(context.Background(), src, Request{Markers: pin,
		Shapes: []Shape{{Type: "circle", Lat: 47.6205, Lon: -122.3493, RadiusM: 3000}}})
	if err != nil {
		t.Fatal(err)
	}
	if withRing.Zoom >= bare.Zoom {
		t.Errorf("zoom with a 3 km ring = %d, want lower than the pin-only %d so the ring fits", withRing.Zoom, bare.Zoom)
	}
}

func TestRender_NothingToFrameIsAnError(t *testing.T) {
	src, _ := fakeTiles(t)
	if _, err := Render(context.Background(), src, Request{}); err == nil {
		t.Error("want an error for a request with no view and nothing drawn")
	}
	if _, err := Render(context.Background(), nil, Request{}); err == nil {
		t.Error("want an error for no tile source")
	}
}

func TestRender_AllShapeTypesDrawWithoutPanicking(t *testing.T) {
	src, _ := fakeTiles(t)
	_, err := Render(context.Background(), src, Request{
		View: View{Center: []float64{47.62, -122.35}, Zoom: 15},
		Shapes: []Shape{
			{Type: "polyline", Points: [][]float64{{47.62, -122.35}, {47.621, -122.351}, {47.622, -122.353}}, Label: "route"},
			{Type: "polygon", Points: [][]float64{{47.62, -122.35}, {47.621, -122.35}, {47.621, -122.348}}, Label: "zone"},
			{Type: "arrow", Points: [][]float64{{47.619, -122.35}, {47.62, -122.349}}, Label: "go"},
			{Type: "circle", Lat: 47.62, Lon: -122.35, RadiusM: 99999999}, // absurd ring: must clip, not hang
		},
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestRender_PartialTileFailureStillRenders(t *testing.T) {
	var n atomic.Int64
	body := solidTile(t, color.RGBA{225, 225, 225, 255})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if n.Add(1)%2 == 0 {
			http.Error(w, "boom", 500)
			return
		}
		w.Write(body)
	}))
	defer srv.Close()
	src := &TileSource{URLTemplate: srv.URL + "/{z}/{x}/{y}", UserAgent: "t"}
	res, err := Render(context.Background(), src, Request{View: View{Center: []float64{10, 10}, Zoom: 8}})
	if err != nil {
		t.Fatalf("a patchy render should still succeed: %v", err)
	}
	if res.MissingTiles == 0 || res.MissingTiles == res.Tiles {
		t.Errorf("missing=%d of %d, want some but not all", res.MissingTiles, res.Tiles)
	}
}

func TestRender_AllTilesFailingIsAnErrorWithoutTheKey(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Error(w, "no", 403) }))
	srv.Close() // dead server: connection refused, and Go's error embeds the full URL
	src := &TileSource{URLTemplate: srv.URL + "/{z}/{x}/{y}?apiKey={api_key}", APIKey: "SECRETKEY", UserAgent: "t"}
	_, err := Render(context.Background(), src, Request{View: View{Center: []float64{10, 10}, Zoom: 8}})
	if err == nil {
		t.Fatal("want an error when every tile fails")
	}
	if strings.Contains(err.Error(), "SECRETKEY") {
		t.Errorf("error leaks the API key: %v", err)
	}
}

func TestTileSource_RejectsNonImageAndDoesNotCacheIt(t *testing.T) {
	// Providers can answer a rate-limit with a 200 and an HTML body; caching that
	// would serve it for a month.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("<html>rate limited</html>"))
	}))
	defer srv.Close()
	cache := &TileCache{Dir: t.TempDir()}
	src := &TileSource{URLTemplate: srv.URL + "/{z}/{x}/{y}", UserAgent: "t", Cache: cache}
	if _, err := src.Tile(context.Background(), 5, 1, 1); err == nil {
		t.Fatal("want a non-image body rejected")
	}
	if _, ok := cache.Get(SourceID(src.URLTemplate), 5, 1, 1); ok {
		t.Error("a non-image response was cached")
	}
}

func TestTileSource_WrapsXAndRejectsOutOfWorldY(t *testing.T) {
	src, _ := fakeTiles(t)
	if _, err := src.Tile(context.Background(), 3, -1, 2); err != nil {
		t.Errorf("x=-1 should wrap around the antimeridian: %v", err)
	}
	if _, err := src.Tile(context.Background(), 3, 0, -1); err == nil {
		t.Error("y above the world should be an error")
	}
	if _, err := src.Tile(context.Background(), 3, 0, 8); err == nil {
		t.Error("y below the world should be an error")
	}
}

func TestSourceID_IgnoresTheKeyAndSeparatesProviders(t *testing.T) {
	a := SourceID("https://a.example/{z}/{x}/{y}?apiKey={api_key}")
	if a != SourceID("https://a.example/{z}/{x}/{y}?apiKey={api_key}") {
		t.Error("SourceID not stable")
	}
	if a == SourceID("https://b.example/{z}/{x}/{y}?apiKey={api_key}") {
		t.Error("different providers share a cache directory")
	}
}

func TestCache_GetRefreshesRecency(t *testing.T) {
	c := &TileCache{Dir: t.TempDir()}
	if err := c.Put("s", 1, 2, 3, []byte("x")); err != nil {
		t.Fatal(err)
	}
	p := c.path("s", 1, 2, 3)
	old := time.Now().Add(-48 * time.Hour)
	os.Chtimes(p, old, old)
	if _, ok := c.Get("s", 1, 2, 3); !ok {
		t.Fatal("miss")
	}
	info, _ := os.Stat(p)
	if time.Since(info.ModTime()) > time.Minute {
		t.Errorf("mtime = %v after a hit, want it refreshed to now", info.ModTime())
	}
}

func TestCache_SweepIdleThenLRUBackstop(t *testing.T) {
	dir := t.TempDir()
	c := &TileCache{Dir: dir}
	age := func(y int, ago time.Duration) {
		if err := c.Put("s", 1, 0, y, make([]byte, 100)); err != nil {
			t.Fatal(err)
		}
		mt := time.Now().Add(-ago)
		os.Chtimes(c.path("s", 1, 0, y), mt, mt)
	}
	age(0, 40*24*time.Hour) // idle past the cutoff
	age(1, 5*24*time.Hour)  // oldest of the survivors
	age(2, 3*24*time.Hour)
	age(3, 1*time.Hour) // newest

	removed, err := c.Sweep(30*24*time.Hour, 0)
	if err != nil || removed != 1 {
		t.Fatalf("idle sweep removed %d (err %v), want exactly the 40-day-old tile", removed, err)
	}
	// 3 x 100 B left; a 250 B ceiling must evict the least recently used one.
	removed, err = c.Sweep(0, 250)
	if err != nil || removed != 1 {
		t.Fatalf("size sweep removed %d (err %v), want 1", removed, err)
	}
	if _, ok := c.Get("s", 1, 0, 1); ok {
		t.Error("the least recently used tile survived the size backstop")
	}
	for _, y := range []int{2, 3} {
		if _, ok := c.Get("s", 1, 0, y); !ok {
			t.Errorf("tile y=%d was evicted but is more recent", y)
		}
	}
}

func TestCache_SweepOnMissingDirIsNotAnError(t *testing.T) {
	c := &TileCache{Dir: filepath.Join(t.TempDir(), "never-created")}
	if n, err := c.Sweep(time.Hour, 1); err != nil || n != 0 {
		t.Errorf("Sweep = %d, %v, want a no-op", n, err)
	}
}

// Keeps the fmt import honest if the table above grows.
var _ = fmt.Sprint
