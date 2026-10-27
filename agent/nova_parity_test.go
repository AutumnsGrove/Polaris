package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"polaris/llm"
	"polaris/tools"
)

// The parity harness (docs/plans/nova-loop.md §7): one scripted OpenRouter-
// shaped HTTP server, driven by a real llm.Client, run through both loops. A
// script is a list of SSE response bodies, one per model call. Assertions
// compare *records* — the request bodies the server actually received, the
// ordered live events, the transcript, the Result — so a new workstream adds a
// script, not a new harness. It runs over real HTTP because that is the only
// way to see the bytes (stream_options, reasoning shape, tool order,
// cache_control) the plan names as the defects that pass a behavioural test
// and quietly cost money.

// sse builds one streamed response body from raw JSON chunks.
func sse(chunks ...string) string {
	var b strings.Builder
	for _, c := range chunks {
		b.WriteString("data: " + c + "\n\n")
	}
	b.WriteString("data: [DONE]\n\n")
	return b.String()
}

func contentChunk(s string) string {
	b, _ := json.Marshal(s)
	return `{"choices":[{"delta":{"content":` + string(b) + `}}]}`
}

func reasoningChunk(s string) string {
	b, _ := json.Marshal(s)
	return `{"choices":[{"delta":{"reasoning":` + string(b) + `}}]}`
}

func toolCallChunk(index int, id, name, args string) string {
	a, _ := json.Marshal(args)
	return fmt.Sprintf(`{"choices":[{"delta":{"tool_calls":[{"index":%d,"id":%q,"type":"function","function":{"name":%q,"arguments":%s}}]}}]}`, index, id, name, a)
}

func finishChunk(reason string, cost float64, prompt, completion, cached int) string {
	return fmt.Sprintf(`{"choices":[{"delta":{},"finish_reason":%q}],"usage":{"prompt_tokens":%d,"completion_tokens":%d,"total_tokens":%d,"cost":%v,"prompt_tokens_details":{"cached_tokens":%d}}}`,
		reason, prompt, completion, prompt+completion, cost, cached)
}

// answerBody is the common "model answers in plain text" response.
func answerBody(text string, cost float64) string {
	return sse(contentChunk(text), finishChunk("stop", cost, 100, 10, 0))
}

// toolBody is a response whose only output is the given calls.
func toolBody(cost float64, calls ...[3]string) string {
	var chunks []string
	for i, c := range calls {
		chunks = append(chunks, toolCallChunk(i, c[0], c[1], c[2]))
	}
	chunks = append(chunks, finishChunk("tool_calls", cost, 100, 10, 0))
	return sse(chunks...)
}

// fakeRouter serves script[i] to the i-th request (the last one repeats) and
// records every request body.
type fakeRouter struct {
	mu     sync.Mutex
	script []string
	status int
	bodies [][]byte
}

func (f *fakeRouter) serve(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		f.mu.Lock()
		n := len(f.bodies)
		f.bodies = append(f.bodies, body)
		script, status := f.script, f.status
		f.mu.Unlock()
		if status != 0 {
			w.WriteHeader(status)
			fmt.Fprint(w, `{"error":{"message":"boom"}}`)
			return
		}
		if n >= len(script) {
			n = len(script) - 1
		}
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, script[n])
	}))
	t.Cleanup(srv.Close)
	return srv
}

// loopRecord is everything observable about one run.
type loopRecord struct {
	bodies []map[string]any
	events []emittedEvent
	result *Result
	err    error
}

type parityCase struct {
	script  []string
	status  int
	setup   func(ctx *tools.Context)
	history []llm.ChatMessage
	message string
	model   string
	routing *llm.ProviderRouting
	reason  *llm.ReasoningParams
	// sessionID mirrors the WithSessionID every real chat turn sets
	// (gateway/turn_thread.go), so the adapter's documented drop of it is
	// exercised rather than assumed.
	sessionID string
	// beforeRun, when set, runs immediately before each loop executes the
	// script. A script whose tools need per-run state (a completion gate, say)
	// re-arms it here, because one parityCase runs twice — once per loop.
	beforeRun func()
}

