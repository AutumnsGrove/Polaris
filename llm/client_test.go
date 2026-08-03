package llm

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// sseServer spins up a fake OpenRouter /chat/completions endpoint that
// writes the given raw SSE lines verbatim, one at a time, flushing after
// each — real enough to exercise doRequest's scanner-based parsing
// without needing a real OpenRouter connection.
func sseServer(t *testing.T, lines []string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		flusher, ok := w.(http.Flusher)
		if !ok {
			t.Fatal("ResponseWriter doesn't support flushing")
		}
		for _, line := range lines {
			fmt.Fprintf(w, "%s\n", line)
			flusher.Flush()
		}
	}))
}

func TestChatCompletionStreaming_AssemblesContentAndUsage(t *testing.T) {
	srv := sseServer(t, []string{
		`data: {"choices":[{"delta":{"content":"Hello, "}}]}`,
		`data: {"choices":[{"delta":{"content":"world!"}}]}`,
		`data: {"choices":[{"delta":{},"finish_reason":"stop"}],"usage":{"prompt_tokens":12,"completion_tokens":4,"total_tokens":16,"cost":0.0007}}`,
		`data: [DONE]`,
	})
	defer srv.Close()

	client := NewClient(srv.URL, "test-key", "test/model", 0.4, 1000)

	var streamed strings.Builder
	resp, err := client.ChatCompletionStreaming(context.Background(), []ChatMessage{{Role: "user", Content: "hi"}},
		func(chunk string) { streamed.WriteString(chunk) }, nil)
	if err != nil {
		t.Fatalf("ChatCompletionStreaming returned error: %v", err)
	}

	if resp.Content != "Hello, world!" {
		t.Errorf("Content = %q, want %q", resp.Content, "Hello, world!")
	}
	if streamed.String() != "Hello, world!" {
		t.Errorf("streamed chunks = %q, want %q", streamed.String(), "Hello, world!")
	}
	if resp.PromptTokens != 12 || resp.CompletionTokens != 4 || resp.TotalTokens != 16 {
		t.Errorf("token counts = %d/%d/%d, want 12/4/16", resp.PromptTokens, resp.CompletionTokens, resp.TotalTokens)
	}
	if resp.CostUSD != 0.0007 {
		t.Errorf("CostUSD = %v, want 0.0007", resp.CostUSD)
	}
	if resp.FinishReason != "stop" {
		t.Errorf("FinishReason = %q, want %q", resp.FinishReason, "stop")
	}
}

func TestChatCompletionWithTools_AssemblesToolCallAcrossChunks(t *testing.T) {
	srv := sseServer(t, []string{
		`data: {"choices":[{"delta":{"tool_calls":[{"index":0,"id":"call_1","type":"function","function":{"name":"web_search","arguments":""}}]}}]}`,
		`data: {"choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":"{\"query\":"}}]}}]}`,
		`data: {"choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":"\"golang\"}"}}]}}]}`,
		`data: {"choices":[{"delta":{},"finish_reason":"tool_calls"}]}`,
		`data: [DONE]`,
	})
	defer srv.Close()

	client := NewClient(srv.URL, "test-key", "test/model", 0.4, 1000)
	resp, err := client.ChatCompletionWithTools(context.Background(), []ChatMessage{{Role: "user", Content: "search for golang"}}, nil, nil, nil)
	if err != nil {
		t.Fatalf("ChatCompletionWithTools returned error: %v", err)
	}

	if len(resp.ToolCalls) != 1 {
		t.Fatalf("got %d tool calls, want 1", len(resp.ToolCalls))
	}
	call := resp.ToolCalls[0]
	if call.ID != "call_1" || call.Function.Name != "web_search" {
		t.Errorf("call = %+v, want id=call_1 name=web_search", call)
	}
	if call.Function.Arguments != `{"query":"golang"}` {
		t.Errorf("Arguments = %q, want assembled across chunks", call.Function.Arguments)
	}
}

func TestChatCompletionWithTools_AccumulatesMultipleParallelToolCalls(t *testing.T) {
	// A model batching independent tool calls in one turn streams each as
	// its own indexed entry in delta.tool_calls, interleaved chunk by
	// chunk rather than one call finishing before the next starts — real
	// providers commonly interleave argument chunks across indices like
	// this instead of completing index 0 before index 1 begins.
	srv := sseServer(t, []string{
		`data: {"choices":[{"delta":{"tool_calls":[{"index":0,"id":"call_1","type":"function","function":{"name":"web_search","arguments":""}}]}}]}`,
		`data: {"choices":[{"delta":{"tool_calls":[{"index":1,"id":"call_2","type":"function","function":{"name":"web_search","arguments":""}}]}}]}`,
		`data: {"choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":"{\"query\":\"golang\"}"}}]}}]}`,
		`data: {"choices":[{"delta":{"tool_calls":[{"index":1,"function":{"arguments":"{\"query\":\"rust\"}"}}]}}]}`,
		`data: {"choices":[{"delta":{},"finish_reason":"tool_calls"}]}`,
		`data: [DONE]`,
	})
	defer srv.Close()

	client := NewClient(srv.URL, "test-key", "test/model", 0.4, 1000)
	resp, err := client.ChatCompletionWithTools(context.Background(), []ChatMessage{{Role: "user", Content: "hi"}}, nil, nil, nil)
	if err != nil {
		t.Fatalf("ChatCompletionWithTools returned error: %v", err)
	}
	if len(resp.ToolCalls) != 2 {
		t.Fatalf("got %d tool calls, want 2: %+v", len(resp.ToolCalls), resp.ToolCalls)
	}
	if resp.ToolCalls[0].ID != "call_1" || resp.ToolCalls[0].Function.Arguments != `{"query":"golang"}` {
		t.Errorf("ToolCalls[0] = %+v, want call_1 with the golang query", resp.ToolCalls[0])
	}
	if resp.ToolCalls[1].ID != "call_2" || resp.ToolCalls[1].Function.Arguments != `{"query":"rust"}` {
		t.Errorf("ToolCalls[1] = %+v, want call_2 with the rust query", resp.ToolCalls[1])
	}
}

