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
	// FromRawCoordinates is true when the query was a "lat, lon" pair
	// rather than resolved through Nominatim, so DisplayName is just the
	// coordinates formatted as text (see the coordPattern branch below).
	// Callers that show DisplayName to a person — weather's forecast
	// chart title, e.g. — can check this and call ReverseGeocode to swap
	// in a natural place name instead, without every caller (nearby_search
	// included) paying for a reverse-geocode round trip it doesn't need.
	FromRawCoordinates bool
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
			return &GeoResult{
				Latitude: lat, Longitude: lon,
				DisplayName:        fmt.Sprintf("%.4f, %.4f", lat, lon),
				FromRawCoordinates: true,
			}, nil
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

// nominatimReverseResult is the subset of Nominatim's reverse-geocode
// response ReverseGeocode needs. The "address" object's fields are used
// in preference to display_name, which is Nominatim's own precise but
// verbose rendering (e.g. "Atlanta, Fulton County, Georgia, 30303, United
// States") — city+state reads naturally in a chart title or forecast
// line the way the full address doesn't.
type nominatimReverseResult struct {
	DisplayName string `json:"display_name"`
	Address     struct {
		City    string `json:"city"`
		Town    string `json:"town"`
		Village string `json:"village"`
		County  string `json:"county"`
		State   string `json:"state"`
	} `json:"address"`
}

// ReverseGeocode resolves coordinates to a natural place name, e.g. "40.71,
// -74.01" -> "New York, New York" — for turning a raw GPS fix (see
// Geocode's coordPattern shortcut and GeoResult.FromRawCoordinates) into
// something worth showing a person, without every Geocode caller paying
// for the round trip: nearby_search only ever needs the coordinates
// themselves, so it calls Geocode alone, while weather calls this too
// specifically to make its forecast chart title/summary line readable.
//
// zoom=10 asks Nominatim for city-level granularity (not "123 Main St",
// not just the state). Returns "" (not an error) whenever Nominatim has
// no address for these coordinates, so callers can fall back to the
// coordinates themselves rather than showing an empty location.
func ReverseGeocode(ctx context.Context, lat, lon float64) (string, error) {
	endpoint := fmt.Sprintf("%s/reverse?lat=%s&lon=%s&format=json&zoom=10&addressdetails=1",
		nominatimBaseURL, strconv.FormatFloat(lat, 'f', -1, 64), strconv.FormatFloat(lon, 'f', -1, 64))

	req, err := http.NewRequestWithContext(ctx, "GET", endpoint, nil)
	if err != nil {
		return "", fmt.Errorf("creating reverse geocode request: %w", err)
	}
	req.Header.Set("User-Agent", "Polaris/1.0 (personal search assistant)")
	req.Header.Set("Accept", "application/json")

	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("reverse geocoding %f,%f: %w", lat, lon, err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("reading reverse geocode response: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("nominatim reverse error (status %d): %s", resp.StatusCode, string(body))
	}

	var result nominatimReverseResult
	if err := json.Unmarshal(body, &result); err != nil {
		return "", fmt.Errorf("parsing reverse geocode response: %w", err)
	}

	place := result.Address.City
	if place == "" {
		place = result.Address.Town
	}
	if place == "" {
		place = result.Address.Village
	}
	if place == "" {
		place = result.Address.County
	}
	switch {
	case place != "" && result.Address.State != "":
		return place + ", " + result.Address.State, nil
	case place != "":
		return place, nil
	default:
		return result.DisplayName, nil
	}
}

// SetNominatimBaseURLForTesting points Geocode/ReverseGeocode at a fake
// server for tests outside this package (tools/weather_test.go, e.g.)
// that need to stub a reverse-geocode response without hitting the real,
// rate-limited Nominatim API. Returns a restore func to defer/Cleanup.
func SetNominatimBaseURLForTesting(url string) (restore func()) {
	old := nominatimBaseURL
	nominatimBaseURL = url
	return func() { nominatimBaseURL = old }
}