// run executes the case through one loop against a fresh server.
func (c parityCase) run(t *testing.T, nova bool) loopRecord {
	t.Helper()
	if c.beforeRun != nil {
		c.beforeRun()
	}
	fr := &fakeRouter{script: c.script, status: c.status}
	srv := fr.serve(t)
	model := c.model
	if model == "" {
		model = "test/model"
	}
	client := llm.NewClient(srv.URL, "test-key", model, 0.7, 4096)
	if c.routing != nil {
		client = client.WithProvider(c.routing)
	}
	if c.reason != nil {
		client = client.WithReasoning(c.reason)
	}
	if c.sessionID != "" {
		client = client.WithSessionID(c.sessionID)
	}
	rec := &recordingEmit{}
	ctx := &tools.Context{LLM: client, Emit: rec.emit, MaxTurns: 6}
	if c.setup != nil {
		c.setup(ctx)
	}
	msg := c.message
	if msg == "" {
		msg = "hello"
	}
	var res *Result
	var err error
	if nova {
		res, err = runWithNova(context.Background(), ctx, c.history, msg)
	} else {
		res, err = runBespoke(context.Background(), ctx, c.history, msg)
	}
	out := loopRecord{events: rec.events, result: res, err: err}
	fr.mu.Lock()
	defer fr.mu.Unlock()
	for _, b := range fr.bodies {
		var m map[string]any
		if json.Unmarshal(b, &m) != nil {
			t.Fatalf("server received non-JSON body: %s", b)
		}
		out.bodies = append(out.bodies, m)
	}
	return out
}

// knownWireDifferences are the request fields the two loops are *expected* to
// disagree on (plan §5.1). Anything else differing is a regression.
func normalizeBody(m map[string]any) map[string]any {
	out := map[string]any{}
	for k, v := range m {
		switch k {
		case "stream_options":
			continue // Nova asks for usage in the stream; Polaris reads it unprompted.
		case "session_id":
			// Dropped by the adapter in v1 (plan §5.1): OpenRouter documents it
			// as a no-op while provider.order is set, which every Polaris caller
			// does. TestNovaParity_PlainTurn asserts the drop itself, so this
			// skip cannot hide the field vanishing from both loops.
			continue
		}
		out[k] = v
	}
	return out
}

// diffBodies reports where the two loops' request sequences differ after
// normalization, as JSON paths with short value excerpts.
func diffBodies(a, b []map[string]any) string {
	if len(a) != len(b) {
		return fmt.Sprintf("request count: bespoke %d, nova %d", len(a), len(b))
	}
	var diffs []string
	for i := range a {
		jsonDiff(fmt.Sprintf("req[%d]", i), normalizeBody(a[i]), normalizeBody(b[i]), &diffs)
	}
	return strings.Join(diffs, "\n")
}

func excerpt(v any) string {
	b, _ := json.Marshal(v)
	if len(b) > 160 {
		return string(b[:160]) + "..."
	}
	return string(b)
}

func jsonDiff(path string, a, b any, out *[]string) {
	if len(*out) >= 12 {
		return
	}
	switch av := a.(type) {
	case map[string]any:
		bv, ok := b.(map[string]any)
		if !ok {
			*out = append(*out, fmt.Sprintf("%s: type differs: %s vs %s", path, excerpt(a), excerpt(b)))
			return
		}
		keys := map[string]bool{}
		for k := range av {
			keys[k] = true
		}
		for k := range bv {
			keys[k] = true
		}
		for k := range keys {
			x, xok := av[k]
			y, yok := bv[k]
			switch {
			case !xok:
				*out = append(*out, fmt.Sprintf("%s.%s: only in nova: %s", path, k, excerpt(y)))
			case !yok:
				*out = append(*out, fmt.Sprintf("%s.%s: only in bespoke: %s", path, k, excerpt(x)))
			default:
				jsonDiff(path+"."+k, x, y, out)
			}
		}
	case []any:
		bv, ok := b.([]any)
		if !ok || len(av) != len(bv) {
			*out = append(*out, fmt.Sprintf("%s: array length/type differs: %d vs %d", path, len(av), len(bv)))
			return
		}
		for i := range av {
			jsonDiff(fmt.Sprintf("%s[%d]", path, i), av[i], bv[i], out)
		}
	default:
		if !reflect.DeepEqual(a, b) {
			*out = append(*out, fmt.Sprintf("%s: bespoke %s, nova %s", path, excerpt(a), excerpt(b)))
		}
	}
}

