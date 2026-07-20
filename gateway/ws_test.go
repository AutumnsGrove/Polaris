package gateway

import (
	"net/http"
	"net/http/httptest"
	"strings"
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
