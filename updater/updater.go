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
	PullOutput     string
	FrontendOutput string
	BuildOutput    string
	BinaryPath     string
}

// Run pulls origin/main, rebuilds the frontend (if pnpm is available),
// and rebuilds the Go binary in repoPath. It does NOT restart anything —
// the caller decides how (procmgr.Restart for the CLI; the gateway
// handler needs to flush its HTTP response first, since restarting kills
// the very process serving it).
func Run(repoPath string) (*Result, error) {
	binaryPath := filepath.Join(repoPath, "polaris")
	result := &Result{}

	// Step 1: git pull
	pullCmd := exec.Command("git", "pull", "origin", "main")
	pullCmd.Dir = repoPath
	pullOut, err := pullCmd.CombinedOutput()
	result.PullOutput = string(pullOut)
	if err != nil {
		return result, fmt.Errorf("git pull failed: %w", err)
	}

	// Step 2: Rebuild the frontend. web/build/ is gitignored (Vite's output
	// isn't byte-reproducible across runs, so committing it just churns the
	// tree on every self-update) — this is the only way the embedded
	// frontend gets built at all, not an optional refresh.
	//
	// The test fixture repo has no web/ directory at all (it's testing the
	// git+go-build plumbing, not the frontend), so that case still skips
	// cleanly. A real deployment without web/ would be a broken checkout.
	webDir := filepath.Join(repoPath, "web")
	switch {
	case !dirExists(webDir):
		result.FrontendOutput = "(skipped - no web/ directory in this checkout)"
	case !hasPnpm():
		if dirExists(filepath.Join(webDir, "build")) {
			result.FrontendOutput = "(skipped - pnpm not installed; embedding the existing, possibly stale, web/build/)"
		} else {
			return result, fmt.Errorf("pnpm not installed and web/build/ doesn't exist — nothing for go:embed to embed")
		}
	default:
		frontendOut, err := rebuildFrontend(repoPath)
		result.FrontendOutput = frontendOut
		if err != nil {
			return result, fmt.Errorf("frontend rebuild failed: %w", err)
		}
	}

	// Step 3: go build
	buildCmd := exec.Command("go", "build", "-ldflags=-s -w", "-o", "polaris", ".")
	buildCmd.Dir = repoPath
	buildOut, err := buildCmd.CombinedOutput()
	result.BuildOutput = string(buildOut)
	result.BinaryPath = binaryPath
	if err != nil {
		return result, fmt.Errorf("go build failed: %w", err)
	}

	return result, nil
}

// hasPnpm checks if pnpm is available in PATH.
func hasPnpm() bool {
	_, err := exec.LookPath("pnpm")
	return err == nil
}

// dirExists checks if a directory exists.
func dirExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.IsDir()
}

// rebuildFrontend runs pnpm install && pnpm run build in web/.
func rebuildFrontend(repoPath string) (string, error) {
	webDir := filepath.Join(repoPath, "web")

	// pnpm install (ensure deps are up to date)
	installCmd := exec.Command("pnpm", "install")
	installCmd.Dir = webDir
	installOut, err := installCmd.CombinedOutput()
	if err != nil {
		return string(installOut), fmt.Errorf("pnpm install: %w", err)
	}

	// pnpm run build
	buildCmd := exec.Command("pnpm", "run", "build")
	buildCmd.Dir = webDir
	buildOut, err := buildCmd.CombinedOutput()
	if err != nil {
		return string(installOut) + "\n" + string(buildOut), fmt.Errorf("pnpm run build: %w", err)
	}

	return string(installOut) + "\n" + string(buildOut), nil
}

// RepoPath is just os.Getwd, wrapped for a clearer call site — both
// callers run from the repo root (systemd/launchd's WorkingDirectory, or
// wherever the CLI is invoked from).
func RepoPath() (string, error) {
	return os.Getwd()
}