// eventSig renders an event as "type payload-json" for ordered comparison.
func eventSig(e emittedEvent) string {
	b, _ := json.Marshal(e.payload)
	return e.eventType + " " + string(b)
}

func eventSigs(evs []emittedEvent, drop ...string) []string {
	skip := map[string]bool{}
	for _, d := range drop {
		skip[d] = true
	}
	var out []string
	for _, e := range evs {
		if skip[e.eventType] {
			continue
		}
		out = append(out, eventSig(e))
	}
	return out
}

// assertParity runs the case through both loops and requires the same request
// bodies, transcript, and result. Events are compared separately per case,
// because some orderings legitimately differ (see each test).
func assertParity(t *testing.T, c parityCase) (bespoke, nova loopRecord) {
	t.Helper()
	bespoke = c.run(t, false)
	nova = c.run(t, true)
	if d := diffBodies(bespoke.bodies, nova.bodies); d != "" {
		t.Errorf("wire bodies differ:\n%s", d)
	}
	if (bespoke.err == nil) != (nova.err == nil) {
		t.Fatalf("error mismatch: bespoke %v, nova %v", bespoke.err, nova.err)
	}
	if bespoke.err != nil && bespoke.err.Error() != nova.err.Error() {
		t.Errorf("error text: bespoke %q, nova %q", bespoke.err, nova.err)
	}
	br, nr := bespoke.result, nova.result
	if br == nil || nr == nil {
		t.Fatalf("nil result: bespoke %v, nova %v", br, nr)
	}
	if br.Answer != nr.Answer {
		t.Errorf("Answer: bespoke %q, nova %q", br.Answer, nr.Answer)
	}
	if !floatsClose(br.CostUSD, nr.CostUSD) {
		t.Errorf("CostUSD: bespoke %v, nova %v", br.CostUSD, nr.CostUSD)
	}
	for name, pair := range map[string][2]int{
		"PromptTokens": {br.PromptTokens, nr.PromptTokens}, "CompletionTokens": {br.CompletionTokens, nr.CompletionTokens},
		"CacheReadTokens": {br.CacheReadTokens, nr.CacheReadTokens}, "LastPromptTokens": {br.LastPromptTokens, nr.LastPromptTokens},
		"LLMCalls": {br.LLMCalls, nr.LLMCalls}, "ContextTokens": {br.ContextTokens, nr.ContextTokens},
		"TurnCount": {br.TurnCount, nr.TurnCount}, "ResearchCalls": {br.ResearchCalls, nr.ResearchCalls},
	} {
		if pair[0] != pair[1] {
			t.Errorf("%s: bespoke %d, nova %d", name, pair[0], pair[1])
		}
	}
	if !reflect.DeepEqual(transcriptJSON(br.Transcript), transcriptJSON(nr.Transcript)) {
		t.Errorf("Transcript differs:\n--- bespoke\n%s\n--- nova\n%s", mustJSON(br.Transcript), mustJSON(nr.Transcript))
	}
	return bespoke, nova
}

func floatsClose(a, b float64) bool {
	d := a - b
	return d < 1e-9 && d > -1e-9
}

func mustJSON(v any) string {
	b, _ := json.MarshalIndent(v, "", " ")
	return string(b)
}

// transcriptJSON normalizes a transcript to decoded JSON so key order — an
// encoder detail — never counts as a difference.
func transcriptJSON(msgs []llm.ChatMessage) []any {
	b, _ := json.Marshal(msgs)
	var out []any
	_ = json.Unmarshal(b, &out)
	return out
}

func diffEvents(t *testing.T, bespoke, nova []string) {
	t.Helper()
	if reflect.DeepEqual(bespoke, nova) {
		return
	}
	t.Errorf("events differ:\n--- bespoke\n%s\n--- nova\n%s", strings.Join(bespoke, "\n"), strings.Join(nova, "\n"))
}

