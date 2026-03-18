package cmd

import (
	"fmt"

	"github.com/spf13/cobra"
)

var addCmd = &cobra.Command{
	Use:   "add [path]",
	Short: "Add current directory (or path) to a workspace",
	Long:  "Add a path to a workspace's path list and restart the environment.",
	Args:  cobra.MaximumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		return fmt.Errorf("not implemented yet")
	},
}

func init() {
	rootCmd.AddCommand(addCmd)
}
