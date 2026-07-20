package gateway

import (
	"net/http"
	"os/exec"
	"strings"
)

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
	// Get commit count (monotonically increasing)
	countCmd := exec.Command("git", "rev-list", "--count", "HEAD")
	countOut, err := countCmd.Output()
	if err != nil {
		return "dev"
	}
	count := strings.TrimSpace(string(countOut))

	// Get short commit hash
	hashCmd := exec.Command("git", "rev-parse", "--short=7", "HEAD")
	hashOut, err := hashCmd.Output()
	if err != nil {
		return "r" + count
	}
	hash := strings.TrimSpace(string(hashOut))

	return "r" + count + "." + hash
}
