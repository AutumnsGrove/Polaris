package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"polaris/llm"
	"polaris/llm/llmtest"
	"polaris/search"
	"polaris/tools"
)

// recordingEmit collects every ctx.Emit call, keyed by event type, so
// tests can assert on exactly what streamed to the (fake) browser. Guarded
// by a mutex — Run dispatches a turn's tool calls concurrently (see
// dispatchToolCallsConcurrently), so more than one handler can call Emit
// at the same instant; production's real emit (gateway/turn.go) has the
// same protection for the same reason.
type recordingEmit struct {
	mu     sync.Mutex
	events []emittedEvent
}

type emittedEvent struct {
	eventType string
	payload   map[string]interface{}
}

func (r *recordingEmit) emit(eventType string, payload map[string]interface{}) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.events = append(r.events, emittedEvent{eventType, payload})
}

func (r *recordingEmit) tokenContent() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	var b strings.Builder
	for _, e := range r.events {
		if e.eventType == "token" {
			b.WriteString(e.payload["content"].(string))
		}
	}
	return b.String()
}

func newTestContext(mock *llmtest.MockClient, rec *recordingEmit, maxTurns int) *tools.Context {
	return &tools.Context{
		LLM:      mock,
		Emit:     rec.emit,
		MaxTurns: maxTurns,
	}
}

func TestRun_PlainAnswerNoToolCalls(t *testing.T) {
	mock := &llmtest.MockClient{
		Responses: []llmtest.Response{
			{
				Resp:   &llm.ChatResponse{Content: "Hello there", PromptTokens: 10, CompletionTokens: 5},
				Chunks: []string{"Hello ", "there"},
			},
		},
	}
	rec := &recordingEmit{}
	ctx := newTestContext(mock, rec, 5)

	result, err := Run(context.Background(), ctx, nil, "hi")
	if err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
	if result.Answer != "Hello there" {
		t.Errorf("Answer = %q, want %q", result.Answer, "Hello there")
	}
	if result.ContextTokens != 15 {
		t.Errorf("ContextTokens = %d, want 15", result.ContextTokens)
	}
	if mock.CallCount() != 1 {
		t.Errorf("CallCount = %d, want 1 (should stop at the first plain answer)", mock.CallCount())
	}
	if got := rec.tokenContent(); got != "Hello there" {
		t.Errorf("streamed tokens = %q, want %q", got, "Hello there")
	}
}

func TestRun_ToolCallThenAnswer(t *testing.T) {
	mock := &llmtest.MockClient{
		Responses: []llmtest.Response{
			{
				Resp: &llm.ChatResponse{
					ToolCalls: []llm.ToolCall{{
						ID: "call-1", Type: "function",
						Function: llm.FunctionCall{Name: "think", Arguments: `{"thought":"let me consider this"}`},
					}},
				},
			},
			{Resp: &llm.ChatResponse{Content: "Final answer", PromptTokens: 20, CompletionTokens: 8}, Chunks: []string{"Final answer"}},
		},
	}
	rec := &recordingEmit{}
	ctx := newTestContext(mock, rec, 5)

	result, err := Run(context.Background(), ctx, nil, "what should I do?")
	if err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
	if result.Answer != "Final answer" {
		t.Errorf("Answer = %q, want %q", result.Answer, "Final answer")
	}
	if mock.CallCount() != 2 {
		t.Fatalf("CallCount = %d, want 2", mock.CallCount())
	}

	// The second call's messages must include the tool's result, tagged
	// with the same tool_call_id, so the model can see what "think"
	// produced before answering.
	secondCallMsgs := mock.Calls[1].Messages
	found := false
	for _, m := range secondCallMsgs {
		if m.Role == "tool" && m.ToolCallID == "call-1" && m.Content == "noted" {
			found = true
		}
	}
	if !found {
		t.Errorf("expected a tool-role message with content %q and tool_call_id %q, got %+v", "noted", "call-1", secondCallMsgs)
	}

	// A "thinking" event should have been emitted by the think tool itself.
	sawThinking := false
	for _, e := range rec.events {
		if e.eventType == "thinking" {
			sawThinking = true
		}
	}
	if !sawThinking {
		t.Error("expected a \"thinking\" event from the think tool dispatch")
	}
}

