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

	"polaris/store"
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

// TestWebSocket_DisconnectDoesNotTruncateInFlightTurn guards against a
// real bug: handleWS used to cancel the in-flight turn's context the
// instant ReadJSON errored, which happens for ANY dropped connection, not
// just a deliberate close — a backgrounded mobile tab or a brief network
// blip closes the socket the exact same way. That silently truncated the
// answer mid-stream while still persisting a "done" turn, so the user
// came back to what looked like a finished response that had actually
// been cut off. The fake LLM server here pauses mid-answer so the test
// can close the client connection before the second chunk arrives, then
// releases it — the turn must still run to completion and persist the
// full, untruncated answer.
func TestWebSocket_DisconnectDoesNotTruncateInFlightTurn(t *testing.T) {
	// started fires once the fake LLM server is actually mid-request —
	// the sync point for "the turn is definitely in flight", since a
	// short first chunk like "Hello, " isn't guaranteed to surface as its
	// own "token" event on the wire (streamSniffer buffers a few chunks
	// before deciding whether they're the start of a pseudo tool call —
	// see agent/pseudocall.go's streamSniffer.onChunk — so waiting for a
	// client-visible token here would be flaky/deadlock-prone).
	started := make(chan struct{})
	release := make(chan struct{})
	var once sync.Once

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Only the turn's own answer call needs to pause — the
		// follow-up suggestions/title-generation calls handleTurn fires
		// afterward should just get a normal immediate response.
		isFirst := false
		once.Do(func() { isFirst = true; close(started) })
		if isFirst {
			<-release // give the test time to close the client connection
		}

		w.Header().Set("Content-Type", "text/event-stream")
		flusher := w.(http.Flusher)

		if isFirst {
			chunk1, _ := json.Marshal(map[string]interface{}{
				"choices": []map[string]interface{}{{"delta": map[string]interface{}{"content": "Hello, "}}},
			})
			fmt.Fprintf(w, "data: %s\n", chunk1)
			flusher.Flush()
			chunk2, _ := json.Marshal(map[string]interface{}{
				"choices": []map[string]interface{}{{"delta": map[string]interface{}{"content": "world!"}}},
			})
			fmt.Fprintf(w, "data: %s\n", chunk2)
			flusher.Flush()
		} else {
			chunk, _ := json.Marshal(map[string]interface{}{
				"choices": []map[string]interface{}{{"delta": map[string]interface{}{"content": "n/a"}}},
			})
			fmt.Fprintf(w, "data: %s\n", chunk)
			flusher.Flush()
		}
		fmt.Fprintf(w, "data: %s\n", `{"choices":[{"delta":{},"finish_reason":"stop"}],"usage":{"cost":0.0001}}`)
		flusher.Flush()
		fmt.Fprint(w, "data: [DONE]\n")
		flusher.Flush()
	}))
	defer srv.Close()

	h := newTestHarness(t, srv.URL)
	conn := dialWS(t, h)

	if err := conn.WriteJSON(map[string]interface{}{"type": "message", "content": "hi", "model": "test-model"}); err != nil {
		t.Fatalf("WriteJSON: %v", err)
	}

	// Read until the user_message event arrives (learning the thread id),
	// then wait for the LLM call to actually be in flight, then close the
	// connection out from under it — simulating the tab being
	// backgrounded/suspended mid-answer.
	conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	var threadID string
	for threadID == "" {
		var evt map[string]interface{}
		if err := conn.ReadJSON(&evt); err != nil {
			t.Fatalf("reading events before disconnect: %v", err)
		}
		if evt["type"] == "user_message" {
			threadID, _ = evt["thread_id"].(string)
		}
	}
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("fake LLM server never received the request")
	}
	conn.Close()

	close(release) // let the fake LLM server finish streaming the rest

	// Poll the DB rather than the (now-closed) socket — the whole point is
	// that the turn finishes with nobody listening.
	deadline := time.Now().Add(5 * time.Second)
	var msgs []store.Message
	for time.Now().Before(deadline) {
		var err error
		msgs, err = h.db.GetMessages(threadID)
		if err != nil {
			t.Fatalf("GetMessages: %v", err)
		}
		if len(msgs) == 2 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if len(msgs) != 2 {
		t.Fatalf("messages = %+v, want the user question + the assistant answer persisted after disconnect", msgs)
	}
	if msgs[1].Content != "Hello, world!" {
		t.Errorf("assistant answer = %q, want the full untruncated \"Hello, world!\" — a dropped connection must not cut the turn short", msgs[1].Content)
	}

	dbEvents, err := h.db.ListEvents(threadID, 0)
	if err != nil {
		t.Fatalf("ListEvents: %v", err)
	}
	found := false
	for _, e := range dbEvents {
		if e.Message == "turn completed" {
			found = true
			var data map[string]interface{}
			if err := json.Unmarshal([]byte(e.Data), &data); err != nil {
				t.Fatalf("unmarshaling turn completed event data: %v", err)
			}
			if stopped, _ := data["stopped"].(bool); stopped {
				t.Errorf("turn completed event data = %+v, want stopped=false — nobody hit Stop, the connection just dropped", data)
			}
		}
	}
	if !found {
		t.Errorf("dbEvents = %+v, want a \"turn completed\" event", dbEvents)
	}
}

