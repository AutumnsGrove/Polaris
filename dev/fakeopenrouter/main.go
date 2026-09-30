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
// -delay (e.g. -delay=1500ms) sleeps that long before every call starts
// streaming — everything answers instantly by default, which is right for
// a test assertion but too fast to ever actually watch a multi-tool-call
// turn stream in, or to catch a thread genuinely mid-turn (GetThread's
// turn_in_progress) via a browser or a page reload. See also
// dev/stack.sh's --fake-llm[-delay] flags, which wire this whole server
// into the one-command dev stack.
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
//
// Plain FIFO breaks down for a feature that fires several genuinely
// concurrent /chat/completions calls at once from one turn — e.g. Pulsar
// Daily's Stage A, which generates every enabled block as its own
// goroutine (see gateway/pulsar_daily.go). Which physical request lands
// in which queue slot then depends on goroutine scheduling, not which
// logical block asked, so "the 3rd queued response" can't reliably target
// "the headlines block's response." Give an entry a "match" substring to
// pin it to whichever request body actually contains that text instead —
// checked ahead of plain FIFO order and independent of queue position;
// entries with no "match" keep serving strict FIFO among themselves, so
// an existing sequential-turn script needs no changes:
//
//	curl -sX POST http://127.0.0.1:18901/_control/queue -d '{"responses":[
//	  {"match":"Top Headlines","content":"Concurrent block reply for headlines specifically."},
//	  {"content":"Generic reply for every other concurrent block this turn fires."}
//	]}'
//
// # Jev (Oracle mode, source verification)
//
// jev.Client hits a completely different endpoint (POST {base}/systemone,
// not /chat/completions, and a plain JSON response, not SSE) with a
// different request/response shape — see jev/jev.go's package doc
// comment. This server answers it too, with its own queue/control
// surface, so a browser/Playwright session can exercise Oracle mode or
// verification against this same fake process instead of needing a real
// Jev-capable OpenRouter key on hand. GET /_control/jev/calls returns raw
// request bodies (each one's "questions" map's keys/criteria are the
// interesting part to assert on — e.g. that a skip_for_focus check really
// didn't get asked). POST /_control/jev/queue schedules answers for
// specific question keys by name — any question in the request NOT
// covered by the queued entry answers itself, picking whichever of its
// own criteria is named "off"/"no"/"none" (falling back to the
// alphabetically-first option), at 100% confidence, so an unscripted
// question never blocks the response or looks like it fired:
//
//	curl -sX POST http://127.0.0.1:18901/_control/jev/queue -d '{"responses":[
//	  {"answers":{
//	    "high_stakes":{"choice":"medical","probabilities":{"medical":0.91,"none":0.06}},
//	    "focus":{"choice":"researcher","probabilities":{"researcher":0.82,"academic":0.11,"off":0.05}}
//	  }}
//	]}'
//
// Same Match/FIFO precedence as the chat queue above, and the same
// "queue empty -> everything answers quiet/off" fallback as
// defaultReply's reasoning — a script that only cares about the chat
// completion side doesn't have to also stub every Jev call it triggers.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
	"sync"
	"time"
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
	// Match, when non-empty, restricts this entry to a request whose raw
	// body contains this substring — see the package doc comment's
	// "Plain FIFO breaks down..." section for why. Entries with Match set
	// are checked, in queue order, before any plain-FIFO entry, and can be
	// consumed out of position; entries with Match empty are untouched by
	// this and continue serving each other in strict arrival order.
	Match string `json:"match,omitempty"`
	// Cost scripts this response's reported usage.cost — real OpenRouter
	// responses carry actual spend here (llm.Client.ChatCompletionStreaming
	// reads it into ChatResponse.CostUSD), and every response defaulted to
	// 0 until this field existed, which made it impossible to script a
	// live, non-zero-cost scenario against a real running server (e.g. to
	// verify a tool's internal filter-pass LLM call actually gets counted
	// toward a thread's total cost, not silently dropped).
	Cost float64 `json:"cost,omitempty"`
	// PromptTokens scripts this response's reported usage.prompt_tokens
	// (default 0, like Cost) — what makes a token display (the turn-info
	// sheet's "Context" vs summed "Tokens in") exercisable end to end
	// against a live server. Script a growing value across a tool turn's
	// responses to see the last-call-vs-sum distinction.
	PromptTokens int `json:"prompt_tokens,omitempty"`
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

