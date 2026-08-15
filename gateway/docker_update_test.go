package gateway

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

// fakeGHCR builds a minimal stand-in for GHCR's token-exchange +
// manifest-digest exchange (see resolveLatestDigest's doc comment).
// tokenStatus/manifestStatus let tests force each step's failure path
// independently; digest is echoed back via Docker-Content-Digest on a
// successful manifest HEAD.
func fakeGHCR(t *testing.T, tokenStatus, manifestStatus int, digest string) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/token", func(w http.ResponseWriter, r *http.Request) {
		if tokenStatus != http.StatusOK {
			w.WriteHeader(tokenStatus)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"token":"fake-anonymous-token"}`))
	})
	mux.HandleFunc("/v2/autumnsgrove/polaris/manifests/latest", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer fake-anonymous-token" {
			t.Errorf("manifest request missing expected bearer token, got %q", r.Header.Get("Authorization"))
		}
		if manifestStatus != http.StatusOK {
			w.WriteHeader(manifestStatus)
			return
		}
		if digest != "" {
			w.Header().Set("Docker-Content-Digest", digest)
		}
		w.WriteHeader(http.StatusOK)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func withGHCRBaseURL(t *testing.T, url string) {
	t.Helper()
	original := ghcrRegistryBaseURL
	ghcrRegistryBaseURL = url
	t.Cleanup(func() { ghcrRegistryBaseURL = original })
}

func TestResolveLatestDigest_HappyPath(t *testing.T) {
	srv := fakeGHCR(t, http.StatusOK, http.StatusOK, "sha256:abc123")
	withGHCRBaseURL(t, srv.URL)

	digest, err := resolveLatestDigest(context.Background(), dockerImageRepo, "latest")
	if err != nil {
		t.Fatalf("resolveLatestDigest() error = %v, want nil", err)
	}
	if digest != "sha256:abc123" {
		t.Errorf("digest = %q, want %q", digest, "sha256:abc123")
	}
}

func TestResolveLatestDigest_TokenFailure(t *testing.T) {
	srv := fakeGHCR(t, http.StatusUnauthorized, http.StatusOK, "sha256:abc123")
	withGHCRBaseURL(t, srv.URL)

	if _, err := resolveLatestDigest(context.Background(), dockerImageRepo, "latest"); err == nil {
		t.Fatal("resolveLatestDigest() error = nil, want an error when the token exchange fails")
	}
}

func TestResolveLatestDigest_ManifestFailure(t *testing.T) {
	srv := fakeGHCR(t, http.StatusOK, http.StatusNotFound, "sha256:abc123")
	withGHCRBaseURL(t, srv.URL)

	if _, err := resolveLatestDigest(context.Background(), dockerImageRepo, "latest"); err == nil {
		t.Fatal("resolveLatestDigest() error = nil, want an error when the manifest check 404s")
	}
}

// TestResolveLatestDigest_MissingDigestHeader guards against silently
// returning an empty digest — writeUpdateSignal would then hand the
// watcher a target like "ghcr.io/autumnsgrove/polaris@" with nothing
// after the @, which `docker compose pull` would reject, but only after
// the button had already reported success.
func TestResolveLatestDigest_MissingDigestHeader(t *testing.T) {
	srv := fakeGHCR(t, http.StatusOK, http.StatusOK, "" /* no digest header */)
	withGHCRBaseURL(t, srv.URL)

	if _, err := resolveLatestDigest(context.Background(), dockerImageRepo, "latest"); err == nil {
		t.Fatal("resolveLatestDigest() error = nil, want an error when GHCR omits Docker-Content-Digest")
	}
}

func TestWriteUpdateSignal_WritesTargetAtomically(t *testing.T) {
	dir := t.TempDir()
	original := dockerUpdateSignalDir
	dockerUpdateSignalDir = filepath.Join(dir, "update-signal")
	t.Cleanup(func() { dockerUpdateSignalDir = original })

	target := "ghcr.io/autumnsgrove/polaris@sha256:abc123"
	if err := writeUpdateSignal(target); err != nil {
		t.Fatalf("writeUpdateSignal() error = %v, want nil", err)
	}

	got, err := os.ReadFile(filepath.Join(dockerUpdateSignalDir, "requested"))
	if err != nil {
		t.Fatalf("reading requested file: %v", err)
	}
	if string(got) != target {
		t.Errorf("requested file content = %q, want %q", got, target)
	}

	// No leftover .tmp file from the rename step.
	if _, err := os.Stat(filepath.Join(dockerUpdateSignalDir, "requested.tmp")); !os.IsNotExist(err) {
		t.Errorf("requested.tmp should not exist after a successful write, stat err = %v", err)
	}
}

// TestHandleDockerUpdate_EndToEnd exercises handleUpdate's actual HTTP
// entry point under POLARIS_DEPLOYMENT=docker, confirming the dispatch
// in update.go actually reaches handleDockerUpdate and that its full
// chain (resolve digest -> write signal -> mark updateStatus finished)
// produces the same {success, restarting: true} shape the bare-metal
// path returns — see handleDockerUpdate's doc comment on why the
// frontend needs no changes to consume this.
func TestHandleDockerUpdate_EndToEnd(t *testing.T) {
	t.Setenv("POLARIS_DEPLOYMENT", "docker")

	srv := fakeGHCR(t, http.StatusOK, http.StatusOK, "sha256:deadbeef")
	withGHCRBaseURL(t, srv.URL)

	dir := t.TempDir()
	original := dockerUpdateSignalDir
	dockerUpdateSignalDir = filepath.Join(dir, "update-signal")
	t.Cleanup(func() { dockerUpdateSignalDir = original })

	h := newTestHarness(t, "")
	resp, err := http.Post(h.url("/api/update"), "application/json", nil)
	if err != nil {
		t.Fatalf("POST /api/update: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}

	snap := h.srvObj.updateStatus.snapshot()
	if snap["success"] != true {
		t.Errorf("updateStatus success = %v, want true", snap["success"])
	}
	if snap["restarting"] != true {
		t.Errorf("updateStatus restarting = %v, want true", snap["restarting"])
	}

	got, err := os.ReadFile(filepath.Join(dockerUpdateSignalDir, "requested"))
	if err != nil {
		t.Fatalf("reading requested file: %v", err)
	}
	want := "ghcr.io/autumnsgrove/polaris@sha256:deadbeef"
	if string(got) != want {
		t.Errorf("requested file content = %q, want %q", got, want)
	}
}

// TestHandleDockerUpdate_AlreadyRunning confirms the Docker branch
// shares the same single-slot guard as bare-metal — two overlapping
// clicks must not both resolve+write a signal (the second write could
// race the watcher already consuming the first).
func TestHandleDockerUpdate_AlreadyRunning(t *testing.T) {
	t.Setenv("POLARIS_DEPLOYMENT", "docker")

	dir := t.TempDir()
	original := dockerUpdateSignalDir
	dockerUpdateSignalDir = filepath.Join(dir, "update-signal")
	t.Cleanup(func() { dockerUpdateSignalDir = original })

	h := newTestHarness(t, "")
	h.srvObj.updateStatus.tryStart("update") // simulate one already in flight

	resp, err := http.Post(h.url("/api/update"), "application/json", nil)
	if err != nil {
		t.Fatalf("POST /api/update: %v", err)
	}
	defer resp.Body.Close()

	if _, err := os.Stat(filepath.Join(dockerUpdateSignalDir, "requested")); !os.IsNotExist(err) {
		t.Errorf("requested file should not be written when an update is already running, stat err = %v", err)
	}
}

// TestHandleDockerRestart_UsesRunningImageNotGHCR confirms handleRestart's
// Docker branch skips digest resolution entirely (POLARIS_RUNNING_IMAGE
// is the target verbatim) — a restart shouldn't hit GHCR at all, let
// alone pick up whatever the latest tag happens to point at right now.
// No fakeGHCR server is set up for this test on purpose: if the restart
// path ever accidentally called resolveLatestDigest, ghcrRegistryBaseURL
// would still point at the real https://ghcr.io default and this test
// would either hang or fail against the live network, making that
// regression loud instead of silently passing.
func TestHandleDockerRestart_UsesRunningImageNotGHCR(t *testing.T) {
	t.Setenv("POLARIS_DEPLOYMENT", "docker")
	t.Setenv("POLARIS_RUNNING_IMAGE", "ghcr.io/autumnsgrove/polaris@sha256:currentlyrunning")

	dir := t.TempDir()
	original := dockerUpdateSignalDir
	dockerUpdateSignalDir = filepath.Join(dir, "update-signal")
	t.Cleanup(func() { dockerUpdateSignalDir = original })

	h := newTestHarness(t, "")
	resp, err := http.Post(h.url("/api/restart"), "application/json", nil)
	if err != nil {
		t.Fatalf("POST /api/restart: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}

	snap := h.srvObj.updateStatus.snapshot()
	if snap["kind"] != "restart" {
		t.Errorf("updateStatus kind = %v, want %q", snap["kind"], "restart")
	}
	if snap["success"] != true || snap["restarting"] != true {
		t.Errorf("updateStatus success/restarting = %v/%v, want true/true", snap["success"], snap["restarting"])
	}

	got, err := os.ReadFile(filepath.Join(dockerUpdateSignalDir, "requested"))
	if err != nil {
		t.Fatalf("reading requested file: %v", err)
	}
	want := "ghcr.io/autumnsgrove/polaris@sha256:currentlyrunning"
	if string(got) != want {
		t.Errorf("requested file content = %q, want %q (the currently-running image, not a freshly resolved digest)", got, want)
	}
}

// TestHandleDockerRestart_MissingEnvVar guards the "this container
// wasn't started via docker-compose.yml" edge case (e.g. a bare `docker
// run` with POLARIS_DEPLOYMENT=docker set by hand but not
// POLARIS_RUNNING_IMAGE) — must fail with a clear error, not write an
// empty/malformed target the watcher would choke on.
func TestHandleDockerRestart_MissingEnvVar(t *testing.T) {
	t.Setenv("POLARIS_DEPLOYMENT", "docker")
	// Deliberately not setting POLARIS_RUNNING_IMAGE.

	dir := t.TempDir()
	original := dockerUpdateSignalDir
	dockerUpdateSignalDir = filepath.Join(dir, "update-signal")
	t.Cleanup(func() { dockerUpdateSignalDir = original })

	h := newTestHarness(t, "")
	resp, err := http.Post(h.url("/api/restart"), "application/json", nil)
	if err != nil {
		t.Fatalf("POST /api/restart: %v", err)
	}
	defer resp.Body.Close()

	snap := h.srvObj.updateStatus.snapshot()
	if snap["success"] != false {
		t.Errorf("updateStatus success = %v, want false when POLARIS_RUNNING_IMAGE is unset", snap["success"])
	}

	if _, err := os.Stat(filepath.Join(dockerUpdateSignalDir, "requested")); !os.IsNotExist(err) {
		t.Errorf("requested file should not be written when POLARIS_RUNNING_IMAGE is missing, stat err = %v", err)
	}
}
