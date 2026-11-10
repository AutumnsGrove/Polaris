package watcher

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

// These tests run the real compose/watcher/codeexec.sh against a fake `docker`
// so the runner's failure handling (issue #203) is pinned without a Docker
// daemon: a request that makes the script die must still produce an error
// result and not be left behind, queued requests must drain in one run, and
// abandoned files must be swept. The sandbox itself is exercised live (see
// docs/plans/code-execution.md); what is under test here is the script's own
// bookkeeping.

type codeExecResult struct {
	Stdout   string `json:"stdout"`
	ExitCode int    `json:"exit_code"`
	TimedOut bool   `json:"timed_out"`
	Error    string `json:"error"`
}

// newRunner lays out the install-dir shape codeexec.sh expects (it derives
// its paths from its own location) with a fake docker first on PATH, and
// returns a function that runs the script once and returns its combined
// output and error.
func newRunner(t *testing.T) (signalDir, workspace string, run func() (string, error), newCmd func() *exec.Cmd) {
	t.Helper()
	for _, bin := range []string{"bash", "python3", "flock"} {
		if _, err := exec.LookPath(bin); err != nil {
			t.Skipf("%s not available", bin)
		}
	}
	script, err := os.ReadFile("codeexec.sh")
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	scriptDir := filepath.Join(root, "compose", "watcher")
	signalDir = filepath.Join(root, "code-exec-signal")
	workspace = filepath.Join(root, "ws")
	binDir := filepath.Join(root, "bin")
	for _, d := range []string{scriptDir, signalDir, workspace, binDir} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	scriptPath := filepath.Join(scriptDir, "codeexec.sh")
	if err := os.WriteFile(scriptPath, script, 0o755); err != nil {
		t.Fatal(err)
	}
	// Only `docker run` has behavior; kill/rm are the no-op cleanups the
	// script always attempts.
	// Every invocation's argv is appended to docker-args.log so tests can
	// assert what the script actually asked docker to do.
	// FAKE_DOCKER_SLEEP makes `docker run` hang for that many seconds, so a test
	// can signal the script while a sandbox is "running".
	fakeDocker := "#!/bin/sh\necho \"$@\" >> \"" + filepath.Join(root, "docker-args.log") + "\"\nif [ \"$1\" = run ]; then\n  [ -n \"$FAKE_DOCKER_SLEEP\" ] && sleep \"$FAKE_DOCKER_SLEEP\"\n  echo fake-sandbox-ok\nfi\nexit 0\n"
	if err := os.WriteFile(filepath.Join(binDir, "docker"), []byte(fakeDocker), 0o755); err != nil {
		t.Fatal(err)
	}
	newCmd = func() *exec.Cmd {
		cmd := exec.Command("bash", scriptPath)
		cmd.Env = append(os.Environ(), "PATH="+binDir+":"+os.Getenv("PATH"))
		return cmd
	}
	run = func() (string, error) {
		cmd := newCmd()
		done := make(chan struct{})
		var out []byte
		var runErr error
		go func() { out, runErr = cmd.CombinedOutput(); close(done) }()
		select {
		case <-done:
		case <-time.After(60 * time.Second):
			_ = cmd.Process.Kill()
			t.Fatal("codeexec.sh did not finish")
		}
		return string(out), runErr
	}
	return signalDir, workspace, run, newCmd
}

func writeRequest(t *testing.T, signalDir, workspace, id string) {
	t.Helper()
	b, _ := json.Marshal(map[string]any{
		"host_workspace_dir": workspace, "code": "print(1)",
		"timeout_seconds": 5, "memory_limit_mb": 64, "pids_limit": 8,
	})
	if err := os.WriteFile(filepath.Join(signalDir, "requested-"+id+".json"), b, 0o644); err != nil {
		t.Fatal(err)
	}
}

