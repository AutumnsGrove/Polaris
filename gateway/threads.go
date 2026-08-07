package gateway

import (
	"encoding/json"
	"net/http"
	"strings"

	"polaris/store"
)

// maxThreadTitleLen bounds a manually-renamed title — generous enough
// for a real title (the auto-generated ones cap much shorter, at
// maxTitleLen in turn.go) while still keeping the sidebar's fixed-width
// row from having to deal with an arbitrarily long string.
const maxThreadTitleLen = 120

func (s *Server) handleListThreads(w http.ResponseWriter, r *http.Request) {
	threads, err := s.db.ListThreads(100)
	if err != nil {
		log.Warn("listing threads failed", "err", err)
		s.db.LogEvent("", "error", "thread", "listing threads failed", map[string]interface{}{"err": err.Error()}, "")
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, threads)
}

func (s *Server) handleGetThread(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	thread, err := s.db.GetThread(id)
	if err != nil {
		log.Warn("getting thread failed", "thread", id, "err", err)
		s.db.LogEvent(id, "warn", "thread", "getting thread failed", map[string]interface{}{"err": err.Error()}, "")
		http.Error(w, "thread not found", http.StatusNotFound)
		return
	}
	messages, err := s.db.GetMessages(id)
	if err != nil {
		log.Warn("getting thread messages failed", "thread", id, "err", err)
		s.db.LogEvent(id, "error", "thread", "getting thread messages failed", map[string]interface{}{"err": err.Error()}, "")
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, struct {
		*store.Thread
		Messages []store.Message `json:"messages"`
	}{thread, messages})
}

// handleUpdateThread applies a partial update to a thread — rename
// and/or favorite, either or both in one request. The only other way a
// title changes is the one-time LLM-generated title right after a new
// thread's first turn (see turn.go's generateTitle); a manual rename
// here always wins over that, whether it happens before or after.
func (s *Server) handleUpdateThread(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")

	var req struct {
		Title    *string `json:"title"`
		Favorite *bool   `json:"favorite"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}

	if req.Title != nil {
		title := strings.TrimSpace(*req.Title)
		if title == "" {
			http.Error(w, "title is required", http.StatusBadRequest)
			return
		}
		if len(title) > maxThreadTitleLen {
			title = title[:maxThreadTitleLen]
		}
		if err := s.db.SetThreadTitle(id, title); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		s.db.LogEvent(id, "info", "thread", "thread renamed", map[string]interface{}{"title": title}, "")
	}

	if req.Favorite != nil {
		if err := s.db.SetThreadFavorite(id, *req.Favorite); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		s.db.LogEvent(id, "info", "thread", "thread favorite changed", map[string]interface{}{"favorite": *req.Favorite}, "")
	}

	w.WriteHeader(http.StatusNoContent)
}

// handleDeleteThread soft-deletes a thread — see store.DeleteThread's
// doc comment. The row and its messages/events stay in the database;
// this just stops it from showing up anywhere in the API.
func (s *Server) handleDeleteThread(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if err := s.db.DeleteThread(id); err != nil {
		log.Warn("deleting thread failed", "thread", id, "err", err)
		s.db.LogEvent(id, "error", "thread", "deleting thread failed", map[string]interface{}{"err": err.Error()}, "")
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	s.db.LogEvent("", "info", "thread", "thread disabled (soft delete)", map[string]interface{}{"thread_id": id}, "")
	w.WriteHeader(http.StatusNoContent)
}
