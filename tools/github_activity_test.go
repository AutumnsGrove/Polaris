package tools

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// --- argument validation ---------------------------------------------------

func TestHandleGitHubActivity_RepoRequired(t *testing.T) {
	ctx := newTestContext()
	result := handleGitHubActivity(`{"kind":"releases"}`, ctx)
	if !strings.HasPrefix(result, "error:") || !strings.Contains(result, "repo") {
		t.Errorf("result = %q, want a repo-required error", result)
	}
}

func TestHandleGitHubActivity_KindRequired(t *testing.T) {
	ctx := newTestContext()
	result := handleGitHubActivity(`{"repo":"octocat/hello-world"}`, ctx)
	if !strings.HasPrefix(result, "error:") || !strings.Contains(result, "kind") {
		t.Errorf("result = %q, want a kind-required error", result)
	}
}

func TestHandleGitHubActivity_InvalidKind(t *testing.T) {
	ctx := newTestContext()
	result := handleGitHubActivity(`{"repo":"octocat/hello-world","kind":"stargazers"}`, ctx)
	if !strings.HasPrefix(result, "error:") || !strings.Contains(result, "kind") {
		t.Errorf("result = %q, want an invalid-kind error", result)
	}
}

func TestHandleGitHubActivity_InvalidRepo(t *testing.T) {
	ctx := newTestContext()
	result := handleGitHubActivity(`{"repo":"not a repo","kind":"releases"}`, ctx)
	if !strings.HasPrefix(result, "error:") {
		t.Errorf("result = %q, want a parse error", result)
	}
}

func TestHandleGitHubActivity_PRNumberRequiredForPRKind(t *testing.T) {
	ctx := newTestContext()
	result := handleGitHubActivity(`{"repo":"octocat/hello-world","kind":"pr"}`, ctx)
	if !strings.HasPrefix(result, "error:") || !strings.Contains(result, "pr_number") {
		t.Errorf("result = %q, want a pr_number-required error", result)
	}
}

func TestHandleGitHubActivity_SinceRequiredForCommitsKind(t *testing.T) {
	ctx := newTestContext()
	result := handleGitHubActivity(`{"repo":"octocat/hello-world","kind":"commits"}`, ctx)
	if !strings.HasPrefix(result, "error:") || !strings.Contains(result, "since") {
		t.Errorf("result = %q, want a since-required error", result)
	}
}

func TestHandleGitHubActivity_CommitsInvalidSinceFormat(t *testing.T) {
	ctx := newTestContext()
	result := handleGitHubActivity(`{"repo":"octocat/hello-world","kind":"commits","since":"not-a-date"}`, ctx)
	if !strings.HasPrefix(result, "error:") || !strings.Contains(result, "since") {
		t.Errorf("result = %q, want a since-parse error", result)
	}
}

// --- parseGitHubSince --------------------------------------------------

func TestParseGitHubSince(t *testing.T) {
	cases := []string{"2026-08-01", "2026-08-01T00:00:00Z", "2026-08-01T12:30:00-07:00"}
	for _, in := range cases {
		if _, err := parseGitHubSince(in); err != nil {
			t.Errorf("parseGitHubSince(%q) unexpected error: %v", in, err)
		}
	}
}

func TestParseGitHubSince_Invalid(t *testing.T) {
	for _, in := range []string{"", "not a date", "08/01/2026", "2026-13-40"} {
		if _, err := parseGitHubSince(in); err == nil {
			t.Errorf("parseGitHubSince(%q) = nil error, want an error", in)
		}
	}
}

// --- clampGitHubLimit --------------------------------------------------

func TestClampGitHubLimit(t *testing.T) {
	five := 5
	zero := 0
	negative := -3
	huge := 999
	cases := []struct {
		name     string
		in       *int
		def, max int
		want     int
	}{
		{"nil uses default", nil, 5, 20, 5},
		{"zero uses default", &zero, 5, 20, 5},
		{"negative uses default", &negative, 5, 20, 5},
		{"within range passes through", &five, 10, 20, 5},
		{"above max clamps to max", &huge, 5, 20, 20},
	}
	for _, c := range cases {
		if got := clampGitHubLimit(c.in, c.def, c.max); got != c.want {
			t.Errorf("%s: clampGitHubLimit(%v, %d, %d) = %d, want %d", c.name, c.in, c.def, c.max, got, c.want)
		}
	}
}

// --- kind=releases -------------------------------------------------------

