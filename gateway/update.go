package gateway

import (
	"fmt"
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
		s.db.LogEvent("", "error", "update", "self-update build failed", map[string]interface{}{
			"err": err.Error(), "pull_output": result.PullOutput, "build_output": result.BuildOutput,
		})
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

	s.db.LogEvent("", "info", "update", "self-update built successfully", map[string]interface{}{
		"pull_output": result.PullOutput, "restarting": restarting,
	})

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
			defer func() {
				if r := recover(); r != nil {
					log.Error("panic in self-update restart goroutine", "panic", r)
					s.db.LogEvent("", "error", "update", "panic during self-update restart", map[string]interface{}{"panic": fmt.Sprint(r)})
				}
			}()
			time.Sleep(300 * time.Millisecond) // give the response time to reach the client
			if err := mgr.Restart(); err != nil {
				log.Error("self-update restart failed", "err", err)
				s.db.LogEvent("", "error", "update", "self-update restart failed", map[string]interface{}{"err": err.Error()})
			}
		}()
	}
}