func TestRun_ParallelToolCallsDispatchedAsOneBatch(t *testing.T) {
	// Two independent tool calls in the SAME model turn — the model
	// batched them (see llm.Client's parallel_tool_calls request field).
	mock := &llmtest.MockClient{
		Responses: []llmtest.Response{
			{
				Resp: &llm.ChatResponse{
					ToolCalls: []llm.ToolCall{
						{ID: "call-1", Type: "function", Function: llm.FunctionCall{Name: "think", Arguments: `{"thought":"first"}`}},
						{ID: "call-2", Type: "function", Function: llm.FunctionCall{Name: "think", Arguments: `{"thought":"second"}`}},
					},
				},
			},
			{Resp: &llm.ChatResponse{Content: "Final answer"}, Chunks: []string{"Final answer"}},
		},
	}
	rec := &recordingEmit{}
	ctx := newTestContext(mock, rec, 5)

	result, err := Run(context.Background(), ctx, nil, "two independent things")
	if err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
	if result.Answer != "Final answer" {
		t.Errorf("Answer = %q, want %q", result.Answer, "Final answer")
	}

	secondCallMsgs := mock.Calls[1].Messages
	// The last message before this call must be the assistant's
	// tool_calls message immediately followed by BOTH tool results with
	// nothing else interleaved between them — the exact ordering
	// OpenRouter/DeepSeek rejects with a 400 ("insufficient tool
	// messages following tool_calls message") if violated. Locate the
	// assistant message carrying both calls, then assert the very next
	// two messages are its tool results, in any order among themselves,
	// but with no other role between them.
	assistantIdx := -1
	for i, m := range secondCallMsgs {
		if m.Role == "assistant" && len(m.ToolCalls) == 2 {
			assistantIdx = i
			break
		}
	}
	if assistantIdx == -1 {
		t.Fatalf("expected one assistant message carrying both tool calls, got %+v", secondCallMsgs)
	}
	if assistantIdx+2 >= len(secondCallMsgs) {
		t.Fatalf("not enough messages after the assistant tool_calls message: %+v", secondCallMsgs)
	}
	seenIDs := map[string]bool{}
	for _, m := range secondCallMsgs[assistantIdx+1 : assistantIdx+3] {
		if m.Role != "tool" {
			t.Errorf("message immediately after the tool_calls batch = role %q, want \"tool\" (nothing may be interleaved): %+v", m.Role, m)
		}
		seenIDs[m.ToolCallID] = true
	}
	if !seenIDs["call-1"] || !seenIDs["call-2"] {
		t.Errorf("tool result messages = %+v, want both call-1 and call-2 represented", secondCallMsgs[assistantIdx+1:assistantIdx+3])
	}
}

