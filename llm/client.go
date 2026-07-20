// Package llm provides a client for OpenAI-compatible chat completion
// APIs, built for OpenRouter. Adapted from her-go's llm client: same
// streaming SSE parsing, provider pinning, and cost/cache metrics
// extraction, trimmed of the fallback-model machinery (this project
// pins one provider per model rather than racing multiple).
package llm

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"polaris/logger"
)

var log = logger.WithPrefix("llm")

// Client talks to an OpenAI-compatible chat completions API.
type Client struct {
	baseURL     string
	apiKey      string
	model       string
	temperature float64
	maxTokens   int
	httpClient  *http.Client

	// Provider routing — OpenRouter-specific. Pins requests to a specific
	// provider (e.g. "xiaomi/fp8") so prompt caching stays consistent —
	// switching providers for the same model usually means losing the cache.
	provider *ProviderRouting

	// sessionID enables OpenRouter sticky routing — all requests with the
	// same session_id are pinned to the same provider endpoint, maximizing
	// prompt cache hits across a thread.
	sessionID string

	// reasoning requests OpenRouter's unified reasoning-token stream for
	// models that support it — nil means don't ask for it (the provider
	// may still reason internally, it just won't be surfaced to us).
	reasoning *ReasoningParams
}

type ProviderRouting struct {
	Order          []string `json:"order,omitempty"`
	Only           []string `json:"only,omitempty"`
	Ignore         []string `json:"ignore,omitempty"`
	AllowFallbacks *bool    `json:"allow_fallbacks,omitempty"`
	Sort           string   `json:"sort,omitempty"`
}

// ReasoningParams mirrors OpenRouter's `reasoning` request field. Effort
// and MaxTokens are mutually exclusive per their API — set at most one.
type ReasoningParams struct {
	Enabled   bool   `json:"enabled,omitempty"`
	Effort    string `json:"effort,omitempty"`
	MaxTokens int    `json:"max_tokens,omitempty"`
}

type ChatMessage struct {
	Role       string     `json:"role"`
	Content    string     `json:"content"`
	ToolCalls  []ToolCall `json:"tool_calls,omitempty"`
	ToolCallID string     `json:"tool_call_id,omitempty"`
}

type ToolCall struct {
	ID       string       `json:"id"`
	Type     string       `json:"type"`
	Function FunctionCall `json:"function"`
}

type FunctionCall struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

type ToolDef struct {
	Type     string          `json:"type"`
	Function ToolFunctionDef `json:"function"`
}

type ToolFunctionDef struct {
	Name        string      `json:"name"`
	Description string      `json:"description"`
	Parameters  interface{} `json:"parameters"`
}

type ChatResponse struct {
	Content          string
	ToolCalls        []ToolCall
	FinishReason     string
	PromptTokens     int
	CompletionTokens int
	TotalTokens      int
	CostUSD          float64
	Model            string
	CacheReadTokens  int
	CacheWriteTokens int
	Provider         string
}

type chatRequest struct {
	Model             string           `json:"model"`
	Messages          []ChatMessage    `json:"messages"`
	Temperature       float64          `json:"temperature"`
	MaxTokens         int              `json:"max_tokens"`
	Tools             []ToolDef        `json:"tools,omitempty"`
	ToolChoice        interface{}      `json:"tool_choice,omitempty"`
	ParallelToolCalls *bool            `json:"parallel_tool_calls,omitempty"`
	Provider          *ProviderRouting `json:"provider,omitempty"`
	SessionID         string           `json:"session_id,omitempty"`
	Stream            bool             `json:"stream,omitempty"`
	Reasoning         *ReasoningParams `json:"reasoning,omitempty"`
}

type promptTokensDetails struct {
	CachedTokens     int `json:"cached_tokens"`
	CacheWriteTokens int `json:"cache_write_tokens"`
}

type openrouterMetadata struct {
	Attempts []struct {
		Provider string `json:"provider"`
	} `json:"attempts,omitempty"`
}

type sseChunk struct {
	Choices []struct {
		Delta struct {
			Content   string             `json:"content"`
			Reasoning string             `json:"reasoning"`
			ToolCalls []sseToolCallDelta `json:"tool_calls"`
		} `json:"delta"`
		FinishReason string `json:"finish_reason"`
	} `json:"choices"`
	Usage *struct {
		PromptTokens        int                  `json:"prompt_tokens"`
		CompletionTokens    int                  `json:"completion_tokens"`
		TotalTokens         int                  `json:"total_tokens"`
		Cost                float64              `json:"cost"`
		PromptTokensDetails *promptTokensDetails `json:"prompt_tokens_details,omitempty"`
	} `json:"usage"`
	Model    string `json:"model"`
	Provider string `json:"provider,omitempty"`
}

