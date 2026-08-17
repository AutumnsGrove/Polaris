package gateway

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
)

func postAsk(t *testing.T, h *testHarness, req AskRequest) (*http.Response, AskResponse) {
	t.Helper()
	body, err := json.Marshal(req)
	if err != nil {
		t.Fatalf("marshaling AskRequest: %v", err)
	}
	resp, err := http.Post(h.url("/api/ask"), "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("POST /api/ask: %v", err)
	}
	defer resp.Body.Close()

	var out AskResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatalf("decoding AskResponse: %v", err)
	}
	return resp, out
}

// TestHandleAsk_NewThread_PersistsSameAsChatTurn is the core guarantee
// this endpoint exists for: a caller hitting /api/ask synchronously ends
// up with the exact same thread/messages/source-tagged row a WebSocket
// chat turn would have produced — the only thing that's different is
// that the caller gets one blocking JSON response instead of a live
// stream of events.
func TestHandleAsk_NewThread_PersistsSameAsChatTurn(t *testing.T) {
	srv := fakeLLMServer(t, "any", "The capital of France is Paris.")
	h := newTestHarness(t, srv.URL)

	resp, out := postAsk(t, h, AskRequest{Content: "what is the capital of france", Model: "test-model", Source: "her-go"})

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	if out.Answer != "The capital of France is Paris." {
		t.Errorf("Answer = %q, want the model's full answer", out.Answer)
	}
	if out.ThreadID == "" {
		t.Fatal("ThreadID is empty, want a generated thread id")
	}
	if out.CostUSD <= 0 {
		t.Errorf("CostUSD = %v, want > 0 (answer + suggestions + title generation all cost something)", out.CostUSD)
	}
	if out.DurationMs <= 0 {
		t.Errorf("DurationMs = %v, want > 0 — an API caller needs this the same as a WebSocket client does", out.DurationMs)
	}
	if out.Title == "" {
		t.Error("Title is empty, want the thread's current title returned directly instead of requiring a follow-up GET /api/threads/{id}")
	}

	thread, err := h.db.GetThread(out.ThreadID)
	if err != nil {
		t.Fatalf("GetThread(%q): %v", out.ThreadID, err)
	}
	if thread.Source != "her-go" {
		t.Errorf("thread.Source = %q, want %q", thread.Source, "her-go")
	}

	msgs, err := h.db.GetMessages(out.ThreadID)
	if err != nil {
		t.Fatalf("GetMessages: %v", err)
	}
	if len(msgs) != 2 || msgs[0].Role != "user" || msgs[1].Content != "The capital of France is Paris." {
		t.Fatalf("messages = %+v, want the user question + the persisted answer", msgs)
	}
}

// TestHandleAsk_OmittedSource_DefaultsToWeb mirrors handleTurn's
// WebSocket behavior: a caller that doesn't set Source gets tagged the
// same as the normal chat UI, not left blank.
func TestHandleAsk_OmittedSource_DefaultsToWeb(t *testing.T) {
	srv := fakeLLMServer(t, "any", "answer")
	h := newTestHarness(t, srv.URL)

	_, out := postAsk(t, h, AskRequest{Content: "a question", Model: "test-model"})

	thread, err := h.db.GetThread(out.ThreadID)
	if err != nil {
		t.Fatalf("GetThread(%q): %v", out.ThreadID, err)
	}
	if thread.Source != "web" {
		t.Errorf("thread.Source = %q, want %q", thread.Source, "web")
	}
}

func TestHandleAsk_EmptyContent_ReturnsBadRequest(t *testing.T) {
	h := newTestHarness(t, "http://127.0.0.1:1")

	resp, err := http.Post(h.url("/api/ask"), "application/json", bytes.NewReader([]byte(`{"content":""}`)))
	if err != nil {
		t.Fatalf("POST /api/ask: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", resp.StatusCode)
	}
}

