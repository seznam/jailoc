//go:build !linux

// Command jailoc-guest runs only inside a Linux workspace VM.
package main

import (
	"fmt"
	"os"
)

func main() {
	fmt.Fprintln(os.Stderr, "jailoc-guest runs only inside a Linux VM")
	os.Exit(1)
}
