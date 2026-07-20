// geocode.go — Nominatim (OpenStreetMap) geocoder, converting text
// locations into coordinates. Ported from her-go's integrate/geocode.go.
//
// Nominatim usage policy: max 1 req/sec, custom User-Agent, no bulk use.
// Fine for personal use. https://nominatim.org/release-docs/develop/api/Search/
package places

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"
)

type GeoResult struct {
	Latitude    float64
	Longitude   float64
	DisplayName string
}

type nominatimResult struct {
	Lat         string `json:"lat"`
	Lon         string `json:"lon"`
	DisplayName string `json:"display_name"`
}

// coordPattern matches raw coordinates like "40.7128, -74.0060".
var coordPattern = regexp.MustCompile(`^(-?\d+\.?\d*)[,\s]+(-?\d+\.?\d*)$`)

// nominatimBaseURL is a var (not a const) so tests can point it at a
// fake server instead of hitting the real, rate-limited Nominatim
// service — Geocode has no client struct to inject a baseURL into the
// way FoursquareClient/SearXNGClient/llm.Client do, since it's a bare
// function with no per-call configuration otherwise.
var nominatimBaseURL = "https://nominatim.openstreetmap.org"

// Geocode converts a text location into coordinates: raw "lat, lon" pairs
// are parsed directly (no API call); everything else goes through
// Nominatim. Returns nil (no error) if the query is empty or unresolvable.
func Geocode(ctx context.Context, query string) (*GeoResult, error) {
	query = strings.TrimSpace(query)
	if query == "" {
		return nil, nil
	}

	if matches := coordPattern.FindStringSubmatch(query); matches != nil {
		lat, err1 := strconv.ParseFloat(matches[1], 64)
		lon, err2 := strconv.ParseFloat(matches[2], 64)
		if err1 == nil && err2 == nil {
			return &GeoResult{Latitude: lat, Longitude: lon, DisplayName: fmt.Sprintf("%.4f, %.4f", lat, lon)}, nil
		}
	}

	endpoint := fmt.Sprintf("%s/search?q=%s&format=json&limit=1",
		nominatimBaseURL, url.QueryEscape(query))

	req, err := http.NewRequestWithContext(ctx, "GET", endpoint, nil)
	if err != nil {
		return nil, fmt.Errorf("creating geocode request: %w", err)
	}
	// Nominatim requires a custom User-Agent identifying the app — generic
	// agents get rate-limited or blocked.
	req.Header.Set("User-Agent", "Polaris/1.0 (personal search assistant)")
	req.Header.Set("Accept", "application/json")

	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("geocoding %q: %w", query, err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("reading geocode response: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("nominatim error (status %d): %s", resp.StatusCode, string(body))
	}

	var results []nominatimResult
	if err := json.Unmarshal(body, &results); err != nil {
		return nil, fmt.Errorf("parsing geocode response: %w", err)
	}
	if len(results) == 0 {
		return nil, nil
	}

	lat, err := strconv.ParseFloat(results[0].Lat, 64)
	if err != nil {
		return nil, fmt.Errorf("parsing latitude: %w", err)
	}
	lon, err := strconv.ParseFloat(results[0].Lon, 64)
	if err != nil {
		return nil, fmt.Errorf("parsing longitude: %w", err)
	}

	return &GeoResult{Latitude: lat, Longitude: lon, DisplayName: results[0].DisplayName}, nil
}
