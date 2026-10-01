package mapsnap

import (
	"image"
	"image/color"
	"math"
	"sync"

	"golang.org/x/image/font"
	"golang.org/x/image/font/gofont/goregular"
	"golang.org/x/image/font/opentype"
	"golang.org/x/image/math/fixed"
)

// Palette mirrors the live card (see mockups/interactive-maps-leaflet.html and
// PRODUCT.md): gold for places, pale blue for the landmark and drawings, a
// warm grey for out-of-range pins. No neon.
var (
	colGold     = color.RGBA{232, 184, 74, 255}
	colLandmark = color.RGBA{169, 196, 232, 255}
	colMuted    = color.RGBA{128, 120, 108, 255}
	colInk      = color.RGBA{29, 26, 22, 255}
	colText     = color.RGBA{236, 230, 220, 255}
)

// pt is a canvas-space pixel position.
type pt struct{ x, y float64 }

type canvas struct{ img *image.RGBA }

// blend composites col over the existing pixel with coverage a in [0,1].
func (c canvas) blend(x, y int, col color.RGBA, a float64) {
	if a <= 0 || !(image.Point{x, y}).In(c.img.Rect) {
		return
	}
	if a > 1 {
		a = 1
	}
	o := c.img.RGBAAt(x, y)
	mix := func(b, f uint8) uint8 { return uint8(float64(b)*(1-a) + float64(f)*a + 0.5) }
	c.img.SetRGBA(x, y, color.RGBA{mix(o.R, col.R), mix(o.G, col.G), mix(o.B, col.B), 255})
}

// coverage turns a signed distance past an edge into antialiased coverage: a
// pixel whose centre is d pixels outside the shape gets 1-d, so edges are one
// pixel of ramp instead of a stair-step.
func coverage(d float64) float64 { return math.Max(0, math.Min(1, 0.5-d)) }

func (c canvas) fillCircle(cx, cy, r float64, col color.RGBA, alpha float64) {
	for y := int(cy - r - 1); y <= int(cy+r+1); y++ {
		for x := int(cx - r - 1); x <= int(cx+r+1); x++ {
			d := math.Hypot(float64(x)+0.5-cx, float64(y)+0.5-cy) - r
			c.blend(x, y, col, coverage(d)*alpha)
		}
	}
}

func (c canvas) strokeCircle(cx, cy, r, w float64, col color.RGBA, alpha float64) {
	ext := r + w
	for y := int(cy - ext - 1); y <= int(cy+ext+1); y++ {
		for x := int(cx - ext - 1); x <= int(cx+ext+1); x++ {
			d := math.Abs(math.Hypot(float64(x)+0.5-cx, float64(y)+0.5-cy)-r) - w/2
			c.blend(x, y, col, coverage(d)*alpha)
		}
	}
}

// dash describes a dash pattern in pixels; on == 0 means solid.
type dash struct{ on, off float64 }

// strokePath draws a thick polyline, continuing the dash pattern across vertices
// so a dashed route doesn't restart its rhythm at every bend.
func (c canvas) strokePath(pts []pt, w float64, col color.RGBA, d dash) {
	var travelled float64
	for i := 0; i+1 < len(pts); i++ {
		a, b := pts[i], pts[i+1]
		segLen := math.Hypot(b.x-a.x, b.y-a.y)
		if segLen == 0 {
			continue
		}
		ux, uy := (b.x-a.x)/segLen, (b.y-a.y)/segLen
		minX, maxX := math.Min(a.x, b.x)-w, math.Max(a.x, b.x)+w
		minY, maxY := math.Min(a.y, b.y)-w, math.Max(a.y, b.y)+w
		for y := int(minY); y <= int(maxY)+1; y++ {
			for x := int(minX); x <= int(maxX)+1; x++ {
				px, py := float64(x)+0.5-a.x, float64(y)+0.5-a.y
				t := math.Max(0, math.Min(segLen, px*ux+py*uy)) // along-segment position, clamped
				dist := math.Hypot(px-t*ux, py-t*uy)
				if d.on > 0 && math.Mod(travelled+t, d.on+d.off) > d.on {
					continue
				}
				c.blend(x, y, col, coverage(dist-w/2))
			}
		}
		travelled += segLen
	}
}

