//go:build darwin

package cmd

import (
	"bytes"
	"context"
	"encoding/hex"
	"errors"
	"fmt"
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
	if keychainItemMissing(err) {
		return nil, errSecretNotCached
	}
	if err != nil {
		return nil, fmt.Errorf("find keychain item: %w", err)
	}
	return hex.DecodeString(strings.TrimSpace(string(out)))
}

func (darwinSecretKeyring) set(account string, data []byte, _ time.Duration) error {
	ctx, cancel := context.WithTimeout(context.Background(), darwinKeychainTimeout)
	defer cancel()
	encoded := hex.EncodeToString(data)
	// Pass the command over stdin to `security -i` so the secret payload never
	// appears in process arguments (argv).
	cmd := exec.CommandContext(ctx, "/usr/bin/security", "-i") //nolint:gosec // G204: fixed binary reading subcommand from stdin
	cmd.Stdin = strings.NewReader("add-generic-password -U -s " + darwinKeychainService + " -a " + account + " -w " + encoded + "\n")
	cmd.Stderr = &bytes.Buffer{}
	return cmd.Run()
}

func (darwinSecretKeyring) delete(account string) error {
	ctx, cancel := context.WithTimeout(context.Background(), darwinKeychainTimeout)
	defer cancel()
	err := exec.CommandContext(ctx, "/usr/bin/security", "delete-generic-password", "-s", darwinKeychainService, "-a", account).Run() //nolint:gosec // G204: fixed binary with validated arguments
	if err != nil && !keychainItemMissing(err) {
		return fmt.Errorf("delete keychain item: %w", err)
	}
	return nil
}

// keychainItemMissing reports whether security(1) failed because the item
// does not exist (errSecItemNotFound, exit status 44).
func keychainItemMissing(err error) bool {
	var exitErr *exec.ExitError
	return errors.As(err, &exitErr) && exitErr.ExitCode() == 44
}
