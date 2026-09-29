package gateway

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"polaris/tools"
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

// toolCallSSEBodyCost is toolCallSSEBody with an explicit usage cost. A test
// verifying that each wizard turn bills its own spend needs the two turns to
// cost different amounts — an equal pair would let a single accidental
// record pass just as easily as the correct two.
func toolCallSSEBodyCost(toolCallJSON string, cost float64) string {
	return strings.Join([]string{
		`data: {"choices":[{"delta":{"tool_calls":[` + toolCallJSON + `]}}]}`,
		fmt.Sprintf(`data: {"choices":[{"delta":{},"finish_reason":"tool_calls"}],"usage":{"cost":%v}}`, cost),
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

	resp, decoded := postWizard(t, h, "/api/wizard/start", map[string]interface{}{"target": tools.WizardPulsarRoutine, "seed": "gaming news"})
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
// finalize_wizard_prompt, mirroring the two-request start-then-turn flow
// the real routine form uses.
func TestHandleWizardTurn_FinalizesPrompt(t *testing.T) {
	// One fake server, two rounds: /start's agent.Run consumes the first
	// (ask_user_question), /turn's agent.Run — a fresh agent.Run call, but
	// the same underlying HTTP server and request counter — consumes the
	// second (finalize_wizard_prompt).
	srv := sequencedSSEServer(t, []string{
		toolCallSSEBody(`{"index":0,"id":"call_1","type":"function","function":{"name":"ask_user_question",` +
			`"arguments":"{\"question\":\"What should this routine check on?\"}"}}`),
		toolCallSSEBody(`{"index":0,"id":"call_2","type":"function","function":{"name":"finalize_wizard_prompt",` +
			`"arguments":"{\"prompt\":\"Summarize the latest Guild Wars 3 news.\",\"name\":\"GW3 news\"}"}}`),
	})
	defer srv.Close()
	h := newTestHarness(t, srv.URL)

	_, start := postWizard(t, h, "/api/wizard/start", map[string]interface{}{"target": tools.WizardPulsarRoutine, "seed": "gaming news"})
	sessionID, _ := start["session_id"].(string)
	if sessionID == "" {
		t.Fatal("no session_id from start")
	}

	resp, turn := postWizard(t, h, "/api/wizard/turn", map[string]interface{}{
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

// TestHandleWizard_RecordsEachTurnsCost covers the ledger half of the
// wizard's zero-persistence design: it creates no message row to bill, so
// each turn's agent.Run cost has to reach aux_usage on its own — and every
// turn must record its own, not just the opener, since an interview runs
// one completion per answer. Two turns at different costs, then the
// folded-in Polaris total must equal their sum (see aux_usage's schema
// comment and gateway/wizard.go's recordWizardCost). Run against two
// different targets: billing lives in the shared handlers, not per target,
// and a target-specific path that skipped it would otherwise go unnoticed.
func TestHandleWizard_RecordsEachTurnsCost(t *testing.T) {
	for _, target := range []string{tools.WizardPulsarRoutine, tools.WizardFieldInstructions} {
		t.Run(target, func(t *testing.T) {
			srv := sequencedSSEServer(t, []string{
				toolCallSSEBodyCost(`{"index":0,"id":"call_1","type":"function","function":{"name":"ask_user_question",`+
					`"arguments":"{\"question\":\"What should this routine check on?\"}"}}`, 0.0001),
				toolCallSSEBodyCost(`{"index":0,"id":"call_2","type":"function","function":{"name":"finalize_wizard_prompt",`+
					`"arguments":"{\"prompt\":\"Summarize the latest Guild Wars 3 news.\",\"name\":\"GW3 news\"}"}}`, 0.0002),
			})
			defer srv.Close()
			h := newTestHarness(t, srv.URL)

			startResp, start := postWizard(t, h, "/api/wizard/start", map[string]interface{}{"target": target, "label": "Trip", "seed": "gaming news"})
			if startResp.StatusCode != http.StatusOK {
				t.Fatalf("start status = %d, want 200", startResp.StatusCode)
			}
			sessionID, _ := start["session_id"].(string)
			if sessionID == "" {
				t.Fatal("no session_id from start")
			}
			turnResp, _ := postWizard(t, h, "/api/wizard/turn", map[string]interface{}{
				"session_id": sessionID,
				"message":    "Guild Wars 3, weekly",
			})
			if turnResp.StatusCode != http.StatusOK {
				t.Fatalf("turn status = %d, want 200", turnResp.StatusCode)
			}

			stats, err := h.db.GetStats(0)
			if err != nil {
				t.Fatalf("GetStats: %v", err)
			}
			const want = 0.0003 // 0.0001 from the start turn + 0.0002 from the follow-up
			if math.Abs(stats.CostBySource.Polaris.TotalCostUSD-want) > 1e-9 {
				t.Errorf("Polaris.TotalCostUSD = %v, want %v — both wizard turns must bill their own cost to aux_usage",
					stats.CostBySource.Polaris.TotalCostUSD, want)
			}
			if math.Abs(stats.TotalCostUSD-want) > 1e-9 {
				t.Errorf("TotalCostUSD = %v, want %v — wizard spend must reach the grand total", stats.TotalCostUSD, want)
			}
		})
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

	resp, decoded := postWizard(t, h, "/api/wizard/start", map[string]interface{}{"target": tools.WizardPulsarRoutine, "seed": ""})
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

	resp, err := http.Post(h.url("/api/wizard/turn"), "application/json",
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

	resp, err := http.Post(h.url("/api/wizard/turn"), "application/json",
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

	_, start := postWizard(t, h, "/api/wizard/start", map[string]interface{}{"target": tools.WizardPulsarRoutine, "seed": "test"})
	sessionID, _ := start["session_id"].(string)
	if sessionID == "" {
		t.Fatal("no session_id from start")
	}

	h.srvObj.wizardMu.Lock()
	h.srvObj.wizardSessions[sessionID].createdAt = time.Now().Add(-wizardSessionTTL - time.Minute)
	h.srvObj.wizardMu.Unlock()

	resp, err := http.Post(h.url("/api/wizard/turn"), "application/json",
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
// interview actually gets — calculator/memory disabled, ask_user_question
// and finalize_wizard_prompt available — by
// inspecting the request the fake model server actually received, the
// same technique CLAUDE.md recommends for asserting what a turn really
// sent (dev/fakeopenrouter's own /_control/calls). Runs for every target:
// the menu lockdown is in the shared runWizardTurn, and each target must
// get it, not just whichever one the test happened to be written for.
func TestRunWizardTurn_DisablesNonInterviewTools(t *testing.T) {
	for _, target := range wizardTestTargets {
		t.Run(target, func(t *testing.T) {
			runWizardToolMenuCheck(t, target)
		})
	}
}

// wizardTestTargets is every target the shared system serves. Literal,
// like prompts_test.go's wizardKinds, so dropping one fails loudly.
var wizardTestTargets = []string{
	tools.WizardPulsarRoutine,
	tools.WizardPulsarDailyBlock,
	tools.WizardPulsarDailyCustomBlock,
	tools.WizardFieldInstructions,
}

func runWizardToolMenuCheck(t *testing.T, target string) {
	t.Helper()
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

	postWizard(t, h, "/api/wizard/start", map[string]interface{}{"target": target, "label": "Anything", "seed": "test"})

	if capturedBody == "" {
		t.Fatal("the fake model server never received a request")
	}
	for _, disabled := range []string{"calculator", "memory", "web_search", "image_search"} {
		if strings.Contains(capturedBody, `"name":"`+disabled+`"`) {
			t.Errorf("request body offered disallowed tool %q to the wizard model: %s", disabled, capturedBody)
		}
	}
	for _, required := range []string{"ask_user_question", "finalize_wizard_prompt"} {
		if !strings.Contains(capturedBody, `"name":"`+required+`"`) {
			t.Errorf("request body is missing the wizard's own tool %q: %s", required, capturedBody)
		}
	}
}

// capturingModelServer returns a fake model endpoint that answers every
// request with plain "ok" and records the latest request body — enough to
// assert what system prompt / opener a wizard start actually sent.
func capturingModelServer(t *testing.T, captured *string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		buf := new(bytes.Buffer)
		buf.ReadFrom(r.Body)
		*captured = buf.String()
		w.Header().Set("Content-Type", "text/event-stream")
		flusher := w.(http.Flusher)
		fmt.Fprint(w, `data: {"choices":[{"delta":{"content":"ok"}}]}`+"\n")
		fmt.Fprint(w, `data: {"choices":[{"delta":{},"finish_reason":"stop"}],"usage":{"cost":0.0001}}`+"\n")
		fmt.Fprint(w, "data: [DONE]\n")
		flusher.Flush()
	}))
}

// TestHandleWizardStart_TargetScopesSystemPrompt covers the shared
// system's core promise end to end, for every target: a start request
// naming a target gets THAT target's system prompt (with its label
// interpolated where the prompt uses one) and none of the others', and
// the session remembers the whole target for follow-up turns without it
// being resent. This is the successor to the old per-surface tests
// (daily block, custom daily block) — same assertions, one table.
func TestHandleWizardStart_TargetScopesSystemPrompt(t *testing.T) {
	// marker is text unique to that target's intro; every other target's
	// prompt must lack it. usesLabel is whether the intro interpolates it.
	cases := []struct {
		target    string
		label     string
		marker    string
		usesLabel bool
	}{
		{tools.WizardPulsarRoutine, "", "helping the user write a good prompt for a Pulsar routine", false},
		{tools.WizardPulsarDailyBlock, "Local", "This is NOT a whole routine prompt", true},
		{tools.WizardPulsarDailyCustomBlock, "Stock Watchlist", `"general purpose" block`, true},
		{tools.WizardFieldInstructions, "Japan Trip", "custom instructions for a Field", true},
	}
	for _, tc := range cases {
		t.Run(tc.target, func(t *testing.T) {
			var captured string
			srv := capturingModelServer(t, &captured)
			defer srv.Close()
			h := newTestHarness(t, srv.URL)

			resp, decoded := postWizard(t, h, "/api/wizard/start", map[string]interface{}{"target": tc.target, "label": tc.label})
			if resp.StatusCode != http.StatusOK {
				t.Fatalf("status = %d, want 200", resp.StatusCode)
			}
			// The request body is JSON, so a quote in the prompt is escaped;
			// compare against the escaped form of the marker.
			escaped := func(s string) string { b, _ := json.Marshal(s); return strings.Trim(string(b), `"`) }
			if !strings.Contains(captured, escaped(tc.marker)) {
				t.Errorf("request body lacks %s's own system prompt (marker %q): %s", tc.target, tc.marker, captured)
			}
			for _, other := range cases {
				if other.target != tc.target && strings.Contains(captured, escaped(other.marker)) {
					t.Errorf("request body for %s leaked %s's system prompt", tc.target, other.target)
				}
			}
			if tc.usesLabel && !strings.Contains(captured, tc.label) {
				t.Errorf("request body doesn't mention the label %q: %s", tc.label, captured)
			}
			if strings.Contains(captured, "{label}") {
				t.Errorf("unsubstituted {label} reached the model: %s", captured)
			}

			sessionID, _ := decoded["session_id"].(string)
			h.srvObj.wizardMu.Lock()
			session, exists := h.srvObj.wizardSessions[sessionID]
			var got tools.WizardTarget
			if exists {
				got = session.target
			}
			h.srvObj.wizardMu.Unlock()
			if !exists {
				t.Fatal("session was not stored server-side")
			}
			if got.Kind != tc.target || got.Label != tc.label {
				t.Errorf("session.target = %+v, want {%s %s} — a follow-up turn needs this remembered", got, tc.target, tc.label)
			}
		})
	}
}

// TestHandleWizardStart_EmptySeedUsesTargetOpener: with no draft to seed
// from, the interview opens with that target's own opener task, not
// another's (and not an empty message).
func TestHandleWizardStart_EmptySeedUsesTargetOpener(t *testing.T) {
	var captured string
	srv := capturingModelServer(t, &captured)
	defer srv.Close()
	h := newTestHarness(t, srv.URL)

	resp, _ := postWizard(t, h, "/api/wizard/start", map[string]interface{}{"target": tools.WizardFieldInstructions, "label": "Japan Trip"})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	if !strings.Contains(captured, "hasn't said what this Field is for yet") {
		t.Errorf("empty-seed start didn't open with the Field target's opener: %s", captured)
	}
}

// TestHandleWizardStart_RejectsUnknownTarget: an unknown or missing target
// must be a 400 with no session and no model call — not an interview
// running on a system prompt assembled from empty strings.
func TestHandleWizardStart_RejectsUnknownTarget(t *testing.T) {
	for name, body := range map[string]map[string]interface{}{
		"unknown": {"target": "nonsense", "seed": "x"},
		"missing": {"seed": "x"},
		"legacy":  {"daily_block_title": "Local"}, // the pre-consolidation request shape
	} {
		t.Run(name, func(t *testing.T) {
			var captured string
			srv := capturingModelServer(t, &captured)
			defer srv.Close()
			h := newTestHarness(t, srv.URL)

			resp, _ := postWizard(t, h, "/api/wizard/start", body)
			if resp.StatusCode != http.StatusBadRequest {
				t.Errorf("status = %d, want 400", resp.StatusCode)
			}
			if captured != "" {
				t.Error("the model was called for a rejected start request")
			}
			h.srvObj.wizardMu.Lock()
			n := len(h.srvObj.wizardSessions)
			h.srvObj.wizardMu.Unlock()
			if n != 0 {
				t.Errorf("%d session(s) stored for a rejected start request", n)
			}
		})
	}
}
