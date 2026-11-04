//go:build darwin || linux

package cmd

import (
	"errors"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strings"

	"golang.org/x/crypto/ssh"

	"github.com/seznam/jailoc/internal/config"
)

// guestHome is the agent's home directory in the guest.
const guestHome = "/home/agent"

// gitFileKeys are the path-valued Git variables whose files git_config
// copies, by lower-case name without subsection (as parseGitConfig reports
// them), with the name used in warnings.
var gitFileKeys = map[string]string{
	"core.excludesfile":      "core.excludesFile",
	"core.attributesfile":    "core.attributesFile",
	"commit.template":        "commit.template",
	"gpg.allowedsignersfile": "gpg.ssh.allowedSignersFile",
}

// gitPathKeys are the path-valued Git variables whose values under the host
// home directory the copied configuration files point at the guest home.
var gitPathKeys = map[string]bool{
	"include.path":           true,
	"includeif.path":         true,
	"core.excludesfile":      true,
	"core.attributesfile":    true,
	"commit.template":        true,
	"gpg.allowedsignersfile": true,
	"user.signingkey":        true,
}

// gitCredentialDirs are the directories under the home directory whose
// files git_config copies only when they hold public data: the allowed
// signers file and an SSH public signing key.
var gitCredentialDirs = map[string]bool{".ssh": true, ".gnupg": true, ".aws": true}

// gitConfigCopier collects the git_config snapshots.
type gitConfigCopier struct {
	home  string
	warn  func(format string, args ...any)
	out   []snapshot
	seen  map[string]bool // host paths
	dests map[string]bool // guest paths
	count int
}

// gitConfigSnapshots returns the snapshots that carry the user's Git
// configuration into the guest: ~/.gitconfig and the config, ignore and
// attributes files of $XDG_CONFIG_HOME/git (~/.config/git when xdg is
// empty), plus the files they reference under the home directory, with
// includes followed. Configuration files are copied with host-home paths
// rewritten for the guest; see rewriteGitConfig. Referenced files that are
// missing or outside the home directory are reported through warn.
func gitConfigSnapshots(home, xdg string, warn func(format string, args ...any)) []snapshot {
	if xdg == "" {
		xdg = filepath.Join(home, ".config")
	}
	c := &gitConfigCopier{home: home, warn: warn, seen: map[string]bool{}, dests: map[string]bool{}}
	c.config("", filepath.Join(home, ".gitconfig"), guestGitConfig)
	dir := filepath.Join(xdg, "git")
	c.config("", filepath.Join(dir, "config"), guestHome+"/.config/git/config")
	c.file("", filepath.Join(dir, "ignore"), guestHome+"/.config/git/ignore")
	c.file("", filepath.Join(dir, "attributes"), guestHome+"/.config/git/attributes")
	return c.out
}

// config copies the configuration file src to dest and follows its
// references. key names the variable that references src, empty for the
// files Git reads by default, which are skipped silently when missing.
func (c *gitConfigCopier) config(key, src, dest string) {
	dest, ok := c.claim(key, src, dest)
	if !ok {
		return
	}
	c.count++
	if c.count > maxConfigFiles {
		if c.count == maxConfigFiles+1 {
			c.warn("more than %d configuration files are included; %s and later includes are not copied to the VM", maxConfigFiles, src)
		}
		return
	}
	data, ok := readSmall(src, maxConfigSize, false)
	if !ok {
		c.warn("%s cannot be read as a configuration file of at most %d bytes; it is not copied to the VM", src, maxConfigSize)
		return
	}
	c.out = append(c.out, snapshot{Mount: mountOf(src, dest), data: rewriteGitConfig(data, c.home)})
	for _, e := range parseGitConfig(data) {
		name := e.section + "." + e.key
		switch {
		case name == "include.path" || name == "includeif.path":
			display := "include.path"
			if e.section == "includeif" {
				display = "includeIf.path"
			}
			if p, ok := c.expand(e.value); ok {
				c.config(display, p, "")
			} else if e.value != "" && !strings.HasPrefix(e.value, "~") && !strings.HasPrefix(e.value, "%(") {
				// Git resolves a relative include against the including file.
				c.config(display, filepath.Join(filepath.Dir(src), e.value), path.Join(path.Dir(dest), filepath.ToSlash(e.value)))
			}
		case gitFileKeys[name] != "":
			if p, ok := c.expand(e.value); ok {
				c.file(gitFileKeys[name], p, "")
			}
		case name == "user.signingkey":
			if p, ok := c.expand(e.value); ok {
				c.signingKey(p)
			}
		}
	}
}

// file copies the regular file src to dest. key is as for config.
func (c *gitConfigCopier) file(key, src, dest string) {
	dest, ok := c.claim(key, src, dest)
	if !ok {
		return
	}
	if !isRegularFile(src) {
		c.warn("%s %s is not a regular file; it is not copied to the VM", key, src)
		return
	}
	c.out = append(c.out, snapshot{Mount: mountOf(src, dest)})
}

