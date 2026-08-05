package gateway

import (
	"context"
	"net/http"
	"os/exec"
	"strings"
	"time"
)

// versionCmdTimeout bounds each git subprocess this reads — without it, a
// hung git (e.g. an index lock held by a concurrent self-update) would
// block every /api/version request indefinitely.
const versionCmdTimeout = 5 * time.Second

// handleVersion returns build info so the frontend can display it and
// force a cache-bust when the version changes.
func (s *Server) handleVersion(w http.ResponseWriter, r *http.Request) {
	version := getVersion()
	writeJSON(w, map[string]string{
		"version": version,
	})
}

// getVersion returns a monotonic version based on git commit count.
// Format: "r<count>.<short-hash>" (e.g., "r347.a1b2c3d")
// Falls back to "dev" if not in a git repo or git command fails.
func getVersion() string {
	ctx, cancel := context.WithTimeout(context.Background(), versionCmdTimeout)
	defer cancel()

	// Get commit count (monotonically increasing)
	countCmd := exec.CommandContext(ctx, "git", "rev-list", "--count", "HEAD")
	countOut, err := countCmd.Output()
	if err != nil {
		log.Warn("getting git commit count failed, falling back to \"dev\"", "err", err)
		return "dev"
	}
	count := strings.TrimSpace(string(countOut))

	// Get short commit hash
	hashCmd := exec.CommandContext(ctx, "git", "rev-parse", "--short=7", "HEAD")
	hashOut, err := hashCmd.Output()
	if err != nil {
		log.Warn("getting git commit hash failed", "err", err)
		return "r" + count
	}
	hash := strings.TrimSpace(string(hashOut))

	return "r" + count + "." + hash
}
