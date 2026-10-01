package mapsnap

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/draw"
	"image/png"
	"math"
	"sync"
)

const (
	// Canvas size. Fixed, so the tile count is bounded by construction: a
	// 768x512 window can straddle at most 4x3 = 12 tiles at any zoom/offset,
	// comfortably under maxTiles. (Zoom changes how much ground a tile covers,
	// never how many tiles the window needs.)
	Width  = 768
	Height = 512

	maxTiles = 16
	// fetchConcurrency stays low: these requests share one provider key and its
	// requests-per-second limit.
	fetchConcurrency = 4

	maxZoom = 18
	// A single pin or a tight cluster would otherwise fit at maxZoom, which is
	// street-corner close and mostly empty tile. 16 keeps some surroundings.
	maxFitZoom = 16
	fitPad     = 56

	pinRadius = 13
)

// Marker is one pin to draw. Kind is "", "place", "landmark" or "muted".
type Marker struct {
	Lat, Lon float64
	Label    string
	Kind     string
}

// Shape is one drawn annotation. Points are [lat, lon] pairs. Only shapes on
// layers that are on by default are passed in — the snapshot is a flat render of
// the default view, not of whatever the viewer has toggled.
type Shape struct {
	Type     string // circle | polyline | polygon | arrow
	Lat, Lon float64
	RadiusM  float64
	Points   [][]float64
	Label    string
}

// View is where to look: Center+Zoom, Bounds, or neither (frame the data).
type View struct {
	Center []float64 // [lat, lon]
	Zoom   int
	Bounds [][]float64 // [[south, west], [north, east]]
}

// Request is everything Render needs; tools converts its payload into this.
type Request struct {
	View        View
	Markers     []Marker
	Shapes      []Shape
	Attribution string
}

// Result is a rendered snapshot.
type Result struct {
	PNG                 []byte
	Width, Height, Zoom int
	Tiles, MissingTiles int
}

// Render stitches tiles from src, draws the request on top, and returns a PNG.
// A tile that fails to load is painted as flat grey and counted in MissingTiles
// rather than failing the render (pins on a patchy map still answer "where");
// only a render with no tiles at all is an error.
func Render(ctx context.Context, src *TileSource, req Request) (*Result, error) {
	if src == nil {
		return nil, errors.New("no tile source configured")
	}
	z, ox, oy, err := resolveView(req)
	if err != nil {
		return nil, err
	}

	img := image.NewRGBA(image.Rect(0, 0, Width, Height))
	draw.Draw(img, img.Bounds(), image.NewUniform(color.RGBA{232, 228, 220, 255}), image.Point{}, draw.Src)

	x0, x1 := int(math.Floor(ox/TileSize)), int(math.Floor((ox+Width-1)/TileSize))
	y0, y1 := int(math.Floor(oy/TileSize)), int(math.Floor((oy+Height-1)/TileSize))
	type job struct{ tx, ty int }
	var jobs []job
	for ty := y0; ty <= y1; ty++ {
		if ty < 0 || ty >= 1<<z {
			continue // above/below the world: nothing to fetch
		}
		for tx := x0; tx <= x1; tx++ {
			jobs = append(jobs, job{tx, ty})
		}
	}
	if len(jobs) > maxTiles {
		return nil, fmt.Errorf("snapshot would need %d tiles (cap %d)", len(jobs), maxTiles)
	}

	var (
		mu       sync.Mutex
		wg       sync.WaitGroup
		missing  int
		firstErr error
		sem      = make(chan struct{}, fetchConcurrency)
	)
	for _, j := range jobs {
		wg.Add(1)
		sem <- struct{}{}
		go func() {
			defer wg.Done()
			defer func() { <-sem }()
			tile, err := src.Tile(ctx, z, j.tx, j.ty)
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				missing++
				if firstErr == nil {
					firstErr = err
				}
				return
			}
			dst := image.Pt(j.tx*TileSize-int(ox), j.ty*TileSize-int(oy))
			draw.Draw(img, image.Rectangle{dst, dst.Add(image.Pt(TileSize, TileSize))}, tile, tile.Bounds().Min, draw.Src)
		}()
	}
	wg.Wait()
	if len(jobs) > 0 && missing == len(jobs) {
		return nil, fmt.Errorf("no tiles could be loaded: %w", firstErr)
	}

	c := canvas{img}
	toCanvas := func(lat, lon float64) pt {
		gx, gy := Project(lat, lon, z)
		return pt{gx - ox, gy - oy}
	}
	drawShapes(c, req.Shapes, z, toCanvas)
	drawMarkers(c, req.Markers, toCanvas)
	drawAttribution(c, req.Attribution)

	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		return nil, fmt.Errorf("encoding snapshot: %w", err)
	}
	return &Result{PNG: buf.Bytes(), Width: Width, Height: Height, Zoom: z, Tiles: len(jobs), MissingTiles: missing}, nil
}

