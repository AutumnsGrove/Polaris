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
