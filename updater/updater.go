// Package updater implements the git-pull-and-rebuild half of Polaris's
// self-update flow. Shared by cmd/update.go (CLI) and the settings
// panel's "push update now" button (gateway's POST /api/update) — same
// steps either way, just triggered from a terminal or a browser.
package updater

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
)

// Result holds combined stdout/stderr from each step, for display in
// whichever UI triggered the update — a CLI or the settings panel.
type Result struct {
	PullOutput  string
	BuildOutput string
	BinaryPath  string
}

// Run pulls origin/main and rebuilds the binary in repoPath. It does NOT
// restart anything — the caller decides how (procmgr.Restart for the
// CLI; the gateway handler needs to flush its HTTP response first, since
// restarting kills the very process serving it).
func Run(repoPath string) (*Result, error) {
	binaryPath := filepath.Join(repoPath, "polaris")

	pullCmd := exec.Command("git", "pull", "origin", "main")
	pullCmd.Dir = repoPath
	pullOut, err := pullCmd.CombinedOutput()
	if err != nil {
		return &Result{PullOutput: string(pullOut)}, fmt.Errorf("git pull failed: %w", err)
	}

	buildCmd := exec.Command("go", "build", "-ldflags=-s -w", "-o", "polaris", ".")
	buildCmd.Dir = repoPath
	buildOut, err := buildCmd.CombinedOutput()
	if err != nil {
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