// resolveView picks the zoom and the global-pixel position of the canvas's
// top-left corner. The origin is floored to a whole pixel so tiles (placed at
// integer offsets) and the vector overlay (projected in floats) share one grid.
func resolveView(req Request) (z int, ox, oy float64, err error) {
	v := req.View
	if len(v.Bounds) == 0 && len(v.Center) == 2 {
		z = v.Zoom
		if z < 1 || z > maxZoom {
			z = 15
		}
		cx, cy := Project(v.Center[0], v.Center[1], z)
		return z, math.Floor(cx - Width/2), math.Floor(cy - Height/2), nil
	}

	// Frame a set of points: explicit bounds, or everything non-muted that was drawn.
	var lats, lons []float64
	add := func(lat, lon float64) { lats, lons = append(lats, lat), append(lons, lon) }
	if len(v.Bounds) == 2 && len(v.Bounds[0]) == 2 && len(v.Bounds[1]) == 2 {
		add(v.Bounds[0][0], v.Bounds[0][1])
		add(v.Bounds[1][0], v.Bounds[1][1])
	} else {
		for _, m := range req.Markers {
			if m.Kind != "muted" {
				add(m.Lat, m.Lon)
			}
		}
		for _, s := range req.Shapes {
			if s.Type == "circle" {
				// metres -> degrees; cos(lat) widens a degree of longitude's pull.
				dLat := s.RadiusM / 111320
				dLon := dLat / math.Max(0.01, math.Cos(s.Lat*math.Pi/180))
				add(s.Lat-dLat, s.Lon-dLon)
				add(s.Lat+dLat, s.Lon+dLon)
			}
			for _, p := range s.Points {
				if len(p) == 2 {
					add(p[0], p[1])
				}
			}
		}
	}
	if len(lats) == 0 {
		return 0, 0, 0, errors.New("nothing to frame: no center, bounds, or non-muted markers/shapes")
	}
	minLat, maxLat := minMax(lats)
	minLon, maxLon := minMax(lons)
	for z = maxFitZoom; z >= 0; z-- {
		left, top := Project(maxLat, minLon, z)
		right, bottom := Project(minLat, maxLon, z)
		if right-left <= Width-2*fitPad && bottom-top <= Height-2*fitPad {
			return z, math.Floor((left+right)/2 - Width/2), math.Floor((top+bottom)/2 - Height/2), nil
		}
	}
	// Unreachable: at zoom 0 the whole world is 256px, narrower than the padded canvas.
	return 0, 0, 0, errors.New("could not fit the requested area")
}

func minMax(xs []float64) (lo, hi float64) {
	lo, hi = xs[0], xs[0]
	for _, x := range xs[1:] {
		lo, hi = math.Min(lo, x), math.Max(hi, x)
	}
	return
}

