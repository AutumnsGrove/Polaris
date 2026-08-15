package cmd

import "github.com/spf13/cobra"

// AppVersion is set by main.go (from main.Version, itself set at build
// time via -ldflags -X — see version.go) before Execute runs. run.go
// threads it into gateway.New so /api/version can report it — see
// gateway.Server's version field's doc comment for why that matters
// specifically for Docker builds.
var AppVersion string

var rootCmd = &cobra.Command{
	Use:   "polaris",
	Short: "Polaris — a private, self-hosted search-augmented AI assistant",
}

func Execute() error {
	return rootCmd.Execute()
}
