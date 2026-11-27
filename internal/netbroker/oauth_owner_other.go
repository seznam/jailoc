//go:build !darwin && !linux

package netbroker

import (
	"errors"
	"os"
)

// requireOwnedByCurrentUser fails closed: without Unix ownership the private
// socket directory cannot be verified, so the pending endpoint is unsupported.
func requireOwnedByCurrentUser(string, os.FileInfo) error {
	return errors.New("verifying parent directory ownership is unsupported on this platform")
}
