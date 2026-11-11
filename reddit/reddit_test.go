package reddit

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// --- fixtures: Reddit's object shapes, which the archive reproduces ---

const archivePostJSON = `{"title":"Go 1.30 released","subreddit_name_prefixed":"r/golang","subreddit":"golang",
 "author":"gopher","score":321,"num_comments":2,"created_utc":1760000000,"selftext":"Body of the post",
 "is_self":true,"permalink":"/r/golang/comments/abc/go_130/"}`

const archiveTreeJSON = `[
 {"kind":"t1","data":{"author":"alice","score":50,"body":"Top comment\nsecond line",
   "replies":{"kind":"Listing","data":{"children":[
     {"kind":"t1","data":{"author":"bob","score":7,"body":"nested reply","replies":""}}]}}}},
 {"kind":"more","data":{"count":12}}
]`

const archiveListingJSON = `{"data":[
 {"title":"First post","subreddit_name_prefixed":"r/golang","author":"a","score":10,"num_comments":3,
  "created_utc":1760000000,"selftext":"hello world","permalink":"/r/golang/comments/1/first/"},
 {"title":"Second post","subreddit_name_prefixed":"r/golang","author":"b","score":5,"num_comments":0,
  "created_utc":1760000000,"selftext":"","permalink":"/r/golang/comments/2/second/"}
]}`

// fakeReddit serves the archive endpoints off one host and records what was
// asked for. Per-endpoint status/body overrides let each test script failures.
type fakeReddit struct {
	postsStatus  int
	postsBody    string
	treeStatus   int
	treeBody     string
	searchStatus int
	searchBody   string

	lastUA    string
	lastPath  string
	lastQuery url.Values
	queries   map[string]url.Values // per archive path, for asserting each request

	// arrivals records when each archive request landed, for the spacing test.
	arrivals []time.Time
}

func (f *fakeReddit) server(t *testing.T) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()

	mux.HandleFunc("/api/posts/ids", func(w http.ResponseWriter, r *http.Request) {
		f.record(r)
		if f.postsStatus != 0 {
			w.WriteHeader(f.postsStatus)
			return
		}
		body := f.postsBody
		if body == "" {
			body = `{"data":[` + archivePostJSON + `]}`
		}
		w.Write([]byte(body))
	})

	mux.HandleFunc("/api/comments/tree", func(w http.ResponseWriter, r *http.Request) {
		f.record(r)
		if f.treeStatus != 0 {
			w.WriteHeader(f.treeStatus)
			return
		}
		body := f.treeBody
		if body == "" {
			body = `{"data":` + archiveTreeJSON + `}`
		}
		w.Write([]byte(body))
	})

	mux.HandleFunc("/api/posts/search", func(w http.ResponseWriter, r *http.Request) {
		f.record(r)
		if f.searchStatus != 0 {
			w.WriteHeader(f.searchStatus)
			return
		}
		body := f.searchBody
		if body == "" {
			body = archiveListingJSON
		}
		w.Write([]byte(body))
	})

	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func (f *fakeReddit) record(r *http.Request) {
	f.lastPath = r.URL.Path
	f.lastQuery = r.URL.Query()
	if f.queries == nil {
		f.queries = map[string]url.Values{}
	}
	f.queries[r.URL.Path] = r.URL.Query()
	f.lastUA = r.Header.Get("User-Agent")
	f.arrivals = append(f.arrivals, time.Now())
}

// --- archive tier (the default path) ---

func TestFetch_ThreadViaArchive(t *testing.T) {
	f := &fakeReddit{}
	srv := f.server(t)
	c := NewClientForTest(srv.URL)

	res, err := c.Fetch(t.Context(), "https://old.reddit.com/r/golang/comments/abc/go_130/")
	if err != nil {
		t.Fatal(err)
	}
	if res.Source != "arctic shift archive" || res.Title != "Go 1.30 released" {
		t.Errorf("got source=%q title=%q", res.Source, res.Title)
	}
	for _, want := range []string{
		"r/golang · u/gopher · 321 points",
		"Body of the post",
		"[u/alice · 50] Top comment",
		"  [u/bob · 7] nested reply",
	} {
		if !strings.Contains(res.Text, want) {
			t.Errorf("text missing %q:\n%s", want, res.Text)
		}
	}
	if strings.Contains(res.Text, "more") {
		t.Errorf("'more' stub leaked into output:\n%s", res.Text)
	}
	if len(f.arrivals) != 2 {
		t.Errorf("archive requests = %d, want exactly 2 (post + comments)", len(f.arrivals))
	}
	if got := f.queries["/api/posts/ids"].Get("ids"); got != "abc" {
		t.Errorf("posts/ids ids = %q, want abc", got)
	}
	if got := f.queries["/api/comments/tree"].Get("link_id"); got != "abc" {
		t.Errorf("comments/tree link_id = %q, want abc", got)
	}
	if !strings.HasPrefix(f.lastUA, "linux:polaris") {
		t.Errorf("User-Agent = %q, want the descriptive platform:app:version shape", f.lastUA)
	}
}

