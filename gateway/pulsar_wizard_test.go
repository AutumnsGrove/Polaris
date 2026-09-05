package gateway

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// sequencedSSEServer serves one pre-baked SSE response body per top-level
// HTTP request it receives (not per SSE line) — request 1 gets bodies[0],
// request 2 gets bodies[1], and so on, holding on the last body for any
// request past the end. Lets a test drive two separate agent.Run calls
// (wizard start, then wizard turn) against one fake model that hands out a
// different tool call each time, without needing to swap the server the
// client is pointed at mid-test.
func sequencedSSEServer(t *testing.T, bodies []string) *httptest.Server {
	t.Helper()
	var reqCount int32
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := int(atomic.AddInt32(&reqCount, 1)) - 1
		if n >= len(bodies) {
			n = len(bodies) - 1
		}
		w.Header().Set("Content-Type", "text/event-stream")
		flusher := w.(http.Flusher)
		fmt.Fprint(w, bodies[n])
		flusher.Flush()
	}))
}

// toolCallSSEBody is one complete SSE response naming a single tool call —
// same shape as sseToolCallServer's round1, just packaged per-round for
// sequencedSSEServer above.
func toolCallSSEBody(toolCallJSON string) string {
	return strings.Join([]string{
		`data: {"choices":[{"delta":{"tool_calls":[` + toolCallJSON + `]}}]}`,
		`data: {"choices":[{"delta":{},"finish_reason":"tool_calls"}],"usage":{"cost":0.0001}}`,
		`data: [DONE]`,
	}, "\n") + "\n"
}

// sseTextOnlyServer fakes a model that replies in plain prose without
// calling any tool — the wizardResponse.Answer fallback path (see its doc
// comment: "a model that answers in plain text anyway still needs
// somewhere to go instead of silently vanishing").
func sseTextOnlyServer(t *testing.T, content string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		flusher := w.(http.Flusher)
		lines := []string{
			fmt.Sprintf(`data: {"choices":[{"delta":{"content":%q}}]}`, content),
			`data: {"choices":[{"delta":{},"finish_reason":"stop"}],"usage":{"cost":0.0001}}`,
			`data: [DONE]`,
		}
		for _, line := range lines {
			fmt.Fprintf(w, "%s\n", line)
			flusher.Flush()
		}
	}))
}

func postWizard(t *testing.T, h *testHarness, path string, body map[string]interface{}) (*http.Response, map[string]interface{}) {
	t.Helper()
	b, _ := json.Marshal(body)
	resp, err := http.Post(h.url(path), "application/json", bytes.NewReader(b))
	if err != nil {
		t.Fatalf("POST %s: %v", path, err)
	}
	var decoded map[string]interface{}
	if resp.StatusCode == http.StatusOK {
		if err := json.NewDecoder(resp.Body).Decode(&decoded); err != nil {
			t.Fatalf("decoding %s response: %v", path, err)
		}
	}
	resp.Body.Close()
	return resp, decoded
}

// TestHandleWizardStart_QuestionPath exercises the interview's normal
// shape: the model calls ask_user_question, and the HTTP response carries
// that question back with a fresh session id — no thread/message ever
// created, per the wizard's whole "zero persistence" design.
func TestHandleWizardStart_QuestionPath(t *testing.T) {
	srv := sseToolCallServer(t, []string{
		`{"index":0,"id":"call_1","type":"function","function":{"name":"ask_user_question",` +
			`"arguments":"{\"question\":\"What should this routine check on?\"}"}}`,
	})
	defer srv.Close()
	h := newTestHarness(t, srv.URL)

	resp, decoded := postWizard(t, h, "/api/pulsar/wizard/start", map[string]interface{}{"seed": "gaming news"})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}

	sessionID, _ := decoded["session_id"].(string)
	if sessionID == "" {
		t.Fatal("session_id is empty, want a fresh session id")
	}
	question, ok := decoded["question"].(map[string]interface{})
	if !ok {
		t.Fatalf("question = %v, want an object", decoded["question"])
	}
	if question["question"] != "What should this routine check on?" {
		t.Errorf("question.question = %v", question["question"])
	}
	if decoded["final"] != nil {
		t.Errorf("final = %v, want nil on the question path", decoded["final"])
	}
	if decoded["answer"] != nil && decoded["answer"] != "" {
		t.Errorf("answer = %v, want empty on the question path", decoded["answer"])
	}

	h.srvObj.wizardMu.Lock()
	_, exists := h.srvObj.wizardSessions[sessionID]
	h.srvObj.wizardMu.Unlock()
	if !exists {
		t.Error("session was not actually stored server-side")
	}
}

