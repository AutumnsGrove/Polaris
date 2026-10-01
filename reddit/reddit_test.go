package reddit

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

const threadJSON = `[
 {"kind":"Listing","data":{"children":[{"kind":"t3","data":{
   "title":"Go 1.30 released","subreddit_name_prefixed":"r/golang","author":"gopher",
   "score":321,"num_comments":2,"created_utc":1760000000,"selftext":"Body of the post","is_self":true,"permalink":"/r/golang/comments/abc/go_130/"}}]}},
 {"kind":"Listing","data":{"children":[
   {"kind":"t1","data":{"author":"alice","score":50,"body":"Top comment\nsecond line",
     "replies":{"kind":"Listing","data":{"children":[
        {"kind":"t1","data":{"author":"bob","score":7,"body":"nested reply","replies":""}}]}}}},
   {"kind":"more","data":{"count":12}}
 ]}}
]`

const listingJSON = `{"kind":"Listing","data":{"children":[
  {"kind":"t3","data":{"title":"First post","subreddit_name_prefixed":"r/golang","author":"a","score":10,"num_comments":3,"created_utc":1760000000,"selftext":"hello world","permalink":"/r/golang/comments/1/first/"}},
  {"kind":"t3","data":{"title":"Second post","subreddit_name_prefixed":"r/golang","author":"b","score":5,"num_comments":0,"created_utc":1760000000,"selftext":"","permalink":"/r/golang/comments/2/second/"}}
]}}`

const rssXML = `<?xml version="1.0" encoding="UTF-8"?>
<feed xmlns="http://www.w3.org/2005/Atom"><title>r/golang</title>
<entry><title>Feed post</title><author><name>/u/carol</name></author><updated>2026-09-30T12:00:00+00:00</updated>
<content type="html">&lt;div class="md"&gt;&lt;p&gt;feed body text&lt;/p&gt;&lt;/div&gt; submitted by &lt;a href="/u/carol"&gt;/u/carol&lt;/a&gt;</content></entry>
</feed>`

// fakeReddit serves token, API and RSS endpoints off one server. api/rss
// are the handlers' bodies/statuses so each test can script failures.
type fakeReddit struct {
	tokenHits, apiHits, rssHits atomic.Int32
	apiStatus, rssStatus        int
	apiBody                     string
	lastAuth, lastTokenForm     string
	lastUA                      string
}

func (f *fakeReddit) server(t *testing.T) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/token", func(w http.ResponseWriter, r *http.Request) {
		f.tokenHits.Add(1)
		_ = r.ParseForm()
		f.lastTokenForm = r.PostForm.Encode()
		w.Write([]byte(`{"access_token":"tok123","expires_in":3600}`))
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		f.lastUA = r.Header.Get("User-Agent")
		if strings.HasSuffix(r.URL.Path, "/.rss") {
			f.rssHits.Add(1)
			if f.rssStatus != 0 {
				w.WriteHeader(f.rssStatus)
				return
			}
			w.Write([]byte(rssXML))
			return
		}
		f.apiHits.Add(1)
		f.lastAuth = r.Header.Get("Authorization")
		if f.apiStatus != 0 {
			w.WriteHeader(f.apiStatus)
			return
		}
		w.Write([]byte(f.apiBody))
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func TestFetch_ThreadViaAPI(t *testing.T) {
	f := &fakeReddit{apiBody: threadJSON}
	srv := f.server(t)
	c := NewClientForTest("id", "secret", srv.URL)

	res, err := c.Fetch(t.Context(), "https://old.reddit.com/r/golang/comments/abc/go_130/")
	if err != nil {
		t.Fatal(err)
	}
	if res.Source != "reddit api" || res.Title != "Go 1.30 released" {
		t.Errorf("got source=%q title=%q", res.Source, res.Title)
	}
	for _, want := range []string{"r/golang · u/gopher · 321 points", "Body of the post", "[u/alice · 50] Top comment", "  [u/bob · 7] nested reply"} {
		if !strings.Contains(res.Text, want) {
			t.Errorf("text missing %q:\n%s", want, res.Text)
		}
	}
	if strings.Contains(res.Text, "12") && strings.Contains(res.Text, "more") {
		t.Errorf("'more' stub leaked into output:\n%s", res.Text)
	}
	if f.lastAuth != "Bearer tok123" {
		t.Errorf("Authorization = %q, want Bearer tok123", f.lastAuth)
	}
	if !strings.Contains(f.lastTokenForm, "grant_type=client_credentials") {
		t.Errorf("token form = %q, want client_credentials grant when a secret is set", f.lastTokenForm)
	}
	if f.rssHits.Load() != 0 {
		t.Error("RSS must not be touched when the API succeeds")
	}
}

