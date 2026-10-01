// Package reddit reads Reddit threads and listings for web_read.
//
// Plain scraping is closed: since mid-2026 Reddit answers unauthenticated
// HTML/.json fetches from non-browser clients with a JS challenge or a 403
// (TLS-fingerprint + IP-reputation checks, so a spoofed User-Agent doesn't
// help), and the hosted readers web_read normally falls back to (Tavily
// Extract, archive.org, Jina Reader) are all refused too — which is why a
// Reddit URL used to come back as nothing. Two paths still work, tried in
// order by Fetch:
//
//  1. Reddit's official OAuth Data API (oauth.reddit.com) with the
//     operator's own app credentials. New app registrations are mostly
//     refused now, but credentials that already exist (e.g. an old
//     third-party-client app) keep working — see config.Reddit. App-only
//     auth: a client secret uses the client_credentials grant, an empty one
//     (Reddit's "installed app" type) uses the installed_client grant.
//  2. The public Atom feeds (<url>/.rss), no credentials at all. Flat text
//     and rate-limited, and Reddit has publicly called feeds "another
//     common surface for scraping", so treat this as a best-effort floor
//     rather than something to build on.
package reddit

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

// maxResponseBytes bounds any single response, same reasoning as
// tools/web_read.go's readLimited — a remote Content-Length is never trusted.
const maxResponseBytes = 10 << 20 // 10MB

// defaultUserAgent follows Reddit's documented "<platform>:<app>:<version>"
// shape. Reddit throttles generic/library User-Agents far harder than a
// descriptive one, so this is worth getting right even for the RSS path.
const defaultUserAgent = "linux:polaris:1.0 (self-hosted assistant; +https://github.com/AutumnsGrove/Polaris)"

// installedClientDeviceID is the value Reddit documents for an app-only
// installed_client grant when there is no per-device identity to report.
const installedClientDeviceID = "DO_NOT_TRACK_THIS_DEVICE"

// ErrUnsupportedURL means the URL is on Reddit but isn't a shape Fetch can
// map to an API path (share links like /r/x/s/abc need a redirect Reddit
// won't serve us, media/image hosts, etc.).
var ErrUnsupportedURL = errors.New("unsupported Reddit URL")

// Client reads Reddit. It is never nil-by-configuration: with no credentials
// it simply skips the OAuth tier and goes straight to RSS, since that path
// needs no key. Safe for concurrent use.
type Client struct {
	clientID     string
	clientSecret string
	userAgent    string
	http         *http.Client

	// Endpoints are fields (not consts) so tests can point them at an
	// httptest server; see NewClientForTest.
	tokenURL string
	apiBase  string
	rssBase  string

	mu          sync.Mutex
	token       string
	tokenExpiry time.Time
}

// NewClient builds a Client. An empty clientID means RSS-only; clientSecret
// may be empty even with a clientID (an installed-app credential). userAgent
// may be empty for the default.
func NewClient(clientID, clientSecret, userAgent string) *Client {
	if userAgent == "" {
		userAgent = defaultUserAgent
	}
	return &Client{
		clientID:     clientID,
		clientSecret: clientSecret,
		userAgent:    userAgent,
		http:         &http.Client{Timeout: 20 * time.Second},
		tokenURL:     "https://www.reddit.com/api/v1/access_token",
		apiBase:      "https://oauth.reddit.com",
		rssBase:      "https://www.reddit.com",
	}
}

// NewClientForTest points every endpoint at base (token at base+"/token",
// API at base, RSS at base). Exported solely for other packages' tests.
func NewClientForTest(clientID, clientSecret, base string) *Client {
	c := NewClient(clientID, clientSecret, "")
	c.http = &http.Client{Timeout: 5 * time.Second}
	c.tokenURL, c.apiBase, c.rssBase = base+"/token", base, base
	return c
}

// Authenticated reports whether OAuth credentials are configured.
func (c *Client) Authenticated() bool { return c.clientID != "" }

// IsRedditURL reports whether rawURL is a Reddit page web_read should route
// here rather than through the generic fetch chain.
func IsRedditURL(rawURL string) bool {
	u, err := url.Parse(rawURL)
	if err != nil {
		return false
	}
	host := strings.ToLower(u.Hostname())
	return host == "redd.it" || host == "reddit.com" || strings.HasSuffix(host, ".reddit.com")
}

// Result is a successfully read Reddit page.
type Result struct {
	Title string
	Text  string
	// Source is "reddit api" or "reddit rss", for logging which tier answered.
	Source string
}

// Fetch reads rawURL via the OAuth API when configured, falling back to the
// public RSS feed if that's unconfigured or fails. The returned error names
// every tier that was tried, so the caller can tell the model *why* Reddit
// couldn't be read rather than just that it couldn't.
func (c *Client) Fetch(ctx context.Context, rawURL string) (*Result, error) {
	p, err := parseURL(rawURL)
	if err != nil {
		return nil, err
	}

	var apiErr error
	if c.Authenticated() {
		res, err := c.fetchAPI(ctx, p)
		if err == nil {
			return res, nil
		}
		apiErr = err
	}

	res, rssErr := c.fetchRSS(ctx, p)
	if rssErr == nil {
		return res, nil
	}
	if apiErr != nil {
		return nil, fmt.Errorf("reddit api: %v; reddit rss: %v", apiErr, rssErr)
	}
	return nil, fmt.Errorf("reddit rss: %v (no Reddit API credentials are configured, so the feed was the only option)", rssErr)
}

// redditPath is a Reddit URL reduced to what both tiers need.
type redditPath struct {
	path     string // normalized, no trailing slash or .json/.rss suffix; always starts with "/"
	query    url.Values
	isThread bool
}

