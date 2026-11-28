//go:build !darwin && !linux

package update

import "context"

// withInstallLock runs fn. Self-update is only supported on darwin and linux,
// so other platforms have no cross-process install lock.
func withInstallLock(_ context.Context, _ string, fn func() error) error { return fn() }
