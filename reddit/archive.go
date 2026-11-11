package reddit

// The archive tier: Arctic Shift (https://arctic-shift.photon-reddit.com), a
// community-run Reddit archive with a keyless JSON API documented at
// https://github.com/ArthurHeitmann/arctic_shift/blob/master/api/README.md.
// On 2026-11-11 it was the only read path that answered for an ordinary
// server: Reddit 403s the site itself, and every Redlib instance we tried was
// behind Anubis or Cloudflare.
//
// Two properties shape this file:
//
//   - Its responses are Reddit-shaped — posts are t3 objects, and a comment's
//     replies are either "" or a Listing exactly like the live API's — so
//     format.go needs no knowledge of where a result came from.
//   - It is one person's server with no commitments ("No uptime or performance
//     guarantees :)", says its README, and it is fed by the same Data API that
//     Reddit closes in March 2027). So the base URL is configurable, requests
//     are spaced out, and the error text distinguishes "archive is down" from
//     "this page isn't in it" — the first is a reason to swap engines, the
//     second is a reason to tell the model there's nothing here.

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

const (
	// defaultArchiveBase is Arctic Shift's public API.
	defaultArchiveBase = "https://arctic-shift.photon-reddit.com"

	// archiveCommentLimit caps how much of a comment tree we ask for. The API
	// accepts up to 25000 and collapses the rest into "more" stubs; a cap
	// keeps a mega-thread from becoming a multi-megabyte read that web_read
	// would truncate anyway.
	archiveCommentLimit = 200

	// archiveListingLimit is a page of thread links: enough to browse, small
	// enough that the result stays a readable tool output.
	archiveListingLimit = 25

	// archiveMinInterval spaces consecutive archive requests. Its README asks
	// for "a couple requests per second" at most, and one thread read is two
	// requests.
	archiveMinInterval = 500 * time.Millisecond

	// archiveMaxRetryWait bounds the one retry archiveGet makes after an
	// overload: above this, waiting out the reset window would stall the turn
	// more than the read is worth.
	archiveMaxRetryWait = 3 * time.Second

	// archiveSettledAfter is when the archive's copy of a post stops changing:
	// it stores a post the moment it appears (score 1, num_comments 0) and
	// refreshes it with the real numbers about 36 hours later. Anything
	// younger gets a note in the rendered text so a "1 point · 0 comments"
	// header isn't mistaken for the post having been ignored.
	archiveSettledAfter = 36 * time.Hour
)

// fetchArchive dispatches a parsed Reddit URL to the archive endpoint that can
// answer it. Every case that ends in the default is a URL shape the archive has
// no equivalent for — say so plainly rather than returning an empty page.
func (c *Client) fetchArchive(ctx context.Context, p redditPath) (*Result, error) {
	switch {
	case p.postID != "":
		return c.archiveThread(ctx, p)
	case p.user != "":
		return c.archivePosts(ctx, p, "author", p.user)
	case p.search && p.subreddit != "":
		return c.archivePosts(ctx, p, "query", p.query.Get("q"))
	case p.subreddit != "":
		return c.archivePosts(ctx, p, "subreddit", p.subreddit)
	}
	return nil, fmt.Errorf("%w: %q — the archive can read threads, subreddit listings, subreddit searches and user pages, but not the site-wide front page", ErrUnsupportedURL, p.path)
}

// archiveThread reads a post and its comment tree: two requests, because the
// archive keeps posts and comments in separate endpoints.
func (c *Client) archiveThread(ctx context.Context, p redditPath) (*Result, error) {
	body, err := c.archiveGet(ctx, "/api/posts/ids", url.Values{"ids": {p.postID}})
	if err != nil {
		return nil, err
	}
	var posts struct {
		Data []post `json:"data"`
	}
	if err := json.Unmarshal(body, &posts); err != nil {
		return nil, fmt.Errorf("arctic shift sent unreadable post data (its API may have changed): %w", err)
	}
	if len(posts.Data) == 0 {
		return nil, fmt.Errorf("%w: post %s — brand-new posts can take a few minutes to be indexed, and deleted or never-indexed posts never show up", ErrNotArchived, p.postID)
	}
	post := posts.Data[0]

	// A readable post with no comments still answers the question far better
	// than an error about the thread, so a tree failure degrades the result
	// instead of sinking it.
	var notes []string
	// Only the archive's "stored the moment it was posted" signature (score 1,
	// no comment count, under 36h old) gets the caveat. A day-old thread whose
	// numbers have already been refreshed doesn't, or the note becomes noise
	// the model repeats back as fact.
	if post.CreatedUTC > 0 && post.Score <= 1 && post.NumComments == 0 {
		if age := time.Since(time.Unix(int64(post.CreatedUTC), 0)); age < archiveSettledAfter {
			notes = append(notes, "this archive copy was stored when the post was new, so its score and comment count may not have caught up and recent replies may be missing")
		}
	}
	comments, err := c.archiveComments(ctx, p.postID)
	if err != nil {
		notes = append(notes, "comments unavailable ("+err.Error()+")")
	}
	title, text := renderThread(post, comments, strings.Join(notes, "; "))
	return &Result{Title: title, Text: text, Source: "arctic shift archive"}, nil
}

