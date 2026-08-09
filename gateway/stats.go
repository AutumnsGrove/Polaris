package gateway

import (
	"net/http"
	"strconv"
)

// handleStats returns the usage/tuning summary from store.Store.GetStats
// — ?days=N scopes the period-sensitive fields to the trailing N days
// (default 30); days<=0 means all time. Backs `polaris stats`, this
// endpoint itself, and the settings panel's small Usage section.
func (s *Server) handleStats(w http.ResponseWriter, r *http.Request) {
	days := 30
	if v := r.URL.Query().Get("days"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			days = n
		}
	}
	stats, err := s.db.GetStats(days)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, stats)
}
