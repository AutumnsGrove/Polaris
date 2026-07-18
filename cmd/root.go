package cmd

import "github.com/spf13/cobra"

var rootCmd = &cobra.Command{
	Use:   "polaris",
	Short: "Polaris — a private, self-hosted search-augmented AI assistant",
}

func Execute() error {
	return rootCmd.Execute()
}
