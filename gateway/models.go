package gateway

import "net/http"

// handleModels lists the model catalog from config.yaml for the
// selector — via liveConfig, so a model added to config.yaml shows up on
// the browser's next request with no restart needed.
func (s *Server) handleModels(w http.ResponseWriter, r *http.Request) {
	type pricingOut struct {
		PromptPerM     float64 `json:"prompt_per_m"`
		CompletionPerM float64 `json:"completion_per_m"`
	}
	type modelOut struct {
		ID      string      `json:"id"`
		Name    string      `json:"name"`
		Default bool        `json:"default"`
		Pricing *pricingOut `json:"pricing,omitempty"`
	}
	cfg := s.liveConfig()
	defaultID := s.effectiveDefaultModel(cfg)
	out := make([]modelOut, 0, len(cfg.Models))
	for _, m := range cfg.Models {
		mo := modelOut{ID: m.ID, Name: m.Name, Default: m.ID == defaultID}
		if m.Pricing != nil {
			mo.Pricing = &pricingOut{PromptPerM: m.Pricing.PromptPerM, CompletionPerM: m.Pricing.CompletionPerM}
		}
		out = append(out, mo)
	}
	writeJSON(w, out)
}