func TestFetch_ThreadCountsCommentsNotShown(t *testing.T) {
	// num_comments is Reddit's total; the archive may hand back fewer (fetch
	// cap or newer replies not indexed yet). Say so instead of implying the
	// thread stopped there.
	f := &fakeReddit{
		postsBody: `{"data":[{"title":"Busy thread","subreddit_name_prefixed":"r/golang","author":"op",
			"score":10,"num_comments":9,"created_utc":1700000000,"permalink":"/r/golang/comments/abc/x/"}]}`,
	}
	srv := f.server(t)
	c := NewClientForTest(srv.URL)

	res, err := c.Fetch(t.Context(), "https://www.reddit.com/r/golang/comments/abc/x/")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(res.Text, "(7 more comments not shown)") {
		t.Errorf("text = %q, want the unshown-comment count", res.Text)
	}
}

func TestFetch_ThreadYoungerThanArchiveSettlingGetsANote(t *testing.T) {
	f := &fakeReddit{
		postsBody: `{"data":[{"title":"Fresh post","subreddit_name_prefixed":"r/golang","author":"op",
			"score":1,"num_comments":0,"created_utc":` + itoa(time.Now().Unix()) + `,"permalink":"/r/golang/comments/abc/x/"}]}`,
	}
	srv := f.server(t)
	c := NewClientForTest(srv.URL)

	res, err := c.Fetch(t.Context(), "https://www.reddit.com/r/golang/comments/abc/x/")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(res.Text, "stored when the post was new") {
		t.Errorf("text = %q, want the settling-window note", res.Text)
	}
}

func TestFetch_ThreadWithCountedButUnarchivedCommentsSaysSo(t *testing.T) {
	// Reddit says a thread has replies the archive hasn't handed over. "(no
	// comments)" would be wrong, and the model would conclude nobody replied.
	f := &fakeReddit{
		postsBody: `{"data":[{"title":"Counted","subreddit_name_prefixed":"r/golang","author":"op",
			"score":40,"num_comments":6,"created_utc":1700000000,"permalink":"/r/golang/comments/abc/x/"}]}`,
		treeBody: `{"data":[]}`,
	}
	srv := f.server(t)
	c := NewClientForTest(srv.URL)

	res, err := c.Fetch(t.Context(), "https://www.reddit.com/r/golang/comments/abc/x/")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(res.Text, "though Reddit counts 6") {
		t.Errorf("text = %q, want the count-versus-archive discrepancy stated", res.Text)
	}
	if strings.Contains(res.Text, "(no comments)") {
		t.Errorf("text = %q, must not claim there are no comments", res.Text)
	}
}

func TestFetch_SettledThreadGetsNoCaveat(t *testing.T) {
	// Firing the freshness note on every young post would have the model
	// repeat "numbers may be stale" about a thread whose numbers are fine.
	f := &fakeReddit{
		postsBody: `{"data":[{"title":"Settled","subreddit_name_prefixed":"r/golang","author":"op",
			"score":1441,"num_comments":27,"created_utc":` + itoa(time.Now().Add(-2*time.Hour).Unix()) + `,
			"permalink":"/r/golang/comments/abc/x/"}]}`,
	}
	srv := f.server(t)
	c := NewClientForTest(srv.URL)

	res, err := c.Fetch(t.Context(), "https://www.reddit.com/r/golang/comments/abc/x/")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(res.Text, "Note:") {
		t.Errorf("text = %q, want no freshness caveat for a thread whose counts look settled", res.Text)
	}
}

func TestFetch_CommentTreeFailureKeepsThePost(t *testing.T) {
	// A post without comments still answers "what does this thread say?" far
	// better than "couldn't read this page" does.
	f := &fakeReddit{treeStatus: http.StatusInternalServerError}
	srv := f.server(t)
	c := NewClientForTest(srv.URL)

	res, err := c.Fetch(t.Context(), "https://www.reddit.com/r/golang/comments/abc/x/")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(res.Text, "Body of the post") || !strings.Contains(res.Text, "comments unavailable") {
		t.Errorf("text = %q, want the post plus a comments-unavailable note", res.Text)
	}
}

