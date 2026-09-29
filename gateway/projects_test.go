package gateway

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"polaris/store"
)

func TestProjectPromptBlock(t *testing.T) {
	if got := projectPromptBlock(nil, []string{"a"}); got != "" {
		t.Errorf("nil project should produce no block, got %q", got)
	}

	p := &store.Project{Name: "Budget", CustomInstructions: "  Answer in metric.  "}
	bare := projectPromptBlock(p, nil)
	if !strings.Contains(bare, "## Project: Budget") || !strings.Contains(bare, "Answer in metric.") {
		t.Errorf("block missing name/instructions: %q", bare)
	}
	if strings.Contains(bare, "/project") {
		t.Errorf("a project with no shared files shouldn't talk about a shared directory: %q", bare)
	}

	withFiles := projectPromptBlock(p, []string{"a.csv", "b.md"})
	for _, want := range []string{"a.csv, b.md", "read-only", "/project", "save_to_project"} {
		if !strings.Contains(withFiles, want) {
			t.Errorf("block missing %q: %q", want, withFiles)
		}
	}

	// A pool that accumulates across many threads must not balloon the prompt.
	many := make([]string, maxProjectPromptFiles+7)
	for i := range many {
		many[i] = fmt.Sprintf("f%03d.txt", i)
	}
	capped := projectPromptBlock(p, many)
	if strings.Contains(capped, fmt.Sprintf("f%03d.txt", maxProjectPromptFiles)) || !strings.Contains(capped, "and 7 more") {
		t.Errorf("file list not capped with an overflow note: %q", capped)
	}
}

func TestJoinCustomInstructions(t *testing.T) {
	cases := []struct{ global, block, want string }{
		{"G", "", "G"},
		{"", "P", "P"},
		{"  ", "P", "P"},
		{"G", "P", "G\n\nP"},
	}
	for _, c := range cases {
		if got := joinCustomInstructions(c.global, c.block); got != c.want {
			t.Errorf("join(%q,%q) = %q, want %q", c.global, c.block, got, c.want)
		}
	}
}

func TestListProjectFiles(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "p1")
	if err := os.MkdirAll(filepath.Join(dir, "subdir"), 0o755); err != nil {
		t.Fatal(err)
	}
	for _, n := range []string{"b.txt", "a.txt", ".code_exec_x.py"} {
		if err := os.WriteFile(filepath.Join(dir, n), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	got := listProjectFiles(root, "p1")
	if strings.Join(got, ",") != "a.txt,b.txt" {
		t.Errorf("got %v, want sorted regular non-dot files only", got)
	}
	if listProjectFiles(root, "never-created") != nil || listProjectFiles("", "p1") != nil {
		t.Error("a missing directory or unconfigured workspace should list nothing")
	}
}

// projectTurnServer is a fake LLM that records every request body.
func projectTurnServer(t *testing.T) (srv *httptest.Server, bodies func() []string) {
	t.Helper()
	var mu sync.Mutex
	var recorded []string
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		mu.Lock()
		recorded = append(recorded, string(body))
		mu.Unlock()
		chunk, _ := json.Marshal(map[string]interface{}{
			"choices": []map[string]interface{}{{"delta": map[string]interface{}{"content": "an answer"}}},
		})
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprintf(w, "data: %s\n", chunk)
		w.(http.Flusher).Flush()
		fmt.Fprintf(w, "data: %s\n", `{"choices":[{"delta":{},"finish_reason":"stop"}],"usage":{"cost":0.0001}}`)
		fmt.Fprint(w, "data: [DONE]\n")
	}))
	t.Cleanup(srv.Close)
	return srv, func() []string {
		mu.Lock()
		defer mu.Unlock()
		return append([]string(nil), recorded...)
	}
}

