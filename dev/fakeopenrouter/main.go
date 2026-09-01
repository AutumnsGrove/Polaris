// Package main implements a minimal stand-in for OpenRouter's streaming
// /chat/completions API — dev-only tooling for exercising Polaris's real
// server/frontend/WebSocket code against a scripted model instead of a
// real (paid, non-deterministic) LLM. Point config.yaml's
// openrouter.base_url at this server's address and everything upstream of
// the model call — agent.Run, tool dispatch, the gateway, the SvelteKit
// frontend — runs completely unmodified; only the actual model backend is
// swapped. Same idea as llm/llmtest.MockClient (used by Go unit tests,
// which hand agent.Run a Context{LLM: mock} directly), just implemented as
// an HTTP double instead of a Go interface double — a live `polaris run`
// process has no such seam: gateway/turn.go always constructs a real
// llm.NewClient hitting the real base_url, hardcoded, with nothing to
// inject a mock client into.
//
// Primary use case: driving Playwright against a real running `polaris
// run` instance — e.g. from a Claude Code remote/cloud session, where
// there's no real OPENROUTER_API_KEY on hand — and needing a screenshot
// that shows the app actually responding, tool calls included, not just
// an empty-state screen.
//
// Usage:
//
//	go run ./dev/fakeopenrouter &
//	# config.yaml: openrouter.base_url: "http://127.0.0.1:18901"
//	# (api_key can be any non-empty string — config.Load requires one
//	# present but this server never checks it)
//
// With nothing queued, every call gets a generic canned plain-text reply
// — enough to exercise a normal turn end-to-end with zero setup. Queue a
// specific scripted response (a tool call, a particular answer) before
// the turn that should produce it:
//
//	curl -sX POST http://127.0.0.1:18901/_control/queue -d '{"responses":[
//	  {"tool_calls":[{"name":"ask_user_question","arguments":{"question":"...","wants_web_search":true}}]},
//	  {"content":"Here is the answer now that research is back on."}
//	]}'
//
// Responses are served FIFO, one per call to /chat/completions, then it
// falls back to the generic reply again once the queue drains — so a
// multi-turn scenario (a tool call, then the follow-up answer once the
// user responds to it) just queues both up front. GET /_control/calls
// returns every request body received so far, raw, for asserting what the
// app actually sent (e.g. that a disabled tool really didn't appear in
// the offered tools list, or that chat mode's system prompt fragment made
// it into the messages). POST /_control/reset clears both the queue and
// that log between test scenarios, so one server process can serve a
// whole Playwright suite instead of needing a fresh restart per case.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"sync"
)

// queuedToolCall is the control API's ergonomic shape for a scripted tool
// call — Arguments is a plain JSON object here (not the pre-stringified
// form OpenRouter's wire format actually uses), so a curl command or a
// Playwright script can write {"question": "...", "wants_web_search":
// true} directly instead of hand-escaping a JSON string.
type queuedToolCall struct {
	Name      string          `json:"name"`
	Arguments json.RawMessage `json:"arguments"`
}

// queuedResponse is one scripted turn: either Content (a plain-text final
// answer) or ToolCalls (ending the turn in a tool_calls finish, same as a
// real model choosing to call a tool instead of answering) — never both,
// mirroring how a real completion is one or the other.
type queuedResponse struct {
	Content   string           `json:"content,omitempty"`
	ToolCalls []queuedToolCall `json:"tool_calls,omitempty"`
}

// defaultReply is what every call gets when the queue is empty — lets a
// script exercise "does a normal turn complete at all" with zero setup,
// and is also what a scenario falls back to once its queued responses run
// out, rather than erroring (unlike llmtest.MockClient, which treats
// running out of queued responses as a test failure — there's no
// equivalent "this is a bug" signal to give here, since a Playwright
// script driving a live browser can easily trigger more turns than it
// explicitly scripted, e.g. title/suggestion generation calls).
const defaultReply = "This is the fake OpenRouter server's default reply — queue a scripted response via POST /_control/queue for anything more specific."