var _ = bytes.Equal

// stubTool registers a deterministic handler for the test and restores whatever
// was there before (driver_test.go overrides several real handlers globally, so
// parity scripts must not lean on a handler's real behaviour).
func stubTool(t *testing.T, name string, fn tools.HandlerFunc) {
	t.Helper()
	prev, had := tools.Registered(name)
	tools.Register(name, fn)
	t.Cleanup(func() {
		if had {
			tools.Register(name, prev)
		}
	})
}

// quietTool is a stub that just returns "noted".
func quietTool(t *testing.T, name string) {
	stubTool(t, name, func(args string, ctx *tools.Context, callID string) string {
		ctx.Emit("tool_call", map[string]interface{}{"tool": name, "call_id": callID})
		ctx.Emit("tool_result", map[string]interface{}{"tool": name, "result": "noted", "call_id": callID})
		return "noted"
	})
}

// --- N1: wire parity -------------------------------------------------------

func TestNovaParity_PlainTurn(t *testing.T) {
	c := parityCase{script: []string{answerBody("Hello there.", 0.0002)}, sessionID: "thread-parity"}
	bespoke, nova := assertParity(t, c)
	diffEvents(t, eventSigs(bespoke.events), eventSigs(nova.events))
	if nova.result.Answer != "Hello there." {
		t.Errorf("Answer = %q", nova.result.Answer)
	}
	// The one request difference that is a deliberate omission rather than an
	// addition: the bespoke client sends OpenRouter's session_id, the adapter
	// drops it (plan §5.1). Pinned here so "the field went missing from both
	// loops" still fails, which normalizeBody alone would hide.
	if got := bespoke.bodies[0]["session_id"]; got != "thread-parity" {
		t.Errorf("bespoke session_id = %v, want thread-parity", got)
	}
	if got, ok := nova.bodies[0]["session_id"]; ok {
		t.Errorf("nova sent session_id = %v; the adapter drops it in v1", got)
	}
}

func TestNovaParity_RoutingReasoningAndCacheMarker(t *testing.T) {
	yes := true
	for _, model := range []string{"anthropic/claude-test", "deepseek/deepseek-test"} {
		t.Run(model, func(t *testing.T) {
			c := parityCase{
				script:  []string{sse(reasoningChunk("thinking "), reasoningChunk("hard"), contentChunk("Answer."), finishChunk("stop", 0.001, 200, 20, 50))},
				model:   model,
				routing: &llm.ProviderRouting{Order: []string{"DeepInfra", "Baidu"}, AllowFallbacks: &yes},
				reason:  &llm.ReasoningParams{Enabled: &yes, Effort: "high"},
			}
			bespoke, nova := assertParity(t, c)
			diffEvents(t, eventSigs(bespoke.events), eventSigs(nova.events))
			body := nova.bodies[0]
			if _, ok := body["reasoning"]; !ok {
				t.Errorf("reasoning object missing from nova body: %v", body)
			}
			if _, ok := body["reasoning_effort"]; ok {
				t.Errorf("nova sent OpenAI's reasoning_effort; Polaris uses the reasoning object")
			}
			_, marked := body["cache_control"]
			if want := strings.HasPrefix(model, "anthropic/"); marked != want {
				t.Errorf("cache_control present = %v, want %v", marked, want)
			}
			if nova.result.CacheReadTokens != 50 {
				t.Errorf("CacheReadTokens = %d, want 50", nova.result.CacheReadTokens)
			}
		})
	}
}

func TestNovaParity_ToolTurnWithCommentary(t *testing.T) {
	quietTool(t, "parity_think")
	c := parityCase{script: []string{
		sse(contentChunk("Let me think. "), toolCallChunk(0, "call_1", "parity_think", `{}`), finishChunk("tool_calls", 0.0003, 120, 15, 0)),
		answerBody("Done thinking.", 0.0004),
	}}
	bespoke, nova := assertParity(t, c)
	diffEvents(t, eventSigs(bespoke.events), eventSigs(nova.events))
}

