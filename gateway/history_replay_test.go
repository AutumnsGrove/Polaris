package gateway

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"polaris/llm"
)

// seedResearchedThread builds a two-turn thread from before transcripts
// existed (no messages.transcript) whose first turn ran a web_search and a
// web_read, logged exactly the way logTurnEvent does — the legacy shape
// loadHistory still has to rebuild.
func seedResearchedThread(t *testing.T, h *testHarness) *Server {
	t.Helper()
	if err := h.db.CreateThread("t1", "Thread", "test-model", "web"); err != nil {
		t.Fatalf("CreateThread: %v", err)
	}
	add := func(role, content, citations, turnID string) {
		if _, err := h.db.AddMessage("t1", role, content, citations, "[]", 0, turnID); err != nil {
			t.Fatalf("AddMessage: %v", err)
		}
	}
	add("user", "what is kimi k2.8?", "[]", "turn1")
	h.db.LogEvent("t1", "info", "tool.web_search", "tool call started", map[string]interface{}{"args": map[string]interface{}{"query": "kimi k2.8"}, "call_id": "functions.web_search:0"}, "turn1")
	h.db.LogEvent("t1", "info", "tool.web_read", "tool call started", map[string]interface{}{"args": map[string]interface{}{"url": "https://example.com/k28"}, "call_id": "functions.web_read:1"}, "turn1")
	// Out of call order, as concurrent dispatch can finish them.
	h.db.LogEvent("t1", "info", "tool.web_read", "tool call finished", map[string]interface{}{"result": "K2.8 costs $0.60/M input", "call_id": "functions.web_read:1"}, "turn1")
	h.db.LogEvent("t1", "info", "tool.web_search", "tool call finished", map[string]interface{}{"result": "1. Kimi K2.8 announced", "call_id": "functions.web_search:0"}, "turn1")
	// A call whose result never got logged must be dropped, not replayed
	// as a dangling tool_calls message.
	h.db.LogEvent("t1", "info", "tool.web_read", "tool call started", map[string]interface{}{"args": map[string]interface{}{"url": "https://example.com/crashed"}, "call_id": "x"}, "turn1")
	add("assistant", "Kimi K2.8 is Moonshot's model.", `[{"title":"K2.8 launch","url":"https://example.com/k28"}]`, "turn1")
	add("user", "how does its pricing compare to k3?", "[]", "turn2")
	add("assistant", "It's cheaper.", "[]", "turn2")
	return &Server{db: h.db}
}

func TestLoadHistory_LegacyTurnReplaysCallsBeforeTheirAnswer(t *testing.T) {
	h := newTestHarness(t, "http://127.0.0.1:1")
	s := seedResearchedThread(t, h)

	history, err := s.loadHistory("t1")
	if err != nil {
		t.Fatalf("loadHistory: %v", err)
	}
	// user, (call+result)x2, answer, user, answer
	if len(history) != 8 {
		t.Fatalf("got %d messages, want 8: %+v", len(history), history)
	}
	if history[0].Role != "user" {
		t.Errorf("history[0] = %+v, want the first question", history[0])
	}
	search, searchResult := history[1], history[2]
	if len(search.ToolCalls) != 1 || search.ToolCalls[0].Function.Name != "web_search" || search.ToolCalls[0].Function.Arguments != `{"query":"kimi k2.8"}` {
		t.Errorf("history[1] = %+v, want the web_search call first, in call order", search)
	}
	if searchResult.Role != "tool" || searchResult.ToolCallID != search.ToolCalls[0].ID || searchResult.Content != "1. Kimi K2.8 announced" {
		t.Errorf("history[2] = %+v, want web_search's own result paired to its call", searchResult)
	}
	read, readResult := history[3], history[4]
	if read.ToolCalls[0].Function.Name != "web_read" || readResult.Content != "K2.8 costs $0.60/M input" || readResult.ToolCallID != read.ToolCalls[0].ID {
		t.Errorf("history[3:5] = %+v %+v, want web_read paired with its own result", read, readResult)
	}
	if search.ToolCalls[0].ID == read.ToolCalls[0].ID {
		t.Error("replayed calls share an id, want each unique")
	}
	if history[5].Role != "assistant" || !strings.HasPrefix(history[5].Content, "Kimi K2.8 is Moonshot's model.") {
		t.Errorf("history[5] = %+v, want turn 1's answer right after its calls", history[5])
	}
	for _, m := range history {
		if strings.Contains(m.Content, "crashed") || (len(m.ToolCalls) > 0 && strings.Contains(m.ToolCalls[0].Function.Arguments, "crashed")) {
			t.Errorf("replayed a call with no logged result: %+v", m)
		}
	}
}

