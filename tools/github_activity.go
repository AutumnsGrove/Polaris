// github_activity is github_repo's exploration counterpart: where
// github_repo answers snapshot/stats questions, this answers "what
// happened" questions — recent releases, a specific PR's detail, recent
// issue activity, or commits since a date. Kept as one tool with a
// required "kind" param (same shape as memory.go's action param) rather
// than four separate tools, since all four are variations on "explore
// into this repo's activity" and a model picking between four
// near-identical HTTP-wrapper tools is worse UX than one tool with a
// clear kind enum.
package tools

import (
	"encoding/json"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"time"

	"polaris/llm"
)

var githubActivityDef = llm.ToolDef{
	Type: "function",
	Function: llm.ToolFunctionDef{
		Name: "github_activity",
		// Description is populated at call time by Defs()/AllDefs() from
		// tools/descriptions/github_activity.yaml — see tools/catalog.go.
		Parameters: map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"repo": map[string]interface{}{
					"type": "string",
					"description": "The repository as \"owner/repo\" (e.g. \"facebook/react\") or a full " +
						"github.com URL.",
				},
				"kind": map[string]interface{}{
					"type": "string",
					"enum": []string{"releases", "pr", "issues", "commits"},
					"description": "releases: recent published releases with notes. pr: a specific pull " +
						"request's detail (requires pr_number). issues: recent issue activity (not pull " +
						"requests). commits: commits since a date (requires since).",
				},
				"pr_number": map[string]interface{}{
					"type":        "integer",
					"description": "The pull request number to fetch. Required for kind=pr, ignored otherwise.",
				},
				"since": map[string]interface{}{
					"type": "string",
					"description": "A date (\"2026-08-01\") or RFC3339 timestamp. Required for kind=commits " +
						"(commits on/after this date). Optional for kind=issues (only issues updated on/after " +
						"this date).",
				},
				"state": map[string]interface{}{
					"type":        "string",
					"enum":        []string{"open", "closed", "all"},
					"description": "For kind=issues only: which issues to include. Default \"open\".",
				},
				"limit": map[string]interface{}{
					"type": "integer",
					"description": "Max number of results for kind=releases (default 5, max 20), kind=issues " +
						"(default 10, max 30), or kind=commits (default 10, max 50). Ignored for kind=pr.",
				},
			},
			"required": []string{"repo", "kind"},
		},
	},
}

func init() { Register("github_activity", handleGitHubActivity) }

func handleGitHubActivity(argsJSON string, ctx *Context) string {
	var args struct {
		Repo     string `json:"repo"`
		Kind     string `json:"kind"`
		PRNumber *int   `json:"pr_number"`
		Since    string `json:"since"`
		State    string `json:"state"`
		Limit    *int   `json:"limit"`
	}
	if err := json.Unmarshal([]byte(argsJSON), &args); err != nil {
		return emitToolError(ctx, "github_activity", nil, "error: "+err.Error())
	}
	if args.Repo == "" {
		return emitToolError(ctx, "github_activity", map[string]interface{}{"repo": args.Repo}, "error: repo is required")
	}
	if args.Kind == "" {
		return emitToolError(ctx, "github_activity", map[string]interface{}{"repo": args.Repo},
			"error: kind is required (one of releases, pr, issues, commits)")
	}

	owner, repo, err := parseGitHubRepo(args.Repo)
	if err != nil {
		return emitToolError(ctx, "github_activity", map[string]interface{}{"repo": args.Repo, "kind": args.Kind},
			"error: "+err.Error())
	}
	slug := owner + "/" + repo

	ctx.Emit("tool_call", map[string]interface{}{
		"tool": "github_activity",
		"args": map[string]interface{}{"repo": slug, "kind": args.Kind},
	})

	var result string
	switch args.Kind {
	case "releases":
		result, err = fetchGitHubReleasesResult(ctx, owner, repo, args.Limit)
	case "pr":
		if args.PRNumber == nil {
			err = fmt.Errorf("pr_number is required for kind=pr")
		} else {
			result, err = fetchGitHubPRDetailResult(ctx, owner, repo, *args.PRNumber)
		}
	case "issues":
		result, err = fetchGitHubIssueActivityResult(ctx, owner, repo, args.State, args.Since, args.Limit)
	case "commits":
		if args.Since == "" {
			err = fmt.Errorf(`since is required for kind=commits (e.g. "2026-08-01")`)
		} else {
			result, err = fetchGitHubCommitsSinceResult(ctx, owner, repo, args.Since, args.Limit)
		}
	default:
		err = fmt.Errorf("unknown kind %q (must be one of releases, pr, issues, commits)", args.Kind)
	}

	if err != nil {
		result = "error: " + err.Error()
		log.Warn("github_activity failed", "repo", slug, "kind", args.Kind, "err", err)
		ctx.Emit("tool_result", map[string]interface{}{"tool": "github_activity", "result": result})
		return result
	}

	log.Info("github_activity", "repo", slug, "kind", args.Kind)
	ctx.Emit("tool_result", map[string]interface{}{
		"tool":      "github_activity",
		"result":    result,
		"citations": ctx.CitationsSnapshot(),
	})
	return result
}

