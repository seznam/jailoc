//go:build !((darwin && arm64) || (linux && (amd64 || arm64)))

package brokerbin

func embedded() []byte { return nil }
