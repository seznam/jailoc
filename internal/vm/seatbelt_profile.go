package vm

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/stacklok/go-microvm/runner"
)

// On macOS the VM runner starts under a Seatbelt (SBPL) profile that only
// grants what the Linux Landlock rules grant: the runtime, the VM's root file
// system, declared shares (read-only shares read-only), disks, logs and the
// broker's NIC socket. Apple's system.sb supplies what any process needs to
// start; everything else is denied. The profile is OS-independent text so it
// can be generated and tested anywhere.

// seatbeltProfile renders the SBPL profile for the runner of cfg executed
// from runnerPath. Every path is canonicalized, because Seatbelt matches the
// resolved path (/tmp is /private/tmp).
func seatbeltProfile(cfg runner.Config, runnerPath string) (string, error) {
	r := &sbplRules{}
	r.b.WriteString("(version 1)\n(deny default)\n(import \"system.sb\")\n")

	if runnerPath == "" {
		return "", errors.New("seatbelt profile: runner path is empty")
	}
	r.allow("process-exec", "literal", runnerPath)
	r.allow("file-read* file-map-executable", "literal", runnerPath)
	for _, d := range filepath.SplitList(cfg.LibDir) {
		if d != "" {
			r.allowLibDir(d)
		}
	}
	if cfg.RootPath != "" {
		r.allow("file-read* file-write*", "subpath", cfg.RootPath)
	}
	for _, m := range cfg.VirtioFS {
		if m.ReadOnly {
			r.allow("file-read*", "subpath", m.HostPath)
			continue
		}
		r.allow("file-read* file-write*", "subpath", m.HostPath)
	}
	// Disks and logs are single files: a literal rule keeps sibling files
	// (the tmp disk's lock file, other state) out of reach.
	for _, d := range cfg.Disks {
		if d.ReadOnly {
			r.allow("file-read*", "literal", d.HostPath)
			continue
		}
		r.allow("file-read* file-write-data", "literal", d.HostPath)
	}
	for _, p := range []string{cfg.ConsoleLog, cfg.VMLogPath} {
		if p != "" {
			r.allow("file-read* file-write*", "literal", p)
		}
	}
	// The broker's NIC socket; libkrun binds vsock sockets itself.
	if cfg.NetSocket != "" {
		r.allowSocket("network-outbound", "remote", cfg.NetSocket)
	}
	for _, v := range cfg.VsockPorts {
		r.allowSocket("network-bind network-inbound", "local", v.SocketPath)
		r.allow("file-read* file-write*", "literal", v.SocketPath)
	}
	if r.err != nil {
		return "", fmt.Errorf("seatbelt profile: %w", r.err)
	}
	return r.b.String(), nil
}

// sbplRules accumulates rules and remembers the first error.
type sbplRules struct {
	b              strings.Builder
	err            error
	seenLibTargets map[string]bool
}

// allow adds (allow ops (filter path)) for the canonical form of path.
func (r *sbplRules) allow(ops, filter, path string) {
	quoted, ok := r.quoted(path)
	if !ok {
		return
	}
	fmt.Fprintf(&r.b, "(allow %s (%s %s))\n", ops, filter, quoted)
}

// allowLibDir grants read+map-executable on dir and on the resolved targets of
// any .dylib symlinks inside dir that point elsewhere (e.g. /opt/homebrew/lib
// symlinks into /opt/homebrew/Cellar).
func (r *sbplRules) allowLibDir(dir string) {
	if r.err != nil {
		return
	}
	canonDir, err := canonicalPath(dir)
	if err != nil {
		r.err = err
		return
	}
	quotedDir, err := sbplQuote(canonDir)
	if err != nil {
		r.err = err
		return
	}
	fmt.Fprintf(&r.b, "(allow file-read* file-map-executable (subpath %s))\n", quotedDir)

	entries, err := os.ReadDir(canonDir)
	if err != nil {
		return
	}
	prefix := canonDir + string(filepath.Separator)
	for _, entry := range entries {
		if entry.Type()&fs.ModeSymlink == 0 || !strings.HasSuffix(entry.Name(), ".dylib") {
			continue
		}
		target, err := filepath.EvalSymlinks(filepath.Join(canonDir, entry.Name()))
		if err != nil {
			continue // ignore broken symlinks in shared directories such as /opt/homebrew/lib
		}
		if target == canonDir || strings.HasPrefix(target, prefix) {
			continue
		}
		if r.seenLibTargets == nil {
			r.seenLibTargets = make(map[string]bool)
		}
		if r.seenLibTargets[target] {
			continue
		}
		r.seenLibTargets[target] = true
		quotedTarget, err := sbplQuote(target)
		if err != nil {
			r.err = err
			return
		}
		fmt.Fprintf(&r.b, "(allow file-read* file-map-executable (literal %s))\n", quotedTarget)
	}
}

// allowSocket adds a rule for the Unix socket at path; side is "remote" or
// "local".
func (r *sbplRules) allowSocket(ops, side, path string) {
	quoted, ok := r.quoted(path)
	if !ok {
		return
	}
	fmt.Fprintf(&r.b, "(allow %s (%s unix-socket (path-literal %s)))\n", ops, side, quoted)
}

func (r *sbplRules) quoted(path string) (string, bool) {
	if r.err != nil {
		return "", false
	}
	canon, err := canonicalPath(path)
	if err != nil {
		r.err = err
		return "", false
	}
	quoted, err := sbplQuote(canon)
	if err != nil {
		r.err = err
		return "", false
	}
	return quoted, true
}

// sbplQuote renders s as an SBPL string literal.
func sbplQuote(s string) (string, error) {
	if strings.ContainsAny(s, "\x00\r\n") {
		return "", fmt.Errorf("path %q contains a NUL or newline byte", s)
	}
	return `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(s) + `"`, nil
}

// canonicalPath makes path absolute and resolves its symlinks. For a path
// that does not exist yet (a log file, a socket) it resolves the longest
// existing ancestor and appends the rest.
func canonicalPath(path string) (string, error) {
	if path == "" {
		return "", errors.New("empty path")
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", fmt.Errorf("absolute path of %s: %w", path, err)
	}
	return resolveExisting(abs)
}

func resolveExisting(abs string) (string, error) {
	resolved, err := filepath.EvalSymlinks(abs)
	if err == nil {
		return resolved, nil
	}
	if !errors.Is(err, fs.ErrNotExist) {
		return "", fmt.Errorf("resolve %s: %w", abs, err)
	}
	parent := filepath.Dir(abs)
	if parent == abs {
		return abs, nil
	}
	resolvedParent, err := resolveExisting(parent)
	if err != nil {
		return "", err
	}
	return filepath.Join(resolvedParent, filepath.Base(abs)), nil
}
