package gateway

import (
	"context"

	"polaris/agent"
	"polaris/config"
	"polaris/llm"
	"polaris/store"
	"polaris/tools"
)

// routedComplete is the tools.Context.Complete seam for one model's side calls
// (web_read's and search_chats' filter pass): the same one-shot path titles and
// suggestions already take — Nova's router, the Go session header, failover —
// plus the provider-usage ledger row, so an OpenCode Go call is not invisible
// to the allowance meter just because a tool made it rather than the gateway.
//
// Before this, those passes went through tools.Context.LLM, a raw llm.Client
// that always talks to OpenRouter: a model routed to Go answered the turn on
// Go and then spent OpenRouter money on every filter pass beside it.
//
// The returned cost is the retail list cost (res.ListCost), the currency the
// turn totals use — never allowance dollars — so callers can AddCost it
// unchanged. onFallback may be nil (a background caller with no live turn to
// chip); a spill is logged either way.
func (s *Server) routedComplete(cfg *config.Config, chatClient llm.ChatClient, modelID, sessionID string, onFallback func(from, to string, err error)) tools.CompleteFunc {
	return routedCompleteFor(s.db, "Polaris/"+s.version, cfg, chatClient, modelID, sessionID, onFallback)
}

// routedCompleteFor is routedComplete without a *Server, for callers that hold
// only a store and a user agent (the Weaver runner is a free function).
func routedCompleteFor(db *store.Store, userAgent string, cfg *config.Config, chatClient llm.ChatClient, modelID, sessionID string, onFallback func(from, to string, err error)) tools.CompleteFunc {
	// CompleteOneShot reads its wire tuning off the concrete client. Anything
	// else (a test double) has no settings to route with, so it stays direct.
	client, ok := chatClient.(*llm.Client)
	// An empty model id has nothing to route by, and CompleteOneShot registers
	// a catalog entry under it (which panics on ""). A caller can legitimately
	// hold one — a Weaver config with no model chosen means "the default" —
	// so degrade to the direct client rather than crash. A nil cfg (tests)
	// has no router to build, which routing.Build would nil-deref on; the
	// Weaver runner documents that a nil cfg keeps the direct path.
	if !ok || modelID == "" || cfg == nil {
		return tools.CompleteFromClient(chatClient)
	}
	return func(ctx context.Context, messages []llm.ChatMessage) (string, float64, error) {
		res, err := agent.CompleteOneShot(ctx, cfg, client, modelID, agent.OneShotOptions{
			SessionID: sessionID,
			UserAgent: userAgent,
			OnFallback: func(from, to string, err error) {
				log.Warn("tool side call spilled providers", "model", modelID, "from", from, "to", to, "err", err)
				if onFallback != nil {
					onFallback(from, to, err)
				}
			},
		}, messages)
		if err != nil {
			return "", 0, err
		}
		logOneShotUsage(db, res, modelID)
		return res.Text, res.ListCost, nil
	}
}

// visionDescriber is the tools.Context.DescribeImage closure for selectedModel's
// vision model, sent through the same one-shot routing as every other side
// call (see routedComplete) instead of llm.Client.DescribeImage's own HTTP
// request, which always went straight to OpenRouter. Same prompt (llm.
// DescribeMessages). ok is false when no vision model is configured;
// sessionID is the thread (or routine) the image belongs to.
//
// Deliberately NOT pinned to the vision model's Provider the way the main chat
// client pins its provider — that pin exists for prompt-cache consistency
// across an ongoing conversation, which doesn't apply to this single one-off
// call, and it actively hurts here: found live that the pinned "xiaomi/fp8"
// endpoint 404s with "No endpoints found that support image input" even though
// the model itself is vision-capable — some provider-specific deployments of a
// multimodal model quietly drop vision support. Leaving provider routing open
// lets OpenRouter pick whichever endpoint actually handles image input.
func (s *Server) visionDescriber(cfg *config.Config, selectedModel config.ModelConfig, sessionID string) (describe func(ctx context.Context, imageBase64, mimeType, instructions string) (string, float64, error), ok bool) {
	visionModel, ok := visionModelFor(cfg, selectedModel)
	if !ok || visionModel.ID == "" {
		return nil, false
	}
	client := llm.NewClient(cfg.OpenRouter.BaseURL, cfg.OpenRouter.APIKey, visionModel.Model, visionModel.Temperature, visionModel.MaxTokens)
	return func(ctx context.Context, imageBase64, mimeType, instructions string) (string, float64, error) {
		res, err := agent.CompleteOneShot(ctx, cfg, client, visionModel.ID, agent.OneShotOptions{
			SessionID: sessionID,
			UserAgent: "Polaris/" + s.version,
			OnFallback: func(from, to string, err error) {
				log.Warn("image description spilled providers", "model", visionModel.ID, "from", from, "to", to, "err", err)
			},
		}, llm.DescribeMessages(imageBase64, mimeType, instructions))
		if err != nil {
			return "", 0, err
		}
		s.logOneShotProviderUsage(res, visionModel.ID)
		return res.Text, res.ListCost, nil
	}, true
}