// --- kind=releases -----------------------------------------------------

type githubRelease struct {
	TagName     string `json:"tag_name"`
	Name        string `json:"name"`
	PublishedAt string `json:"published_at"`
	Body        string `json:"body"`
	HTMLURL     string `json:"html_url"`
	Draft       bool   `json:"draft"`
	Prerelease  bool   `json:"prerelease"`
}

func fetchGitHubReleasesResult(ctx *Context, owner, repo string, limit *int) (string, error) {
	n := clampGitHubLimit(limit, 5, 20)
	reqURL := githubAPIBaseURL + "/repos/" + owner + "/" + repo + "/releases?per_page=" + strconv.Itoa(n)

	body, _, err := githubHTTPGet(ctx.Ctx, reqURL, ctx.GitHubToken)
	if err != nil {
		return "", err
	}
	var releases []githubRelease
	if err := json.Unmarshal(body, &releases); err != nil {
		return "", fmt.Errorf("parsing releases response: %w", err)
	}

	slug := owner + "/" + repo
	var sb strings.Builder
	count := 0
	for _, r := range releases {
		if r.Draft {
			continue
		}
		if count >= n {
			break
		}
		count++

		title := r.Name
		if title == "" {
			title = r.TagName
		}
		fmt.Fprintf(&sb, "### %s (%s)\n", title, r.TagName)
		if published, err := time.Parse(time.RFC3339, r.PublishedAt); err == nil {
			fmt.Fprintf(&sb, "Published %s\n", published.Format("2006-01-02"))
		}
		if r.Prerelease {
			sb.WriteString("(prerelease)\n")
		}
		if r.Body != "" {
			fmt.Fprintf(&sb, "%s\n", collapseWhitespace(r.Body))
		}
		fmt.Fprintf(&sb, "%s\n\n", r.HTMLURL)
	}

	if count == 0 {
		return fmt.Sprintf("%s has no published releases.", slug), nil
	}

	ctx.AddCitation(Citation{Title: slug + " releases", URL: "https://github.com/" + slug + "/releases"})
	return fmt.Sprintf("Recent releases for %s:\n\n%s", slug, sb.String()), nil
}

// --- kind=pr -------------------------------------------------------------

type githubPRDetail struct {
	Number    int    `json:"number"`
	Title     string `json:"title"`
	State     string `json:"state"`
	Merged    bool   `json:"merged"`
	MergedAt  string `json:"merged_at"`
	CreatedAt string `json:"created_at"`
	User      struct {
		Login string `json:"login"`
	} `json:"user"`
	Body         string `json:"body"`
	HTMLURL      string `json:"html_url"`
	Additions    int    `json:"additions"`
	Deletions    int    `json:"deletions"`
	ChangedFiles int    `json:"changed_files"`
	Base         struct {
		Ref string `json:"ref"`
	} `json:"base"`
	Head struct {
		Ref string `json:"ref"`
	} `json:"head"`
}

