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

// TestReasoningLeak_MarkerRedirectsToOnReasoningNotContent guards the fix
// for a real production episode: DeepSeek's official OpenRouter endpoint
// streamed its own internal `<|reasoning|>` control token straight through
// delta.content instead of delta.reasoning, right at the boundary where a
// reasoning burst ended and the model kept deliberating before its next
// tool call. Left unhandled, that raw marker plus everything after it
// showed up as if it were the assistant's real reply. The fix redirects a
// detected leak to onReasoning rather than dropping it — the leaked text
// is still genuine chain-of-thought, just mistagged.
func TestReasoningLeak_MarkerRedirectsToOnReasoningNotContent(t *testing.T) {
	srv := sseServer(t, []string{
		`data: {"choices":[{"delta":{"reasoning":"thinking about it..."}}]}`,
		`data: {"choices":[{"delta":{"content":"<|reasoning|>. They use a dedicated header when thinking."}}]}`,
		`data: {"choices":[{"delta":{"reasoning":"back to normal reasoning"}}]}`,
		`data: {"choices":[{"delta":{"content":"This is the real answer."}}]}`,
		`data: {"choices":[{"delta":{},"finish_reason":"stop"}]}`,
		`data: [DONE]`,
	})
	defer srv.Close()

	client := NewClient(srv.URL, "test-key", "test/model", 0.4, 1000)

	var streamedContent, streamedReasoning strings.Builder
	resp, err := client.ChatCompletionStreaming(context.Background(), []ChatMessage{{Role: "user", Content: "hi"}},
		func(chunk string) { streamedContent.WriteString(chunk) },
		func(chunk string) { streamedReasoning.WriteString(chunk) })
	if err != nil {
		t.Fatalf("ChatCompletionStreaming returned error: %v", err)
	}

	if resp.Content != "This is the real answer." {
		t.Errorf("resp.Content = %q, want only the real answer — leaked reasoning must not be in the final content", resp.Content)
	}
	if streamedContent.String() != "This is the real answer." {
		t.Errorf("streamed content = %q, want only the real answer", streamedContent.String())
	}
	wantReasoning := "thinking about it...<|reasoning|>. They use a dedicated header when thinking.back to normal reasoning"
	if streamedReasoning.String() != wantReasoning {
		t.Errorf("streamed reasoning = %q, want %q", streamedReasoning.String(), wantReasoning)
	}
}

// TestReasoningLeak_MarkerSplitAcrossChunksIsStillDetected covers the case
// where the SSE stream splits the marker token itself across two separate
// delta.content chunks — realistic since providers don't guarantee a
// control token arrives as one atomic chunk.
func TestReasoningLeak_MarkerSplitAcrossChunksIsStillDetected(t *testing.T) {
	srv := sseServer(t, []string{
		`data: {"choices":[{"delta":{"reasoning":"pass 1"}}]}`,
		`data: {"choices":[{"delta":{"content":"<|reas"}}]}`,
		`data: {"choices":[{"delta":{"content":"oning|>leaked thought"}}]}`,
		`data: {"choices":[{"delta":{},"finish_reason":"stop"}]}`,
		`data: [DONE]`,
	})
	defer srv.Close()

	client := NewClient(srv.URL, "test-key", "test/model", 0.4, 1000)

	var streamedContent, streamedReasoning strings.Builder
	resp, err := client.ChatCompletionStreaming(context.Background(), []ChatMessage{{Role: "user", Content: "hi"}},
		func(chunk string) { streamedContent.WriteString(chunk) },
		func(chunk string) { streamedReasoning.WriteString(chunk) })
	if err != nil {
		t.Fatalf("ChatCompletionStreaming returned error: %v", err)
	}

	if resp.Content != "" {
		t.Errorf("resp.Content = %q, want empty — the whole burst was a leaked marker split across chunks", resp.Content)
	}
	if !strings.Contains(streamedReasoning.String(), "<|reasoning|>leaked thought") {
		t.Errorf("streamed reasoning = %q, want it to contain the reassembled marker and following text", streamedReasoning.String())
	}
	if streamedContent.Len() != 0 {
		t.Errorf("streamed content = %q, want empty", streamedContent.String())
	}
}

