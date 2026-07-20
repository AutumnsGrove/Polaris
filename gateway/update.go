package gateway

import (
	"net/http"
	"time"

	"polaris/procmgr"
	"polaris/updater"
)

// handleUpdate runs the same git-pull-and-rebuild steps as `polaris
// update`, then restarts the service — triggered from the settings
// panel instead of an SSH session. The response is flushed to the
// client *before* restarting: systemctl/launchctl kills this very
// process, so the client needs its answer in hand first.
func (s *Server) handleUpdate(w http.ResponseWriter, r *http.Request) {
	repoPath, err := updater.RepoPath()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	result, err := updater.Run(repoPath)
	if err != nil {
		writeJSON(w, map[string]interface{}{
			"success": false,
			"error":   err.Error(),
			"log":     result.PullOutput + "\n" + result.BuildOutput,
		})
		return
	}

	cfg := s.liveConfig()
	mgr, mgrErr := procmgr.New(cfg.Service.Label)
	restarting := mgrErr == nil && mgr.IsManaged()

	writeJSON(w, map[string]interface{}{
		"success":    true,
		"log":        result.PullOutput + "\nbuild successful",
		"restarting": restarting,
	})

	if restarting {
		if flusher, ok := w.(http.Flusher); ok {
			flusher.Flush()
		}
		go func() {
			time.Sleep(300 * time.Millisecond) // give the response time to reach the client
			if err := mgr.Restart(); err != nil {
				log.Error("self-update restart failed", "err", err)
			}
		}()
	}
}