func TestFetch_ListingViaArchiveNewestFirst(t *testing.T) {
	f := &fakeReddit{}
	srv := f.server(t)
	c := NewClientForTest(srv.URL)

	res, err := c.Fetch(t.Context(), "https://www.reddit.com/r/golang/top/?t=week")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"1. First post", "https://www.reddit.com/r/golang/comments/1/first/", "hello world", "2. Second post"} {
		if !strings.Contains(res.Text, want) {
			t.Errorf("listing missing %q:\n%s", want, res.Text)
		}
	}
	// Asking for "top" can't be honoured; the output has to say what it did.
	if !strings.Contains(res.Text, "newest first") || !strings.Contains(res.Text, "not by top") {
		t.Errorf("listing = %q, want a note that the order isn't 'top'", res.Text)
	}
	if got := f.lastQuery; got.Get("subreddit") != "golang" || got.Get("sort") != "desc" || got.Get("after") != "7d" {
		t.Errorf("search query = %v, want subreddit=golang sort=desc after=7d", got)
	}
	if got := f.lastQuery.Get("limit"); got != "25" {
		t.Errorf("limit = %q, want 25", got)
	}
}

func TestFetch_SubredditSearchAndUserPagesRouteToSearch(t *testing.T) {
	for _, tc := range []struct {
		url   string
		key   string
		value string
	}{
		{"https://www.reddit.com/r/golang/search/?q=channels&restrict_sr=on", "query", "channels"},
		{"https://www.reddit.com/u/spez", "author", "spez"},
		{"https://www.reddit.com/user/spez/submitted/", "author", "spez"},
	} {
		f := &fakeReddit{}
		srv := f.server(t)
		c := NewClientForTest(srv.URL)
		if _, err := c.Fetch(t.Context(), tc.url); err != nil {
			t.Fatalf("%s: %v", tc.url, err)
		}
		if got := f.lastQuery.Get(tc.key); got != tc.value {
			t.Errorf("%s: %s = %q, want %q (query %v)", tc.url, tc.key, got, tc.value, f.lastQuery)
		}
	}
}

func TestFetch_BoundedWindowsForListingShapes(t *testing.T) {
	// Unbounded, these are the queries the archive times out on; bounded, they
	// are also the ones a reader actually meant.
	for _, tc := range []struct {
		url  string
		want string
	}{
		{"https://www.reddit.com/r/golang/", "1year"},
		{"https://www.reddit.com/r/golang/hot/", "1year"},
		{"https://www.reddit.com/r/golang/top/", "1day"},
		{"https://www.reddit.com/r/golang/top/?t=all", ""},
		{"https://www.reddit.com/r/golang/top/?t=month", "1month"},
		{"https://www.reddit.com/u/spez", "1year"},
		{"https://www.reddit.com/u/spez?t=week", "7d"},
		{"https://www.reddit.com/r/golang/search/?q=channels", ""},
	} {
		f := &fakeReddit{}
		srv := f.server(t)
		c := NewClientForTest(srv.URL)
		if _, err := c.Fetch(t.Context(), tc.url); err != nil {
			t.Fatalf("%s: %v", tc.url, err)
		}
		if got := f.queries["/api/posts/search"].Get("after"); got != tc.want {
			t.Errorf("%s: after = %q, want %q", tc.url, got, tc.want)
		}
	}
}

func TestFetch_SiteFrontPageIsUnsupportedByTheArchive(t *testing.T) {
	f := &fakeReddit{}
	srv := f.server(t)
	c := NewClientForTest(srv.URL)

	_, err := c.Fetch(t.Context(), "https://www.reddit.com/r/popular")
	if !errors.Is(err, ErrUnsupportedURL) {
		t.Fatalf("err = %v, want ErrUnsupportedURL", err)
	}
	if !strings.Contains(err.Error(), "front page") {
		t.Errorf("err = %v, want it to say what the archive can't do", err)
	}
}

// --- archive failure modes: each one has to say what went wrong ---

