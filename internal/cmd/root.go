package cmd

import (
	"fmt"

	"github.com/spf13/cobra"
)

var rootCmd = &cobra.Command{
	Use:   "jailoc",
	Short: "Manage sandboxed OpenCode Docker environments",
	RunE: func(cmd *cobra.Command, args []string) error {
		// TODO T8: detect CWD workspace, prompt to add if unknown, auto-up, attach
		return fmt.Errorf("not implemented yet")
	},
}

// Execute is the entrypoint for the CLI. Version info is passed from main via ldflags.
func Execute(version, commit, date string) error {
	rootCmd.Version = fmt.Sprintf("%s (commit: %s, built: %s)", version, commit, date)
	return rootCmd.Execute()
}
