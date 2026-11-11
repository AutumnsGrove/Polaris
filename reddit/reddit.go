// Package reddit reads Reddit threads and listings for web_read.
//
// Reddit closed every door in 2026: unauthenticated HTML/.json fetches from
// non-browser clients get a 403 or a JS challenge (TLS-fingerprint and
// IP-reputation checks, so a spoofed User-Agent doesn't help), Atom feeds
// stopped on 2026-11-13, Data API app registrations closed on 2026-10-31, and
// the public Data API itself ends in March 2027. Alternative frontends don't
// rescue it either: self-hosted Redlib v0.40.0 gets the same 403s from an
// ordinary residential IP, and the public instances behind redirector hubs
// like ueddit.com sit behind Anubis/Cloudflare bot walls (all verified live on
// 2026-11-11). There is no first-party path left to build on.
//
// Reads go through one source: the Arctic Shift archive (see archive.go), a
// community-run Reddit archive with a keyless JSON API whose posts and
// comments are shaped like Reddit's own, so they render with formatting code
// that outlives the archive itself.
//
// That is still expressed as an ordered source list rather than a single call,
// because the archive is one person's server fed by the API Reddit shuts down
// in March 2027 — the next engine is a question of when, not if. Adding one
// (another archive, a frontend that starts working again, a paid API) is an
// entry in newSources; nothing outside this package changes. Fetch names every
// source that failed and why, which is the text that tells a model not to
// retry and tells a human which engine broke.
package reddit

import (
	"context"
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

// ErrUnsupportedURL means the URL is on Reddit but isn't a shape any source
// can map to something it knows how to read (share links like /r/x/s/abc need
// a redirect Reddit won't serve us, media/image hosts, the site front page).
var ErrUnsupportedURL = errors.New("unsupported Reddit URL")

// ErrNotArchived means the archive answered normally but has no copy of the
// requested thing. Deliberately distinct from "the archive is down": the first
// means too new, deleted, or not carried, the second means retry later or swap
// engines, and both messages say which they are.
var ErrNotArchived = errors.New("not in the archive")

// Client reads Reddit. It is never nil-by-configuration: the archive needs no
// key. Safe for concurrent use.
type Client struct {
	http      *http.Client
	userAgent string
	sources   []source

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

// NewClient builds a Client. userAgent may be empty for the default.
// archiveBase may be empty for Arctic Shift, or point anywhere that speaks the
// same API — the knob exists so a dead archive is a config change rather than
// a code change.
func NewClient(userAgent, archiveBase string) *Client {
	if userAgent == "" {
		userAgent = defaultUserAgent
	}
	if archiveBase == "" {
		archiveBase = defaultArchiveBase
	}
	c := &Client{
		userAgent:   userAgent,
		http:        &http.Client{Timeout: 20 * time.Second},
		archiveBase: strings.TrimRight(archiveBase, "/"),
		// Tests override this; see NewClientForTest.
		archiveInterval: archiveMinInterval,
	}
	c.sources = c.newSources()
	return c
}

// NewClientForTest points the archive at base and disables its request
// spacing, which exists for a real archive's benefit and would just make tests
// sleep. Exported solely for other packages' tests.
func NewClientForTest(base string) *Client {
	c := NewClient("", base)
	c.http = &http.Client{Timeout: 5 * time.Second}
	c.archiveInterval = 0
	return c
}

// newSources is the read chain, in the order Fetch tries it. One entry today —
// the OAuth Data API that used to be the backup is gone, because Reddit stopped
// accepting app registrations on 2026-10-31 and denied the access request for
// this project, so there is no way to obtain credentials to give it.
func (c *Client) newSources() []source {
	return []source{{name: "arctic shift archive", fetch: c.fetchArchive}}
}

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
	// Source names the tier that answered ("arctic shift archive"), for
	// logging and for web_read's [via …] tag.
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
	path  string // normalized, no trailing slash or .json/.rss suffix; always starts with "/"
	query url.Values

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