// jevAnswerScript is one scripted question's answer — see
// jev.ChoiceAnswer. Probabilities defaults to {Choice: 1.0} when omitted,
// the common case for a script that only cares which option won, not the
// exact odds.
type jevAnswerScript struct {
	Choice        string             `json:"choice"`
	Probabilities map[string]float64 `json:"probabilities,omitempty"`
}

// queuedJevResponse is one scripted /systemone call — Answers is keyed by
// question key (e.g. "focus", "high_stakes", "chip_pulsar"), same shape
// gateway/oracle.go's RunOracle asks for. Any question in the real
// request NOT covered here answers itself via a quiet default — see
// jevQuietAnswer.
type queuedJevResponse struct {
	Answers map[string]jevAnswerScript `json:"answers,omitempty"`
	Cost    float64                    `json:"cost,omitempty"`
	// Match: see queuedResponse.Match's doc comment — identical semantics,
	// just checked against a /systemone request body instead of a
	// /chat/completions one.
	Match string `json:"match,omitempty"`
}

// jevRequestBody mirrors jev.go's own requestBody/questionWire just
// enough to read back each question's criteria (option set) — needed to
// pick a quiet default for any question a queued response doesn't cover.
type jevRequestBody struct {
	Questions map[string]struct {
		Criteria map[string]string `json:"criteria"`
	} `json:"questions"`
}

type server struct {
	mu    sync.Mutex
	queue []queuedResponse
	calls []json.RawMessage
	// delay is how long each call sleeps before it starts streaming its
	// response — 0 by default (instant, the normal case for a Playwright/
	// CI script that just wants a real turn to complete). A real model's
	// tool-calling turn is far from instant, and a scripted multi-tool-call
	// scenario against the default 0 delay finishes in low tens of
	// milliseconds — plenty fast for a test assertion, but too fast for a
	// human (or a screenshot/mid-turn state check) to ever see it "still
	// running." Set via -delay to slow every call down uniformly instead.
	delay time.Duration

	// jevQueue/jevCalls: Jev's own queue/call-log, entirely separate from
	// the chat-completion ones above — see the package doc comment's
	// "Jev" section.
	jevQueue []queuedJevResponse
	jevCalls []json.RawMessage
}

func (s *server) handleChatCompletions(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	s.mu.Lock()
	s.calls = append(s.calls, json.RawMessage(body))
	s.mu.Unlock()

	if s.delay > 0 {
		time.Sleep(s.delay)
	}

	resp := s.takeResponse(string(body))

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
			"usage":   map[string]interface{}{"prompt_tokens": resp.PromptTokens, "completion_tokens": 0, "total_tokens": resp.PromptTokens, "cost": resp.Cost},
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
			"usage":   map[string]interface{}{"prompt_tokens": resp.PromptTokens, "completion_tokens": 0, "total_tokens": resp.PromptTokens, "cost": resp.Cost},
			"model":   "fake-openrouter",
		})
		sseLine(string(finishChunk))
	}
	sseLine("[DONE]")
}

