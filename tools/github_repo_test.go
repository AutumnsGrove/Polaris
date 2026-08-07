package tools

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestParseGitHubRepo(t *testing.T) {
	cases := map[string][2]string{
		"facebook/react":                                        {"facebook", "react"},
		"facebook/react/":                                       {"facebook", "react"},
		"https://github.com/facebook/react":                     {"facebook", "react"},
		"https://github.com/facebook/react/":                    {"facebook", "react"},
		"http://github.com/facebook/react":                      {"facebook", "react"},
		"github.com/facebook/react":                             {"facebook", "react"},
		"https://github.com/facebook/react.git":                 {"facebook", "react"},
		"git@github.com:facebook/react.git":                     {"facebook", "react"},
		"https://github.com/facebook/react/tree/main":           {"facebook", "react"},
		"https://github.com/facebook/react/blob/main/README.md": {"facebook", "react"},
	}
	for input, want := range cases {
		owner, repo, err := parseGitHubRepo(input)
		if err != nil {
			t.Errorf("parseGitHubRepo(%q) unexpected error: %v", input, err)
			continue
		}
		if owner != want[0] || repo != want[1] {
			t.Errorf("parseGitHubRepo(%q) = (%q, %q), want (%q, %q)", input, owner, repo, want[0], want[1])
		}
	}
}

func TestParseGitHubRepo_Invalid(t *testing.T) {
	for _, input := range []string{"", "not a repo", "https://example.com/facebook/react"} {
		if _, _, err := parseGitHubRepo(input); err == nil {
			t.Errorf("parseGitHubRepo(%q) = nil error, want an error", input)
		}
	}
}

func TestParseGitHubLastPage(t *testing.T) {
	cases := map[string]int{
		"": 0,
		`<https://api.github.com/repos/x/y/commits?per_page=1&page=2>; rel="next", <https://api.github.com/repos/x/y/commits?per_page=1&page=1523>; rel="last"`: 1523,
		`<https://api.github.com/repos/x/y/commits?per_page=1&page=2>; rel="next"`:                                                                              0,
	}
	for header, want := range cases {
		if got := parseGitHubLastPage(header); got != want {
			t.Errorf("parseGitHubLastPage(%q) = %d, want %d", header, got, want)
		}
	}
}

func TestFormatGitHubCount(t *testing.T) {
	cases := map[int]string{
		0:       "0",
		42:      "42",
		999:     "999",
		1000:    "1,000",
		23400:   "23,400",
		1234567: "1,234,567",
	}
	for n, want := range cases {
		if got := formatGitHubCount(n); got != want {
			t.Errorf("formatGitHubCount(%d) = %q, want %q", n, got, want)
		}
	}
}

func TestHandleGitHubRepo_RepoRequired(t *testing.T) {
	ctx := newTestContext()
	result := handleGitHubRepo(`{}`, ctx)
	if result == "" || result[:6] != "error:" {
		t.Errorf("result = %q, want a repo-required error", result)
	}
}

func TestHandleGitHubRepo_InvalidRepo(t *testing.T) {
	ctx := newTestContext()
	result := handleGitHubRepo(`{"repo":"not a repo"}`, ctx)
	if result == "" || result[:6] != "error:" {
		t.Errorf("result = %q, want a parse error", result)
	}
}

func TestHandleGitHubRepo_NotFound(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		w.Write([]byte(`{"message":"Not Found"}`))
	}))
	t.Cleanup(srv.Close)
	original := githubAPIBaseURL
	githubAPIBaseURL = srv.URL
	t.Cleanup(func() { githubAPIBaseURL = original })

	ctx := newTestContext()
	result := handleGitHubRepo(`{"repo":"ghost/ghost"}`, ctx)
	if result == "" || result[:6] != "error:" {
		t.Errorf("result = %q, want a not-found error", result)
	}
}

