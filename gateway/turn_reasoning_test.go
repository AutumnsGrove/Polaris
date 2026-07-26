package gateway

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

// reasoningToolServer fakes a reasoning-capable model across two agent
// loop iterations: round 1 reasons, then calls the dependency-free
// "think" tool (so no SearXNG is needed); round 2 reasons again, then
// produces the final answer. Exercises flushReasoning's interrupt-flush
// logic in gateway/turn.go — a real reasoning burst followed by a tool
// call, followed by another reasoning burst, followed by the final
// answer — the same interleaving a reasoning model actually produces.
func reasoningToolServer(t *testing.T) *httptest.Server {
	t.Helper()
	var reqCount int32

	round1 := []string{
		`data: {"choices":[{"delta":{"reasoning":"Let me think "}}]}`,
		`data: {"choices":[{"delta":{"reasoning":"about this first."}}]}`,
		`data: {"choices":[{"delta":{"tool_calls":[{"index":0,"id":"call_1","type":"function","function":{"name":"think","arguments":"{\"thought\":\"noted internally\"}"}}]}}]}`,
		`data: {"choices":[{"delta":{},"finish_reason":"tool_calls"}],"usage":{"cost":0.0001}}`,
		`data: [DONE]`,
	}
	round2 := []string{
		`data: {"choices":[{"delta":{"reasoning":"Now I'm ready "}}]}`,
		`data: {"choices":[{"delta":{"reasoning":"to answer."}}]}`,
		`data: {"choices":[{"delta":{"content":"Final answer."}}]}`,
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

func TestHandleTurn_ReasoningBurstsPersistInOrderAroundToolCalls(t *testing.T) {
	srv := reasoningToolServer(t)
	defer srv.Close()
	h := newTestHarness(t, srv.URL)
	conn := dialWS(t, h)

	if err := conn.WriteJSON(map[string]interface{}{
		"type": "message", "content": "test reasoning persistence", "model": "test-model",
	}); err != nil {
		t.Fatalf("WriteJSON: %v", err)
	}

	events := readEventsUntilDone(t, conn, 5*time.Second)
	last := events[len(events)-1]
	if last["type"] != "done" {
		t.Fatalf("last event = %+v, want type=done", last)
	}
	threadID, _ := last["thread_id"].(string)

	dbEvents, err := h.db.ListEvents(threadID, 0)
	if err != nil {
		t.Fatalf("ListEvents: %v", err)
	}

	// Extract just the message-level sequence relevant to this check —
	// ignoring "turn started"/"turn completed"/"title"/"suggestions"
	// bookkeeping, which interleave around this core reasoning/thinking
	// sequence but aren't what's being tested here.
	var sequence []string
	var reasoningContents []string
	for _, e := range dbEvents {
		if e.Message == "reasoning" || e.Message == "thinking" {
			sequence = append(sequence, e.Message)
		}
		if e.Message == "reasoning" {
			var data struct {
				Content string `json:"content"`
			}
			if err := json.Unmarshal([]byte(e.Data), &data); err != nil {
				t.Fatalf("unmarshaling reasoning event data: %v", err)
			}
			reasoningContents = append(reasoningContents, data.Content)
		}
	}

	wantSequence := []string{"reasoning", "thinking", "reasoning"}
	if len(sequence) != len(wantSequence) {
		t.Fatalf("event sequence = %v, want %v", sequence, wantSequence)
	}
	for i, want := range wantSequence {
		if sequence[i] != want {
			t.Errorf("sequence[%d] = %q, want %q (full sequence: %v)", i, sequence[i], want, sequence)
		}
	}

	if len(reasoningContents) != 2 {
		t.Fatalf("got %d reasoning events, want 2 (one burst before the tool call, one after)", len(reasoningContents))
	}
	if reasoningContents[0] != "Let me think about this first." {
		t.Errorf("first reasoning burst = %q, want the two chunks joined, not split into separate rows", reasoningContents[0])
	}
	if reasoningContents[1] != "Now I'm ready to answer." {
		t.Errorf("second reasoning burst = %q, want the two chunks joined", reasoningContents[1])
	}
}
