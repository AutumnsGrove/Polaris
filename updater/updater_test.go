package updater

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
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

	runGit(t, base, "init", "--bare", remote)
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

func TestHashFile_SameContentSameHash(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "lockfile")
	if err := os.WriteFile(path, []byte("dependencies: foo@1.0.0\n"), 0o644); err != nil {
		t.Fatalf("writing test file: %v", err)
	}

	h1 := hashFile(path)
	h2 := hashFile(path)
	if h1 == "" {
		t.Fatal("hashFile returned empty string for an existing file")
	}
	if h1 != h2 {
		t.Errorf("hashFile(%q) = %q then %q, want identical hashes for unchanged content", path, h1, h2)
	}
}

func TestHashFile_DifferentContentDifferentHash(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "lockfile")

	os.WriteFile(path, []byte("dependencies: foo@1.0.0\n"), 0o644)
	h1 := hashFile(path)

	os.WriteFile(path, []byte("dependencies: foo@2.0.0\n"), 0o644)
	h2 := hashFile(path)

	if h1 == h2 {
		t.Errorf("hashFile returned the same hash %q for different content", h1)
	}
}

func TestHashFile_MissingFileReturnsEmpty(t *testing.T) {
	if got := hashFile(filepath.Join(t.TempDir(), "does-not-exist")); got != "" {
		t.Errorf("hashFile(missing) = %q, want empty string", got)
	}
}

func TestRebuildFrontend_SkipsInstallWhenLockfileUnchanged(t *testing.T) {
	if _, err := exec.LookPath("pnpm"); err != nil {
		t.Skip("pnpm not on PATH")
	}

	dir := t.TempDir()
	webDir := filepath.Join(dir, "web")
	if err := os.MkdirAll(filepath.Join(webDir, "node_modules"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	lockPath := filepath.Join(webDir, "pnpm-lock.yaml")
	os.WriteFile(lockPath, []byte("lockfileVersion: '9.0'\n"), 0o644)

	// Prime the cache marker as if a prior install already ran against
	// this exact lockfile content.
	os.WriteFile(lockfileHashPath(webDir), []byte(hashFile(lockPath)), 0o644)

	// package.json is required for `pnpm run build` to resolve a script,
	// but since we're only exercising the install-skip decision here (not
	// asserting a successful build), a script that just exits 0 is enough.
	os.WriteFile(filepath.Join(webDir, "package.json"), []byte(`{"scripts":{"build":"true"}}`), 0o644)

	out, err := rebuildFrontend(dir)
	if err != nil {
		t.Fatalf("rebuildFrontend returned error: %v\noutput: %s", err, out)
	}
	if !strings.Contains(out, "skipping pnpm install") {
		t.Errorf("output = %q, want it to report skipping the install step", out)
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