// githubMux builds a fake GitHub API server covering the four endpoints
// github_repo calls: repo info, two commits pages (Link-header pagination
// trick), open PRs, and the README.
func githubMux(t *testing.T) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()

	mux.HandleFunc("/repos/octocat/hello-world", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{
			"full_name": "octocat/hello-world",
			"html_url": "https://github.com/octocat/hello-world",
			"description": "My first repository on GitHub!",
			"language": "Go",
			"stargazers_count": 23400,
			"forks_count": 900,
			"open_issues_count": 15,
			"created_at": "2013-05-24T16:14:19Z",
			"archived": false,
			"license": {"name": "MIT License"}
		}`))
	})

	mux.HandleFunc("/repos/octocat/hello-world/commits", func(w http.ResponseWriter, r *http.Request) {
		page := r.URL.Query().Get("page")
		w.Header().Set("Content-Type", "application/json")
		if page == "42" {
			w.Write([]byte(`[{"commit":{"committer":{"date":"2013-05-25T10:00:00Z"}}}]`))
			return
		}
		w.Header().Set("Link", `<`+r.URL.String()+`&page=2>; rel="next", <`+r.URL.String()+`&page=42>; rel="last"`)
		w.Write([]byte(`[{"commit":{"committer":{"date":"2026-08-01T12:00:00Z"}}}]`))
	})

	mux.HandleFunc("/repos/octocat/hello-world/pulls", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Link", `<`+r.URL.String()+`&page=7>; rel="last"`)
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`[{}]`))
	})

	mux.HandleFunc("/repos/octocat/hello-world/readme", func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Accept"); got != "application/vnd.github.raw+json" {
			t.Errorf("readme request Accept = %q, want application/vnd.github.raw+json", got)
		}
		w.Write([]byte("# Hello World\n\nThis is the readme."))
	})

	return httptest.NewServer(mux)
}

func TestHandleGitHubRepo_FullStatsAndReadme(t *testing.T) {
	srv := githubMux(t)
	t.Cleanup(srv.Close)
	original := githubAPIBaseURL
	githubAPIBaseURL = srv.URL
	t.Cleanup(func() { githubAPIBaseURL = original })

	ctx := newTestContext()
	result := handleGitHubRepo(`{"repo":"octocat/hello-world"}`, ctx)
	if result == "" || result[:6] == "error:" {
		t.Fatalf("result = %q, want a formatted stats block", result)
	}

	for _, want := range []string{
		"octocat/hello-world",
		"23,400 stars",
		"900 forks",
		"MIT License license",
		"Go",
		"42 commits on the default branch",
		"First commit: 2013-05-25",
		"Most recent commit: 2026-08-01",
		"7 open pull requests",
		"8 open issues", // open_issues_count (15) minus open PRs (7)
		"--- README ---",
		"This is the readme.",
	} {
		if !strings.Contains(result, want) {
			t.Errorf("result missing %q\nfull result:\n%s", want, result)
		}
	}

	if len(ctx.Citations) != 1 || ctx.Citations[0].URL != "https://github.com/octocat/hello-world" {
		t.Errorf("Citations = %+v, want the repo added", ctx.Citations)
	}
}

func TestHandleGitHubRepo_SkipsReadmeWhenIncludeReadmeFalse(t *testing.T) {
	readmeCalled := false
	srv := githubMux(t)
	// Wrap the mux to detect whether /readme was ever hit.
	origHandler := srv.Config.Handler
	srv.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/readme") {
			readmeCalled = true
		}
		origHandler.ServeHTTP(w, r)
	})
	t.Cleanup(srv.Close)
	original := githubAPIBaseURL
	githubAPIBaseURL = srv.URL
	t.Cleanup(func() { githubAPIBaseURL = original })

	ctx := newTestContext()
	result := handleGitHubRepo(`{"repo":"octocat/hello-world","include_readme":false}`, ctx)
	if result == "" || result[:6] == "error:" {
		t.Fatalf("result = %q, want a formatted stats block", result)
	}
	if strings.Contains(result, "README") {
		t.Errorf("result = %q, want no README section when include_readme is false", result)
	}
	if readmeCalled {
		t.Error("readme endpoint was called despite include_readme:false")
	}
}

func TestFetchGitHubRepoStats_UsesBearerTokenWhenSet(t *testing.T) {
	var gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"full_name":"octocat/hello-world","html_url":"https://github.com/octocat/hello-world","created_at":"2013-05-24T16:14:19Z"}`))
	}))
	t.Cleanup(srv.Close)
	original := githubAPIBaseURL
	githubAPIBaseURL = srv.URL
	t.Cleanup(func() { githubAPIBaseURL = original })

	_, err := fetchGitHubRepoStats(context.Background(), "octocat", "hello-world", "test-token-123")
	if err != nil {
		t.Fatalf("fetchGitHubRepoStats returned error: %v", err)
	}
	if gotAuth != "Bearer test-token-123" {
		t.Errorf("Authorization header = %q, want %q", gotAuth, "Bearer test-token-123")
	}
}

func TestFormatGitHubAge(t *testing.T) {
	now := time.Now()
	if got := formatGitHubAge(now.AddDate(-2, -3, 0)); !strings.Contains(got, "2 years") {
		t.Errorf("formatGitHubAge(2y3mo ago) = %q, want it to mention years", got)
	}
	if got := formatGitHubAge(now.AddDate(0, -5, 0)); !strings.Contains(got, "months") {
		t.Errorf("formatGitHubAge(5mo ago) = %q, want it to mention months", got)
	}
	if got := formatGitHubAge(now.AddDate(0, 0, -3)); !strings.Contains(got, "days") {
		t.Errorf("formatGitHubAge(3d ago) = %q, want it to mention days", got)
	}
}
