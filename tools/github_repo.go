// github_repo answers the numeric-stats questions web_search/web_read can't
// reliably answer for a GitHub repo (star count, commit history, open
// issue/PR counts) by querying GitHub's REST API directly instead of
// scraping the rendered repo page. GitHub's API works unauthenticated
// (60 requests/hour), so this needs no config to function at all — an
// optional token (ctx.GitHubToken) just raises that ceiling to 5000/hour,
// same "optional, better with it" shape as places.FoursquareClient/
// tavily.Client, except here it's a header on plain net/http calls rather
// than a whole client type, since GitHub's REST API needs no other setup.
package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"polaris/llm"
)

var githubRepoDef = llm.ToolDef{
	Type: "function",
	Function: llm.ToolFunctionDef{
		Name: "github_repo",
		// Description is populated at call time by Defs()/AllDefs() from
		// tools/descriptions/github_repo.yaml — see tools/catalog.go.
		Parameters: map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"repo": map[string]interface{}{
					"type": "string",
					"description": "The repository as \"owner/repo\" (e.g. \"facebook/react\") or a full " +
						"github.com URL.",
				},
				"include_readme": map[string]interface{}{
					"type": "boolean",
					"description": "Set false to skip fetching the README and return only the stats block. " +
						"Default true.",
				},
			},
			"required": []string{"repo"},
		},
	},
}

func init() { Register("github_repo", handleGitHubRepo) }

// githubAPIBaseURL is a var (not a const) so tests can point it at a fake
// server, same pattern as places.nominatimBaseURL and web_read.go's
// waybackAvailabilityAPI.
var githubAPIBaseURL = "https://api.github.com"

const githubUserAgent = "Polaris/1.0 (personal search assistant; +https://github.com/AutumnsGrove/Polaris)"

func handleGitHubRepo(argsJSON string, ctx *Context) string {
	var args struct {
		Repo          string `json:"repo"`
		IncludeReadme *bool  `json:"include_readme"`
	}
	if err := json.Unmarshal([]byte(argsJSON), &args); err != nil {
		return emitToolError(ctx, "github_repo", nil, "error: "+err.Error())
	}
	if args.Repo == "" {
		return emitToolError(ctx, "github_repo", map[string]interface{}{"repo": args.Repo}, "error: repo is required")
	}
	includeReadme := true
	if args.IncludeReadme != nil {
		includeReadme = *args.IncludeReadme
	}

	owner, repo, err := parseGitHubRepo(args.Repo)
	if err != nil {
		return emitToolError(ctx, "github_repo", map[string]interface{}{"repo": args.Repo}, "error: "+err.Error())
	}
	slug := owner + "/" + repo

	ctx.Emit("tool_call", map[string]interface{}{
		"tool": "github_repo",
		"args": map[string]interface{}{"repo": slug},
	})

	stats, err := fetchGitHubRepoStats(ctx.Ctx, owner, repo, ctx.GitHubToken)
	if err != nil {
		result := "error: " + err.Error()
		log.Warn("github_repo failed", "repo", slug, "err", err)
		ctx.Emit("tool_result", map[string]interface{}{"tool": "github_repo", "result": result})
		return result
	}

	result := formatGitHubRepoStats(stats)

	if includeReadme {
		readme, err := fetchGitHubReadme(ctx.Ctx, owner, repo, ctx.GitHubToken)
		if err != nil {
			// Missing README (or a transient fetch failure) shouldn't sink
			// the whole call — the stats above are already good.
			log.Warn("github_repo: readme fetch failed", "repo", slug, "err", err)
			result += "\n\nREADME: unavailable (" + err.Error() + ")"
		} else {
			result += "\n\n--- README ---\n" + readme
		}
	}

	log.Info("github_repo", "repo", slug, "stars", stats.Stars, "commits", stats.CommitCount)
	ctx.AddCitation(Citation{Title: stats.FullName, URL: stats.HTMLURL})
	ctx.Emit("tool_result", map[string]interface{}{
		"tool":      "github_repo",
		"result":    result,
		"citations": ctx.CitationsSnapshot(),
	})
	return result
}

// githubRepoSlugPattern matches a bare "owner/repo" input with no URL
// wrapper — checked before githubURLPattern so a plain slug never has to
// round-trip through url.Parse.
var githubRepoSlugPattern = regexp.MustCompile(`^[A-Za-z0-9._-]+/[A-Za-z0-9._-]+$`)