type sseToolCallDelta struct {
	Index    int    `json:"index"`
	ID       string `json:"id"`
	Type     string `json:"type"`
	Function struct {
		Name      string `json:"name"`
		Arguments string `json:"arguments"`
	} `json:"function"`
}

type partialToolCall struct {
	id        string
	callType  string
	name      string
	arguments strings.Builder
}

// requestTimeout bounds a single OpenRouter call. Deliberately NOT set as
// an http.Client.Timeout — that applies to the entire round trip
// including streaming the response body, so a client-level timeout would
// cut off a long-but-healthy stream at exactly the same point every time.
// A per-request context deadline (applied in doRequest) achieves the same
// "don't hang forever" goal without that footgun.
const requestTimeout = 3 * time.Minute

func NewClient(baseURL, apiKey, model string, temperature float64, maxTokens int) *Client {
	return &Client{
		baseURL:     baseURL,
		apiKey:      apiKey,
		model:       model,
		temperature: temperature,
		maxTokens:   maxTokens,
		httpClient:  &http.Client{},
	}
}

func (c *Client) WithProvider(p *ProviderRouting) *Client {
	c.provider = p
	return c
}

func (c *Client) WithSessionID(id string) *Client {
	c.sessionID = id
	return c
}

func (c *Client) WithReasoning(r *ReasoningParams) *Client {
	c.reasoning = r
	return c
}

// ChatCompletionWithTools sends a conversation with tool definitions using
// tool_choice "auto" — the model free-flows between calling tools and
// answering directly once it has enough context. When it streams plain
// content instead of a tool call, that's the final answer: onChunk
// delivers it token by token as it arrives, so the driver loop doesn't
// need a second call (or a "reply" signal tool) to stream the answer.
//
// Uses the streaming endpoint under the hood so we can abort the instant
// the model tries to batch a second tool call — this enforces strictly
// sequential tool execution even if the model ignores
// parallel_tool_calls:false.
//
// onReasoning delivers a reasoning-capable model's internal "thinking"
// tokens as they stream, separately from onChunk's visible answer tokens
// — nil-safe, pass nil if the caller doesn't care.
//
// reqCtx cancels the in-flight HTTP request the instant it's cancelled
// (the "stop" button) — a cancellation is treated as a graceful early
// finish, not an error: whatever content/reasoning streamed before the
// cancel is still returned rather than discarded.
func (c *Client) ChatCompletionWithTools(reqCtx context.Context, messages []ChatMessage, tools []ToolDef, onChunk func(string), onReasoning func(string)) (*ChatResponse, error) {
	return c.doRequest(reqCtx, messages, tools, "auto", onChunk, onReasoning)
}

// ChatCompletionStreaming sends a plain (no-tools) conversation and
// streams tokens to onChunk as they arrive. Used for the final
// user-facing answer once the tool loop is done gathering context.
func (c *Client) ChatCompletionStreaming(reqCtx context.Context, messages []ChatMessage, onChunk func(string), onReasoning func(string)) (*ChatResponse, error) {
	return c.doRequest(reqCtx, messages, nil, nil, onChunk, onReasoning)
}

