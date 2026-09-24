package gateway

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// recordingAgentLLM fakes OpenRouter for full turns and records every
// agent-loop request body it gets. Agent calls are told apart from the
// side calls a turn also makes (title, follow-up suggestions) by carrying
// a "tools" list, so side calls can't shift which scripted reply an agent
// call gets, however the suggestions goroutine happens to be scheduled.
type recordingAgentLLM struct {
	mu      sync.Mutex
	replies []string // SSE bodies for successive agent calls
	bodies  [][]byte // recorded agent-call request bodies, in order
}

func (f *recordingAgentLLM) serve(t *testing.T) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var probe struct {
			Tools json.RawMessage `json:"tools"`
		}
		_ = json.Unmarshal(body, &probe)

		reply := sseAnswerBody("Side-call reply")
		if len(probe.Tools) > 0 {
			f.mu.Lock()
			n := len(f.bodies)
			f.bodies = append(f.bodies, body)
			if n < len(f.replies) {
				reply = f.replies[n]
			}
			f.mu.Unlock()
		}
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, reply)
		w.(http.Flusher).Flush()
	}))
}

func sseAnswerBody(content string) string {
	return strings.Join([]string{
		fmt.Sprintf(`data: {"choices":[{"delta":{"content":%q}}]}`, content),
		`data: {"choices":[{"delta":{},"finish_reason":"stop"}],"usage":{"cost":0.0001}}`,
		`data: [DONE]`,
	}, "\n") + "\n"
}

// wireMessages splits a recorded request body's "messages" array into each
// element's exact bytes as sent — json.RawMessage keeps them untouched.
func wireMessages(t *testing.T, body []byte) (msgs []json.RawMessage, tools json.RawMessage) {
	t.Helper()
	var req struct {
		Messages []json.RawMessage `json:"messages"`
		Tools    json.RawMessage   `json:"tools"`
	}
	if err := json.Unmarshal(body, &req); err != nil {
		t.Fatalf("decoding recorded request: %v", err)
	}
	return req.Messages, req.Tools
}

// TestFollowUpTurn_RequestExtendsPreviousTurnByteForByte is the property
// docs/plans/verbatim-turn-transcripts.md rests on: a follow-up's request
// must open with exactly the bytes the previous turn's last request sent —
// system prompt, tools list, and the whole earlier tool round (commentary,
// provider call ids, full results) — followed only by the previous final
// answer and the new question. Providers cache on an exact-prefix match, so
// any reconstruction step that reshapes an earlier turn, or any volatile
// value near the top of the system prompt (it used to carry the time to the
// minute), fails this test instead of silently defeating caching live.
func TestFollowUpTurn_RequestExtendsPreviousTurnByteForByte(t *testing.T) {
	fake := &recordingAgentLLM{replies: []string{
		// Turn 1, call 1: commentary plus a two-call batch.
		strings.Join([]string{
			`data: {"choices":[{"delta":{"content":"Let me compute that."}}]}`,
			`data: {"choices":[{"delta":{"tool_calls":[` +
				`{"index":0,"id":"call_x1","type":"function","function":{"name":"calculator","arguments":"{\"expression\":\"2+2\"}"}},` +
				`{"index":1,"id":"call_x2","type":"function","function":{"name":"calculator","arguments":"{\"expression\":\"3*3\"}"}}]}}]}`,
			`data: {"choices":[{"delta":{},"finish_reason":"tool_calls"}],"usage":{"cost":0.0001}}`,
			`data: [DONE]`,
		}, "\n") + "\n",
		// Turn 1, call 2: the answer.
		sseAnswerBody("2+2 is 4 and 3*3 is 9."),
		// Turn 2.
		sseAnswerBody("Their sum is 13."),
	}}
	srv := fake.serve(t)
	defer srv.Close()
	h := newTestHarness(t, srv.URL)

	_, first := postAsk(t, h, AskRequest{Content: "what are 2+2 and 3*3?"})
	if first.ThreadID == "" {
		t.Fatalf("turn 1 returned no thread id: %+v", first)
	}
	_, second := postAsk(t, h, AskRequest{ThreadID: first.ThreadID, Content: "and their sum?"})
	if second.Answer == "" {
		t.Fatalf("turn 2 returned no answer: %+v", second)
	}

	fake.mu.Lock()
	defer fake.mu.Unlock()
	if len(fake.bodies) != 3 {
		t.Fatalf("got %d agent calls, want 3 (two for turn 1, one for turn 2)", len(fake.bodies))
	}
	prev, prevTools := wireMessages(t, fake.bodies[1])
	next, nextTools := wireMessages(t, fake.bodies[2])

	if !bytes.Equal(prevTools, nextTools) {
		t.Errorf("tools list changed between turns:\nturn 1: %s\nturn 2: %s", prevTools, nextTools)
	}
	if len(next) != len(prev)+2 {
		t.Fatalf("turn 2 sent %d messages, want %d (turn 1's last request + its answer + the new question)", len(next), len(prev)+2)
	}
	for i := range prev {
		if !bytes.Equal(prev[i], next[i]) {
			t.Errorf("message %d differs between turns, breaking the cached prefix:\nturn 1: %s\nturn 2: %s", i, prev[i], next[i])
		}
	}
	if want := `{"role":"assistant","content":"2+2 is 4 and 3*3 is 9."}`; string(next[len(prev)]) != want {
		t.Errorf("turn 1's answer replayed as %s, want %s (no appended source note)", next[len(prev)], want)
	}
	if !strings.Contains(string(next[len(prev)+1]), "and their sum?") {
		t.Errorf("last message = %s, want the new question", next[len(prev)+1])
	}
}
