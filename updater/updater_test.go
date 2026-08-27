package updater

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

func runGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v in %s failed: %v\n%s", args, dir, err, out)
	}
	return string(out)
}

// setupTestRepo builds a bare "remote" and a working "local" clone with
// a trivial buildable Go program, local git identity configured so
// commits work regardless of the environment's global git config, and
// origin/main pushed and tracked — i.e. exactly the shape Run() expects
// to operate on.
func setupTestRepo(t *testing.T) (repoPath string) {
	t.Helper()
	base := t.TempDir()
	remote := filepath.Join(base, "remote.git")
	local := filepath.Join(base, "local")

	runGit(t, base, "init", "--bare", "-b", "main", remote)
	runGit(t, base, "init", "-b", "main", local)
	runGit(t, local, "config", "user.email", "test@example.com")
	runGit(t, local, "config", "user.name", "Test")
	runGit(t, local, "remote", "add", "origin", remote)

	if err := os.WriteFile(filepath.Join(local, "go.mod"), []byte("module updatertestfixture\n\ngo 1.21\n"), 0o644); err != nil {
		t.Fatalf("writing go.mod: %v", err)
	}
	if err := os.WriteFile(filepath.Join(local, "main.go"), []byte("package main\n\nfunc main() {}\n"), 0o644); err != nil {
		t.Fatalf("writing main.go: %v", err)
	}
	runGit(t, local, "add", ".")
	runGit(t, local, "commit", "-m", "initial commit")
	runGit(t, local, "push", "-u", "origin", "main")

	return local
}

func TestRun_HappyPath(t *testing.T) {
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("go toolchain not on PATH")
	}
	repoPath := setupTestRepo(t)

	result, err := Run(repoPath)
	if err != nil {
		t.Fatalf("Run returned error: %v\npull output: %s\nbuild output: %s", err, result.PullOutput, result.BuildOutput)
	}
	if result.BinaryPath != filepath.Join(repoPath, "polaris") {
		t.Errorf("BinaryPath = %q, want %q", result.BinaryPath, filepath.Join(repoPath, "polaris"))
	}
	if _, err := os.Stat(result.BinaryPath); err != nil {
		t.Errorf("expected the built binary to exist at %s: %v", result.BinaryPath, err)
	}
}

func TestRun_GitPullFailureStopsBeforeBuild(t *testing.T) {
	// A directory that isn't a git repo at all — "git pull" fails
	// immediately, and Run() must report that failure (with PullOutput
	// populated) rather than proceeding to "go build".
	dir := t.TempDir()

	result, err := Run(dir)
	if err == nil {
		t.Fatal("expected an error when repoPath isn't a git repository")
	}
	if !strings.Contains(err.Error(), "git pull failed") {
		t.Errorf("err = %v, want it to identify the git pull step", err)
	}
	if result.BuildOutput != "" {
		t.Errorf("BuildOutput = %q, want empty — build must not run after a failed pull", result.BuildOutput)
	}
}

// TestAcquireLock_RejectsConcurrentCallOnSameRepo guards against a
// found-in-audit bug: `polaris update` (SSH, cmd/update.go) and the
// settings panel's HTTP-triggered update (gateway/update.go) are two
// entirely separate process invocations that both operate on the same
// repoPath, with no coordination between them beyond gateway's own
// in-process mutex (which only ever sees requests to one already-running
// server). Concurrently running `git pull` and `go build -o polaris` in
// the same working directory risks a corrupted or truncated binary — and
// per AcquireLock's doc comment, the lock must also stay held through the
// restart that follows Run, not just the build, so this exercises
// AcquireLock directly rather than through Run (which, since the caller
// now owns locking end to end, no longer touches the lock at all).
func TestAcquireLock_RejectsConcurrentCallOnSameRepo(t *testing.T) {
	repoPath := t.TempDir()

	release, err := AcquireLock(repoPath)
	if err != nil {
		t.Fatalf("AcquireLock: %v", err)
	}

	// Simulates a second process (or a second concurrent caller in this
	// one) trying to update the same repo while the first "holds" it —
	// e.g. still inside its own restart, per AcquireLock's doc comment.
	_, err = AcquireLock(repoPath)
	if err == nil {
		t.Fatal("expected AcquireLock to reject a concurrent call while the lock is held")
	}
	if !strings.Contains(err.Error(), "already in progress") {
		t.Errorf("err = %v, want it to explain another update is already in progress", err)
	}

	release()

	// Once released, a normal AcquireLock must succeed exactly as if
	// there'd been no contention at all.
	release2, err := AcquireLock(repoPath)
	if err != nil {
		t.Errorf("AcquireLock after releasing the first lock returned error: %v", err)
	} else {
		release2()
	}
}

