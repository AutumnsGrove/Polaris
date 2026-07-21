// Package updater implements the git-pull-and-rebuild half of Polaris's
// self-update flow. Shared by cmd/update.go (CLI) and the settings
// panel's "push update now" button (gateway's POST /api/update) — same
// steps either way, just triggered from a terminal or a browser.
package updater

import (
	"crypto/sha256"
	"encoding/hex"
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

// lockfileHashPath lives inside node_modules (already gitignored) rather
// than tracked anywhere — it's a local cache marker, not state anyone
// else needs to see.
func lockfileHashPath(webDir string) string {
	return filepath.Join(webDir, "node_modules", ".pnpm-lock-hash")
}

// hashFile returns the hex sha256 of a file's contents, or "" if it
// can't be read (missing lockfile, etc — callers treat that as "no
// cached install to compare against").
func hashFile(path string) string {
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// rebuildFrontend runs pnpm install (skipped if pnpm-lock.yaml is
// unchanged since the last successful install and node_modules already
// exists — on the potato's ARM board, `pnpm install`'s resolve step
// alone costs real time even when it ends up a no-op) followed by pnpm
// run build.
func rebuildFrontend(repoPath string) (string, error) {
	webDir := filepath.Join(repoPath, "web")
	lockPath := filepath.Join(webDir, "pnpm-lock.yaml")
	hashPath := lockfileHashPath(webDir)

	currentHash := hashFile(lockPath)
	cachedHashBytes, _ := os.ReadFile(hashPath)
	cachedHash := string(cachedHashBytes)
	nodeModulesOK := dirExists(filepath.Join(webDir, "node_modules"))

	var installOut string
	if currentHash != "" && currentHash == cachedHash && nodeModulesOK {
		installOut = "pnpm-lock.yaml unchanged, node_modules present — skipping pnpm install"
	} else {
		installCmd := exec.Command("pnpm", "install")
		installCmd.Dir = webDir
		out, err := installCmd.CombinedOutput()
		installOut = string(out)
		if err != nil {
			return installOut, fmt.Errorf("pnpm install: %w", err)
		}
		// Record what we just installed against, so the next run can skip
		// cleanly. Best-effort: a write failure here just means the next
		// run reinstalls unnecessarily, not a broken build.
		if currentHash != "" {
			_ = os.WriteFile(hashPath, []byte(currentHash), 0o644)
		}
	}

	// pnpm run build
	buildCmd := exec.Command("pnpm", "run", "build")
	buildCmd.Dir = webDir
	buildOut, err := buildCmd.CombinedOutput()
	if err != nil {
		return installOut + "\n" + string(buildOut), fmt.Errorf("pnpm run build: %w", err)
	}

	return installOut + "\n" + string(buildOut), nil
}

// RepoPath is just os.Getwd, wrapped for a clearer call site — both
// callers run from the repo root (systemd/launchd's WorkingDirectory, or
// wherever the CLI is invoked from).
func RepoPath() (string, error) {
	return os.Getwd()
}