// takeResponse pops and returns the queued response for one incoming
// request body — a matched entry (Match set and contained in body) ahead
// of the next plain-FIFO entry (Match empty), regardless of queue
// position, or defaultReply if the queue has nothing eligible. See
// queuedResponse.Match's doc comment for why a matched lookup is needed
// at all alongside plain FIFO.
func (s *server) takeResponse(body string) queuedResponse {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i, resp := range s.queue {
		if resp.Match != "" && strings.Contains(body, resp.Match) {
			s.queue = append(s.queue[:i:i], s.queue[i+1:]...)
			return resp
		}
	}
	for i, resp := range s.queue {
		if resp.Match == "" {
			s.queue = append(s.queue[:i:i], s.queue[i+1:]...)
			return resp
		}
	}
	return queuedResponse{Content: defaultReply}
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
	s.jevQueue = nil
	s.jevCalls = nil
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

// jevQuietAnswer picks the "nothing fired" option from a question's own
// criteria set — "off"/"no"/"none"/"general"/"standard"/"any"/"answer"/
// "evergreen" in that priority order (prompts.yaml's checks each use one of
// these for their quiet option: intent's is "general", depth's "standard",
// source_type's "any", task's "answer", recency's "evergreen" — without them
// an unscripted question answered its alphabetically-first option and fired
// a nudge in every test), or the alphabetically-first criteria key if none
// of those are present,
// so an unusual/future check still gets a deterministic, valid answer
// instead of an empty Choice that would fail RunOracle's
// Probabilities[Choice] threshold lookup.
func jevQuietAnswer(criteria map[string]string) jevAnswerScript {
	for _, quiet := range []string{"off", "no", "none", "general", "standard", "any", "answer", "evergreen"} {
		if _, ok := criteria[quiet]; ok {
			return jevAnswerScript{Choice: quiet, Probabilities: map[string]float64{quiet: 1.0}}
		}
	}
	first := ""
	for k := range criteria {
		if first == "" || k < first {
			first = k
		}
	}
	return jevAnswerScript{Choice: first, Probabilities: map[string]float64{first: 1.0}}
}

// takeJevResponse mirrors takeResponse's match/FIFO precedence, just
// against s.jevQueue — see queuedJevResponse.Match's doc comment.
func (s *server) takeJevResponse(body string) (queuedJevResponse, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i, resp := range s.jevQueue {
		if resp.Match != "" && strings.Contains(body, resp.Match) {
			s.jevQueue = append(s.jevQueue[:i:i], s.jevQueue[i+1:]...)
			return resp, true
		}
	}
	for i, resp := range s.jevQueue {
		if resp.Match == "" {
			s.jevQueue = append(s.jevQueue[:i:i], s.jevQueue[i+1:]...)
			return resp, true
		}
	}
	return queuedJevResponse{}, false
}

func (s *server) handleSystemOne(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	s.mu.Lock()
	s.jevCalls = append(s.jevCalls, json.RawMessage(body))
	s.mu.Unlock()

	if s.delay > 0 {
		time.Sleep(s.delay)
	}

	var req jevRequestBody
	if err := json.Unmarshal(body, &req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	scripted, _ := s.takeJevResponse(string(body))

	answers := make(map[string]struct {
		Choice        string             `json:"choice"`
		Probabilities map[string]float64 `json:"probabilities"`
		Confidence    float64            `json:"confidence"`
	}, len(req.Questions))
	for key, q := range req.Questions {
		a, ok := scripted.Answers[key]
		if !ok {
			a = jevQuietAnswer(q.Criteria)
		}
		conf := a.Probabilities[a.Choice]
		answers[key] = struct {
			Choice        string             `json:"choice"`
			Probabilities map[string]float64 `json:"probabilities"`
			Confidence    float64            `json:"confidence"`
		}{Choice: a.Choice, Probabilities: a.Probabilities, Confidence: conf}
	}

	resp := map[string]interface{}{
		"answers": answers,
		"usage":   map[string]interface{}{"input_tokens": 0, "output_tokens": 0, "cost": scripted.Cost},
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp)
}

func (s *server) handleJevQueue(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Responses []queuedJevResponse `json:"responses"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	s.mu.Lock()
	s.jevQueue = append(s.jevQueue, req.Responses...)
	n := len(s.jevQueue)
	s.mu.Unlock()
	fmt.Fprintf(w, `{"queued":%d}`, n)
}

func (s *server) handleJevCalls(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	calls := s.jevCalls
	s.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(calls)
}

func main() {
	addr := flag.String("addr", "127.0.0.1:18901", "listen address")
	delay := flag.Duration("delay", 0, "sleep this long before each call starts streaming its response — 0 (default) answers instantly; set e.g. 1500ms to slow a scripted multi-tool-call turn down enough to actually watch it stream or catch it mid-turn")
	flag.Parse()

	s := &server{delay: *delay}
	mux := http.NewServeMux()
	mux.HandleFunc("/chat/completions", s.handleChatCompletions)
	mux.HandleFunc("/_control/queue", s.handleQueue)
	mux.HandleFunc("/_control/reset", s.handleReset)
	mux.HandleFunc("/_control/calls", s.handleCalls)
	mux.HandleFunc("/systemone", s.handleSystemOne)
	mux.HandleFunc("/_control/jev/queue", s.handleJevQueue)
	mux.HandleFunc("/_control/jev/calls", s.handleJevCalls)

	log.Printf("fake OpenRouter stub listening on %s (delay=%s) — point openrouter.base_url at it in config.yaml", *addr, *delay)
	log.Fatal(http.ListenAndServe(*addr, mux))
}