func drawShapes(c canvas, shapes []Shape, z int, toCanvas func(lat, lon float64) pt) {
	lbl := face(13)
	for _, s := range shapes {
		var pts []pt
		for _, p := range s.Points {
			if len(p) == 2 {
				pts = append(pts, toCanvas(p[0], p[1]))
			}
		}
		var labelAt pt
		switch s.Type {
		case "circle":
			ctr := toCanvas(s.Lat, s.Lon)
			r := s.RadiusM / MetersPerPixel(s.Lat, z)
			c.fillCircle(ctr.x, ctr.y, r, colRing, 0.12)
			c.strokeCircle(ctr.x, ctr.y, r, 3, colRing, 0.95)
			labelAt = pt{ctr.x, ctr.y + r + 18}
			if labelAt.y > Height-30 { // ring runs off the bottom: tuck the label inside it
				labelAt = pt{ctr.x, ctr.y + r - 10}
			}
		case "polyline":
			// Dashed, matching the live card's route styling.
			c.strokePath(pts, 4, colRing, dash{on: 10, off: 7})
			if len(pts) > 0 {
				labelAt = pt{pts[len(pts)/2].x, pts[len(pts)/2].y - 10}
			}
		case "polygon":
			c.fillPolygon(pts, colRing, 0.15)
			closed := append(append([]pt{}, pts...), pts[0])
			c.strokePath(closed, 3, colRing, dash{})
			if len(pts) > 0 {
				labelAt = pts[0]
			}
		case "arrow":
			c.strokePath(pts, 4, colRing, dash{})
			if len(pts) == 2 {
				c.arrowHead(pts[0], pts[1], 4, colRing)
				labelAt = pt{(pts[0].x + pts[1].x) / 2, (pts[0].y+pts[1].y)/2 - 10}
			}
		}
		if s.Label != "" {
			w := textWidth(lbl, s.Label)
			x := math.Max(4, math.Min(Width-w-4, labelAt.x-w/2))
			y := math.Max(16, math.Min(Height-8, labelAt.y))
			c.drawText(x, y, s.Label, lbl, colRing, colHalo)
		}
	}
}

func drawMarkers(c canvas, markers []Marker, toCanvas func(lat, lon float64) pt) {
	num, lbl := face(13), face(13)
	type placed struct {
		m Marker
		p pt
	}
	var visible []placed
	n := 0
	for _, m := range markers {
		p := toCanvas(m.Lat, m.Lon)
		// Numbered in the order the model was told (landmarks aren't numbered),
		// counted even when off-canvas so numbers match the live card.
		numLabel := ""
		if m.Kind != "landmark" {
			n++
			numLabel = fmt.Sprint(n)
		}
		if p.x < -pinRadius || p.y < -pinRadius || p.x > Width+pinRadius || p.y > Height+pinRadius {
			continue
		}
		fill := colGold
		switch m.Kind {
		case "landmark":
			fill = colRing
		case "muted":
			fill = colMuted
		}
		c.fillCircle(p.x, p.y, pinRadius+2, colInk, 1)
		c.fillCircle(p.x, p.y, pinRadius, fill, 1)
		if m.Kind == "landmark" {
			c.fillPolygon(star(p.x, p.y, 8), colHalo, 1)
		} else {
			w := textWidth(num, numLabel)
			c.drawText(p.x-w/2, p.y+4.5, numLabel, num, colInk, fill)
		}
		visible = append(visible, placed{m, p})
	}
	// Labels after every pin so a later pin never covers an earlier label.
	for _, v := range visible {
		if v.m.Label == "" {
			continue
		}
		w := textWidth(lbl, v.m.Label)
		x := v.p.x + pinRadius + 6
		if x+w > Width-4 { // would run off the right edge: put it on the left
			x = v.p.x - pinRadius - 6 - w
		}
		c.drawText(math.Max(4, x), v.p.y+5, v.m.Label, lbl, colInk, colHalo)
	}
}

// drawAttribution stamps the tile provider's required credit bottom-right, on a
// translucent plate so it reads on any tile colour. Providers require it to be
// visible, so it can't be skipped for a "clean" image.
func drawAttribution(c canvas, text string) {
	if text == "" {
		return
	}
	f := face(11)
	w := textWidth(f, text)
	x0, y0 := Width-int(w)-14, Height-20
	for y := y0; y < Height; y++ {
		for x := x0; x < Width; x++ {
			c.blend(x, y, colHalo, 0.8)
		}
	}
	c.drawText(float64(x0)+7, float64(Height)-7, text, f, colInk, colHalo)
}
