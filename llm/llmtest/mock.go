// Package llmtest provides a test double for llm.ChatClient so tests of
// agent.Run, tools.web_read's filter pass, and gateway's
// compaction/suggestion calls don't need a real OpenRouter connection —
// they queue up exactly the sequence of responses the code under test is
// expected to produce and assert against what was actually sent.
//
// Deliberately its own package (not llm/mock.go) so production code never
// accidentally imports test-only helpers — only _test.go files reach for
// this.
package llmtest

import (
	"context"
	"fmt"
	"sync"

	"polaris/llm"
)

// Response is one queued reply. Chunks/ReasoningChunks, if set, are
// streamed to onChunk/onReasoning (in order) before MockClient returns
// Resp — use these to exercise streaming-forwarding behavior without a
// real HTTP server. Leave Resp nil and set Err to simulate a failed call.
type Response struct {
	Resp            *llm.ChatResponse
	Err             error
	Chunks          []string
	ReasoningChunks []string
}

// Call records one invocation, for assertions after the code under test
// has run.
type Call struct {
	Messages []llm.ChatMessage
	Tools    []llm.ToolDef
}

// MockClient implements llm.ChatClient, returning Responses in order —
// one per call across both ChatCompletionWithTools and
// ChatCompletionStreaming, since real code often mixes both within one
// turn (agent.Run's tool loop, then gateway's separate suggestion/
// compaction calls). Safe for the same concurrency patterns as the real
// client (a mutex guards the call counter and log).
type MockClient struct {
	Responses []Response

	mu    sync.Mutex
	calls int
	Calls []Call
}

func (m *MockClient) ChatCompletionWithTools(_ context.Context, messages []llm.ChatMessage, tools []llm.ToolDef, onChunk func(string), onReasoning func(string)) (*llm.ChatResponse, error) {
	return m.next(messages, tools, onChunk, onReasoning)
}

func (m *MockClient) ChatCompletionStreaming(_ context.Context, messages []llm.ChatMessage, onChunk func(string), onReasoning func(string)) (*llm.ChatResponse, error) {
	return m.next(messages, nil, onChunk, onReasoning)
}

func (m *MockClient) next(messages []llm.ChatMessage, tools []llm.ToolDef, onChunk, onReasoning func(string)) (*llm.ChatResponse, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.Calls = append(m.Calls, Call{Messages: messages, Tools: tools})
	if m.calls >= len(m.Responses) {
		return nil, fmt.Errorf("llmtest.MockClient: call %d exceeds %d queued responses", m.calls+1, len(m.Responses))
	}
	r := m.Responses[m.calls]
	m.calls++

	for _, c := range r.Chunks {
		if onChunk != nil {
			onChunk(c)
		}
	}
	for _, c := range r.ReasoningChunks {
		if onReasoning != nil {
			onReasoning(c)
		}
	}
	if r.Err != nil {
		return nil, r.Err
	}
	return r.Resp, nil
}

// CallCount returns how many calls have been made so far — useful for
// asserting the code under test stopped at the right point (e.g. didn't
// keep calling past a plain-text final answer).
func (m *MockClient) CallCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.calls
}

var _ llm.ChatClient = (*MockClient)(nil)
