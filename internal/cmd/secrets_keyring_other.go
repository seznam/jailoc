//go:build !darwin && !linux

package cmd

import "time"

type noopSecretKeyring struct{}

var defaultSecretKeyring secretKeyring = noopSecretKeyring{}

func (noopSecretKeyring) get(string) ([]byte, error)              { return nil, errSecretNotCached }
func (noopSecretKeyring) set(string, []byte, time.Duration) error { return nil }
func (noopSecretKeyring) delete(string) error                     { return nil }
