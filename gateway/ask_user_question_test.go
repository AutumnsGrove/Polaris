package gateway

import (
	"encoding/json"
	"testing"
	"time"

	"polaris/tools"
)

// TestWebSocket_AskUserQuestionEndsTurnAndPersists exercises the whole
// redesigned path end to end: the tool call ends the turn instead of
// blocking, the "done" event carries the pending question so a live
// session can render it immediately, the assistant message persists it
// to the database (so it survives a reload/restart), and — since
// suggesting follow-ups to a question the assistant just asked makes no
// sense — no "suggestions" event ever follows.
func TestWebSocket_AskUserQuestionEndsTurnAndPersists(t *testing.T) {
	// sseToolCallServer's round2 must never actually be reached — Run
	// ends the turn right after dispatching ask_user_question (see
	// agent.Run's PendingQuestion check), so the fake model is never
	// asked for a second completion.
	srv := sseToolCallServer(t, []string{
		`{"index":0,"id":"call_1","type":"function","function":{"name":"ask_user_question",` +
			`"arguments":"{\"question\":\"What's your current location?\",\"wants_location\":true}"}}`,
	})
	defer srv.Close()
	h := newTestHarness(t, srv.URL)
	conn := dialWS(t, h)

	if err := conn.WriteJSON(map[string]interface{}{
		"type": "message", "content": "find me a coffee shop", "model": "test-model",
	}); err != nil {
		t.Fatalf("WriteJSON: %v", err)
	}

	events := readEventsUntilDone(t, conn, 5*time.Second)

	var done map[string]interface{}
	for _, e := range events {
		if e["type"] == "done" {
			done = e
		}
		if e["type"] == "suggestions" {
			t.Fatalf("got a suggestions event, want none after a turn-ending question: %+v", e)
		}
	}
	if done == nil {
		t.Fatalf("never saw a done event: %+v", events)
	}

	pq, ok := done["pending_question"].(map[string]interface{})
	if !ok {
		t.Fatalf("done event's pending_question = %v, want an object", done["pending_question"])
	}
	if pq["question"] != "What's your current location?" {
		t.Errorf("pending_question.question = %v", pq["question"])
	}
	if pq["wants_location"] != true {
		t.Errorf("pending_question.wants_location = %v, want true", pq["wants_location"])
	}

	threadID, _ := done["thread_id"].(string)
	msgs, err := h.db.GetMessages(threadID)
	if err != nil {
		t.Fatalf("GetMessages: %v", err)
	}
	var assistant *struct {
		Content         string
		PendingQuestion string
	}
	for _, m := range msgs {
		if m.Role == "assistant" {
			assistant = &struct {
				Content         string
				PendingQuestion string
			}{m.Content, m.PendingQuestion}
		}
	}
	if assistant == nil {
		t.Fatalf("no persisted assistant message: %+v", msgs)
	}
	if assistant.Content != "What's your current location?" {
		t.Errorf("persisted assistant content = %q, want the literal question", assistant.Content)
	}
	if assistant.PendingQuestion == "" {
		t.Fatal("persisted pending_question column is empty")
	}
	var stored tools.PendingQuestion
	if err := json.Unmarshal([]byte(assistant.PendingQuestion), &stored); err != nil {
		t.Fatalf("unmarshaling persisted pending_question: %v", err)
	}
	if !stored.WantsLocation {
		t.Error("persisted pending_question.WantsLocation = false, want true")
	}

	// Confirm the suggestions goroutine really was skipped, not just slow
	// to arrive — bounded wait for one more frame; a timeout here IS the
	// pass condition.
	conn.SetReadDeadline(time.Now().Add(300 * time.Millisecond))
	var extra map[string]interface{}
	if err := conn.ReadJSON(&extra); err == nil {
		t.Fatalf("got an unexpected extra event after done: %+v", extra)
	}
}
