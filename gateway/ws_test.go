package gateway

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

// dialWS connects to the harness's /ws endpoint over a real TCP
// connection (httptest.Server actually listens), exercising the real
// handleWS/handleTurn goroutine plumbing end-to-end rather than calling
// handleTurn directly.
func dialWS(t *testing.T, h *testHarness) *websocket.Conn {
	t.Helper()
	wsURL := "ws" + strings.TrimPrefix(h.srv.URL, "http") + "/ws"
	conn, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatalf("dialing /ws: %v", err)
	}
	t.Cleanup(func() { conn.Close() })
	return conn
}

func readEventsUntilDone(t *testing.T, conn *websocket.Conn, timeout time.Duration) []map[string]interface{} {
	t.Helper()
	conn.SetReadDeadline(time.Now().Add(timeout))
	var events []map[string]interface{}
	for {
		var evt map[string]interface{}
		if err := conn.ReadJSON(&evt); err != nil {
			t.Fatalf("reading event: %v (events so far: %+v)", err, events)
		}
		events = append(events, evt)
		if evt["type"] == "done" || evt["type"] == "error" {
			return events
		}
	}
}

func TestWebSocket_FullTurn_HappyPath(t *testing.T) {
	srv := fakeLLMServer(t, "any", "The capital of France is Paris.")
	h := newTestHarness(t, srv.URL)
	conn := dialWS(t, h)

	if err := conn.WriteJSON(map[string]interface{}{
		"type": "message", "content": "what is the capital of france", "model": "test-model",
	}); err != nil {
		t.Fatalf("WriteJSON: %v", err)
	}

	events := readEventsUntilDone(t, conn, 5*time.Second)

	var threadID string
	sawUserMessage := false
	for _, e := range events {
		if e["type"] == "user_message" {
			sawUserMessage = true
			threadID, _ = e["thread_id"].(string)
		}
	}
	if !sawUserMessage {
		t.Fatalf("never saw a user_message event: %+v", events)
	}

	last := events[len(events)-1]
	if last["type"] != "done" {
		t.Fatalf("last event = %+v, want type=done", last)
	}
	if cost, _ := last["cost_usd"].(float64); cost <= 0 {
		t.Errorf("cost_usd = %v, want > 0 (answer + suggestions + title generation all cost something)", last["cost_usd"])
	}

	// The thread must exist, with an LLM-generated title (not just the
	// truncated raw question) since this was its first and only turn.
	thread, err := h.db.GetThread(threadID)
	if err != nil {
		t.Fatalf("GetThread(%q): %v", threadID, err)
	}
	if thread.Title == "what is the capital of france" {
		t.Errorf("title = %q, want the LLM-generated title, not the raw fallback", thread.Title)
	}

	msgs, err := h.db.GetMessages(threadID)
	if err != nil {
		t.Fatalf("GetMessages: %v", err)
	}
	if len(msgs) != 2 || msgs[1].Content != "The capital of France is Paris." {
		t.Fatalf("messages = %+v, want the user question + the persisted answer", msgs)
	}

	// Durable event trail: at minimum a start and a completion.
	dbEvents, err := h.db.ListEvents(threadID, 0)
	if err != nil {
		t.Fatalf("ListEvents: %v", err)
	}
	var sawStarted, sawCompleted bool
	for _, e := range dbEvents {
		if e.Message == "turn started" {
			sawStarted = true
		}
		if e.Message == "turn completed" {
			sawCompleted = true
		}
	}
	if !sawStarted || !sawCompleted {
		t.Errorf("dbEvents = %+v, want both \"turn started\" and \"turn completed\"", dbEvents)
	}
}

func TestWebSocket_SecondTurnDoesNotRegenerateTitle(t *testing.T) {
	srv := fakeLLMServer(t, "any", "an answer")
	h := newTestHarness(t, srv.URL)
	conn := dialWS(t, h)

	if err := conn.WriteJSON(map[string]interface{}{"type": "message", "content": "first question", "model": "test-model"}); err != nil {
		t.Fatalf("WriteJSON: %v", err)
	}
	events := readEventsUntilDone(t, conn, 5*time.Second)
	threadID, _ := events[len(events)-1]["thread_id"].(string)

	thread, err := h.db.GetThread(threadID)
	if err != nil {
		t.Fatalf("GetThread: %v", err)
	}
	titleAfterFirstTurn := thread.Title

	if err := conn.WriteJSON(map[string]interface{}{
		"type": "message", "thread_id": threadID, "content": "a follow-up question", "model": "test-model",
	}); err != nil {
		t.Fatalf("WriteJSON (second turn): %v", err)
	}
	readEventsUntilDone(t, conn, 5*time.Second)

	thread, err = h.db.GetThread(threadID)
	if err != nil {
		t.Fatalf("GetThread after second turn: %v", err)
	}
	if thread.Title != titleAfterFirstTurn {
		t.Errorf("title changed after the second turn: %q -> %q, want it untouched", titleAfterFirstTurn, thread.Title)
	}
}

