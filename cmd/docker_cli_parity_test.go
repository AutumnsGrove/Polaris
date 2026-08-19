package cmd

import (
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestRunDockerStats_HappyPath confirms the fetched-over-HTTP path
// prints in exactly the same shape as the bare-metal local-store path
// (printStats is shared by both — see stats.go) by exercising real
// store.Stats JSON, not a hand-rolled fixture.
func TestRunDockerStats_HappyPath(t *testing.T) {
	fakeLocalPolarisServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/stats" {
			t.Errorf("path = %s, want /api/stats", r.URL.Path)
		}
		if got := r.URL.Query().Get("days"); got != "7" {
			t.Errorf("days query param = %q, want %q", got, "7")
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"period_days": 7,
			"total_cost_usd": 1.2345,
			"period_cost_usd": 0.5,
			"thread_count": 3,
			"turn_count": 10,
			"avg_turn_duration_ms": 4500,
			"tool_call_counts": {"web_search": 5},
			"tool_error_counts": {"web_search": 1},
			"check_in_count": 2,
			"stale_streak_count": 0,
			"max_turns_wrapup_count": 0
		}`))
	})

	output := captureStdout(t, func() {
		if err := runDockerStats(7); err != nil {
			t.Fatalf("runDockerStats() error = %v, want nil", err)
		}
	})

	if !strings.Contains(output, "last 7 days") {
		t.Errorf("output = %q, want the period label", output)
	}
	if !strings.Contains(output, "web_search") {
		t.Errorf("output = %q, want the tool call breakdown", output)
	}
	if !strings.Contains(output, "$1.2345") {
		t.Errorf("output = %q, want the total cost", output)
	}
}

func TestRunDockerStats_ServerError(t *testing.T) {
	fakeLocalPolarisServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	})

	if err := runDockerStats(30); err == nil {
		t.Fatal("runDockerStats() error = nil, want an error on a 500")
	}
}

func TestRunDockerSearch_HappyPath(t *testing.T) {
	fakeLocalPolarisServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("method = %s, want POST", r.Method)
		}
		if r.URL.Path != "/api/ask" {
			t.Errorf("path = %s, want /api/ask", r.URL.Path)
		}
		body, _ := io.ReadAll(r.Body)
		if !strings.Contains(string(body), `"content":"capital of France"`) {
			t.Errorf("request body = %s, want it to carry the query as content", body)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"thread_id": "abc",
			"answer": "Paris is the capital of France.",
			"citations": [{"title": "France - Wikipedia", "url": "https://en.wikipedia.org/wiki/France"}],
			"cost_usd": 0.0007
		}`))
	})

	output := captureStdout(t, func() {
		if err := runDockerSearch("capital of France", ""); err != nil {
			t.Fatalf("runDockerSearch() error = %v, want nil", err)
		}
	})

	if !strings.Contains(output, "Paris is the capital of France.") {
		t.Errorf("output = %q, want the answer printed", output)
	}
	if !strings.Contains(output, "France - Wikipedia") {
		t.Errorf("output = %q, want the citation printed", output)
	}
	if !strings.Contains(output, "$0.00070") {
		t.Errorf("output = %q, want the cost printed", output)
	}
}

func TestRunDockerSearch_ServerReportedError(t *testing.T) {
	fakeLocalPolarisServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"error":"the server is restarting — please retry in a few seconds"}`))
	})

	err := runDockerSearch("anything", "")
	if err == nil {
		t.Fatal("runDockerSearch() error = nil, want the server's reported error")
	}
	if !strings.Contains(err.Error(), "restarting") {
		t.Errorf("error = %q, want the real server-reported message", err.Error())
	}
}

func TestRunDockerAtlasSearch_HappyPath(t *testing.T) {
	fakeLocalPolarisServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/search" {
			t.Errorf("path = %s, want /api/search", r.URL.Path)
		}
		if got := r.URL.Query().Get("q"); got != "rust async runtime" {
			t.Errorf("q query param = %q, want %q", got, "rust async runtime")
		}
		if got := r.URL.Query().Get("max_results"); got != "3" {
			t.Errorf("max_results query param = %q, want %q", got, "3")
		}
		if got := r.URL.Query().Get("page"); got != "1" {
			t.Errorf("page query param = %q, want %q", got, "1")
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"query": "rust async runtime",
			"results": [
				{"title": "Tokio", "url": "https://tokio.rs", "content": "An async runtime.", "score": 0.03, "engine": "duckduckgo", "rank_state": "raise"}
			]
		}`))
	})

	output := captureStdout(t, func() {
		if err := runDockerAtlasSearch("rust async runtime", 3, 1, ""); err != nil {
			t.Fatalf("runDockerAtlasSearch() error = %v, want nil", err)
		}
	})

	if !strings.Contains(output, "Tokio") || !strings.Contains(output, "https://tokio.rs") {
		t.Errorf("output = %q, want the result's title and URL", output)
	}
	if !strings.Contains(output, "via duckduckgo") {
		t.Errorf("output = %q, want the engine attribution", output)
	}
	if !strings.Contains(output, "raise") {
		t.Errorf("output = %q, want the non-default rank state shown", output)
	}
}

func TestRunDockerAtlasSearch_NoResults(t *testing.T) {
	fakeLocalPolarisServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"query": "asdfqwerty", "results": []}`))
	})

	output := captureStdout(t, func() {
		if err := runDockerAtlasSearch("asdfqwerty", 8, 1, ""); err != nil {
			t.Fatalf("runDockerAtlasSearch() error = %v, want nil", err)
		}
	})

	if !strings.Contains(output, "no results") {
		t.Errorf("output = %q, want a no-results message", output)
	}
}

func TestRunDockerAtlasSearch_ServerError(t *testing.T) {
	fakeLocalPolarisServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
		_, _ = w.Write([]byte("searxng unreachable"))
	})

	err := runDockerAtlasSearch("anything", 8, 1, "")
	if err == nil {
		t.Fatal("runDockerAtlasSearch() error = nil, want an error on a non-200")
	}
	if !strings.Contains(err.Error(), "searxng unreachable") {
		t.Errorf("error = %q, want the server's response body included", err.Error())
	}
}

// TestRunInstall_RefusesUnderDocker confirms `polaris install` doesn't
// silently write a systemd/launchd unit for the orphaned host-side
// binary when the current directory is actually a Docker install.
func TestRunInstall_RefusesUnderDocker(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "docker-compose.yml"), []byte("services: {}\n"), 0o644); err != nil {
		t.Fatalf("writing docker-compose.yml: %v", err)
	}
	t.Chdir(dir)

	err := runInstall(nil, nil)
	if err == nil {
		t.Fatal("runInstall() error = nil, want a refusal under a Docker install")
	}
	if !strings.Contains(err.Error(), "Docker install") {
		t.Errorf("error = %q, want it to explain this is a Docker install", err.Error())
	}
}
