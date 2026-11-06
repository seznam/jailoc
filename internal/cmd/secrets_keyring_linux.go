//go:build linux

package cmd

import (
	"errors"
	"fmt"
	"math"
	"time"

	"golang.org/x/sys/unix"
)

type linuxSecretKeyring struct{}

var defaultSecretKeyring secretKeyring = linuxSecretKeyring{}

func (linuxSecretKeyring) get(account string) ([]byte, error) {
	id, err := unix.KeyctlSearch(unix.KEY_SPEC_USER_KEYRING, "user", account, 0)
	if keyMissing(err) {
		return nil, errSecretNotCached
	}
	if err != nil {
		return nil, fmt.Errorf("search keyring: %w", err)
	}
	for range 3 {
		size, err := unix.KeyctlBuffer(unix.KEYCTL_READ, id, nil, 0)
		if err != nil || size <= 0 {
			return nil, fmt.Errorf("read keyring entry size: %w", err)
		}
		buf := make([]byte, size)
		n, err := unix.KeyctlBuffer(unix.KEYCTL_READ, id, buf, 0)
		if err != nil {
			return nil, fmt.Errorf("read keyring entry: %w", err)
		}
		if n < 0 {
			return nil, fmt.Errorf("invalid keyring entry size %d", n)
		}
		if n <= len(buf) {
			return buf[:n], nil
		}
	}
	return nil, fmt.Errorf("keyring entry %q changed size concurrently", account)
}

// userKeyPermissions grants full access to the possessor and UID owner so
// KEYCTL_SET_TIMEOUT, KEYCTL_READ and KEYCTL_UNLINK succeed even when the
// user keyring is not linked into the process's session keyring.
const userKeyPermissions = 0x3f3f0000

func (linuxSecretKeyring) set(account string, data []byte, ttl time.Duration) error {
	sec := int(math.Ceil(ttl.Seconds()))
	if sec <= 0 {
		return nil
	}
	id, err := unix.AddKey("user", account, data, unix.KEY_SPEC_PROCESS_KEYRING)
	if err != nil {
		return fmt.Errorf("add keyring entry: %w", err)
	}
	defer func() {
		_, _ = unix.KeyctlInt(unix.KEYCTL_UNLINK, id, unix.KEY_SPEC_PROCESS_KEYRING, 0, 0)
	}()
	if _, err := unix.KeyctlInt(unix.KEYCTL_SETPERM, id, userKeyPermissions, 0, 0); err != nil {
		return fmt.Errorf("set keyring entry permissions: %w", err)
	}
	if _, err := unix.KeyctlInt(unix.KEYCTL_SET_TIMEOUT, id, sec, 0, 0); err != nil {
		return fmt.Errorf("set keyring entry timeout: %w", err)
	}
	if _, err := unix.KeyctlInt(unix.KEYCTL_LINK, id, unix.KEY_SPEC_USER_KEYRING, 0, 0); err != nil {
		return fmt.Errorf("link keyring entry: %w", err)
	}
	return nil
}

func (linuxSecretKeyring) delete(account string) error {
	id, err := unix.KeyctlSearch(unix.KEY_SPEC_USER_KEYRING, "user", account, 0)
	if err == nil {
		_, err = unix.KeyctlInt(unix.KEYCTL_UNLINK, id, unix.KEY_SPEC_USER_KEYRING, 0, 0)
	}
	if err != nil && !keyMissing(err) {
		return fmt.Errorf("delete keyring entry: %w", err)
	}
	return nil
}

// keyMissing reports whether a keyctl error means the entry is gone.
func keyMissing(err error) bool {
	return errors.Is(err, unix.ENOKEY) || errors.Is(err, unix.EKEYEXPIRED) || errors.Is(err, unix.EKEYREVOKED)
}
