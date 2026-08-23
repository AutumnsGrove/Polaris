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

// handleListBackups returns every existing backup, newest first. With
// ?remote=1 it lists what's actually in R2 instead — `polaris backup list
// --remote` under Docker hits this exact query param (see
// cmd/backup.go's runDockerBackupListRemote) since that CLI, running on
// the host, has no more direct access to R2's API than it does to the
// container's data volume.
func (s *Server) handleListBackups(w http.ResponseWriter, r *http.Request) {
	cfg := s.liveConfig()

	if r.URL.Query().Get("remote") != "" {
		client := cfg.R2Client()
		if client == nil {
			http.Error(w, "r2 is not configured", http.StatusBadRequest)
			return
		}
		objects, err := client.List(r.Context())
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		writeJSON(w, objects)
		return
	}

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
// Also mirrors the new backup to R2 when configured, same as the
// scheduler and the bare-metal CLI path — a Docker install shouldn't get
// weaker off-device protection than a bare-metal one just because this is
// the handler that actually runs Create for it.
func (s *Server) handleCreateBackup(w http.ResponseWriter, r *http.Request) {
	cfg := s.liveConfig()
	info, err := backup.Create(cfg.Database.Path, cfg.Backup.Dir)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if err := backup.Mirror(info, cfg.R2Client()); err != nil {
		log.Warn("mirroring on-demand backup to r2 failed", "name", info.Name, "err", err)
	}
	writeJSON(w, info)
}
