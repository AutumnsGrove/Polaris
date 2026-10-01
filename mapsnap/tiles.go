package mapsnap

import (
	"bytes"
	"context"
	"fmt"
	"image"
	_ "image/jpeg" // tile providers serve either; decode both
	_ "image/png"
	"io"
	"net/http"
	"strings"
	"time"
)

// maxTileBytes bounds one tile response. Real raster tiles are a few KB to ~100KB;
// anything near this is not a tile.
const maxTileBytes = 1 << 20

// TileSource fetches tiles from one provider through the shared cache.
type TileSource struct {
	// URLTemplate has {z} {x} {y} and optionally {api_key} placeholders.
	URLTemplate string
	// APIKey is substituted for {api_key} at request time only. It is never part
	// of the cache key and is scrubbed from every error this type returns (an
	// http.Client error embeds the full request URL, key included).
	APIKey    string
	UserAgent string
	// Client should be the SSRF-safe one in production (tools.SafeDialContext).
	// nil falls back to a plain client with a timeout — fine for tests.
	Client *http.Client
	Cache  *TileCache
}

func (s *TileSource) client() *http.Client {
	if s.Client != nil {
		return s.Client
	}
	return &http.Client{Timeout: 10 * time.Second}
}

func (s *TileSource) scrub(msg string) string {
	if s.APIKey == "" {
		return msg
	}
	return strings.ReplaceAll(msg, s.APIKey, "***")
}

func (s *TileSource) url(z, x, y int) string {
	r := strings.NewReplacer("{z}", fmt.Sprint(z), "{x}", fmt.Sprint(x), "{y}", fmt.Sprint(y), "{api_key}", s.APIKey)
	return r.Replace(s.URLTemplate)
}

// Tile returns the decoded tile at z/x/y, from cache when present. x is wrapped
// around the antimeridian; y outside the world is an error (callers skip it).
func (s *TileSource) Tile(ctx context.Context, z, x, y int) (image.Image, error) {
	n := 1 << z
	if y < 0 || y >= n {
		return nil, fmt.Errorf("tile row %d outside the world at zoom %d", y, z)
	}
	x = ((x % n) + n) % n
	source := SourceID(s.URLTemplate)

	if s.Cache != nil {
		if data, ok := s.Cache.Get(source, z, x, y); ok {
			if img, _, err := image.Decode(bytes.NewReader(data)); err == nil {
				return img, nil
			}
			// A corrupt cached file falls through to a refetch, which overwrites it.
		}
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, s.url(z, x, y), nil)
	if err != nil {
		return nil, fmt.Errorf("building tile request: %s", s.scrub(err.Error()))
	}
	req.Header.Set("User-Agent", s.UserAgent)
	resp, err := s.client().Do(req)
	if err != nil {
		return nil, fmt.Errorf("fetching tile %d/%d/%d: %s", z, x, y, s.scrub(err.Error()))
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("fetching tile %d/%d/%d: status %d", z, x, y, resp.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxTileBytes+1))
	if err != nil {
		return nil, fmt.Errorf("reading tile %d/%d/%d: %s", z, x, y, s.scrub(err.Error()))
	}
	if len(data) > maxTileBytes {
		return nil, fmt.Errorf("tile %d/%d/%d is implausibly large", z, x, y)
	}
	// Decode before caching: a provider's rate-limit or error page can arrive as a
	// 200 with an HTML body, and caching that would serve it for 30 days.
	img, _, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("tile %d/%d/%d is not a valid image", z, x, y)
	}
	if s.Cache != nil {
		_ = s.Cache.Put(source, z, x, y, data) // a cache write failure never fails the render
	}
	return img, nil
}