func fetchGitHubPRDetailResult(ctx *Context, owner, repo string, number int) (string, error) {
	reqURL := githubAPIBaseURL + "/repos/" + owner + "/" + repo + "/pulls/" + strconv.Itoa(number)

	body, _, err := githubHTTPGet(ctx.Ctx, reqURL, ctx.GitHubToken)
	if err != nil {
		return "", err
	}
	var pr githubPRDetail
	if err := json.Unmarshal(body, &pr); err != nil {
		return "", fmt.Errorf("parsing pull request response: %w", err)
	}

	status := pr.State
	if pr.Merged {
		status = "merged"
	}

	var sb strings.Builder
	fmt.Fprintf(&sb, "PR #%d: %s\n%s\n", pr.Number, pr.Title, pr.HTMLURL)
	fmt.Fprintf(&sb, "by %s · %s\n", pr.User.Login, status)
	fmt.Fprintf(&sb, "%s <- %s\n", pr.Base.Ref, pr.Head.Ref)
	fmt.Fprintf(&sb, "+%d -%d across %d files\n", pr.Additions, pr.Deletions, pr.ChangedFiles)
	if pr.Merged {
		if merged, err := time.Parse(time.RFC3339, pr.MergedAt); err == nil {
			fmt.Fprintf(&sb, "Merged %s\n", merged.Format("2006-01-02"))
		}
	}
	if pr.Body != "" {
		fmt.Fprintf(&sb, "\n%s\n", collapseWhitespace(pr.Body))
	}

	ctx.AddCitation(Citation{Title: fmt.Sprintf("%s/%s PR #%d", owner, repo, pr.Number), URL: pr.HTMLURL})
	return sb.String(), nil
}

// --- kind=issues ---------------------------------------------------------

type githubIssue struct {
	Number    int    `json:"number"`
	Title     string `json:"title"`
	State     string `json:"state"`
	CreatedAt string `json:"created_at"`
	UpdatedAt string `json:"updated_at"`
	Comments  int    `json:"comments"`
	HTMLURL   string `json:"html_url"`
	User      struct {
		Login string `json:"login"`
	} `json:"user"`
	// PullRequest is only present on entries that are actually pull
	// requests — GitHub's issues API returns both issues and PRs in the
	// same list, this is the field that tells them apart.
	PullRequest *struct{} `json:"pull_request"`
}

func fetchGitHubIssueActivityResult(ctx *Context, owner, repo, state, since string, limit *int) (string, error) {
	n := clampGitHubLimit(limit, 10, 30)
	if state == "" {
		state = "open"
	}

	reqURL := githubAPIBaseURL + "/repos/" + owner + "/" + repo + "/issues?state=" + url.QueryEscape(state) +
		"&sort=updated&direction=desc&per_page=" + strconv.Itoa(n)
	if since != "" {
		sinceTime, err := parseGitHubSince(since)
		if err != nil {
			return "", err
		}
		reqURL += "&since=" + url.QueryEscape(sinceTime.UTC().Format(time.RFC3339))
	}

	body, _, err := githubHTTPGet(ctx.Ctx, reqURL, ctx.GitHubToken)
	if err != nil {
		return "", err
	}
	var issues []githubIssue
	if err := json.Unmarshal(body, &issues); err != nil {
		return "", fmt.Errorf("parsing issues response: %w", err)
	}

	slug := owner + "/" + repo
	var sb strings.Builder
	count := 0
	for _, iss := range issues {
		if iss.PullRequest != nil {
			continue
		}
		if count >= n {
			break
		}
		count++

		fmt.Fprintf(&sb, "#%d %s [%s]\n", iss.Number, iss.Title, iss.State)
		created, createdErr := time.Parse(time.RFC3339, iss.CreatedAt)
		updated, updatedErr := time.Parse(time.RFC3339, iss.UpdatedAt)
		if createdErr == nil {
			fmt.Fprintf(&sb, "Opened %s by %s", created.Format("2006-01-02"), iss.User.Login)
			if updatedErr == nil {
				fmt.Fprintf(&sb, ", updated %s", updated.Format("2006-01-02"))
			}
			fmt.Fprintf(&sb, ", %d comments\n", iss.Comments)
		}
		fmt.Fprintf(&sb, "%s\n\n", iss.HTMLURL)
	}

	if count == 0 {
		return fmt.Sprintf("No matching issue activity found for %s.", slug), nil
	}

	ctx.AddCitation(Citation{Title: slug + " issues", URL: "https://github.com/" + slug + "/issues"})
	return fmt.Sprintf("Recent issue activity for %s (state=%s):\n\n%s", slug, state, sb.String()), nil
}