// TestReasoningLeak_OrdinaryContentAfterReasoningIsUnaffected is the
// regression guard for the common case: content streaming normally right
// after a reasoning burst, with no marker present, must pass straight
// through and not get held back or misrouted.
func TestReasoningLeak_OrdinaryContentAfterReasoningIsUnaffected(t *testing.T) {
	srv := sseServer(t, []string{
		`data: {"choices":[{"delta":{"reasoning":"figuring it out"}}]}`,
		`data: {"choices":[{"delta":{"content":"Paris is the capital of France."}}]}`,
		`data: {"choices":[{"delta":{},"finish_reason":"stop"}]}`,
		`data: [DONE]`,
	})
	defer srv.Close()

	client := NewClient(srv.URL, "test-key", "test/model", 0.4, 1000)

	var streamedContent strings.Builder
	resp, err := client.ChatCompletionStreaming(context.Background(), []ChatMessage{{Role: "user", Content: "hi"}},
		func(chunk string) { streamedContent.WriteString(chunk) }, func(string) {})
	if err != nil {
		t.Fatalf("ChatCompletionStreaming returned error: %v", err)
	}
	if resp.Content != "Paris is the capital of France." {
		t.Errorf("resp.Content = %q, want the ordinary answer untouched", resp.Content)
	}
	if streamedContent.String() != "Paris is the capital of France." {
		t.Errorf("streamed content = %q, want the ordinary answer untouched", streamedContent.String())
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

// TestChatCompletion_429RetriesThenSucceeds verifies doRequest's retry loop
// actually recovers from a transient 429 instead of failing on the first
// one — the real-world case this guards is OpenRouter's own retry across a
// pinned Provider list still handing back 429 for a shared/pooled tier
// that clears a moment later (see models/models.go's deepseek entry).
func TestChatCompletion_429RetriesThenSucceeds(t *testing.T) {
	origDelay := retry429BaseDelay
	retry429BaseDelay = time.Millisecond
	defer func() { retry429BaseDelay = origDelay }()

	var attempts int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts++
		if attempts < 3 {
			w.WriteHeader(http.StatusTooManyRequests)
			w.Write([]byte(`{"error":{"message":"rate limited","code":429}}`))
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprintf(w, "data: %s\n", `{"choices":[{"delta":{"content":"recovered"}}]}`)
		fmt.Fprint(w, "data: [DONE]\n")
	}))
	defer srv.Close()

	client := NewClient(srv.URL, "test-key", "test/model", 0.4, 1000)
	resp, err := client.ChatCompletionStreaming(context.Background(), []ChatMessage{{Role: "user", Content: "hi"}}, func(string) {}, nil)
	if err != nil {
		t.Fatalf("expected the retry to eventually succeed, got error: %v", err)
	}
	if resp.Content != "recovered" {
		t.Errorf("Content = %q, want %q", resp.Content, "recovered")
	}
	if attempts != 3 {
		t.Errorf("attempts = %d, want 3 (two 429s then a success)", attempts)
	}
}

