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
//
// Resolves through EffectiveThreadID first — turn.go logs a turn's
// events under storageThreadID (see its doc comment), which is a hidden
// fork's own id whenever the active variant isn't root's own content.
// Querying by the raw path id in that case would silently return no
// events at all for that variant's tool calls/reasoning, even though
// they're fully persisted.
func (s *Server) handleThreadEvents(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	effectiveID, err := s.db.EffectiveThreadID(id)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	events, err := s.db.ListEvents(effectiveID, parseLimit(r, 500))
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
