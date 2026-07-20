// Package places wraps the Foursquare Places API (nearby search) and
// Nominatim (text location -> coordinates), adapted from her-go's
// integrate package for Polaris's simpler, stateless-per-query use case
// (no location history / saved home location — see config.DefaultLocation
// for the one persistent fallback this project does keep).
package places

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"strings"
	"time"
)

type FoursquareClient struct {
	apiKey  string
	baseURL string
	http    *http.Client
}

// NewFoursquareClient returns nil if apiKey is empty — callers check for
// nil to know whether Foursquare is configured at all.
func NewFoursquareClient(apiKey string) *FoursquareClient {
	if apiKey == "" {
		return nil
	}
	return &FoursquareClient{apiKey: apiKey, baseURL: foursquareBaseURL, http: &http.Client{Timeout: 15 * time.Second}}
}

const foursquareBaseURL = "https://places-api.foursquare.com"

// foursquareAPIVersion is date-based versioning sent via header (the API
// dropped /v3/ path versioning in 2026). Bump deliberately when adopting
// new API features.
const foursquareAPIVersion = "2025-06-17"

const (
	defaultRadiusM = 5000   // 5km — reasonable walking/driving distance
	maxRadiusM     = 100000 // Foursquare's Places API ceiling
)

type Place struct {
	FSQPlaceID string          `json:"fsq_place_id"`
	Name       string          `json:"name"`
	Location   PlaceLocation   `json:"location"`
	Categories []PlaceCategory `json:"categories"`
	Distance   int             `json:"distance"`
	Latitude   float64         `json:"latitude"`
	Longitude  float64         `json:"longitude"`
}

type PlaceLocation struct {
	Address          string `json:"address"`
	FormattedAddress string `json:"formatted_address"`
	Locality         string `json:"locality"`
	Region           string `json:"region"`
	Country          string `json:"country"`
}

type PlaceCategory struct {
	Name string `json:"name"`
}

type placeSearchResponse struct {
	Results []Place `json:"results"`
}

// SearchNearby finds places near (lat, lon) matching query, sorted by
// distance. The caller is responsible for geocoding text locations first.
func (c *FoursquareClient) SearchNearby(ctx context.Context, lat, lon float64, query string, radiusM, limit int) ([]Place, error) {
	if limit <= 0 || limit > 50 {
		limit = 10
	}
	if radiusM <= 0 {
		radiusM = defaultRadiusM
	}
	if radiusM > maxRadiusM {
		radiusM = maxRadiusM
	}

	endpoint := fmt.Sprintf("%s/places/search?ll=%f,%f&radius=%d&limit=%d&sort=DISTANCE",
		c.baseURL, lat, lon, radiusM, limit)
	if query != "" {
		endpoint += "&query=" + url.QueryEscape(query)
	}
	endpoint += "&fields=fsq_place_id,name,location,categories,distance,latitude,longitude"

	req, err := http.NewRequestWithContext(ctx, "GET", endpoint, nil)
	if err != nil {
		return nil, fmt.Errorf("creating request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+c.apiKey)
	req.Header.Set("X-Places-Api-Version", foursquareAPIVersion)
	req.Header.Set("Accept", "application/json")

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("searching places: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("reading response: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("foursquare error (status %d): %s", resp.StatusCode, string(body))
	}

	var searchResp placeSearchResponse
	if err := json.Unmarshal(body, &searchResp); err != nil {
		return nil, fmt.Errorf("parsing places: %w", err)
	}
	return searchResp.Results, nil
}

// FormatPlaces renders results as plain text for the agent's context —
// not the UI's citation badges (those come from PlaceMapsURL per place).
func FormatPlaces(places []Place) string {
	if len(places) == 0 {
		return "No places found nearby."
	}
	var b strings.Builder
	for i, p := range places {
		dist := FormatDistance(p.Distance)
		cats := JoinCategories(p.Categories)
		fmt.Fprintf(&b, "%d. %s", i+1, p.Name)
		if cats != "" {
			fmt.Fprintf(&b, " (%s)", cats)
		}
		fmt.Fprintf(&b, " — %s", dist)
		if addr := PlaceAddress(p); addr != "" {
			fmt.Fprintf(&b, " | %s", addr)
		}
		b.WriteString("\n")
	}
	return b.String()
}

func JoinCategories(cats []PlaceCategory) string {
	if len(cats) == 0 {
		return ""
	}
	names := make([]string, len(cats))
	for i, c := range cats {
		names[i] = c.Name
	}
	return strings.Join(names, ", ")
}

func PlaceAddress(p Place) string {
	if p.Location.FormattedAddress != "" {
		return p.Location.FormattedAddress
	}
	return p.Location.Address
}

func PlaceMapsURL(p Place) string {
	if p.Latitude == 0 && p.Longitude == 0 {
		return ""
	}
	return fmt.Sprintf("https://maps.google.com/?q=%f,%f", p.Latitude, p.Longitude)
}

func FormatDistance(meters int) string {
	if meters <= 0 {
		return "nearby"
	}
	if meters < 1000 {
		return fmt.Sprintf("%dm away", meters)
	}
	walkMins := int(math.Round(float64(meters) / 80.0))
	km := float64(meters) / 1000.0
	if walkMins <= 30 {
		return fmt.Sprintf("%.1fkm (~%d min walk)", km, walkMins)
	}
	return fmt.Sprintf("%.1fkm away", km)
}

// FilterByRelevance drops results whose name/categories share no words
// with the query — Foursquare's text search matches address text too, so
// a "coffee shop" query can otherwise return a barber shop on Coffee St.
func FilterByRelevance(places []Place, query string) []Place {
	queryWords := strings.Fields(strings.ToLower(query))
	if len(queryWords) == 0 {
		return places
	}
	filtered := make([]Place, 0, len(places))
	for _, p := range places {
		if placeMatchesQuery(p, queryWords) {
			filtered = append(filtered, p)
		}
	}
	if len(filtered) == 0 {
		return places // a weak match beats no results
	}
	return filtered
}

func placeMatchesQuery(p Place, queryWords []string) bool {
	nameLower := strings.ToLower(p.Name)
	for _, qw := range queryWords {
		if strings.Contains(nameLower, qw) {
			return true
		}
	}
	for _, cat := range p.Categories {
		catLower := strings.ToLower(cat.Name)
		for _, qw := range queryWords {
			if strings.Contains(catLower, qw) {
				return true
			}
		}
	}
	return false
}