func TestWebSocket_LLMErrorSurfacesAsErrorEvent(t *testing.T) {
	badSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		w.Write([]byte(`{"error":{"message":"bad key"}}`))
	}))
	defer badSrv.Close()

	h := newTestHarness(t, badSrv.URL)
	conn := dialWS(t, h)

	if err := conn.WriteJSON(map[string]interface{}{"type": "message", "content": "hi", "model": "test-model"}); err != nil {
		t.Fatalf("WriteJSON: %v", err)
	}
	events := readEventsUntilDone(t, conn, 5*time.Second)
	last := events[len(events)-1]
	if last["type"] != "error" {
		t.Fatalf("last event = %+v, want type=error for a failed LLM call", last)
	}

	// The turn failure must be visible in the durable event trail too.
	threadID, _ := last["thread_id"].(string)
	dbEvents, err := h.db.ListEvents(threadID, 0)
	if err != nil {
		t.Fatalf("ListEvents: %v", err)
	}
	found := false
	for _, e := range dbEvents {
		if e.Level == "error" && e.Message == "turn failed" {
			found = true
		}
	}
	if !found {
		t.Errorf("dbEvents = %+v, want a \"turn failed\" error event", dbEvents)
	}
}

// TestWebSocket_EmptyAnswerSurfacesAsErrorEvent guards against a real
// bug: a reasoning model that spends its whole completion budget on
// hidden reasoning tokens can return empty visible content with no
// error at all (see generateTitle's doc comment for the concrete case
// that surfaced this pattern). Left unchecked in the primary answer
// path, this used to fall straight through to AddMessage and silently
// persist a blank assistant turn — no error event, no log trace.
func TestWebSocket_EmptyAnswerSurfacesAsErrorEvent(t *testing.T) {
	srv := fakeLLMServer(t, "any", "")
	h := newTestHarness(t, srv.URL)
	conn := dialWS(t, h)

	if err := conn.WriteJSON(map[string]interface{}{"type": "message", "content": "hi", "model": "test-model"}); err != nil {
		t.Fatalf("WriteJSON: %v", err)
	}
	events := readEventsUntilDone(t, conn, 5*time.Second)
	last := events[len(events)-1]
	if last["type"] != "error" {
		t.Fatalf("last event = %+v, want type=error for an empty answer", last)
	}

	threadID, _ := last["thread_id"].(string)
	msgs, err := h.db.GetMessages(threadID)
	if err != nil {
		t.Fatalf("GetMessages: %v", err)
	}
	if len(msgs) != 1 {
		t.Errorf("messages = %+v, want only the user question — no blank assistant turn persisted", msgs)
	}

	dbEvents, err := h.db.ListEvents(threadID, 0)
	if err != nil {
		t.Fatalf("ListEvents: %v", err)
	}
	found := false
	for _, e := range dbEvents {
		if e.Level == "warn" && e.Message == "model returned an empty answer" {
			found = true
		}
	}
	if !found {
		t.Errorf("dbEvents = %+v, want a \"model returned an empty answer\" warn event", dbEvents)
	}
}

// TestWebSocket_RejectsConcurrentTurnOnSameConnection guards against a
// found-in-audit bug: handleWS spawned a goroutine per incoming "message"
// with no check that a turn was already in flight on that connection, so
// a second message arriving before the first turn finished silently
// overwrote the shared cancel slot — orphaning the first turn's "stop"
// capability with no way to cancel it. The LLM server here blocks the
// first turn's call until the test releases it, so the race is
// deterministic rather than timing-dependent.
func TestWebSocket_RejectsConcurrentTurnOnSameConnection(t *testing.T) {
	release := make(chan struct{})
	var mu sync.Mutex
	first := true

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		isFirst := first
		first = false
		mu.Unlock()
		if isFirst {
			<-release
		}

		chunk, err := json.Marshal(map[string]interface{}{
			"choices": []map[string]interface{}{{"delta": map[string]interface{}{"content": "answer"}}},
		})
		if err != nil {
			t.Fatalf("marshaling fake SSE chunk: %v", err)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		flusher := w.(http.Flusher)
		fmt.Fprintf(w, "data: %s\n", chunk)
		flusher.Flush()
		fmt.Fprintf(w, "data: %s\n", `{"choices":[{"delta":{},"finish_reason":"stop"}],"usage":{"cost":0.0001}}`)
		fmt.Fprint(w, "data: [DONE]\n")
	}))
	defer srv.Close()

	h := newTestHarness(t, srv.URL)
	conn := dialWS(t, h)

	if err := conn.WriteJSON(map[string]interface{}{"type": "message", "content": "first", "model": "test-model"}); err != nil {
		t.Fatalf("WriteJSON (first): %v", err)
	}

	// Give handleWS's read loop time to actually process the first frame
	// and register the cancel slot before sending the second — WriteJSON
	// returning only means the client wrote to the socket, not that the
	// server has read it yet. The first turn is still blocked on `release`
	// regardless, so this only needs to outrun the read-loop's own
	// bookkeeping, not the LLM call.
	time.Sleep(50 * time.Millisecond)

	if err := conn.WriteJSON(map[string]interface{}{"type": "message", "content": "second", "model": "test-model"}); err != nil {
		t.Fatalf("WriteJSON (second): %v", err)
	}

	conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	rejected := false
	for !rejected {
		var evt map[string]interface{}
		if err := conn.ReadJSON(&evt); err != nil {
			t.Fatalf("reading event: %v", err)
		}
		if evt["type"] == "done" {
			t.Fatalf("first turn completed before the rejection was observed — race didn't trigger: %+v", evt)
		}
		if evt["type"] == "error" && strings.Contains(fmt.Sprint(evt["message"]), "already in progress") {
			rejected = true
		}
	}

	close(release) // let the first turn finish
	readEventsUntilDone(t, conn, 5*time.Second)
}