// doRequest sends one chat-completions call and streams the SSE response,
// shared by both the tool-calling and plain-answer paths above — they
// differ only in whether tools/toolChoice are set, everything about
// building the request and parsing the response back is identical.
//
// toolChoice is nil for a plain (no-tools) call; "auto" for a
// tool-calling one, letting the model free-flow between calling a tool
// and answering directly.
//
// Every call gets requestTimeout via reqCtx regardless of whether tools
// were offered — previously only the tool-calling path had this, so a
// long compaction/suggestion call (ChatCompletionStreaming) had no bound
// beyond an idle TCP connection ever timing out.
func (c *Client) doRequest(reqCtx context.Context, messages []ChatMessage, tools []ToolDef, toolChoice interface{}, onChunk func(string), onReasoning func(string)) (*ChatResponse, error) {
	reqBody := chatRequest{
		Model:       c.model,
		Messages:    messages,
		Temperature: c.temperature,
		MaxTokens:   c.maxTokens,
		Tools:       tools,
		ToolChoice:  toolChoice,
		Stream:      true,
	}
	if len(tools) > 0 {
		f := false
		reqBody.ParallelToolCalls = &f
	}
	if c.provider != nil {
		reqBody.Provider = c.provider
	}
	if c.sessionID != "" {
		reqBody.SessionID = c.sessionID
	}
	if c.reasoning != nil {
		reqBody.Reasoning = c.reasoning
	}

	jsonData, err := json.Marshal(reqBody)
	if err != nil {
		return nil, fmt.Errorf("marshaling request: %w", err)
	}

	ctx, cancel := context.WithTimeout(reqCtx, requestTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, "POST", c.baseURL+"/chat/completions", bytes.NewReader(jsonData))
	if err != nil {
		return nil, fmt.Errorf("creating request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+c.apiKey)
	req.Header.Set("HTTP-Referer", "https://github.com/AutumnsGrove/Polaris")
	req.Header.Set("X-Title", "Polaris")
	req.Header.Set("Accept", "text/event-stream")
	req.Header.Set("X-OpenRouter-Metadata", "enabled")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("calling LLM API (stream): %w", err)
	}
	bodyClosed := false
	bodyClose := func() {
		if !bodyClosed {
			resp.Body.Close()
			bodyClosed = true
		}
	}
	defer bodyClose()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("LLM API returned %d: %s", resp.StatusCode, string(body))
	}

	scanner := bufio.NewScanner(resp.Body)
	scanner.Buffer(make([]byte, 1024*1024), 1024*1024)

	partials := make(map[int]*partialToolCall)
	var contentBuilder strings.Builder
	var finishReason string
	var promptTokens, completionTokens, totalTokens int
	var cacheReadTokens, cacheWriteTokens int
	var costUSD float64
	var respModel, respProvider string

readLoop:
	for scanner.Scan() {
		line := scanner.Text()
		if line == "data: [DONE]" {
			break
		}
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		var chunk sseChunk
		if err := json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &chunk); err != nil {
			log.Debug("skipping malformed SSE chunk", "err", err)
			continue
		}
		if chunk.Model != "" {
			respModel = chunk.Model
		}
		if chunk.Provider != "" {
			respProvider = chunk.Provider
		}
		if chunk.Usage != nil {
			promptTokens = chunk.Usage.PromptTokens
			completionTokens = chunk.Usage.CompletionTokens
			totalTokens = chunk.Usage.TotalTokens
			costUSD = chunk.Usage.Cost
			if d := chunk.Usage.PromptTokensDetails; d != nil {
				cacheReadTokens = d.CachedTokens
				cacheWriteTokens = d.CacheWriteTokens
			}
		}
		if len(chunk.Choices) == 0 {
			continue
		}
		delta := chunk.Choices[0].Delta
		if chunk.Choices[0].FinishReason != "" {
			finishReason = chunk.Choices[0].FinishReason
		}
		if delta.Reasoning != "" && onReasoning != nil {
			onReasoning(delta.Reasoning)
		}
		if delta.Content != "" {
			contentBuilder.WriteString(delta.Content)
			if onChunk != nil {
				onChunk(delta.Content)
			}
		}
		for _, tc := range delta.ToolCalls {
			if tc.Index >= 1 {
				bodyClose()
				break readLoop
			}
			p, ok := partials[tc.Index]
			if !ok {
				p = &partialToolCall{}
				partials[tc.Index] = p
			}
			if tc.ID != "" {
				p.id = tc.ID
			}
			if tc.Type != "" {
				p.callType = tc.Type
			}
			if tc.Function.Name != "" {
				p.name = tc.Function.Name
			}
			p.arguments.WriteString(tc.Function.Arguments)
		}
	}
	if err := scanner.Err(); err != nil && ctx.Err() == nil {
		return nil, fmt.Errorf("reading SSE stream: %w", err)
	}
	// ctx.Err() != nil means this ended because the caller stopped it (the
	// "stop" button, or its own timeout) — not a real failure. Whatever
	// streamed before the cancel is still a valid, if partial, response.

	var toolCalls []ToolCall
	if p, ok := partials[0]; ok && p.name != "" {
		args := p.arguments.String()
		if args != "" && !json.Valid([]byte(args)) {
			if ctx.Err() != nil {
				// Cancelled mid-argument-stream — nothing salvageable for
				// this tool call. Drop it rather than error: agent.Run
				// treats "no tool calls" as a normal (if early) finish.
			} else {
				return nil, fmt.Errorf("truncated tool call arguments: %.100s", args)
			}
		} else {
			callType := p.callType
			if callType == "" {
				callType = "function"
			}
			toolCalls = []ToolCall{{ID: p.id, Type: callType, Function: FunctionCall{Name: p.name, Arguments: args}}}
		}
	}
	if finishReason == "" && len(toolCalls) > 0 {
		finishReason = "tool_calls"
	}

	return &ChatResponse{
		Content:          contentBuilder.String(),
		ToolCalls:        toolCalls,
		FinishReason:     finishReason,
		PromptTokens:     promptTokens,
		CompletionTokens: completionTokens,
		TotalTokens:      totalTokens,
		CostUSD:          costUSD,
		Model:            respModel,
		CacheReadTokens:  cacheReadTokens,
		CacheWriteTokens: cacheWriteTokens,
		Provider:         respProvider,
	}, nil
}
