// Package updater implements the git-pull-and-rebuild half of Polaris's
// self-update flow. Shared by cmd/update.go (CLI) and the settings
// panel's "push update now" button (gateway's POST /api/update) — same
// steps either way, just triggered from a terminal or a browser.
package updater

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"time"

	"polaris/logger"
)

var log = logger.WithPrefix("updater")

// pullTimeout/buildTimeout bound the two subprocesses Run shoots off —
// without them, a git pull stuck on a credential prompt or network stall,
// or a go build wedged on a module fetch, would hang the self-update flow
// forever. buildTimeout is generous (not a tight CI-style budget): the
// potato is a weak SBC (see Run's doc comment) where a plain `go build`
// can legitimately take a couple of minutes.
const (
	pullTimeout  = 2 * time.Minute
	buildTimeout = 5 * time.Minute
)

// Result holds combined stdout/stderr from each step, for display in
// whichever UI triggered the update — a CLI or the settings panel.
type Result struct {
	PullOutput  string
	BuildOutput string
	BinaryPath  string
}

// Run pulls origin/main and rebuilds the binary in repoPath. web/build/
// is committed straight to git rather than rebuilt here — the potato is
// a Le Potato SBC, too weak to run pnpm install + vite build in any
// reasonable time on every self-update. The frontend is built once, on
// a real machine, via the .githooks/pre-commit hook (see README), so
// go:embed always has a ready static bundle sitting in the checkout
// this pulls. It does NOT restart anything — the caller decides how
// (procmgr.Restart for the CLI; the gateway handler needs to flush its
// HTTP response first, since restarting kills the very process serving
// it).
//
// Run does NOT itself acquire AcquireLock — the caller must already be
// holding it, and must keep holding it through the restart that follows
// Run, not just through this call. See AcquireLock's doc comment for the
// race that requires this: this used to acquire+release the lock
// entirely internally, which protected the pull/build but left the
// restart itself — which can legitimately take tens of seconds (see
// procmgr/systemd.go's TimeoutStopSec) — completely unguarded, wide open
// to a second update racing in and overwriting the very binary the OS
// was mid-exec'ing for the first restart.
func Run(repoPath string) (*Result, error) {
	binaryPath := filepath.Join(repoPath, "polaris")

	// Defensive: a previous run that hit a real merge conflict below could
	// have left the checkout mid-merge, which would make every update
	// after it fail identically forever with no way for a fresh attempt to
	// know that's what's wrong. Safe unconditionally: MERGE_HEAD only
	// exists before a merge commit is actually made, so this can't discard
	// any committed work — see abortLeftoverMerge's doc comment.
	abortLeftoverMerge(repoPath)

	pullCtx, pullCancel := context.WithTimeout(context.Background(), pullTimeout)
	defer pullCancel()
	// --no-rebase pins the reconciliation strategy explicitly rather than
	// relying on the host's ambient pull.rebase/pull.ff git config.
	// Verified live against the potato: with neither set (true there, and
	// true of a fresh git init in general), a modern git (2.47+) refuses
	// to even attempt a merge on a genuinely divergent checkout — it
	// hard-fails with "Need to specify how to reconcile divergent
	// branches" instead of merging, which would otherwise look like an
	// ordinary transient failure rather than the same "wedged until
	// someone manually fixes git" problem abortLeftoverMerge exists to
	// avoid (found and fixed identically in compose/watcher/update.sh's
	// equivalent step — this is the bare-metal side of the same bug).
	pullCmd := exec.CommandContext(pullCtx, "git", "pull", "--no-rebase", "origin", "main")
	pullCmd.Dir = repoPath
	pullOut, err := pullCmd.CombinedOutput()
	if err != nil {
		log.Warn("git pull failed", "err", err, "timed_out", pullCtx.Err() != nil)
		// A real conflict (not just a network failure) leaves .git
		// mid-merge — clean that up now so the *next* Run starts fresh
		// instead of failing the exact same way forever.
		abortLeftoverMerge(repoPath)
		return &Result{PullOutput: string(pullOut)}, fmt.Errorf("git pull failed: %w", err)
	}

	buildCtx, buildCancel := context.WithTimeout(context.Background(), buildTimeout)
	defer buildCancel()
	buildCmd := exec.CommandContext(buildCtx, "go", "build", "-ldflags=-s -w", "-o", "polaris", ".")
	buildCmd.Dir = repoPath
	buildOut, err := buildCmd.CombinedOutput()
	if err != nil {
		log.Warn("go build failed", "err", err, "timed_out", buildCtx.Err() != nil)
		return &Result{PullOutput: string(pullOut), BuildOutput: string(buildOut)}, fmt.Errorf("go build failed: %w", err)
	}

	return &Result{PullOutput: string(pullOut), BuildOutput: string(buildOut), BinaryPath: binaryPath}, nil
}