func TestNovaParity_ParallelBatchKeepsCallOrder(t *testing.T) {
	var mu sync.Mutex
	started := 0
	both := make(chan struct{})
	for _, name := range []string{"parity_slow", "parity_fast"} {
		name := name
		stubTool(t, name, func(args string, ctx *tools.Context, callID string) string {
			mu.Lock()
			started++
			if started%2 == 0 {
				close(both)
			}
			mu.Unlock()
			// Both calls must be in flight together or this deadlocks the test:
			// that is the "same concurrency as today" half of N3.
			<-both
			ctx.Emit("tool_call", map[string]interface{}{"tool": name, "call_id": callID})
			return name + " done"
		})
	}
	run := func(nova bool) loopRecord {
		mu.Lock()
		started = 0
		both = make(chan struct{})
		mu.Unlock()
		return parityCase{script: []string{
			toolBody(0.0003, [3]string{"call_a", "parity_slow", `{}`}, [3]string{"call_b", "parity_fast", `{}`}),
			answerBody("ok", 0.0001),
		}}.run(t, nova)
	}
	b, n := run(false), run(true)
	if d := diffBodies(b.bodies, n.bodies); d != "" {
		t.Errorf("wire bodies differ:\n%s", d)
	}
	if !reflect.DeepEqual(transcriptJSON(b.result.Transcript), transcriptJSON(n.result.Transcript)) {
		t.Errorf("transcripts differ:\n%s\n---\n%s", mustJSON(b.result.Transcript), mustJSON(n.result.Transcript))
	}
	// The second request must carry both tool results, in call order, with no
	// user message between them.
	msgs := n.bodies[1]["messages"].([]any)
	var roles []string
	for _, m := range msgs {
		roles = append(roles, m.(map[string]any)["role"].(string))
	}
	if got := strings.Join(roles, ","); got != "system,user,assistant,tool,tool" {
		t.Errorf("roles = %s, want system,user,assistant,tool,tool", got)
	}
	if id := msgs[3].(map[string]any)["tool_call_id"]; id != "call_a" {
		t.Errorf("first tool result is for %v, want call_a", id)
	}
}

func TestNovaParity_ResearchNudgesAndStaleStreak(t *testing.T) {
	// No new citations ever, so every call after the first extends a stale streak.
	stubTool(t, "web_search", func(args string, ctx *tools.Context, callID string) string {
		ctx.Emit("tool_call", map[string]interface{}{"tool": "web_search", "call_id": callID})
		ctx.Emit("tool_result", map[string]interface{}{"tool": "web_search", "result": "no results", "call_id": callID})
		return "no results"
	})
	var script []string
	for i := 1; i <= 6; i++ {
		script = append(script, toolBody(0.0001, [3]string{fmt.Sprintf("call_%d", i), "web_search", fmt.Sprintf(`{"query":"q%d"}`, i)}))
	}
	script = append(script, answerBody("Enough.", 0.0001))
	c := parityCase{script: script, setup: func(ctx *tools.Context) { ctx.MaxTurns = 12 }}
	bespoke, nova := assertParity(t, c)
	diffEvents(t, eventSigs(bespoke.events), eventSigs(nova.events))
	if nova.result.ResearchCalls != 6 {
		t.Errorf("ResearchCalls = %d, want 6", nova.result.ResearchCalls)
	}
	var nudgeKinds []string
	for _, e := range nova.events {
		if e.eventType == "agent_nudge" {
			nudgeKinds = append(nudgeKinds, e.payload["args"].(map[string]interface{})["kind"].(string))
		}
	}
	if !strings.Contains(strings.Join(nudgeKinds, ","), "check_in") || !strings.Contains(strings.Join(nudgeKinds, ","), "stale_streak") {
		t.Errorf("nudges fired = %v, want both check_in and stale_streak", nudgeKinds)
	}
}

