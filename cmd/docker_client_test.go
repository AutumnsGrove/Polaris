package cmd

import (
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestIsDockerComposeInstall(t *testing.T) {
	dir := t.TempDir()
	if isDockerComposeInstall(dir) {
		t.Error("got true for a directory with no docker-compose.yml, want false")
	}

	if err := os.WriteFile(filepath.Join(dir, "docker-compose.yml"), []byte("services: {}\n"), 0o644); err != nil {
		t.Fatalf("writing docker-compose.yml: %v", err)
	}
	if !isDockerComposeInstall(dir) {
		t.Error("got false for a directory with docker-compose.yml, want true")
	}
}

// fakeLocalPolarisServer starts an httptest server bound to
// 127.0.0.1:<some port>, matching how the real container's published
// port looks to a host-side process, and points POLARIS_PORT at it so
// dockerLocalBaseURL resolves to this fake instead of a real
// deployment.
func fakeLocalPolarisServer(t *testing.T, handler http.HandlerFunc) {
	t.Helper()
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listening on 127.0.0.1: %v", err)
	}
	srv := httptest.NewUnstartedServer(handler)
	srv.Listener.Close()
	srv.Listener = lis
	srv.Start()
	t.Cleanup(srv.Close)

	_, port, err := net.SplitHostPort(lis.Addr().String())
	if err != nil {
		t.Fatalf("splitting listener address: %v", err)
	}
	t.Setenv("POLARIS_PORT", port)
}

func TestRunDockerModeCall_Success(t *testing.T) {
	fakeLocalPolarisServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("method = %s, want POST", r.Method)
		}
		if r.URL.Path != "/api/update" {
			t.Errorf("path = %s, want /api/update", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"success":true,"log":"requested update to ghcr.io/x@sha256:abc","restarting":true}`))
	})

	output := captureStdout(t, func() {
		if err := runDockerModeCall("/api/update"); err != nil {
			t.Fatalf("runDockerModeCall() error = %v, want nil", err)
		}
	})

	if !strings.Contains(output, "requested update to ghcr.io/x@sha256:abc") {
		t.Errorf("output = %q, want the server's log message printed", output)
	}
	if !strings.Contains(output, "watcher will pull and restart") {
		t.Errorf("output = %q, want the restarting notice printed", output)
	}
}

func TestRunDockerModeCall_AlreadyRunning(t *testing.T) {
	fakeLocalPolarisServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"success":false,"already_running":true,"error":"an update or restart is already in progress"}`))
	})

	err := runDockerModeCall("/api/update")
	if err == nil {
		t.Fatal("runDockerModeCall() error = nil, want an error when already_running")
	}
	if !strings.Contains(err.Error(), "already in progress") {
		t.Errorf("error = %q, want it to mention an operation already in progress", err.Error())
	}
}

func TestRunDockerModeCall_ServerReportedFailure(t *testing.T) {
	fakeLocalPolarisServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"success":false,"error":"getting ghcr token: token request failed (status 403)"}`))
	})

	err := runDockerModeCall("/api/update")
	if err == nil {
		t.Fatal("runDockerModeCall() error = nil, want the server's reported error")
	}
	if !strings.Contains(err.Error(), "token request failed") {
		t.Errorf("error = %q, want the server's actual error message", err.Error())
	}
}

// TestRunDockerModeCall_ConnectionRefused covers what a stopped/never-
// started container looks like to this CLI — must fail with something
// actionable, not a raw connection-refused stack.
func TestRunDockerModeCall_ConnectionRefused(t *testing.T) {
	// A port nothing is listening on — reuse the "reserve then close"
	// trick to get a genuinely free port without a real server there.
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listening: %v", err)
	}
	_, port, _ := net.SplitHostPort(lis.Addr().String())
	lis.Close()
	t.Setenv("POLARIS_PORT", port)

	err = runDockerModeCall("/api/update")
	if err == nil {
		t.Fatal("runDockerModeCall() error = nil, want an error when nothing is listening")
	}
	if !strings.Contains(err.Error(), "is the container running") {
		t.Errorf("error = %q, want an actionable hint about the container not running", err.Error())
	}
}
