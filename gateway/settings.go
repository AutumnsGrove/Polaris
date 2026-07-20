package gateway

import (
	"encoding/json"
	"net/http"
)

const (
	settingTheme        = "theme"       // "dark" or "light"
	settingShowPrices   = "show_prices" // "true" or "false"
	settingDefaultModel = "default_model"
)

func (s *Server) handleGetSettings(w http.ResponseWriter, r *http.Request) {
	cfg := s.liveConfig()

	all, err := s.db.AllSettings()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	theme := all[settingTheme]
	if theme == "" {
		theme = "dark"
	}
	showPrices := true
	if v, ok := all[settingShowPrices]; ok {
		showPrices = v == "true"
	}

	writeJSON(w, map[string]interface{}{
		"theme":                 theme,
		"show_prices":           showPrices,
		"default_model":         s.effectiveDefaultModel(cfg),
		"context_window_tokens": cfg.ContextWindowTokens,
	})
}

func (s *Server) handlePutSettings(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Theme        *string `json:"theme"`
		ShowPrices   *bool   `json:"show_prices"`
		DefaultModel *string `json:"default_model"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}

	if req.Theme != nil {
		if *req.Theme != "dark" && *req.Theme != "light" {
			http.Error(w, "theme must be 'dark' or 'light'", http.StatusBadRequest)
			return
		}
		if err := s.db.SetSetting(settingTheme, *req.Theme); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
	}
	if req.ShowPrices != nil {
		value := "false"
		if *req.ShowPrices {
			value = "true"
		}
		if err := s.db.SetSetting(settingShowPrices, value); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
	}
	if req.DefaultModel != nil {
		cfg := s.liveConfig()
		if cfg.ModelByID(*req.DefaultModel).ID != *req.DefaultModel {
			http.Error(w, "unknown model id", http.StatusBadRequest)
			return
		}
		if err := s.db.SetSetting(settingDefaultModel, *req.DefaultModel); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
	}

	w.WriteHeader(http.StatusNoContent)
}
