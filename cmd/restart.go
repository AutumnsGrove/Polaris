package cmd

import (
	"fmt"

	"github.com/spf13/cobra"

	"polaris/logger"
	"polaris/procmgr"
	"polaris/updater"
)

var restartCmd = &cobra.Command{
	Use:   "restart",
	Short: "Cleanly restart the running service, no pull or rebuild",
	Long: `Restarts the running service in place — no pull, no rebuild, no new
image. Auto-detects how this install runs from whether docker-compose.yml
exists in the current directory:

Bare-metal: restarts via the platform's service manager (systemd on the
potato, launchd for local dev).

Docker: recreates the container from whatever image is already running
(via the host-side update watcher), skipping GHCR entirely — a restart
should never silently pick up whatever :latest happens to point at right
now.

Use this instead of ` + "`polaris update`" + ` when there's nothing new to pull:
running update with no upstream changes still does a git pull/GHCR check
(a no-op either way) before it ever restarts anything, which can stall for
tens of seconds for zero benefit on bare-metal's weak CPU. This skips
straight to the restart.

The settings panel's "Restart Polaris" button does the same thing over
HTTP (POST /api/restart) — this CLI command is for SSH access.

Bare-metal only: shares the same lock as ` + "`polaris update`" + ` (and the
settings panel's Update/Restart buttons) so the two can never race each
other — a plain restart won't fire mid-build, and an update won't start
mid-restart.`,
	RunE: runRestart,
}

func init() {
	rootCmd.AddCommand(restartCmd)
}

func runRestart(cmd *cobra.Command, args []string) error {
	repoPath, err := updater.RepoPath()
	if err != nil {
		return fmt.Errorf("failed to get working directory: %w", err)
	}

	// Docker mode: no systemd/launchd unit for this CLI to restart at
	// all — the container's own restart policy plays that role. See
	// cmd/update.go's identical check for why isDockerComposeInstall,
	// not gateway.deploymentMode, is the right signal for a host-side
	// process.
	if isDockerComposeInstall(repoPath) {
		return runDockerModeCall("/api/restart")
	}

	log := logger.WithPrefix("restart")

	// Same file lock handleUpdate/handleRestart hold across the gateway's
	// own pull+build+restart or plain-restart sequence — see
	// updater.AcquireLock's doc comment. A plain restart doesn't touch the
	// repo or the binary, but still needs to keep an update from starting
	// mid-restart, and keep a second restart from piling on top of this
	// one.
	release, err := updater.AcquireLock(repoPath)
	if err != nil {
		return err
	}
	defer release()

	mgr, err := procmgr.New("polaris")
	if err != nil {
		return fmt.Errorf("failed to create process manager: %w", err)
	}

	if !mgr.IsManaged() {
		return fmt.Errorf("service is not managed by systemd/launchd — restart manually")
	}

	log.Info("restarting service...")
	if err := mgr.Restart(); err != nil {
		return fmt.Errorf("failed to restart service: %w", err)
	}

	fmt.Println("service restarted successfully")
	log.Info("restart complete")
	return nil
}