func TestFetch_InstalledAppGrantWhenNoSecret(t *testing.T) {
	f := &fakeReddit{apiBody: listingJSON}
	srv := f.server(t)
	c := NewClientForTest("apollo-id", "", srv.URL)

	if _, err := c.Fetch(t.Context(), "https://www.reddit.com/r/golang/top/?t=week"); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(f.lastTokenForm, "installed_client") || !strings.Contains(f.lastTokenForm, "device_id=") {
		t.Errorf("token form = %q, want installed_client grant with a device_id", f.lastTokenForm)
	}
}

func TestFetch_ListingFormatsPostsWithThreadLinks(t *testing.T) {
	f := &fakeReddit{apiBody: listingJSON}
	srv := f.server(t)
	c := NewClientForTest("id", "secret", srv.URL)

	res, err := c.Fetch(t.Context(), "https://www.reddit.com/r/golang/")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"1. First post", "https://www.reddit.com/r/golang/comments/1/first/", "hello world", "2. Second post"} {
		if !strings.Contains(res.Text, want) {
			t.Errorf("listing missing %q:\n%s", want, res.Text)
		}
	}
}

func TestFetch_TokenIsCachedAcrossCalls(t *testing.T) {
	f := &fakeReddit{apiBody: listingJSON}
	srv := f.server(t)
	c := NewClientForTest("id", "secret", srv.URL)
	for i := 0; i < 3; i++ {
		if _, err := c.Fetch(t.Context(), "https://www.reddit.com/r/golang/"); err != nil {
			t.Fatal(err)
		}
	}
	if got := f.tokenHits.Load(); got != 1 {
		t.Errorf("token requests = %d, want 1 (cached)", got)
	}
}

func TestFetch_FallsBackToRSSWhenAPIFails(t *testing.T) {
	f := &fakeReddit{apiStatus: http.StatusForbidden}
	srv := f.server(t)
	c := NewClientForTest("id", "secret", srv.URL)

	res, err := c.Fetch(t.Context(), "https://www.reddit.com/r/golang/comments/abc/go_130/")
	if err != nil {
		t.Fatal(err)
	}
	if res.Source != "reddit rss" || !strings.Contains(res.Text, "feed body text") || !strings.Contains(res.Text, "u/carol") {
		t.Errorf("got source=%q text=%q", res.Source, res.Text)
	}
	if strings.Contains(res.Text, "submitted by") {
		t.Errorf("feed boilerplate not stripped: %q", res.Text)
	}
}

func TestFetch_NoCredentialsGoesStraightToRSS(t *testing.T) {
	f := &fakeReddit{}
	srv := f.server(t)
	c := NewClientForTest("", "", srv.URL)

	res, err := c.Fetch(t.Context(), "https://www.reddit.com/r/golang/comments/abc/go_130/")
	if err != nil {
		t.Fatal(err)
	}
	if res.Source != "reddit rss" {
		t.Errorf("source = %q, want rss", res.Source)
	}
	if f.tokenHits.Load() != 0 || f.apiHits.Load() != 0 {
		t.Errorf("token/api hit with no credentials configured (%d/%d)", f.tokenHits.Load(), f.apiHits.Load())
	}
	if !strings.HasPrefix(f.lastUA, "linux:polaris") {
		t.Errorf("User-Agent = %q, want Reddit's descriptive platform:app:version shape", f.lastUA)
	}
}

