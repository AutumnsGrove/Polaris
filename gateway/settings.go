package gateway

import (
	"encoding/json"
	"net/http"

	"polaris/agent"
	"polaris/store"
	"polaris/tools"
)

const (
	settingTheme        = "theme" // "dark" or "light"
	settingDefaultModel = "default_model"
	// settingDefaultFocusMode is the composer's standing focus mode —
	// applied to every new message until changed, same "sticky until
	// changed" semantics as settingDefaultModel. Empty/"off" means no
	// default (the composer starts with focus off, as today).
	settingDefaultFocusMode = "default_focus_mode"
	// settingVoiceInputMode picks how VoiceButton.svelte's mic button
	// behaves: "hold" (press and hold to record, release to send — the
	// original behavior) or "toggle" (tap once to start, tap again to
	// stop). Defaults to "toggle" — hold-to-record turned out to be
	// unreliable enough in practice (slow to register, easy to release
	// early) that it went mostly unused in favor of the iOS keyboard's
	// own dictation button instead.
	settingVoiceInputMode = "voice_input_mode"
	// settingDisabledTools stores a JSON-encoded []string of tool names the
	// user has individually turned off from the settings panel — see
	// DisabledToolsFromStore and tools.ToggleableTools. Empty/unset means
	// nothing is disabled, same as an empty array.
	settingDisabledTools = "disabled_tools"
	// settingMemoryEnabled stores "false" to turn the memory feature off
	// entirely; any other value (including unset) means enabled — so
	// existing installs default to memory being on exactly as before this
	// setting existed. Deliberately its own dedicated setting rather than
	// folded into settingDisabledTools/tools.ToggleableTools: memory
	// already has its own settings-panel section (not the generic Tools
	// checkbox list — see nonToggleable in tools/catalog.go), and this
	// toggle gates more than just the tool call the model can make (see
	// MemoryEnabledFromStore's doc comment).
	settingMemoryEnabled = "memory_enabled"
)

// DisabledToolsFromStore reads the disabled_tools setting and returns it as
// the lookup set tools.Context.DisabledTools expects — shared by
// handleTurn (the WebSocket and POST /api/ask paths, both funneled through
// gateway/turn.go) and cmd/search.go's one-shot CLI path, so a tool
// disabled from the settings panel is actually honored everywhere a real
// tools.Context gets built from the operator's own settings, not just the
// web UI. A nil db (shouldn't happen outside tests) or a missing/corrupt
// setting both degrade to "nothing disabled" rather than an error — same
// fail-open shape as every other settings read in this file.
func DisabledToolsFromStore(db *store.Store) map[string]bool {
	if db == nil {
		return nil
	}
	raw, err := db.GetSetting(settingDisabledTools)
	if err != nil || raw == "" {
		return nil
	}
	var names []string
	if err := json.Unmarshal([]byte(raw), &names); err != nil {
		log.Warn("parsing disabled_tools setting failed, treating as none disabled", "err", err)
		return nil
	}
	set := make(map[string]bool, len(names))
	for _, n := range names {
		set[n] = true
	}
	return set
}

// MemoryEnabledFromStore reads the memory_enabled setting — shared by
// gateway/turn.go (the WebSocket and POST /api/ask paths) and
// cmd/search.go's one-shot CLI path, same "every real entry point honors
// the operator's own settings" reasoning as DisabledToolsFromStore.
// Callers use this to decide whether to wire tools.Context's five memory
// closures (ListMemories/GetMemory/WriteMemory/EditMemory/ForgetMemory) at
// all — leaving them nil when memory is off, rather than wiring them and
// separately gating the memory tool, means catalog.go's existing
// "memory_store" Requires check (ctx.WriteMemory != nil) already excludes
// the tool with no changes needed there, AND tools.MemoryIndexPrompt
// (gated on ctx.ListMemories == nil) stops injecting the {memories}
// prompt section too — turning memory off is a real "this context has no
// memory capability at all" rather than a tool visible-but-blocked. Same
// pattern cmd/benchmark.go already uses deliberately for its isolated
// runs (see registry.go's doc comment on these fields). A nil db, a read
// error, or an unset value all default to true (memory on).
func MemoryEnabledFromStore(db *store.Store) bool {
	if db == nil {
		return true
	}
	val, err := db.GetSetting(settingMemoryEnabled)
	if err != nil {
		return true
	}
	return val != "false"
}

