package cmd

import (
	"fmt"

	"github.com/spf13/cobra"
)

var upCmd = &cobra.Command{
	Use:   "up [workspace]",
	Short: "Start a workspace environment",
	Long:  "Start the Docker Compose environment for a workspace. If already running, no-op.",
	Args:  cobra.MaximumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		return fmt.Errorf("not implemented yet")
	},
}

func init() {
	rootCmd.AddCommand(upCmd)
}
