package gateway

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"
)

// blockingSSEServer serves the same tool-call SSE body for every request,
// but only after release is closed — lets a test hold a wizard turn's
// agent.Run call "in flight" for as long as it wants, so a second request
// against the same session can be fired while the first is provably still
// running.
func blockingSSEServer(t *testing.T, toolCallJSON string, release <-chan struct{}) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-release
		w.Header().Set("Content-Type", "text/event-stream")
		flusher := w.(http.Flusher)
		lines := []string{
			`data: {"choices":[{"delta":{"tool_calls":[` + toolCallJSON + `]}}]}`,
			`data: {"choices":[{"delta":{},"finish_reason":"tool_calls"}],"usage":{"cost":0.0001}}`,
			`data: [DONE]`,
		}
		for _, line := range lines {
			fmt.Fprintf(w, "%s\n", line)
			flusher.Flush()
		}
	}))
}

// TestHandleWizardTurn_RejectsConcurrentTurnOnSameSession reproduces (pre-fix)
// and guards against (post-fix) a lost-update race: handleWizardTurn used to
// read session.history, release wizardMu for the (possibly slow) agent.Run
// call, then write the result back with no guard against a second concurrent
// request on the same session_id doing the exact same thing — the second
// write would silently clobber the first turn's contribution to history, the
// wizard's only record of the conversation (it's never persisted to the DB).
// Mirrors ws.go's own "a response is already in progress" rejection for a
// connection's in-flight turn.
func TestHandleWizardTurn_RejectsConcurrentTurnOnSameSession(t *testing.T) {
	startSrv := sseToolCallServer(t, []string{
		`{"index":0,"id":"call_1","type":"function","function":{"name":"ask_user_question","arguments":"{\"question\":\"Q1\"}"}}`,
	})
	defer startSrv.Close()
	h := newTestHarness(t, startSrv.URL)

	_, decoded := postWizard(t, h, "/api/pulsar/wizard/start", map[string]interface{}{"seed": "gaming news"})
	sessionID, _ := decoded["session_id"].(string)
	if sessionID == "" {
		t.Fatal("session_id is empty")
	}

	// runWizardTurn builds its LLM client fresh from s.liveConfig() on every
	// call (config.Load re-reads config.yaml from disk unconditionally, no
	// mtime cache — see liveConfig's own doc comment) — rewriting the same
	// config path, still pointed at the same db/attachments dirs, points the
	// next wizard turn at the blocking server instead.
	release := make(chan struct{})
	turnSrv := blockingSSEServer(t, `{"index":0,"id":"call_2","type":"function","function":{"name":"ask_user_question","arguments":"{\"question\":\"Q2\"}"}}`, release)
	defer turnSrv.Close()
	writeTestConfig(t, filepath.Dir(h.cfgPath), turnSrv.URL)

	firstDone := make(chan *http.Response, 1)
	go func() {
		resp, _ := postWizard(t, h, "/api/pulsar/wizard/turn", map[string]interface{}{"session_id": sessionID, "message": "first"})
		firstDone <- resp
	}()

	// Poll until the first request has actually marked the session busy —
	// avoids a fixed sleep racing against goroutine scheduling.
	deadline := time.Now().Add(2 * time.Second)
	for {
		h.srvObj.wizardMu.Lock()
		busy := h.srvObj.wizardSessions[sessionID].busy
		h.srvObj.wizardMu.Unlock()
		if busy {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("first request never marked the session busy")
		}
		time.Sleep(5 * time.Millisecond)
	}

	secondResp, _ := postWizard(t, h, "/api/pulsar/wizard/turn", map[string]interface{}{"session_id": sessionID, "message": "second"})
	if secondResp.StatusCode != http.StatusConflict {
		t.Errorf("second concurrent request status = %d, want %d (already in progress)", secondResp.StatusCode, http.StatusConflict)
	}

	close(release)
	firstResp := <-firstDone
	if firstResp.StatusCode != http.StatusOK {
		t.Errorf("first request status = %d, want 200", firstResp.StatusCode)
	}

	h.srvObj.wizardMu.Lock()
	busy := h.srvObj.wizardSessions[sessionID].busy
	h.srvObj.wizardMu.Unlock()
	if busy {
		t.Error("session left marked busy after the in-flight turn finished")
	}
}
