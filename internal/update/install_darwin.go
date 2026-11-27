//go:build darwin

package update

import (
	"errors"
	"fmt"

	"golang.org/x/sys/unix"
)

const quarantineAttr = "com.apple.quarantine"

// removeQuarantine clears the Gatekeeper quarantine attribute from path so the
// freshly installed binary can run. A missing attribute, or a filesystem
// without extended attributes, is not an error.
func removeQuarantine(path string) error {
	err := unix.Removexattr(path, quarantineAttr)
	if err == nil || errors.Is(err, unix.ENOATTR) || errors.Is(err, unix.ENOTSUP) {
		return nil
	}

	return fmt.Errorf("remove %s from %q: %w", quarantineAttr, path, err)
}
