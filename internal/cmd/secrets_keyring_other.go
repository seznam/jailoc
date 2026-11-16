//go:build !darwin && !linux

package cmd

import (
	"context"
	"time"
)

type noopSecretKeyring struct{}

var defaultSecretKeyring secretKeyring = noopSecretKeyring{}

func (noopSecretKeyring) get(context.Context, string) ([]byte, error) {
	return nil, errSecretNotCached
}
func (noopSecretKeyring) set(context.Context, string, []byte, time.Duration) error { return nil }
func (noopSecretKeyring) delete(context.Context, string) error                     { return nil }