func TestRun_ParallelToolCalls_CitationsFromBothSurvive(t *testing.T) {
	// Two web_search calls hitting a fake SearXNG concurrently — this is
	// the real-world shape (the model batching two independent searches)
	// and exercises tools.Context.AddCitation's mutex under actual
	// concurrent writers, not just sequential ones.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query().Get("q")
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"results":[{"title":"Result for %s","url":"https://example.com/%s","content":"snippet"}]}`, q, q)
	}))
	defer srv.Close()

	mock := &llmtest.MockClient{
		Responses: []llmtest.Response{
			{
				Resp: &llm.ChatResponse{
					ToolCalls: []llm.ToolCall{
						{ID: "call-1", Type: "function", Function: llm.FunctionCall{Name: "web_search", Arguments: `{"query":"golang"}`}},
						{ID: "call-2", Type: "function", Function: llm.FunctionCall{Name: "web_search", Arguments: `{"query":"rust"}`}},
					},
				},
			},
			{Resp: &llm.ChatResponse{Content: "Final answer"}, Chunks: []string{"Final answer"}},
		},
	}
	rec := &recordingEmit{}
	ctx := newTestContext(mock, rec, 5)
	ctx.SearXNG = search.NewSearXNGClient(srv.URL)

	result, err := Run(context.Background(), ctx, nil, "compare golang and rust")
	if err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
	if len(result.Citations) != 2 {
		t.Fatalf("got %d citations, want 2 (both parallel searches' results): %+v", len(result.Citations), result.Citations)
	}
	urls := map[string]bool{}
	for _, c := range result.Citations {
		urls[c.URL] = true
	}
	if !urls["https://example.com/golang"] || !urls["https://example.com/rust"] {
		t.Errorf("citation URLs = %v, want both example.com/golang and example.com/rust", urls)
	}
}

func TestRun_CommentaryBeforeToolCallEmittedAsDistinctItem(t *testing.T) {
	// The model says something ("Let me check that.") in the SAME
	// response as a real tool call — this used to get silently merged
	// into the final answer's flat content string once every later turn's
	// text piled on top of it. It should instead surface as a distinct
	// "commentary" event, emitted before that turn's tool_call.
	mock := &llmtest.MockClient{
		Responses: []llmtest.Response{
			{
				Resp: &llm.ChatResponse{
					Content: "Let me check that.",
					ToolCalls: []llm.ToolCall{{
						ID: "call-1", Type: "function",
						Function: llm.FunctionCall{Name: "think", Arguments: `{"thought":"checking"}`},
					}},
				},
			},
			{Resp: &llm.ChatResponse{Content: "Final answer"}, Chunks: []string{"Final answer"}},
		},
	}
	rec := &recordingEmit{}
	ctx := newTestContext(mock, rec, 5)

	result, err := Run(context.Background(), ctx, nil, "check something for me")
	if err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
	if result.Answer != "Final answer" {
		t.Errorf("Answer = %q, want %q (commentary shouldn't leak into the real answer)", result.Answer, "Final answer")
	}

	// "think" (the tool call used above) emits a "thinking" event, not
	// "tool_call"/"tool_result" (those are specific to search/read/etc.
	// tools) — using it here as the marker for "after the commentary" is
	// just as valid a proof of ordering.
	commentaryIdx, thinkingIdx := -1, -1
	for i, e := range rec.events {
		if e.eventType == "commentary" && commentaryIdx == -1 {
			commentaryIdx = i
			if got := e.payload["content"]; got != "Let me check that." {
				t.Errorf("commentary content = %v, want %q", got, "Let me check that.")
			}
		}
		if e.eventType == "thinking" && thinkingIdx == -1 {
			thinkingIdx = i
		}
	}
	if commentaryIdx == -1 {
		t.Fatal("expected a \"commentary\" event, got none")
	}
	if thinkingIdx == -1 {
		t.Fatal("expected a \"thinking\" event from the tool dispatch, got none")
	}
	if commentaryIdx > thinkingIdx {
		t.Errorf("commentary emitted at index %d, thinking at %d — commentary should come first", commentaryIdx, thinkingIdx)
	}
}

func TestRun_NoCommentaryEventWhenTurnHasNoLeadingContent(t *testing.T) {
	// A tool call with no preceding text shouldn't produce an empty
	// "commentary" event — nothing to show, nothing to emit.
	mock := &llmtest.MockClient{
		Responses: []llmtest.Response{
			{
				Resp: &llm.ChatResponse{
					ToolCalls: []llm.ToolCall{{
						ID: "call-1", Type: "function",
						Function: llm.FunctionCall{Name: "think", Arguments: `{"thought":"checking"}`},
					}},
				},
			},
			{Resp: &llm.ChatResponse{Content: "Final answer"}, Chunks: []string{"Final answer"}},
		},
	}
	rec := &recordingEmit{}
	ctx := newTestContext(mock, rec, 5)

	if _, err := Run(context.Background(), ctx, nil, "do the thing"); err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
	for _, e := range rec.events {
		if e.eventType == "commentary" {
			t.Errorf("unexpected commentary event: %+v", e)
		}
	}
}

func TestRun_MaxTurnsExhausted_ForcesWrapUp(t *testing.T) {
	// MaxTurns=1: the loop's single iteration is consumed by a tool call,
	// so it should fall through to the forced wrap-up path (a second,
	// no-tools call) rather than looping again or erroring.
	mock := &llmtest.MockClient{
		Responses: []llmtest.Response{
			{
				Resp: &llm.ChatResponse{
					ToolCalls: []llm.ToolCall{{
						ID: "call-1", Type: "function",
						Function: llm.FunctionCall{Name: "think", Arguments: `{"thought":"still working"}`},
					}},
				},
			},
			{Resp: &llm.ChatResponse{Content: "Best guess given what I have"}, Chunks: []string{"Best guess given what I have"}},
		},
	}
	rec := &recordingEmit{}
	ctx := newTestContext(mock, rec, 1)

	result, err := Run(context.Background(), ctx, nil, "a hard multi-step question")
	if err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
	if result.Answer != "Best guess given what I have" {
		t.Errorf("Answer = %q, want the wrap-up content", result.Answer)
	}
	if mock.CallCount() != 2 {
		t.Fatalf("CallCount = %d, want 2 (tool-call turn + forced wrap-up)", mock.CallCount())
	}

	wrapUpMsgs := mock.Calls[1].Messages
	last := wrapUpMsgs[len(wrapUpMsgs)-1]
	if !strings.Contains(last.Content, "Wrap up now") {
		t.Errorf("last message before wrap-up call = %q, want it to instruct wrapping up", last.Content)
	}
}

func TestRun_PseudoToolCallDetectedAndDispatched(t *testing.T) {
	// A provider that falls back to writing the tool call as literal XML
	// in the content field instead of populating ToolCalls (see
	// pseudocall.go) must still get dispatched, and none of that raw
	// syntax should leak out as a "token" event.
	pseudo := `<tool_call><function=think><parameter=thought>reasoning as text</parameter></function></tool_call>`
	mock := &llmtest.MockClient{
		Responses: []llmtest.Response{
			{Resp: &llm.ChatResponse{Content: pseudo}, Chunks: []string{pseudo}},
			{Resp: &llm.ChatResponse{Content: "Real answer"}, Chunks: []string{"Real answer"}},
		},
	}
	rec := &recordingEmit{}
	ctx := newTestContext(mock, rec, 5)

	result, err := Run(context.Background(), ctx, nil, "question")
	if err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
	if result.Answer != "Real answer" {
		t.Errorf("Answer = %q, want %q", result.Answer, "Real answer")
	}
	if got := rec.tokenContent(); strings.Contains(got, "<tool_call>") {
		t.Errorf("streamed tokens leaked pseudo tool call syntax: %q", got)
	}
	if mock.CallCount() != 2 {
		t.Errorf("CallCount = %d, want 2 (pseudo call dispatched, then a real answer)", mock.CallCount())
	}
}

func TestRun_InjectsResearchCheckInAfterInterval(t *testing.T) {
	// A fake SearXNG that always returns one distinct result — enough for
	// handleWebSearch to succeed and add a citation each time, so
	// len(ctx.Citations) is meaningfully non-zero by the check-in point.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"results": []map[string]interface{}{
				{"title": "Result", "url": "https://example.com/" + r.URL.Query().Get("q"), "content": "text", "score": 5.0},
			},
		})
	}))
	defer srv.Close()

	// researchCheckInInterval calls to web_search, then a plain final answer.
	responses := make([]llmtest.Response, 0, researchCheckInInterval+1)
	for i := 0; i < researchCheckInInterval; i++ {
		responses = append(responses, llmtest.Response{
			Resp: &llm.ChatResponse{
				ToolCalls: []llm.ToolCall{{
					ID: "call-1", Type: "function",
					Function: llm.FunctionCall{Name: "web_search", Arguments: `{"query":"q"}`},
				}},
			},
		})
	}
	responses = append(responses, llmtest.Response{
		Resp:   &llm.ChatResponse{Content: "Final answer"},
		Chunks: []string{"Final answer"},
	})

	mock := &llmtest.MockClient{Responses: responses}
	rec := &recordingEmit{}
	ctx := newTestContext(mock, rec, researchCheckInInterval+5)
	ctx.SearXNG = search.NewSearXNGClient(srv.URL)

	if _, err := Run(context.Background(), ctx, nil, "a question needing several searches"); err != nil {
		t.Fatalf("Run returned error: %v", err)
	}

	// The call right after the researchCheckInInterval-th web_search must
	// include the check-in nudge as the last message.
	checkInCallMsgs := mock.Calls[researchCheckInInterval].Messages
	last := checkInCallMsgs[len(checkInCallMsgs)-1]
	if last.Role != "user" || !strings.Contains(last.Content, "Checkpoint") {
		t.Errorf("message before call %d = %+v, want a Checkpoint check-in", researchCheckInInterval, last)
	}
}

// fakeSearXNGFixedURL always returns the same single result, regardless
// of query — every web_search call "finds" a source ctx.Citations has
// already deduplicated away, so len(ctx.Citations) never grows past 1.
// Simulates the real "echo chamber retrieval" failure: distinct
// rephrased queries that keep surfacing the same source.
func fakeSearXNGFixedURL(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"results": []map[string]interface{}{
				{"title": "Same Result", "url": "https://example.com/always-the-same", "content": "text", "score": 5.0},
			},
		})
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestRun_InjectsStaleStreakMessageWhenCitationsDontGrow(t *testing.T) {
	srv := fakeSearXNGFixedURL(t)

	// The first call always "discovers" a citation (there's nothing to
	// compare against yet), so the stale streak only starts accumulating
	// from the second call onward — staleStreakThreshold+1 total calls
	// are needed to actually trip it.
	totalCalls := staleStreakThreshold + 1
	responses := make([]llmtest.Response, 0, totalCalls+1)
	for i := 0; i < totalCalls; i++ {
		responses = append(responses, llmtest.Response{
			Resp: &llm.ChatResponse{
				ToolCalls: []llm.ToolCall{{
					ID: "call-1", Type: "function",
					Function: llm.FunctionCall{Name: "web_search", Arguments: `{"query":"q"}`},
				}},
			},
		})
	}
	responses = append(responses, llmtest.Response{
		Resp:   &llm.ChatResponse{Content: "Final answer"},
		Chunks: []string{"Final answer"},
	})

	mock := &llmtest.MockClient{Responses: responses}
	rec := &recordingEmit{}
	ctx := newTestContext(mock, rec, totalCalls+5)
	ctx.SearXNG = search.NewSearXNGClient(srv.URL)

	if _, err := Run(context.Background(), ctx, nil, "a question that keeps finding the same source"); err != nil {
		t.Fatalf("Run returned error: %v", err)
	}

	// After staleStreakThreshold consecutive calls that added no new
	// citations, the very next LLM call must have been warned about it.
	msgs := mock.Calls[totalCalls].Messages
	found := false
	for _, m := range msgs {
		if m.Role == "user" && strings.Contains(m.Content, "zero new sources") {
			found = true
		}
	}
	if !found {
		t.Errorf("messages before call %d = %+v, want a stale-streak warning", totalCalls, msgs)
	}
}

func TestRun_CheckInAndStaleStreakCanCoexistOnSameCall(t *testing.T) {
	// First 3 queries each surface a genuinely new URL; the last 2 reuse
	// the 3rd query's URL, so by researchCheckInInterval (5) calls, the
	// stale streak (threshold 2) has also just tripped — both signals
	// measure different things and neither should suppress the other.
	if researchCheckInInterval != 5 || staleStreakThreshold != 2 {
		t.Skip("test assumes researchCheckInInterval=5 and staleStreakThreshold=2")
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"results": []map[string]interface{}{
				{"title": "Result", "url": "https://example.com/" + r.URL.Query().Get("q"), "content": "text", "score": 5.0},
			},
		})
	}))
	defer srv.Close()

	queries := []string{"q1", "q2", "q3", "q3", "q3"} // last two repeat q3's URL
	responses := make([]llmtest.Response, 0, len(queries)+1)
	for _, q := range queries {
		responses = append(responses, llmtest.Response{
			Resp: &llm.ChatResponse{
				ToolCalls: []llm.ToolCall{{
					ID: "call-1", Type: "function",
					Function: llm.FunctionCall{Name: "web_search", Arguments: `{"query":"` + q + `"}`},
				}},
			},
		})
	}
	responses = append(responses, llmtest.Response{
		Resp:   &llm.ChatResponse{Content: "Final answer"},
		Chunks: []string{"Final answer"},
	})

	mock := &llmtest.MockClient{Responses: responses}
	rec := &recordingEmit{}
	ctx := newTestContext(mock, rec, len(queries)+5)
	ctx.SearXNG = search.NewSearXNGClient(srv.URL)

	if _, err := Run(context.Background(), ctx, nil, "a question"); err != nil {
		t.Fatalf("Run returned error: %v", err)
	}

	msgs := mock.Calls[len(queries)].Messages
	var sawCheckpoint, sawStaleStreak bool
	for _, m := range msgs {
		if m.Role != "user" {
			continue
		}
		if strings.Contains(m.Content, "Checkpoint") {
			sawCheckpoint = true
		}
		if strings.Contains(m.Content, "zero new sources") {
			sawStaleStreak = true
		}
	}
	if !sawCheckpoint || !sawStaleStreak {
		t.Errorf("messages before call %d = %+v, want BOTH a Checkpoint and a stale-streak warning present together", len(queries), msgs)
	}
}

func TestRun_LLMErrorPropagates(t *testing.T) {
	mock := &llmtest.MockClient{
		Responses: []llmtest.Response{
			{Err: errors.New("connection reset")},
		},
	}
	rec := &recordingEmit{}
	ctx := newTestContext(mock, rec, 5)

	_, err := Run(context.Background(), ctx, nil, "hi")
	if err == nil {
		t.Fatal("expected an error, got nil")
	}
	if !strings.Contains(err.Error(), "connection reset") {
		t.Errorf("err = %v, want it to wrap the underlying failure", err)
	}
}