func (c *Client) archiveComments(ctx context.Context, postID string) ([]thing, error) {
	body, err := c.archiveGet(ctx, "/api/comments/tree", url.Values{
		"link_id": {postID},
		"limit":   {strconv.Itoa(archiveCommentLimit)},
	})
	if err != nil {
		return nil, err
	}
	var tree struct {
		Data []thing `json:"data"`
	}
	if err := json.Unmarshal(body, &tree); err != nil {
		return nil, fmt.Errorf("arctic shift sent an unreadable comment tree (its API may have changed): %w", err)
	}
	return tree.Data, nil
}

// archivePosts covers the three listing-shaped URLs — subreddit, subreddit
// search, user page — which all hit /api/posts/search with a different filter.
func (c *Client) archivePosts(ctx context.Context, p redditPath, key, value string) (*Result, error) {
	if strings.TrimSpace(value) == "" {
		return nil, fmt.Errorf("%w: %q has no %s to look up", ErrUnsupportedURL, p.path, key)
	}
	params := url.Values{key: {value}}
	params.Set("limit", strconv.Itoa(archiveListingLimit))
	// The archive orders by created_utc only — there is no hot/top/rising
	// ranking to reproduce, so ask for newest and say so below.
	params.Set("sort", "desc")
	if w := archivePostWindow(p, key); w != "" {
		params.Set("after", w)
	}

	body, err := c.archiveGet(ctx, "/api/posts/search", params)
	if err != nil {
		return nil, err
	}
	var posts struct {
		Data []post `json:"data"`
	}
	if err := json.Unmarshal(body, &posts); err != nil {
		return nil, fmt.Errorf("arctic shift sent unreadable post data (its API may have changed): %w", err)
	}
	if len(posts.Data) == 0 {
		return nil, fmt.Errorf("%w: no %s %s in this window", ErrNotArchived, key, value)
	}

	text, ok := renderListing(posts.Data, listingNote(p))
	if !ok {
		return nil, fmt.Errorf("%w: %s %s has no readable posts", ErrNotArchived, key, value)
	}
	title := "Reddit listing"
	if key == "author" {
		title = "Reddit user " + value
	}
	return &Result{Title: title, Text: text, Source: "arctic shift archive"}, nil
}

// archivePostWindow picks the date window for a listing-shaped query.
//
// An explicit ?t= always wins (and "all" means no bound at all). Without one,
// the default is bounded, for two reasons: an unbounded search by author or
// subreddit is an expensive query this archive does time out on — its own docs
// warn that very active users and subreddits time out, and /u/spez without a
// bound did exactly that in live testing — and the unbounded answer is the
// wrong answer anyway. Someone reading /u/x wants the last year, not 2006, and
// Reddit's own default window for top/best listings is a single day.
func archivePostWindow(p redditPath, key string) string {
	if p.query.Get("t") != "" {
		return archiveWindow(p.query.Get("t"))
	}
	switch {
	case key == "author":
		// Still the costliest shape: the archive's docs warn that searches by
		// very active users time out, and bounding them doesn't fully fix that
		// (a 90-day window can time out where a year doesn't, depending on
		// load). It's a rare read, and the failure is a clear, non-retryable
		// message.
		return "1year"
	case key == "query":
		// Keyword search is cheap by the archive's own account, and an old
		// thread is exactly what someone searching for a topic wants.
		return ""
	case p.sort == "top" || p.sort == "best" || p.sort == "controversial":
		return "1day"
	}
	return "1year"
}

// archiveWindow maps Reddit's ?t= listing window onto the archive's relative
// date filter. Returns "" for "all" and anything unknown, which the archive
// reads as no lower bound.
//
// Only the spellings listed here are accepted, and it's picky: "7d" works
// where "1week" is a 400 ("'after' must be a valid date"). Each form below was
// checked against the live API rather than guessed.
func archiveWindow(t string) string {
	switch t {
	case "hour":
		return "1hour"
	case "day":
		return "1day"
	case "week":
		return "7d"
	case "month":
		return "1month"
	case "year":
		return "365d"
	}
	return ""
}

