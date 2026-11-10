package gateway

import (
	"context"

	"polaris/agent"
	"polaris/config"
	"polaris/llm"
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
func (s *Server) routedComplete(cfg *config.Config, client *llm.Client, modelID, sessionID string, onFallback func(from, to string, err error)) tools.CompleteFunc {
	return func(ctx context.Context, messages []llm.ChatMessage) (string, float64, error) {
		res, err := agent.CompleteOneShot(ctx, cfg, client, modelID, agent.OneShotOptions{
			SessionID: sessionID,
			UserAgent: "Polaris/" + s.version,
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
		s.logOneShotProviderUsage(res, modelID)
		return res.Text, res.ListCost, nil
	}
}