// TestHandleWizardTurn_FinalizesPrompt drives a session through to
// finalize_pulsar_prompt, mirroring the two-request start-then-turn flow
// the real routine form uses.
func TestHandleWizardTurn_FinalizesPrompt(t *testing.T) {
	// One fake server, two rounds: /start's agent.Run consumes the first
	// (ask_user_question), /turn's agent.Run — a fresh agent.Run call, but
	// the same underlying HTTP server and request counter — consumes the
	// second (finalize_pulsar_prompt).
	srv := sequencedSSEServer(t, []string{
		toolCallSSEBody(`{"index":0,"id":"call_1","type":"function","function":{"name":"ask_user_question",` +
			`"arguments":"{\"question\":\"What should this routine check on?\"}"}}`),
		toolCallSSEBody(`{"index":0,"id":"call_2","type":"function","function":{"name":"finalize_pulsar_prompt",` +
			`"arguments":"{\"prompt\":\"Summarize the latest Guild Wars 3 news.\",\"name\":\"GW3 news\"}"}}`),
	})
	defer srv.Close()
	h := newTestHarness(t, srv.URL)

	_, start := postWizard(t, h, "/api/pulsar/wizard/start", map[string]interface{}{"seed": "gaming news"})
	sessionID, _ := start["session_id"].(string)
	if sessionID == "" {
		t.Fatal("no session_id from start")
	}

	resp, turn := postWizard(t, h, "/api/pulsar/wizard/turn", map[string]interface{}{
		"session_id": sessionID,
		"message":    "Guild Wars 3, weekly",
	})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}

	final, ok := turn["final"].(map[string]interface{})
	if !ok {
		t.Fatalf("final = %v, want an object", turn["final"])
	}
	if final["prompt"] != "Summarize the latest Guild Wars 3 news." {
		t.Errorf("final.prompt = %v", final["prompt"])
	}
	if final["name"] != "GW3 news" {
		t.Errorf("final.name = %v", final["name"])
	}
	if turn["question"] != nil {
		t.Errorf("question = %v, want nil once finalized", turn["question"])
	}

	// The whole point of the wizard: no thread or message was ever created
	// for this ephemeral interview.
	threads, err := h.db.ListThreads(100)
	if err != nil {
		t.Fatalf("ListThreads: %v", err)
	}
	if len(threads) != 0 {
		t.Errorf("ListThreads = %d threads, want 0 — the wizard must not persist anything", len(threads))
	}
}

// TestHandleWizardStart_PlainProseFallsBackToAnswer covers wizardResponse's
// documented escape hatch: a model reply with neither tool call must still
// surface something to the client instead of leaving all three fields
// empty (the exact "wizard looked frozen" bug its doc comment describes).
func TestHandleWizardStart_PlainProseFallsBackToAnswer(t *testing.T) {
	srv := sseTextOnlyServer(t, "Sure, tell me more about what you'd like tracked.")
	defer srv.Close()
	h := newTestHarness(t, srv.URL)

	resp, decoded := postWizard(t, h, "/api/pulsar/wizard/start", map[string]interface{}{"seed": ""})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	if decoded["question"] != nil {
		t.Errorf("question = %v, want nil for a plain-prose reply", decoded["question"])
	}
	if decoded["final"] != nil {
		t.Errorf("final = %v, want nil for a plain-prose reply", decoded["final"])
	}
	answer, _ := decoded["answer"].(string)
	if answer == "" {
		t.Error("answer is empty — a plain-prose reply must not vanish silently")
	}
}

