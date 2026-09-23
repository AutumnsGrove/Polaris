package gateway

import (
	"encoding/json"
	"fmt"
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
	// settingCustomInstructions stores the settings panel's free-text
	// operator-steering field — see CustomInstructionsFromStore and
	// agent/driver.go's applyCustomInstructionsPlaceholder. Empty/unset
	// means no custom instructions, same as an empty string.
	settingCustomInstructions = "custom_instructions"
	// settingPersonName/settingPersonPronouns: optional operator-supplied
	// guidance about themselves, entered in the general settings panel's
	// "About you" section — used by both the main assistant's system
	// prompt (agent/driver.go's personGuidance/{person} placeholder) and
	// Weaver's (gateway/constellation_weaver.go), since both need the same
	// underlying fact. Originally Constellation-owned (constellation_config's
	// person_name/person_pronouns columns, added for Weaver's pronoun
	// accuracy) before being promoted here so the main assistant benefits
	// too — see store.go's migration comment for the one-time data copy.
	settingPersonName     = "person_name"
	settingPersonPronouns = "person_pronouns"
	// settingFullTurnHistory stores "true" to replay every earlier turn's
	// tool calls and results to the model on follow-ups, not just its
	// answers — see loadHistoryWithToolResults. Off (unset, or anything
	// else) by default: it makes every follow-up in a researched thread
	// cost noticeably more, so it's opt-in rather than a silent change to
	// what existing threads spend.
	settingFullTurnHistory = "full_turn_history"
)

// maxPersonNameChars/maxPersonPronounsChars mirror the settings-panel
// inputs' own maxlength (see SettingsPanel.svelte) — generous for a name
// or a pronoun set, but enough of a ceiling that a malformed client can't
// wedge an arbitrarily large value into a string substituted into every
// single turn's system prompt.
const maxPersonNameChars = 80
const maxPersonPronounsChars = 40

// maxCustomInstructionsChars caps settingCustomInstructions — this text
// gets substituted into the system prompt on every single turn, so an
// unbounded paste (a whole document, accidentally) would silently inflate
// every request's cost and context usage rather than failing loudly at
// save time.
const maxCustomInstructionsChars = 4000

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

// CustomInstructionsFromStore reads the custom_instructions setting —
// shared by gateway/turn.go and cmd/search.go's one-shot CLI path, same
// "every real entry point honors the operator's own settings" reasoning as
// DisabledToolsFromStore/MemoryEnabledFromStore. A nil db, a read error, or
// an unset value all default to "" (no custom instructions), which
// applyCustomInstructionsPlaceholder collapses the {custom_instructions}
// placeholder down to nothing.
func CustomInstructionsFromStore(db *store.Store) string {
	if db == nil {
		return ""
	}
	val, err := db.GetSetting(settingCustomInstructions)
	if err != nil {
		return ""
	}
	return val
}

// PersonNameFromStore/PersonPronounsFromStore read the operator's "About
// you" fields — shared by gateway/turn.go (the main assistant's system
// prompt) and gateway/constellation_weaver.go (Weaver's), same "one
// underlying setting, two independent readers" shape as
// CustomInstructionsFromStore. A nil db, a read error, or an unset value
// all default to "" (no guidance), matching every other FromStore
// function's fail-open convention in this file.
func PersonNameFromStore(db *store.Store) string {
	if db == nil {
		return ""
	}
	val, err := db.GetSetting(settingPersonName)
	if err != nil {
		return ""
	}
	return val
}

func PersonPronounsFromStore(db *store.Store) string {
	if db == nil {
		return ""
	}
	val, err := db.GetSetting(settingPersonPronouns)
	if err != nil {
		return ""
	}
	return val
}

// FullTurnHistoryFromStore reads the full_turn_history setting — see
// settingFullTurnHistory. Unlike MemoryEnabledFromStore, a nil db, a read
// error, or an unset value all default to false: this setting only ever
// adds cost, so failing open means failing to the cheaper behavior.
func FullTurnHistoryFromStore(db *store.Store) bool {
	if db == nil {
		return false
	}
	val, err := db.GetSetting(settingFullTurnHistory)
	if err != nil {
		return false
	}
	return val == "true"
}

