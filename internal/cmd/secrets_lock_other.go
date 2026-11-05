//go:build !darwin && !linux

package cmd

// withSecretCacheLock runs fn. Other platforms have no secret cache.
func withSecretCacheLock(_ string, fn func() error) error { return fn() }
