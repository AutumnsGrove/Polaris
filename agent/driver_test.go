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
	"time"

	"polaris/embed"
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

func TestLoadSystemPrompt_AppliesFocusModeInstruction(t *testing.T) {
	base := loadSystemPrompt(&tools.Context{}, false, "", false, false)
	brief := loadSystemPrompt(&tools.Context{}, false, FocusModeBrief, false, false)
	if brief == base {
		t.Error("loadSystemPrompt(false, FocusModeBrief, false) should differ from the no-focus-mode prompt")
	}
	if !strings.Contains(brief, "Focus mode: Brief") {
		t.Errorf("prompt = %q, want it to contain the Brief focus mode instruction", brief)
	}
}

func TestLoadSystemPrompt_UnknownFocusModeIsNoOp(t *testing.T) {
	base := loadSystemPrompt(&tools.Context{}, false, "", false, false)
	unknown := loadSystemPrompt(&tools.Context{}, false, "not_a_real_mode", false, false)
	if base != unknown {
		t.Errorf("an unrecognized focus mode should leave the prompt unchanged, got a difference")
	}
}

func TestRun_FocusModeInstructionReachesSystemPrompt(t *testing.T) {
	mock := &llmtest.MockClient{
		Responses: []llmtest.Response{
			{Resp: &llm.ChatResponse{Content: "answer"}, Chunks: []string{"answer"}},
		},
	}
	rec := &recordingEmit{}
	ctx := newTestContext(mock, rec, 5)
	ctx.FocusMode = FocusModeAcademic

	if _, err := Run(context.Background(), ctx, nil, "what is dark matter"); err != nil {
		t.Fatalf("Run returned error: %v", err)
	}

	systemMsg := mock.Calls[0].Messages[0]
	if systemMsg.Role != "system" {
		t.Fatalf("Messages[0].Role = %q, want %q", systemMsg.Role, "system")
	}
	if !strings.Contains(systemMsg.Content, "Focus mode: Academic") {
		t.Errorf("system prompt sent to the LLM doesn't contain the Academic focus mode instruction: %q", systemMsg.Content)
	}
}

