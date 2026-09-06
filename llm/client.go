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
	"sort"
	"strings"
	"time"

	"polaris/logger"
)

var log = logger.WithPrefix("llm")

// ChatClient is the subset of *Client that callers (agent.Run,
// tools.web_read's optional filter pass, gateway's compaction/suggestion
// calls) actually depend on. Extracted as an interface so tests can
// substitute a fake instead of a real *Client — see llm/llmtest for one.
type ChatClient interface {
	// ChatCompletionWithTools sends a conversation with tool definitions,
	// tool_choice "auto" — see *Client's doc comment for the full contract.
	ChatCompletionWithTools(reqCtx context.Context, messages []ChatMessage, tools []ToolDef, onChunk func(string), onReasoning func(string)) (*ChatResponse, error)
	// ChatCompletionStreaming sends a plain (no-tools) conversation.
	ChatCompletionStreaming(reqCtx context.Context, messages []ChatMessage, onChunk func(string), onReasoning func(string)) (*ChatResponse, error)
}

var _ ChatClient = (*Client)(nil)

// Client talks to an OpenAI-compatible chat completions API.
type Client struct {
	baseURL     string
	apiKey      string
	model       string
	temperature float64
	maxTokens   int
	httpClient  *http.Client

	// Provider routing — OpenRouter-specific. Pins requests to a specific
	// provider (e.g. "xiaomi/fp8") — or an ordered list of them — so prompt
	// caching stays consistent — switching providers for the same model
	// usually means losing the cache. With a single entry this is a hard
	// pin; with more than one (a primary plus fallback(s), e.g.
	// models/models.go's DeepSeek entries) OpenRouter keeps preferring
	// entry 0 as long as it's healthy, so caching stays stable in
	// practice, but a mid-thread failover to a later entry does lose the
	// cache for that exchange — see sessionID below for why nothing here
	// papers over that.
	provider *ProviderRouting

	// sessionID is sent as OpenRouter's session_id, but per their own
	// docs it's a no-op whenever `provider` above is set: "Sticky routing
	// is not used when you specify a manual provider order via
	// provider.order — in that case, your explicit ordering takes
	// priority." Every caller in this codebase sets provider, so this
	// field currently does nothing for cache stickiness — whatever
	// consistency callers get comes from OpenRouter's own preference for
	// provider.Order[0], not from this. Kept because a provider-less
	// caller could still benefit from it, and because dropping it now
	// would be removing working API surface for no reason.
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
//
// Enabled is a *bool, not bool: omitting the reasoning field entirely
// (WithReasoning(nil), i.e. c.reasoning == nil) is not the same request
// as sending {"enabled": false}. A reasoning-native model (this app's
// deepseek default) can still reason internally by default when the
// field is left off altogether, burning part of MaxTokens on hidden
// reasoning before any visible content — see generateTitle's doc comment
// in gateway/turn.go for a real case this caused. A plain `bool` field
// with `omitempty` couldn't express "explicitly false" at all (Go's
// encoding/json omits a false bool the same as an unset one), so callers
// that need reasoning genuinely off for a cheap auxiliary call (a
// title, a suggestions list) need a real tri-state: nil field omitted
// entirely, &false sent as {"enabled":false}, &true sent as
// {"enabled":true}.
type ReasoningParams struct {
	Enabled   *bool  `json:"enabled,omitempty"`
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

// reasoningLeakMarkers lists literal control tokens a reasoning-capable
// provider is supposed to consume internally to gate its own
// reasoning/content SSE split — never meant to reach a client at all.
// Observed once in production straight from DeepSeek's official
// OpenRouter endpoint: right at the boundary where a reasoning burst
// ended and the model kept deliberating (still not a final answer, just
// more "let me check X" before another tool call), the provider's own
// tagging dropped the ball for that stretch and streamed the raw marker
// plus everything after it through delta.content instead of
// delta.reasoning. reasoningLeakSniffer treats each entry here as a
// literal prefix to watch for — add newly observed markers as they turn up.
var reasoningLeakMarkers = []string{"<|reasoning|>"}

// reasoningLeakSniffer catches a reasoningLeakMarkers leak and redirects
// it to onReasoning instead of onChunk — never discards it. The leaked
// text is still genuine chain-of-thought the user should be able to see;
// it just arrived mistagged, so the fix is re-filing it under the right
// heading, not stripping it (stripping would silently delete real
// reasoning the same way the original bug silently mislabeled it).
//
// It only re-checks at reasoning/content boundaries: arm() is called once
// up front and again every time a real delta.Reasoning chunk arrives —
// the only place a leak has ever been observed to start, since it's
// exactly where the provider's own split can lose track. Content
// chunks arriving mid-run, with no boundary in between, are trusted
// without re-buffering.
type reasoningLeakSniffer struct {
	// content receives chunks confirmed to be real visible content —
	// caller is responsible for both feeding the final response text and
	// forwarding to the caller's own onChunk.
	content func(string)
	// reasoning receives chunks confirmed (or presumed, once leaking) to
	// be misrouted reasoning — never added to the response's content.
	reasoning func(string)

	armed   bool
	leaking bool
	buf     strings.Builder
}

func (s *reasoningLeakSniffer) maxMarkerLen() int {
	max := 0
	for _, m := range reasoningLeakMarkers {
		if len(m) > max {
			max = len(m)
		}
	}
	return max
}

// arm re-primes the sniffer to check the next content chunk(s) against
// reasoningLeakMarkers, and clears any prior leaking verdict — called
// right as a real delta.Reasoning chunk confirms the provider's own
// split is (at that instant) working correctly, so whatever comes next
// deserves a fresh look rather than inheriting the last verdict forever.
func (s *reasoningLeakSniffer) arm() {
	s.flush()
	s.armed = true
	s.leaking = false
}

func (s *reasoningLeakSniffer) onChunk(chunk string) {
	if s.leaking {
		s.reasoning(chunk)
		return
	}
	if !s.armed {
		s.content(chunk)
		return
	}
	s.buf.WriteString(chunk)
	if s.buf.Len() < s.maxMarkerLen() {
		return
	}
	s.resolve()
}

func (s *reasoningLeakSniffer) resolve() {
	s.armed = false
	buf := s.buf.String()
	s.buf.Reset()
	if buf == "" {
		return
	}
	for _, m := range reasoningLeakMarkers {
		if strings.HasPrefix(buf, m) {
			s.leaking = true
			s.reasoning(buf)
			return
		}
	}
	s.content(buf)
}

// flush resolves whatever's still buffered as ordinary content — reached
// either mid-stream (a reasoning boundary showed up again before enough
// bytes arrived to rule out a marker, so it can't have been one) or at
// the very end of the response.
func (s *reasoningLeakSniffer) flush() {
	if !s.armed {
		return
	}
	s.resolve()
}

// APIError wraps a non-200 response from the LLM API. Its Error() message
// is user-facing — gateway/turn.go's "turn failed" path sends err.Error()
// straight into the chat transcript as the assistant's reply (see its
// send(ServerEvent{Type: "error", ...}) call), so a raw provider body
// lands verbatim in front of the user with no translation layer above
// this. A 429 gets a plain-language explanation instead of OpenRouter's
// full nested JSON (per-provider attempt list, doc links, user_id, ...) —
// observed live 2026-09-06 dumping straight into an active conversation.
// Other status codes keep the raw body: they're rare enough in practice
// (bad API key, malformed request) that the debugging detail is worth
// more than the politeness, and unlike 429 "just try again" doesn't apply.
type APIError struct {
	StatusCode int
	Body       string
}

func (e *APIError) Error() string {
	if e.StatusCode == http.StatusTooManyRequests {
		msg := "The AI provider is temporarily overloaded and rate-limited this request, even after retrying. This usually clears within a minute or two — try sending your message again shortly."
		if providers := attemptedProviders(e.Body); len(providers) > 0 {
			msg += fmt.Sprintf(" (providers tried: %s)", strings.Join(providers, ", "))
		}
		return msg
	}
	return fmt.Sprintf("LLM API returned %d: %s", e.StatusCode, e.Body)
}

// openrouterErrorBody is a best-effort partial decode of OpenRouter's error
// shape — only the fields attemptedProviders actually reads. Not a stable
// documented contract, so every field is optional and a parse failure is
// silent (see attemptedProviders' doc comment).
type openrouterErrorBody struct {
	Error struct {
		Metadata struct {
			ProviderName   string `json:"provider_name"`
			PreviousErrors []struct {
				ProviderName string `json:"provider_name"`
			} `json:"previous_errors"`
		} `json:"metadata"`
		OpenRouterMetadata struct {
			Attempts []struct {
				Provider string `json:"provider"`
			} `json:"attempts"`
		} `json:"openrouter_metadata"`
	} `json:"error"`
}

// attemptedProviders extracts which of models.go's pinned providers
// OpenRouter actually tried from a 429 error body, so the friendly message
// above can say e.g. "providers tried: Baidu, DeepInfra" instead of a bare
// "rate limited" with no indication of which specific provider(s) in a
// possibly 5-entry Provider list are the ones actually down right now —
// useful for deciding whether it's worth widening the list further (see
// models/models.go's deepseek entry) or just bad luck. openrouter_metadata.
// attempts is the most complete source (every provider tried this request,
// in order) when present; error.metadata's provider_name/previous_errors is
// the fallback for whatever shape omits it. Best-effort only: OpenRouter's
// error JSON isn't a stable contract, so a parse failure or missing field
// just yields an empty/partial list, never a hard error — this is cosmetic
// information, not load-bearing.
func attemptedProviders(body string) []string {
	var parsed openrouterErrorBody
	if err := json.Unmarshal([]byte(body), &parsed); err != nil {
		return nil
	}
	seen := make(map[string]bool)
	var providers []string
	add := func(name string) {
		if name == "" || seen[name] {
			return
		}
		seen[name] = true
		providers = append(providers, name)
	}
	for _, a := range parsed.Error.OpenRouterMetadata.Attempts {
		add(a.Provider)
	}
	add(parsed.Error.Metadata.ProviderName)
	for _, pe := range parsed.Error.Metadata.PreviousErrors {
		add(pe.ProviderName)
	}
	return providers
}

// max429Retries is how many extra attempts a 429 gets beyond the first
// (so 3 total). retry429BaseDelay is doubled each attempt (2s, 4s) — a
// var, not a const, so tests can shrink it instead of a real test run
// eating several seconds of sleep. Only 429 is retried here: a shared/
// pooled provider tier (e.g. deepseek's Baidu pin in models/models.go,
// priced far below OpenRouter's list rate) can saturate under other
// OpenRouter users' traffic for a few seconds and then clear on its own —
// a 401/400/5xx instead comes from this account's own request or config
// and won't fix itself by waiting. Observed live 2026-09-06: both entries
// in a two-provider Provider list returned 429 tpm_rate_limit_exceeded on
// the same request, i.e. OpenRouter had already exhausted the whole
// fallback chain before the error ever reached us — retrying here is
// what actually gives a saturated pool time to drain.
var (
	max429Retries     = 2
	retry429BaseDelay = 2 * time.Second
)

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
// parallel_tool_calls is requested as true — a model that decides it needs
// three independent web_search calls can emit all three in one turn
// instead of round-tripping through the full loop three times. Each
// parallel call streams as its own indexed entry in delta.tool_calls (see
// sseToolCallDelta.Index); doRequest accumulates every index it sees, not
// just the first, and returns them all in ChatResponse.ToolCalls in index
// order. agent.Run dispatches that whole batch concurrently — see its
// dispatchToolCallsConcurrently.
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
		// Explicit true, not just omitted — OpenRouter passes this through
		// to the underlying provider (see ChatCompletionWithTools' doc
		// comment); being explicit documents the intent rather than
		// relying on whatever a given provider defaults to.
		t := true
		reqBody.ParallelToolCalls = &t
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

	var resp *http.Response
	for attempt := 0; ; attempt++ {
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

		var doErr error
		resp, doErr = c.httpClient.Do(req)
		if doErr != nil {
			if ctx.Err() != nil {
				// Cancelled (the "stop" button) or timed out before the
				// request even got a response — same "not a real failure"
				// treatment a cancellation gets when it lands mid-stream
				// instead (see ctx.Err() a few lines below, past the scanner
				// loop). Without this, a stop landing in the gap between one
				// LLM call finishing tool dispatch and the next one starting
				// — a real, easily-hit window, not a rare edge case —
				// surfaced as a raw "context canceled" error instead of the
				// graceful early finish every caller (see agent.Run's doc
				// comment) is told a cancellation always produces, no matter
				// where it lands.
				return &ChatResponse{}, nil
			}
			return nil, fmt.Errorf("calling LLM API (stream): %w", doErr)
		}

		if resp.StatusCode == http.StatusOK {
			break
		}

		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		apiErr := &APIError{StatusCode: resp.StatusCode, Body: string(body)}

		if resp.StatusCode != http.StatusTooManyRequests || attempt >= max429Retries {
			return nil, apiErr
		}
		delay := retry429BaseDelay * time.Duration(1<<attempt)
		select {
		case <-time.After(delay):
		case <-ctx.Done():
			return nil, apiErr
		}
	}
	bodyClosed := false
	bodyClose := func() {
		if !bodyClosed {
			resp.Body.Close()
			bodyClosed = true
		}
	}
	defer bodyClose()

	scanner := bufio.NewScanner(resp.Body)
	scanner.Buffer(make([]byte, 1024*1024), 1024*1024)

	partials := make(map[int]*partialToolCall)
	var contentBuilder strings.Builder
	var finishReason string
	var promptTokens, completionTokens, totalTokens int
	var cacheReadTokens, cacheWriteTokens int
	var costUSD float64
	var respModel, respProvider string

	leakSniffer := &reasoningLeakSniffer{
		content: func(s string) {
			contentBuilder.WriteString(s)
			if onChunk != nil {
				onChunk(s)
			}
		},
		reasoning: func(s string) {
			if onReasoning != nil {
				onReasoning(s)
			}
		},
	}
	leakSniffer.arm()

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
		if delta.Reasoning != "" {
			if onReasoning != nil {
				onReasoning(delta.Reasoning)
			}
			// A real reasoning chunk just confirmed the provider's own
			// split is working right now — re-check the next content
			// chunk(s) against reasoningLeakMarkers rather than trusting
			// them on the strength of a verdict from earlier in the
			// stream. See reasoningLeakSniffer's doc comment.
			leakSniffer.arm()
		}
		if delta.Content != "" {
			leakSniffer.onChunk(delta.Content)
		}
		for _, tc := range delta.ToolCalls {
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
	leakSniffer.flush()
	if err := scanner.Err(); err != nil && ctx.Err() == nil {
		return nil, fmt.Errorf("reading SSE stream: %w", err)
	}
	// ctx.Err() != nil means this ended because the caller stopped it (the
	// "stop" button, or its own timeout) — not a real failure. Whatever
	// streamed before the cancel is still a valid, if partial, response.

	// Collected in index order (map iteration order is random) so a
	// multi-tool-call turn's ToolCalls slice — and therefore the assistant
	// message agent.Run builds from it — comes back in the same order the
	// model emitted them, deterministically across runs.
	var toolCalls []ToolCall
	indices := make([]int, 0, len(partials))
	for idx := range partials {
		indices = append(indices, idx)
	}
	sort.Ints(indices)
	for _, idx := range indices {
		p := partials[idx]
		if p.name == "" {
			continue
		}
		args := p.arguments.String()
		if args != "" && !json.Valid([]byte(args)) {
			if ctx.Err() != nil {
				// Cancelled mid-argument-stream — nothing salvageable for
				// this one call. Drop just it rather than erroring the
				// whole batch: agent.Run treats a smaller (or empty)
				// ToolCalls slice as a normal (if early) finish.
				continue
			}
			return nil, fmt.Errorf("truncated tool call arguments: %.100s", args)
		}
		callType := p.callType
		if callType == "" {
			callType = "function"
		}
		toolCalls = append(toolCalls, ToolCall{ID: p.id, Type: callType, Function: FunctionCall{Name: p.name, Arguments: args}})
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
