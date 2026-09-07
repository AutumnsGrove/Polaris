// memory_import.go backs the Memory settings page's "bring memories from
// another AI" flow (see docs/plans or GitHub issue #36 for the original
// design): a copyable prompt the user pastes into another assistant
// (claude.ai, ChatGPT) to get a plain-text dump of what it remembers about
// them, then pastes back here to seed Polaris's own memory store.
//
// Deliberately one-shot, not a real session like gateway/pulsar_wizard.go's
// interview — the import model never needs to ask the user anything (it's
// parsing a dump, not conducting an interview), so the whole parse/dedup/
// write pass runs inside a single request via runMemoryToolLoop, the same
// engine handleMemoryChat uses for a short instruction, just with its own
// system prompt and a much higher turn budget (a real dump can be dozens of
// distinct facts, each its own tool call).
package gateway

import (
	"encoding/json"
	"net/http"
	"strings"

	"polaris/prompts"
)

// maxMemoryImportToolTurns is far higher than maxMemoryChatToolTurns (6) —
// that budget is sized for a single short instruction naming at most a
// handful of facts, while an imported dump can legitimately contain dozens
// of distinct lines across five categories, each needing its own write/edit
// call before the model can reply with a final summary.
const maxMemoryImportToolTurns = 40

func (s *Server) handleMemoryExportPrompt(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, map[string]string{"prompt": prompts.Get().Turn.MemoryExportPrompt})
}

func (s *Server) handleMemoryImport(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Dump string `json:"dump"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}
	dump := strings.TrimSpace(req.Dump)
	if dump == "" {
		http.Error(w, "dump is required", http.StatusBadRequest)
		return
	}

	// Same shutdown-draining registration handleTurn/handleMemoryChat use —
	// this makes a real (likely multi-round) LLM call and mutates the DB.
	if !s.TryStartTurn() {
		http.Error(w, "the server is restarting — please retry in a few seconds", http.StatusServiceUnavailable)
		return
	}
	defer s.FinishTurn()

	summary, err := s.runMemoryToolLoop(r.Context(), prompts.Get().Turn.MemoryImportSystem, dump, maxMemoryImportToolTurns)
	if err != nil {
		log.Warn("memory import completion failed", "err", err)
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	memories, err := s.db.ListMemoriesFull()
	if err != nil {
		log.Warn("listing memories after import failed", "err", err)
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	s.db.LogEvent("", "info", "memory", "memories imported from another AI via settings panel", map[string]interface{}{"dump_chars": len(dump)}, "")
	writeJSON(w, map[string]interface{}{"message": summary, "memories": memories})
}