// TestChatCompletion_429ExhaustedReturnsFriendlyError verifies that once
// every retry is used up on a 429 that never clears, the caller gets a
// plain-language message instead of OpenRouter's raw nested error JSON
// (provider attempt list, doc links, user_id, ...) — the exact blob that
// otherwise lands verbatim in the chat transcript via gateway/turn.go's
// error ServerEvent.
func TestChatCompletion_429ExhaustedReturnsFriendlyError(t *testing.T) {
	origDelay := retry429BaseDelay
	retry429BaseDelay = time.Millisecond
	defer func() { retry429BaseDelay = origDelay }()

	var attempts int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts++
		w.WriteHeader(http.StatusTooManyRequests)
		w.Write([]byte(`{"error":{"message":"Provider returned error","code":429,"metadata":{"raw":"deepseek/deepseek-v4-flash-0731 is temporarily rate-limited upstream."}}}`))
	}))
	defer srv.Close()

	client := NewClient(srv.URL, "test-key", "test/model", 0.4, 1000)
	_, err := client.ChatCompletionStreaming(context.Background(), []ChatMessage{{Role: "user", Content: "hi"}}, func(string) {}, nil)
	if err == nil {
		t.Fatal("expected an error once every 429 retry is exhausted")
	}
	if strings.Contains(err.Error(), "metadata") || strings.Contains(err.Error(), "raw") {
		t.Errorf("err = %v, want a plain-language message, not OpenRouter's raw JSON body", err)
	}
	if attempts != max429Retries+1 {
		t.Errorf("attempts = %d, want %d (the initial attempt plus every retry)", attempts, max429Retries+1)
	}
}

// TestChatCompletion_429ErrorListsAttemptedProviders verifies the friendly
// 429 message names which specific provider(s) OpenRouter actually tried,
// parsed out of its error body — the real incident this guards used a body
// shaped just like this fixture (Baidu as the current failure, DeepInfra in
// previous_errors, both listed again in openrouter_metadata.attempts).
func TestChatCompletion_429ErrorListsAttemptedProviders(t *testing.T) {
	origDelay := retry429BaseDelay
	retry429BaseDelay = time.Millisecond
	defer func() { retry429BaseDelay = origDelay }()

	body := `{"error":{"message":"Provider returned error","code":429,"metadata":{"raw":"rate-limited upstream","provider_name":"Baidu","previous_errors":[{"code":429,"message":"Provider returned error","provider_name":"DeepInfra"}]},"openrouter_metadata":{"attempts":[{"provider":"Baidu","status":429},{"provider":"DeepInfra","status":429}]}}}`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
		w.Write([]byte(body))
	}))
	defer srv.Close()

	client := NewClient(srv.URL, "test-key", "test/model", 0.4, 1000)
	_, err := client.ChatCompletionStreaming(context.Background(), []ChatMessage{{Role: "user", Content: "hi"}}, func(string) {}, nil)
	if err == nil {
		t.Fatal("expected an error once every 429 retry is exhausted")
	}
	if !strings.Contains(err.Error(), "Baidu") || !strings.Contains(err.Error(), "DeepInfra") {
		t.Errorf("err = %v, want it to name both attempted providers", err)
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

// TestChatCompletion_ContextCancelBeforeResponseReturnsEmptyNotError closes
// a gap the test above didn't cover: that one only exercised cancellation
// landing *after* httpClient.Do returned a response (mid-stream). A
// cancellation landing *before* Do even returns — the realistic case is
// agent.Run's loop getting stopped in the gap between one turn's tool
// dispatch finishing and the next LLM call starting — used to fall through
// to a hard error instead, contradicting agent.Run's own doc comment that
// a cancellation is never treated as an error regardless of where it lands.
func TestChatCompletion_ContextCancelBeforeResponseReturnsEmptyNotError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		flusher := w.(http.Flusher)
		fmt.Fprintf(w, "data: %s\n", `{"choices":[{"delta":{"content":"should never be seen"}}]}`)
		flusher.Flush()
	}))
	defer srv.Close()

	client := NewClient(srv.URL, "test-key", "test/model", 0.4, 1000)
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // already cancelled before the call even starts — Do() itself must fail

	resp, err := client.ChatCompletionStreaming(ctx, []ChatMessage{{Role: "user", Content: "hi"}}, func(string) {}, nil)
	if err != nil {
		t.Fatalf("expected an already-cancelled context to return an empty response, not an error: %v", err)
	}
	if resp.Content != "" {
		t.Errorf("Content = %q, want empty — Do() must have failed before any bytes streamed", resp.Content)
	}
}
