// Package mapsnap renders show_map's static snapshot: stitch map tiles, draw the
// model's pins and shapes on top, encode a PNG. See docs/plans/interactive-maps.md.
//
// It deliberately imports nothing from tools/ (tools calls it, not the reverse),
// so it defines its own small input types and the caller converts.
package mapsnap

import "math"

// TileSize is the edge of one raster tile in pixels (standard XYZ tiles).
const TileSize = 256

// Project converts lat/lon to global pixel coordinates at zoom z in the Web
// Mercator (EPSG:3857) pixel grid the XYZ tile scheme uses: x grows east, y grows
// south, the whole world is TileSize*2^z pixels square.
func Project(lat, lon float64, z int) (x, y float64) {
	n := float64(TileSize) * math.Pow(2, float64(z))
	x = (lon + 180) / 360 * n
	// Mercator is undefined at the poles; the tile scheme stops at ~85.0511°.
	lat = math.Max(-85.0511, math.Min(85.0511, lat))
	latR := lat * math.Pi / 180
	y = (1 - math.Log(math.Tan(latR)+1/math.Cos(latR))/math.Pi) / 2 * n
	return
}

// MetersPerPixel is the ground distance one pixel covers at lat/zoom — what turns
// a circle's radius_m into a pixel radius. It shrinks with cos(lat): a fixed
// radius is a bigger circle on screen the further from the equator you are.
func MetersPerPixel(lat float64, z int) float64 {
	return 156543.03392 * math.Cos(lat*math.Pi/180) / math.Pow(2, float64(z))
}
