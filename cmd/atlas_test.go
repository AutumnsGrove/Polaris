package cmd

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeAtlasTestConfig points searxng.base_url at a fake server and
// database.path at this test's own tempdir — without the latter,
// gateway.ResolveAtlasSearch's mandatory *store.Store dependency (see
// cmd/atlas.go) would resolve config.Load's "./polaris.db" default
// against the cmd package's source directory under `go test`, leaving a
// stray file behind after every run.
func writeAtlasTestConfig(t *testing.T, searxngBaseURL string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	contents := fmt.Sprintf(`
openrouter:
  api_key: "test-key"
searxng:
  base_url: %q
database:
  path: %q
`, searxngBaseURL, filepath.Join(dir, "test.db"))
	if err := os.WriteFile(path, []byte(contents), 0o644); err != nil {
		t.Fatalf("writing test config: %v", err)
	}
	return path
}

func fakeSearXNGForAtlasCmd(t *testing.T, results []map[string]interface{}) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{"query": r.URL.Query().Get("q"), "results": results})
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestRunAtlasSearch_PrintsResults(t *testing.T) {
	srv := fakeSearXNGForAtlasCmd(t, []map[string]interface{}{
		{"title": "Go 1.24 released", "url": "https://go.dev/blog/go1.24", "content": "New release"},
	})

	origConfigPath, origMaxResults, origPage, origCategory, origVerbose := configPath, atlasMaxResults, atlasPage, atlasCategory, atlasVerbose
	configPath = writeAtlasTestConfig(t, srv.URL)
	atlasMaxResults, atlasPage, atlasCategory, atlasVerbose = 20, 1, "", false
	t.Cleanup(func() {
		configPath, atlasMaxResults, atlasPage, atlasCategory, atlasVerbose = origConfigPath, origMaxResults, origPage, origCategory, origVerbose
	})

	output := captureStdout(t, func() {
		if err := runAtlasSearch(nil, []string{"golang", "release"}); err != nil {
			t.Fatalf("runAtlasSearch returned error: %v", err)
		}
	})

	if !strings.Contains(output, "Go 1.24 released") || !strings.Contains(output, "https://go.dev/blog/go1.24") {
		t.Errorf("output = %q, want the SearXNG result printed", output)
	}
}

func TestRunAtlasSearch_VerboseShowsProviderAndCacheState(t *testing.T) {
	srv := fakeSearXNGForAtlasCmd(t, []map[string]interface{}{
		{"title": "Result", "url": "https://a.com/1", "content": "c"},
	})

	origConfigPath, origMaxResults, origPage, origCategory, origVerbose := configPath, atlasMaxResults, atlasPage, atlasCategory, atlasVerbose
	configPath = writeAtlasTestConfig(t, srv.URL)
	atlasMaxResults, atlasPage, atlasCategory, atlasVerbose = 20, 1, "", true
	t.Cleanup(func() {
		configPath, atlasMaxResults, atlasPage, atlasCategory, atlasVerbose = origConfigPath, origMaxResults, origPage, origCategory, origVerbose
	})

	output := captureStdout(t, func() {
		if err := runAtlasSearch(nil, []string{"q"}); err != nil {
			t.Fatalf("runAtlasSearch returned error: %v", err)
		}
	})

	if !strings.Contains(output, "provider=searxng") {
		t.Errorf("output = %q, want it to show provider=searxng", output)
	}
	if !strings.Contains(output, "cache=MISS") {
		t.Errorf("output = %q, want it to show a cache miss on the first fetch", output)
	}
}

func TestRunAtlasSearch_SecondCallIsServedFromCache(t *testing.T) {
	var hits int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{"query": "q", "results": []map[string]interface{}{
			{"title": "Result", "url": "https://a.com/1", "content": "c"},
		}})
	}))
	t.Cleanup(srv.Close)

	origConfigPath, origMaxResults, origPage, origCategory, origVerbose := configPath, atlasMaxResults, atlasPage, atlasCategory, atlasVerbose
	configPath = writeAtlasTestConfig(t, srv.URL)
	atlasMaxResults, atlasPage, atlasCategory, atlasVerbose = 20, 1, "", true
	t.Cleanup(func() {
		configPath, atlasMaxResults, atlasPage, atlasCategory, atlasVerbose = origConfigPath, origMaxResults, origPage, origCategory, origVerbose
	})

	for i := 0; i < 2; i++ {
		if err := runAtlasSearch(nil, []string{"q"}); err != nil {
			t.Fatalf("runAtlasSearch (call %d) returned error: %v", i, err)
		}
	}

	if hits != 1 {
		t.Errorf("searxng hits = %d, want 1 — the second CLI invocation should reuse the same on-disk cache the first one wrote", hits)
	}
}

func TestRunAtlasSearch_NoResults(t *testing.T) {
	srv := fakeSearXNGForAtlasCmd(t, nil)

	origConfigPath, origMaxResults, origPage, origCategory, origVerbose := configPath, atlasMaxResults, atlasPage, atlasCategory, atlasVerbose
	configPath = writeAtlasTestConfig(t, srv.URL)
	atlasMaxResults, atlasPage, atlasCategory, atlasVerbose = 20, 1, "", false
	t.Cleanup(func() {
		configPath, atlasMaxResults, atlasPage, atlasCategory, atlasVerbose = origConfigPath, origMaxResults, origPage, origCategory, origVerbose
	})

	output := captureStdout(t, func() {
		if err := runAtlasSearch(nil, []string{"something", "obscure"}); err != nil {
			t.Fatalf("runAtlasSearch returned error: %v", err)
		}
	})

	if strings.TrimSpace(output) != "no results" {
		t.Errorf("output = %q, want %q", output, "no results")
	}
}
