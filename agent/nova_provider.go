package agent

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/AutumnsGrove/nova/message"
	"github.com/AutumnsGrove/nova/provider"
	"github.com/AutumnsGrove/nova/provider/openrouter"

	"polaris/llm"
)

// novaRequestTimeout bounds a single provider call, matching llm.Client's
// requestTimeout (passed as agent.Config.RequestTimeout).
const novaRequestTimeout = 3 * time.Minute

// novaRetry matches llm.Client exactly — three total attempts, 2s then 4s
// (docs/plans/nova-loop.md §12.2). Nova's own defaults are deliberately wider
// and are not used; widening is a later, deliberate change.
var novaRetry = struct {
	Max  int
	Base time.Duration
}{Max: 2, Base: 2 * time.Second}

// novaModel is everything about the model that goes on the Nova agent.Config
// rather than into the provider.
type novaModel struct {
	Name        string
	Temperature float64
	MaxTokens   int
	PromptCache bool
	Options     map[string]any
}

// novaProviderFor builds the provider for one run from ctx.LLM.
//
// A real *llm.Client describes itself (llm.SettingsProvider), so the run gets
// a genuine Nova OpenRouter provider carrying the same base URL, key, routing
// and reasoning. Anything else — llmtest.MockClient, in practice — is bridged
// as-is, so the existing scripted-model tests drive the Nova loop unchanged
// instead of needing a second mock.
func novaProviderFor(client llm.ChatClient) (*polarisProvider, novaModel) {
	sp, ok := client.(llm.SettingsProvider)
	if !ok {
		return &polarisProvider{inner: chatClientProvider{client}}, novaModel{Name: "chat-client"}
	}
	s := sp.Settings()
	m := novaModel{Name: s.Model, Temperature: s.Temperature, MaxTokens: s.MaxTokens, PromptCache: s.PromptCaching}
	opts := map[string]any{}
	if s.Provider != nil {
		r := openrouter.Routing{Order: s.Provider.Order, Only: s.Provider.Only, Ignore: s.Provider.Ignore, AllowFallbacks: s.Provider.AllowFallbacks, Sort: s.Provider.Sort}
		for k, v := range r.Options() {
			opts[k] = v
		}
	}
	if s.Reasoning != nil {
		// OpenRouter's unified `reasoning` object, not Nova's ReasoningEffort
		// (which emits OpenAI's reasoning_effort) — see plan §5.1.
		if raw, err := json.Marshal(s.Reasoning); err == nil {
			var obj map[string]any
			if json.Unmarshal(raw, &obj) == nil {
				opts["reasoning"] = obj
			}
		}
	}
	if len(opts) > 0 {
		m.Options = opts
	}
	inner := openrouter.New(openrouter.Config{
		APIKey:  s.APIKey,
		BaseURL: s.BaseURL,
		Referer: "https://github.com/AutumnsGrove/Polaris",
		Title:   "Polaris",
		// llm.Client always sent "content":"" on a tool-call-only assistant
		// message; keeping it means replayed history re-encodes to the same
		// bytes and the provider's prompt cache survives the switch.
		EmptyToolCallContent: true,
	})
	return &polarisProvider{inner: inner}, m
}

// polarisProvider layers what llm.Client did that Nova's provider does not onto
// any inner provider: the OpenRouter metadata header and the reasoning-leak
// re-filing. (The per-call deadline is agent.Config.RequestTimeout, and
// Polaris's user-facing error type is translateRunError, applied by the caller.)
type polarisProvider struct {
	inner provider.Provider
}

func (p *polarisProvider) Name() string { return p.inner.Name() }

