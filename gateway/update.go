package gateway

import (
	"net/http"
	"sync"
	"time"
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

// handleUpdate resolves the latest published image's digest from GHCR
// and hands off to the host-side update watcher — triggered from the
// settings panel instead of an SSH session.
func (s *Server) handleUpdate(w http.ResponseWriter, r *http.Request) {
	s.handleDockerUpdate(w, r)
}

// handleRestart recreates the container from whatever image is already
// running, via the host-side update watcher, skipping GHCR entirely.
func (s *Server) handleRestart(w http.ResponseWriter, r *http.Request) {
	s.handleDockerRestart(w, r)
}

// handleUpdateStatus reports whether an update or restart is currently
// running (and, once it's finished, how it went) — polled by a client
// that reopened the settings panel or reloaded the page after triggering
// either one, so it can resume showing progress instead of assuming idle
// and inviting a second, overlapping click.
func (s *Server) handleUpdateStatus(w http.ResponseWriter, r *http.Request) {
	snap := s.updateStatus.snapshot()
	// The in-process updateStatus only ever records "the signal file was
	// written" (see handleDockerUpdate/handleDockerRestart) — the real
	// outcome is decided later, on the host, by a process this one never
	// hears back from directly. Layer the host watcher's own result file
	// on top so a client polling this endpoint (waitForServerAndReload)
	// can learn the actual reason an update failed — e.g. a bad SQL
	// migration — instead of just timing out after two minutes waiting for
	// a version bump that a rolled-back update will never produce.
	pending := dockerUpdateRequestPending()
	snap["docker_pending"] = pending
	if !pending {
		if res := readDockerWatcherResult(); res != nil {
			snap["docker_watcher_status"] = res.Status
			snap["docker_watcher_detail"] = res.Detail
			snap["docker_watcher_finished_at"] = res.FinishedAt
		}
	}
	writeJSON(w, snap)
}
