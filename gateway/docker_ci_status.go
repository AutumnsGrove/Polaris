// waitForPublishWorkflow closes a real race window in handleDockerUpdate:
// GHCR's ":latest" tag has no "half-published" state visible via its
// API — the old manifest just stays put, unchanged, until a new push
// completes and the tag atomically flips. So a click on "Update
// Polaris" landing in the narrow window between a commit reaching
// main and docker-publish.yml finishing its ~1.5-3.5 minute build (see
// project history) would have resolveLatestDigest silently resolve to
// the PREVIOUS build, report success, and deliver stale code with no
// indication anything was off.
package gateway

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"
)

// githubActionsAPIBaseURL is a var, not a const, so tests can point it
// at a fake httptest server — same convention as
// tools/github_repo.go's githubAPIBaseURL and this package's own
// ghcrRegistryBaseURL.
var githubActionsAPIBaseURL = "https://api.github.com"

// dockerPublishWorkflowFile must match the actual filename under
// .github/workflows/ — GitHub's runs-listing endpoint is keyed by it.
const dockerPublishWorkflowFile = "docker-publish.yml"

// ciPollInterval/ciMaxWait are vars, not consts, so tests can shrink
// them instead of a timeout-path test taking 6 real minutes to run.
// ciMaxWait comfortably exceeds the slowest docker-publish.yml run
// observed in practice (multi-arch, both platforms pinned to
// $BUILDPLATFORM so neither runs under QEMU — see the Dockerfile) plus
// headroom for a slow GHCR push or a colder Actions cache.
var (
	ciPollInterval = 5 * time.Second
	ciMaxWait      = 6 * time.Minute
)

const githubAPIRequestTimeout = 10 * time.Second

// waitForPublishWorkflow polls whether the most recent docker-publish.yml
// run for main is still queued/in_progress, and if so, blocks (bounded
// by ciMaxWait) until it finishes before returning — so the caller's
// subsequent resolveLatestDigest call is guaranteed to see the build
// that's actually in flight, not whatever it was resolving before that
// run started.
//
// Deliberately best-effort: any failure checking GitHub's API (rate
// limited, transient network issue, GitHub itself having a bad day)
// just logs a warning and returns immediately, letting the update
// proceed exactly as it did before this existed. This closes a real
// but narrow race window — it must never become a new way for
// "Update Polaris" to fail outright over a GitHub API hiccup that has
// nothing to do with whether the update itself would have worked.
func waitForPublishWorkflow(ctx context.Context, githubToken string) {
	deadline := time.Now().Add(ciMaxWait)
	for {
		inProgress, err := publishWorkflowInProgress(ctx, githubToken)
		if err != nil {
			log.Warn("checking docker-publish.yml run status failed, proceeding without waiting on CI", "err", err)
			return
		}
		if !inProgress {
			return
		}
		if time.Now().After(deadline) {
			log.Warn("docker-publish.yml still running after max wait, proceeding anyway", "waited", ciMaxWait)
			return
		}
		log.Info("docker-publish.yml is still building the latest commit, waiting before resolving the image to pull")
		select {
		case <-ctx.Done():
			return
		case <-time.After(ciPollInterval):
		}
	}
}

// publishWorkflowInProgress reports whether the single most recent
// docker-publish.yml run for the main branch has status "queued" or
// "in_progress" — a plain, unauthenticated call works (this is a
// public repo), but an optional token (config.yaml's github.token,
// already used by the github_repo tool) raises the rate limit the same
// way it does there, worth reusing here since this call can happen
// several times per update if CI is genuinely still running.
func publishWorkflowInProgress(ctx context.Context, token string) (bool, error) {
	url := githubActionsAPIBaseURL + "/repos/autumnsgrove/polaris/actions/workflows/" + dockerPublishWorkflowFile + "/runs?branch=main&per_page=1"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return false, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}

	client := &http.Client{Timeout: githubAPIRequestTimeout}
	resp, err := client.Do(req)
	if err != nil {
		return false, fmt.Errorf("fetching workflow runs: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return false, fmt.Errorf("github actions api status %d", resp.StatusCode)
	}

	var body struct {
		WorkflowRuns []struct {
			Status string `json:"status"` // "queued" | "in_progress" | "completed"
		} `json:"workflow_runs"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return false, fmt.Errorf("decoding workflow runs response: %w", err)
	}
	if len(body.WorkflowRuns) == 0 {
		return false, nil
	}

	status := body.WorkflowRuns[0].Status
	return status == "queued" || status == "in_progress", nil
}
