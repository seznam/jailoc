//go:build !linux && !darwin

package vm

import "github.com/stacklok/go-microvm/runner"

// vmmSpawner returns nil: confinement of the VM runner is Linux (Landlock) and
// macOS (Seatbelt) only, so other hosts use go-microvm's default spawner.
func vmmSpawner(_ *consoleRedactor) runner.Spawner { return nil }
