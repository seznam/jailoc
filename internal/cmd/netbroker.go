package cmd

import (
	"github.com/seznam/jailoc/internal/netbroker"
	"github.com/spf13/cobra"
)

// netbrokerCmd runs the network broker of a vm-backend workspace. The vm
// supervisor starts it by re-executing the jailoc binary, so the broker ships
// inside jailoc instead of as a separate binary.
var netbrokerCmd = &cobra.Command{
	Use:                "netbroker",
	Short:              "Run the network broker of a vm-backend workspace (internal)",
	Hidden:             true,
	DisableFlagParsing: true,
	SilenceUsage:       true,
	RunE: func(_ *cobra.Command, args []string) error {
		return netbroker.Main(args)
	},
}

func init() {
	rootCmd.AddCommand(netbrokerCmd)
}
