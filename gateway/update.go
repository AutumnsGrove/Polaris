package gateway

import (
	"fmt"
	"net/http"
	"sync"
	"time"

	"polaris/procmgr"
	"polaris/updater"
)

// updateStatus tracks the one self-update-or-restart that can run at a
// time, shared across every request — not just the one that triggered it.
// Without this, closing the settings panel (or the phone backgrounding/
// killing the tab) mid-build loses all trace of "an update is running":
// reopening shows the idle button again, inviting a second click that
// kicks off a second concurrent `git pull && go build` racing the first in
// the same working directory. tryStart/finish bracket exactly the
// synchronous phase in handleUpdate/handleRestart below (git-pull-and-
// build for the former, nothing but claiming the slot for the latter);
// snapshot lets any client — including one that reloaded mid-operation —
// ask "is it still going, and if not, how did it end" instead of assuming
// idle.
//
// One shared slot for both operations, not two independent ones: both
// mutate the same running binary via the same service-manager restart, so
// letting an update and a plain restart run concurrently would race two
// `mgr.Restart()` calls (or a restart landing mid-build) exactly the way
// two concurrent updates would — see updater.AcquireLock's doc comment for
// the file-level lock that backs this same guarantee across separate
// processes (the CLI vs. this server).
type updateStatus struct {
	mu sync.Mutex

	// kind is "update" (git pull + go build + restart) or "restart" (just
	// the restart) — set by tryStart, read back via snapshot so the
	// settings panel can show "Updating…" vs "Restarting…" instead of
	// guessing from which button was last clicked (which reloading mid-
	// operation would lose track of).
	kind       string
	running    bool
	startedAt  time.Time
	done       bool
	success    bool
	log        string
	errMsg     string
	restarting bool

	// restartErr is set from the restart goroutine in handleUpdate/
	// handleRestart below, strictly after finish() already ran (finish
	// only knows the build succeeded — or, for a plain restart, that
	// nothing stopped it from starting — and a restart was *attempted*;
	// the restart itself happens asynchronously afterward, since
	// mgr.Restart() blocks on systemd/launchd, and the HTTP response has
	// to reach the client first). A separate field, not folded into
	// errMsg/success, because without it a failed `sudo systemctl
	// restart` (a polkit hiccup, a bad unit file, ...) was only ever
	// logged — snapshot kept reporting "success: true, restarting: true"
	// forever, so a client polling /api/update/status had no way to
	// learn the binary never actually got swapped and was still running
	// the pre-operation code.
	restartErr string
}

// tryStart claims the single update-or-restart slot, returning false (and
// a zero time.Time) if one's already running — the caller must not start
// a second operation. The returned startedAt identifies this run for
// setRestartError below, since the restart goroutine finishes well after
// finish() (and possibly after a second operation has already started).
func (u *updateStatus) tryStart(kind string) (bool, time.Time) {
	u.mu.Lock()
	defer u.mu.Unlock()
	if u.running {
		return false, time.Time{}
	}
	u.kind = kind
	u.running = true
	u.startedAt = time.Now()
	u.done = false
	return true, u.startedAt
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
	u.restartErr = "" // a fresh run supersedes any previous run's restart outcome
}

// setRestartError records that the restart command itself (systemctl/
// launchctl restart) failed after tryStart succeeded — see restartErr's
// doc comment. Guarded against a later run's finish() clearing it out from
// under a stale goroutine: only applies if this is still the same run (no
// update or restart has started since).
func (u *updateStatus) setRestartError(startedAt time.Time, err string) {
	u.mu.Lock()
	defer u.mu.Unlock()
	if !u.startedAt.Equal(startedAt) {
		return
	}
	u.restartErr = err
}

func (u *updateStatus) snapshot() map[string]interface{} {
	u.mu.Lock()
	defer u.mu.Unlock()
	return map[string]interface{}{
		"kind":          u.kind,
		"running":       u.running,
		"done":          u.done,
		"success":       u.success,
		"log":           u.log,
		"error":         u.errMsg,
		"restarting":    u.restarting,
		"restart_error": u.restartErr,
	}
}

