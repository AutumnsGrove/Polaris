package gateway

import (
	"bufio"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"polaris/store"
)

// setWorkspaceDir rewrites h's config.yaml to add a code_exec.workspace_dir
// pointing at workspaceDir — liveConfig() reloads config.yaml fresh on
// every request, so this takes effect on the very next HTTP call, no
// server restart needed. The base default test config (writeTestConfig)
// never sets code_exec at all, matching a deployment with no workspace
// configured — most of this file's tests need one for real, hence this
// helper.
func setWorkspaceDir(t *testing.T, h *testHarness, workspaceDir string) {
	t.Helper()
	dir := filepath.Dir(h.cfgPath)
	contents := fmt.Sprintf(`
server:
  port: 0
openrouter:
  api_key: "test-key"
  base_url: %q
database:
  path: %q
default_model: "mimo-pro"
code_exec:
  workspace_dir: %q
`, h.llmBaseURL, filepath.Join(dir, "test.db"), workspaceDir)
	if err := os.WriteFile(h.cfgPath, []byte(contents), 0o644); err != nil {
		t.Fatalf("rewriting test config: %v", err)
	}
}

func TestHandleGetWorkspaceFile_NotFound(t *testing.T) {
	h := newTestHarness(t, "")
	setWorkspaceDir(t, h, filepath.Join(t.TempDir(), "workspaces"))
	resp, err := http.Get(h.url("/api/workspace/thread-1/missing.png"))
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("status = %d, want 404", resp.StatusCode)
	}
}

func TestHandleGetWorkspaceFile_PathTraversalRejected(t *testing.T) {
	h := newTestHarness(t, "")
	workspaceDir := filepath.Join(t.TempDir(), "workspaces")
	setWorkspaceDir(t, h, workspaceDir)

	// Write a real secret file one level above the workspace root, then
	// try to reach it via a thread_id of "..".
	secretPath := filepath.Join(filepath.Dir(workspaceDir), "secret.txt")
	if err := os.WriteFile(secretPath, []byte("do not serve me"), 0o644); err != nil {
		t.Fatalf("writing secret file: %v", err)
	}

	resp, err := http.Get(h.url("/api/workspace/../secret.txt"))
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer resp.Body.Close()
	// net/http's own client/ServeMux path cleaning already collapses
	// ".." segments before this ever reaches the handler, but the
	// assertion that matters is behavioral: whatever status comes back,
	// the secret's contents must never be served.
	body, _ := io.ReadAll(resp.Body)
	if string(body) == "do not serve me" {
		t.Errorf("path traversal served a file outside the workspace root: %q", string(body))
	}
}

// TestHandleGetWorkspaceFile_EncodedTraversalRejected reproduces (pre-fix)
// and guards against (post-fix) a real path traversal: the old check
// validated target against `filepath.Join(root, threadID)` — an
// intermediate "base" itself built from the untrusted threadID, not the
// real root — so it checked nothing when threadID alone had already
// escaped root. net/http's own ServeMux collapses a literal ".." path
// segment before routing (see TestHandleGetWorkspaceFile_PathTraversalRejected),
// but a percent-encoded "%2e%2e" only decodes to ".." AFTER {thread_id}
// has already matched it as one opaque segment, bypassing that collapse
// entirely — confirmed live: GET /api/workspace/%2e%2e/secret.txt served a
// file one level above the workspace root under the old check. Uses a raw
// socket, not http.Get, since Go's http.Client may normalize the request
// URL client-side before ever sending it — this needs to prove the
// server itself resists a raw, adversarial request line.
func TestHandleGetWorkspaceFile_EncodedTraversalRejected(t *testing.T) {
	h := newTestHarness(t, "")
	workspaceDir := filepath.Join(t.TempDir(), "workspaces")
	setWorkspaceDir(t, h, workspaceDir)

	secretPath := filepath.Join(filepath.Dir(workspaceDir), "secret.txt")
	if err := os.WriteFile(secretPath, []byte("do not serve me"), 0o644); err != nil {
		t.Fatalf("writing secret file: %v", err)
	}

	u, _ := url.Parse(h.srv.URL)
	attempts := []string{
		"/api/workspace/%2e%2e/secret.txt",
		"/api/workspace/..%2fsecret.txt/x",
		"/api/workspace/foo%2f..%2f..%2fsecret.txt/x",
		"/api/workspace/%2e%2e%2f%2e%2e%2fsecret.txt/x",
	}
	for _, path := range attempts {
		conn, err := net.Dial("tcp", u.Host)
		if err != nil {
			t.Fatalf("dial: %v", err)
		}
		fmt.Fprintf(conn, "GET %s HTTP/1.1\r\nHost: %s\r\nConnection: close\r\n\r\n", path, u.Host)
		resp, _ := io.ReadAll(bufio.NewReader(conn))
		conn.Close()
		if strings.Contains(string(resp), "do not serve me") {
			t.Errorf("path %q served the secret file! response:\n%s", path, string(resp))
		}
	}
}