// githubURLPattern pulls owner/repo out of any github.com URL shape: https
// or bare host, optional ".git" suffix, and any trailing path (a specific
// file, branch, or tree). Non-greedy on the repo group so a trailing
// "/blob/main/..." doesn't get swallowed into the repo name itself.
var githubURLPattern = regexp.MustCompile(`github\.com[:/]([A-Za-z0-9._-]+)/([A-Za-z0-9._-]+?)(?:\.git)?(?:/.*)?$`)

// parseGitHubRepo accepts a bare "owner/repo" slug or any common
// github.com URL shape (https://, bare host, git@ SSH form, with or
// without a trailing .git or path) and returns just the owner and repo.
func parseGitHubRepo(input string) (owner, repo string, err error) {
	trimmed := strings.TrimSpace(input)
	trimmed = strings.TrimSuffix(trimmed, "/")

	if githubRepoSlugPattern.MatchString(trimmed) {
		parts := strings.SplitN(trimmed, "/", 2)
		return parts[0], parts[1], nil
	}
	if m := githubURLPattern.FindStringSubmatch(trimmed); m != nil {
		return m[1], m[2], nil
	}
	return "", "", fmt.Errorf("couldn't parse a GitHub owner/repo from %q", input)
}

// githubRepoInfo is the subset of GET /repos/{owner}/{repo} this needs.
// OpenIssuesCount is a well-known GitHub API quirk: it's issues *and* pull
// requests combined, not issues alone — see fetchGitHubRepoStats, which
// subtracts the open PR count (fetched separately) to recover a real
// issues-only number.
type githubRepoInfo struct {
	FullName        string `json:"full_name"`
	HTMLURL         string `json:"html_url"`
	Description     string `json:"description"`
	Language        string `json:"language"`
	StargazersCount int    `json:"stargazers_count"`
	ForksCount      int    `json:"forks_count"`
	OpenIssuesCount int    `json:"open_issues_count"`
	CreatedAt       string `json:"created_at"`
	Archived        bool   `json:"archived"`
	License         *struct {
		Name string `json:"name"`
	} `json:"license"`
}

// githubCommit is the subset of a commits-API list entry this needs.
// Committer date (not author date) is used for ordering/timestamps below —
// it's what GitHub's own commit list is actually ordered by, so it stays
// consistent with "most recent"/"first" as the API itself defines them
// (author date can differ, e.g. after a rebase or cherry-pick).
type githubCommit struct {
	Commit struct {
		Committer struct {
			Date string `json:"date"`
		} `json:"committer"`
	} `json:"commit"`
}

type githubRepoStats struct {
	FullName    string
	HTMLURL     string
	Description string
	Language    string
	License     string
	Archived    bool

	Stars      int
	Forks      int
	OpenPRs    int
	OpenIssues int

	CommitCount   int
	CreatedAt     time.Time
	FirstCommitAt time.Time
	LastCommitAt  time.Time
}

