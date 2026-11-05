//go:build !darwin && !linux

package cmd

import "context"

// withSecretCacheLock runs fn. Other platforms have no secret cache.
func withSecretCacheLock(_ context.Context, _ string, fn func() error) error { return fn() }
