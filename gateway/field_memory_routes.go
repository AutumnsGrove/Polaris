package gateway

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"

	"polaris/store"
	"polaris/tools"
)

// The routes below back a Field page's "Memories" list (issue #133): the
// same view/edit/forget a settings-panel user gets for global memory, scoped
// to one Field's own store. There is deliberately no create route — entries
// appear by the model writing them during a turn in the field.

func (s *Server) handleListFieldMemories(w http.ResponseWriter, r *http.Request) {
	f := s.loadField(w, r.PathValue("id"))
	if f == nil {
		return
	}
	memories, err := s.db.ListFieldMemoriesFull(f.ID)
	if err != nil {
		log.Warn("listing field memories failed", "field", f.ID, "err", err)
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, memories)
}

// handleFieldMemoryChat is the Field page's "tell it what to remember" box:
// the same instruction-driven memory tool loop Settings uses, bound to this
// field's own store only (never global, regardless of the field's mode —
// this box manages the field's memories).
func (s *Server) handleFieldMemoryChat(w http.ResponseWriter, r *http.Request) {
	f := s.loadField(w, r.PathValue("id"))
	if f == nil {
		return
	}
	s.serveMemoryChat(w, r, fieldOnlyClosures(s.db, f.ID),
		func() ([]store.Memory, error) { return s.db.ListFieldMemoriesFull(f.ID) },
		"field memory changed via field page chat")
}

// handleImportFieldMemories is the Field page's "bring memories from another
// AI": the global import pass, writing into this field's own store.
func (s *Server) handleImportFieldMemories(w http.ResponseWriter, r *http.Request) {
	f := s.loadField(w, r.PathValue("id"))
	if f == nil {
		return
	}
	s.serveMemoryImport(w, r, fieldOnlyClosures(s.db, f.ID),
		func() ([]store.Memory, error) { return s.db.ListFieldMemoriesFull(f.ID) },
		"field memories imported from another AI via field page")
}

// handleExportFieldMemories downloads this field's memories in the same
// plain-text format as the global export.
func (s *Server) handleExportFieldMemories(w http.ResponseWriter, r *http.Request) {
	f := s.loadField(w, r.PathValue("id"))
	if f == nil {
		return
	}
	memories, err := s.db.ListFieldMemoriesFull(f.ID)
	if err != nil {
		log.Warn("listing field memories for export failed", "field", f.ID, "err", err)
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeMemoryExport(w, memories, "polaris-field-memories", "Polaris Field memory export — "+f.Name)
}

func (s *Server) handleUpdateFieldMemory(w http.ResponseWriter, r *http.Request) {
	f := s.loadField(w, r.PathValue("id"))
	if f == nil {
		return
	}
	name := r.PathValue("name")
	var req struct {
		Type        string `json:"type"`
		Description string `json:"description"`
		Content     string `json:"content"`
		OccurredAt  string `json:"occurred_at"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}
	if req.Type != "" && !validMemoryTypes[req.Type] {
		http.Error(w, "type must be one of user, feedback, project, reference", http.StatusBadRequest)
		return
	}
	if len(req.Description) > tools.MaxMemoryDescriptionChars {
		http.Error(w, fmt.Sprintf("description must be %d characters or fewer", tools.MaxMemoryDescriptionChars), http.StatusBadRequest)
		return
	}
	if req.OccurredAt != "" && !occurredAtRe.MatchString(req.OccurredAt) {
		http.Error(w, "occurred_at must be a plain YYYY-MM-DD date", http.StatusBadRequest)
		return
	}
	if err := s.db.UpdateFieldMemory(f.ID, name, req.Type, req.Description, req.Content, req.OccurredAt); err != nil {
		if errors.Is(err, store.ErrMemoryNotFound) {
			http.Error(w, "memory not found", http.StatusNotFound)
		} else {
			log.Warn("updating field memory failed", "field", f.ID, "name", name, "err", err)
			http.Error(w, err.Error(), http.StatusInternalServerError)
		}
		return
	}
	s.db.LogEvent("", "info", "memory", "field memory edited via field page", map[string]interface{}{"field": f.ID, "name": name}, "")
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleDeleteFieldMemory(w http.ResponseWriter, r *http.Request) {
	f := s.loadField(w, r.PathValue("id"))
	if f == nil {
		return
	}
	name := r.PathValue("name")
	if err := s.db.DeleteFieldMemory(f.ID, name); err != nil {
		if errors.Is(err, store.ErrMemoryNotFound) {
			http.Error(w, "memory not found", http.StatusNotFound)
		} else {
			log.Warn("deleting field memory failed", "field", f.ID, "name", name, "err", err)
			http.Error(w, err.Error(), http.StatusInternalServerError)
		}
		return
	}
	s.db.LogEvent("", "info", "memory", "field memory disabled (soft delete) via field page", map[string]interface{}{"field": f.ID, "name": name}, "")
	w.WriteHeader(http.StatusNoContent)
}
