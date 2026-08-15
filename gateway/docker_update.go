// handleDockerUpdate and its helpers are handleUpdate's Docker-mode
// counterpart — see that function's doc comment in update.go for the
// bare-metal git-pull-and-rebuild path this replaces under Docker.
package gateway

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// dockerImageRepo is the GHCR repository this build checks for updates
// against — must match docker-compose.yml's `image:` default
// (ghcr.io/autumnsgrove/polaris). Not user-configurable: like the
// model registry, this identifies what software this is, not an
// operator preference.
const dockerImageRepo = "autumnsgrove/polaris"

// dockerUpdateSignalDir is where compose/watcher/update.sh (running on
// the host, outside any container) looks for a pending update request —
// see docker-compose.yml's bind mount of ./update-signal onto this
// exact container path. A var, not a const, so tests can point it at a
// temp directory instead of actually writing under /data.
var dockerUpdateSignalDir = "/data/update-signal"

// ghcrRegistryBaseURL is a var, not a const, so tests can point it at a
// fake httptest server — same convention as tools/github_repo.go's
// githubAPIBaseURL.
var ghcrRegistryBaseURL = "https://ghcr.io"

const ghcrRequestTimeout = 15 * time.Second

// handleDockerUpdate resolves the latest published image's digest and
// hands off to the host-side update watcher by writing it to
// dockerUpdateSignalDir/requested, instead of git-pull-and-rebuilding
// in this process — there's no .git here (see .dockerignore) and
// rebuilding inside a running container fights the whole point of
// immutable images. Everything past writing that file — actually
// pulling and recreating this container — happens outside this
// process entirely, deliberately: see docker-compose.yml's comment on
// why Docker control stays off this container, the one process here
// that runs model-directed outbound requests.
//
// Reuses updateStatus and returns the same {success, restarting: true}
// response shape handleUpdate's bare-metal path does, so the existing
// frontend code (pushUpdate/waitForServerAndReload in
// state.svelte.ts) needs no changes at all — it already just polls
// /api/version until it changes, tolerant of the server being
// unreachable for a while, which is exactly what happens while the
// watcher pulls and swaps the container out from under this handler's
// own process.
func (s *Server) handleDockerUpdate(w http.ResponseWriter, r *http.Request) {
	started, _ := s.updateStatus.tryStart("update")
	if !started {
		writeJSON(w, map[string]interface{}{
			"success":         false,
			"already_running": true,
			"error":           "an update or restart is already in progress",
		})
		return
	}

	// Close the race window where CI is still building the commit that
	// was just pushed — without this, resolveLatestDigest below could
	// silently resolve the PREVIOUS build and report success while
	// actually delivering stale code. Best-effort: never blocks the
	// update over a GitHub API hiccup, see its own doc comment.
	waitForPublishWorkflow(r.Context(), s.liveConfig().GitHub.Token)

	digest, err := resolveLatestDigest(r.Context(), dockerImageRepo, "latest")
	if err != nil {
		s.updateStatus.finish(false, "", err.Error(), false)
		s.db.LogEvent("", "error", "update", "resolving latest image digest failed", map[string]interface{}{"err": err.Error()}, "")
		writeJSON(w, map[string]interface{}{
			"success": false,
			"error":   err.Error(),
		})
		return
	}

	target := "ghcr.io/" + dockerImageRepo + "@" + digest
	s.finishDockerSignal(w, "update", target, "requested update to")
}

// handleDockerRestart is handleRestart's Docker-mode counterpart: the
// same "recreate the container, but don't check for or pull anything
// new" restart bare-metal's handleRestart does with mgr.Restart() and
// no build — here that means handing the watcher the image reference
// this container is *already* running (POLARIS_RUNNING_IMAGE, set in
// docker-compose.yml from the same POLARIS_IMAGE value the `image:`
// field itself resolves to) instead of resolving a fresh digest from
// GHCR the way handleDockerUpdate does. `docker compose pull` on an
// already-present image is a fast no-op, so reusing the exact same
// watcher/script path for both costs nothing extra.
func (s *Server) handleDockerRestart(w http.ResponseWriter, r *http.Request) {
	started, _ := s.updateStatus.tryStart("restart")
	if !started {
		writeJSON(w, map[string]interface{}{
			"success":         false,
			"already_running": true,
			"error":           "an update or restart is already in progress",
		})
		return
	}

	target := os.Getenv("POLARIS_RUNNING_IMAGE")
	if target == "" {
		err := "POLARIS_RUNNING_IMAGE is not set — this container wasn't started via the project's docker-compose.yml"
		s.updateStatus.finish(false, "", err, false)
		writeJSON(w, map[string]interface{}{
			"success": false,
			"error":   err,
		})
		return
	}

	s.finishDockerSignal(w, "restart", target, "requested restart against")
}

