// Package reddit reads Reddit threads and listings for web_read.
//
// Reddit closed nearly every door in 2026: unauthenticated HTML/.json fetches
// from non-browser clients get a 403 or a JS challenge (TLS-fingerprint and
// IP-reputation checks, so a spoofed User-Agent doesn't help), Atom feeds stop
// being served on 2026-11-13, new Data API app registrations closed on
// 2026-10-31, and the public Data API itself ends in March 2027. Alternative
// frontends don't rescue it either: self-hosted Redlib v0.40.0 gets the same
// 403s from an ordinary residential IP, and the public instances behind
// redirector hubs like ueddit.com sit behind Anubis/Cloudflare bot walls (all
// verified live on 2026-11-11).
//
// Reads therefore go through an ordered list of sources, not one hardcoded
// trick — see newSources:
//
//  1. The Arctic Shift archive (default; no key, no credentials) — a
//     community-run Reddit archive whose JSON API returns Reddit-shaped post
//     and comment objects, so it feeds the same formatting code as the
//     official API. It carries a thread and its full comment tree for anything
//     more than a few minutes old; brand-new comments can lag hours, and
//     score/comment counts only settle after roughly 36h.
//  2. Reddit's own OAuth Data API, registered only when the operator has
//     credentials. UNVERIFIED against live Reddit: app registration is closed
//     and our own access request was denied, so this tier exists for whoever
//     already holds an old app's credentials. If Reddit refuses it, the
//     archive below still answers.
//
// Fetch tries each source in order and, when all of them fail, returns an
// error naming every source and what happened to it. That text matters twice:
// the model needs to know not to retry or spend a Tavily credit, and whoever
// is deciding whether to swap an engine needs to see which one broke and how.
// Adding a source (another archive, a frontend that starts working again, a
// paid API) is one entry in newSources — nothing outside this package changes.
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
// shape. Both Reddit and the archive throttle generic/library User-Agents
// harder than a descriptive one, so this is worth getting right.
const defaultUserAgent = "linux:polaris:1.0 (self-hosted assistant; +https://github.com/AutumnsGrove/Polaris)"

// installedClientDeviceID is the value Reddit documents for an app-only
// installed_client grant when there is no per-device identity to report.
const installedClientDeviceID = "DO_NOT_TRACK_THIS_DEVICE"

// ErrUnsupportedURL means the URL is on Reddit but isn't a shape any source
// can map to something it knows how to read (share links like /r/x/s/abc need
// a redirect Reddit won't serve us, media/image hosts, the site front page).
var ErrUnsupportedURL = errors.New("unsupported Reddit URL")

// ErrNotArchived means the archive answered normally but has no copy of the
// requested thing. Deliberately distinct from "the archive is down": the first
// means too new, deleted, or not carried, the second means retry later or swap
// engines, and both messages say which they are.
var ErrNotArchived = errors.New("not in the archive")

// Client reads Reddit. It is never nil-by-configuration: with no credentials
// it skips the OAuth tier and reads through the archive, which needs no key.
// Safe for concurrent use.
type Client struct {
	http      *http.Client
	userAgent string
	sources   []source

	// OAuth tier state; unused by the archive tier.
	clientID     string
	clientSecret string
	tokenURL     string
	apiBase      string

	mu          sync.Mutex
	token       string
	tokenExpiry time.Time

	// Archive tier state. archiveNext is the earliest time the next request
	// may start (see archiveInterval); the mutex also keeps a single thread
	// read's two requests from racing each other into a 429.
	archiveBase     string
	archiveInterval time.Duration
	archiveMu       sync.Mutex
	archiveNext     time.Time
}

// source is one way of reading a Reddit page. Fetch tries sources in order;
// name is what Result.Source and the aggregated failure message report, so
// keep it short and provider-specific.
type source struct {
	name  string
	fetch func(context.Context, redditPath) (*Result, error)
}

// NewClient builds a Client. An empty clientID means archive-only; clientSecret
// may be empty even with a clientID (an installed-app credential). userAgent
// may be empty for the default. archiveBase may be empty for Arctic Shift, or
// point anywhere that speaks the same API — the knob exists so a dead archive
// is a config change rather than a code change.
func NewClient(clientID, clientSecret, userAgent, archiveBase string) *Client {
	if userAgent == "" {
		userAgent = defaultUserAgent
	}
	if archiveBase == "" {
		archiveBase = defaultArchiveBase
	}
	c := &Client{
		clientID:     clientID,
		clientSecret: clientSecret,
		userAgent:    userAgent,
		http:         &http.Client{Timeout: 20 * time.Second},
		tokenURL:     "https://www.reddit.com/api/v1/access_token",
		apiBase:      "https://oauth.reddit.com",
		archiveBase:  strings.TrimRight(archiveBase, "/"),
		// Tests override this; see NewClientForTest.
		archiveInterval: archiveMinInterval,
	}
	c.sources = c.newSources()
	return c
}

