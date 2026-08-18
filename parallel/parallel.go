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
	MaxResults int `json:"max_results,omitempty"`
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
	if maxResults <= 0 {
		maxResults = 5
	}

	payload, err := json.Marshal(searchRequest{
		Objective:        query,
		SearchQueries:    []string{query},
		Mode:             "turbo",
		AdvancedSettings: &advancedSettings{MaxResults: maxResults},
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

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("reading parallel response: %w", err)
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
