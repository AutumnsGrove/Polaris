package cmd

import "github.com/spf13/cobra"

var rootCmd = &cobra.Command{
	Use:   "localassistant",
	Short: "LocalAssistant — a private, self-hosted search-augmented AI assistant",
}

func Execute() error {
	return rootCmd.Execute()
}