func parseURL(rawURL string) (redditPath, error) {
	u, err := url.Parse(rawURL)
	if err != nil || !IsRedditURL(rawURL) {
		return redditPath{}, fmt.Errorf("%w: %q", ErrUnsupportedURL, rawURL)
	}

	path := strings.TrimSuffix(strings.TrimSuffix(strings.TrimRight(u.Path, "/"), ".json"), ".rss")
	path = strings.TrimRight(path, "/")

	// redd.it/<id> and /gallery/<id> are short forms of /comments/<id>.
	if strings.ToLower(u.Hostname()) == "redd.it" {
		id := strings.Trim(path, "/")
		if id == "" || strings.Contains(id, "/") {
			return redditPath{}, fmt.Errorf("%w: %q", ErrUnsupportedURL, rawURL)
		}
		path = "/comments/" + id
	}
	segs := strings.Split(strings.Trim(path, "/"), "/")
	if len(segs) == 2 && segs[0] == "gallery" {
		path = "/comments/" + segs[1]
		segs = []string{"comments", segs[1]}
	}
	if path == "" {
		path = "/r/popular"
		segs = []string{"r", "popular"}
	}

	for i, s := range segs {
		// /r/<sub>/s/<code> share links only resolve via a redirect that
		// Reddit refuses to non-browser clients; /media is an image proxy.
		if (s == "s" && i == 2) || s == "media" {
			return redditPath{}, fmt.Errorf("%w: %q (share/media links can't be resolved — open the thread's normal /comments/ URL instead)", ErrUnsupportedURL, rawURL)
		}
	}

	isThread := false
	for _, s := range segs {
		if s == "comments" {
			isThread = true
		}
	}
	return redditPath{path: path, query: u.Query(), isThread: isThread}, nil
}

// --- OAuth tier ---

func (c *Client) accessToken(ctx context.Context, force bool) (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !force && c.token != "" && time.Now().Before(c.tokenExpiry) {
		return c.token, nil
	}

	form := url.Values{}
	if c.clientSecret != "" {
		form.Set("grant_type", "client_credentials")
	} else {
		form.Set("grant_type", "https://oauth.reddit.com/grants/installed_client")
		form.Set("device_id", installedClientDeviceID)
	}
	req, err := http.NewRequestWithContext(ctx, "POST", c.tokenURL, strings.NewReader(form.Encode()))
	if err != nil {
		return "", err
	}
	req.SetBasicAuth(c.clientID, c.clientSecret)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("User-Agent", c.userAgent)

	body, status, err := c.do(req)
	if err != nil {
		return "", fmt.Errorf("requesting token: %w", err)
	}
	if status != http.StatusOK {
		return "", fmt.Errorf("token request returned status %d", status)
	}
	var tr struct {
		AccessToken string `json:"access_token"`
		ExpiresIn   int    `json:"expires_in"`
		Error       string `json:"error"`
	}
	if err := json.Unmarshal(body, &tr); err != nil {
		return "", fmt.Errorf("parsing token response: %w", err)
	}
	// Reddit reports bad credentials as HTTP 200 + {"error": "..."} on some
	// grant types, so a 200 alone isn't proof of a token.
	if tr.AccessToken == "" {
		return "", fmt.Errorf("token request rejected (%s) — check reddit.client_id/client_secret", firstNonEmpty(tr.Error, "no access_token in response"))
	}
	if tr.ExpiresIn <= 0 {
		tr.ExpiresIn = 3600
	}
	c.token = tr.AccessToken
	// Refresh a minute early so a token never expires mid-request.
	c.tokenExpiry = time.Now().Add(time.Duration(tr.ExpiresIn)*time.Second - time.Minute)
	return c.token, nil
}

func (c *Client) fetchAPI(ctx context.Context, p redditPath) (*Result, error) {
	q := url.Values{}
	for k, v := range p.query {
		q[k] = v
	}
	q.Set("raw_json", "1")
	if !p.isThread && q.Get("limit") == "" {
		q.Set("limit", "25")
	}
	endpoint := c.apiBase + p.path + "?" + q.Encode()

	var body []byte
	for attempt := 0; attempt < 2; attempt++ {
		tok, err := c.accessToken(ctx, attempt > 0)
		if err != nil {
			return nil, err
		}
		req, err := http.NewRequestWithContext(ctx, "GET", endpoint, nil)
		if err != nil {
			return nil, err
		}
		req.Header.Set("Authorization", "Bearer "+tok)
		req.Header.Set("User-Agent", c.userAgent)

		b, status, err := c.do(req)
		if err != nil {
			return nil, err
		}
		// A 401 on the first attempt means the cached token was revoked or
		// expired early — fetch a fresh one and retry once.
		if status == http.StatusUnauthorized && attempt == 0 {
			continue
		}
		if status != http.StatusOK {
			return nil, fmt.Errorf("api returned status %d", status)
		}
		body = b
		break
	}

	title, text, err := formatAPI(body)
	if err != nil {
		return nil, err
	}
	return &Result{Title: title, Text: text, Source: "reddit api"}, nil
}

// do runs req and returns the size-bounded body and status code.
func (c *Client) do(req *http.Request) ([]byte, int, error) {
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, 0, err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes+1))
	if err != nil {
		return nil, resp.StatusCode, fmt.Errorf("reading response: %w", err)
	}
	if len(data) > maxResponseBytes {
		return nil, resp.StatusCode, fmt.Errorf("response exceeds %d byte limit", maxResponseBytes)
	}
	return data, resp.StatusCode, nil
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}