// TestHandleAsk_ContinuingThread_ReusesExistingThreadID exercises the
// "second question in the same conversation" path — thread_id set means
// no new thread/source tagging happens, the turn just appends to history.
func TestHandleAsk_ContinuingThread_ReusesExistingThreadID(t *testing.T) {
	srv := fakeLLMServer(t, "any", "answer")
	h := newTestHarness(t, srv.URL)

	_, first := postAsk(t, h, AskRequest{Content: "first question", Model: "test-model", Source: "her-go"})
	_, second := postAsk(t, h, AskRequest{Content: "second question", Model: "test-model", ThreadID: first.ThreadID})

	if second.ThreadID != first.ThreadID {
		t.Errorf("second.ThreadID = %q, want it to match the first turn's %q", second.ThreadID, first.ThreadID)
	}

	msgs, err := h.db.GetMessages(first.ThreadID)
	if err != nil {
		t.Fatalf("GetMessages: %v", err)
	}
	if len(msgs) != 4 {
		t.Fatalf("got %d messages after two turns, want 4", len(msgs))
	}
}

// commentaryThenAnswerServer fakes a model that talks before calling a
// tool ("Let me check that.") and only then gives the real final answer —
// the same shape reasoningToolServer (turn_reasoning_test.go) uses, minus
// the reasoning bursts, since this is specifically about the plain-content
// case.
func commentaryThenAnswerServer(t *testing.T) *httptest.Server {
	t.Helper()
	var reqCount int32

	round1 := []string{
		`data: {"choices":[{"delta":{"content":"Let me check that."}}]}`,
		`data: {"choices":[{"delta":{"tool_calls":[{"index":0,"id":"call_1","type":"function","function":{"name":"think","arguments":"{\"thought\":\"noted internally\"}"}}]}}]}`,
		`data: {"choices":[{"delta":{},"finish_reason":"tool_calls"}],"usage":{"cost":0.0001}}`,
		`data: [DONE]`,
	}
	round2 := []string{
		`data: {"choices":[{"delta":{"content":"The real answer."}}]}`,
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

// TestHandleAsk_CommentaryDoesNotLeakIntoAnswer guards a real bug: content
// the model streamed before deciding to call a tool ("commentary" — see
// ServerEvent's doc comment in protocol.go) used to get permanently glued
// onto the front of AskResponse.Answer, because handleAsk's token
// accumulator had no equivalent of the WebSocket frontend's "clear on
// commentary" handling in state.svelte.ts. Confirmed as a real,
// user-observed symptom (not just theoretical) before this fix landed.
func TestHandleAsk_CommentaryDoesNotLeakIntoAnswer(t *testing.T) {
	srv := commentaryThenAnswerServer(t)
	h := newTestHarness(t, srv.URL)

	_, out := postAsk(t, h, AskRequest{Content: "check something for me", Model: "test-model"})

	if out.Answer != "The real answer." {
		t.Errorf("Answer = %q, want %q (commentary must not leak into it)", out.Answer, "The real answer.")
	}
}

func TestHandleAskStream_HappyPath(t *testing.T) {
	srv := fakeLLMServer(t, "any", "streamed answer")
	h := newTestHarness(t, srv.URL)

	resp, err := http.Post(h.url("/api/ask/stream"), "application/json",
		bytes.NewReader([]byte(`{"content":"a question","model":"test-model"}`)))
	if err != nil {
		t.Fatalf("POST /api/ask/stream: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); ct != "application/x-ndjson" {
		t.Errorf("Content-Type = %q, want application/x-ndjson", ct)
	}

	var sawToken, sawDone bool
	var doneThreadID string
	scanner := bufio.NewScanner(resp.Body)
	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			continue
		}
		var evt ServerEvent
		if err := json.Unmarshal([]byte(line), &evt); err != nil {
			t.Fatalf("unmarshaling NDJSON line %q: %v", line, err)
		}
		switch evt.Type {
		case "token":
			sawToken = true
		case "done":
			sawDone = true
			doneThreadID = evt.ThreadID
		}
	}
	if err := scanner.Err(); err != nil {
		t.Fatalf("reading NDJSON stream: %v", err)
	}

	if !sawToken {
		t.Error("expected at least one \"token\" event in the stream")
	}
	if !sawDone {
		t.Error("expected a \"done\" event to close the stream")
	}
	if doneThreadID == "" {
		t.Error("\"done\" event's thread_id is empty, want a generated thread id")
	}
}

func TestHandleAskStream_EmptyContent_ReturnsBadRequest(t *testing.T) {
	h := newTestHarness(t, "http://127.0.0.1:1")

	resp, err := http.Post(h.url("/api/ask/stream"), "application/json", bytes.NewReader([]byte(`{"content":""}`)))
	if err != nil {
		t.Fatalf("POST /api/ask/stream: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", resp.StatusCode)
	}
}