// TestRun_DoesNotTouchTheLock confirms Run's contract change: locking is
// now entirely the caller's responsibility (AcquireLock, held across Run
// AND the restart that follows it — see both doc comments), so Run itself
// must succeed freely whether or not anything holds the lock, and must
// leave a lock the caller took out completely alone.
func TestRun_DoesNotTouchTheLock(t *testing.T) {
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("go toolchain not on PATH")
	}
	repoPath := setupTestRepo(t)

	release, err := AcquireLock(repoPath)
	if err != nil {
		t.Fatalf("AcquireLock: %v", err)
	}
	defer release()

	// Run must succeed even while the caller (this test) already holds
	// the lock — it no longer acquires its own.
	if _, err := Run(repoPath); err != nil {
		t.Fatalf("Run returned error while the caller already held the lock: %v", err)
	}

	// The lock Run supposedly doesn't touch must still be held afterward
	// — a second AcquireLock attempt (simulating a concurrent caller)
	// must still be rejected.
	if _, err := AcquireLock(repoPath); err == nil {
		t.Error("AcquireLock succeeded after Run — Run must not have released the caller's lock")
	}
}

func TestWrapFlockError_DistinguishesContentionFromOtherFailures(t *testing.T) {
	contention := wrapFlockError("/some/path", syscall.EWOULDBLOCK)
	if !strings.Contains(contention.Error(), "already in progress") {
		t.Errorf("contention error = %v, want it to say another update is already in progress", contention)
	}

	other := wrapFlockError("/some/path", syscall.EACCES)
	if strings.Contains(other.Error(), "already in progress") {
		t.Errorf("non-contention error = %v, want it NOT to claim another update is running", other)
	}
	if !strings.Contains(other.Error(), "/some/path") {
		t.Errorf("non-contention error = %v, want it to mention the lock path for debugging", other)
	}
}

// TestRun_MergeConflictDoesNotWedgeTheNextRun guards against a
// found-in-audit bug: git pull (no --ff-only) can hit a real conflict
// and leave the checkout mid-merge (.git/MERGE_HEAD present) with no
// cleanup — every future Run() would then fail the exact same way
// forever, since a fresh `git pull` refuses outright while a merge is
// already in progress. Verified by actually creating a real conflict
// (not a mocked one) and confirming both that the first Run reports it
// as a failure, and that a *second* Run — simulating the very next
// `polaris update` — succeeds instead of failing identically.
func TestRun_MergeConflictDoesNotWedgeTheNextRun(t *testing.T) {
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("go toolchain not on PATH")
	}
	repoPath := setupTestRepo(t)

	// Diverge: a commit on origin, and a different local commit touching
	// the same line — a real, unresolvable-by-git conflict.
	clone := t.TempDir()
	runGit(t, filepath.Dir(repoPath), "clone", filepath.Join(filepath.Dir(repoPath), "remote.git"), clone)
	runGit(t, clone, "config", "user.email", "test@example.com")
	runGit(t, clone, "config", "user.name", "Test")
	if err := os.WriteFile(filepath.Join(clone, "main.go"), []byte("package main\n\nfunc main() { println(\"origin\") }\n"), 0o644); err != nil {
		t.Fatalf("writing conflicting origin change: %v", err)
	}
	runGit(t, clone, "commit", "-am", "origin change")
	runGit(t, clone, "push", "-q", "origin", "main")

	if err := os.WriteFile(filepath.Join(repoPath, "main.go"), []byte("package main\n\nfunc main() { println(\"local\") }\n"), 0o644); err != nil {
		t.Fatalf("writing conflicting local change: %v", err)
	}
	runGit(t, repoPath, "commit", "-am", "local conflicting change")

	// First Run: hits the real conflict and must report failure.
	result, err := Run(repoPath)
	if err == nil {
		t.Fatal("expected Run to fail on a genuine merge conflict")
	}
	if !strings.Contains(err.Error(), "git pull failed") {
		t.Errorf("err = %v, want it to identify the git pull step", err)
	}
	if result.BuildOutput != "" {
		t.Errorf("BuildOutput = %q, want empty — build must not run after a failed pull", result.BuildOutput)
	}

	if _, statErr := os.Stat(filepath.Join(repoPath, ".git", "MERGE_HEAD")); statErr == nil {
		t.Fatal("MERGE_HEAD still present after a failed Run — the checkout is left wedged")
	}

	// Resolve the actual divergence the way an operator would (or just
	// take origin's version) so this second Run can succeed — this isn't
	// testing conflict *resolution*, only that the checkout isn't
	// permanently stuck refusing every future pull.
	runGit(t, repoPath, "reset", "--hard", "origin/main")

	if _, err := Run(repoPath); err != nil {
		t.Fatalf("second Run (simulating the next `polaris update`) still failed after the conflict was cleared: %v", err)
	}
}

func TestRepoPath_ReturnsWorkingDirectory(t *testing.T) {
	wd, err := os.Getwd()
	if err != nil {
		t.Fatalf("os.Getwd: %v", err)
	}
	got, err := RepoPath()
	if err != nil {
		t.Fatalf("RepoPath returned error: %v", err)
	}
	if got != wd {
		t.Errorf("RepoPath() = %q, want %q", got, wd)
	}
}