func readResult(t *testing.T, signalDir, id string) codeExecResult {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(signalDir, "result-"+id+".json"))
	if err != nil {
		t.Fatalf("no result for %s: %v", id, err)
	}
	var r codeExecResult
	if err := json.Unmarshal(b, &r); err != nil {
		t.Fatalf("result for %s is not valid JSON: %v\n%s", id, err, b)
	}
	return r
}

func exists(p string) bool { _, err := os.Stat(p); return err == nil }

// The #203 failure mode: the script dying after it has picked a request up
// used to leave the request file behind and write no result, so Polaris
// waited out its whole timeout and blamed a missing watcher. Reverting the
// EXIT trap makes this fail.
func TestCodeExec_DeathMidRequestWritesErrorResultAndRemovesRequest(t *testing.T) {
	signalDir, _, run, _ := newRunner(t)
	req := filepath.Join(signalDir, "requested-bad.json")
	if err := os.WriteFile(req, []byte("{not json"), 0o644); err != nil {
		t.Fatal(err)
	}

	out, err := run()
	if err == nil {
		t.Fatal("expected the script to exit nonzero on an unparseable request")
	}
	if !strings.Contains(out, "requested-bad.json abandoned") {
		t.Errorf("the leftover request's name should be logged, got:\n%s", out)
	}
	if exists(req) {
		t.Error("the request file was left behind")
	}
	r := readResult(t, signalDir, "bad")
	if !strings.Contains(r.Error, "failed unexpectedly") || !strings.Contains(r.Error, "reading the request") {
		t.Errorf("error result should say what failed, got %q", r.Error)
	}
}