func TestNovaParity_PseudoToolCallIsHiddenDispatchedAndContinues(t *testing.T) {
	quietTool(t, "parity_think")
	pseudo := `<tool_call><function=parity_think><parameter=thought>hmm</parameter></function></tool_call>`
	c := parityCase{script: []string{
		sse(contentChunk(pseudo), finishChunk("stop", 0.0002, 100, 10, 0)),
		answerBody("Final answer.", 0.0002),
	}}
	bespoke, nova := assertParity(t, c)
	diffEvents(t, eventSigs(bespoke.events), eventSigs(nova.events))
	for _, e := range nova.events {
		if e.eventType == "token" && strings.Contains(fmt.Sprint(e.payload["content"]), "<tool_call>") {
			t.Errorf("pseudo tool call leaked to the live stream: %v", e.payload)
		}
	}
}

func TestNovaParity_EmptyAnswerIsRetried(t *testing.T) {
	c := parityCase{script: []string{
		sse(reasoningChunk("pondering"), finishChunk("stop", 0.0002, 100, 10, 0)),
		answerBody("Now an answer.", 0.0002),
	}}
	bespoke, nova := assertParity(t, c)
	diffEvents(t, eventSigs(bespoke.events), eventSigs(nova.events))
	if nova.result.Answer != "Now an answer." {
		t.Errorf("Answer = %q", nova.result.Answer)
	}
}

func TestNovaParity_AskUserQuestionEndsTurnWithoutAnotherCall(t *testing.T) {
	c := parityCase{
		script: []string{toolBody(0.0003, [3]string{"call_1", "ask_user_question", `{"question":"Which city?","options":["Paris","Rome"]}`})},
		setup:  func(ctx *tools.Context) { ctx.RequestLocation = func() (string, bool) { return "", false } },
	}
	bespoke, nova := assertParity(t, c)
	diffEvents(t, eventSigs(bespoke.events), eventSigs(nova.events))
	if len(nova.bodies) != 1 {
		t.Errorf("model was called %d times, want 1", len(nova.bodies))
	}
	if nova.result.PendingQuestion == nil || nova.result.Answer != "Which city?" {
		t.Errorf("PendingQuestion = %v, Answer = %q", nova.result.PendingQuestion, nova.result.Answer)
	}
}

func TestNovaParity_WrapUpAtTurnBudget(t *testing.T) {
	quietTool(t, "parity_think")
	c := parityCase{
		script: []string{
			toolBody(0.0001, [3]string{"c1", "parity_think", `{}`}),
			toolBody(0.0001, [3]string{"c2", "parity_think", `{}`}),
			answerBody("Best effort answer.", 0.0002),
		},
		setup: func(ctx *tools.Context) { ctx.MaxTurns = 2 },
	}
	bespoke, nova := assertParity(t, c)
	diffEvents(t, eventSigs(bespoke.events), eventSigs(nova.events))
	if len(nova.bodies) != 3 {
		t.Fatalf("requests = %d, want 3 (two turns + wrap-up)", len(nova.bodies))
	}
	last := nova.bodies[2]
	if _, ok := last["tools"]; ok {
		t.Errorf("wrap-up request still offers tools")
	}
	for _, k := range []string{"tool_choice", "parallel_tool_calls"} {
		if _, ok := last[k]; ok {
			t.Errorf("wrap-up request carries %s without tools", k)
		}
	}
}

func TestNovaParity_WrapUpThatStillWritesAPseudoCall(t *testing.T) {
	quietTool(t, "parity_think")
	pseudo := `<tool_call><function=parity_think><parameter=thought>more</parameter></function></tool_call>`
	c := parityCase{
		script: []string{
			toolBody(0.0001, [3]string{"c1", "parity_think", `{}`}),
			sse(contentChunk(pseudo), finishChunk("stop", 0.0001, 100, 10, 0)),
		},
		setup: func(ctx *tools.Context) { ctx.MaxTurns = 1 },
	}
	bespoke, nova := assertParity(t, c)
	diffEvents(t, eventSigs(bespoke.events), eventSigs(nova.events))
	if nova.result.Answer != wrapUpFallback {
		t.Errorf("Answer = %q, want the human fallback", nova.result.Answer)
	}
}