// --- kind=commits --------------------------------------------------------

type githubCommitSummary struct {
	SHA     string `json:"sha"`
	HTMLURL string `json:"html_url"`
	Commit  struct {
		Message string `json:"message"`
		Author  struct {
			Name string `json:"name"`
		} `json:"author"`
		Committer struct {
			Date string `json:"date"`
		} `json:"committer"`
	} `json:"commit"`
}

func fetchGitHubCommitsSinceResult(ctx *Context, owner, repo, since string, limit *int) (string, error) {
	n := clampGitHubLimit(limit, 10, 50)

	sinceTime, err := parseGitHubSince(since)
	if err != nil {
		return "", err
	}
	sinceParam := sinceTime.UTC().Format(time.RFC3339)

	reqURL := githubAPIBaseURL + "/repos/" + owner + "/" + repo + "/commits?since=" +
		url.QueryEscape(sinceParam) + "&per_page=" + strconv.Itoa(n)

	body, _, err := githubHTTPGet(ctx.Ctx, reqURL, ctx.GitHubToken)
	if err != nil {
		return "", err
	}
	var commits []githubCommitSummary
	if err := json.Unmarshal(body, &commits); err != nil {
		return "", fmt.Errorf("parsing commits response: %w", err)
	}

	slug := owner + "/" + repo
	if len(commits) == 0 {
		return fmt.Sprintf("No commits found for %s since %s.", slug, sinceTime.Format("2006-01-02")), nil
	}

	var sb strings.Builder
	for _, c := range commits {
		shortSHA := c.SHA
		if len(shortSHA) > 7 {
			shortSHA = shortSHA[:7]
		}
		firstLine := c.Commit.Message
		if idx := strings.Index(firstLine, "\n"); idx != -1 {
			firstLine = firstLine[:idx]
		}
		fmt.Fprintf(&sb, "%s %s — %s", shortSHA, firstLine, c.Commit.Author.Name)
		if d, err := time.Parse(time.RFC3339, c.Commit.Committer.Date); err == nil {
			fmt.Fprintf(&sb, ", %s", d.Format("2006-01-02"))
		}
		fmt.Fprintf(&sb, "\n%s\n\n", c.HTMLURL)
	}

	ctx.AddCitation(Citation{Title: slug + " commits", URL: "https://github.com/" + slug + "/commits"})
	return fmt.Sprintf("Commits since %s for %s:\n\n%s", sinceTime.Format("2006-01-02"), slug, sb.String()), nil
}

// --- shared helpers --------------------------------------------------------

// githubSinceDateLayout is the date-only form the tool description asks
// the model for ("2026-08-01") — parseGitHubSince also accepts a full
// RFC3339 timestamp for a model that supplies one anyway.
const githubSinceDateLayout = "2006-01-02"

// parseGitHubSince accepts either a bare date or an RFC3339 timestamp and
// normalizes to a time.Time; a bare date is treated as UTC midnight.
func parseGitHubSince(s string) (time.Time, error) {
	if s == "" {
		return time.Time{}, fmt.Errorf("since is required")
	}
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return t, nil
	}
	if t, err := time.Parse(githubSinceDateLayout, s); err == nil {
		return t, nil
	}
	return time.Time{}, fmt.Errorf("couldn't parse since date %q (expected YYYY-MM-DD or RFC3339)", s)
}

// clampGitHubLimit resolves an optional user-supplied limit against a
// default and a hard max — nil, zero, or negative all fall back to def,
// matching include_readme's *bool "nil means default" pattern in
// github_repo.go rather than requiring every caller to pass a value.
func clampGitHubLimit(requested *int, def, max int) int {
	if requested == nil || *requested <= 0 {
		return def
	}
	if *requested > max {
		return max
	}
	return *requested
}