func TestCodeExec_MissingWorkspaceIsAnErrorResultNotASilentExit(t *testing.T) {
	signalDir, _, run, _ := newRunner(t)
	if err := os.WriteFile(filepath.Join(signalDir, "requested-nows.json"), []byte(`{"code":"print(1)"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := run(); err != nil {
		t.Fatalf("a refused request is handled, not a script failure: %v", err)
	}
	if r := readResult(t, signalDir, "nows"); !strings.Contains(r.Error, "host_workspace_dir") {
		t.Errorf("got %q", r.Error)
	}
	if exists(filepath.Join(signalDir, "requested-nows.json")) {
		t.Error("the request file was left behind")
	}
}

// One invocation must drain everything pending; relying on the path unit to
// re-fire is what the old script did.
func TestCodeExec_DrainsEveryPendingRequestInOneRun(t *testing.T) {
	signalDir, workspace, run, _ := newRunner(t)
	for _, id := range []string{"a", "b", "c"} {
		writeRequest(t, signalDir, workspace, id)
	}
	if out, err := run(); err != nil {
		t.Fatalf("run failed: %v\n%s", err, out)
	}
	for _, id := range []string{"a", "b", "c"} {
		r := readResult(t, signalDir, id)
		if r.ExitCode != 0 || r.Error != "" || !strings.Contains(r.Stdout, "fake-sandbox-ok") {
			t.Errorf("%s: unexpected result %+v", id, r)
		}
		if exists(filepath.Join(signalDir, "requested-"+id+".json")) {
			t.Errorf("request %s was not consumed", id)
		}
	}
}

func TestCodeExec_SweepsAbandonedFilesButNotFreshOnes(t *testing.T) {
	signalDir, workspace, run, _ := newRunner(t)
	old := time.Now().Add(-time.Hour)
	for _, name := range []string{"requested-stale.json", "result-stale.json"} {
		p := filepath.Join(signalDir, name)
		if err := os.WriteFile(p, []byte("{}"), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(p, old, old); err != nil {
			t.Fatal(err)
		}
	}
	writeRequest(t, signalDir, workspace, "fresh")

	out, err := run()
	if err != nil {
		t.Fatalf("run failed: %v\n%s", err, out)
	}
	for _, name := range []string{"requested-stale.json", "result-stale.json"} {
		if exists(filepath.Join(signalDir, name)) {
			t.Errorf("%s should have been swept", name)
		}
		if !strings.Contains(out, name) {
			t.Errorf("the sweep should log %s by name, got:\n%s", name, out)
		}
	}
	if r := readResult(t, signalDir, "fresh"); r.Error != "" {
		t.Errorf("a fresh request must still run, got %+v", r)
	}
}

// `docker -v relative/path:/workspace` is a named-volume spec, not a bind
// mount, so a relative host_workspace_dir (the bare-metal dev config's own
// "./workspaces") failed every call with exit 125 before reaching the sandbox.
func TestCodeExec_RelativeWorkspaceIsMadeAbsoluteForTheBindMount(t *testing.T) {
	signalDir, _, run, _ := newRunner(t)
	writeRequest(t, signalDir, "./workspaces/thread-1", "rel")

	if out, err := run(); err != nil {
		t.Fatalf("run failed: %v\n%s", err, out)
	}
	logBytes, err := os.ReadFile(filepath.Join(filepath.Dir(signalDir), "docker-args.log"))
	if err != nil {
		t.Fatal(err)
	}
	root := filepath.Dir(signalDir)
	want := "-v " + filepath.Join(root, "workspaces/thread-1") + ":/workspace"
	if !strings.Contains(string(logBytes), want) {
		t.Errorf("docker run should bind-mount the absolute path, want %q in:\n%s", want, logBytes)
	}
}

// SIGTERM is how systemd enforces TimeoutStartSec (the unit's 120s sits right on
// top of a 120s code_exec timeout) and how a restart stops the unit. The runner
// must (1) still hand Polaris an error result that says it was stopped, not a
// generic failure, (2) remove the request, and (3) not leave the watchdog
// subshell behind: it inherits the flock fd, so an orphaned one holds the lock
// and makes every later run print "already in progress" and skip, which
// stranded code_exec for a day once before (issue #85).
func TestCodeExec_SigtermWritesAStoppedResultAndDoesNotStrandTheLock(t *testing.T) {
	signalDir, workspace, run, newCmd := newRunner(t)
	t.Setenv("FAKE_DOCKER_SLEEP", "30")
	writeRequest(t, signalDir, workspace, "slow")

	cmd := newCmd()
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	// Wait until the fake sandbox is genuinely "running", then stop the script.
	argsLog := filepath.Join(filepath.Dir(signalDir), "docker-args.log")
	started := false
	for i := 0; i < 200 && !started; i++ {
		if b, err := os.ReadFile(argsLog); err == nil && strings.Contains(string(b), "run ") {
			started = true
		} else {
			time.Sleep(25 * time.Millisecond)
		}
	}
	if !started {
		_ = cmd.Process.Kill()
		t.Fatal("the fake sandbox never started")
	}
	if err := cmd.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() { _ = cmd.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(15 * time.Second):
		_ = cmd.Process.Kill()
		t.Fatal("the script did not exit after SIGTERM")
	}

	r := readResult(t, signalDir, "slow")
	if !strings.Contains(r.Error, "stopped before it finished") || strings.Contains(r.Error, "failed unexpectedly") {
		t.Errorf("error should say the runner was stopped, got %q", r.Error)
	}
	if exists(filepath.Join(signalDir, "requested-slow.json")) {
		t.Error("the request file was left behind")
	}

	// The lock must be free: a fresh request has to actually run, not be skipped.
	t.Setenv("FAKE_DOCKER_SLEEP", "")
	writeRequest(t, signalDir, workspace, "after")
	out, err := run()
	if err != nil {
		t.Fatalf("follow-up run failed: %v\n%s", err, out)
	}
	if strings.Contains(out, "already in progress") {
		t.Fatalf("the lock was left held after SIGTERM:\n%s", out)
	}
	if got := readResult(t, signalDir, "after"); got.Error != "" || !strings.Contains(got.Stdout, "fake-sandbox-ok") {
		t.Errorf("the follow-up request did not run: %+v", got)
	}
}
