package gateway

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// capturingLLM answers every /chat/completions call with a canned reply and
// records each request's system prompt, so a test can assert what the model
// was actually told rather than what the code believes it sent.
func capturingLLM(t *testing.T) (srv *httptest.Server, systemPrompts func() []string) {
	t.Helper()
	var mu sync.Mutex
	var seen []string
	chunk := `{"choices":[{"delta":{"content":"Paris."}}]}`
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Messages []struct {
				Role    string `json:"role"`
				Content any    `json:"content"`
			} `json:"messages"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		if len(body.Messages) > 0 && body.Messages[0].Role == "system" {
			mu.Lock()
			seen = append(seen, fmt.Sprint(body.Messages[0].Content))
			mu.Unlock()
		}
		w.Header().Set("Content-Type", "text/event-stream")
		flusher := w.(http.Flusher)
		fmt.Fprintf(w, "data: %s\n\n", chunk)
		flusher.Flush()
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{},\"finish_reason\":\"stop\"}],\"usage\":{\"cost\":0.0001}}\n\n")
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	t.Cleanup(srv.Close)
	return srv, func() []string {
		mu.Lock()
		defer mu.Unlock()
		return append([]string(nil), seen...)
	}
}

func anyContains(prompts []string, needle string) bool {
	for _, p := range prompts {
		if strings.Contains(p, needle) {
			return true
		}
	}
	return false
}

// TestVisuals_OnlyTheLiveChatTurnIsTaughtBlocks is the end-to-end guarantee
// behind "chat only": the same question gets the `ui` grammar over /ws (the
// surface that renders blocks) and does NOT over /api/ask (whose answer lands
// in a terminal or another program as plain text). Both reach handleTurn, so
// only ClientMessage.Interactive tells them apart.
func TestVisuals_OnlyTheLiveChatTurnIsTaughtBlocks(t *testing.T) {
	const marker = "## Visual blocks"

	t.Run("websocket turn gets the grammar at the default (low) dial", func(t *testing.T) {
		llm, prompts := capturingLLM(t)
		h := newTestHarness(t, llm.URL)
		conn := dialWS(t, h)
		if err := conn.WriteJSON(map[string]interface{}{"type": "message", "content": "capital of france", "model": "test-model"}); err != nil {
			t.Fatalf("WriteJSON: %v", err)
		}
		readEventsUntilDone(t, conn, 5*time.Second)
		if !anyContains(prompts(), marker) {
			t.Errorf("a live chat turn should be taught the ui grammar; system prompts seen: %d", len(prompts()))
		}
	})

	t.Run("websocket turn gets nothing when the dial is Off", func(t *testing.T) {
		llm, prompts := capturingLLM(t)
		h := newTestHarness(t, llm.URL)
		if err := h.db.SetSetting(settingVisuals, "off"); err != nil {
			t.Fatalf("SetSetting: %v", err)
		}
		conn := dialWS(t, h)
		if err := conn.WriteJSON(map[string]interface{}{"type": "message", "content": "capital of france", "model": "test-model"}); err != nil {
			t.Fatalf("WriteJSON: %v", err)
		}
		readEventsUntilDone(t, conn, 5*time.Second)
		if len(prompts()) == 0 {
			t.Fatal("the model was never called")
		}
		if anyContains(prompts(), marker) {
			t.Error("Visuals=off must remove the grammar from the prompt")
		}
	})

	t.Run("a voice-call turn is read aloud, so it gets nothing", func(t *testing.T) {
		llm, prompts := capturingLLM(t)
		h := newTestHarness(t, llm.URL)
		conn := dialWS(t, h)
		if err := conn.WriteJSON(map[string]interface{}{"type": "message", "content": "capital of france", "model": "test-model", "voice_mode": true}); err != nil {
			t.Fatalf("WriteJSON: %v", err)
		}
		readEventsUntilDone(t, conn, 5*time.Second)
		if len(prompts()) == 0 {
			t.Fatal("the model was never called")
		}
		if anyContains(prompts(), marker) {
			t.Error("a voice-mode turn must not be taught `ui` blocks")
		}
	})

	t.Run("/api/ask turn never gets the grammar", func(t *testing.T) {
		llm, prompts := capturingLLM(t)
		h := newTestHarness(t, llm.URL)
		postAsk(t, h, AskRequest{Content: "capital of france", Model: "test-model"})
		if len(prompts()) == 0 {
			t.Fatal("the model was never called")
		}
		if anyContains(prompts(), marker) {
			t.Error("an /api/ask turn renders as plain text and must not be taught `ui` blocks, even at the default dial")
		}
	})
}