// handleUpdate runs the same git-pull-and-rebuild steps as `polaris
// update`, then restarts the service — triggered from the settings
// panel instead of an SSH session. The response is flushed to the
// client *before* restarting: systemctl/launchctl kills this very
// process, so the client needs its answer in hand first.
func (s *Server) handleUpdate(w http.ResponseWriter, r *http.Request) {
	if deploymentMode() == "docker" {
		s.handleDockerUpdate(w, r)
		return
	}

	started, startedAt := s.updateStatus.tryStart("update")
	if !started {
		writeJSON(w, map[string]interface{}{
			"success":         false,
			"already_running": true,
			"error":           "an update or restart is already in progress",
		})
		return
	}

	repoPath, err := updater.RepoPath()
	if err != nil {
		s.updateStatus.finish(false, "", err.Error(), false)
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	// Held across the whole pull+build+restart sequence below, not just
	// the build — see updater.AcquireLock's doc comment for why releasing
	// early would leave the restart window (which can take tens of
	// seconds — see procmgr/systemd.go's TimeoutStopSec) open to a second
	// update racing in and overwriting the binary the OS is mid-exec'ing
	// for this one's restart. Carried into beginAsyncRestart below when
	// restarting, since the response has to reach the client (and this
	// handler return) before that goroutine's Restart() call runs.
	release, err := updater.AcquireLock(repoPath)
	if err != nil {
		s.updateStatus.finish(false, "", err.Error(), false)
		writeJSON(w, map[string]interface{}{
			"success": false,
			"error":   err.Error(),
		})
		return
	}

	result, err := updater.Run(repoPath)
	if err != nil {
		release()
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

	if !restarting {
		// Nothing more will touch the repo or the binary on this run —
		// safe to let the next update/restart proceed immediately rather
		// than holding the lock for no reason.
		release()
	}

	writeJSON(w, map[string]interface{}{
		"success":    true,
		"log":        logOut,
		"restarting": restarting,
	})

	if restarting {
		s.beginAsyncRestart(w, mgr, release, startedAt, "update")
	}
}

// handleRestart cleanly restarts the service with no git pull, no
// go build — just `mgr.Restart()`. This is what `polaris update` (and the
// settings panel's "Update Polaris" button) end up doing too once the
// build succeeds, but running the FULL update flow purely to force a
// restart pulls (a no-op when nothing's changed) and rebuilds (a real,
// if usually fast, `go build`) before it ever gets there — on the potato's
// weak CPU that can stall for tens of seconds for no benefit, exactly the
// "using the updater just to reboot doesn't work well" complaint this
// endpoint exists to fix. Shares updateStatus and the same file lock as
// handleUpdate so the two can never race each other.
func (s *Server) handleRestart(w http.ResponseWriter, r *http.Request) {
	started, startedAt := s.updateStatus.tryStart("restart")
	if !started {
		writeJSON(w, map[string]interface{}{
			"success":         false,
			"already_running": true,
			"error":           "an update or restart is already in progress",
		})
		return
	}

	repoPath, err := updater.RepoPath()
	if err != nil {
		s.updateStatus.finish(false, "", err.Error(), false)
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	// Same lock handleUpdate holds across its own pull+build+restart —
	// see updater.AcquireLock's doc comment. A plain restart doesn't
	// touch the repo or the binary itself, but still needs to keep an
	// update from starting (and racing its own restart) mid-flight, and
	// keep a second restart from piling on top of this one.
	release, err := updater.AcquireLock(repoPath)
	if err != nil {
		s.updateStatus.finish(false, "", err.Error(), false)
		writeJSON(w, map[string]interface{}{
			"success": false,
			"error":   err.Error(),
		})
		return
	}

	cfg := s.liveConfig()
	mgr, mgrErr := procmgr.New(cfg.Service.Label)
	if mgrErr != nil || !mgr.IsManaged() {
		release()
		errMsg := "service is not managed by systemd/launchd — restart manually"
		if mgrErr != nil {
			errMsg = mgrErr.Error()
		}
		s.updateStatus.finish(false, "", errMsg, false)
		writeJSON(w, map[string]interface{}{
			"success": false,
			"error":   errMsg,
		})
		return
	}

	s.updateStatus.finish(true, "restart requested", "", true)
	s.db.LogEvent("", "info", "restart", "clean restart requested", nil, "")

	writeJSON(w, map[string]interface{}{
		"success":    true,
		"restarting": true,
	})

	s.beginAsyncRestart(w, mgr, release, startedAt, "restart")
}

// beginAsyncRestart flushes the HTTP response (so the client has its
// answer in hand before this very process gets killed) and then restarts
// the service in a detached goroutine — shared by handleUpdate (after a
// successful build) and handleRestart (no build, straight to the restart).
// release is invoked once the goroutine finishes either way — see
// updater.AcquireLock's doc comment on why the lock must be held through
// the whole restart, not released the instant the response goes out.
func (s *Server) beginAsyncRestart(w http.ResponseWriter, mgr procmgr.Manager, release func(), startedAt time.Time, source string) {
	if flusher, ok := w.(http.Flusher); ok {
		flusher.Flush()
	}
	go func() {
		defer release()
		defer func() {
			if r := recover(); r != nil {
				log.Error("panic in restart goroutine", "source", source, "panic", r)
				s.db.LogEvent("", "error", source, "panic during restart", map[string]interface{}{"panic": fmt.Sprint(r)}, "")
			}
		}()
		time.Sleep(300 * time.Millisecond) // give the response time to reach the client
		if err := mgr.Restart(); err != nil {
			log.Error("restart failed", "source", source, "err", err)
			s.db.LogEvent("", "error", source, "restart failed", map[string]interface{}{"err": err.Error()}, "")
			// Surfaced via /api/update/status so the settings panel can
			// tell the user the restart command itself failed, instead of
			// spinning on waitForServerAndReload until its own 2-minute
			// deadline — see restartErr's doc comment on why this
			// couldn't just be folded into finish().
			s.updateStatus.setRestartError(startedAt, err.Error())
		}
	}()
}

// handleUpdateStatus reports whether an update or restart is currently
// running (and, once it's finished, how it went) — polled by a client
// that reopened the settings panel or reloaded the page after triggering
// either one, so it can resume showing progress instead of assuming idle
// and inviting a second, overlapping click.
func (s *Server) handleUpdateStatus(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, s.updateStatus.snapshot())
}