func TestFetch_NotArchivedIsItsOwnError(t *testing.T) {
	f := &fakeReddit{postsBody: `{"data":[]}`}
	srv := f.server(t)
	c := NewClientForTest(srv.URL)

	_, err := c.Fetch(t.Context(), "https://www.reddit.com/r/golang/comments/abc/x/")
	if !errors.Is(err, ErrNotArchived) {
		t.Fatalf("err = %v, want ErrNotArchived", err)
	}
	if !strings.Contains(err.Error(), "brand-new posts can take a few minutes") {
		t.Errorf("err = %v, want a hint at why a post might be missing", err)
	}
	if strings.Contains(err.Error(), "unreachable") || strings.Contains(err.Error(), "returned status") {
		t.Errorf("err = %v, want a missing page, not an outage", err)
	}
}

func TestFetch_RateLimitReportsTheResetWindow(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-RateLimit-Reset", "42")
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer srv.Close()

	c := NewClientForTest(srv.URL)
	_, err := c.Fetch(t.Context(), "https://www.reddit.com/r/golang/comments/abc/x/")
	if err == nil || !strings.Contains(err.Error(), "overloaded") || !strings.Contains(err.Error(), "42s") {
		t.Errorf("err = %v, want the archive's reset window", err)
	}
}

func TestFetch_ArchiveOverload422ReportsItsOwnReason(t *testing.T) {
	// Verified live: when the archive is loaded it answers 422 with
	// "Timeout. Maybe slow down a bit" — not a bad query, and not a 429.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-RateLimit-Reset", "9")
		w.WriteHeader(http.StatusUnprocessableEntity)
		w.Write([]byte(`{"data":null,"error":"Timeout. Maybe slow down a bit"}`))
	}))
	defer srv.Close()

	c := NewClientForTest(srv.URL)
	_, err := c.Fetch(t.Context(), "https://www.reddit.com/r/golang/comments/abc/x/")
	if err == nil || !strings.Contains(err.Error(), "Timeout. Maybe slow down a bit") || !strings.Contains(err.Error(), "9s") {
		t.Errorf("err = %v, want the archive's own reason and reset window", err)
	}
}

func TestFetch_ArchiveBadQueryShowsTheAPIsOwnError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		w.Write([]byte(`{"data":null,"error":"'after' must be a valid date (epoch, ISO, or relative)"}`))
	}))
	defer srv.Close()

	c := NewClientForTest(srv.URL)
	_, err := c.Fetch(t.Context(), "https://www.reddit.com/r/golang/top/?t=week")
	if err == nil || !strings.Contains(err.Error(), "status 400") || !strings.Contains(err.Error(), "'after' must be a valid date") {
		t.Errorf("err = %v, want the API's validation message, so a broken mapping is obvious", err)
	}
}

