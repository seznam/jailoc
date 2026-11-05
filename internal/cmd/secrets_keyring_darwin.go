//go:build darwin

package cmd

import (
	"bytes"
	"context"
	"encoding/hex"
	"os/exec"
	"strings"
	"time"
)

const (
	darwinKeychainService = "jailoc-secret-cache"
	darwinKeychainTimeout = 10 * time.Second
)

type darwinSecretKeyring struct{}

var defaultSecretKeyring secretKeyring = darwinSecretKeyring{}

func (darwinSecretKeyring) get(account string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), darwinKeychainTimeout)
	defer cancel()
	out, err := exec.CommandContext(ctx, "/usr/bin/security", "find-generic-password", "-s", darwinKeychainService, "-a", account, "-w").Output() //nolint:gosec // G204: fixed binary with validated arguments
	if err != nil {
		return nil, err
	}
	return hex.DecodeString(strings.TrimSpace(string(out)))
}

func (darwinSecretKeyring) set(account string, data []byte, _ time.Duration) error {
	ctx, cancel := context.WithTimeout(context.Background(), darwinKeychainTimeout)
	defer cancel()
	encoded := hex.EncodeToString(data)
	cmd := exec.CommandContext(ctx, "/usr/bin/security", "add-generic-password", "-U", "-s", darwinKeychainService, "-a", account, "-w", encoded) //nolint:gosec // G204: fixed binary with hex-encoded payload
	cmd.Stderr = &bytes.Buffer{}
	return cmd.Run()
}

func (darwinSecretKeyring) delete(account string) error {
	ctx, cancel := context.WithTimeout(context.Background(), darwinKeychainTimeout)
	defer cancel()
	_ = exec.CommandContext(ctx, "/usr/bin/security", "delete-generic-password", "-s", darwinKeychainService, "-a", account).Run() //nolint:gosec // G204: fixed binary with validated arguments
	return nil
}
