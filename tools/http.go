package tools

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"time"
)

// httpUserAgent is sent on every outbound request this package's tools
// make to a third-party API — a handful of providers (GitHub, Open
// Library among them) reject or rate-limit requests with no User-Agent
// at all, and a consistent identifying string is good citizenship either
// way.
const httpUserAgent = "Polaris/1.0 (personal search assistant)"

// maxAPIResponseBytes bounds a plain JSON API response read via
// httpGetJSON (and the same-shaped ad hoc client.Do calls elsewhere in
// this package that don't go through it) — same "never trust a remote
// Content-Length header" reasoning fetch_url.go/web_read.go/view_image.go
// already apply to arbitrary fetched content, just not previously applied
// here since these providers (GitHub, Last.fm, TMDB, Open Library, Open-
// Meteo, dictionaryapi.dev, Wikipedia/arXiv) were assumed well-behaved.
// This is a backstop against a misbehaving/compromised upstream or a
// redirect landing somewhere unexpected, not a tight fit — a normal JSON
// response from any of these is a few KB to a few hundred KB at most.
const maxAPIResponseBytes = 10 << 20 // 10MB

// httpGetJSON issues a GET request and returns its raw body plus status
// code — the transport plumbing (request construction, User-Agent, a
// 10-second timeout, reading the body) is identical across every JSON
// API this package's tools call, even though each one's response/error
// shape differs (Last.fm's HTTP-200-with-error-field vs TMDB's non-2xx
// status, for two). Callers own their own status-code and error-shape
// parsing; this only exists so that plumbing isn't copy-pasted anew in
// every *Get function.
func httpGetJSON(ctx context.Context, url string) (body []byte, statusCode int, err error) {
	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return nil, 0, err
	}
	req.Header.Set("User-Agent", httpUserAgent)

	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, 0, err
	}
	defer resp.Body.Close()

	body, err = io.ReadAll(io.LimitReader(resp.Body, maxAPIResponseBytes+1))
	if err != nil {
		return nil, resp.StatusCode, err
	}
	if len(body) > maxAPIResponseBytes {
		return nil, resp.StatusCode, fmt.Errorf("response exceeds %d byte limit", maxAPIResponseBytes)
	}
	return body, resp.StatusCode, nil
}
