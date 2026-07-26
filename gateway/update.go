package gateway

import (
	"fmt"
	"net/http"
	"sync"
	"time"

	"polaris/procmgr"
	"polaris/updater"
)

// updateStatus tracks the one self-update that can run at a time, shared
// across every request — not just the one that triggered it. Without
// this, closing the settings panel (or the phone backgrounding/killing
// the tab) mid-build loses all trace of "an update is running": reopening
// shows the idle button again, inviting a second click that kicks off a
// second concurrent `git pull && go build` racing the first in the same
// working directory. tryStart/finish bracket exactly the synchronous
// git-pull-and-build phase in handleUpdate below; snapshot lets any
// client — including one that reloaded mid-update — ask "is it still
// going, and if not, how did it end" instead of assuming idle.
type updateStatus struct {
	mu         sync.Mutex
	running    bool
	startedAt  time.Time
	done       bool
	success    bool
	log        string
	errMsg     string
	restarting bool
}

// tryStart claims the single update slot, returning false if one's
// already running — the caller must not start a second git pull/build.
func (u *updateStatus) tryStart() bool {
	u.mu.Lock()
	defer u.mu.Unlock()
	if u.running {
		return false
	}
	u.running = true
	u.startedAt = time.Now()
	u.done = false
	return true
}

func (u *updateStatus) finish(success bool, log, errMsg string, restarting bool) {
	u.mu.Lock()
	defer u.mu.Unlock()
	u.running = false
	u.done = true
	u.success = success
	u.log = log
	u.errMsg = errMsg
	u.restarting = restarting
}

func (u *updateStatus) snapshot() map[string]interface{} {
	u.mu.Lock()
	defer u.mu.Unlock()
	return map[string]interface{}{
		"running":    u.running,
		"done":       u.done,
		"success":    u.success,
		"log":        u.log,
		"error":      u.errMsg,
		"restarting": u.restarting,
	}
}

// handleUpdate runs the same git-pull-and-rebuild steps as `polaris
// update`, then restarts the service — triggered from the settings
// panel instead of an SSH session. The response is flushed to the
// client *before* restarting: systemctl/launchctl kills this very
// process, so the client needs its answer in hand first.
func (s *Server) handleUpdate(w http.ResponseWriter, r *http.Request) {
	if !s.updateStatus.tryStart() {
		writeJSON(w, map[string]interface{}{
			"success":         false,
			"already_running": true,
			"error":           "an update is already in progress",
		})
		return
	}

	repoPath, err := updater.RepoPath()
	if err != nil {
		s.updateStatus.finish(false, "", err.Error(), false)
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	result, err := updater.Run(repoPath)
	if err != nil {
		logOut := result.PullOutput + "\n" + result.BuildOutput
		s.updateStatus.finish(false, logOut, err.Error(), false)
		s.db.LogEvent("", "error", "update", "self-update build failed", map[string]interface{}{
			"err": err.Error(), "pull_output": result.PullOutput, "build_output": result.BuildOutput,
		}, "")
		writeJSON(w, map[string]interface{}{
			"success": false,
			"error":   err.Error(),
			"log":     logOut,
		})
		return
	}

	cfg := s.liveConfig()
	mgr, mgrErr := procmgr.New(cfg.Service.Label)
	restarting := mgrErr == nil && mgr.IsManaged()
	logOut := result.PullOutput + "\nbuild successful"

	s.updateStatus.finish(true, logOut, "", restarting)

	s.db.LogEvent("", "info", "update", "self-update built successfully", map[string]interface{}{
		"pull_output": result.PullOutput, "restarting": restarting,
	}, "")

	writeJSON(w, map[string]interface{}{
		"success":    true,
		"log":        logOut,
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
					s.db.LogEvent("", "error", "update", "panic during self-update restart", map[string]interface{}{"panic": fmt.Sprint(r)}, "")
				}
			}()
			time.Sleep(300 * time.Millisecond) // give the response time to reach the client
			if err := mgr.Restart(); err != nil {
				log.Error("self-update restart failed", "err", err)
				s.db.LogEvent("", "error", "update", "self-update restart failed", map[string]interface{}{"err": err.Error()}, "")
			}
		}()
	}
}

// handleUpdateStatus reports whether an update is currently running (and,
// once it's finished, how it went) — polled by a client that reopened the
// settings panel or reloaded the page after triggering an update, so it
// can resume showing progress instead of assuming idle and inviting a
// second, overlapping "push update now" click.
func (s *Server) handleUpdateStatus(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, s.updateStatus.snapshot())
}