func TestHandleWizardTurn_RejectsEmptyMessage(t *testing.T) {
	srv := sseTextOnlyServer(t, "hi")
	defer srv.Close()
	h := newTestHarness(t, srv.URL)

	resp, err := http.Post(h.url("/api/pulsar/wizard/turn"), "application/json",
		bytes.NewReader([]byte(`{"session_id":"whatever","message":"   "}`)))
	if err != nil {
		t.Fatalf("POST wizard/turn: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("status = %d, want 400 for a blank message", resp.StatusCode)
	}
}

func TestHandleWizardTurn_UnknownSessionIsGone(t *testing.T) {
	srv := sseTextOnlyServer(t, "hi")
	defer srv.Close()
	h := newTestHarness(t, srv.URL)

	resp, err := http.Post(h.url("/api/pulsar/wizard/turn"), "application/json",
		bytes.NewReader([]byte(`{"session_id":"does-not-exist","message":"hello"}`)))
	if err != nil {
		t.Fatalf("POST wizard/turn: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusGone {
		t.Errorf("status = %d, want 410 for an unknown session", resp.StatusCode)
	}
}

// TestHandleWizardTurn_ExpiredSessionIsGone covers handleWizardTurn's own
// lazy TTL check (the "on next access" half of the two-layered eviction —
// see wizardSessionTTL's doc comment; sweepExpiredWizardSessions below
// covers the other half).
func TestHandleWizardTurn_ExpiredSessionIsGone(t *testing.T) {
	srv := sseTextOnlyServer(t, "hi")
	defer srv.Close()
	h := newTestHarness(t, srv.URL)

	_, start := postWizard(t, h, "/api/pulsar/wizard/start", map[string]interface{}{"seed": "test"})
	sessionID, _ := start["session_id"].(string)
	if sessionID == "" {
		t.Fatal("no session_id from start")
	}

	h.srvObj.wizardMu.Lock()
	h.srvObj.wizardSessions[sessionID].createdAt = time.Now().Add(-wizardSessionTTL - time.Minute)
	h.srvObj.wizardMu.Unlock()

	resp, err := http.Post(h.url("/api/pulsar/wizard/turn"), "application/json",
		bytes.NewReader([]byte(`{"session_id":"`+sessionID+`","message":"still there?"}`)))
	if err != nil {
		t.Fatalf("POST wizard/turn: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusGone {
		t.Errorf("status = %d, want 410 for an expired session", resp.StatusCode)
	}

	h.srvObj.wizardMu.Lock()
	_, stillThere := h.srvObj.wizardSessions[sessionID]
	h.srvObj.wizardMu.Unlock()
	if stillThere {
		t.Error("the lazy check found the session expired but didn't evict it from the map")
	}
}

// TestSweepExpiredWizardSessions covers the sweep's own memory-leak-guard
// role — a session nobody ever comes back to (so the lazy check in
// handleWizardTurn never fires) must still eventually be evicted by the
// scheduler's periodic sweep.
func TestSweepExpiredWizardSessions(t *testing.T) {
	srv := sseTextOnlyServer(t, "hi")
	defer srv.Close()
	h := newTestHarness(t, srv.URL)

	h.srvObj.wizardMu.Lock()
	h.srvObj.wizardSessions["stale"] = &wizardSession{createdAt: time.Now().Add(-wizardSessionTTL - time.Minute)}
	h.srvObj.wizardSessions["fresh"] = &wizardSession{createdAt: time.Now()}
	h.srvObj.wizardMu.Unlock()

	h.srvObj.sweepExpiredWizardSessions()

	h.srvObj.wizardMu.Lock()
	defer h.srvObj.wizardMu.Unlock()
	if _, ok := h.srvObj.wizardSessions["stale"]; ok {
		t.Error("sweep left a session past its TTL in the map")
	}
	if _, ok := h.srvObj.wizardSessions["fresh"]; !ok {
		t.Error("sweep evicted a session that hadn't expired yet")
	}
}

// TestRunWizardTurn_DisablesNonInterviewTools confirms the tool menu the
// interview actually gets — calculator/memory/read_attachment/visualize
// disabled, ask_user_question and finalize_pulsar_prompt available — by
// inspecting the request the fake model server actually received, the
// same technique CLAUDE.md recommends for asserting what a turn really
// sent (dev/fakeopenrouter's own /_control/calls).
func TestRunWizardTurn_DisablesNonInterviewTools(t *testing.T) {
	var capturedBody string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		buf := new(bytes.Buffer)
		buf.ReadFrom(r.Body)
		capturedBody = buf.String()
		w.Header().Set("Content-Type", "text/event-stream")
		flusher := w.(http.Flusher)
		fmt.Fprint(w, `data: {"choices":[{"delta":{"content":"ok"}}]}`+"\n")
		fmt.Fprint(w, `data: {"choices":[{"delta":{},"finish_reason":"stop"}],"usage":{"cost":0.0001}}`+"\n")
		fmt.Fprint(w, "data: [DONE]\n")
		flusher.Flush()
	}))
	defer srv.Close()
	h := newTestHarness(t, srv.URL)

	postWizard(t, h, "/api/pulsar/wizard/start", map[string]interface{}{"seed": "test"})

	if capturedBody == "" {
		t.Fatal("the fake model server never received a request")
	}
	for _, disabled := range []string{"calculator", "memory", "read_attachment", "visualize", "web_search", "image_search"} {
		if strings.Contains(capturedBody, `"name":"`+disabled+`"`) {
			t.Errorf("request body offered disallowed tool %q to the wizard model: %s", disabled, capturedBody)
		}
	}
	for _, required := range []string{"ask_user_question", "finalize_pulsar_prompt"} {
		if !strings.Contains(capturedBody, `"name":"`+required+`"`) {
			t.Errorf("request body is missing the wizard's own tool %q: %s", required, capturedBody)
		}
	}
}
