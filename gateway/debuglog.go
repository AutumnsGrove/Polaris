package gateway

import (
	"encoding/json"
	"net/http"
)

// handleDebugLog is TEMPORARY instrumentation for chasing the "thread
// bump-back" bug (see memory: project_thread_bump_back_root_cause) — the
// frontend beacons a note here at the handful of places currentThreadId
// changes or a version-mismatch reload fires, so the next occurrence can be
// read back from the events table instead of relying on the user watching
// DevTools live. Remove this handler, its route, and the beacon() calls in
// state.svelte.ts once the mechanism is confirmed and fixed.
func (s *Server) handleDebugLog(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Message string                 `json:"message"`
		Data    map[string]interface{} `json:"data"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	s.db.LogEvent("", "info", "client-debug", body.Message, body.Data, "")
	w.WriteHeader(http.StatusNoContent)
}