// ThemeFromStore reads the theme setting for tools.Context.UITheme (see
// tools.CodeExecThemePrompt) — same "default rather than fail" reasoning
// as MemoryEnabledFromStore/CustomInstructionsFromStore above. A nil db, a
// read error, or an unset value all default to "dark", matching this
// app's own default theme (see handleGetSettings' identical fallback for
// the settings panel itself).
func ThemeFromStore(db *store.Store) string {
	if db == nil {
		return "dark"
	}
	val, err := db.GetSetting(settingTheme)
	if err != nil || val == "" {
		return "dark"
	}
	return val
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
	agent.FocusModeResearcher:      true,
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
		"theme":              theme,
		"default_model":      s.effectiveDefaultModel(cfg),
		"default_focus_mode": all[settingDefaultFocusMode],
		"voice_input_mode":   voiceInputMode,
		// Effective, not raw config — see effectiveContextWindowTokens.
		"context_window_tokens": effectiveContextWindowTokens(cfg.ContextWindowTokens, FullTurnHistoryFromStore(s.db)),
		"full_turn_history":     FullTurnHistoryFromStore(s.db),
		"disabled_tools":        disabledTools,
		// toggleable_tools is static catalog data (name + description), not
		// a per-user setting — sent alongside so the settings panel can
		// render checkboxes without hardcoding tool names/descriptions that
		// only otherwise live in tools/descriptions/*.yaml.
		"toggleable_tools":    tools.ToggleableTools(),
		"memory_enabled":      MemoryEnabledFromStore(s.db),
		"custom_instructions": all[settingCustomInstructions],
		"person_name":         all[settingPersonName],
		"person_pronouns":     all[settingPersonPronouns],
	})
}

func (s *Server) handlePutSettings(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Theme              *string   `json:"theme"`
		DefaultModel       *string   `json:"default_model"`
		DefaultFocusMode   *string   `json:"default_focus_mode"`
		VoiceInputMode     *string   `json:"voice_input_mode"`
		DisabledTools      *[]string `json:"disabled_tools"`
		MemoryEnabled      *bool     `json:"memory_enabled"`
		CustomInstructions *string   `json:"custom_instructions"`
		PersonName         *string   `json:"person_name"`
		PersonPronouns     *string   `json:"person_pronouns"`
		FullTurnHistory    *bool     `json:"full_turn_history"`
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
	if req.CustomInstructions != nil {
		if len(*req.CustomInstructions) > maxCustomInstructionsChars {
			http.Error(w, fmt.Sprintf("custom_instructions must be %d characters or fewer", maxCustomInstructionsChars), http.StatusBadRequest)
			return
		}
		if err := s.db.SetSetting(settingCustomInstructions, *req.CustomInstructions); err != nil {
			log.Warn("saving custom_instructions setting failed", "err", err)
			s.db.LogEvent("", "error", "settings", "saving custom_instructions setting failed", map[string]interface{}{"err": err.Error()}, "")
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		s.db.LogEvent("", "info", "settings", "custom instructions changed", map[string]interface{}{"length": len(*req.CustomInstructions)}, "")
	}
	if req.PersonName != nil {
		if len(*req.PersonName) > maxPersonNameChars {
			http.Error(w, fmt.Sprintf("person_name must be %d characters or fewer", maxPersonNameChars), http.StatusBadRequest)
			return
		}
		if err := s.db.SetSetting(settingPersonName, *req.PersonName); err != nil {
			log.Warn("saving person_name setting failed", "err", err)
			s.db.LogEvent("", "error", "settings", "saving person_name setting failed", map[string]interface{}{"err": err.Error()}, "")
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		s.db.LogEvent("", "info", "settings", "person name changed", map[string]interface{}{"length": len(*req.PersonName)}, "")
	}
	if req.PersonPronouns != nil {
		if len(*req.PersonPronouns) > maxPersonPronounsChars {
			http.Error(w, fmt.Sprintf("person_pronouns must be %d characters or fewer", maxPersonPronounsChars), http.StatusBadRequest)
			return
		}
		if err := s.db.SetSetting(settingPersonPronouns, *req.PersonPronouns); err != nil {
			log.Warn("saving person_pronouns setting failed", "err", err)
			s.db.LogEvent("", "error", "settings", "saving person_pronouns setting failed", map[string]interface{}{"err": err.Error()}, "")
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		s.db.LogEvent("", "info", "settings", "person pronouns changed", map[string]interface{}{"pronouns": *req.PersonPronouns}, "")
	}

	if req.FullTurnHistory != nil {
		val := "false"
		if *req.FullTurnHistory {
			val = "true"
		}
		if err := s.db.SetSetting(settingFullTurnHistory, val); err != nil {
			log.Warn("saving full_turn_history setting failed", "err", err)
			s.db.LogEvent("", "error", "settings", "saving full_turn_history setting failed", map[string]interface{}{"err": err.Error()}, "")
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		s.db.LogEvent("", "info", "settings", "full turn history changed", map[string]interface{}{"full_turn_history": *req.FullTurnHistory}, "")
	}

	w.WriteHeader(http.StatusNoContent)
}