type server struct {
	mu    sync.Mutex
	queue []queuedResponse
	calls []json.RawMessage
}

func (s *server) handleChatCompletions(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	s.mu.Lock()
	s.calls = append(s.calls, json.RawMessage(body))
	var resp queuedResponse
	if len(s.queue) > 0 {
		resp = s.queue[0]
		s.queue = s.queue[1:]
	} else {
		resp = queuedResponse{Content: defaultReply}
	}
	s.mu.Unlock()

	w.Header().Set("Content-Type", "text/event-stream")
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}
	sseLine := func(data string) {
		fmt.Fprintf(w, "data: %s\n\n", data)
		flusher.Flush()
	}

	if len(resp.ToolCalls) > 0 {
		for i, tc := range resp.ToolCalls {
			args := string(tc.Arguments)
			if args == "" {
				args = "{}"
			}
			chunk, _ := json.Marshal(map[string]interface{}{
				"choices": []map[string]interface{}{{
					"delta": map[string]interface{}{
						"tool_calls": []map[string]interface{}{{
							"index": i,
							"id":    fmt.Sprintf("call_%d", i),
							"type":  "function",
							"function": map[string]string{
								"name":      tc.Name,
								"arguments": args,
							},
						}},
					},
					"finish_reason": "",
				}},
				"model": "fake-openrouter",
			})
			sseLine(string(chunk))
		}
		finishChunk, _ := json.Marshal(map[string]interface{}{
			"choices": []map[string]interface{}{{"delta": map[string]interface{}{}, "finish_reason": "tool_calls"}},
			"usage":   map[string]interface{}{"prompt_tokens": 0, "completion_tokens": 0, "total_tokens": 0, "cost": 0},
			"model":   "fake-openrouter",
		})
		sseLine(string(finishChunk))
	} else {
		content := resp.Content
		if content == "" {
			content = defaultReply
		}
		// Chunked rather than sent whole — exercises the frontend's live
		// token-by-token rendering path instead of one giant "token" event.
		const chunkSize = 24
		for len(content) > 0 {
			n := chunkSize
			if n > len(content) {
				n = len(content)
			}
			chunk, _ := json.Marshal(map[string]interface{}{
				"choices": []map[string]interface{}{{
					"delta":         map[string]interface{}{"content": content[:n]},
					"finish_reason": "",
				}},
				"model": "fake-openrouter",
			})
			sseLine(string(chunk))
			content = content[n:]
		}
		finishChunk, _ := json.Marshal(map[string]interface{}{
			"choices": []map[string]interface{}{{"delta": map[string]interface{}{}, "finish_reason": "stop"}},
			"usage":   map[string]interface{}{"prompt_tokens": 0, "completion_tokens": 0, "total_tokens": 0, "cost": 0},
			"model":   "fake-openrouter",
		})
		sseLine(string(finishChunk))
	}
	sseLine("[DONE]")
}

func (s *server) handleQueue(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Responses []queuedResponse `json:"responses"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	s.mu.Lock()
	s.queue = append(s.queue, req.Responses...)
	n := len(s.queue)
	s.mu.Unlock()
	fmt.Fprintf(w, `{"queued":%d}`, n)
}

func (s *server) handleReset(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	s.queue = nil
	s.calls = nil
	s.mu.Unlock()
	w.WriteHeader(http.StatusNoContent)
}

func (s *server) handleCalls(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	calls := s.calls
	s.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(calls)
}

func main() {
	addr := flag.String("addr", "127.0.0.1:18901", "listen address")
	flag.Parse()

	s := &server{}
	mux := http.NewServeMux()
	mux.HandleFunc("/chat/completions", s.handleChatCompletions)
	mux.HandleFunc("/_control/queue", s.handleQueue)
	mux.HandleFunc("/_control/reset", s.handleReset)
	mux.HandleFunc("/_control/calls", s.handleCalls)

	log.Printf("fake OpenRouter stub listening on %s — point openrouter.base_url at it in config.yaml", *addr)
	log.Fatal(http.ListenAndServe(*addr, mux))
}
