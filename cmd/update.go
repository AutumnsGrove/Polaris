package cmd

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"

	"github.com/spf13/cobra"

	"polaris/logger"
	"polaris/procmgr"
)

var updateCmd = &cobra.Command{
	Use:   "update",
	Short: "Pull latest code, rebuild, and restart the service",
	Long: `Pulls the latest code from the main branch, rebuilds the binary, and
restarts the service — no scp'd binaries, no manual redeploy steps.

Steps:
  1. git pull origin main
  2. go build -o polaris
  3. Restart service (systemd on the potato, launchd for local dev)`,
	RunE: runUpdate,
}

func init() {
	rootCmd.AddCommand(updateCmd)
}

func runUpdate(cmd *cobra.Command, args []string) error {
	log := logger.WithPrefix("update")

	repoPath, err := os.Getwd()
	if err != nil {
		return fmt.Errorf("failed to get working directory: %w", err)
	}
	binaryPath := filepath.Join(repoPath, "polaris")

	log.Info("pulling changes from origin/main...")
	pullCmd := exec.Command("git", "pull", "origin", "main")
	pullCmd.Dir = repoPath
	pullOut, err := pullCmd.CombinedOutput()
	if err != nil {
		fmt.Printf("pull failed:\n%s\n", string(pullOut))
		return fmt.Errorf("git pull failed: %w", err)
	}
	fmt.Printf("%s\n", string(pullOut))

	log.Info("building...")
	buildCmd := exec.Command("go", "build", "-ldflags=-s -w", "-o", "polaris", ".")
	buildCmd.Dir = repoPath
	buildOut, err := buildCmd.CombinedOutput()
	if err != nil {
		fmt.Printf("build failed:\n%s\n", string(buildOut))
		return fmt.Errorf("go build failed: %w", err)
	}
	fmt.Println("build successful")

	log.Info("restarting service...")
	mgr, err := procmgr.New("polaris")
	if err != nil {
		return fmt.Errorf("failed to create process manager: %w", err)
	}

	if !mgr.IsManaged() {
		fmt.Println("service is not managed by systemd/launchd — restart manually.")
		fmt.Printf("binary updated at: %s\n", binaryPath)
		return nil
	}

	if err := mgr.Restart(); err != nil {
		return fmt.Errorf("failed to restart service: %w", err)
	}

	fmt.Println("service restarted successfully")
	log.Info("update complete", "binary", binaryPath)
	return nil
}