// NewClientForTest points every endpoint at base (token at base+"/token", API
// at base, archive at base) and disables the archive's request spacing, which
// exists for a real archive's benefit and would just make tests sleep.
// Exported solely for other packages' tests.
func NewClientForTest(clientID, clientSecret, base string) *Client {
	c := NewClient(clientID, clientSecret, "", base)
	c.http = &http.Client{Timeout: 5 * time.Second}
	c.tokenURL, c.apiBase = base+"/token", base
	c.archiveInterval = 0
	return c
}

// newSources is the read chain, in the order Fetch tries it. The OAuth tier is
// only registered when credentials exist, so an unconfigured install makes
// exactly one request per read and its failure text doesn't mention a tier it
// never had.
func (c *Client) newSources() []source {
	var out []source
	if c.Authenticated() {
		out = append(out, source{name: "reddit api", fetch: c.fetchAPI})
	}
	return append(out, source{name: "arctic shift archive", fetch: c.fetchArchive})
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
	// Source names the tier that answered ("arctic shift archive", "reddit
	// api"), for logging and for web_read's [via …] tag.
	Source string
}

// Fetch reads rawURL through the first source that succeeds.
func (c *Client) Fetch(ctx context.Context, rawURL string) (*Result, error) {
	p, err := parseURL(rawURL)
	if err != nil {
		return nil, err
	}

	errs := make([]error, 0, len(c.sources))
	for _, s := range c.sources {
		res, err := s.fetch(ctx, p)
		if err == nil {
			return res, nil
		}
		// A cancelled turn isn't a source failing: don't try the next one and
		// don't dress the cancellation up as a source error.
		if ctxErr := ctx.Err(); ctxErr != nil {
			return nil, ctxErr
		}
		errs = append(errs, fmt.Errorf("%s: %w", s.name, err))
	}
	return nil, &FetchError{URL: rawURL, Failures: errs}
}

// FetchError reports every source's failure for one read, in the order they
// were tried. It unwraps to the individual errors, so callers can still ask
// errors.Is(err, ErrNotArchived) — or ErrUnsupportedURL — without parsing text.
// The message names every source because "the archive is down" and "this page
// isn't in it" call for different responses, and only the source that answered
// can say which happened.
type FetchError struct {
	URL      string
	Failures []error
}

func (e *FetchError) Error() string {
	shown := make([]string, 0, len(e.Failures))
	for _, err := range e.Failures {
		shown = append(shown, err.Error())
	}
	return "no Reddit source could read it (" + strings.Join(shown, "; ") + ")"
}

// Unwrap exposes every source's error to errors.Is/errors.As.
func (e *FetchError) Unwrap() []error { return e.Failures }

// redditPath is a Reddit URL reduced to the parts the sources need.
type redditPath struct {
	path     string // normalized, no trailing slash or .json/.rss suffix; always starts with "/"
	query    url.Values
	isThread bool

	// Derived pieces, filled by parseURL. Empty when the URL has no such part
	// (and empty subreddit for the synthetic "popular"/"all" listings).
	postID    string
	subreddit string
	user      string
	sort      string // the listing's sort segment: hot, new, top, rising…
	search    bool   // /r/<sub>/search
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

	p := redditPath{path: path, query: u.Query()}
	for i, s := range segs {
		switch {
		case s == "comments":
			p.isThread = true
			if i+1 < len(segs) {
				p.postID = segs[i+1]
			}
		case s == "r" && i == 0 && i+1 < len(segs):
			// "popular" and "all" are synthetic site-wide listings, not
			// subreddits an archive can list.
			if sub := segs[i+1]; sub != "popular" && sub != "all" {
				p.subreddit = sub
			}
		case (s == "u" || s == "user") && i == 0 && i+1 < len(segs):
			p.user = segs[i+1]
		}
	}
	if p.subreddit != "" && len(segs) >= 3 {
		switch segs[2] {
		case "search":
			p.search = true
		case "comments":
			// Thread; postID already set above.
		default:
			p.sort = segs[2]
		}
	}
	return p, nil
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

	body, status, _, err := c.do(req)
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

		b, status, _, err := c.do(req)
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

// do runs req and returns the size-bounded body, status code and headers.
func (c *Client) do(req *http.Request) ([]byte, int, http.Header, error) {
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, 0, nil, err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes+1))
	if err != nil {
		return nil, resp.StatusCode, resp.Header, fmt.Errorf("reading response: %w", err)
	}
	if len(data) > maxResponseBytes {
		return nil, resp.StatusCode, resp.Header, fmt.Errorf("response exceeds %d byte limit", maxResponseBytes)
	}
	return data, resp.StatusCode, resp.Header, nil
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}