func TestChatCompletionWithTools_RequestsParallelToolCallsTrue(t *testing.T) {
	var captured chatRequest
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		if err := json.Unmarshal(body, &captured); err != nil {
			t.Fatalf("unmarshaling request body: %v", err)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: [DONE]\n")
	}))
	defer srv.Close()

	client := NewClient(srv.URL, "test-key", "test/model", 0.4, 1000)
	_, err := client.ChatCompletionWithTools(context.Background(), []ChatMessage{{Role: "user", Content: "hi"}},
		[]ToolDef{{Type: "function", Function: ToolFunctionDef{Name: "web_search"}}}, nil, nil)
	if err != nil {
		t.Fatalf("ChatCompletionWithTools returned error: %v", err)
	}
	if captured.ParallelToolCalls == nil || !*captured.ParallelToolCalls {
		t.Errorf("ParallelToolCalls = %v, want a pointer to true", captured.ParallelToolCalls)
	}
}

// TestWithReasoning_ExplicitlyDisabledIsSentOnTheWire guards against a
// real regression: ReasoningParams.Enabled used to be a plain bool, so
// WithReasoning(&ReasoningParams{Enabled: false}) — meant to explicitly
// turn reasoning off for a cheap auxiliary call like a thread title —
// serialized identically to leaving Enabled unset entirely, since
// encoding/json's omitempty drops a false bool same as a zero value.
// That silently left reasoning-native models free to reason internally
// anyway, burning part of the completion budget on hidden tokens (see
// gateway/turn.go's generateTitle). Enabled is now *bool specifically so
// &false round-trips onto the wire as {"enabled":false}, distinct from a
// nil ReasoningParams sending no reasoning field at all.
func TestWithReasoning_ExplicitlyDisabledIsSentOnTheWire(t *testing.T) {
	var rawBody string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		rawBody = string(body)
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: [DONE]\n")
	}))
	defer srv.Close()

	client := NewClient(srv.URL, "test-key", "test/model", 0.4, 300).
		WithReasoning(&ReasoningParams{Enabled: func() *bool { b := false; return &b }()})
	_, err := client.ChatCompletionStreaming(context.Background(), []ChatMessage{{Role: "user", Content: "hi"}}, func(string) {}, nil)
	if err != nil {
		t.Fatalf("ChatCompletionStreaming returned error: %v", err)
	}

	if !strings.Contains(rawBody, `"reasoning":{"enabled":false}`) {
		t.Errorf("request body = %s, want an explicit {\"enabled\":false} reasoning field", rawBody)
	}
}

func TestChatCompletion_NonOKStatus(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		w.Write([]byte(`{"error":{"message":"Missing Authentication header","code":401}}`))
	}))
	defer srv.Close()

	client := NewClient(srv.URL, "bad-key", "test/model", 0.4, 1000)
	_, err := client.ChatCompletionStreaming(context.Background(), []ChatMessage{{Role: "user", Content: "hi"}}, func(string) {}, nil)
	if err == nil {
		t.Fatal("expected an error for a 401 response")
	}
	if !strings.Contains(err.Error(), "401") || !strings.Contains(err.Error(), "Missing Authentication") {
		t.Errorf("err = %v, want it to include the status and body", err)
	}
}

func TestChatCompletion_MalformedSSELineIsSkippedNotFatal(t *testing.T) {
	srv := sseServer(t, []string{
		`data: not valid json at all`,
		`data: {"choices":[{"delta":{"content":"still works"}}]}`,
		`data: [DONE]`,
	})
	defer srv.Close()

	client := NewClient(srv.URL, "test-key", "test/model", 0.4, 1000)
	resp, err := client.ChatCompletionStreaming(context.Background(), []ChatMessage{{Role: "user", Content: "hi"}}, func(string) {}, nil)
	if err != nil {
		t.Fatalf("expected malformed line to be skipped, got error: %v", err)
	}
	if resp.Content != "still works" {
		t.Errorf("Content = %q, want %q", resp.Content, "still works")
	}
}

func TestChatCompletion_ContextCancelMidStreamReturnsPartialNotError(t *testing.T) {
	// The server holds the connection open indefinitely after one chunk;
	// cancelling reqCtx should surface whatever streamed so far as a valid
	// (if partial) response, not an error — see doRequest's ctx.Err()
	// handling.
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		flusher := w.(http.Flusher)
		fmt.Fprintf(w, "data: %s\n", `{"choices":[{"delta":{"content":"partial"}}]}`)
		flusher.Flush()
		select {
		case <-release:
		case <-r.Context().Done():
		}
	}))
	defer func() { close(release); srv.Close() }()

	client := NewClient(srv.URL, "test-key", "test/model", 0.4, 1000)
	ctx, cancel := context.WithCancel(context.Background())

	go func() {
		time.Sleep(50 * time.Millisecond)
		cancel()
	}()

	resp, err := client.ChatCompletionStreaming(ctx, []ChatMessage{{Role: "user", Content: "hi"}}, func(string) {}, nil)
	if err != nil {
		t.Fatalf("expected a cancelled request to return partial content, not an error: %v", err)
	}
	if resp.Content != "partial" {
		t.Errorf("Content = %q, want the content streamed before cancellation", resp.Content)
	}
}
