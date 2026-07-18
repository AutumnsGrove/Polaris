package cmd

import (
	"fmt"
	"os"
	"os/user"
	"path/filepath"

	"github.com/spf13/cobra"

	"polaris/procmgr"
)

var installCmd = &cobra.Command{
	Use:   "install",
	Short: "Install Polaris as a systemd (Linux) or launchd (macOS) service and start it",
	Long: `Generates a systemd unit (Linux) or launchd plist (macOS) pointing at this
binary in its current directory, registers it with the supervisor, and starts it.
Restart=always keeps it running; 'polaris update' (or the settings panel's
update button) handles pulling new code and restarting afterward.`,
	RunE: runInstall,
}

func init() {
	rootCmd.AddCommand(installCmd)
}

func runInstall(cmd *cobra.Command, args []string) error {
	repoPath, err := os.Getwd()
	if err != nil {
		return fmt.Errorf("failed to get working directory: %w", err)
	}

	// Writing the systemd unit to /etc requires sudo, but the service
	// itself shouldn't run as root — a network-facing process with API
	// keys and a database has no business needing root privileges. sudo
	// re-execs this binary as root, so user.Current() would report
	// "root" here; SUDO_USER (set by sudo itself) is how a privileged
	// process recovers who actually invoked it.
	username := os.Getenv("SUDO_USER")
	if username == "" {
		u, err := user.Current()
		if err != nil {
			return fmt.Errorf("failed to get current user: %w", err)
		}
		username = u.Username
	}

	mgr, err := procmgr.New("polaris")
	if err != nil {
		return fmt.Errorf("failed to create process manager: %w", err)
	}

	cfg := procmgr.ServiceConfig{
		Label:      "polaris",
		BinaryPath: filepath.Join(repoPath, "polaris"),
		WorkDir:    repoPath,
		LogDir:     filepath.Join(repoPath, "logs"),
		User:       username,
		Path:       os.Getenv("PATH"),
	}

	if err := mgr.Install(cfg); err != nil {
		return fmt.Errorf("installing service: %w", err)
	}

	fmt.Printf("Installed and started via %s\n", mgr.Name())
	return nil
}
