//go:build unix

package config

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"syscall"
)

func defaultPolicyPaths() []string {
	if runtime.GOOS == "darwin" {
		return []string{
			"/Library/Application Support/jailoc/policy.toml",
			"/etc/jailoc/policy.toml",
		}
	}
	return []string{"/etc/jailoc/policy.toml"}
}

func verifyPolicyTrust(path string) error {
	return verifyPolicyTrustWithUID(path, "", 0)
}

// verifyPolicyTrustWithUID verifies that path and every ancestor directory up
// to stopAt (or filesystem root when stopAt is empty), including across
// symlinks, are owned by expectedUID and not group/world-writable, and that
// the resolved target is a regular file.
func verifyPolicyTrustWithUID(path, stopAt string, expectedUID uint32) error {
	abs, err := filepath.Abs(path)
	if err != nil {
		return fmt.Errorf("resolve absolute path %q: %w", path, err)
	}
	if stopAt != "" {
		if stopAbs, err := filepath.Abs(stopAt); err == nil {
			stopAt = filepath.Clean(stopAbs)
		}
	}
	if err := checkPathChainTrust(abs, stopAt, expectedUID); err != nil {
		return err
	}
	resolved, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return fmt.Errorf("resolve symlinks for %q: %w", abs, err)
	}
	if resolved != abs {
		if err := checkPathChainTrust(resolved, stopAt, expectedUID); err != nil {
			return err
		}
	}
	info, err := os.Lstat(resolved)
	if err != nil {
		return fmt.Errorf("stat %q: %w", resolved, err)
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("%q must be a regular file", resolved)
	}
	return nil
}

func checkPathChainTrust(abs, stopAt string, expectedUID uint32) error {
	cur := filepath.Clean(abs)
	for {
		info, err := os.Lstat(cur)
		if err != nil {
			return fmt.Errorf("stat %q: %w", cur, err)
		}
		st, ok := info.Sys().(*syscall.Stat_t)
		if !ok {
			return fmt.Errorf("read ownership of %q: unsupported stat type", cur)
		}
		if st.Uid != expectedUID {
			return fmt.Errorf("%q is owned by uid %d, must be owned by uid %d", cur, st.Uid, expectedUID)
		}
		// Symlinks themselves have 0777 mode on Linux; check mode on non-symlinks.
		if info.Mode()&os.ModeSymlink == 0 && info.Mode().Perm()&0o022 != 0 {
			return fmt.Errorf("%q has unsafe permissions %04o (must not be group- or world-writable)", cur, info.Mode().Perm())
		}
		if stopAt != "" && cur == stopAt {
			break
		}
		parent := filepath.Dir(cur)
		if parent == cur {
			break
		}
		cur = parent
	}
	return nil
}
