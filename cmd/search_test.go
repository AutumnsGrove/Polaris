package cmd

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
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
	// database.path explicitly scoped into the same tempdir — without
	// it, config.Load's own "./polaris.db" default resolves against
	// whatever the test binary's actual working directory happens to be
	// (the cmd package's source directory under `go test`), and runSearch
	// opening a real store.Store (needed to check Parallel's usage cap)
	// would leave a stray polaris.db sitting there after every test run.
	contents := fmt.Sprintf(`
openrouter:
  api_key: "test-key"
  base_url: %q
database:
  path: %q
default_model: "mimo-pro"
`, llmBaseURL, filepath.Join(dir, "test.db"))
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
	if !strings.Contains(output, "model: MiMo v2.5 Pro") {
		t.Errorf("output = %q, want the model name printed", output)
	}
	if !strings.Contains(output, "cost: $0.0002") {
		t.Errorf("output = %q, want the cost printed", output)
	}
}

// searchChatsToolCallServer fakes a model that calls search_chats first,
// then answers from the result — a two-round SSE server, same shape as
// gateway/ask_test.go's commentaryThenAnswerServer.
func searchChatsToolCallServer(t *testing.T) *httptest.Server {
	t.Helper()
	var reqCount int32

	round1 := []string{
		`data: {"choices":[{"delta":{"tool_calls":[{"index":0,"id":"call_1","type":"function","function":{"name":"search_chats","arguments":"{\"action\":\"search\",\"query\":\"graphics card\"}"}}]}}]}`,
		`data: {"choices":[{"delta":{},"finish_reason":"tool_calls"}],"usage":{"cost":0.0001}}`,
		`data: [DONE]`,
	}
	round2 := []string{
		`data: {"choices":[{"delta":{"content":"No past threads matched that."}}]}`,
		`data: {"choices":[{"delta":{},"finish_reason":"stop"}],"usage":{"cost":0.0001}}`,
		`data: [DONE]`,
	}

	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := atomic.AddInt32(&reqCount, 1)
		w.Header().Set("Content-Type", "text/event-stream")
		flusher := w.(http.Flusher)
		lines := round1
		if n >= 2 {
			lines = round2
		}
		for _, line := range lines {
			fmt.Fprintf(w, "%s\n", line)
			flusher.Flush()
		}
	}))
}

// TestRunSearch_SearchChatsToolIsWired is this file's own version of the
// gap CLAUDE.md flags for cmd/search.go specifically: a live-only
// capability (here, search_chats' SearchThreads/ListRecentThreads/
// ReadThread closures) that's easy to wire for the web UI/websocket path
// and forget for `polaris search`'s CLI one-shot path — the exact class of
// gap that bit web_search's Brave/Parallel wiring in this same file
// before. Drives a real tool-call
// round trip through runSearch rather than inspecting agentCtx directly
// (which is a local variable, not exposed) — if search_chats' closures
// were left nil here, catalog.go's "chat_search" gate would exclude the
// tool from the model's menu entirely and this fake model's tool call
// would never even be offered/dispatched, or handleSearchChats would
// return "search_chats is not available in this context" instead of a
// real (even if empty) search result.
func TestRunSearch_SearchChatsToolIsWired(t *testing.T) {
	srv := searchChatsToolCallServer(t)

	origConfigPath, origModel := configPath, searchModel
	configPath = writeSearchTestConfig(t, srv.URL)
	searchModel = ""
	t.Cleanup(func() { configPath, searchModel = origConfigPath, origModel })

	output := captureStdout(t, func() {
		if err := runSearch(nil, []string{"did", "I", "ask", "about", "graphics", "cards"}); err != nil {
			t.Fatalf("runSearch returned error: %v", err)
		}
	})

	if strings.Contains(output, "not available in this context") {
		t.Errorf("output = %q, want search_chats' closures wired even on the CLI one-shot path", output)
	}
	if !strings.Contains(output, "No past threads matched that.") {
		t.Errorf("output = %q, want the final answer after the search_chats round trip", output)
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
