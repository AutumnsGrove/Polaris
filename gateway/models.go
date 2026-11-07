package gateway

import (
	"net/http"

	"github.com/AutumnsGrove/nova/provider"

	"polaris/agent"
	"polaris/routing"
)

// handleModels lists the model catalog from config.yaml for the
// selector — via liveConfig, so a model added to config.yaml shows up on
// the browser's next request with no restart needed.
func (s *Server) handleModels(w http.ResponseWriter, r *http.Request) {
	type pricingOut struct {
		PromptPerM         float64 `json:"prompt_per_m"`
		CompletionPerM     float64 `json:"completion_per_m"`
		LongPromptTokens   int     `json:"long_prompt_tokens,omitempty"`
		LongPromptPerM     float64 `json:"long_prompt_per_m,omitempty"`
		LongCompletionPerM float64 `json:"long_completion_per_m,omitempty"`
	}
	type modelOut struct {
		ID      string      `json:"id"`
		Name    string      `json:"name"`
		Default bool        `json:"default"`
		Pricing *pricingOut `json:"pricing,omitempty"`
		// Compaction threshold for this model, so the thread menu's
		// context-usage % matches when auto-compaction actually fires.
		CompactionTokens int `json:"compaction_tokens"`
		// Effective serving backend and wire slug from the live routing
		// policy (route.Primary) — what the picker badges read, so they
		// agree with the turn loop instead of guessing from config.
		Primary string `json:"primary"`
		// GoMonthlyLimitUSD is the subscription's monthly allowance for
		// this model from Nova's catalog (0 = no Go entry, not unlimited).
		GoMonthlyLimitUSD float64 `json:"go_monthly_limit_usd"`
	}
	cfg := s.liveConfig()
	defaultID := s.effectiveDefaultModel(cfg)
	router := routing.Build(cfg, routing.Options{ForceOpenRouter: agent.ForceOpenRouter()})
	out := make([]modelOut, 0, len(cfg.Models))
	for _, m := range cfg.Models {
		mo := modelOut{ID: m.ID, Name: m.Name, Default: m.ID == defaultID, CompactionTokens: cfg.CompactionThreshold(m)}
		if p := m.Pricing; p != nil {
			mo.Pricing = &pricingOut{
				PromptPerM: p.PromptPerM, CompletionPerM: p.CompletionPerM,
				LongPromptTokens: p.LongPromptTokens, LongPromptPerM: p.LongPromptPerM, LongCompletionPerM: p.LongCompletionPerM,
			}
		}
		if primary, _, _ := router.Primary(m.ID); primary != "" {
			mo.Primary = primary
		}
		if m.GoModel != "" {
			if gm, ok := provider.GetModel("opencode-go/" + m.GoModel); ok {
				mo.GoMonthlyLimitUSD = gm.MonthlyLimitUSD
			}
		}
		out = append(out, mo)
	}
	writeJSON(w, out)
}
