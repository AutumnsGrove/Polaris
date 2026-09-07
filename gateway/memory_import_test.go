package gateway

import (
	"bytes"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"polaris/prompts"
	"polaris/store"
)

// textSSEBody is toolCallSSEBody's counterpart for a plain-prose reply
// (no tool call) — the shape runMemoryToolLoop's loop treats as "done".
func textSSEBody(content string) string {
	return strings.Join([]string{
		`data: {"choices":[{"delta":{"content":` + mustJSON(content) + `}}]}`,
		`data: {"choices":[{"delta":{},"finish_reason":"stop"}],"usage":{"cost":0.0001}}`,
		`data: [DONE]`,
	}, "\n") + "\n"
}

func mustJSON(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}

func TestHandleMemoryExportPrompt(t *testing.T) {
	h := newTestHarness(t, "")
	resp, err := http.Get(h.url("/api/memories/export-prompt"))
	if err != nil {
		t.Fatalf("GET /api/memories/export-prompt: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	var out struct {
		Prompt string `json:"prompt"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatalf("decoding response: %v", err)
	}
	if out.Prompt != prompts.Get().Turn.MemoryExportPrompt {
		t.Errorf("Prompt = %q, want the configured memory_export_prompt", out.Prompt)
	}
	if !strings.Contains(out.Prompt, "Categories") {
		t.Errorf("Prompt = %q, want it to look like the claude.ai-style export prompt", out.Prompt)
	}
}

func TestHandleMemoryImport_RequiresDump(t *testing.T) {
	h := newTestHarness(t, "")
	body, _ := json.Marshal(map[string]string{"dump": "   "})
	resp, err := http.Post(h.url("/api/memories/import"), "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", resp.StatusCode)
	}
}

// TestHandleMemoryImport_WritesMultipleFactsAndReturnsSummary drives a fake
// model through two separate write calls (one per round — sequencedSSEServer
// hands out a different body per top-level request, matching how
// runMemoryToolLoop's loop makes one ChatCompletionWithTools call per
// round) before it finally answers in plain text, exercising the exact
// multi-round shape a real dump needs: several tool calls, not just the
// single-round case memoryChatLLMServer already covers for handleMemoryChat.
func TestHandleMemoryImport_WritesMultipleFactsAndReturnsSummary(t *testing.T) {
	srv := sequencedSSEServer(t, []string{
		toolCallSSEBody(`{"index":0,"id":"call_1","type":"function","function":{"name":"memory",` +
			`"arguments":"{\"action\":\"write\",\"name\":\"user-role\",\"type\":\"user\",\"description\":\"backend engineer\",\"content\":\"backend engineer\"}"}}`),
		toolCallSSEBody(`{"index":0,"id":"call_2","type":"function","function":{"name":"memory",` +
			`"arguments":"{\"action\":\"write\",\"name\":\"user-timezone\",\"type\":\"user\",\"description\":\"US/Pacific\",\"content\":\"US/Pacific\",\"occurred_at\":\"2026-01-15\"}"}}`),
		textSSEBody("Imported 2 memories: your role and your timezone."),
	})
	defer srv.Close()
	h := newTestHarness(t, srv.URL)

	body, _ := json.Marshal(map[string]string{"dump": "## Identity\n[2026-01-15] - US/Pacific timezone\n[unknown] - backend engineer"})
	resp, err := http.Post(h.url("/api/memories/import"), "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("POST /api/memories/import: %v", err)
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
	if out.Message != "Imported 2 memories: your role and your timezone." {
		t.Errorf("Message = %q", out.Message)
	}
	if len(out.Memories) != 2 {
		t.Fatalf("Memories = %+v, want 2 written rows", out.Memories)
	}

	m, err := h.db.GetMemory("user-timezone")
	if err != nil {
		t.Fatalf("GetMemory: %v", err)
	}
	if m.OccurredAt != "2026-01-15" {
		t.Errorf("OccurredAt = %q, want the imported date preserved", m.OccurredAt)
	}
}
