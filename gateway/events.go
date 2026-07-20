package gateway

import (
	"net/http"
	"strconv"
)

// handleThreadEvents returns a thread's full event trail (turn
// start/finish/failure, every tool call/result, thinking steps,
// compaction) oldest-first — the durable record of exactly what happened
// during it, independent of whether the turn ever reached a normal
// "done" and independent of the log files' own retention.
func (s *Server) handleThreadEvents(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	events, err := s.db.ListEvents(id, parseLimit(r, 500))
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, events)
}

// handleRecentEvents returns the most recent events across every thread,
// newest first, plus thread-less ones (startup, self-update, a config
// reload failure) — for "what's been happening" without knowing which
// thread to look at first.
func (s *Server) handleRecentEvents(w http.ResponseWriter, r *http.Request) {
	events, err := s.db.ListRecentEvents(parseLimit(r, 200))
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, events)
}

func parseLimit(r *http.Request, fallback int) int {
	if v := r.URL.Query().Get("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			return n
		}
	}
	return fallback
}