// signingKey copies an SSH public key named by user.signingKey. A private key
// is never copied: signing in the VM goes through the forwarded SSH agent.
func (c *gitConfigCopier) signingKey(src string) {
	const key = "user.signingKey"
	data, ok := readSmall(src, maxPointerSize, false)
	if !ok {
		if c.exists(key, src) {
			c.warn("%s %s is not an SSH public key; it is not copied to the VM (point it at the public key and enable ssh_auth_sock)", key, src)
		}
		return
	}
	if _, _, _, _, err := ssh.ParseAuthorizedKey(data); err != nil {
		c.warn("%s %s is not an SSH public key; it is not copied to the VM (point it at the public key and enable ssh_auth_sock)", key, src)
		return
	}
	c.file(key, src, "")
}

// claim returns the destination of src and reports whether src is yet to be
// copied. An empty dest is derived from the location of src under the home
// directory; src outside the home directory, in a credentials directory or
// missing is reported and skipped.
func (c *gitConfigCopier) claim(key, src, dest string) (string, bool) {
	if c.seen[src] {
		return "", false
	}
	c.seen[src] = true
	if dest == "" {
		rel, err := filepath.Rel(c.home, src)
		if err != nil || rel == "." || rel == ".." || strings.HasPrefix(rel, "../") {
			c.warn("%s %s is outside the home directory; it is not copied to the VM", key, src)
			return "", false
		}
		first, _, _ := strings.Cut(rel, "/")
		public := key == "gpg.ssh.allowedSignersFile" || key == "user.signingKey"
		if gitCredentialDirs[first] && !public {
			c.warn("%s %s is in ~/%s; it is not copied to the VM", key, src, first)
			return "", false
		}
		dest = guestHome + "/" + filepath.ToSlash(rel)
	}
	if dest != guestHome && !strings.HasPrefix(dest, guestHome+"/") {
		c.warn("%s %s is outside the home directory in the VM; it is not copied", key, src)
		return "", false
	}
	if c.dests[dest] {
		return "", false
	}
	if key == "" {
		if _, err := os.Stat(src); errors.Is(err, fs.ErrNotExist) { //nolint:gosec // G703: Git configuration path
			return "", false
		}
	} else if !c.exists(key, src) {
		return "", false
	}
	c.dests[dest] = true
	return dest, true
}

// exists reports whether src exists, warning when it does not.
func (c *gitConfigCopier) exists(key, src string) bool {
	if _, err := os.Stat(src); errors.Is(err, fs.ErrNotExist) { //nolint:gosec // G703: Git configuration path
		c.warn("%s %s does not exist; it is not copied to the VM", key, src)
		return false
	}
	return true
}

// expand returns the absolute host path of a path value, expanding a leading
// ~/ to the home directory. It reports false for values that are not
// absolute paths, such as relative paths and literal keys.
func (c *gitConfigCopier) expand(value string) (string, bool) {
	if rest, ok := strings.CutPrefix(value, "~/"); ok {
		value = filepath.Join(c.home, rest)
	}
	if !filepath.IsAbs(value) {
		return "", false
	}
	return filepath.Clean(value), true
}

// mountOf returns the read-only snapshot mount of src at dest.
func mountOf(src, dest string) config.Mount {
	return config.Mount{Host: src, Container: dest, Mode: "ro"}
}

// gitdirTilde matches an includeIf section header whose gitdir condition
// starts with ~/.
var gitdirTilde = regexp.MustCompile(`(?i)^(\s*\[\s*includeif\s+"gitdir(?:/i)?:)~/`)

// rewriteGitConfig adapts a Git configuration file for the guest, where the
// home directory is /home/agent but workspace paths keep their host
// location. Path values under the host home directory become ~/ paths, so
// they name the copies in the guest home, and gitdir conditions starting with
// ~/ name the host home directory, so they keep matching the shared
// repositories.
func rewriteGitConfig(data []byte, home string) []byte {
	lines := strings.Split(string(data), "\n")
	out := make([]string, 0, len(lines))
	entries := parseGitConfig(data)
	next := 0
	for n := 0; n < len(lines); n++ {
		for next < len(entries) && entries[next].first < n {
			next++
		}
		line := lines[n]
		if next < len(entries) && entries[next].first == n {
			e := entries[next]
			if rest, ok := strings.CutPrefix(e.value, home+"/"); ok && gitPathKeys[e.section+"."+e.key] {
				line = rewriteGitEntry(line, "~/"+rest)
				n = e.last
			}
		}
		if m := gitdirTilde.FindStringSubmatchIndex(line); m != nil {
			line = line[:m[3]] + quoteGitString(home) + "/" + line[m[1]:]
		}
		out = append(out, line)
	}
	return []byte(strings.Join(out, "\n"))
}

// rewriteGitEntry returns the configuration line of the variable that starts
// on line with its value replaced by value. A section header in front of the
// variable is kept.
func rewriteGitEntry(line, value string) string {
	prefix := line[:len(line)-len(strings.TrimLeft(line, " \t"))]
	rest := strings.TrimLeft(line, " \t")
	if strings.HasPrefix(rest, "[") {
		end := strings.IndexByte(rest, ']')
		prefix += rest[:end+1] + " "
		rest = strings.TrimLeft(rest[end+1:], " \t")
	}
	key, _, _ := strings.Cut(rest, "=")
	return prefix + strings.TrimSpace(key) + " = \"" + quoteGitString(value) + "\""
}

// quoteGitString escapes s for a double-quoted Git configuration string.
func quoteGitString(s string) string {
	return strings.NewReplacer(`\`, `\\`, `"`, `\"`, "\n", `\n`, "\t", `\t`).Replace(s)
}
