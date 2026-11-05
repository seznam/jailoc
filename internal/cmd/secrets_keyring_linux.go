//go:build linux

package cmd

import (
	"fmt"
	"math"
	"time"

	"golang.org/x/sys/unix"
)

type linuxSecretKeyring struct{}

var defaultSecretKeyring secretKeyring = linuxSecretKeyring{}

func (linuxSecretKeyring) get(account string) ([]byte, error) {
	id, err := unix.KeyctlSearch(unix.KEY_SPEC_USER_KEYRING, "user", account, 0)
	if err != nil {
		return nil, err
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

func (linuxSecretKeyring) set(account string, data []byte, ttl time.Duration) error {
	id, err := unix.AddKey("user", account, data, unix.KEY_SPEC_USER_KEYRING)
	if err != nil {
		return fmt.Errorf("add keyring entry: %w", err)
	}
	if sec := int(math.Ceil(ttl.Seconds())); sec > 0 {
		_, _ = unix.KeyctlInt(unix.KEYCTL_SET_TIMEOUT, id, sec, 0, 0)
	}
	return nil
}

func (linuxSecretKeyring) delete(account string) error {
	id, err := unix.KeyctlSearch(unix.KEY_SPEC_USER_KEYRING, "user", account, 0)
	if err != nil {
		return nil
	}
	_, _ = unix.KeyctlInt(unix.KEYCTL_UNLINK, id, unix.KEY_SPEC_USER_KEYRING, 0, 0)
	return nil
}
