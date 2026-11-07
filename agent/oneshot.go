package agent

import (
	"context"
	"encoding/json"

	"github.com/AutumnsGrove/nova/message"
	"github.com/AutumnsGrove/nova/provider"
	"github.com/AutumnsGrove/nova/provider/chat"
	"github.com/AutumnsGrove/nova/provider/openrouter"

	"polaris/config"
	"polaris/llm"
	"polaris/routing"
)

// One-shot model calls — titles, suggestions, extraction, compaction
// summaries, memory import — run through CompleteOneShot instead of a
// hand-built llm.Client: one place where a Polaris id becomes a routed
// call, so the call sites can't drift from each other or from the turn
// loop. chat.Complete is one-shot only (it returns the finalized response
// with no stream events); the turn loop keeps streaming through
// Router.Stream directly.

// OneShotOptions tunes one CompleteOneShot call.
type OneShotOptions struct {
	// SessionID identifies the conversation for the Go session header.
	// Every one-shot runs inside some conversation (a thread, a routine,
	// a wizard session) — pass its stable id, never "". A call without
	// one fails the Go header check outright (400, no failover), so an
	// empty id would turn the flip into an outage for exactly these
	// background calls.
	SessionID string
	// UserAgent names the app on the wire ("Polaris/<version>").
	UserAgent string
	// Tools, when non-empty, offers tool calls (the memory-import loop);
	// ToolChoice controls them. Nil/empty is a plain completion.
	Tools      []provider.ToolDef
	ToolChoice any
	// OnFallback fires when the Go attempt spills to OpenRouter, so the
	// caller can surface the paid retry instead of staying silent.
	OnFallback func(from, to string, err error)
}

// OneShotResult is one completed one-shot call: the finalized text (and
// the full message for tool-call loops), what it consumed in the totals'
// currency, and which backend served it.
type OneShotResult struct {
	Text     string
	Message  message.Message
	Usage    provider.Usage
	Provider string
	// Spilled is true when something other than the route's primary
	// answered — the usage folds the failed leg in, approximate rather
	// than exact.
	Spilled bool
	// ListCost is routing.ListCost(Usage): retail in the totals'
	// currency, never allowance dollars.
	ListCost float64
}

// CompleteOneShot runs one model call for a registry model through the
// same routing, headers, and usage reporting a loop turn gets, without an
// agent. client carries the wire tuning (model slug, temperature, max
// tokens, provider order, reasoning) — read off it via Settings, exactly
// as novaProviderFor does for turns, so a ported site sends byte-identical
// options to what its llm.Client used to send.
func CompleteOneShot(ctx context.Context, cfg *config.Config, client *llm.Client, modelID string, opts OneShotOptions, msgs []llm.ChatMessage) (*OneShotResult, error) {
	settings := client.Settings()
	r := routing.Build(cfg, routing.Options{
		SessionID:       func() string { return opts.SessionID },
		UserAgent:       opts.UserAgent,
		ForceOpenRouter: ForceOpenRouter(),
		OnFallback:      opts.OnFallback,
	})
	// The catalog id resolves to its wire slug through Nova's catalog,
	// registered from the config registry on every Build — but a client
	// hand-built outside the registry (tests) still needs a mapping, or
	// chat.Complete would send the bare catalog id to the wire. The
	// client's own slug is the source of truth there; a registry entry
	// always wins over it.
	if _, ok := provider.GetModel(modelID); !ok && settings.Model != "" {
		provider.RegisterModel(provider.Model{ID: modelID, Provider: routing.OpenRouter, Slug: settings.Model})
	}
	temp := settings.Temperature
	promptCache := settings.PromptCaching
	resp, err := chat.Complete(ctx, r, chat.Request{
		Model:           modelID,
		Messages:        toNovaMessages(msgs),
		MaxTokens:       settings.MaxTokens,
		Temperature:     &temp,
		ProviderOptions: providerOptionsForSettings(settings),
		// Explicit, never catalog-decided: llm.Client sends the marker
		// exactly when the model slug is anthropic-prefixed, and
		// Settings.PromptCaching is that same bit — nil would let Nova
		// re-derive it from a catalog entry that doesn't know our ids.
		PromptCache: &promptCache,
		Tools:       opts.Tools,
		ToolChoice:  opts.ToolChoice,
	})
	if err != nil {
		return nil, translateProviderError(err)
	}
	primary, _, _ := r.Primary(modelID)
	return &OneShotResult{
		Text:     resp.Message.Content,
		Message:  resp.Message,
		Usage:    resp.Usage,
		Provider: resp.Provider,
		Spilled:  resp.Provider != "" && primary != "" && resp.Provider != primary,
		ListCost: routing.ListCost(resp.Usage),
	}, nil
}

// providerOptionsForSettings renders llm.Settings' routing and reasoning
// into Nova ProviderOptions — the same object the turn loop sends, so
// one-shots and turns stay byte-identical on the wire. Shared with
// novaProviderFor; neither site hand-rolls it.
func providerOptionsForSettings(s llm.Settings) map[string]any {
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
	if len(opts) == 0 {
		return nil
	}
	return opts
}

// translateProviderError turns a Nova provider error into the type the rest
// of Polaris keys on — the same mapping as polarisProvider.
// translateRunError, factored out so one-shots report failures identically.
func translateProviderError(err error) error {
	return (&polarisProvider{}).translateRunError(err)
}
