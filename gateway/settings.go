package gateway

import (
	"encoding/json"
	"net/http"

	"polaris/agent"
)

const (
	settingTheme        = "theme"       // "dark" or "light"
	settingShowPrices   = "show_prices" // "true" or "false"
	settingDefaultModel = "default_model"
	// settingDefaultFocusMode is the composer's standing focus mode —
	// applied to every new message until changed, same "sticky until
	// changed" semantics as settingDefaultModel. Empty/"off" means no
	// default (the composer starts with focus off, as today).
	settingDefaultFocusMode = "default_focus_mode"
)

// validFocusModes mirrors agent.FocusMode's non-"off" values (see
// agent/driver.go) — "off" itself is valid too (it just means "no
// default"), handled separately below rather than added to this set,
// since it reads oddly next to the descriptive doc comment ones.
var validFocusModes = map[string]bool{
	agent.FocusModeBrief:           true,
	agent.FocusModeAcademic:        true,
	agent.FocusModeNews:            true,
	agent.FocusModeFirstPrinciples: true,
	agent.FocusModeSocratic:        true,
}

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
		"default_focus_mode":    all[settingDefaultFocusMode],
		"context_window_tokens": cfg.ContextWindowTokens,
	})
}

func (s *Server) handlePutSettings(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Theme            *string `json:"theme"`
		ShowPrices       *bool   `json:"show_prices"`
		DefaultModel     *string `json:"default_model"`
		DefaultFocusMode *string `json:"default_focus_mode"`
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
			log.Warn("saving theme setting failed", "err", err)
			s.db.LogEvent("", "error", "settings", "saving theme setting failed", map[string]interface{}{"err": err.Error()}, "")
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		s.db.LogEvent("", "info", "settings", "theme changed", map[string]interface{}{"theme": *req.Theme}, "")
	}
	if req.ShowPrices != nil {
		value := "false"
		if *req.ShowPrices {
			value = "true"
		}
		if err := s.db.SetSetting(settingShowPrices, value); err != nil {
			log.Warn("saving show_prices setting failed", "err", err)
			s.db.LogEvent("", "error", "settings", "saving show_prices setting failed", map[string]interface{}{"err": err.Error()}, "")
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		s.db.LogEvent("", "info", "settings", "show_prices changed", map[string]interface{}{"show_prices": value}, "")
	}
	if req.DefaultModel != nil {
		cfg := s.liveConfig()
		if cfg.ModelByID(*req.DefaultModel).ID != *req.DefaultModel {
			http.Error(w, "unknown model id", http.StatusBadRequest)
			return
		}
		if err := s.db.SetSetting(settingDefaultModel, *req.DefaultModel); err != nil {
			log.Warn("saving default_model setting failed", "err", err)
			s.db.LogEvent("", "error", "settings", "saving default_model setting failed", map[string]interface{}{"err": err.Error()}, "")
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		s.db.LogEvent("", "info", "settings", "default model changed", map[string]interface{}{"model": *req.DefaultModel}, "")
	}
	if req.DefaultFocusMode != nil {
		mode := *req.DefaultFocusMode
		if mode != "" && mode != "off" && !validFocusModes[mode] {
			http.Error(w, "unknown focus mode", http.StatusBadRequest)
			return
		}
		if mode == "off" {
			mode = "" // stored as empty — handleGetSettings already treats "" as "no default"
		}
		if err := s.db.SetSetting(settingDefaultFocusMode, mode); err != nil {
			log.Warn("saving default_focus_mode setting failed", "err", err)
			s.db.LogEvent("", "error", "settings", "saving default_focus_mode setting failed", map[string]interface{}{"err": err.Error()}, "")
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		s.db.LogEvent("", "info", "settings", "default focus mode changed", map[string]interface{}{"focus_mode": mode}, "")
	}

	w.WriteHeader(http.StatusNoContent)
}
