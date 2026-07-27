// Package tavily wraps Tavily's Extract API — a paid, remote fallback for
// pages web_read's free goquery-based fetch can't get through: JS-rendered
// SPAs whose content only exists after client-side JS runs. Tavily runs
// that rendering on their infrastructure, not ours, which is the whole
// point — a real headless-Chrome fallback isn't something the potato has
// room for.
package tavily

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
// know whether Tavily is configured at all, mirroring
// places.NewFoursquareClient's optional-dependency pattern.
func NewClient(apiKey string) *Client {
	if apiKey == "" {
		return nil
	}
	return &Client{
		apiKey:  apiKey,
		baseURL: "https://api.tavily.com",
		// Advanced extraction (real rendering) is slower than a plain
		// fetch — Tavily's own docs default to a 30s budget for it.
		http: &http.Client{Timeout: 30 * time.Second},
	}
}

// NewClientForTest builds a Client against a custom baseURL — exported
// solely so other packages' tests (web_read's fallback chain) can point
// it at an httptest server instead of the real API. Not used outside tests.
func NewClientForTest(apiKey, baseURL string) *Client {
	return &Client{apiKey: apiKey, baseURL: baseURL, http: &http.Client{Timeout: 5 * time.Second}}
}

type extractRequest struct {
	URLs         string `json:"urls"`
	ExtractDepth string `json:"extract_depth"`
	Format       string `json:"format"`
}

type extractResponse struct {
	Results []struct {
		URL        string `json:"url"`
		RawContent string `json:"raw_content"`
	} `json:"results"`
	FailedResults []struct {
		URL   string `json:"url"`
		Error string `json:"error"`
	} `json:"failed_results"`
}

// Extract fetches and extracts a single URL via Tavily's Extract API.
// advanced=true requests Tavily's JS-rendering-capable mode (their
// "extract_depth: advanced") at 2x the credit cost of basic mode — always
// worth it here, since web_read only calls this once the free path has
// already failed.
func (c *Client) Extract(ctx context.Context, rawURL string, advanced bool) (text string, err error) {
	depth := "basic"
	if advanced {
		depth = "advanced"
	}

	payload, err := json.Marshal(extractRequest{URLs: rawURL, ExtractDepth: depth, Format: "text"})
	if err != nil {
		return "", err
	}

	req, err := http.NewRequestWithContext(ctx, "POST", c.baseURL+"/extract", bytes.NewReader(payload))
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bearer "+c.apiKey)
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.http.Do(req)
	if err != nil {
		return "", fmt.Errorf("tavily extract request failed: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("reading tavily response: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("tavily error (status %d): %s", resp.StatusCode, string(body))
	}

	var extractResp extractResponse
	if err := json.Unmarshal(body, &extractResp); err != nil {
		return "", fmt.Errorf("parsing tavily response: %w", err)
	}

	// Failures come back as HTTP 200 with the URL listed in
	// FailedResults instead of Results — Tavily's batch-oriented shape,
	// even for our single-URL calls.
	if len(extractResp.Results) == 0 {
		if len(extractResp.FailedResults) > 0 {
			return "", fmt.Errorf("tavily extract failed: %s", extractResp.FailedResults[0].Error)
		}
		return "", fmt.Errorf("tavily returned no results")
	}

	return strings.TrimSpace(extractResp.Results[0].RawContent), nil
}
