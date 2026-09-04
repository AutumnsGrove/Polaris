// Package brave wraps Brave's Search API — a real, multi-result search
// index (not an AI-summarized answer like tavily/parallel), used as the
// second-tier fallback once the self-hosted SearXNG instance itself is
// degraded (see search.SearchResponse.Degraded): SearXNG -> Brave ->
// Parallel -> Tavily. Brave goes ahead of Parallel/Tavily specifically
// because it returns real result listings suited to Atlas's browsing UI,
// where Parallel/Tavily's pre-summarized, agent-oriented output isn't a
// good fit. Brave has no ongoing free tier beyond a one-time $5/mo
// signup credit — every query bills the account's card on file, so
// callers must check a persisted usage count (see store.Store's
// api_usage table) against a hard monthly cap themselves before calling
// Search; this package has no awareness of the cap, it only executes
// the request it's asked to.
package brave

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"time"
)

// MaxCount is Brave's own hard ceiling on results per request (the
// "count" param) — asking for more doesn't get more, so Search always
// requests this many rather than accepting a caller-supplied count that
// might quietly get clamped server-side without the caller knowing.
// Exported so callers building a cache key around "what did we actually
// request" (see gateway's Atlas resolver) can reference the real value
// instead of hardcoding 20 a second time.
const MaxCount = 20

// maxOffset is Brave's own hard ceiling on how many pages deep "offset"
// can go (0-indexed, so offsets 0-9 are valid — 10 real pages). Combined
// with MaxCount, that's up to 200 total results reachable per query.
const maxOffset = 9

// MonthlyCap is the hard ceiling on Brave Search API calls per calendar
// month, matching Brave's own $5/mo signup credit (~1,000 queries at
// $5/1,000) — unlike Parallel/Tavily, Brave has no ongoing free tier, so
// every query past this bills the account's card on file. Brave's own
// dashboard also auto-stops the key at 1,000/mo, so callers enforcing
// this (see store.Store's api_usage table) are belt-and-suspenders
// rather than the only thing standing between usage and a bill — kept
// anyway for a clear in-app error message instead of silently hitting
// Brave's own 4xx. Exported so every caller (tools/web_search.go's
// agent-facing fallback, gateway/search.go's Atlas fallback) enforces
// the exact same number rather than two consts drifting apart.
const MonthlyCap = 1000

type Client struct {
	apiKey  string
	baseURL string
	// imagesBaseURL is Brave's separate Image Search endpoint — see
	// images.go's doc comment on why this is a second field rather than a
	// package-level const: NewClientForTest needs to point both endpoints
	// at the same fake server.
	imagesBaseURL string
	http          *http.Client
}

// NewClient returns nil if apiKey is empty — callers check for nil to
// know whether Brave is configured at all, mirroring tavily.NewClient's
// and parallel.NewClient's optional-dependency pattern.
func NewClient(apiKey string) *Client {
	if apiKey == "" {
		return nil
	}
	return &Client{
		apiKey:        apiKey,
		baseURL:       "https://api.search.brave.com/res/v1/web/search",
		imagesBaseURL: "https://api.search.brave.com/res/v1/images/search",
		http:          &http.Client{Timeout: 15 * time.Second},
	}
}

// NewClientForTest builds a Client against a custom baseURL — exported
// solely so other packages' tests can point it at an httptest server
// instead of the real API. Not used outside tests. imagesBaseURL is
// baseURL+"/images", a distinct sub-path so one fake server can mux
// Search and SearchImages requests apart by path.
func NewClientForTest(apiKey, baseURL string) *Client {
	return &Client{apiKey: apiKey, baseURL: baseURL, imagesBaseURL: baseURL + "/images", http: &http.Client{Timeout: 5 * time.Second}}
}

type SearchResult struct {
	Title   string
	URL     string
	Content string
}

type SearchResponse struct {
	Query   string
	Results []SearchResult
	// MoreAvailable mirrors Brave's own query.more_results_available —
	// whether a caller should bother requesting the next offset at all,
	// same idea as search.SearchResponse.HasMore.
	MoreAvailable bool
}

type searchAPIResponse struct {
	Query struct {
		MoreResultsAvailable bool `json:"more_results_available"`
	} `json:"query"`
	Web struct {
		Results []struct {
			Title         string   `json:"title"`
			URL           string   `json:"url"`
			Description   string   `json:"description"`
			ExtraSnippets []string `json:"extra_snippets"`
		} `json:"results"`
	} `json:"web"`
}

// Search runs a query against Brave's Web Search API at the given
// 0-indexed offset (clamped to [0, maxOffset]) — always requesting
// MaxCount results, Brave's own ceiling, so a caller doing its own
// virtual sub-pagination (splitting one real 20-result page into two
// 10-result display pages, say) gets the most raw results per real
// request rather than under-asking.
func (c *Client) Search(ctx context.Context, query string, offset int) (*SearchResponse, error) {
	if offset < 0 {
		offset = 0
	}
	if offset > maxOffset {
		offset = maxOffset
	}

	u := fmt.Sprintf("%s?q=%s&count=%d&offset=%d", c.baseURL, url.QueryEscape(query), MaxCount, offset)
	req, err := http.NewRequestWithContext(ctx, "GET", u, nil)
	if err != nil {
		return nil, err
	}
	// Brave's own header name, not the Authorization: Bearer scheme
	// tavily/foursquare/etc. use — confirmed against Brave's official API
	// docs, not assumed from a generic REST convention.
	req.Header.Set("X-Subscription-Token", c.apiKey)
	req.Header.Set("Accept", "application/json")

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("brave search request failed: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("reading brave response: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("brave error (status %d): %s", resp.StatusCode, string(body))
	}

	var apiResp searchAPIResponse
	if err := json.Unmarshal(body, &apiResp); err != nil {
		return nil, fmt.Errorf("parsing brave response: %w", err)
	}

	results := make([]SearchResult, 0, len(apiResp.Web.Results))
	for _, r := range apiResp.Web.Results {
		content := r.Description
		// extra_snippets are separately-extracted passages beyond the
		// main description, same "join with a blank line" treatment
		// parallel.Client.Search gives Parallel's own Excerpts field.
		for _, s := range r.ExtraSnippets {
			content += "\n\n" + s
		}
		results = append(results, SearchResult{Title: r.Title, URL: r.URL, Content: content})
	}

	return &SearchResponse{Query: query, Results: results, MoreAvailable: apiResp.Query.MoreResultsAvailable}, nil
}