// fillPolygon fills with an even-odd test at pixel centres (no edge AA: the
// outline stroked over it supplies the clean edge).
func (c canvas) fillPolygon(pts []pt, col color.RGBA, alpha float64) {
	if len(pts) < 3 {
		return
	}
	minX, maxX, minY, maxY := pts[0].x, pts[0].x, pts[0].y, pts[0].y
	for _, p := range pts {
		minX, maxX = math.Min(minX, p.x), math.Max(maxX, p.x)
		minY, maxY = math.Min(minY, p.y), math.Max(maxY, p.y)
	}
	for y := int(minY); y <= int(maxY)+1; y++ {
		for x := int(minX); x <= int(maxX)+1; x++ {
			fx, fy := float64(x)+0.5, float64(y)+0.5
			inside := false
			for i, j := 0, len(pts)-1; i < len(pts); j, i = i, i+1 {
				if (pts[i].y > fy) != (pts[j].y > fy) &&
					fx < (pts[j].x-pts[i].x)*(fy-pts[i].y)/(pts[j].y-pts[i].y)+pts[i].x {
					inside = !inside
				}
			}
			if inside {
				c.blend(x, y, col, alpha)
			}
		}
	}
}

// arrowHead strokes a two-barbed head at b, pointing away from a.
func (c canvas) arrowHead(a, b pt, w float64, col color.RGBA) {
	ang := math.Atan2(b.y-a.y, b.x-a.x)
	const head, spread = 14.0, 0.45
	for _, s := range []float64{-spread, spread} {
		tip := pt{b.x - head*math.Cos(ang+s), b.y - head*math.Sin(ang+s)}
		c.strokePath([]pt{b, tip}, w, col, dash{})
	}
}

var (
	faceMu sync.Mutex
	parsed *opentype.Font
	faces  = map[float64]font.Face{}
)

// face returns the embedded Go Regular at the given pixel size, cached. An
// embedded TTF (not basicfont's fixed 7x13) so labels are legible at phone scale
// and the renderer needs no font files on the potato. Renders can run
// concurrently (two show_map calls in one response), hence the lock.
func face(size float64) font.Face {
	faceMu.Lock()
	defer faceMu.Unlock()
	if parsed == nil {
		f, err := opentype.Parse(goregular.TTF)
		if err != nil {
			panic("mapsnap: embedded font failed to parse: " + err.Error()) // compiled-in data; can't happen at runtime
		}
		parsed = f
	}
	if f, ok := faces[size]; ok {
		return f
	}
	f, err := opentype.NewFace(parsed, &opentype.FaceOptions{Size: size, DPI: 72, Hinting: font.HintingFull})
	if err != nil {
		panic("mapsnap: building font face: " + err.Error())
	}
	faces[size] = f
	return f
}

// textWidth measures s in pixels.
func textWidth(f font.Face, s string) float64 {
	return float64(font.MeasureString(f, s)) / 64
}

// drawText draws s with its baseline-left at (x, y), over a dark halo so it stays
// readable on any tile colour. The halo is the same string stamped at the eight
// neighbouring offsets.
func (c canvas) drawText(x, y float64, s string, f font.Face, fg color.RGBA) {
	draw := func(ox, oy float64, col color.RGBA) {
		d := &font.Drawer{Dst: c.img, Src: image.NewUniform(col), Face: f,
			Dot: fixed.Point26_6{X: fixed.I(int(x + ox)), Y: fixed.I(int(y + oy))}}
		d.DrawString(s)
	}
	halo := color.RGBA{colInk.R, colInk.G, colInk.B, 255}
	for _, o := range [][2]float64{{-1, -1}, {0, -1}, {1, -1}, {-1, 0}, {1, 0}, {-1, 1}, {0, 1}, {1, 1}} {
		draw(o[0], o[1], halo)
	}
	draw(0, 0, fg)
}

// star returns a five-pointed star's outline centred at (cx, cy).
func star(cx, cy, outer float64) []pt {
	inner := outer * 0.45
	var out []pt
	for i := 0; i < 10; i++ {
		r := outer
		if i%2 == 1 {
			r = inner
		}
		a := -math.Pi/2 + float64(i)*math.Pi/5
		out = append(out, pt{cx + r*math.Cos(a), cy + r*math.Sin(a)})
	}
	return out
}