func githubActivityMux(t *testing.T) *http.ServeMux {
	t.Helper()
	mux := http.NewServeMux()

	mux.HandleFunc("/repos/octocat/hello-world/releases", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`[
			{
				"tag_name": "v2.0.0",
				"name": "Version 2.0.0",
				"published_at": "2026-08-20T10:00:00Z",
				"body": "Adds widgets.",
				"html_url": "https://github.com/octocat/hello-world/releases/tag/v2.0.0",
				"draft": false,
				"prerelease": false
			},
			{
				"tag_name": "v2.0.0-rc1",
				"name": "Version 2.0.0 RC1",
				"published_at": "2026-08-10T10:00:00Z",
				"body": "Release candidate.",
				"html_url": "https://github.com/octocat/hello-world/releases/tag/v2.0.0-rc1",
				"draft": false,
				"prerelease": true
			},
			{
				"tag_name": "v1.9.0-draft",
				"name": "Draft",
				"published_at": "2026-08-05T10:00:00Z",
				"body": "wip",
				"html_url": "https://github.com/octocat/hello-world/releases/tag/v1.9.0-draft",
				"draft": true,
				"prerelease": false
			}
		]`))
	})

	mux.HandleFunc("/repos/octocat/empty/releases", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`[]`))
	})

	mux.HandleFunc("/repos/octocat/hello-world/pulls/42", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{
			"number": 42,
			"title": "Fix the widget renderer",
			"state": "closed",
			"merged": true,
			"merged_at": "2026-08-15T09:00:00Z",
			"created_at": "2026-08-10T09:00:00Z",
			"user": {"login": "octocat"},
			"body": "This fixes the widget renderer crashing on empty input.",
			"html_url": "https://github.com/octocat/hello-world/pull/42",
			"additions": 40,
			"deletions": 12,
			"changed_files": 3,
			"base": {"ref": "main"},
			"head": {"ref": "fix-widget"}
		}`))
	})

	mux.HandleFunc("/repos/octocat/hello-world/pulls/999", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		w.Write([]byte(`{"message":"Not Found"}`))
	})

	mux.HandleFunc("/repos/octocat/hello-world/issues", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if got := r.URL.Query().Get("state"); got != "" {
			w.Header().Set("X-Test-State", got)
		}
		w.Write([]byte(`[
			{
				"number": 101,
				"title": "Widgets sometimes disappear",
				"state": "open",
				"created_at": "2026-08-18T00:00:00Z",
				"updated_at": "2026-08-19T00:00:00Z",
				"user": {"login": "reporter1"},
				"comments": 3,
				"html_url": "https://github.com/octocat/hello-world/issues/101"
			},
			{
				"number": 100,
				"title": "This is actually a PR",
				"state": "open",
				"created_at": "2026-08-17T00:00:00Z",
				"updated_at": "2026-08-17T00:00:00Z",
				"user": {"login": "someone"},
				"comments": 0,
				"html_url": "https://github.com/octocat/hello-world/issues/100",
				"pull_request": {"url": "https://api.github.com/repos/octocat/hello-world/pulls/100"}
			}
		]`))
	})

	mux.HandleFunc("/repos/octocat/hello-world/commits", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`[
			{
				"sha": "abc1234567890",
				"html_url": "https://github.com/octocat/hello-world/commit/abc1234567890",
				"commit": {
					"message": "Fix widget crash\n\nLonger body here.",
					"author": {"name": "octocat"},
					"committer": {"date": "2026-08-20T12:00:00Z"}
				}
			}
		]`))
	})

	mux.HandleFunc("/repos/octocat/empty/commits", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`[]`))
	})

	return mux
}

func newGithubActivityTestServer(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(githubActivityMux(t))
	t.Cleanup(srv.Close)
	original := githubAPIBaseURL
	githubAPIBaseURL = srv.URL
	t.Cleanup(func() { githubAPIBaseURL = original })
	return srv
}

func TestHandleGitHubActivity_Releases_Success(t *testing.T) {
	newGithubActivityTestServer(t)
	ctx := newTestContext()
	result := handleGitHubActivity(`{"repo":"octocat/hello-world","kind":"releases"}`, ctx)
	if strings.HasPrefix(result, "error:") {
		t.Fatalf("result = %q, want a formatted release list", result)
	}
	for _, want := range []string{"v2.0.0", "Adds widgets.", "v2.0.0-rc1", "prerelease", "2026-08-20"} {
		if !strings.Contains(result, want) {
			t.Errorf("result missing %q\nfull result:\n%s", want, result)
		}
	}
	if strings.Contains(result, "v1.9.0-draft") {
		t.Errorf("result should not include the draft release\nfull result:\n%s", result)
	}
	if len(ctx.Citations) != 1 {
		t.Errorf("Citations = %+v, want exactly one citation", ctx.Citations)
	}
}

func TestHandleGitHubActivity_Releases_EmptyList(t *testing.T) {
	newGithubActivityTestServer(t)
	ctx := newTestContext()
	result := handleGitHubActivity(`{"repo":"octocat/empty","kind":"releases"}`, ctx)
	if strings.HasPrefix(result, "error:") {
		t.Fatalf("result = %q, want a friendly no-releases message, not an error", result)
	}
	if !strings.Contains(result, "no") {
		t.Errorf("result = %q, want it to mention there are no releases", result)
	}
}

func TestHandleGitHubActivity_Releases_LimitClampedToRequestedCount(t *testing.T) {
	newGithubActivityTestServer(t)
	ctx := newTestContext()
	result := handleGitHubActivity(`{"repo":"octocat/hello-world","kind":"releases","limit":1}`, ctx)
	if strings.HasPrefix(result, "error:") {
		t.Fatalf("result = %q, want a formatted release list", result)
	}
	if !strings.Contains(result, "v2.0.0") || strings.Contains(result, "v2.0.0-rc1") {
		t.Errorf("result = %q, want only the single most recent (non-draft) release with limit:1", result)
	}
}

// --- kind=pr ---------------------------------------------------------------

func TestHandleGitHubActivity_PR_Success(t *testing.T) {
	newGithubActivityTestServer(t)
	ctx := newTestContext()
	result := handleGitHubActivity(`{"repo":"octocat/hello-world","kind":"pr","pr_number":42}`, ctx)
	if strings.HasPrefix(result, "error:") {
		t.Fatalf("result = %q, want a formatted PR detail block", result)
	}
	for _, want := range []string{
		"#42", "Fix the widget renderer", "octocat", "merged",
		"main", "fix-widget", "+40", "-12", "3 files",
		"This fixes the widget renderer crashing on empty input.",
	} {
		if !strings.Contains(result, want) {
			t.Errorf("result missing %q\nfull result:\n%s", want, result)
		}
	}
	if len(ctx.Citations) != 1 || ctx.Citations[0].URL != "https://github.com/octocat/hello-world/pull/42" {
		t.Errorf("Citations = %+v, want the PR URL", ctx.Citations)
	}
}

func TestHandleGitHubActivity_PR_NotFound(t *testing.T) {
	newGithubActivityTestServer(t)
	ctx := newTestContext()
	result := handleGitHubActivity(`{"repo":"octocat/hello-world","kind":"pr","pr_number":999}`, ctx)
	if !strings.HasPrefix(result, "error:") {
		t.Errorf("result = %q, want a not-found error", result)
	}
}

// --- kind=issues -------------------------------------------------------

func TestHandleGitHubActivity_Issues_Success(t *testing.T) {
	newGithubActivityTestServer(t)
	ctx := newTestContext()
	result := handleGitHubActivity(`{"repo":"octocat/hello-world","kind":"issues"}`, ctx)
	if strings.HasPrefix(result, "error:") {
		t.Fatalf("result = %q, want a formatted issue list", result)
	}
	if !strings.Contains(result, "#101") || !strings.Contains(result, "Widgets sometimes disappear") {
		t.Errorf("result missing issue #101\nfull result:\n%s", result)
	}
	if strings.Contains(result, "#100") {
		t.Errorf("result = %q, should filter out issue #100 since it's actually a pull request", result)
	}
}

func TestHandleGitHubActivity_Issues_StatePassedThrough(t *testing.T) {
	newGithubActivityTestServer(t)
	var gotState string
	origMux := githubActivityMux(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/issues") {
			gotState = r.URL.Query().Get("state")
		}
		origMux.ServeHTTP(w, r)
	}))
	t.Cleanup(srv.Close)
	original := githubAPIBaseURL
	githubAPIBaseURL = srv.URL
	t.Cleanup(func() { githubAPIBaseURL = original })

	ctx := newTestContext()
	handleGitHubActivity(`{"repo":"octocat/hello-world","kind":"issues","state":"all"}`, ctx)
	if gotState != "all" {
		t.Errorf("issues request state = %q, want %q", gotState, "all")
	}
}

func TestHandleGitHubActivity_Issues_DefaultStateIsOpen(t *testing.T) {
	newGithubActivityTestServer(t)
	var gotState string
	origMux := githubActivityMux(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/issues") {
			gotState = r.URL.Query().Get("state")
		}
		origMux.ServeHTTP(w, r)
	}))
	t.Cleanup(srv.Close)
	original := githubAPIBaseURL
	githubAPIBaseURL = srv.URL
	t.Cleanup(func() { githubAPIBaseURL = original })

	ctx := newTestContext()
	handleGitHubActivity(`{"repo":"octocat/hello-world","kind":"issues"}`, ctx)
	if gotState != "open" {
		t.Errorf("issues request state = %q, want default %q", gotState, "open")
	}
}

// --- kind=commits --------------------------------------------------------

func TestHandleGitHubActivity_Commits_Success(t *testing.T) {
	newGithubActivityTestServer(t)
	ctx := newTestContext()
	result := handleGitHubActivity(`{"repo":"octocat/hello-world","kind":"commits","since":"2026-08-01"}`, ctx)
	if strings.HasPrefix(result, "error:") {
		t.Fatalf("result = %q, want a formatted commit list", result)
	}
	for _, want := range []string{"abc1234", "Fix widget crash", "octocat", "2026-08-20"} {
		if !strings.Contains(result, want) {
			t.Errorf("result missing %q\nfull result:\n%s", want, result)
		}
	}
	if len(ctx.Citations) != 1 {
		t.Errorf("Citations = %+v, want exactly one citation", ctx.Citations)
	}
}

func TestHandleGitHubActivity_Commits_SinceSentAsQueryParam(t *testing.T) {
	newGithubActivityTestServer(t)
	var gotSince string
	origMux := githubActivityMux(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/commits") {
			gotSince = r.URL.Query().Get("since")
		}
		origMux.ServeHTTP(w, r)
	}))
	t.Cleanup(srv.Close)
	original := githubAPIBaseURL
	githubAPIBaseURL = srv.URL
	t.Cleanup(func() { githubAPIBaseURL = original })

	ctx := newTestContext()
	handleGitHubActivity(`{"repo":"octocat/hello-world","kind":"commits","since":"2026-08-01"}`, ctx)
	if !strings.HasPrefix(gotSince, "2026-08-01T00:00:00") {
		t.Errorf("commits request since = %q, want it normalized to a full RFC3339 timestamp", gotSince)
	}
}

func TestHandleGitHubActivity_Commits_EmptyList(t *testing.T) {
	newGithubActivityTestServer(t)
	ctx := newTestContext()
	result := handleGitHubActivity(`{"repo":"octocat/empty","kind":"commits","since":"2026-08-01"}`, ctx)
	if strings.HasPrefix(result, "error:") {
		t.Fatalf("result = %q, want a friendly no-commits message, not an error", result)
	}
	if !strings.Contains(result, "No commits") {
		t.Errorf("result = %q, want it to mention there are no commits", result)
	}
}

// --- shared behavior across kinds ---------------------------------------

func TestHandleGitHubActivity_UsesBearerTokenWhenSet(t *testing.T) {
	var gotAuth string
	mux := githubActivityMux(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		mux.ServeHTTP(w, r)
	}))
	t.Cleanup(srv.Close)
	original := githubAPIBaseURL
	githubAPIBaseURL = srv.URL
	t.Cleanup(func() { githubAPIBaseURL = original })

	ctx := newTestContext()
	ctx.GitHubToken = "test-token-123"
	handleGitHubActivity(`{"repo":"octocat/hello-world","kind":"releases"}`, ctx)
	if gotAuth != "Bearer test-token-123" {
		t.Errorf("Authorization header = %q, want %q", gotAuth, "Bearer test-token-123")
	}
}

func TestHandleGitHubActivity_RepoNotFound(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		w.Write([]byte(`{"message":"Not Found"}`))
	}))
	t.Cleanup(srv.Close)
	original := githubAPIBaseURL
	githubAPIBaseURL = srv.URL
	t.Cleanup(func() { githubAPIBaseURL = original })

	ctx := newTestContext()
	result := handleGitHubActivity(`{"repo":"ghost/ghost","kind":"releases"}`, ctx)
	if !strings.HasPrefix(result, "error:") {
		t.Errorf("result = %q, want a not-found error", result)
	}
}

func TestHandleGitHubActivity_RateLimited(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-RateLimit-Remaining", "0")
		w.WriteHeader(http.StatusForbidden)
		w.Write([]byte(`{"message":"API rate limit exceeded"}`))
	}))
	t.Cleanup(srv.Close)
	original := githubAPIBaseURL
	githubAPIBaseURL = srv.URL
	t.Cleanup(func() { githubAPIBaseURL = original })

	ctx := newTestContext()
	result := handleGitHubActivity(`{"repo":"octocat/hello-world","kind":"commits","since":"2026-08-01"}`, ctx)
	if !strings.HasPrefix(result, "error:") || !strings.Contains(result, "rate limit") {
		t.Errorf("result = %q, want a rate-limit error", result)
	}
}
