package gateway

import (
	"strings"
	"testing"
)

// seedResearchedThread builds a two-turn thread whose first turn ran a
// web_search and a web_read, logged exactly the way logTurnEvent does.
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

func TestLoadHistoryWithToolResults_ReplaysCallsBeforeTheirAnswer(t *testing.T) {
	h := newTestHarness(t, "http://127.0.0.1:1")
	s := seedResearchedThread(t, h)

	history, err := s.loadHistoryWithToolResults("t1", 1_000_000)
	if err != nil {
		t.Fatalf("loadHistoryWithToolResults: %v", err)
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

func TestLoadHistoryWithToolResults_OverBudgetFallsBackToAnswersOnly(t *testing.T) {
	h := newTestHarness(t, "http://127.0.0.1:1")
	s := seedResearchedThread(t, h)

	history, err := s.loadHistoryWithToolResults("t1", 10)
	if err != nil {
		t.Fatalf("loadHistoryWithToolResults: %v", err)
	}
	if len(history) != 4 {
		t.Fatalf("got %d messages, want 4 (no replayed calls): %+v", len(history), history)
	}
	for _, m := range history {
		if len(m.ToolCalls) > 0 || m.Role == "tool" {
			t.Errorf("replayed %+v despite a budget too small for it", m)
		}
	}
	// The sources note still rides along either way.
	if !strings.Contains(history[1].Content, "https://example.com/k28") {
		t.Errorf("history[1] = %q, want its sources note even without replayed results", history[1].Content)
	}
}

func TestEffectiveContextWindowTokens(t *testing.T) {
	cases := []struct {
		configured int
		full       bool
		want       int
	}{
		{100_000, false, 100_000},
		{100_000, true, fullTurnHistoryContextWindowTokens},
		{300_000, true, 300_000}, // never lowers an operator's own larger threshold
	}
	for _, c := range cases {
		if got := effectiveContextWindowTokens(c.configured, c.full); got != c.want {
			t.Errorf("effectiveContextWindowTokens(%d, %v) = %d, want %d", c.configured, c.full, got, c.want)
		}
	}
}