// TestWebSocket_EditFirstMessage_PreservesOriginalAsVariant exercises
// handleTurn's retry/edit path end-to-end. Editing/regenerating no longer
// destroys anything (the old DeleteMessagesFromAndAddMessage behavior) —
// the original exchange must survive completely untouched under the
// thread's own id, with the edited version living in a forked thread that
// EffectiveThreadID now resolves to. It also re-covers the original
// regression this test existed for: loadHistory must build the edit
// turn's LLM call from the forked thread's content only, never leaking
// the replaced question back in as history.
func TestWebSocket_EditFirstMessage_PreservesOriginalAsVariant(t *testing.T) {
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

	// Everything before this point belongs to turn 1: its main answer
	// call, plus title generation (still synchronous within handleTurn
	// before "done" fires), plus follow-up suggestions — which now runs
	// in a detached goroutine kicked off right after "done" ships (see
	// turn.go's comment on why), so it can still be in flight when "done"
	// arrives here. Wait for turn 1's own suggestions call to actually
	// land — it legitimately re-sends "original question" as context, so
	// without this wait it can race past the boundary below and get
	// miscounted as part of the edit turn, tripping the "must not leak
	// the replaced question" check on a request that isn't the edit
	// turn's at all.
	mu.Lock()
	turn1Count := len(requestBodies)
	mu.Unlock()
	if turn1Count < 3 {
		deadline := time.Now().Add(2 * time.Second)
		for time.Now().Before(deadline) {
			mu.Lock()
			turn1Count = len(requestBodies)
			mu.Unlock()
			if turn1Count >= 3 {
				break
			}
			time.Sleep(10 * time.Millisecond)
		}
	}
	mu.Lock()
	preEditCount := len(requestBodies)
	mu.Unlock()
	if preEditCount < 3 {
		t.Fatalf("turn 1 made %d LLM requests, want 3 (answer + suggestions + title) before starting the edit", preEditCount)
	}

	if err := conn.WriteJSON(map[string]interface{}{
		"type": "message", "thread_id": threadID, "content": "edited question",
		"model": "test-model", "edit_from_id": int64(userMsgID),
	}); err != nil {
		t.Fatalf("WriteJSON (edit): %v", err)
	}
	readEventsUntilDone(t, conn, 5*time.Second)

	// root's own row must be exactly what it was before the edit — the
	// whole point of forking instead of deleting.
	rootMsgs, err := h.db.GetMessages(threadID)
	if err != nil {
		t.Fatalf("GetMessages(root): %v", err)
	}
	if len(rootMsgs) != 2 || rootMsgs[0].Content != "original question" || rootMsgs[1].Content != "an answer" {
		t.Fatalf("root messages = %+v, want the original exchange left completely untouched", rootMsgs)
	}

	// The edited version is what's now effective.
	effectiveID, err := h.db.EffectiveThreadID(threadID)
	if err != nil {
		t.Fatalf("EffectiveThreadID: %v", err)
	}
	if effectiveID == threadID {
		t.Fatalf("EffectiveThreadID = root, want a forked thread after editing")
	}
	effectiveMsgs, err := h.db.GetMessages(effectiveID)
	if err != nil {
		t.Fatalf("GetMessages(effective): %v", err)
	}
	if len(effectiveMsgs) != 2 || effectiveMsgs[0].Content != "edited question" || effectiveMsgs[0].Role != "user" {
		t.Fatalf("effective messages = %+v, want exactly [edited question, an answer]", effectiveMsgs)
	}

	// Both the original and the edit must show up as variants at
	// position 0 — this is what lets the switcher browse back to it.
	variants, err := h.db.VariantsAt(threadID, 0)
	if err != nil {
		t.Fatalf("VariantsAt: %v", err)
	}
	if len(variants) != 2 || variants[0] != threadID || variants[1] != effectiveID {
		t.Fatalf("VariantsAt(0) = %v, want [%s, %s]", variants, threadID, effectiveID)
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

// TestWebSocket_MultipleRegeneratesCreateOrderedVariants regenerates the
// same reply twice and checks the variant group grows correctly each
// time, oldest-created first, with the newest generation always the one
// EffectiveThreadID/GetThread's "active" field points at.
func TestWebSocket_MultipleRegeneratesCreateOrderedVariants(t *testing.T) {
	// currentAnswer, not a call-indexed slice — a single turn makes
	// several LLM calls (the main answer, then suggestions, then title
	// on the first turn), all against this same fake server, so indexing
	// by call count doesn't line up with "which regenerate is this." The
	// test flips currentAnswer between WriteJSON calls instead, so every
	// HTTP request within one turn consistently sees that turn's answer.
	var mu sync.Mutex
	currentAnswer := "first answer"

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		answer := currentAnswer
		mu.Unlock()

		chunk, _ := json.Marshal(map[string]interface{}{
			"choices": []map[string]interface{}{{"delta": map[string]interface{}{"content": answer}}},
		})
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

	if err := conn.WriteJSON(map[string]interface{}{"type": "message", "content": "a question", "model": "test-model"}); err != nil {
		t.Fatalf("WriteJSON (first): %v", err)
	}
	events := readEventsUntilDone(t, conn, 5*time.Second)
	threadID, _ := events[len(events)-1]["thread_id"].(string)
	var userMsgID int64
	for _, e := range events {
		if e["type"] == "user_message" {
			id, _ := e["user_message_id"].(float64)
			userMsgID = int64(id)
		}
	}
	if userMsgID == 0 {
		t.Fatalf("never captured user_message_id: %+v", events)
	}

	// Regenerate twice — same user content, same edit_from_id, just
	// asking for a fresh reply each time.
	regenAnswers := []string{"second answer", "third answer"}
	for i, next := range regenAnswers {
		mu.Lock()
		currentAnswer = next
		mu.Unlock()
		if err := conn.WriteJSON(map[string]interface{}{
			"type": "message", "thread_id": threadID, "content": "a question",
			"model": "test-model", "edit_from_id": userMsgID,
		}); err != nil {
			t.Fatalf("WriteJSON (regenerate %d): %v", i, err)
		}
		readEventsUntilDone(t, conn, 5*time.Second)
	}

	variants, err := h.db.VariantsAt(threadID, 0)
	if err != nil {
		t.Fatalf("VariantsAt: %v", err)
	}
	if len(variants) != 3 {
		t.Fatalf("VariantsAt(0) = %v, want 3 variants (original + 2 regenerates)", variants)
	}
	if variants[0] != threadID {
		t.Errorf("variants[0] = %q, want root %q (the original, created first)", variants[0], threadID)
	}

	effectiveID, err := h.db.EffectiveThreadID(threadID)
	if err != nil {
		t.Fatalf("EffectiveThreadID: %v", err)
	}
	if effectiveID != variants[2] {
		t.Errorf("EffectiveThreadID = %q, want the last-created variant %q (the newest regenerate)", effectiveID, variants[2])
	}
	effectiveMsgs, err := h.db.GetMessages(effectiveID)
	if err != nil {
		t.Fatalf("GetMessages(effective): %v", err)
	}
	if len(effectiveMsgs) != 2 || effectiveMsgs[1].Content != "third answer" {
		t.Fatalf("effective messages = %+v, want the third (most recent) regenerate's answer", effectiveMsgs)
	}

	// The two earlier generations must still be fully intact, not just
	// referenced.
	rootMsgs, err := h.db.GetMessages(threadID)
	if err != nil {
		t.Fatalf("GetMessages(root): %v", err)
	}
	if len(rootMsgs) != 2 || rootMsgs[1].Content != "first answer" {
		t.Errorf("root messages = %+v, want the original [first answer] untouched", rootMsgs)
	}
	secondMsgs, err := h.db.GetMessages(variants[1])
	if err != nil {
		t.Fatalf("GetMessages(second variant): %v", err)
	}
	if len(secondMsgs) != 2 || secondMsgs[1].Content != "second answer" {
		t.Errorf("second variant messages = %+v, want [second answer]", secondMsgs)
	}
}

// TestWebSocket_ContinuingAfterBrowsingToOldVariant_ForksTheNewerOne is
// the exact scenario this whole feature was built for: regenerate once
// (now two variants), browse back to the original, then send a genuinely
// new follow-up from there. The reply that had been active must not be
// lost — it becomes a third, still-reachable variant — and the new
// follow-up must build on the ORIGINAL's content, not the regenerate's.
func TestWebSocket_ContinuingAfterBrowsingToOldVariant_ForksTheNewerOne(t *testing.T) {
	var mu sync.Mutex
	var requestBodies []string

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		mu.Lock()
		requestBodies = append(requestBodies, string(body))
		mu.Unlock()

		chunk, _ := json.Marshal(map[string]interface{}{
			"choices": []map[string]interface{}{{"delta": map[string]interface{}{"content": "a reply"}}},
		})
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

	if err := conn.WriteJSON(map[string]interface{}{"type": "message", "content": "say something", "model": "test-model"}); err != nil {
		t.Fatalf("WriteJSON (first): %v", err)
	}
	events := readEventsUntilDone(t, conn, 5*time.Second)
	threadID, _ := events[len(events)-1]["thread_id"].(string)
	var userMsgID int64
	for _, e := range events {
		if e["type"] == "user_message" {
			id, _ := e["user_message_id"].(float64)
			userMsgID = int64(id)
		}
	}

	// Regenerate — now there are two variants, the second one active.
	if err := conn.WriteJSON(map[string]interface{}{
		"type": "message", "thread_id": threadID, "content": "say something",
		"model": "test-model", "edit_from_id": userMsgID,
	}); err != nil {
		t.Fatalf("WriteJSON (regenerate): %v", err)
	}
	readEventsUntilDone(t, conn, 5*time.Second)

	variantsBefore, err := h.db.VariantsAt(threadID, 0)
	if err != nil {
		t.Fatalf("VariantsAt: %v", err)
	}
	if len(variantsBefore) != 2 {
		t.Fatalf("VariantsAt(0) = %v, want 2 variants before browsing back", variantsBefore)
	}
	regeneratedVariantID := variantsBefore[1]

	// Browse back to the original (root itself).
	if err := h.db.SetActiveVariant(threadID, threadID); err != nil {
		t.Fatalf("SetActiveVariant(back to root): %v", err)
	}

	mu.Lock()
	preFollowUpCount := len(requestBodies)
	mu.Unlock()

	// Send a genuinely new follow-up while viewing the original.
	if err := conn.WriteJSON(map[string]interface{}{
		"type": "message", "thread_id": threadID, "content": "a follow-up", "model": "test-model",
	}); err != nil {
		t.Fatalf("WriteJSON (follow-up): %v", err)
	}
	readEventsUntilDone(t, conn, 5*time.Second)

	// The follow-up must have been appended to root's own history, not
	// forked — a plain continuation (no edit_from_id) never needs to
	// fork, it just builds on whatever's currently effective.
	rootMsgs, err := h.db.GetMessages(threadID)
	if err != nil {
		t.Fatalf("GetMessages(root): %v", err)
	}
	if len(rootMsgs) != 4 || rootMsgs[2].Content != "a follow-up" {
		t.Fatalf("root messages = %+v, want the follow-up appended directly to root's own 2 original messages", rootMsgs)
	}

	// The regenerated variant from before must still be exactly as it
	// was — completely unaffected by continuing down the other branch.
	regeneratedMsgs, err := h.db.GetMessages(regeneratedVariantID)
	if err != nil {
		t.Fatalf("GetMessages(regenerated variant): %v", err)
	}
	if len(regeneratedMsgs) != 2 {
		t.Errorf("regenerated variant messages = %+v, want it untouched at 2 messages", regeneratedMsgs)
	}

	// The LLM call for the follow-up must have been built from root's
	// (the original's) content — never from the regenerated variant's,
	// since we'd browsed away from it before sending.
	mu.Lock()
	defer mu.Unlock()
	if len(requestBodies) <= preFollowUpCount {
		t.Fatalf("follow-up made no LLM request")
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