func TestNovaParity_HTTPErrorSurfacesAsAPIError(t *testing.T) {
	c := parityCase{status: http.StatusBadRequest, script: []string{""}}
	bespoke, nova := assertParity(t, c)
	var ae *llm.APIError
	if !asAPIError(nova.err, &ae) || ae.StatusCode != 400 {
		t.Errorf("nova error = %v (%T), want *llm.APIError 400", nova.err, nova.err)
	}
	_ = bespoke
}

func TestNovaParity_HistoryAndModeReinforcement(t *testing.T) {
	c := parityCase{
		history: []llm.ChatMessage{{Role: "user", Content: "first"}, {Role: "assistant", Content: "reply"}},
		message: "follow-up",
		setup:   func(ctx *tools.Context) { ctx.FocusMode = FocusModeBrief },
		script:  []string{answerBody("short.", 0.0001)},
	}
	bespoke, nova := assertParity(t, c)
	diffEvents(t, eventSigs(bespoke.events), eventSigs(nova.events))
	if got := len(nova.result.Transcript); got != 3 {
		t.Errorf("transcript = %d messages, want user + reinforcement + answer", got)
	}
}

func TestNovaParity_ReasoningMarkerLeakIsRefiledAsReasoning(t *testing.T) {
	c := parityCase{script: []string{sse(
		reasoningChunk("real thought"),
		contentChunk("<|reasoning|> still deliberating"),
		finishChunk("stop", 0.0001, 100, 10, 0),
	)}}
	bespoke, nova := assertParity(t, c)
	diffEvents(t, eventSigs(bespoke.events), eventSigs(nova.events))
}

func asAPIError(err error, target **llm.APIError) bool {
	for err != nil {
		if ae, ok := err.(*llm.APIError); ok {
			*target = ae
			return true
		}
		u, ok := err.(interface{ Unwrap() error })
		if !ok {
			return false
		}
		err = u.Unwrap()
	}
	return false
}

// --- N7: images -------------------------------------------------------------

const tinyPNG = "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR4nGNgYGD4DwABBAEAHwFSsQAAAABJRU5ErkJggg=="

func seeTool(t *testing.T, name string, n int) {
	seeToolOrdered(t, name, n, nil, nil)
}

// seeToolOrdered is seeTool with an optional completion gate: a non-nil wait
// blocks the call until it returns, and a non-nil done is called once this
// call's events and queued images are in. Only a test with two image-queuing
// tools in one batch needs it — see
// TestNovaParity_ImagesFollowTheWholeToolBatch.
func seeToolOrdered(t *testing.T, name string, n int, wait, done func()) {
	stubTool(t, name, func(args string, ctx *tools.Context, callID string) string {
		if wait != nil {
			wait()
		}
		ctx.Emit("tool_call", map[string]interface{}{"tool": name, "call_id": callID})
		for i := 0; i < n; i++ {
			ctx.AddPendingImageMessage(llm.ChatMessage{
				Role:      "user",
				Content:   fmt.Sprintf("[image %d from %s]", i+1, callID),
				ImageURLs: []string{"data:image/png;base64," + tinyPNG},
			})
		}
		ctx.Emit("tool_result", map[string]interface{}{"tool": name, "result": "queued", "call_id": callID})
		if done != nil {
			done()
		}
		return "queued"
	})
}

