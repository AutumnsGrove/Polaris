package cmd

import (
	"fmt"

	"github.com/spf13/cobra"

	"polaris/logger"
	"polaris/procmgr"
	"polaris/updater"
)

var updateCmd = &cobra.Command{
	Use:   "update",
	Short: "Pull latest code, rebuild, and restart the service",
	Long: `Updates and restarts the service — no scp'd binaries, no manual redeploy steps.
Behaves differently depending on how this install runs, auto-detected from
whether docker-compose.yml exists in the current directory:

Bare-metal:
  1. git pull origin main
  2. go build -o polaris
  3. Restart service (systemd on the potato, launchd for local dev)

Docker: resolves the latest published image's digest from GHCR (waiting out
an in-progress CI build if one just landed) and hands off to the host-side
update watcher, which pulls and recreates the container.

The settings panel's "push update now" button does the same thing over
HTTP (POST /api/update) — this CLI command is for SSH access.`,
	RunE: runUpdate,
}

func init() {
	rootCmd.AddCommand(updateCmd)
}

func runUpdate(cmd *cobra.Command, args []string) error {
	repoPath, err := updater.RepoPath()
	if err != nil {
		return fmt.Errorf("failed to get working directory: %w", err)
	}

	// Docker mode: there's no local .git to pull and no local go build
	// to run — this CLI runs on the host, outside any container, so
	// even if it happened to rebuild something, that binary would have
	// nothing to do with what's actually serving traffic. Delegate to
	// the real running container's own /api/update instead — see
	// runDockerModeCall's doc comment.
	//
	// Deliberately NOT gateway.DeploymentMode(): that reads
	// POLARIS_DEPLOYMENT, which is only ever set *inside* the
	// container's own environment (docker-compose.yml's environment:
	// block) — this CLI process, running on the host over SSH, never
	// has it set regardless of which way the install actually runs.
	// isDockerComposeInstall checks for docker-compose.yml in the
	// install directory instead, the one signal a host-side process
	// can actually observe.
	if isDockerComposeInstall(repoPath) {
		return runDockerModeCall("/api/update")
	}

	log := logger.WithPrefix("update")

	// Held across the whole pull+build+restart sequence below, not just
	// the build — see AcquireLock's doc comment for why releasing early
	// would leave the restart window open to a second update racing in.
	release, err := updater.AcquireLock(repoPath)
	if err != nil {
		return err
	}
	defer release()

	log.Info("pulling changes from origin/main...")
	result, err := updater.Run(repoPath)
	if err != nil {
		log.Error("self-update failed", "err", err, "pull_output", result.PullOutput, "build_output", result.BuildOutput)
		fmt.Printf("%s\n%s\n", result.PullOutput, result.BuildOutput)
		return err
	}
	fmt.Printf("%s\nbuild successful\n", result.PullOutput)

	log.Info("restarting service...")
	mgr, err := procmgr.New("polaris")
	if err != nil {
		return fmt.Errorf("failed to create process manager: %w", err)
	}

	if !mgr.IsManaged() {
		fmt.Println("service is not managed by systemd/launchd — restart manually.")
		fmt.Printf("binary updated at: %s\n", result.BinaryPath)
		return nil
	}

	if err := mgr.Restart(); err != nil {
		return fmt.Errorf("failed to restart service: %w", err)
	}

	fmt.Println("service restarted successfully")
	log.Info("update complete", "binary", result.BinaryPath)
	return nil
}
