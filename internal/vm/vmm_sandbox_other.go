//go:build !linux

package vm

import "github.com/stacklok/go-microvm/runner"

// vmmSpawner returns nil: Landlock confinement of the VM runner is Linux-only,
// so other hosts use go-microvm's default spawner.
func vmmSpawner() runner.Spawner { return nil }
