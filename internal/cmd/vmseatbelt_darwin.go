//go:build darwin

package cmd

import (
	"github.com/seznam/jailoc/internal/vm"
	"github.com/spf13/cobra"
)

// vmSeatbeltCmd confines itself with the Seatbelt profile it receives from the
// VM spawner and execs the VM runner. The spawner starts it by re-executing
// the jailoc binary, so the shim ships inside jailoc.
var vmSeatbeltCmd = &cobra.Command{
	Use:                "vm-seatbelt",
	Short:              "Run the VM runner under a Seatbelt profile (internal)",
	Hidden:             true,
	DisableFlagParsing: true,
	SilenceUsage:       true,
	PersistentPreRunE:  func(*cobra.Command, []string) error { return nil },
	RunE: func(_ *cobra.Command, args []string) error {
		return vm.RunSeatbeltShim(args)
	},
}

func init() {
	rootCmd.AddCommand(vmSeatbeltCmd)
}
