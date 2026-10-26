// Command jailoc-netbroker serves the single network interface of a VM
// workspace from a userspace TCP/IP stack and enforces the workspace network
// policy on the real destination of every guest flow. The same command is
// available as the hidden "jailoc netbroker". See docs/research/p2-broker.md.
package main

import (
	"fmt"
	"os"

	"github.com/seznam/jailoc/internal/netbroker"
)

func main() {
	if err := netbroker.Main(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "jailoc-netbroker:", err)
		os.Exit(1)
	}
}