func (p *polarisProvider) Stream(ctx context.Context, req provider.Request, onEvent func(provider.StreamEvent)) (*provider.Response, error) {
	if req.ExtraHeaders == nil {
		req.ExtraHeaders = map[string]string{}
	}
	// Asks OpenRouter to attach per-provider attempt metadata, which the 429
	// message surfaces ("providers tried: ...").
	req.ExtraHeaders["X-OpenRouter-Metadata"] = "enabled"

	var content strings.Builder
	leaked := false
	emit := func(ev provider.StreamEvent) {
		if onEvent != nil {
			onEvent(ev)
		}
	}
	filter := llm.NewLeakFilter(func(s string) {
		content.WriteString(s)
		emit(provider.StreamEvent{Type: provider.StreamTextDelta, Text: s})
	}, func(s string) {
		leaked = true
		emit(provider.StreamEvent{Type: provider.StreamReasoningDelta, Text: s})
	})
	resp, err := p.inner.Stream(ctx, req, func(ev provider.StreamEvent) {
		switch ev.Type {
		case provider.StreamTextDelta:
			filter.Content(ev.Text)
		case provider.StreamReasoningDelta:
			emit(ev)
			filter.ReasoningSeen()
		default:
			emit(ev)
		}
	})
	filter.Flush()
	if err != nil {
		return nil, err
	}
	if resp != nil && leaked {
		resp.Message.Content = content.String()
	}
	return resp, nil
}

// translateRunError turns Nova's provider errors back into the types the rest
// of Polaris keys on: *llm.APIError (whose Error() is the user-facing text,
// including the friendly 429 message) for an HTTP failure. Transport errors
// keep their *url.Error in the chain, so llm.IsNetworkError still works.
func (p *polarisProvider) translateRunError(err error) error {
	if err == nil {
		return nil
	}
	var pe *provider.Error
	if errors.As(err, &pe) && pe.Status != 0 {
		body := pe.Body
		if body == "" {
			body = pe.Message
		}
		return &llm.APIError{StatusCode: pe.Status, Body: body}
	}
	return err
}

// chatClientProvider bridges an llm.ChatClient to a Nova provider.
type chatClientProvider struct {
	client llm.ChatClient
}

func (chatClientProvider) Name() string { return "chat-client" }

func (p chatClientProvider) Stream(ctx context.Context, req provider.Request, onEvent func(provider.StreamEvent)) (*provider.Response, error) {
	msgs := fromNovaMessages(req.Messages)
	emit := func(ev provider.StreamEvent) {
		if onEvent != nil {
			onEvent(ev)
		}
	}
	onChunk := func(s string) { emit(provider.StreamEvent{Type: provider.StreamTextDelta, Text: s}) }
	onReasoning := func(s string) { emit(provider.StreamEvent{Type: provider.StreamReasoningDelta, Text: s}) }

	var resp *llm.ChatResponse
	var err error
	if len(req.Tools) > 0 {
		resp, err = p.client.ChatCompletionWithTools(ctx, msgs, fromNovaDefs(req.Tools), onChunk, onReasoning)
	} else {
		resp, err = p.client.ChatCompletionStreaming(ctx, msgs, onChunk, onReasoning)
	}
	if err != nil {
		return nil, err
	}
	if resp == nil {
		resp = &llm.ChatResponse{}
	}
	out := &provider.Response{
		Message:      message.Message{Role: message.RoleAssistant, Content: resp.Content},
		FinishReason: resp.FinishReason,
		Model:        resp.Model,
		Usage: provider.Usage{
			PromptTokens:     resp.PromptTokens,
			CompletionTokens: resp.CompletionTokens,
			TotalTokens:      resp.TotalTokens,
			CachedTokens:     resp.CacheReadTokens,
			CacheWriteTokens: resp.CacheWriteTokens,
			CostUSD:          resp.CostUSD,
		},
	}
	for _, tc := range resp.ToolCalls {
		c := message.ToolCall{ID: tc.ID, Type: tc.Type, Function: message.FunctionCall{Name: tc.Function.Name, Arguments: tc.Function.Arguments}}
		out.Message.ToolCalls = append(out.Message.ToolCalls, c)
		call := c
		emit(provider.StreamEvent{Type: provider.StreamToolCall, ToolCall: &call})
	}
	return out, nil
}
