package gateway

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"polaris/store"
)

func TestHandleListMemories(t *testing.T) {
	h := newTestHarness(t, "")
	if err := h.db.CreateMemory("user-timezone", "user", "the user's timezone", "US/Pacific"); err != nil {
		t.Fatalf("CreateMemory: %v", err)
	}

	resp, err := http.Get(h.url("/api/memories"))
	if err != nil {
		t.Fatalf("GET /api/memories: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}

	var memories []store.Memory
	if err := json.NewDecoder(resp.Body).Decode(&memories); err != nil {
		t.Fatalf("decoding response: %v", err)
	}
	if len(memories) != 1 || memories[0].Name != "user-timezone" || memories[0].Content != "US/Pacific" {
		t.Errorf("memories = %+v, want the one seeded row with full content", memories)
	}
}

func TestHandleUpdateMemory(t *testing.T) {
	h := newTestHarness(t, "")
	if err := h.db.CreateMemory("partial", "user", "orig desc", "orig content"); err != nil {
		t.Fatalf("CreateMemory: %v", err)
	}

	body, _ := json.Marshal(map[string]string{"description": "new desc"})
	req, _ := http.NewRequest(http.MethodPatch, h.url("/api/memories/partial"), bytes.NewReader(body))
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("PATCH /api/memories/partial: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("status = %d, want 204", resp.StatusCode)
	}

	m, err := h.db.GetMemory("partial")
	if err != nil {
		t.Fatalf("GetMemory: %v", err)
	}
	if m.Description != "new desc" || m.Content != "orig content" {
		t.Errorf("GetMemory = %+v, want description updated and content untouched", m)
	}
}

func TestHandleUpdateMemory_UnknownNameReturns404(t *testing.T) {
	h := newTestHarness(t, "")
	body, _ := json.Marshal(map[string]string{"description": "x"})
	req, _ := http.NewRequest(http.MethodPatch, h.url("/api/memories/nope"), bytes.NewReader(body))
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("PATCH: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("status = %d, want 404", resp.StatusCode)
	}
}

func TestHandleUpdateMemory_RejectsOverlongDescription(t *testing.T) {
	h := newTestHarness(t, "")
	if err := h.db.CreateMemory("m", "user", "d", "c"); err != nil {
		t.Fatalf("CreateMemory: %v", err)
	}
	long := strings.Repeat("a", 2001)
	body, _ := json.Marshal(map[string]string{"description": long})
	req, _ := http.NewRequest(http.MethodPatch, h.url("/api/memories/m"), bytes.NewReader(body))
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("PATCH: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", resp.StatusCode)
	}
}

func TestHandleDeleteMemory(t *testing.T) {
	h := newTestHarness(t, "")
	if err := h.db.CreateMemory("temp", "project", "d", "c"); err != nil {
		t.Fatalf("CreateMemory: %v", err)
	}

	req, _ := http.NewRequest(http.MethodDelete, h.url("/api/memories/temp"), nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("DELETE: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("status = %d, want 204", resp.StatusCode)
	}
	if _, err := h.db.GetMemory("temp"); err != store.ErrMemoryNotFound {
		t.Errorf("GetMemory after delete: err = %v, want ErrMemoryNotFound", err)
	}
}

func TestHandleDeleteMemory_UnknownNameReturns404(t *testing.T) {
	h := newTestHarness(t, "")
	req, _ := http.NewRequest(http.MethodDelete, h.url("/api/memories/nope"), nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("DELETE: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("status = %d, want 404", resp.StatusCode)
	}
}

// memoryChatLLMServer fakes a model that calls the memory tool once, then
// answers in plain text — the two-round shape handleMemoryChat's loop
// expects (see sseToolCallServer in location_roundtrip_test.go, same
// pattern applied to the memory tool instead of a location probe).
func memoryChatLLMServer(t *testing.T, toolArgsJSON, confirmation string) *httptest.Server {
	t.Helper()
	var reqCount int32

	round1 := []string{
		fmt.Sprintf(`data: {"choices":[{"delta":{"tool_calls":[{"index":0,"id":"call_1","type":"function","function":{"name":"memory","arguments":%q}}]}}]}`, toolArgsJSON),
		`data: {"choices":[{"delta":{},"finish_reason":"tool_calls"}],"usage":{"cost":0.0001}}`,
		`data: [DONE]`,
	}
	round2 := []string{
		fmt.Sprintf(`data: {"choices":[{"delta":{"content":%q}}]}`, confirmation),
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

func TestHandleMemoryChat_AppliesModelChosenEditAndReturnsConfirmation(t *testing.T) {
	toolArgs := `{"action":"forget","name":"stale-fact"}`
	srv := memoryChatLLMServer(t, toolArgs, "Forgot the memory about the stale fact.")
	h := newTestHarness(t, srv.URL)
	if err := h.db.CreateMemory("stale-fact", "project", "no longer true", "this was true once"); err != nil {
		t.Fatalf("CreateMemory: %v", err)
	}

	body, _ := json.Marshal(map[string]string{"instruction": "forget the stale fact memory"})
	resp, err := http.Post(h.url("/api/memories/chat"), "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("POST /api/memories/chat: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}

	var out struct {
		Message  string         `json:"message"`
		Memories []store.Memory `json:"memories"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatalf("decoding response: %v", err)
	}
	if out.Message != "Forgot the memory about the stale fact." {
		t.Errorf("Message = %q", out.Message)
	}
	if len(out.Memories) != 0 {
		t.Errorf("Memories = %+v, want empty after forget", out.Memories)
	}
	if _, err := h.db.GetMemory("stale-fact"); err != store.ErrMemoryNotFound {
		t.Errorf("GetMemory after chat-driven forget: err = %v, want ErrMemoryNotFound", err)
	}
}

func TestHandleMemoryChat_RequiresInstruction(t *testing.T) {
	h := newTestHarness(t, "")
	body, _ := json.Marshal(map[string]string{"instruction": "   "})
	resp, err := http.Post(h.url("/api/memories/chat"), "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", resp.StatusCode)
	}
}