// TestWebSocket_EditFirstMessage_AtomicallyReplacesAndRebuildsHistory
// exercises handleTurn's retry/edit path end-to-end — regression coverage
// for the same audit finding as store's
// TestDeleteMessagesFromAndAddMessage_* tests, but for the full wiring:
// loadHistory's excludeFromID must still produce a "post-edit" view of
// history even though the actual DELETE is now deferred to the same
// transaction as the new message's INSERT (see handleTurn's comments).
// If that exclusion logic were wrong, the edit turn's LLM call would see
// the original (soon-to-be-replaced) question as history alongside the
// edited one, rather than the edited one replacing it.
func TestWebSocket_EditFirstMessage_AtomicallyReplacesAndRebuildsHistory(t *testing.T) {
	var mu sync.Mutex
	var requestBodies []string

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		mu.Lock()
		requestBodies = append(requestBodies, string(body))
		mu.Unlock()

		chunk, err := json.Marshal(map[string]interface{}{
			"choices": []map[string]interface{}{{"delta": map[string]interface{}{"content": "an answer"}}},
		})
		if err != nil {
			t.Fatalf("marshaling fake SSE chunk: %v", err)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		flusher := w.(http.Flusher)
		fmt.Fprintf(w, "data: %s\n", chunk)
		flusher.Flush()
		fmt.Fprintf(w, "data: %s\n", `{"choices":[{"delta":{},"finish_reason":"stop"}],"usage":{"cost":0.0001}}`)
		fmt.Fprint(w, "data: [DONE]\n")
	}))
	defer srv.Close()

	h := newTestHarness(t, srv.URL)
	conn := dialWS(t, h)

	if err := conn.WriteJSON(map[string]interface{}{"type": "message", "content": "original question", "model": "test-model"}); err != nil {
		t.Fatalf("WriteJSON (first): %v", err)
	}
	events := readEventsUntilDone(t, conn, 5*time.Second)
	threadID, _ := events[len(events)-1]["thread_id"].(string)

	var userMsgID float64
	for _, e := range events {
		if e["type"] == "user_message" {
			userMsgID, _ = e["user_message_id"].(float64)
		}
	}
	if userMsgID == 0 {
		t.Fatalf("never captured user_message_id: %+v", events)
	}

	// Everything before this point belongs to turn 1 (its main answer call
	// plus any post-processing like suggestions/title generation, all
	// synchronous within handleTurn before "done" fires) — a clean
	// boundary for isolating what the edit turn alone sends the LLM.
	mu.Lock()
	preEditCount := len(requestBodies)
	mu.Unlock()

	if err := conn.WriteJSON(map[string]interface{}{
		"type": "message", "thread_id": threadID, "content": "edited question",
		"model": "test-model", "edit_from_id": int64(userMsgID),
	}); err != nil {
		t.Fatalf("WriteJSON (edit): %v", err)
	}
	readEventsUntilDone(t, conn, 5*time.Second)

	msgs, err := h.db.GetMessages(threadID)
	if err != nil {
		t.Fatalf("GetMessages: %v", err)
	}
	if len(msgs) != 2 || msgs[0].Content != "edited question" || msgs[0].Role != "user" {
		t.Fatalf("messages = %+v, want exactly [edited question, an answer]", msgs)
	}

	mu.Lock()
	defer mu.Unlock()
	if len(requestBodies) <= preEditCount {
		t.Fatalf("edit turn made no LLM requests at all: %d total, %d before the edit", len(requestBodies), preEditCount)
	}
	for _, body := range requestBodies[preEditCount:] {
		if strings.Contains(body, "original question") {
			t.Errorf("an edit-turn LLM request still contained the replaced question: %s", body)
		}
	}
}

func TestWebSocket_UnknownModelFallsBackToDefault(t *testing.T) {
	srv := fakeLLMServer(t, "any", "an answer")
	h := newTestHarness(t, srv.URL)
	conn := dialWS(t, h)

	if err := conn.WriteJSON(map[string]interface{}{
		"type": "message", "content": "hi", "model": "does-not-exist",
	}); err != nil {
		t.Fatalf("WriteJSON: %v", err)
	}
	events := readEventsUntilDone(t, conn, 5*time.Second)
	last := events[len(events)-1]
	// config.ModelByID falls back to the default model rather than
	// erroring on an unrecognized id — the turn should still complete.
	if last["type"] != "done" {
		t.Errorf("last event = %+v, want a normal completion despite the unknown model id", last)
	}
}
