package cmd

import (
	"github.com/spf13/cobra"
)

var updateCmd = &cobra.Command{
	Use:   "update",
	Short: "Pull latest code, rebuild, and restart the service",
	Long: `Resolves the latest published image's digest from GHCR (waiting out an
in-progress CI build if one just landed) and hands off to the host-side
update watcher, which pulls and recreates the container.

The settings panel's "push update now" button does the same thing over
HTTP (POST /api/update) — this CLI command is for SSH access.`,
	RunE: runUpdate,
}

func init() {
	rootCmd.AddCommand(updateCmd)
}

func runUpdate(cmd *cobra.Command, args []string) error {
	return runDockerModeCall("/api/update")
}
