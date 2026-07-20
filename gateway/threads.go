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
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, threads)
}

func (s *Server) handleGetThread(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	thread, err := s.db.GetThread(id)
	if err != nil {
		http.Error(w, "thread not found", http.StatusNotFound)
		return
	}
	messages, err := s.db.GetMessages(id)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, struct {
		*store.Thread
		Messages []store.Message `json:"messages"`
	}{thread, messages})
}

// handleRenameThread lets the sidebar rename a thread directly — the
// only other way a title changes is the one-time LLM-generated title
// right after a new thread's first turn (see turn.go's generateTitle);
// this always wins over that, whether it happens before or after.
func (s *Server) handleRenameThread(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")

	var req struct {
		Title string `json:"title"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}

	title := strings.TrimSpace(req.Title)
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
	s.db.LogEvent(id, "info", "thread", "thread renamed", map[string]interface{}{"title": title})
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleDeleteThread(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if err := s.db.DeleteThread(id); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
