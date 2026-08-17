package gateway

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
)

// handleSearch backs Atlas's results page — a thin wrapper around the same
// search.SearXNGClient the web_search agent tool uses (see search.go's
// Server.searxng field), so domain ranking and the blocklist apply
// identically whether a query comes from a person typing into Atlas or the
// assistant calling web_search on their behalf.
func (s *Server) handleSearch(w http.ResponseWriter, r *http.Request) {
	query := strings.TrimSpace(r.URL.Query().Get("q"))
	if query == "" {
		http.Error(w, "q is required", http.StatusBadRequest)
		return
	}
	category := r.URL.Query().Get("category")

	maxResults := 8
	if raw := r.URL.Query().Get("max_results"); raw != "" {
		if n, err := strconv.Atoi(raw); err == nil && n > 0 {
			maxResults = n
		}
	}

	resp, err := s.searxng.Search(r.Context(), query, maxResults, category)
	if err != nil {
		log.Warn("atlas search failed", "query", query, "category", category, "err", err)
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}

	// Best-effort, off the response path: a search that succeeded is worth
	// showing even if recording it to history fails for some reason (a
	// locked/corrupt DB shouldn't take Atlas itself down).
	if err := s.db.RecordSearch(query); err != nil {
		log.Warn("recording search history failed", "query", query, "err", err)
	}

	writeJSON(w, resp)
}

// handleListSearchHistory backs Atlas's sidebar — same shape as
// handleListThreads for the chat sidebar.
func (s *Server) handleListSearchHistory(w http.ResponseWriter, r *http.Request) {
	entries, err := s.db.ListSearchHistory(100)
	if err != nil {
		log.Warn("listing search history failed", "err", err)
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, entries)
}

// handleUpdateSearchHistory toggles a search entry's Favorites-section
// membership — the search-history analog of handleUpdateThread's favorite
// field, minus rename (a past query has no separate title to edit).
func (s *Server) handleUpdateSearchHistory(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.Error(w, "invalid id", http.StatusBadRequest)
		return
	}

	var req struct {
		Favorite *bool `json:"favorite"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}

	if req.Favorite != nil {
		if err := s.db.SetSearchHistoryFavorite(id, *req.Favorite); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
	}

	w.WriteHeader(http.StatusNoContent)
}
