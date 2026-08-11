package gateway

import (
	"context"
	"net/http"
	"os/exec"
	"strings"
	"sync"
	"time"
)

// versionCmdTimeout bounds each git subprocess this reads — without it, a
// hung git (e.g. an index lock held by a concurrent self-update) would
// block the one process-lifetime computation below indefinitely.
const versionCmdTimeout = 5 * time.Second

// handleVersion returns build info so the frontend can display it and
// force a cache-bust when the version changes.
func (s *Server) handleVersion(w http.ResponseWriter, r *http.Request) {
	version := getVersion()
	writeJSON(w, map[string]string{
		"version": version,
	})
}

var (
	versionOnce   sync.Once
	cachedVersion string
)

// getVersion returns a monotonic version identifying the code this
// process is actually running, computed once (effectively at startup)
// and cached for the rest of the process's life — never re-read from
// git per request.
//
// This used to shell out fresh on every call. handleUpdate's self-update
// (see updater.Run) runs `git pull` followed by `go build` in this exact
// working directory, and deliberately does NOT stop or restart the
// server first — the old binary keeps serving every request, including
// this one, for however long the pull+build takes (potentially minutes
// on the potato's ARM CPU). The instant `git pull` lands, a live read
// here would see HEAD move to the new commit well before the new binary
// even finishes building, let alone restarts into — and the frontend's
// checkVersion() (state.svelte.ts) treats any observed version change as
// "go reload now", forcing an unannounced full-page reload on every
// connected client's next 30s poll, mid-conversation, for a build that
// isn't even running yet. Caching for the process lifetime ties the
// reported version to what this binary actually is, which is what
// "has it changed" is supposed to mean — it only ever changes across an
// actual restart, exactly when a reload is warranted.
func getVersion() string {
	versionOnce.Do(func() {
		cachedVersion = computeVersion()
	})
	return cachedVersion
}

// computeVersion does the actual git shell-outs — Format: "r<count>.<short-hash>"
// (e.g., "r347.a1b2c3d"). Falls back to "dev" if not in a git repo or git
// command fails.
func computeVersion() string {
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
