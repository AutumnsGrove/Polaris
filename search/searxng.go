// Package search wraps a self-hosted SearXNG instance for web search.
// Ported from her-go's search/searxng.go.
package search

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

type SearchResult struct {
	Title     string  `json:"title"`
	URL       string  `json:"url"`
	Content   string  `json:"content"`
	Score     float64 `json:"score"`
	Thumbnail string  `json:"thumbnail,omitempty"`
}

type SearchResponse struct {
	Query   string         `json:"query"`
	Answer  string         `json:"answer,omitempty"`
	Results []SearchResult `json:"results"`
}

type SearXNGClient struct {
	baseURL string
	http    *http.Client
}

func NewSearXNGClient(baseURL string) *SearXNGClient {
	return &SearXNGClient{
		baseURL: strings.TrimSuffix(baseURL, "/"),
		http:    &http.Client{Timeout: 15 * time.Second},
	}
}

type searxngResponse struct {
	Query   string          `json:"query"`
	Results []searxngResult `json:"results"`
}

type searxngResult struct {
	Title     string  `json:"title"`
	URL       string  `json:"url"`
	Content   string  `json:"content"`
	Score     float64 `json:"score"`
	Thumbnail string  `json:"thumbnail"`
}

// Search performs a web search via SearXNG and returns up to maxResults
// relevance-ranked results. SearXNG doesn't produce an AI-generated
// answer summary, so Answer is always empty here (unlike Tavily).
// category filters which SearXNG engines answer the query — "" (SearXNG's
// default "general" category) searches ordinary web-indexed pages, which
// for broad queries like "atlanta ga news" routinely surface each outlet's
// homepage rather than a specific story, since the homepage's title/text
// matches a broad phrase just as well as any article does. "news" routes
// to dedicated news-search engines (Google News, Bing News, etc.), which
// index actual articles rather than static pages.
func (c *SearXNGClient) Search(ctx context.Context, query string, maxResults int, category string) (*SearchResponse, error) {
	if maxResults <= 0 {
		maxResults = 5
	}

	u := fmt.Sprintf("%s/search?format=json&q=%s", c.baseURL, url.QueryEscape(query))
	if category != "" {
		u += "&categories=" + url.QueryEscape(category)
	}

	req, err := http.NewRequestWithContext(ctx, "GET", u, nil)
	if err != nil {
		return nil, fmt.Errorf("creating request: %w", err)
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("search request failed: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("reading response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("searxng error (status %d): %s", resp.StatusCode, string(body))
	}

	var searxngResp searxngResponse
	if err := json.Unmarshal(body, &searxngResp); err != nil {
		return nil, fmt.Errorf("parsing response: %w", err)
	}

	results := make([]SearchResult, 0, len(searxngResp.Results))
	for i, r := range searxngResp.Results {
		if i >= maxResults {
			break
		}
		normalizedScore := r.Score / 10.0
		if normalizedScore > 1.0 {
			normalizedScore = 1.0
		}
		results = append(results, SearchResult{
			Title:     r.Title,
			URL:       r.URL,
			Content:   r.Content,
			Score:     normalizedScore,
			Thumbnail: r.Thumbnail,
		})
	}

	return &SearchResponse{Query: query, Results: results}, nil
}