// TestLoadHistory_LegacyTurnsStayIdenticalAsThreadGrows is
// the property prompt caching actually needs: an earlier version decided
// per request which turns' calls fit a budget, which meant turn 1's own
// reconstructed shape could change once turn 3 showed up competing for the
// same budget — breaking any provider's exact-prefix cache for the whole
// conversation, not just the newest turn. Unconditional replay has no
// per-request decision left to make, so the prefix covering turn 1 must be
// byte-for-byte the same before and after a new turn is appended.
func TestLoadHistory_LegacyTurnsStayIdenticalAsThreadGrows(t *testing.T) {
	h := newTestHarness(t, "http://127.0.0.1:1")
	s := seedResearchedThread(t, h)

	before, err := s.loadHistory("t1")
	if err != nil {
		t.Fatalf("loadHistory (before): %v", err)
	}
	// turn1's slice: user, (call+result)x2, answer — everything before
	// turn2's own "how does its pricing compare" question.
	turn1Before := before[:6]

	if _, err := h.db.AddMessage("t1", "user", "and after that?", "[]", "[]", 0, "turn3"); err != nil {
		t.Fatalf("AddMessage: %v", err)
	}
	h.db.LogEvent("t1", "info", "tool.web_search", "tool call started", map[string]interface{}{"args": map[string]interface{}{"query": "a third, much larger turn"}, "call_id": "c"}, "turn3")
	h.db.LogEvent("t1", "info", "tool.web_search", "tool call finished", map[string]interface{}{"result": strings.Repeat("x", 50_000), "call_id": "c"}, "turn3")
	if _, err := h.db.AddMessage("t1", "assistant", "sure", "[]", "[]", 0, "turn3"); err != nil {
		t.Fatalf("AddMessage: %v", err)
	}

	after, err := s.loadHistory("t1")
	if err != nil {
		t.Fatalf("loadHistory (after): %v", err)
	}
	turn1After := after[:6]

	if len(turn1Before) != len(turn1After) {
		t.Fatalf("turn 1's reconstructed shape changed length: %d before, %d after", len(turn1Before), len(turn1After))
	}
	for i := range turn1Before {
		if !reflect.DeepEqual(turn1Before[i], turn1After[i]) {
			t.Errorf("turn 1 entry %d changed once turn 3 was added:\nbefore: %+v\nafter:  %+v", i, turn1Before[i], turn1After[i])
		}
	}
}

// A turn with a stored transcript replays it as-is — the model-facing user
// message it opens with stands in for the bare stored one, and its tool
// round keeps the provider's own call ids and batching — while an older
// turn in the same thread still gets the legacy rebuild.
func TestLoadHistory_ReplaysStoredTranscriptVerbatim(t *testing.T) {
	h := newTestHarness(t, "http://127.0.0.1:1")
	s := seedResearchedThread(t, h)

	turn3 := []llm.ChatMessage{
		{Role: "user", Content: "and the context window?\n\n[Attached file: spec.pdf]"},
		{Role: "assistant", Content: "Checking both.", ToolCalls: []llm.ToolCall{
			{ID: "functions.web_read:0", Type: "function", Function: llm.FunctionCall{Name: "web_read", Arguments: `{"url":"https://a"}`}},
			{ID: "functions.web_read:1", Type: "function", Function: llm.FunctionCall{Name: "web_read", Arguments: `{"url":"https://b"}`}},
		}},
		{Role: "tool", ToolCallID: "functions.web_read:0", Content: strings.Repeat("a", 30_000)},
		{Role: "tool", ToolCallID: "functions.web_read:1", Content: "b"},
		{Role: "assistant", Content: "256K for both."},
	}
	if _, err := h.db.AddMessage("t1", "user", "and the context window?", "[]", "[]", 0, "turn3"); err != nil {
		t.Fatalf("AddMessage: %v", err)
	}
	id, err := h.db.AddMessage("t1", "assistant", "256K for both.", `[{"title":"A","url":"https://a"}]`, "[]", 0, "turn3")
	if err != nil {
		t.Fatalf("AddMessage: %v", err)
	}
	encoded, err := llm.EncodeTranscript(turn3)
	if err != nil {
		t.Fatalf("EncodeTranscript: %v", err)
	}
	if err := h.db.SetMessageTranscript(id, encoded); err != nil {
		t.Fatalf("SetMessageTranscript: %v", err)
	}

	history, err := s.loadHistory("t1")
	if err != nil {
		t.Fatalf("loadHistory: %v", err)
	}
	// Legacy turns 1-2 (8 messages, see the test above), then turn 3's
	// transcript and nothing else — no second copy of its user message,
	// no source note on its answer.
	if len(history) != 8+len(turn3) {
		t.Fatalf("got %d messages, want %d: %+v", len(history), 8+len(turn3), history)
	}
	want, _ := json.Marshal(turn3)
	got, _ := json.Marshal(history[8:])
	if string(got) != string(want) {
		t.Fatalf("turn 3 not replayed verbatim:\nwant %s\ngot  %s", want, got)
	}
}
