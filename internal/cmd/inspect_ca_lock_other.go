//go:build !darwin && !linux

package cmd

// withInspectCALock runs fn. Other platforms run no VM, so no other process
// generates the CA.
func withInspectCALock(_ string, fn func() error) error { return fn() }
