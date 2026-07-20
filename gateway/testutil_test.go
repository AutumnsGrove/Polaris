package gateway

import (
	"fmt"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"polaris/config"
	"polaris/store"
)

// testHarness bundles a live Server (backed by a real temp-file SQLite
// store and a real config.yaml, exactly like production) behind an
// httptest.Server, plus the config path so tests can rewrite it and
// exercise liveConfig()'s hot-reload behavior.
type testHarness struct {
	srv        *httptest.Server
	cfgPath    string
	db         *store.Store
	llmBaseURL string
}

// writeTestConfig renders a minimal config.yaml pointing OpenRouter at
// llmBaseURL (a fake SSE server tests provide) so any code path that
// builds its own llm.Client from cfg (generateSuggestions, generateTitle,
// handleTurn) hits that fake instead of the real OpenRouter.
func writeTestConfig(t *testing.T, dir, llmBaseURL string) string {
	t.Helper()
	path := filepath.Join(dir, "config.yaml")
	contents := fmt.Sprintf(`
server:
  port: 0
openrouter:
  api_key: "test-key"
  base_url: %q
database:
  path: %q
default_model: "test-model"
models:
  - id: "test-model"
    name: "Test Model One"
    model: "test/model-one"
    provider: ["test"]
    temperature: 0.4
    max_tokens: 1000
  - id: "other-model"
    name: "Other Model"
    model: "test/other-model"
    provider: ["test"]
    temperature: 0.5
    max_tokens: 1000
`, llmBaseURL, filepath.Join(dir, "test.db"))
	if err := os.WriteFile(path, []byte(contents), 0o644); err != nil {
		t.Fatalf("writing test config: %v", err)
	}
	return path
}

func newTestHarness(t *testing.T, llmBaseURL string) *testHarness {
	t.Helper()
	dir := t.TempDir()
	cfgPath := writeTestConfig(t, dir, llmBaseURL)

	cfg, err := config.Load(cfgPath)
	if err != nil {
		t.Fatalf("config.Load: %v", err)
	}
	db, err := store.Open(cfg.Database.Path)
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { db.Close() })

	s := New(cfg, cfgPath, db, nil)
	httpSrv := httptest.NewServer(s.Handler())
	t.Cleanup(httpSrv.Close)

	return &testHarness{srv: httpSrv, cfgPath: cfgPath, db: db, llmBaseURL: llmBaseURL}
}

func (h *testHarness) url(path string) string {
	return h.srv.URL + path
}

// rewriteConfig overwrites config.yaml with a new model list, for tests
// of liveConfig()'s hot-reload behavior — mirrors what a user editing
// the file by hand while the server keeps running looks like.
func (h *testHarness) rewriteConfig(t *testing.T, extraModel string) {
	t.Helper()
	dir := filepath.Dir(h.cfgPath)
	contents := fmt.Sprintf(`
server:
  port: 0
openrouter:
  api_key: "test-key"
  base_url: %q
database:
  path: %q
default_model: "test-model"
models:
  - id: "test-model"
    name: "Test Model One"
    model: "test/model-one"
    provider: ["test"]
    temperature: 0.4
    max_tokens: 1000
  - id: %q
    name: "Freshly Added Model"
    model: "test/new-model"
    provider: ["test"]
    temperature: 0.4
    max_tokens: 1000
`, h.llmBaseURL, filepath.Join(dir, "test.db"), extraModel)
	if err := os.WriteFile(h.cfgPath, []byte(contents), 0o644); err != nil {
		t.Fatalf("rewriting test config: %v", err)
	}
}
