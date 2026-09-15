package cmd

import (
	"github.com/spf13/cobra"
)

var restartCmd = &cobra.Command{
	Use:   "restart",
	Short: "Cleanly restart the running service, no pull or rebuild",
	Long: `Recreates the container from whatever image is already running (via the
host-side update watcher), skipping GHCR entirely — a restart should never
silently pick up whatever :latest happens to point at right now.

Use this instead of ` + "`polaris update`" + ` when there's nothing new to pull:
running update with no upstream changes still does a GHCR digest check (a
no-op either way) before it ever restarts anything. This skips straight to
the restart.

The settings panel's "Restart Polaris" button does the same thing over
HTTP (POST /api/restart) — this CLI command is for SSH access.`,
	RunE: runRestart,
}

func init() {
	rootCmd.AddCommand(restartCmd)
}

func runRestart(cmd *cobra.Command, args []string) error {
	return runDockerModeCall("/api/restart")
}
