//go:build !((darwin || linux) && (arm64 || amd64))

package guestbin

func embedded() []byte { return nil }