// validVoiceInputModes gates handlePutSettings — see settingVoiceInputMode.
var validVoiceInputModes = map[string]bool{"hold": true, "toggle": true}

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
	voiceInputMode := all[settingVoiceInputMode]
	if !validVoiceInputModes[voiceInputMode] {
		voiceInputMode = "toggle"
	}

	// disabledTools defaults to an empty (non-nil) slice rather than the
	// zero value of a nil map read — writeJSON encodes a nil []string as
	// `null`, and the frontend's checkbox list would rather see `[]` than
	// have to special-case null on every load.
	disabledTools := []string{}
	for name := range DisabledToolsFromStore(s.db) {
		disabledTools = append(disabledTools, name)
	}

	writeJSON(w, map[string]interface{}{
		"theme":                 theme,
		"default_model":         s.effectiveDefaultModel(cfg),
		"default_focus_mode":    all[settingDefaultFocusMode],
		"voice_input_mode":      voiceInputMode,
		"context_window_tokens": cfg.ContextWindowTokens,
		"disabled_tools":        disabledTools,
		// toggleable_tools is static catalog data (name + description), not
		// a per-user setting — sent alongside so the settings panel can
		// render checkboxes without hardcoding tool names/descriptions that
		// only otherwise live in tools/descriptions/*.yaml.
		"toggleable_tools": tools.ToggleableTools(),
		"memory_enabled":   MemoryEnabledFromStore(s.db),
	})
}

func (s *Server) handlePutSettings(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Theme            *string   `json:"theme"`
		DefaultModel     *string   `json:"default_model"`
		DefaultFocusMode *string   `json:"default_focus_mode"`
		VoiceInputMode   *string   `json:"voice_input_mode"`
		DisabledTools    *[]string `json:"disabled_tools"`
		MemoryEnabled    *bool     `json:"memory_enabled"`
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
	if req.VoiceInputMode != nil {
		if !validVoiceInputModes[*req.VoiceInputMode] {
			http.Error(w, "voice_input_mode must be 'hold' or 'toggle'", http.StatusBadRequest)
			return
		}
		if err := s.db.SetSetting(settingVoiceInputMode, *req.VoiceInputMode); err != nil {
			log.Warn("saving voice_input_mode setting failed", "err", err)
			s.db.LogEvent("", "error", "settings", "saving voice_input_mode setting failed", map[string]interface{}{"err": err.Error()}, "")
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		s.db.LogEvent("", "info", "settings", "voice input mode changed", map[string]interface{}{"voice_input_mode": *req.VoiceInputMode}, "")
	}
	if req.DisabledTools != nil {
		valid := make(map[string]bool)
		for _, t := range tools.ToggleableTools() {
			valid[t.Name] = true
		}
		for _, name := range *req.DisabledTools {
			if !valid[name] {
				http.Error(w, "unknown or non-toggleable tool: "+name, http.StatusBadRequest)
				return
			}
		}
		encoded, err := json.Marshal(*req.DisabledTools)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if err := s.db.SetSetting(settingDisabledTools, string(encoded)); err != nil {
			log.Warn("saving disabled_tools setting failed", "err", err)
			s.db.LogEvent("", "error", "settings", "saving disabled_tools setting failed", map[string]interface{}{"err": err.Error()}, "")
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		s.db.LogEvent("", "info", "settings", "disabled tools changed", map[string]interface{}{"disabled_tools": *req.DisabledTools}, "")
	}
	if req.MemoryEnabled != nil {
		val := "true"
		if !*req.MemoryEnabled {
			val = "false"
		}
		if err := s.db.SetSetting(settingMemoryEnabled, val); err != nil {
			log.Warn("saving memory_enabled setting failed", "err", err)
			s.db.LogEvent("", "error", "settings", "saving memory_enabled setting failed", map[string]interface{}{"err": err.Error()}, "")
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		s.db.LogEvent("", "info", "settings", "memory enabled changed", map[string]interface{}{"memory_enabled": *req.MemoryEnabled}, "")
	}

	w.WriteHeader(http.StatusNoContent)
}
