//go:build darwin && vmintegration

package vmintegration

import (
	"fmt"
	"os"

	"github.com/seznam/jailoc/internal/vm"
)

// maybeRunSeatbeltShim handles the macOS Seatbelt spawner re-executing the
// vmintegration test binary when builder tests call vm.Run in-process.
func maybeRunSeatbeltShim() {
	if len(os.Args) > 1 && os.Args[1] == "vm-seatbelt" {
		if err := vm.RunSeatbeltShim(os.Args[2:]); err != nil {
			fmt.Fprintln(os.Stderr, err)
		}
		os.Exit(1) // RunSeatbeltShim only returns on failure
	}
}
