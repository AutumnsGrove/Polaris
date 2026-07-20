package llm

import (
	"context"
	"fmt"
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

func TestChatCompletionWithTools_SecondToolCallStopsStreamEarly(t *testing.T) {
	// doRequest enforces strictly sequential tool execution: the instant a
	// second tool call (index >= 1) appears, it stops reading rather than
	// waiting for [DONE] — the model tried to batch calls despite
	// parallel_tool_calls:false.
	srv := sseServer(t, []string{
		`data: {"choices":[{"delta":{"tool_calls":[{"index":0,"id":"call_1","type":"function","function":{"name":"web_search","arguments":"{}"}}]}}]}`,
		`data: {"choices":[{"delta":{"tool_calls":[{"index":1,"id":"call_2","type":"function","function":{"name":"web_read","arguments":"{}"}}]}}]}`,
		`data: [DONE]`,
	})
	defer srv.Close()

	client := NewClient(srv.URL, "test-key", "test/model", 0.4, 1000)
	resp, err := client.ChatCompletionWithTools(context.Background(), []ChatMessage{{Role: "user", Content: "hi"}}, nil, nil, nil)
	if err != nil {
		t.Fatalf("ChatCompletionWithTools returned error: %v", err)
	}
	if len(resp.ToolCalls) != 1 || resp.ToolCalls[0].Function.Name != "web_search" {
		t.Errorf("ToolCalls = %+v, want only the first call", resp.ToolCalls)
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