func TestNovaParity_ImagesFollowTheWholeToolBatch(t *testing.T) {
	// The flush appends queued image messages in tool *completion* order in both
	// loops, so with two tools each queueing one image the scheduler decided which
	// caption sat in which message — and the bespoke run and the Nova run then
	// disagreed on the wire (and in the transcript) about one run in twelve. The gate
	// pins parity_see_a to finish first so the comparison below stays a real
	// byte-for-byte one; the calls are still dispatched as one concurrent batch,
	// which TestNovaParity_ParallelBatchKeepsCallOrder is what proves.
	var mu sync.Mutex
	var aDone chan struct{}
	seeToolOrdered(t, "parity_see_a", 1, nil, func() {
		mu.Lock()
		defer mu.Unlock()
		close(aDone)
	})
	seeToolOrdered(t, "parity_see_b", 1, func() {
		mu.Lock()
		done := aDone
		mu.Unlock()
		<-done
	}, nil)
	c := parityCase{
		script: []string{
			toolBody(0.0003, [3]string{"call_a", "parity_see_a", `{}`}, [3]string{"call_b", "parity_see_b", `{}`}),
			answerBody("I see two images.", 0.0002),
		},
		setup: func(ctx *tools.Context) { ctx.Multimodal = true },
		// Each loop gets its own gate: a channel left closed by the previous run
		// would let parity_see_b start before parity_see_a and put the race back.
		beforeRun: func() {
			mu.Lock()
			defer mu.Unlock()
			aDone = make(chan struct{})
		},
	}
	bespoke, nova := assertParity(t, c)
	diffEvents(t, eventSigs(bespoke.events), eventSigs(nova.events))

	msgs := nova.bodies[1]["messages"].([]any)
	var roles []string
	for _, m := range msgs {
		roles = append(roles, m.(map[string]any)["role"].(string))
	}
	// Both tool results first, in call order, and only then the image messages.
	if got := strings.Join(roles, ","); got != "system,user,assistant,tool,tool,user,user" {
		t.Fatalf("roles = %s", got)
	}
	img := msgs[5].(map[string]any)["content"].([]any)
	if len(img) != 2 || img[0].(map[string]any)["type"] != "text" || img[1].(map[string]any)["type"] != "image_url" {
		t.Errorf("image message content = %v, want [text, image_url]", img)
	}
	// The transcript round-trips the pixels so the next turn replays them.
	var carried int
	for _, m := range nova.result.Transcript {
		carried += len(m.ImageURLs)
	}
	if carried != 2 {
		t.Errorf("transcript carries %d images, want 2", carried)
	}
}

// A text-only model never gets pixels: Nova drops image parts for a model whose
// modalities exclude "image". Polaris already gates view_image "see" on
// ctx.Multimodal, so this only bites on a thread that switched from a vision
// model to a text one — where the bespoke loop would have sent the image and
// earned a provider 400.
func TestNova_TextOnlyModelDropsImageParts(t *testing.T) {
	seeTool(t, "parity_see_a", 1)
	c := parityCase{
		script: []string{
			toolBody(0.0003, [3]string{"call_a", "parity_see_a", `{}`}),
			answerBody("ok", 0.0002),
		},
	}
	nova := c.run(t, true)
	for _, m := range nova.bodies[1]["messages"].([]any) {
		if content, ok := m.(map[string]any)["content"].([]any); ok {
			t.Errorf("a text-only model was sent an array-content message: %v", content)
		}
	}
}

// --- cancellation ------------------------------------------------------------

// The stop button cancels reqCtx mid-stream. Run's contract (its doc comment)
// is a graceful early finish — no error, whatever streamed so far as the
// answer — wherever the cancel lands.
func TestNovaParity_StopMidStreamIsAGracefulFinish(t *testing.T) {
	run := func(nova bool) (*Result, error) {
		started := make(chan struct{})
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "text/event-stream")
			fmt.Fprint(w, "data: "+contentChunk("Partial answer so far. And a bit more to flush.")+"\n\n")
			w.(http.Flusher).Flush()
			close(started)
			<-r.Context().Done()
		}))
		defer srv.Close()
		client := llm.NewClient(srv.URL, "k", "test/model", 0.7, 100)
		reqCtx, cancel := context.WithCancel(context.Background())
		go func() { <-started; time.Sleep(50 * time.Millisecond); cancel() }()
		ctx := &tools.Context{LLM: client, Emit: (&recordingEmit{}).emit, MaxTurns: 3}
		if nova {
			return runWithNova(reqCtx, ctx, nil, "hello")
		}
		return runBespoke(reqCtx, ctx, nil, "hello")
	}
	for name, nova := range map[string]bool{"bespoke": false, "nova": true} {
		t.Run(name, func(t *testing.T) {
			res, err := run(nova)
			if err != nil {
				t.Fatalf("a stop must not be an error, got %v", err)
			}
			if !strings.HasPrefix(res.Answer, "Partial answer") {
				t.Errorf("Answer = %q, want the text streamed before the stop", res.Answer)
			}
		})
	}
}
