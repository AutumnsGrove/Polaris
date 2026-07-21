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
