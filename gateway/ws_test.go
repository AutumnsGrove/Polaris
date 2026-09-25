package gateway

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"sort"
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

// TestWebSocket_TitleSeedOverridesGenerateTitleInput covers a real bug:
// Pulsar Daily's expand-to-chat seeds Content with a synthetic
// instruction wrapper ("The user tapped an expand affordance..."), and
// generateTitle, given only that, was observed live hallucinating a
// title that answers the wrapper's embedded instruction instead of
// titling it. TitleSeed lets a caller hand generateTitle cleaner input
// instead — this confirms the title-generation call actually receives
// TitleSeed's text, not Content's.
func TestWebSocket_TitleSeedOverridesGenerateTitleInput(t *testing.T) {
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

	wrapperContent := `The user tapped an expand affordance on a Pulsar Daily block titled "Picture of the Day" ` +
		`with this content: A nebula. Tell me more about what's shown in this image.`
	titleSeed := "Picture of the Day: A nebula"

	if err := conn.WriteJSON(map[string]interface{}{
		"type": "message", "content": wrapperContent, "model": "test-model",
		"source": "pulsar-daily", "title_seed": titleSeed,
	}); err != nil {
		t.Fatalf("WriteJSON: %v", err)
	}
	readEventsUntilDone(t, conn, 5*time.Second)

	mu.Lock()
	defer mu.Unlock()
	// Found by content, not position — suggestions run detached (see
	// turn.go's own comment on that goroutine) and can race past "done"
	// or land before/after title generation depending on timing, so
	// request order isn't a reliable signal here.
	var titleRequest string
	for _, body := range requestBodies {
		if strings.Contains(body, "Write a short thread title") {
			titleRequest = body
			break
		}
	}
	if titleRequest == "" {
		t.Fatalf("no title-generation request found among %d captured: %v", len(requestBodies), requestBodies)
	}
	if strings.Contains(titleRequest, "tapped an expand affordance") {
		t.Errorf("title-generation request still contains the synthetic wrapper text, want TitleSeed to have replaced it: %s", titleRequest)
	}
	if !strings.Contains(titleRequest, titleSeed) {
		t.Errorf("title-generation request doesn't contain the TitleSeed text %q: %s", titleSeed, titleRequest)
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

// weaverToolNames is the exact tool set catalog.go's WeaverRun gate leaves
// standing: Weaver's own five tools plus search_chats (offered here since
// the harness's non-anonymous turn always wires SearchThreads) — never
// think/calculator/web_search/anything else from the main catalog.
var weaverToolNames = []string{"create_star", "link_stars", "read_star", "search_chats", "search_stars", "update_star"}

// toolBearingRequestToolNames extracts every request body's "tools" array
// of function names — filtered to only bodies that actually carry a
// non-empty tools array, since generateTitle/generateSuggestions each build
// their own toolless completion request and would otherwise pollute the
// list with an empty entry per turn.
func toolBearingRequestToolNames(t *testing.T, bodies []string) [][]string {
	t.Helper()
	var out [][]string
	for _, body := range bodies {
		var req struct {
			Tools []struct {
				Function struct {
					Name string `json:"name"`
				} `json:"function"`
			} `json:"tools"`
		}
		if err := json.Unmarshal([]byte(body), &req); err != nil {
			t.Fatalf("unmarshaling request body: %v", err)
		}
		if len(req.Tools) == 0 {
			continue
		}
		names := make([]string, 0, len(req.Tools))
		for _, tool := range req.Tools {
			names = append(names, tool.Function.Name)
		}
		sort.Strings(names)
		out = append(out, names)
	}
	return out
}

// TestWebSocket_WeaverSourceThread_RestrictsToolsToWeaverSet covers issue
// #94's "Talk to Weaver": a brand-new thread created with source: "weaver"
// (the same client-supplied-source mechanism Pulsar Daily's expand-to-chat
// already uses for "pulsar-daily") must run through Weaver's own agent
// loop, not the main assistant's — offered() only takes tools/catalog.go's
// WeaverRun branch when gateway/turn.go's handleTurn actually sets
// agentCtx.WeaverRun, which previously never happened for any turn coming
// through the ordinary WebSocket path (only the background scheduler's
// RunShootingStar set it, via newWeaverToolContext).
func TestWebSocket_WeaverSourceThread_RestrictsToolsToWeaverSet(t *testing.T) {
	var mu sync.Mutex
	var requestBodies []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		mu.Lock()
		requestBodies = append(requestBodies, string(body))
		mu.Unlock()
		chunk, _ := json.Marshal(map[string]interface{}{
			"choices": []map[string]interface{}{{"delta": map[string]interface{}{"content": "an answer"}}},
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

	if err := conn.WriteJSON(map[string]interface{}{
		"type": "message", "content": "the Framework 13 and ThinkPad stars are the same thing, please merge them",
		"model": "test-model", "source": "weaver",
	}); err != nil {
		t.Fatalf("WriteJSON: %v", err)
	}
	events := readEventsUntilDone(t, conn, 5*time.Second)
	threadID, _ := events[len(events)-1]["thread_id"].(string)

	thread, err := h.db.GetThreadRaw(threadID)
	if err != nil {
		t.Fatalf("GetThreadRaw: %v", err)
	}
	if thread.Source != "weaver" {
		t.Errorf("thread.Source = %q, want %q", thread.Source, "weaver")
	}

	mu.Lock()
	toolSets := toolBearingRequestToolNames(t, requestBodies)
	mu.Unlock()
	if len(toolSets) != 1 {
		t.Fatalf("tool-bearing requests = %d, want exactly 1: %v", len(toolSets), toolSets)
	}
	if got := toolSets[0]; !equalStringSlices(got, weaverToolNames) {
		t.Errorf("offered tools = %v, want exactly %v", got, weaverToolNames)
	}
}

// TestWebSocket_WeaverThreadContinuation_StaysRestricted covers the one
// path shooting stars never exercise (they're always single-shot): a
// second message into an already-existing Weaver thread. The frontend
// never resends source on a continuation (see ClientMessage.Source's own
// doc comment — "only read on thread creation"), so handleTurn must read
// the thread's own persisted source back from the DB rather than trusting
// anything on this later message.
func TestWebSocket_WeaverThreadContinuation_StaysRestricted(t *testing.T) {
	var mu sync.Mutex
	var requestBodies []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		mu.Lock()
		requestBodies = append(requestBodies, string(body))
		mu.Unlock()
		chunk, _ := json.Marshal(map[string]interface{}{
			"choices": []map[string]interface{}{{"delta": map[string]interface{}{"content": "an answer"}}},
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

	if err := conn.WriteJSON(map[string]interface{}{
		"type": "message", "content": "first message", "model": "test-model", "source": "weaver",
	}); err != nil {
		t.Fatalf("WriteJSON (first turn): %v", err)
	}
	events := readEventsUntilDone(t, conn, 5*time.Second)
	threadID, _ := events[len(events)-1]["thread_id"].(string)

	// No source field here — mirrors the real frontend, which only ever
	// sets source on the message that creates a brand-new thread.
	if err := conn.WriteJSON(map[string]interface{}{
		"type": "message", "thread_id": threadID, "content": "a follow-up", "model": "test-model",
	}); err != nil {
		t.Fatalf("WriteJSON (second turn): %v", err)
	}
	readEventsUntilDone(t, conn, 5*time.Second)

	mu.Lock()
	toolSets := toolBearingRequestToolNames(t, requestBodies)
	mu.Unlock()
	if len(toolSets) != 2 {
		t.Fatalf("tool-bearing requests = %d, want exactly 2 (one per turn): %v", len(toolSets), toolSets)
	}
	if got := toolSets[1]; !equalStringSlices(got, weaverToolNames) {
		t.Errorf("second turn's offered tools = %v, want exactly %v (source-less continuation must still resolve to the thread's own persisted source)", got, weaverToolNames)
	}
}

// TestWebSocket_GhostThread_DisconnectDeletesUnpromotedThread is the
// hardening test for the full-fidelity ghost-thread-promotion redesign's
// riskiest new mechanism: connWG's ordering guarantee. It combines
// TestWebSocket_DisconnectDoesNotTruncateInFlightTurn's "close the socket
// while the LLM call is genuinely still in flight" setup with a ghost
// turn, to prove two things at once — the write still completes in full
// after disconnect (same as any other thread), and only THEN does the
// thread get hard-deleted, rather than the delete racing (or worse,
// preceding) the still-in-flight write.
func TestWebSocket_GhostThread_DisconnectDeletesUnpromotedThread(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	var once sync.Once

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		isFirst := false
		once.Do(func() { isFirst = true; close(started) })
		if isFirst {
			<-release
		}

		w.Header().Set("Content-Type", "text/event-stream")
		flusher := w.(http.Flusher)

		content := "n/a"
		if isFirst {
			content = "Hello, ghost world!"
		}
		chunk, _ := json.Marshal(map[string]interface{}{
			"choices": []map[string]interface{}{{"delta": map[string]interface{}{"content": content}}},
		})
		fmt.Fprintf(w, "data: %s\n", chunk)
		flusher.Flush()
		fmt.Fprintf(w, "data: %s\n", `{"choices":[{"delta":{},"finish_reason":"stop"}],"usage":{"cost":0.0001}}`)
		flusher.Flush()
		fmt.Fprint(w, "data: [DONE]\n")
		flusher.Flush()
	}))
	defer srv.Close()

	h := newTestHarness(t, srv.URL)
	conn := dialWS(t, h)

	if err := conn.WriteJSON(map[string]interface{}{
		"type": "message", "content": "hi", "model": "test-model", "anonymous": true,
	}); err != nil {
		t.Fatalf("WriteJSON: %v", err)
	}

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

	// The row must already exist, tagged ghost, before disconnect — the
	// point of this test is what happens to it AFTER, not whether ghost
	// turns persist at all (see TestHandleAsk_Ghost_PersistsAsGhostTaggedThread
	// for that).
	rawThread, err := h.db.GetThreadRaw(threadID)
	if err != nil {
		t.Fatalf("GetThreadRaw before disconnect: %v", err)
	}
	if !rawThread.Ghost {
		t.Fatal("thread not tagged ghost before disconnect")
	}

	conn.Close()

	// The fake LLM handler is still deliberately parked on <-release at
	// this point — meaning the turn goroutine's AddMessage call hasn't
	// run yet, and connWG.Wait() inside the disconnect-sweep defer (which
	// fires the instant conn.Close() makes the server's ReadJSON error)
	// must therefore still be blocked on it too. Checking state HERE,
	// deliberately before releasing the LLM call, is what makes the
	// ordering assertion below non-racy: unlike polling after release is
	// closed (where the write and the delete can both complete within
	// microseconds of each other, too fast for any poll interval to
	// reliably catch the in-between state), this window is held open for
	// as long as the test wants.
	time.Sleep(150 * time.Millisecond)
	if _, err := h.db.GetThreadRaw(threadID); err != nil {
		t.Fatalf("GetThreadRaw while the write is still genuinely in flight: %v, want the thread to still exist — the disconnect-sweep must not have raced ahead of connWG.Wait()", err)
	}
	if msgs, err := h.db.GetMessages(threadID); err != nil {
		t.Fatalf("GetMessages while the write is still in flight: %v", err)
	} else if len(msgs) != 1 {
		t.Fatalf("messages = %+v while the write is still in flight, want exactly 1 (the user message only — the assistant answer hasn't been generated yet)", msgs)
	}

	close(release) // let the fake LLM server finish streaming the answer

	// Now confirm the thread (and, via cascade, its messages) gets
	// hard-deleted once connWG.Wait() unblocks and the disconnect-sweep
	// actually runs — the write completed first (this thread would not
	// exist at all right now if it hadn't, given the check above), so
	// this deletion is real cleanup of an unpromoted ghost thread, not a
	// race against a write that never happened.
	deadline := time.Now().Add(5 * time.Second)
	var stillExists bool
	for time.Now().Before(deadline) {
		_, err = h.db.GetThreadRaw(threadID)
		stillExists = err == nil
		if !stillExists {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if stillExists {
		t.Error("thread still exists after disconnect, want it hard-deleted since it was never promoted")
	}
	if msgs, err := h.db.GetMessages(threadID); err != nil {
		t.Fatalf("GetMessages after delete: %v", err)
	} else if len(msgs) != 0 {
		t.Errorf("GetMessages returned %d rows after the thread was deleted, want 0 (cascade)", len(msgs))
	}
}

// TestWebSocket_GhostThread_PromotedSurvivesDisconnect confirms the other
// side of DeleteThreadPermanently's race-safety: a thread promoted before
// the connection disconnects must survive, not get swept up by the same
// cleanup that would have deleted it had it stayed ghost.
func TestWebSocket_GhostThread_PromotedSurvivesDisconnect(t *testing.T) {
	srv := fakeLLMServer(t, "any", "Hello, promoted world!")
	h := newTestHarness(t, srv.URL)
	conn := dialWS(t, h)

	if err := conn.WriteJSON(map[string]interface{}{
		"type": "message", "content": "hi", "model": "test-model", "anonymous": true,
	}); err != nil {
		t.Fatalf("WriteJSON: %v", err)
	}
	events := readEventsUntilDone(t, conn, 5*time.Second)
	last := events[len(events)-1]
	if last["type"] != "done" {
		t.Fatalf("last event = %+v, want type=done", last)
	}
	threadID, _ := last["thread_id"].(string)
	if threadID == "" {
		t.Fatal("done event carried no thread_id")
	}

	if err := h.db.PromoteGhostThread(threadID); err != nil {
		t.Fatalf("PromoteGhostThread: %v", err)
	}

	conn.Close()

	// Give the (now-irrelevant, since nothing in ghostThreadIDs for this
	// connection is still ghost) disconnect-sweep defer time to run and
	// confirm it left the now-permanent thread alone.
	time.Sleep(200 * time.Millisecond)

	thread, err := h.db.GetThread(threadID)
	if err != nil {
		t.Fatalf("GetThread after promote+disconnect: %v, want it to survive", err)
	}
	if thread.Ghost {
		t.Error("thread still tagged ghost after promotion")
	}
	if msgs, err := h.db.GetMessages(threadID); err != nil {
		t.Fatalf("GetMessages: %v", err)
	} else if len(msgs) != 2 {
		t.Errorf("GetMessages returned %d rows, want 2 (user + assistant), want the promoted thread's content intact", len(msgs))
	}
}

// TestWebSocket_GhostThread_RegainsMemoryAndChatSearchOncePromoted is the
// user-facing follow-up requirement's hardening test: promotion must
// restore memory/chat_search tool access on the very next turn with zero
// extra client signaling — the client doesn't need to (and, per
// protocol.go's Anonymous doc comment, isn't trusted to) tell the server
// "I'm not ghost anymore" on that next turn; the server re-derives ghost
// status fresh off the thread's own persisted row every time.
func TestWebSocket_GhostThread_RegainsMemoryAndChatSearchOncePromoted(t *testing.T) {
	var mu sync.Mutex
	var requestBodies []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		mu.Lock()
		requestBodies = append(requestBodies, string(body))
		mu.Unlock()
		chunk, _ := json.Marshal(map[string]interface{}{
			"choices": []map[string]interface{}{{"delta": map[string]interface{}{"content": "an answer"}}},
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

	// Turn 1: ghost. Memory is on by default (MemoryEnabledFromStore's
	// nil-db/unset-setting fallback), so its absence here is entirely
	// down to ghost gating, not the operator having turned it off.
	if err := conn.WriteJSON(map[string]interface{}{
		"type": "message", "content": "hi", "model": "test-model", "anonymous": true,
	}); err != nil {
		t.Fatalf("WriteJSON (turn 1): %v", err)
	}
	events := readEventsUntilDone(t, conn, 5*time.Second)
	threadID, _ := events[len(events)-1]["thread_id"].(string)
	if threadID == "" {
		t.Fatal("turn 1's done event carried no thread_id")
	}

	if err := h.db.PromoteGhostThread(threadID); err != nil {
		t.Fatalf("PromoteGhostThread: %v", err)
	}

	// Turn 2: a plain continuation — no anonymous field at all, matching
	// how a real promoted client would behave, though turn.go ignores it
	// either way for a continuation.
	if err := conn.WriteJSON(map[string]interface{}{
		"type": "message", "content": "follow-up", "model": "test-model", "thread_id": threadID,
	}); err != nil {
		t.Fatalf("WriteJSON (turn 2): %v", err)
	}
	readEventsUntilDone(t, conn, 5*time.Second)

	mu.Lock()
	toolSets := toolBearingRequestToolNames(t, requestBodies)
	mu.Unlock()
	if len(toolSets) != 2 {
		t.Fatalf("tool-bearing requests = %d, want exactly 2 (one per turn): %v", len(toolSets), toolSets)
	}
	if contains(toolSets[0], "memory") || contains(toolSets[0], "search_chats") {
		t.Errorf("turn 1 (still ghost) offered tools = %v, want memory/search_chats withheld", toolSets[0])
	}
	if !contains(toolSets[1], "memory") {
		t.Errorf("turn 2 (promoted) offered tools = %v, want memory present", toolSets[1])
	}
	if !contains(toolSets[1], "search_chats") {
		t.Errorf("turn 2 (promoted) offered tools = %v, want search_chats present", toolSets[1])
	}
}

func contains(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}

func equalStringSlices(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