func TestRun_DoesNotBlockOnEmbedWarmUp(t *testing.T) {
	// The whole point of warmUpEmbedClient firing in a goroutine is that a
	// slow (or cold-starting) Ollama never holds up the actual turn — Run
	// must return based on the LLM's own response time, not wait around
	// for the warm-up request it kicked off.
	requestArrived := make(chan struct{}, 1)
	embedSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(200 * time.Millisecond) // stand-in for a slow/cold-starting Ollama
		select {
		case requestArrived <- struct{}{}:
		default:
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{"embedding": []float32{0.1, 0.2}})
	}))
	defer embedSrv.Close()

	mock := &llmtest.MockClient{
		Responses: []llmtest.Response{
			{Resp: &llm.ChatResponse{Content: "answer"}, Chunks: []string{"answer"}},
		},
	}
	rec := &recordingEmit{}
	ctx := newTestContext(mock, rec, 5)
	ctx.Embed = embed.NewClient(embedSrv.URL, "")

	start := time.Now()
	if _, err := Run(context.Background(), ctx, nil, "hi"); err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
	if elapsed := time.Since(start); elapsed > 150*time.Millisecond {
		t.Errorf("Run took %v, want it to return well before the 200ms embed warm-up finishes", elapsed)
	}

	select {
	case <-requestArrived:
	case <-time.After(2 * time.Second):
		t.Error("warm-up request never reached the fake Ollama server")
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

// TestRun_AskUserQuestionEndsTurn covers the whole "pause the turn instead
// of blocking in memory" design: ask_user_question must make Run return
// immediately after dispatching it — never looping back to the model for
// a second completion. Only one mock response is queued on purpose; if
// Run incorrectly called the LLM again, MockClient would run out of
// responses and this test would fail loudly instead of silently passing.
func TestRun_AskUserQuestionEndsTurn(t *testing.T) {
	mock := &llmtest.MockClient{
		Responses: []llmtest.Response{
			{
				Resp: &llm.ChatResponse{
					ToolCalls: []llm.ToolCall{{
						ID: "call-1", Type: "function",
						Function: llm.FunctionCall{
							Name:      "ask_user_question",
							Arguments: `{"question":"What's your current location?","options":["Share my location"],"wants_location":true}`,
						},
					}},
					PromptTokens: 15, CompletionTokens: 5,
				},
			},
		},
	}
	rec := &recordingEmit{}
	ctx := newTestContext(mock, rec, 5)

	result, err := Run(context.Background(), ctx, nil, "find me a coffee shop")
	if err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
	if mock.CallCount() != 1 {
		t.Fatalf("CallCount = %d, want 1 (Run must not loop back to the model after ask_user_question)", mock.CallCount())
	}
	if result.Answer != "What's your current location?" {
		t.Errorf("Answer = %q, want the literal question text", result.Answer)
	}
	if result.PendingQuestion == nil {
		t.Fatal("PendingQuestion is nil, want it populated")
	}
	if result.PendingQuestion.Question != "What's your current location?" {
		t.Errorf("PendingQuestion.Question = %q", result.PendingQuestion.Question)
	}
	if !result.PendingQuestion.WantsLocation {
		t.Error("PendingQuestion.WantsLocation = false, want true")
	}
	if len(result.PendingQuestion.Options) != 1 || result.PendingQuestion.Options[0] != "Share my location" {
		t.Errorf("PendingQuestion.Options = %v", result.PendingQuestion.Options)
	}
	// The question text must also reach the frontend as a "token" event —
	// a tool call's arguments never flow through the normal content-chunk
	// onChunk path, so without an explicit emit here a live session's
	// turn.content stays empty until the next reload re-reads it from the
	// persisted message instead (a real bug caught by manual testing).
	if got := rec.tokenContent(); got != "What's your current location?" {
		t.Errorf("streamed token content = %q, want the question text", got)
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
	ctx.SearXNG = search.NewSearXNGClient(srv.URL, nil)

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

func TestRun_EmptyContentNoToolCallsRetriesInsteadOfReturningEmpty(t *testing.T) {
	// A turn with no tool calls and no content used to be trusted as a
	// valid final answer outright — the exact shape a model produces when
	// it burns its whole response on private reasoning (visible only via
	// onReasoning) without ever committing to a tool call or answer text.
	// Confirms Run nudges and retries instead of returning that as Result.
	mock := &llmtest.MockClient{
		Responses: []llmtest.Response{
			{Resp: &llm.ChatResponse{Content: ""}},
			{Resp: &llm.ChatResponse{Content: "Real answer"}, Chunks: []string{"Real answer"}},
		},
	}
	rec := &recordingEmit{}
	ctx := newTestContext(mock, rec, 5)

	result, err := Run(context.Background(), ctx, nil, "a question the model reasons about but never answers")
	if err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
	if result.Answer != "Real answer" {
		t.Errorf("Answer = %q, want %q (the retry's answer)", result.Answer, "Real answer")
	}
	if mock.CallCount() != 2 {
		t.Fatalf("CallCount = %d, want 2 (empty turn + retry)", mock.CallCount())
	}

	retryMsgs := mock.Calls[1].Messages
	last := retryMsgs[len(retryMsgs)-1]
	if !strings.Contains(last.Content, "no answer and no tool call") {
		t.Errorf("last message before retry = %q, want the empty-answer nudge", last.Content)
	}

	foundNudge := false
	for _, e := range rec.events {
		if e.eventType == "agent_nudge" {
			if args, ok := e.payload["args"].(map[string]interface{}); ok && args["kind"] == "empty_answer" {
				foundNudge = true
			}
		}
	}
	if !foundNudge {
		t.Error("expected an agent_nudge event with kind=empty_answer")
	}
}

func TestRun_EmptyWrapUpStaysEmptyForCallerToHandle(t *testing.T) {
	// The forced wrap-up (last LLM call Run makes, after maxTurns runs
	// out) deliberately does NOT paper over an empty answerText the way
	// the main loop's mid-turn retry does above — there's no budget left
	// to retry, and callers already have their own contract for a
	// genuinely empty Result.Answer (gateway/turn.go's handleTurn checks
	// for it and surfaces a proper "error" event instead of persisting a
	// blank assistant turn — see TestWebSocket_EmptyAnswerSurfacesAsErrorEvent
	// in gateway/ws_test.go). Silently rewriting it into placeholder text
	// here would defeat that downstream check for every caller.
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
			{Resp: &llm.ChatResponse{Content: ""}},
		},
	}
	rec := &recordingEmit{}
	ctx := newTestContext(mock, rec, 1)

	result, err := Run(context.Background(), ctx, nil, "a hard multi-step question")
	if err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
	if result.Answer != "" {
		t.Errorf("Answer = %q, want empty — the wrap-up must not fabricate placeholder text", result.Answer)
	}
}

func TestRun_InjectsQuerySimilarityWarningAfterRepeatedNearDuplicateQueries(t *testing.T) {
	// A fake Ollama server that always returns the same embedding
	// regardless of the query text — this test is about the tracking/
	// threshold wiring in query_similarity.go, not about whether
	// nomic-embed-text itself judges two strings similar, so pinning the
	// embedding removes that variable and makes every consecutive pair
	// deterministically identical (cosine similarity 1.0).
	embedSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{"embedding": []float32{0.9, 0.1, 0.1}})
	}))
	defer embedSrv.Close()

	webSearchResp := func(id, query string) *llm.ChatResponse {
		return &llm.ChatResponse{ToolCalls: []llm.ToolCall{{
			ID: id, Type: "function",
			Function: llm.FunctionCall{Name: "web_search", Arguments: fmt.Sprintf(`{"query":%q}`, query)},
		}}}
	}
	mock := &llmtest.MockClient{
		Responses: []llmtest.Response{
			{Resp: webSearchResp("1", "cat facts")},
			{Resp: webSearchResp("2", "cat facts info")},
			{Resp: webSearchResp("3", "facts about cats")},
			{Resp: &llm.ChatResponse{Content: "Done"}, Chunks: []string{"Done"}},
		},
	}
	rec := &recordingEmit{}
	ctx := newTestContext(mock, rec, 10)
	ctx.Embed = embed.NewClient(embedSrv.URL, "")

	if _, err := Run(context.Background(), ctx, nil, "tell me about cats"); err != nil {
		t.Fatalf("Run returned error: %v", err)
	}

	// After the 2nd consecutive near-duplicate query (querySimilarityStreakThreshold),
	// the very next LLM call must have been warned about it.
	msgs := mock.Calls[3].Messages
	found := false
	for _, m := range msgs {
		if m.Role == "user" && strings.Contains(m.Content, "semantically almost identical") {
			found = true
		}
	}
	if !found {
		t.Errorf("messages before call 3 = %+v, want a query-similarity warning", msgs)
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
	ctx.SearXNG = search.NewSearXNGClient(srv.URL, nil)

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

func TestRun_DeepResearchDelaysCheckInMessage(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"results": []map[string]interface{}{
				{"title": "Result", "url": "https://example.com/" + r.URL.Query().Get("q"), "content": "text", "score": 5.0},
			},
		})
	}))
	defer srv.Close()

	// Deep Research doubles researchCheckInInterval — the plain interval's
	// worth of calls should NOT trigger a check-in the way
	// TestRun_InjectsResearchCheckInAfterInterval proves it normally would.
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
	ctx.SearXNG = search.NewSearXNGClient(srv.URL, nil)
	ctx.DeepResearch = true

	if _, err := Run(context.Background(), ctx, nil, "a question needing several searches"); err != nil {
		t.Fatalf("Run returned error: %v", err)
	}

	checkInCallMsgs := mock.Calls[researchCheckInInterval].Messages
	last := checkInCallMsgs[len(checkInCallMsgs)-1]
	if last.Role == "user" && strings.Contains(last.Content, "Checkpoint") {
		t.Errorf("check-in fired at the normal interval under Deep Research — want it delayed to %dx", deepResearchCheckInMultiplier)
	}
}

