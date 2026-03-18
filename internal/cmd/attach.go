package cmd

import (
	"fmt"

	"github.com/spf13/cobra"
)

var inDocker bool

var attachCmd = &cobra.Command{
	Use:   "attach [workspace]",
	Short: "Attach to a running workspace (host opencode attach by default)",
	Args:  cobra.MaximumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		return fmt.Errorf("not implemented yet")
	},
}

func init() {
	attachCmd.Flags().BoolVar(&inDocker, "in-docker", false, "Run attach inside the container via exec instead of host opencode attach")
	rootCmd.AddCommand(attachCmd)
}