// abortLeftoverMerge aborts an in-progress git merge in repoPath, if any
// — called both before Run's own pull (in case a previous run left one
// behind) and after a failed pull (in case this run's own attempt just
// created one). MERGE_HEAD only exists between a merge starting and its
// commit landing, so aborting it can never discard committed work, only
// an already-broken merge attempt that shouldn't have been left sitting
// there. Errors are deliberately ignored — this is best-effort cleanup,
// not something worth failing Run over; if MERGE_HEAD doesn't exist,
// `git merge --abort` fails harmlessly and there's nothing to report.
func abortLeftoverMerge(repoPath string) {
	if _, err := os.Stat(filepath.Join(repoPath, ".git", "MERGE_HEAD")); err != nil {
		return
	}
	log.Warn("found a leftover merge in progress, aborting it before continuing")
	cmd := exec.Command("git", "merge", "--abort")
	cmd.Dir = repoPath
	_ = cmd.Run()
}

// RepoPath is just os.Getwd, wrapped for a clearer call site — both
// callers run from the repo root (systemd/launchd's WorkingDirectory, or
// wherever the CLI is invoked from).
func RepoPath() (string, error) {
	return os.Getwd()
}

// AcquireLock takes an exclusive, advisory file lock on a lockfile inside
// repoPath, so `polaris update` (SSH, cmd/update.go) and the settings
// panel's HTTP-triggered update (gateway/update.go) can't run concurrently
// and race two `git pull`s / two `go build -o polaris`s in the same
// working directory into a corrupted or truncated binary.
// gateway/update.go's updateStatus mutex only serializes concurrent
// requests to one already-running server process — it has no way to know
// about a second, entirely separate `polaris update` process started over
// SSH at the same moment.
//
// The caller must hold this across the *entire* update-and-restart
// sequence — Run, then whatever actually triggers the service restart —
// not just the build. Releasing right after Run (as this used to do
// internally) leaves the restart itself unprotected: procmgr.Restart can
// legitimately take tens of seconds (see procmgr/systemd.go's
// TimeoutStopSec), and a second update racing in during that window would
// overwrite the very binary file the OS is in the middle of exec'ing for
// the first restart — the exact corrupted-binary race this lock exists to
// prevent, just relocated from the build window into the restart window.
// cmd/update.go can just `defer release()` since it runs synchronously
// end to end; gateway/update.go's handler has to carry it into the
// detached goroutine that performs the actual restart, since the HTTP
// response (and the handler returning) has to happen before that restart
// can run.
//
// Unlike a plain "does a lockfile exist" check, flock is released by the
// kernel automatically if this process exits for any reason — including
// the restart this whole flow ends with on success — so there's no stale
// lock left behind to require manual cleanup after a crash.
//
// LOCK_NB (non-blocking): a second caller finding the lock already held
// fails immediately with a clear error, rather than blocking and possibly
// running its update at a surprising later moment.
func AcquireLock(repoPath string) (release func(), err error) {
	lockPath := filepath.Join(repoPath, ".update.lock")
	f, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return nil, fmt.Errorf("opening update lock file: %w", err)
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		f.Close()
		return nil, wrapFlockError(lockPath, err)
	}
	return func() {
		syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
		f.Close()
	}, nil
}

// wrapFlockError distinguishes real contention (LOCK_NB's documented
// EWOULDBLOCK/EAGAIN, meaning another process actually holds the lock)
// from every other way Flock can fail — a filesystem that doesn't support
// flock at all, ENOLCK, a permissions problem, etc. Folding both into the
// same "another update is already in progress" message used to send
// anyone debugging a permanently-broken self-update looking for a
// nonexistent concurrent run instead of the real, structural problem with
// the host — and on a filesystem that genuinely can't flock, every future
// call would fail the exact same misleading way forever.
func wrapFlockError(lockPath string, err error) error {
	if errors.Is(err, syscall.EWOULDBLOCK) {
		return fmt.Errorf("another update is already in progress: %w", err)
	}
	return fmt.Errorf("acquiring update lock at %s: %w", lockPath, err)
}
