package gateway

import "net/http"

// handleModels lists the model catalog from config.yaml for the
// selector — via liveConfig, so a model added to config.yaml shows up on
// the browser's next request with no restart needed.
func (s *Server) handleModels(w http.ResponseWriter, r *http.Request) {
	type modelOut struct {
		ID      string `json:"id"`
		Name    string `json:"name"`
		Default bool   `json:"default"`
	}
	cfg := s.liveConfig()
	defaultID := s.effectiveDefaultModel(cfg)
	out := make([]modelOut, 0, len(cfg.Models))
	for _, m := range cfg.Models {
		out = append(out, modelOut{ID: m.ID, Name: m.Name, Default: m.ID == defaultID})
	}
	writeJSON(w, out)
}
