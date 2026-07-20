package agent

import (
	"context"
	"errors"
	"strings"
	"testing"

	"polaris/llm"
	"polaris/llm/llmtest"
	"polaris/tools"
)

// recordingEmit collects every ctx.Emit call, keyed by event type, so
// tests can assert on exactly what streamed to the (fake) browser.
type recordingEmit struct {
	events []emittedEvent
}

type emittedEvent struct {
	eventType string
	payload   map[string]interface{}
}

func (r *recordingEmit) emit(eventType string, payload map[string]interface{}) {
	r.events = append(r.events, emittedEvent{eventType, payload})
}

func (r *recordingEmit) tokenContent() string {
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
