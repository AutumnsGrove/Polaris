package brave

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
)

// imagesDefaultCount is what SearchImages requests when count <= 0 — far
// under Brave's own default (count=50) and hard ceiling (200), since
// image_search only ever renders a handful of tiles (see
// tools/image_search.go's doc comment on why 10, not brave.MaxCount).
const imagesDefaultCount = 10

type ImageSearchResult struct {
	Title string
	// URL is the source page the image was found on, not the image file
	// itself — matches tools.Card's URL field (click-through target),
	// with ImageSrc separately holding the thumbnail to actually display.
	URL      string
	ImageSrc string
	// FullImageURL is Brave's own "properties.url" — the full-resolution
	// image, distinct from ImageSrc/Thumbnail.Src which is deliberately a
	// small preview. Always populated when Brave provides it, independent
	// of whether ImageSrc had to fall back to this same field below.
	FullImageURL string
	// Source is the result's display domain (Brave's own "source" field) —
	// falls back to a parsed hostname from URL when Brave omits it, same
	// defensive shape as tools/image_search.go's domain fallback for
	// SearXNG results.
	Source string
}

type ImageSearchResponse struct {
	Query   string
	Results []ImageSearchResult
}

type imageSearchAPIResponse struct {
	Results []struct {
		Title     string `json:"title"`
		URL       string `json:"url"`
		Source    string `json:"source"`
		Thumbnail struct {
			Src string `json:"src"`
		} `json:"thumbnail"`
		Properties struct {
			URL string `json:"url"`
		} `json:"properties"`
	} `json:"results"`
}

// SearchImages runs a query against Brave's Image Search API — a
// separate endpoint from Search above (see imagesBaseURL), same
// X-Subscription-Token auth. count is clamped to [1, 200], Brave's own
// range; callers wanting a small gallery should pass a small count
// themselves rather than relying on Brave to under-return.
func (c *Client) SearchImages(ctx context.Context, query string, count int) (*ImageSearchResponse, error) {
	if count <= 0 {
		count = imagesDefaultCount
	}
	if count > 200 {
		count = 200
	}

	u := fmt.Sprintf("%s?q=%s&count=%d", c.imagesBaseURL, url.QueryEscape(query), count)
	req, err := http.NewRequestWithContext(ctx, "GET", u, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("X-Subscription-Token", c.apiKey)
	req.Header.Set("Accept", "application/json")

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("brave image search request failed: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("reading brave image search response: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("brave image search error (status %d): %s", resp.StatusCode, string(body))
	}

	var apiResp imageSearchAPIResponse
	if err := json.Unmarshal(body, &apiResp); err != nil {
		return nil, fmt.Errorf("parsing brave image search response: %w", err)
	}

	results := make([]ImageSearchResult, 0, len(apiResp.Results))
	for _, r := range apiResp.Results {
		imageSrc := r.Thumbnail.Src
		if imageSrc == "" {
			imageSrc = r.Properties.URL
		}
		source := r.Source
		if source == "" {
			if parsed, err := url.Parse(r.URL); err == nil {
				source = parsed.Hostname()
			}
		}
		results = append(results, ImageSearchResult{
			Title: r.Title, URL: r.URL, ImageSrc: imageSrc, FullImageURL: r.Properties.URL, Source: source,
		})
	}

	return &ImageSearchResponse{Query: query, Results: results}, nil
}
