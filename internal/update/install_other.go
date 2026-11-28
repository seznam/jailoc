//go:build !darwin

package update

// removeQuarantine is a no-op outside macOS, which is the only supported host
// with a download quarantine attribute.
func removeQuarantine(string) error {
	return nil
}