func TestFetch_BothTiersFailNamesBoth(t *testing.T) {
	f := &fakeReddit{apiStatus: http.StatusForbidden, rssStatus: http.StatusTooManyRequests}
	srv := f.server(t)
	c := NewClientForTest("id", "secret", srv.URL)

	_, err := c.Fetch(t.Context(), "https://www.reddit.com/r/golang/")
	if err == nil || !strings.Contains(err.Error(), "reddit api") || !strings.Contains(err.Error(), "429") {
		t.Errorf("err = %v, want both tiers' failures named", err)
	}
}

func TestFetch_RetriesOnceAfter401WithFreshToken(t *testing.T) {
	var apiCalls atomic.Int32
	mux := http.NewServeMux()
	var tokens atomic.Int32
	mux.HandleFunc("/token", func(w http.ResponseWriter, r *http.Request) {
		n := tokens.Add(1)
		w.Write([]byte(`{"access_token":"tok` + string(rune('0'+n)) + `","expires_in":3600}`))
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if apiCalls.Add(1) == 1 {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		if r.Header.Get("Authorization") != "Bearer tok2" {
			t.Errorf("retry used %q, want the refreshed token", r.Header.Get("Authorization"))
		}
		w.Write([]byte(listingJSON))
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	c := NewClientForTest("id", "secret", srv.URL)
	if _, err := c.Fetch(t.Context(), "https://www.reddit.com/r/golang/"); err != nil {
		t.Fatal(err)
	}
	if tokens.Load() != 2 {
		t.Errorf("tokens issued = %d, want 2", tokens.Load())
	}
}

func TestFetch_BadCredentialsReportedFromTokenBody(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/token", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"error":"invalid_grant"}`)) // Reddit answers 200 + error body
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusNotFound) })
	srv := httptest.NewServer(mux)
	defer srv.Close()

	c := NewClientForTest("id", "wrong", srv.URL)
	_, err := c.Fetch(t.Context(), "https://www.reddit.com/r/golang/")
	if err == nil || !strings.Contains(err.Error(), "invalid_grant") {
		t.Errorf("err = %v, want the token endpoint's error surfaced", err)
	}
}

func TestParseURL(t *testing.T) {
	cases := []struct {
		in       string
		wantPath string
		thread   bool
		wantErr  bool
	}{
		{"https://www.reddit.com/r/golang/comments/abc/slug/", "/r/golang/comments/abc/slug", true, false},
		{"https://old.reddit.com/r/golang/comments/abc/slug/.json", "/r/golang/comments/abc/slug", true, false},
		{"https://reddit.com/r/golang/top/?t=week", "/r/golang/top", false, false},
		{"https://redd.it/abc123", "/comments/abc123", true, false},
		{"https://www.reddit.com/gallery/xyz", "/comments/xyz", true, false},
		{"https://www.reddit.com/", "/r/popular", false, false},
		{"https://www.reddit.com/r/golang/s/AbCdEf", "", false, true},
		{"https://example.com/r/golang", "", false, true},
	}
	for _, tc := range cases {
		got, err := parseURL(tc.in)
		if tc.wantErr {
			if !errors.Is(err, ErrUnsupportedURL) {
				t.Errorf("%s: err = %v, want ErrUnsupportedURL", tc.in, err)
			}
			continue
		}
		if err != nil || got.path != tc.wantPath || got.isThread != tc.thread {
			t.Errorf("%s: got %+v err=%v, want path=%q thread=%v", tc.in, got, err, tc.wantPath, tc.thread)
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
