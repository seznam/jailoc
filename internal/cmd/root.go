package cmd

import (
	"fmt"

	"github.com/spf13/cobra"
)

var rootCmd = &cobra.Command{
	Use:   "jailoc",
	Short: "Manage sandboxed OpenCode Docker environments",
}

// Execute is the entrypoint for the CLI. Version info is passed from main via ldflags.
func Execute(version, commit, date string) error {
	rootCmd.Version = fmt.Sprintf("%s (commit: %s, built: %s)", version, commit, date)
	return rootCmd.Execute()
}
