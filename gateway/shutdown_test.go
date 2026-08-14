package gateway

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"
)

// TestShutdown_ActiveTurnSurvivesBeginShutdown is the core claim behind
// the self-update hardening: a turn already running when a restart is
// triggered must be allowed to finish and persist, not get cut off
// mid-flight. cmd/run.go calls BeginShutdown() then WaitForActiveTurns()
// on SIGTERM (systemd's `systemctl restart`, see procmgr/systemd.go) —
// this exercises that exact sequence against a real in-flight WS turn,
// using the same started/release fake-LLM-server pattern as
// TestWebSocket_DisconnectDoesNotTruncateInFlightTurn.
func TestShutdown_ActiveTurnSurvivesBeginShutdown(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	var once sync.Once

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		isFirst := false
		once.Do(func() { isFirst = true; close(started) })
		if isFirst {
			<-release
		}
		chunk, _ := json.Marshal(map[string]interface{}{
			"choices": []map[string]interface{}{{"delta": map[string]interface{}{"content": "an answer"}}},
		})
		w.Header().Set("Content-Type", "text/event-stream")
		flusher := w.(http.Flusher)
		w.Write([]byte("data: "))
		w.Write(chunk)
		w.Write([]byte("\n"))
		flusher.Flush()
		w.Write([]byte(`data: {"choices":[{"delta":{},"finish_reason":"stop"}],"usage":{"cost":0.0001}}` + "\n"))
		w.Write([]byte("data: [DONE]\n"))
		flusher.Flush()
	}))
	defer srv.Close()

	h := newTestHarness(t, srv.URL)
	conn := dialWS(t, h)

	if err := conn.WriteJSON(map[string]interface{}{"type": "message", "content": "hi", "model": "test-model"}); err != nil {
		t.Fatalf("WriteJSON: %v", err)
	}

	// Confirm the turn is genuinely in flight (registered with
	// activeTurns) before triggering shutdown — otherwise this would just
	// prove BeginShutdown doesn't block a turn that hasn't started yet,
	// which isn't the scenario being guarded against.
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("fake LLM server never received the request")
	}

	h.srvObj.BeginShutdown()

	// WaitForActiveTurns must not return early just because BeginShutdown
	// was called — the turn is still genuinely running.
	waitDone := make(chan error, 1)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		waitDone <- h.srvObj.WaitForActiveTurns(ctx)
	}()

	select {
	case err := <-waitDone:
		t.Fatalf("WaitForActiveTurns returned (err=%v) before the in-flight turn was released — it must wait for the turn to finish", err)
	case <-time.After(200 * time.Millisecond):
		// Expected: still waiting, because the turn is still blocked on
		// `release`.
	}

	close(release) // let the fake LLM server finish streaming

	select {
	case err := <-waitDone:
		if err != nil {
			t.Fatalf("WaitForActiveTurns returned an error (%v), want nil — the turn should finish well within the 5s budget", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("WaitForActiveTurns never returned after the turn was released")
	}

	// The whole point: the turn's messages must be fully persisted, exactly
	// as if no shutdown had ever been triggered.
	events := readEventsUntilDone(t, conn, 2*time.Second)
	last := events[len(events)-1]
	if last["type"] != "done" {
		t.Fatalf("last event = %+v, want type=done — the turn must complete normally despite the shutdown", last)
	}
	threadID, _ := last["thread_id"].(string)

	msgs, err := h.db.GetMessages(threadID)
	if err != nil {
		t.Fatalf("GetMessages: %v", err)
	}
	if len(msgs) != 2 || msgs[1].Content != "an answer" {
		t.Fatalf("messages = %+v, want the user question + the full persisted answer", msgs)
	}
}

// TestShutdown_RejectsNewTurnsOnceBegun guards the other half of the
// fix: once BeginShutdown has run, a brand-new turn must never start at
// all — starting one now would just mean it gets killed moments later
// when cmd/run.go's process actually exits, which is exactly the
// "thread reverts to its pre-message state" failure mode being avoided.
func TestShutdown_RejectsNewTurnsOnceBegun(t *testing.T) {
	srv := fakeLLMServer(t, "any", "should never be reached")
	h := newTestHarness(t, srv.URL)
	conn := dialWS(t, h)

	h.srvObj.BeginShutdown()

	if err := conn.WriteJSON(map[string]interface{}{"type": "message", "content": "hi", "model": "test-model"}); err != nil {
		t.Fatalf("WriteJSON: %v", err)
	}

	conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	var evt map[string]interface{}
	if err := conn.ReadJSON(&evt); err != nil {
		t.Fatalf("reading event: %v", err)
	}
	if evt["type"] != "error" {
		t.Fatalf("event = %+v, want an error rejecting the turn during shutdown", evt)
	}

	// No turn should have started, so activeTurns must already be at
	// zero — WaitForActiveTurns must return immediately.
	ctx, cancel := context.WithTimeout(context.Background(), 1*time.Second)
	defer cancel()
	if err := h.srvObj.WaitForActiveTurns(ctx); err != nil {
		t.Fatalf("WaitForActiveTurns = %v, want nil — no turn should have started to wait on", err)
	}
}

// TestShutdown_AbortActiveTurnsNotifiesClientPastDeadline is a regression
// test for the actual "thread bump-back" mechanism, confirmed live against
// the production server: a turn that outruns shutdownGrace (a multi-step
// research turn doing several sequential tool calls is the realistic case
// — reproduced with a synthetic turn cut off mid-tool-call at exactly the
// drain deadline) used to just have its connection go silent when
// cmd/run.go gave up and the process exited. No 'done', no 'error' — the
// frontend's AppState.busy never clears normally and the new thread this
// turn belonged to never gets adopted as currentThreadId (see
// state.svelte.ts's 'done' handler), so the UI is left showing whatever
// thread was open before, indistinguishable from "the app bumped me back
// to an old conversation." AbortActiveTurns exists so cmd/run.go can tell
// the client explicitly instead of just vanishing.
func TestShutdown_AbortActiveTurnsNotifiesClientPastDeadline(t *testing.T) {
	started := make(chan struct{})
	blockForever := make(chan struct{})
	var once sync.Once

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		once.Do(func() { close(started) })
		<-blockForever // simulates a turn that outlives the drain deadline
	}))
	defer srv.Close()
	// srv.Close() (deferred above, so it runs LAST — defers unwind LIFO)
	// blocks until every outstanding request finishes. The handler above
	// deliberately never returns on its own (that's the whole point:
	// simulating a turn cmd/run.go gave up waiting on), so it must be
	// unblocked first or Close() hangs forever.
	defer close(blockForever)

	h := newTestHarness(t, srv.URL)
	conn := dialWS(t, h)

	if err := conn.WriteJSON(map[string]interface{}{"type": "message", "content": "hi", "model": "test-model"}); err != nil {
		t.Fatalf("WriteJSON: %v", err)
	}

	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("fake LLM server never received the request")
	}

	h.srvObj.BeginShutdown()

	// Simulate cmd/run.go's drain deadline expiring while the turn is
	// still genuinely in flight — this is the exact moment the old code
	// just gave up silently.
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	if err := h.srvObj.WaitForActiveTurns(ctx); err == nil {
		t.Fatal("WaitForActiveTurns returned nil, want a deadline error — the turn is deliberately still blocked")
	}

	h.srvObj.AbortActiveTurns("the server is restarting and this turn couldn't finish in time — please retry")

	// 'user_message' arrives first (persisted well before the LLM call
	// even starts — see handleTurn) — the abort event is whatever comes
	// after it, since this turn never got far enough to send anything else.
	conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	for i := 0; i < 5; i++ {
		var evt map[string]interface{}
		if err := conn.ReadJSON(&evt); err != nil {
			t.Fatalf("reading event: %v", err)
		}
		if evt["type"] == "error" {
			return
		}
	}
	t.Fatal("never received an explicit error telling the client this turn was cut off")
}
