// Package parallel wraps Parallel's Search API — the first-choice
// fallback when the self-hosted SearXNG instance itself is degraded (see
// search.SearchResponse.Degraded), ahead of tavily's Search fallback.
// Preferred over Tavily specifically for its free tier (5,000
// requests/month vs. Tavily's 1,000), but the account has a card on
// file — going over the free tier bills real money — so callers must
// check a persisted usage count (see store.Store's api_usage table)
// against that 5,000 cap themselves before calling Search; this package
// has no awareness of the cap or any quota remaining on the account, it
// only executes the request it's asked to.
package parallel

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

type Client struct {
	apiKey  string
	baseURL string
	http    *http.Client
}

// maxResponseBytes bounds a response read from Parallel's API — same
// "never trust a remote Content-Length header" reasoning tools/web_read.go
// and friends apply to arbitrary fetched content, applied here to a paid
// third-party API response instead of assuming it's always small.
const maxResponseBytes = 10 << 20 // 10MB

// NewClient returns nil if apiKey is empty — callers check for nil to
// know whether Parallel is configured at all, mirroring
// tavily.NewClient's optional-dependency pattern.
func NewClient(apiKey string) *Client {
	if apiKey == "" {
		return nil
	}
	return &Client{
		apiKey:  apiKey,
		baseURL: "https://api.parallel.ai",
		http:    &http.Client{Timeout: 15 * time.Second},
	}
}

// NewClientForTest builds a Client against a custom baseURL — exported
// solely so other packages' tests can point it at an httptest server
// instead of the real API. Not used outside tests.
func NewClientForTest(apiKey, baseURL string) *Client {
	return &Client{apiKey: apiKey, baseURL: baseURL, http: &http.Client{Timeout: 5 * time.Second}}
}

type SearchResult struct {
	Title   string
	URL     string
	Content string
}

type SearchResponse struct {
	Query   string
	Results []SearchResult
}

type searchRequest struct {
	// Objective and SearchQueries carry the same query text — Objective
	// is the natural-language framing the API uses to shape relevance,
	// SearchQueries (required, "3-6 words" per Parallel's own docs) is
	// the literal query list. A single ad-hoc query fills both
	// identically rather than trying to split it into a "goal" versus a
	// "keyword phrase", which isn't a distinction this codebase's callers
	// (a plain web_search string) have any basis to make.
	Objective        string            `json:"objective,omitempty"`
	SearchQueries    []string          `json:"search_queries"`
	Mode             string            `json:"mode,omitempty"`
	AdvancedSettings *advancedSettings `json:"advanced_settings,omitempty"`
}

type advancedSettings struct {
	MaxResults   int           `json:"max_results,omitempty"`
	SourcePolicy *sourcePolicy `json:"source_policy,omitempty"`
}

// sourcePolicy lives under advanced_settings, not at the request's top
// level (a top-level source_policy is rejected with extra_forbidden —
// verified live).
type sourcePolicy struct {
	AfterDate string `json:"after_date,omitempty"`
}

// recencyDays is how far back each web_search recency value reaches.
// Parallel has no relative "past week" enum — only an absolute after_date
// cutoff — so the window has to be turned into a date at call time.
var recencyDays = map[string]int{"day": 1, "week": 7, "month": 30, "year": 365}

// afterDate returns the YYYY-MM-DD cutoff for a recency window relative to
// now, or "" when recency is unset/unrecognized. Takes now as a parameter
// so tests don't depend on the wall clock. UTC, since Parallel's date is
// timezone-less and a local date could land a day off the server's.
func afterDate(now time.Time, recency string) string {
	days, ok := recencyDays[recency]
	if !ok {
		return ""
	}
	return now.UTC().AddDate(0, 0, -days).Format("2006-01-02")
}

type searchAPIResponse struct {
	Results []struct {
		URL         string   `json:"url"`
		Title       *string  `json:"title"`
		PublishDate *string  `json:"publish_date"`
		Excerpts    []string `json:"excerpts"`
	} `json:"results"`
}

// Search runs a query against Parallel's Search API. Always mode:
// "turbo" — the cheapest of the four documented modes (turbo/fast/basic/
// advanced) at roughly 1/5th "advanced"'s per-request cost — this is a
// fallback-of-last-resort against a scarce budget, not a case where the
// higher-quality tiers are worth paying more for.
func (c *Client) Search(ctx context.Context, query string, maxResults int) (*SearchResponse, error) {
	return c.SearchRecent(ctx, query, maxResults, "")
}

// SearchRecent is Search plus an optional recency window ("day", "week",
// "month", "year"; "" for no filter), sent as source_policy.after_date.
// Note turbo mode returns publish_date null, so the filter can't be
// double-checked from the response — it was verified live by the result
// set shifting (old versioned docs dropped) and by a malformed date being
// rejected with a 400, which proves the field is actually parsed.
func (c *Client) SearchRecent(ctx context.Context, query string, maxResults int, recency string) (*SearchResponse, error) {
	if maxResults <= 0 {
		maxResults = 5
	}

	settings := &advancedSettings{MaxResults: maxResults}
	if d := afterDate(time.Now(), recency); d != "" {
		settings.SourcePolicy = &sourcePolicy{AfterDate: d}
	}
	payload, err := json.Marshal(searchRequest{
		Objective:        query,
		SearchQueries:    []string{query},
		Mode:             "turbo",
		AdvancedSettings: settings,
	})
	if err != nil {
		return nil, err
	}

	req, err := http.NewRequestWithContext(ctx, "POST", c.baseURL+"/v1/search", bytes.NewReader(payload))
	if err != nil {
		return nil, err
	}
	// Parallel's own header name, not the Authorization: Bearer scheme
	// tavily/foursquare/etc. use — confirmed against the live API, not
	// assumed from a generic REST convention.
	req.Header.Set("x-api-key", c.apiKey)
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("parallel search request failed: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes+1))
	if err != nil {
		return nil, fmt.Errorf("reading parallel response: %w", err)
	}
	if len(body) > maxResponseBytes {
		return nil, fmt.Errorf("parallel response exceeds %d byte limit", maxResponseBytes)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("parallel error (status %d): %s", resp.StatusCode, string(body))
	}

	var apiResp searchAPIResponse
	if err := json.Unmarshal(body, &apiResp); err != nil {
		return nil, fmt.Errorf("parsing parallel response: %w", err)
	}

	results := make([]SearchResult, 0, len(apiResp.Results))
	for _, r := range apiResp.Results {
		title := ""
		if r.Title != nil {
			title = *r.Title
		}
		results = append(results, SearchResult{
			Title: title,
			URL:   r.URL,
			// Excerpts is an array of separately-extracted passages, not
			// one block of text — joined with a blank line between so
			// they still read as distinct snippets rather than run
			// together.
			Content: strings.Join(r.Excerpts, "\n\n"),
		})
	}

	return &SearchResponse{Query: query, Results: results}, nil
}
