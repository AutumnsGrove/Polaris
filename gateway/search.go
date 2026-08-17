package gateway

import (
	"net/http"
	"strconv"
)

// handleSearch backs Atlas's results page — a thin wrapper around the same
// search.SearXNGClient the web_search agent tool uses (see search.go's
// Server.searxng field), so domain ranking and the blocklist apply
// identically whether a query comes from a person typing into Atlas or the
// assistant calling web_search on their behalf.
func (s *Server) handleSearch(w http.ResponseWriter, r *http.Request) {
	query := r.URL.Query().Get("q")
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
	writeJSON(w, resp)
}
