// backup.go exposes GET/POST /api/backup, which `polaris backup
// list`/`create` hit under Docker (cmd/backup.go's thin-client pattern,
// same as search/stats/update/restart) since that CLI, running on the
// host, has no direct filesystem access to the container's data volume.
// Bare-metal's own CLI path skips these entirely and talks to
// backup.List/Create directly — see cmd/backup.go.
package gateway

import (
	"net/http"

	"polaris/backup"
)

// handleListBackups returns every existing backup, newest first.
func (s *Server) handleListBackups(w http.ResponseWriter, r *http.Request) {
	cfg := s.liveConfig()
	infos, err := backup.List(cfg.Backup.Dir)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, infos)
}

// handleCreateBackup takes a backup right now, outside the daily
// schedule (backup.RunScheduler, started by cmd/run.go) — the same
// on-demand path `polaris backup create` uses directly on bare-metal.
func (s *Server) handleCreateBackup(w http.ResponseWriter, r *http.Request) {
	cfg := s.liveConfig()
	info, err := backup.Create(cfg.Database.Path, cfg.Backup.Dir)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, info)
}
