package gateway

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

// fakeWorkflowRuns serves a minimal stand-in for GitHub's
// list-workflow-runs endpoint, returning a single run with the given
// status on every request statusFn is called for.
func fakeWorkflowRuns(t *testing.T, statusFn func() string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		status := statusFn()
		w.Header().Set("Content-Type", "application/json")
		if status == "" {
			_, _ = w.Write([]byte(`{"workflow_runs":[]}`))
			return
		}
		_, _ = w.Write([]byte(`{"workflow_runs":[{"status":"` + status + `"}]}`))
	}))
	t.Cleanup(srv.Close)
	return srv
}

func withGitHubActionsBaseURL(t *testing.T, url string) {
	t.Helper()
	original := githubActionsAPIBaseURL
	githubActionsAPIBaseURL = url
	t.Cleanup(func() { githubActionsAPIBaseURL = original })
}

func TestPublishWorkflowInProgress_InProgress(t *testing.T) {
	srv := fakeWorkflowRuns(t, func() string { return "in_progress" })
	withGitHubActionsBaseURL(t, srv.URL)

	got, err := publishWorkflowInProgress(context.Background(), "")
	if err != nil {
		t.Fatalf("publishWorkflowInProgress() error = %v, want nil", err)
	}
	if !got {
		t.Error("got false, want true for status=in_progress")
	}
}

func TestPublishWorkflowInProgress_Queued(t *testing.T) {
	srv := fakeWorkflowRuns(t, func() string { return "queued" })
	withGitHubActionsBaseURL(t, srv.URL)

	got, err := publishWorkflowInProgress(context.Background(), "")
	if err != nil {
		t.Fatalf("publishWorkflowInProgress() error = %v, want nil", err)
	}
	if !got {
		t.Error("got false, want true for status=queued")
	}
}

func TestPublishWorkflowInProgress_Completed(t *testing.T) {
	srv := fakeWorkflowRuns(t, func() string { return "completed" })
	withGitHubActionsBaseURL(t, srv.URL)

	got, err := publishWorkflowInProgress(context.Background(), "")
	if err != nil {
		t.Fatalf("publishWorkflowInProgress() error = %v, want nil", err)
	}
	if got {
		t.Error("got true, want false for status=completed")
	}
}

// TestPublishWorkflowInProgress_NoRuns guards a repo/workflow with no
// run history yet — must not error or report "in progress" just
// because there's nothing to look at.
func TestPublishWorkflowInProgress_NoRuns(t *testing.T) {
	srv := fakeWorkflowRuns(t, func() string { return "" })
	withGitHubActionsBaseURL(t, srv.URL)

	got, err := publishWorkflowInProgress(context.Background(), "")
	if err != nil {
		t.Fatalf("publishWorkflowInProgress() error = %v, want nil", err)
	}
	if got {
		t.Error("got true, want false when there are no workflow runs at all")
	}
}

func TestPublishWorkflowInProgress_APIFailure(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	t.Cleanup(srv.Close)
	withGitHubActionsBaseURL(t, srv.URL)

	if _, err := publishWorkflowInProgress(context.Background(), ""); err == nil {
		t.Fatal("publishWorkflowInProgress() error = nil, want an error when the GitHub API 500s")
	}
}

// TestWaitForPublishWorkflow_PollsUntilComplete is the core behavior
// this whole mechanism exists for: a run that's genuinely in progress
// when the wait starts must not let the caller proceed until it's
// actually done — this simulates a run finishing partway through the
// wait and confirms waitForPublishWorkflow doesn't return early.
func TestWaitForPublishWorkflow_PollsUntilComplete(t *testing.T) {
	var calls int32
	srv := fakeWorkflowRuns(t, func() string {
		n := atomic.AddInt32(&calls, 1)
		if n < 3 {
			return "in_progress"
		}
		return "completed"
	})
	withGitHubActionsBaseURL(t, srv.URL)

	originalInterval, originalMaxWait := ciPollInterval, ciMaxWait
	ciPollInterval = 10 * time.Millisecond
	ciMaxWait = 2 * time.Second
	t.Cleanup(func() { ciPollInterval, ciMaxWait = originalInterval, originalMaxWait })

	start := time.Now()
	waitForPublishWorkflow(context.Background(), "")
	elapsed := time.Since(start)

	if got := atomic.LoadInt32(&calls); got < 3 {
		t.Errorf("publishWorkflowInProgress was called %d times, want at least 3 (polled until completed)", got)
	}
	// Must have actually waited across at least 2 poll intervals, not
	// returned instantly — proves it didn't just check once and give up.
	if elapsed < 2*ciPollInterval {
		t.Errorf("waitForPublishWorkflow returned after %v, want at least %v (should have polled)", elapsed, 2*ciPollInterval)
	}
}

// TestWaitForPublishWorkflow_ReturnsImmediatelyWhenNotRunning is the
// common case (no update pushed recently) — must not add any polling
// delay at all when there's nothing in flight.
func TestWaitForPublishWorkflow_ReturnsImmediatelyWhenNotRunning(t *testing.T) {
	srv := fakeWorkflowRuns(t, func() string { return "completed" })
	withGitHubActionsBaseURL(t, srv.URL)

	originalInterval := ciPollInterval
	ciPollInterval = 5 * time.Second // would make this test slow if hit even once
	t.Cleanup(func() { ciPollInterval = originalInterval })

	start := time.Now()
	waitForPublishWorkflow(context.Background(), "")
	if elapsed := time.Since(start); elapsed > 500*time.Millisecond {
		t.Errorf("waitForPublishWorkflow took %v for an already-completed run, want near-instant", elapsed)
	}
}

// TestWaitForPublishWorkflow_DegradesGracefullyOnAPIFailure is the
// safety property this mechanism must never violate: a GitHub API
// problem unrelated to whether the update itself would succeed must
// never block or fail the update.
func TestWaitForPublishWorkflow_DegradesGracefullyOnAPIFailure(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	t.Cleanup(srv.Close)
	withGitHubActionsBaseURL(t, srv.URL)

	originalInterval := ciPollInterval
	ciPollInterval = 5 * time.Second
	t.Cleanup(func() { ciPollInterval = originalInterval })

	start := time.Now()
	waitForPublishWorkflow(context.Background(), "")
	if elapsed := time.Since(start); elapsed > 500*time.Millisecond {
		t.Errorf("waitForPublishWorkflow took %v after an API failure, want near-instant fallback", elapsed)
	}
}

// TestWaitForPublishWorkflow_RespectsMaxWait guards against a stuck
// run (or a bug in this code) hanging an update forever.
func TestWaitForPublishWorkflow_RespectsMaxWait(t *testing.T) {
	srv := fakeWorkflowRuns(t, func() string { return "in_progress" }) // never completes
	withGitHubActionsBaseURL(t, srv.URL)

	originalInterval, originalMaxWait := ciPollInterval, ciMaxWait
	ciPollInterval = 10 * time.Millisecond
	ciMaxWait = 50 * time.Millisecond
	t.Cleanup(func() { ciPollInterval, ciMaxWait = originalInterval, originalMaxWait })

	start := time.Now()
	waitForPublishWorkflow(context.Background(), "")
	if elapsed := time.Since(start); elapsed > 500*time.Millisecond {
		t.Errorf("waitForPublishWorkflow took %v for a run stuck in_progress, want it to give up around ciMaxWait (50ms)", elapsed)
	}
}