// listingNote pre-empts a wrong assumption: a model that asked for "top of the
// week" gets newest-first results, and nothing else in the output says so.
func listingNote(p redditPath) string {
	switch p.sort {
	case "", "new":
		return ""
	}
	return "newest first — the archive can only order by posting time, not by " + p.sort
}

// archiveGet performs one archive request, spaced from the previous one, and
// turns each documented failure into a message that says what it means.
//
// Overload gets one bounded retry. The archive's reset window is usually about
// a minute (it's shared, not per-client — a listing query from one machine
// closes the window for the next), so waiting it out would stall a turn; but
// when it's only a second or two, waiting costs less than failing the read.
func (c *Client) archiveGet(ctx context.Context, path string, params url.Values) ([]byte, error) {
	body, retryAfter, err := c.archiveOnce(ctx, path, params)
	if err == nil {
		return body, nil
	}
	if retryAfter <= 0 || retryAfter > archiveMaxRetryWait {
		return nil, err
	}
	select {
	case <-time.After(retryAfter + 250*time.Millisecond):
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	if body, _, err = c.archiveOnce(ctx, path, params); err == nil {
		return body, nil
	}
	return nil, err
}

// archiveOnce is a single request. It returns the server's own retry hint
// (zero when the failure isn't an overload) so archiveGet can decide whether
// waiting is worth it.
func (c *Client) archiveOnce(ctx context.Context, path string, params url.Values) ([]byte, time.Duration, error) {
	if !c.archiveWait(ctx) {
		return nil, 0, ctx.Err()
	}

	endpoint := c.archiveBase + path
	if len(params) > 0 {
		endpoint += "?" + params.Encode()
	}
	req, err := http.NewRequestWithContext(ctx, "GET", endpoint, nil)
	if err != nil {
		return nil, 0, err
	}
	req.Header.Set("User-Agent", c.userAgent)
	req.Header.Set("Accept", "application/json")

	body, status, header, err := c.do(req)
	if err != nil {
		// Transport-level failure: DNS, TLS, timeout, refused. That's the
		// archive being down, not the page being absent.
		return nil, 0, fmt.Errorf("arctic shift archive unreachable: %w", err)
	}
	if status == http.StatusOK {
		return body, 0, nil
	}

	// The API explains itself in {"error": "..."}, and those explanations are
	// the whole point of a readable failure here: "Timeout. Maybe slow down a
	// bit" and "'after' must be a valid date" mean opposite things, and a bare
	// status code would hide which one happened.
	var apiErr struct {
		Error string `json:"error"`
	}
	detail := ""
	if json.Unmarshal(body, &apiErr) == nil && strings.TrimSpace(apiErr.Error) != "" {
		detail = " — " + strings.TrimSpace(apiErr.Error)
	}
	retryAfter := parseReset(header.Get("X-RateLimit-Reset"))

	// 429, and also 422 with a timeout message, are what this archive sends
	// when it is loaded rather than when the query is wrong (verified live:
	// 422 carries "Timeout. Maybe slow down a bit" plus the same
	// X-RateLimit-Reset headers 429 does).
	if status == http.StatusTooManyRequests || status == http.StatusUnprocessableEntity {
		if retryAfter > 0 {
			return nil, retryAfter, fmt.Errorf("arctic shift is overloaded (retry in %ds)%s", int(retryAfter.Seconds()), detail)
		}
		return nil, 0, fmt.Errorf("arctic shift is overloaded%s", detail)
	}
	if status == http.StatusNotFound {
		return nil, 0, fmt.Errorf("arctic shift has no %s (its API may have changed)", path)
	}
	return nil, 0, fmt.Errorf("arctic shift returned status %d%s", status, detail)
}

// parseReset reads the X-RateLimit-Reset header, which is whole seconds until
// the window reopens.
func parseReset(v string) time.Duration {
	secs, err := strconv.Atoi(strings.TrimSpace(v))
	if err != nil || secs <= 0 {
		return 0
	}
	return time.Duration(secs) * time.Second
}

// archiveWait blocks until this client's next request slot, reserving the slot
// after it. Returns false if the context ended first. The interval is zero in
// tests (NewClientForTest), so this never matters there.
func (c *Client) archiveWait(ctx context.Context) bool {
	c.archiveMu.Lock()
	start := time.Now()
	if c.archiveNext.After(start) {
		start = c.archiveNext
	}
	c.archiveNext = start.Add(c.archiveInterval)
	c.archiveMu.Unlock()

	if wait := time.Until(start); wait > 0 {
		select {
		case <-time.After(wait):
		case <-ctx.Done():
			return false
		}
	}
	return true
}