func fetchGitHubRepoStats(ctx context.Context, owner, repo, token string) (*githubRepoStats, error) {
	base := githubAPIBaseURL + "/repos/" + owner + "/" + repo

	body, _, err := githubHTTPGet(ctx, base, token)
	if err != nil {
		return nil, err
	}
	var info githubRepoInfo
	if err := json.Unmarshal(body, &info); err != nil {
		return nil, fmt.Errorf("parsing repo response: %w", err)
	}

	stats := &githubRepoStats{
		FullName:    info.FullName,
		HTMLURL:     info.HTMLURL,
		Description: info.Description,
		Language:    info.Language,
		Stars:       info.StargazersCount,
		Forks:       info.ForksCount,
		Archived:    info.Archived,
	}
	if info.License != nil {
		stats.License = info.License.Name
	}
	if createdAt, err := time.Parse(time.RFC3339, info.CreatedAt); err == nil {
		stats.CreatedAt = createdAt
	}

	// Commit count and first/most-recent commit dates via the classic
	// Link-header pagination trick: the commits API has no direct "total
	// commits" field, but requesting per_page=1 puts the total commit
	// count directly in the Link header's rel="last" page number, and
	// fetching that last page gets the oldest commit in one more request.
	// Best-effort — an empty repo (no commits yet) 409s here, which
	// shouldn't sink stats that already loaded fine above.
	commitsURL := base + "/commits?per_page=1"
	if latestBody, headers, err := githubHTTPGet(ctx, commitsURL, token); err != nil {
		log.Warn("github_repo: fetching commits failed", "repo", owner+"/"+repo, "err", err)
	} else {
		var latestPage []githubCommit
		if json.Unmarshal(latestBody, &latestPage) == nil && len(latestPage) > 0 {
			if d, err := time.Parse(time.RFC3339, latestPage[0].Commit.Committer.Date); err == nil {
				stats.LastCommitAt = d
				stats.FirstCommitAt = d
			}
			stats.CommitCount = 1

			if lastPage := parseGitHubLastPage(headers.Get("Link")); lastPage > 1 {
				stats.CommitCount = lastPage
				oldestURL := commitsURL + "&page=" + strconv.Itoa(lastPage)
				if oldestBody, _, err := githubHTTPGet(ctx, oldestURL, token); err == nil {
					var oldestPage []githubCommit
					if json.Unmarshal(oldestBody, &oldestPage) == nil && len(oldestPage) > 0 {
						if d, err := time.Parse(time.RFC3339, oldestPage[0].Commit.Committer.Date); err == nil {
							stats.FirstCommitAt = d
						}
					}
				}
			}
		}
	}

	// Open PR count, same Link-header trick against the pulls endpoint.
	// Also best-effort: on failure OpenPRs stays 0, and OpenIssues below
	// just falls back to GitHub's combined issues+PRs count rather than
	// the split-out issues-only number.
	pullsURL := base + "/pulls?state=open&per_page=1"
	if pullsBody, pullsHeaders, err := githubHTTPGet(ctx, pullsURL, token); err != nil {
		log.Warn("github_repo: fetching open PR count failed", "repo", owner+"/"+repo, "err", err)
	} else if lastPage := parseGitHubLastPage(pullsHeaders.Get("Link")); lastPage > 0 {
		stats.OpenPRs = lastPage
	} else {
		var pulls []struct{}
		if json.Unmarshal(pullsBody, &pulls) == nil {
			stats.OpenPRs = len(pulls)
		}
	}

	stats.OpenIssues = info.OpenIssuesCount - stats.OpenPRs
	if stats.OpenIssues < 0 {
		stats.OpenIssues = 0
	}

	return stats, nil
}

// githubLinkLastPagePattern pulls the page number out of a Link header's
// rel="last" entry, e.g. `<https://api.github.com/repos/x/y/commits?per_page=1&page=1523>; rel="last"`.
var githubLinkLastPagePattern = regexp.MustCompile(`[?&]page=(\d+)[^>]*>;\s*rel="last"`)

// parseGitHubLastPage returns the total page count implied by a response's
// Link header, or 0 if there's no rel="last" entry — which GitHub omits
// entirely when everything fits on one page, i.e. total count is 0 or 1.
func parseGitHubLastPage(linkHeader string) int {
	if linkHeader == "" {
		return 0
	}
	m := githubLinkLastPagePattern.FindStringSubmatch(linkHeader)
	if m == nil {
		return 0
	}
	n, err := strconv.Atoi(m[1])
	if err != nil {
		return 0
	}
	return n
}