func TestFetch_OverloadWithShortWindowIsWaitedOut(t *testing.T) {
	// A one- or two-second reset is the archive being momentarily busy. One
	// retry turns that into a successful read instead of a dead turn.
	var hits atomic.Int32
	mux := http.NewServeMux()
	mux.HandleFunc("/api/posts/ids", func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.Header().Set("X-RateLimit-Reset", "1")
		w.WriteHeader(http.StatusUnprocessableEntity)
		w.Write([]byte(`{"data":null,"error":"Timeout. Maybe slow down a bit"}`))
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	c := NewClientForTest(srv.URL)
	_, err := c.Fetch(t.Context(), "https://www.reddit.com/r/golang/comments/abc/x/")
	// The retry is real, so this one test pays the (short) wait; asserting the
	// retry happened at all is what matters.
	if !strings.Contains(err.Error(), "overloaded") || hits.Load() != 2 {
		t.Errorf("err = %v after %d requests, want two attempts then the overload error", err, hits.Load())
	}
}

func TestFetch_ArchiveOutageIsReportedAsUnreachable(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	srv.Close() // nothing listening now

	c := NewClientForTest(srv.URL)
	_, err := c.Fetch(t.Context(), "https://www.reddit.com/r/golang/comments/abc/x/")
	if err == nil || !strings.Contains(err.Error(), "arctic shift archive unreachable") {
		t.Errorf("err = %v, want an unreachable-archive error", err)
	}
}

func TestFetch_ArchiveServerErrorIsReportedWithStatus(t *testing.T) {
	f := &fakeReddit{postsStatus: http.StatusBadGateway}
	srv := f.server(t)
	c := NewClientForTest(srv.URL)

	_, err := c.Fetch(t.Context(), "https://www.reddit.com/r/golang/comments/abc/x/")
	if err == nil || !strings.Contains(err.Error(), "status 502") {
		t.Errorf("err = %v, want the archive's status code", err)
	}
}

func TestFetch_ArchiveSpacesRequests(t *testing.T) {
	f := &fakeReddit{}
	srv := f.server(t)
	c := NewClient("", srv.URL) // real spacing, not the test default
	c.http = &http.Client{Timeout: 5 * time.Second}
	c.archiveInterval = 40 * time.Millisecond

	if _, err := c.Fetch(t.Context(), "https://www.reddit.com/r/golang/comments/abc/x/"); err != nil {
		t.Fatal(err)
	}
	if len(f.arrivals) != 2 {
		t.Fatalf("archive requests = %d, want 2 (post + comments)", len(f.arrivals))
	}
	if gap := f.arrivals[1].Sub(f.arrivals[0]); gap < 30*time.Millisecond {
		t.Errorf("requests %v apart, want them spaced out", gap)
	}
}

func TestFetch_CancelledContextStopsTheChain(t *testing.T) {
	// An interrupted turn isn't "the archive failed": don't try the next
	// source and don't spend another request on it.
	f := &fakeReddit{}
	srv := f.server(t)
	c := NewClient("", srv.URL)
	c.http = &http.Client{Timeout: 5 * time.Second}

	canceled, stop := context.WithCancel(t.Context())
	stop()
	_, err := c.Fetch(canceled, "https://www.reddit.com/r/golang/comments/abc/x/")
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
	if len(f.arrivals) != 0 {
		t.Errorf("sources were still called after cancellation (%d archive requests)", len(f.arrivals))
	}
}

// --- URL parsing ---

func TestParseURL(t *testing.T) {
	cases := []struct {
		in                string
		wantPath          string
		thread            bool
		postID, sub, user string
		sort              string
		search            bool
		wantErr           bool
	}{
		{in: "https://www.reddit.com/r/golang/comments/abc/slug/", wantPath: "/r/golang/comments/abc/slug", postID: "abc", sub: "golang"},
		{in: "https://old.reddit.com/r/golang/comments/abc/slug/.json", wantPath: "/r/golang/comments/abc/slug", postID: "abc", sub: "golang"},
		{in: "https://reddit.com/r/golang/top/?t=week", wantPath: "/r/golang/top", sub: "golang", sort: "top"},
		{in: "https://www.reddit.com/r/golang/new/", wantPath: "/r/golang/new", sub: "golang", sort: "new"},
		{in: "https://www.reddit.com/r/golang/search/?q=x", wantPath: "/r/golang/search", sub: "golang", search: true},
		{in: "https://www.reddit.com/u/spez", wantPath: "/u/spez", user: "spez"},
		{in: "https://redd.it/abc123", wantPath: "/comments/abc123", postID: "abc123"},
		{in: "https://www.reddit.com/gallery/xyz", wantPath: "/comments/xyz", postID: "xyz"},
		{in: "https://www.reddit.com/", wantPath: "/r/popular"},
		{in: "https://www.reddit.com/r/popular", wantPath: "/r/popular"},
		{in: "https://www.reddit.com/r/golang/s/AbCdEf", wantErr: true},
		{in: "https://example.com/r/golang", wantErr: true},
	}
	for _, tc := range cases {
		got, err := parseURL(tc.in)
		if tc.wantErr {
			if !errors.Is(err, ErrUnsupportedURL) {
				t.Errorf("%s: err = %v, want ErrUnsupportedURL", tc.in, err)
			}
			continue
		}
		if err != nil {
			t.Errorf("%s: unexpected error %v", tc.in, err)
			continue
		}
		if got.path != tc.wantPath || got.postID != tc.postID || got.subreddit != tc.sub ||
			got.user != tc.user || got.sort != tc.sort || got.search != tc.search {
			t.Errorf("%s: got %+v, want path=%q id=%q sub=%q user=%q sort=%q search=%v",
				tc.in, got, tc.wantPath, tc.postID, tc.sub, tc.user, tc.sort, tc.search)
		}
	}
}

func TestIsRedditURL(t *testing.T) {
	for in, want := range map[string]bool{
		"https://www.reddit.com/r/x":     true,
		"https://old.reddit.com/r/x":     true,
		"https://redd.it/abc":            true,
		"https://notreddit.com/r/x":      false,
		"https://reddit.com.evil.io/r/x": false,
		"https://example.com":            false,
	} {
		if got := IsRedditURL(in); got != want {
			t.Errorf("IsRedditURL(%q) = %v, want %v", in, got, want)
		}
	}
}

func itoa(n int64) string {
	return strconv.FormatInt(n, 10)
}
