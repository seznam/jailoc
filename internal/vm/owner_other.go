//go:build !linux

package vm

// normalizeOwnership is a no-op: on macOS the guest sees the ownership the
// image cache records in override_stat extended attributes.
func normalizeOwnership(string) error { return nil }
