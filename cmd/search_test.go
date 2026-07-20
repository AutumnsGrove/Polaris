package cmd

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// fakeOpenRouterForSearch serves one canned plain-text streamed answer —
// enough for runSearch's agent.Run call to produce a final answer
// without any tool calls.
func fakeOpenRouterForSearch(t *testing.T, answer string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		flusher := w.(http.Flusher)
		fmt.Fprintf(w, "data: {\"choices\":[{\"delta\":{\"content\":%q}}]}\n", answer)
		flusher.Flush()
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{},\"finish_reason\":\"stop\"}],\"usage\":{\"cost\":0.0002}}\n")
		fmt.Fprint(w, "data: [DONE]\n")
	}))
	t.Cleanup(srv.Close)
	return srv
}

func writeSearchTestConfig(t *testing.T, llmBaseURL string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	contents := fmt.Sprintf(`
openrouter:
  api_key: "test-key"
  base_url: %q
default_model: "test-model"
models:
  - id: "test-model"
    name: "Test Model"
    model: "test/model"
    provider: ["test"]
    temperature: 0.4
    max_tokens: 1000
`, llmBaseURL)
	if err := os.WriteFile(path, []byte(contents), 0o644); err != nil {
		t.Fatalf("writing test config: %v", err)
	}
	return path
}

func captureStdout(t *testing.T, fn func()) string {
	t.Helper()
	old := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	os.Stdout = w
	fn()
	w.Close()
	os.Stdout = old

	out, err := io.ReadAll(r)
	if err != nil {
		t.Fatalf("reading captured stdout: %v", err)
	}
	return string(out)
}

func TestRunSearch_HappyPath(t *testing.T) {
	srv := fakeOpenRouterForSearch(t, "Paris is the capital of France.")

	origConfigPath, origModel := configPath, searchModel
	configPath = writeSearchTestConfig(t, srv.URL)
	searchModel = ""
	t.Cleanup(func() { configPath, searchModel = origConfigPath, origModel })

	output := captureStdout(t, func() {
		if err := runSearch(nil, []string{"what", "is", "the", "capital", "of", "france"}); err != nil {
			t.Fatalf("runSearch returned error: %v", err)
		}
	})

	if !strings.Contains(output, "Paris is the capital of France.") {
		t.Errorf("output = %q, want the answer printed", output)
	}
	if !strings.Contains(output, "model: Test Model") {
		t.Errorf("output = %q, want the model name printed", output)
	}
	if !strings.Contains(output, "cost: $0.0002") {
		t.Errorf("output = %q, want the cost printed", output)
	}
}

func TestRunSearch_ConfigLoadError(t *testing.T) {
	origConfigPath := configPath
	configPath = filepath.Join(t.TempDir(), "does-not-exist.yaml")
	t.Cleanup(func() { configPath = origConfigPath })

	if err := runSearch(nil, []string{"hello"}); err == nil {
		t.Fatal("expected an error for a missing config file")
	}
}
