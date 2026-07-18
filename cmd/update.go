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
	Long: `Pulls the latest code from the main branch, rebuilds the binary, and
restarts the service — no scp'd binaries, no manual redeploy steps.

Steps:
  1. git pull origin main
  2. go build -o polaris
  3. Restart service (systemd on the potato, launchd for local dev)

The settings panel's "push update now" button does the same thing over
HTTP (POST /api/update) — this CLI command is for SSH access.`,
	RunE: runUpdate,
}

func init() {
	rootCmd.AddCommand(updateCmd)
}

func runUpdate(cmd *cobra.Command, args []string) error {
	log := logger.WithPrefix("update")

	repoPath, err := updater.RepoPath()
	if err != nil {
		return fmt.Errorf("failed to get working directory: %w", err)
	}

	log.Info("pulling changes from origin/main...")
	result, err := updater.Run(repoPath)
	if err != nil {
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
