package gateway

import "net/http"

// handleHealthz is a liveness check for systemd/launchd's Restart=always
// and for anything external (uptime monitor, load balancer health probe)
// that wants a cheap, unauthenticated signal the process is up and its
// database is actually reachable — not just that the HTTP server is
// accepting connections, which a wedged DB handle wouldn't catch.
func (s *Server) handleHealthz(w http.ResponseWriter, r *http.Request) {
	if err := s.db.Ping(); err != nil {
		w.WriteHeader(http.StatusServiceUnavailable)
		writeJSON(w, map[string]string{"status": "error", "error": err.Error()})
		return
	}
	writeJSON(w, map[string]string{"status": "ok", "version": s.getVersion()})
}