func TestRun_DeepResearchRaisesMaxTurns(t *testing.T) {
	// ctx.MaxTurns alone would cut the loop off after defaultMaxTurns-ish
	// turns and force a wrap-up — set it deliberately low (3) and prove
	// Deep Research's multiplier gives the loop enough room to run a 4th
	// real tool-call turn instead of hitting the forced-wrapup path.
	mock := &llmtest.MockClient{
		Responses: []llmtest.Response{
			{Resp: &llm.ChatResponse{ToolCalls: []llm.ToolCall{{ID: "1", Type: "function", Function: llm.FunctionCall{Name: "think", Arguments: `{"thought":"a"}`}}}}},
			{Resp: &llm.ChatResponse{ToolCalls: []llm.ToolCall{{ID: "2", Type: "function", Function: llm.FunctionCall{Name: "think", Arguments: `{"thought":"b"}`}}}}},
			{Resp: &llm.ChatResponse{ToolCalls: []llm.ToolCall{{ID: "3", Type: "function", Function: llm.FunctionCall{Name: "think", Arguments: `{"thought":"c"}`}}}}},
			{Resp: &llm.ChatResponse{Content: "Final answer"}, Chunks: []string{"Final answer"}},
		},
	}
	rec := &recordingEmit{}
	ctx := newTestContext(mock, rec, 3)
	ctx.DeepResearch = true

	result, err := Run(context.Background(), ctx, nil, "question")
	if err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
	if result.Answer != "Final answer" {
		t.Errorf("Answer = %q, want the real final answer, not a forced wrap-up", result.Answer)
	}
	if mock.CallCount() != 4 {
		t.Errorf("CallCount = %d, want 4 (3 tool-call turns + real answer) — MaxTurns=3 without the Deep Research multiplier would have forced a wrap-up instead", mock.CallCount())
	}
}