// finishDockerSignal is handleDockerUpdate/handleDockerRestart's shared
// tail: write the resolved target to the update watcher's signal file,
// mark updateStatus finished, and respond — identical past the point
// each has already decided what target image to hand the watcher.
func (s *Server) finishDockerSignal(w http.ResponseWriter, kind, target, logVerb string) {
	if err := writeUpdateSignal(target); err != nil {
		s.updateStatus.finish(false, "", err.Error(), false)
		s.db.LogEvent("", "error", kind, "writing update signal failed", map[string]interface{}{"err": err.Error()}, "")
		writeJSON(w, map[string]interface{}{
			"success": false,
			"error":   err.Error(),
		})
		return
	}

	logOut := logVerb + " " + target + " — the host update watcher will pull and restart shortly"
	s.updateStatus.finish(true, logOut, "", true)
	s.db.LogEvent("", "info", kind, "docker "+kind+" requested", map[string]interface{}{"target": target}, "")

	writeJSON(w, map[string]interface{}{
		"success":    true,
		"log":        logOut,
		"restarting": true,
	})
}

// writeUpdateSignal atomically writes target to
// dockerUpdateSignalDir/requested via a temp-file-then-rename, not a
// direct write — polaris-update.path fires the instant this file
// exists (see that unit's PathExists), so a partial write here could
// hand the watcher a truncated image reference.
func writeUpdateSignal(target string) error {
	if err := os.MkdirAll(dockerUpdateSignalDir, 0o755); err != nil {
		return fmt.Errorf("creating update signal directory: %w", err)
	}
	dest := filepath.Join(dockerUpdateSignalDir, "requested")
	tmp := dest + ".tmp"
	if err := os.WriteFile(tmp, []byte(target), 0o644); err != nil {
		return fmt.Errorf("writing update signal: %w", err)
	}
	if err := os.Rename(tmp, dest); err != nil {
		return fmt.Errorf("finalizing update signal: %w", err)
	}
	return nil
}

// resolveLatestDigest queries GHCR's OCI Distribution API for repo:tag's
// current manifest digest ("sha256:..."), without needing docker CLI or
// socket access — a plain two-step HTTPS exchange (an anonymous pull
// token, then a HEAD for the manifest's Docker-Content-Digest header)
// any container with outbound internet already has, same as every
// other tool call this app makes.
func resolveLatestDigest(ctx context.Context, repo, tag string) (string, error) {
	token, err := ghcrAnonymousToken(ctx, repo)
	if err != nil {
		return "", fmt.Errorf("getting ghcr token: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodHead, ghcrRegistryBaseURL+"/v2/"+repo+"/manifests/"+tag, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	// Every manifest-list/index media type GHCR might serve for a
	// multi-arch tag (amd64 + arm64, for the potato), plus the plain v2
	// manifest as a fallback for a single-arch image.
	req.Header.Set("Accept", strings.Join([]string{
		"application/vnd.oci.image.index.v1+json",
		"application/vnd.docker.distribution.manifest.list.v2+json",
		"application/vnd.docker.distribution.manifest.v2+json",
	}, ","))

	client := &http.Client{Timeout: ghcrRequestTimeout}
	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("checking manifest: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("ghcr manifest check failed (status %d)", resp.StatusCode)
	}
	digest := resp.Header.Get("Docker-Content-Digest")
	if digest == "" {
		return "", errors.New("ghcr response had no Docker-Content-Digest header")
	}
	return digest, nil
}

// ghcrAnonymousToken exchanges for a pull-scoped anonymous token — GHCR
// requires this even for public images, unlike Docker Hub's plain
// unauthenticated manifest reads.
func ghcrAnonymousToken(ctx context.Context, repo string) (string, error) {
	url := ghcrRegistryBaseURL + "/token?service=ghcr.io&scope=repository:" + repo + ":pull"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "", err
	}
	client := &http.Client{Timeout: ghcrRequestTimeout}
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("token request failed (status %d)", resp.StatusCode)
	}
	var body struct {
		Token string `json:"token"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return "", fmt.Errorf("decoding token response: %w", err)
	}
	if body.Token == "" {
		return "", errors.New("ghcr token response had no token")
	}
	return body.Token, nil
}