func TestHandleGetWorkspaceFile_Success(t *testing.T) {
	h := newTestHarness(t, "")
	workspaceDir := filepath.Join(t.TempDir(), "workspaces")
	setWorkspaceDir(t, h, workspaceDir)

	threadDir := filepath.Join(workspaceDir, "thread-1")
	if err := os.MkdirAll(threadDir, 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	pngBytes := []byte("\x89PNG\r\n\x1a\n-fake-chart-bytes-")
	if err := os.WriteFile(filepath.Join(threadDir, "chart.png"), pngBytes, 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	resp, err := http.Get(h.url("/api/workspace/thread-1/chart.png"))
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); ct != "image/png" {
		t.Errorf("Content-Type = %q, want image/png", ct)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("reading body: %v", err)
	}
	if string(body) != string(pngBytes) {
		t.Errorf("body = %q, want the workspace file's bytes verbatim", string(body))
	}
}

func TestHandleGetWorkspaceFile_NoWorkspaceConfigured(t *testing.T) {
	h := newTestHarness(t, "")
	// writeTestConfig never sets code_exec.workspace_dir, so this
	// deployment has no workspace at all — the route must 404, not
	// panic on an empty WorkspaceDir.
	resp, err := http.Get(h.url("/api/workspace/thread-1/chart.png"))
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("status = %d, want 404", resp.StatusCode)
	}
}

// A thread inside a Field can read the field's shared directory through
// the same route (so `show` of a shared file works), the thread's own file
// wins a name clash, and a thread OUTSIDE the field never sees it.
func TestHandleGetWorkspaceFile_FieldFallback(t *testing.T) {
	h := newTestHarness(t, "")
	workspaceDir := filepath.Join(t.TempDir(), "workspaces")
	setWorkspaceDir(t, h, workspaceDir)

	p, err := h.db.CreateField(store.Field{Name: "p"})
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"in-field", "outside"} {
		if err := h.db.CreateThread(id, id, "m", "web"); err != nil {
			t.Fatal(err)
		}
	}
	if err := h.db.SetThreadField("in-field", &p.ID); err != nil {
		t.Fatal(err)
	}

	write := func(dir, name, content string) {
		t.Helper()
		if err := os.MkdirAll(filepath.Join(workspaceDir, dir), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(workspaceDir, dir, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write(p.ID, "shared.txt", "from the pool")
	write(p.ID, "both.txt", "shared version")
	write("in-field", "both.txt", "own version")

	get := func(thread, file string) (int, string) {
		t.Helper()
		resp, err := http.Get(h.url("/api/workspace/" + thread + "/" + file))
		if err != nil {
			t.Fatalf("GET: %v", err)
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, string(b)
	}

	if code, body := get("in-field", "shared.txt"); code != http.StatusOK || body != "from the pool" {
		t.Errorf("field thread reading a shared file: %d %q", code, body)
	}
	if code, body := get("in-field", "both.txt"); code != http.StatusOK || body != "own version" {
		t.Errorf("own file should shadow the shared one: %d %q", code, body)
	}
	if code, _ := get("outside", "shared.txt"); code != http.StatusNotFound {
		t.Errorf("a thread outside the field read the field's file: status %d, want 404", code)
	}
	// {filename} is one URL segment, so ".." (percent-encoded past net/http's
	// path cleaning) is the only traversal-capable value — and the thread's
	// own tier already resolves it to an existing directory, so the request
	// never reaches the field tier's own Rel check (kept as defense in
	// depth). The point here is only that nothing is ever served for it.
	if code, _ := get("in-field", "%2e%2e"); code == http.StatusOK {
		t.Errorf("encoded traversal was served with 200")
	}
}
