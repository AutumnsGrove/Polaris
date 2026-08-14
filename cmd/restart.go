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
	Long: `Restarts the running service in place via the platform's service
manager (systemd on the potato, launchd for local dev) — no git pull, no
go build, just the restart.

Use this instead of ` + "`polaris update`" + ` when there's nothing new to pull:
running update with no upstream changes still does a git pull (a no-op)
and a go build (a real rebuild, even if usually fast) before it ever
restarts anything, which can stall for tens of seconds on the potato's
weak CPU for zero benefit. This skips straight to the restart.

The settings panel's "Restart Polaris" button does the same thing over
HTTP (POST /api/restart) — this CLI command is for SSH access.

Shares the same lock as ` + "`polaris update`" + ` (and the settings panel's
Update/Restart buttons) so the two can never race each other — a plain
restart won't fire mid-build, and an update won't start mid-restart.`,
	RunE: runRestart,
}

func init() {
	rootCmd.AddCommand(restartCmd)
}

func runRestart(cmd *cobra.Command, args []string) error {
	log := logger.WithPrefix("restart")

	repoPath, err := updater.RepoPath()
	if err != nil {
		return fmt.Errorf("failed to get working directory: %w", err)
	}

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
