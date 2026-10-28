package cmd

import (
	"net/http"
	"strings"
	"testing"
)

// TestRunEmbeddingsStatus_HappyPath pins the thin-client contract: the CLI
// asks the running container for /api/embeddings/status and renders what it
// gets, including the coverage denominators it must report against
// (eligible, not raw) and the stale-model warning.
func TestRunEmbeddingsStatus_HappyPath(t *testing.T) {
	fakeLocalPolarisServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/embeddings/status" {
			t.Errorf("path = %s, want /api/embeddings/status", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"configured": true,
			"model": "perplexity/pplx-embed-v1-0.6b",
			"index": {
				"message": {"indexed": 1856, "eligible": 1856, "stale_model": 0},
				"star": {"indexed": 200, "eligible": 220, "stale_model": 3}
			},
			"cost_usd": 0.004028
		}`))
	})

	output := captureStdout(t, func() {
		if err := runEmbeddingsStatus(nil, nil); err != nil {
			t.Fatalf("runEmbeddingsStatus() error = %v, want nil", err)
		}
	})

	for _, want := range []string{"perplexity/pplx-embed-v1-0.6b", "1856 / 1856", "200 / 220", "reindex", "$0.0040"} {
		if !strings.Contains(output, want) {
			t.Errorf("output = %q, want it to contain %q", output, want)
		}
	}
}

// TestRunEmbeddingsStatus_NotConfigured covers the deployment that never
// turned embeddings on: a clear "set this" message, not an empty table or
// an error.
func TestRunEmbeddingsStatus_NotConfigured(t *testing.T) {
	fakeLocalPolarisServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"configured": false}`))
	})

	output := captureStdout(t, func() {
		if err := runEmbeddingsStatus(nil, nil); err != nil {
			t.Fatalf("runEmbeddingsStatus() error = %v, want nil", err)
		}
	})
	if !strings.Contains(output, "embeddings.provider") {
		t.Errorf("output = %q, want a pointer to the config key to set", output)
	}
}

// TestRunEmbeddingsJob_ReportsCountsAndCost covers the backfill response
// rendering, including the provider-reported spend the operator is agreeing
// to.
func TestRunEmbeddingsJob_ReportsCountsAndCost(t *testing.T) {
	fakeLocalPolarisServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("method = %s, want POST", r.Method)
		}
		if r.URL.Path != "/api/embeddings/backfill" {
			t.Errorf("path = %s, want /api/embeddings/backfill", r.URL.Path)
		}
		if got := r.URL.Query().Get("kind"); got != "all" {
			t.Errorf("kind = %q, want the default all", got)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"messages": 1856, "stars": 220, "cost_usd": 0.004028, "kind": "all", "reindex": false}`))
	})

	embeddingsKind = "all"
	embeddingsLimit = 0
	output := captureStdout(t, func() {
		if err := runEmbeddingsJob("backfill"); err != nil {
			t.Fatalf("runEmbeddingsJob() error = %v, want nil", err)
		}
	})
	for _, want := range []string{"1856", "220", "0.004028"} {
		if !strings.Contains(output, want) {
			t.Errorf("output = %q, want it to contain %q", output, want)
		}
	}
}

// TestRunEmbeddingsJob_ConflictIsExplained covers the 409 the server returns
// for an unconfigured deployment or an already-running backfill — the CLI
// must say which, rather than surfacing a bare status code.
func TestRunEmbeddingsJob_ConflictIsExplained(t *testing.T) {
	fakeLocalPolarisServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusConflict)
		_, _ = w.Write([]byte(`{"error":"an embedding backfill is already running"}`))
	})

	embeddingsKind = "all"
	err := runEmbeddingsJob("backfill")
	if err == nil {
		t.Fatal("want an error for a 409, got nil")
	}
	if !strings.Contains(err.Error(), "already running") && !strings.Contains(err.Error(), "not configured") {
		t.Errorf("error = %q, want it to explain the conflict", err)
	}
}

// TestEmbeddingsReindex_RequiresConfirmation pins the destructive-command
// guard: reindex deletes data, so it must refuse without --yes.
func TestEmbeddingsReindex_RequiresConfirmation(t *testing.T) {
	embeddingsYes = false
	if err := embeddingsReindexCmd.RunE(embeddingsReindexCmd, nil); err == nil {
		t.Fatal("reindex without --yes should refuse, got nil")
	}
}