func TestLoadSystemPrompt_AppliesDeepResearchInstruction(t *testing.T) {
	base := loadSystemPrompt(&tools.Context{}, false, "", false, false)
	deep := loadSystemPrompt(&tools.Context{}, false, "", true, false)
	if deep == base {
		t.Error("loadSystemPrompt(false, \"\", true) should differ from the non-deep-research prompt")
	}
	if !strings.Contains(deep, "Deep Research mode is active") {
		t.Errorf("prompt = %q, want it to contain the deep research instruction", deep)
	}
}

func TestLoadSystemPrompt_AppliesNoResearchInstruction(t *testing.T) {
	base := loadSystemPrompt(&tools.Context{}, false, "", false, false)
	chat := loadSystemPrompt(&tools.Context{}, false, "", false, true)
	if chat == base {
		t.Error("loadSystemPrompt(false, \"\", false, true) should differ from the normal prompt")
	}
	if !strings.Contains(chat, "Chat mode is active") {
		t.Errorf("prompt = %q, want it to contain the no-research instruction", chat)
	}
}

func TestLoadSystemPrompt_NoResearchExcludesResearchToolsFromToolsList(t *testing.T) {
	ctx := &tools.Context{NoResearch: true}
	prompt := loadSystemPrompt(ctx, false, "", false, true)
	if strings.Contains(prompt, "- web_search:") {
		t.Errorf("prompt = %q, want web_search excluded from {tools} when NoResearch is true", prompt)
	}
	if !strings.Contains(prompt, "- think:") {
		t.Errorf("prompt = %q, want non-research tools like think to remain listed", prompt)
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
	ctx.SearXNG = search.NewSearXNGClient(srv.URL, nil)

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
	ctx.SearXNG = search.NewSearXNGClient(srv.URL, nil)

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

// TestRun_ErrorAfterPartialCostStillReportsCost covers a real gap found
// live while benchmarking Deep Research: a turn that makes several real,
// billed LLM calls before a later call fails (e.g. llm/client.go's
// "truncated tool call arguments" on a long research turn) previously
// discarded every dollar already spent — `return nil, err` threw away
// totalCost along with everything else, so callers had no way to know
// real money was spent on a turn that ultimately errored. Run must
// return a non-nil Result carrying the accumulated cost alongside the
// error, not just the error — existing callers that only check err and
// ignore result on failure are unaffected either way.
func TestRun_ErrorAfterPartialCostStillReportsCost(t *testing.T) {
	mock := &llmtest.MockClient{
		Responses: []llmtest.Response{
			{
				Resp: &llm.ChatResponse{
					CostUSD: 0.01,
					ToolCalls: []llm.ToolCall{{
						ID: "call-1", Type: "function",
						Function: llm.FunctionCall{Name: "think", Arguments: `{"thought":"let me consider this"}`},
					}},
				},
			},
			{Err: errors.New("truncated tool call arguments")},
		},
	}
	rec := &recordingEmit{}
	ctx := newTestContext(mock, rec, 5)

	result, err := Run(context.Background(), ctx, nil, "hi")
	if err == nil {
		t.Fatal("expected an error, got nil")
	}
	if result == nil {
		t.Fatal("Run returned a nil Result alongside the error — the first call's $0.01 is now unrecoverable")
	}
	if result.CostUSD != 0.01 {
		t.Errorf("result.CostUSD = %v, want 0.01 (the cost already accrued before the failing call)", result.CostUSD)
	}
}

// TestRun_WrapUpErrorStillReportsCost covers the same gap at the other
// error return site — the forced wrap-up call (after maxTurns is
// exhausted) failing must not discard whatever the loop itself already
// spent getting there.
func TestRun_WrapUpErrorStillReportsCost(t *testing.T) {
	responses := []llmtest.Response{}
	for i := 0; i < 2; i++ {
		responses = append(responses, llmtest.Response{
			Resp: &llm.ChatResponse{
				CostUSD: 0.02,
				ToolCalls: []llm.ToolCall{{
					ID: fmt.Sprintf("call-%d", i), Type: "function",
					Function: llm.FunctionCall{Name: "think", Arguments: `{"thought":"still going"}`},
				}},
			},
		})
	}
	responses = append(responses, llmtest.Response{Err: errors.New("wrap-up call failed")})
	mock := &llmtest.MockClient{Responses: responses}
	rec := &recordingEmit{}
	ctx := newTestContext(mock, rec, 2) // maxTurns=2, so the 3rd call is the forced wrap-up

	result, err := Run(context.Background(), ctx, nil, "hi")
	if err == nil {
		t.Fatal("expected an error, got nil")
	}
	if result == nil {
		t.Fatal("Run returned a nil Result alongside the wrap-up error — the loop's $0.04 is now unrecoverable")
	}
	if result.CostUSD != 0.04 {
		t.Errorf("result.CostUSD = %v, want 0.04 (both loop calls' cost, accrued before the wrap-up call failed)", result.CostUSD)
	}
}