// End to end through a real WebSocket turn and the real request body: a
// thread born in a project is bound to it, its instructions reach the
// model (and keep reaching it on a continuation, re-read off the root row),
// memory_mode=none withholds the memory tool, and an ordinary thread gets
// none of it.
func TestWebSocket_ProjectThread_InstructionsMemoryAndBinding(t *testing.T) {
	srv, bodies := projectTurnServer(t)
	h := newTestHarness(t, srv.URL)

	alpha, err := h.db.CreateProject(store.Project{
		Name: "Alpha", CustomInstructions: "ALWAYS-ANSWER-IN-HAIKU", MemoryMode: store.ProjectMemoryNone,
	})
	if err != nil {
		t.Fatal(err)
	}
	beta, err := h.db.CreateProject(store.Project{Name: "Beta", CustomInstructions: "BETA-RULES"})
	if err != nil {
		t.Fatal(err)
	}
	conn := dialWS(t, h)

	send := func(m map[string]interface{}) []map[string]interface{} {
		t.Helper()
		m["type"], m["model"] = "message", "test-model"
		if err := conn.WriteJSON(m); err != nil {
			t.Fatalf("WriteJSON: %v", err)
		}
		return readEventsUntilDone(t, conn, 5*time.Second)
	}
	threadOf := func(evs []map[string]interface{}) string {
		id, _ := evs[len(evs)-1]["thread_id"].(string)
		if id == "" {
			t.Fatal("done event carried no thread_id")
		}
		return id
	}

	// Turn 1: new thread inside Alpha.
	alphaThread := threadOf(send(map[string]interface{}{"content": "hi", "project_id": alpha.ID}))
	th, err := h.db.GetThread(alphaThread)
	if err != nil || th.ProjectID == nil || *th.ProjectID != alpha.ID {
		t.Fatalf("thread not bound to its project at creation: %+v err %v", th, err)
	}
	// Turn 2: continuation with NO project_id — must still be in Alpha.
	send(map[string]interface{}{"content": "and again", "thread_id": alphaThread})
	// Turn 3: a project with default memory keeps the memory tool.
	send(map[string]interface{}{"content": "hi", "project_id": beta.ID})
	// Turn 4: ordinary thread.
	send(map[string]interface{}{"content": "hi"})

	all := bodies()
	var chatBodies []string
	for _, b := range all {
		if strings.Contains(b, `"tools"`) { // skip title-generation calls
			chatBodies = append(chatBodies, b)
		}
	}
	if len(chatBodies) != 4 {
		t.Fatalf("tool-bearing requests = %d, want 4 (one per turn)", len(chatBodies))
	}
	names := toolBearingRequestToolNames(t, chatBodies)

	for i, label := range []string{"alpha turn 1", "alpha continuation"} {
		if !strings.Contains(chatBodies[i], "ALWAYS-ANSWER-IN-HAIKU") || !strings.Contains(chatBodies[i], "## Project: Alpha") {
			t.Errorf("%s: project instructions never reached the model", label)
		}
		if contains(names[i], "memory") {
			t.Errorf("%s: memory tool offered despite memory_mode=none: %v", label, names[i])
		}
		if contains(names[i], "save_to_project") {
			// No workspace configured in the default test config, so the tool
			// is (correctly) not offered — see catalog.go's project_workspace.
			t.Errorf("%s: save_to_project offered with no code_exec workspace configured", label)
		}
	}
	if !strings.Contains(chatBodies[2], "BETA-RULES") || strings.Contains(chatBodies[2], "ALWAYS-ANSWER-IN-HAIKU") {
		t.Error("beta turn should carry beta's instructions and only those")
	}
	if !contains(names[2], "memory") {
		t.Errorf("beta (default memory mode) lost the memory tool: %v", names[2])
	}
	if strings.Contains(chatBodies[3], "## Project:") || strings.Contains(chatBodies[3], "BETA-RULES") {
		t.Error("an ordinary thread got project content")
	}
	if !contains(names[3], "memory") {
		t.Errorf("ordinary thread lost the memory tool: %v", names[3])
	}
}

// A stale picker (project deleted since the page loaded) must error cleanly
// BEFORE a thread row exists, not leave an orphan behind.
func TestWebSocket_ProjectThread_UnknownProjectErrorsWithoutCreatingThread(t *testing.T) {
	srv, _ := projectTurnServer(t)
	h := newTestHarness(t, srv.URL)
	conn := dialWS(t, h)

	if err := conn.WriteJSON(map[string]interface{}{
		"type": "message", "content": "hi", "model": "test-model", "project_id": "no-such-project",
	}); err != nil {
		t.Fatal(err)
	}
	evs := readEventsUntilDone(t, conn, 5*time.Second)
	last := evs[len(evs)-1]
	if last["type"] != "error" {
		t.Fatalf("last event = %v, want an error", last)
	}
	threads, err := h.db.ListThreads(10)
	if err != nil || len(threads) != 0 {
		t.Errorf("a failed project bind left %d thread(s) behind (err %v)", len(threads), err)
	}
}
