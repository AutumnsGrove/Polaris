// Package updater implements the git-pull-and-rebuild half of Polaris's
// self-update flow. Shared by cmd/update.go (CLI) and the settings
// panel's "push update now" button (gateway's POST /api/update) — same
// steps either way, just triggered from a terminal or a browser.
package updater

import (
	"context"
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
func Run(repoPath string) (*Result, error) {
	release, err := acquireUpdateLock(repoPath)
	if err != nil {
		return &Result{}, err
	}
	defer release()

	binaryPath := filepath.Join(repoPath, "polaris")

	pullCtx, pullCancel := context.WithTimeout(context.Background(), pullTimeout)
	defer pullCancel()
	pullCmd := exec.CommandContext(pullCtx, "git", "pull", "origin", "main")
	pullCmd.Dir = repoPath
	pullOut, err := pullCmd.CombinedOutput()
	if err != nil {
		log.Warn("git pull failed", "err", err, "timed_out", pullCtx.Err() != nil)
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

// RepoPath is just os.Getwd, wrapped for a clearer call site — both
// callers run from the repo root (systemd/launchd's WorkingDirectory, or
// wherever the CLI is invoked from).
func RepoPath() (string, error) {
	return os.Getwd()
}

// acquireUpdateLock takes an exclusive, advisory file lock on a lockfile
// inside repoPath, so `polaris update` (SSH, cmd/update.go) and the
// settings panel's HTTP-triggered update (gateway/update.go) can't run
// concurrently and race two `git pull`s / two `go build -o polaris`s in
// the same working directory into a corrupted or truncated binary.
// gateway/update.go's updateStatus mutex only serializes concurrent
// requests to one already-running server process — it has no way to know
// about a second, entirely separate `polaris update` process started over
// SSH at the same moment.
//
// Unlike a plain "does a lockfile exist" check, flock is released by the
// kernel automatically if this process exits for any reason — including
// the restart this whole flow ends with on success — so there's no stale
// lock left behind to require manual cleanup after a crash.
//
// LOCK_NB (non-blocking): a second caller finding the lock already held
// fails immediately with a clear error, rather than blocking and possibly
// running its update at a surprising later moment.
func acquireUpdateLock(repoPath string) (release func(), err error) {
	lockPath := filepath.Join(repoPath, ".update.lock")
	f, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return nil, fmt.Errorf("opening update lock file: %w", err)
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		f.Close()
		return nil, fmt.Errorf("another update is already in progress: %w", err)
	}
	return func() {
		syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
		f.Close()
	}, nil
}
