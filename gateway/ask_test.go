package gateway

import (
	"bytes"
	"encoding/json"
	"net/http"
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