// githubHTTPGet is the shared GET helper for every JSON call this tool
// makes (repo info, commits, pulls) — sets GitHub's recommended headers,
// attaches the optional bearer token, and surfaces a clear error for the
// two response shapes worth telling apart from a generic failure: repo
// not found, and rate-limited. Returns the response headers too, since
// callers need the Link header for pagination counts.
func githubHTTPGet(ctx context.Context, rawURL, token string) ([]byte, http.Header, error) {
	req, err := http.NewRequestWithContext(ctx, "GET", rawURL, nil)
	if err != nil {
		return nil, nil, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	req.Header.Set("User-Agent", githubUserAgent)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}

	client := &http.Client{Timeout: 15 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, nil, fmt.Errorf("fetching %s: %w", rawURL, err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, nil, fmt.Errorf("reading github response: %w", err)
	}

	if resp.StatusCode == http.StatusNotFound {
		// Generic wording rather than "repository not found" — this helper
		// is shared by github_activity.go's PR/issue/commit lookups too, so
		// a 404 here doesn't always mean the repo itself is missing.
		return nil, nil, fmt.Errorf("not found (or private, and no token configured that can see it)")
	}
	if resp.StatusCode == http.StatusForbidden && resp.Header.Get("X-RateLimit-Remaining") == "0" {
		return nil, nil, fmt.Errorf("github api rate limit exceeded — unauthenticated requests are capped at " +
			"60/hour; configure github.token in config.yaml for a 5000/hour limit")
	}
	if resp.StatusCode != http.StatusOK {
		return nil, nil, fmt.Errorf("github api error (status %d): %s", resp.StatusCode, collapseWhitespace(string(body)))
	}
	return body, resp.Header, nil
}

// fetchGitHubReadme fetches the repo's README as raw text via GitHub's
// raw-content media type, which returns the file's plain bytes directly —
// no base64 decoding needed, unlike the default JSON response shape for
// this endpoint. Reuses web_read.go's collapseWhitespace/maxExtractedChars
// for the same truncation behavior as any other fetched document.
func fetchGitHubReadme(ctx context.Context, owner, repo, token string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, "GET", githubAPIBaseURL+"/repos/"+owner+"/"+repo+"/readme", nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Accept", "application/vnd.github.raw+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	req.Header.Set("User-Agent", githubUserAgent)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}

	client := &http.Client{Timeout: 15 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("fetching readme: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("reading readme response: %w", err)
	}
	if resp.StatusCode == http.StatusNotFound {
		return "", fmt.Errorf("no README found")
	}
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("github api error (status %d)", resp.StatusCode)
	}

	return collapseWhitespace(string(body)), nil
}

func formatGitHubRepoStats(s *githubRepoStats) string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "%s\n%s\n\n", s.FullName, s.HTMLURL)
	if s.Description != "" {
		fmt.Fprintf(&sb, "%s\n\n", s.Description)
	}

	fmt.Fprintf(&sb, "%s stars · %s forks", formatGitHubCount(s.Stars), formatGitHubCount(s.Forks))
	if s.Language != "" {
		fmt.Fprintf(&sb, " · %s", s.Language)
	}
	if s.License != "" {
		fmt.Fprintf(&sb, " · %s license", s.License)
	}
	if s.Archived {
		sb.WriteString(" · archived")
	}
	sb.WriteString("\n")

	if !s.CreatedAt.IsZero() {
		fmt.Fprintf(&sb, "Created %s (%s ago)\n", s.CreatedAt.Format("2006-01-02"), formatGitHubAge(s.CreatedAt))
	}
	if !s.FirstCommitAt.IsZero() {
		fmt.Fprintf(&sb, "First commit: %s\n", s.FirstCommitAt.Format("2006-01-02"))
	}
	if !s.LastCommitAt.IsZero() {
		fmt.Fprintf(&sb, "Most recent commit: %s\n", s.LastCommitAt.Format("2006-01-02"))
	}
	if s.CommitCount > 0 {
		fmt.Fprintf(&sb, "%s commits on the default branch\n", formatGitHubCount(s.CommitCount))
	}
	fmt.Fprintf(&sb, "%d open issues · %d open pull requests\n", s.OpenIssues, s.OpenPRs)

	return sb.String()
}

// formatGitHubAge renders a created_at timestamp as a rough human age —
// "X years, Y months" once it's past a year old, otherwise just months or
// days. Precise to the point of being readable, not to the day.
func formatGitHubAge(t time.Time) string {
	d := time.Since(t)
	days := d.Hours() / 24

	years := int(days / 365.25)
	if years > 0 {
		months := int(days/30.44) % 12
		if months > 0 {
			return fmt.Sprintf("%d years, %d months", years, months)
		}
		return fmt.Sprintf("%d years", years)
	}
	months := int(days / 30.44)
	if months > 0 {
		return fmt.Sprintf("%d months", months)
	}
	return fmt.Sprintf("%d days", int(days))
}

// formatGitHubCount adds thousands separators (23,400) — GitHub's own UI
// convention for star/fork/commit counts, and much more readable than a
// bare six-digit number for a popular repo.
func formatGitHubCount(n int) string {
	s := strconv.Itoa(n)
	neg := strings.HasPrefix(s, "-")
	if neg {
		s = s[1:]
	}
	var parts []string
	for len(s) > 3 {
		parts = append([]string{s[len(s)-3:]}, parts...)
		s = s[:len(s)-3]
	}
	parts = append([]string{s}, parts...)
	out := strings.Join(parts, ",")
	if neg {
		out = "-" + out
	}
	return out
}
