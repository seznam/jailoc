//go:build darwin || linux

package cmd

import (
	"errors"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"syscall"

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
	"user.signingkey":        "user.signingKey",
}

// publicFile is a kind of file that holds public data only, which git_config
// copies even from a credentials directory.
type publicFile struct {
	// parse returns the public data in a file of the kind, and reports false
	// when the file is not of the kind. Only the returned data is copied.
	parse func(data []byte) ([]byte, bool)
	// problem is the warning when parse reports false.
	problem string
}

// gitPublicFiles are the public files of gitFileKeys. A private key is never
// copied: signing in the VM goes through the forwarded SSH agent.
var gitPublicFiles = map[string]*publicFile{
	"gpg.allowedsignersfile": {allowedSigners, "is not an allowed signers file of SSH public keys; it is not copied to the VM"},
	"user.signingkey":        {publicKey, "is not an SSH public key; it is not copied to the VM (point it at the public key and enable ssh_auth_sock)"},
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
// files git_config copies only when they are gitPublicFiles.
var gitCredentialDirs = []string{".ssh", ".gnupg", ".aws"}

// protected is an existing directory of gitCredentialDirs or a file in one,
// with the name of the directory.
type protected struct {
	dir  string
	info fs.FileInfo
}

// gitConfigCopier collects the git_config snapshots.
type gitConfigCopier struct {
	home   string
	xdg    string
	warn   func(format string, args ...any)
	out    []snapshot
	refs   []func()           // references yet to be followed
	seen   map[[2]string]bool // host paths with their requested guest paths
	dests  map[string]claim   // guest paths
	dirs   []protected        // credentials directories
	files  []protected        // files in the credentials directories
	stores []fs.FileInfo      // Git credential stores
	count  int
}

// claim is the host file copied to a guest path.
type claim struct {
	src  string
	info fs.FileInfo
}

// gitConfigSnapshots returns the snapshots that carry the user's Git
// configuration into the guest: ~/.gitconfig and the config, ignore and
// attributes files of $XDG_CONFIG_HOME/git (~/.config/git when xdg is
// empty), plus the files they reference under the home directory, with
// includes followed. Configuration files are copied with host-home paths
// rewritten for the guest; see rewriteGitConfig. Referenced files that are
// missing, outside the home directory or hold credentials are reported
// through warn. Every snapshot carries the content jailoc read and checked.
func gitConfigSnapshots(home, xdg string, warn func(format string, args ...any)) []snapshot {
	if xdg == "" {
		xdg = filepath.Join(home, ".config")
	}
	c := &gitConfigCopier{home: home, xdg: xdg, warn: warn, seen: map[[2]string]bool{}, dests: map[string]claim{}}
	for _, name := range gitCredentialDirs {
		c.protect(name)
	}
	for _, p := range []string{
		filepath.Join(home, ".git-credentials"),
		filepath.Join(xdg, "git", "credentials"),
		filepath.Join(home, ".config", "git", "credentials"),
	} {
		if info, err := os.Stat(p); err == nil { //nolint:gosec // G703: Git credential store path
			c.stores = append(c.stores, info)
		}
	}
	// The files Git reads by default claim their guest paths before any
	// reference does.
	c.config("", filepath.Join(home, ".gitconfig"), guestGitConfig)
	dir := filepath.Join(xdg, "git")
	c.config("", filepath.Join(dir, "config"), guestHome+"/.config/git/config")
	c.file("", filepath.Join(dir, "ignore"), guestHome+"/.config/git/ignore", nil)
	c.file("", filepath.Join(dir, "attributes"), guestHome+"/.config/git/attributes", nil)
	for len(c.refs) > 0 {
		ref := c.refs[0]
		c.refs = c.refs[1:]
		ref()
	}
	return c.out
}

// maxProtectedEntries bounds the entries protect walks, so a huge credentials
// directory cannot stall the start of a workspace.
const maxProtectedEntries = 1 << 16

// protect records the credentials directory name under the home directory,
// the directories it links to and the files in all of them. Recording the
// files, and the files their links point to, catches hard links to them
// elsewhere. Entries that cannot be read are skipped; the directory check
// still covers the paths under them.
func (c *gitConfigCopier) protect(name string) {
	queue := []string{filepath.Join(c.home, name)}
	entries := 0
	for len(queue) > 0 && entries < maxProtectedEntries {
		// WalkDir does not descend into a symbolic link to a directory, so
		// it walks the resolved path.
		root, err := filepath.EvalSymlinks(queue[0])
		queue = queue[1:]
		if err != nil {
			continue
		}
		info, err := os.Stat(root) //nolint:gosec // G703: credentials directory path
		if err != nil || !info.IsDir() || slices.ContainsFunc(c.dirs, func(d protected) bool { return os.SameFile(d.info, info) }) {
			continue
		}
		c.dirs = append(c.dirs, protected{dir: name, info: info})
		_ = filepath.WalkDir(root, func(p string, e fs.DirEntry, err error) error { // the walk function never fails
			if entries++; entries > maxProtectedEntries {
				return fs.SkipAll
			}
			if err != nil || e.IsDir() {
				return nil
			}
			info, err := os.Stat(p) //nolint:gosec // G703: credentials directory entry
			switch {
			case err != nil:
			case info.IsDir():
				queue = append(queue, p)
			case info.Mode().IsRegular():
				c.files = append(c.files, protected{dir: name, info: info})
			}
			return nil
		})
	}
}

// config copies the configuration file src to dest and queues its
// references. key names the variable that references src, empty for the
// files Git reads by default, which are skipped silently when missing.
func (c *gitConfigCopier) config(key, src, dest string) {
	if c.seen[[2]string{src, dest}] {
		return
	}
	// The limit applies before reading, so excess includes cost nothing.
	if c.count >= maxConfigFiles {
		if c.count == maxConfigFiles {
			c.warn("more than %d configuration files are included; %s and later includes are not copied to the VM", maxConfigFiles, src)
			c.count++
		}
		return
	}
	dest, data, ok := c.load(key, src, dest, false)
	if !ok {
		return
	}
	c.count++
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
				c.refs = append(c.refs, func() { c.config(display, p, "") })
			} else if e.value != "" && !strings.HasPrefix(e.value, "~") && !strings.HasPrefix(e.value, "%(") {
				// Git resolves a relative include against the including file.
				p, d := filepath.Join(filepath.Dir(src), e.value), path.Join(path.Dir(dest), filepath.ToSlash(e.value))
				c.refs = append(c.refs, func() { c.config(display, p, d) })
			}
		case gitFileKeys[name] != "":
			if p, ok := c.expand(e.value); ok {
				c.refs = append(c.refs, func() { c.file(gitFileKeys[name], p, "", gitPublicFiles[name]) })
			}
		}
	}
}

// file copies the regular file src to dest. key is as for config. public is
// the kind of public file src must be, nil for any file outside the
// credentials directories.
func (c *gitConfigCopier) file(key, src, dest string, public *publicFile) {
	dest, data, ok := c.load(key, src, dest, public != nil)
	if !ok {
		return
	}
	if public != nil {
		if data, ok = public.parse(data); !ok {
			c.warn("%s %s %s", key, src, public.problem)
			return
		}
	}
	c.out = append(c.out, snapshot{Mount: mountOf(src, dest), data: data})
}

// load claims dest for src and returns dest and the content of src. An empty
// dest is derived from the location of src under the home directory. src is
// reported and skipped when it is outside the home directory, missing, not a
// regular file of at most maxConfigSize bytes, a Git credential store, in a
// credentials directory unless public, or another file than the one that
// already claimed dest. The home directory check applies to src as written;
// the other checks also apply to the file that symbolic links in src resolve
// to, and the content is read from that file.
func (c *gitConfigCopier) load(key, src, dest string, public bool) (string, []byte, bool) {
	visit := [2]string{src, dest}
	if c.seen[visit] {
		return "", nil, false
	}
	c.seen[visit] = true
	name := strings.TrimPrefix(key+" "+src, " ")
	// A relative include carries its dest but must still stay in the home
	// directory or the directory Git reads its default files from.
	if !within(src, c.home) && (dest == "" || !within(src, c.xdg)) {
		c.warn("%s is outside the home directory; it is not copied to the VM", name)
		return "", nil, false
	}
	if dest == "" {
		rel, _ := filepath.Rel(c.home, src) // within checked that src is in the home directory
		dest = guestHome + "/" + filepath.ToSlash(rel)
	}
	if dest != guestHome && !strings.HasPrefix(dest, guestHome+"/") {
		c.warn("%s is outside the home directory in the VM; it is not copied", name)
		return "", nil, false
	}
	resolved, err := filepath.EvalSymlinks(src)
	var info fs.FileInfo
	if err == nil {
		info, err = os.Lstat(resolved) //nolint:gosec // G703: path named by the Git configuration
	}
	if errors.Is(err, fs.ErrNotExist) {
		if key != "" {
			c.warn("%s does not exist; it is not copied to the VM", name)
		}
		return "", nil, false
	}
	if err != nil {
		c.warn("%s cannot be resolved (%v); it is not copied to the VM", name, err)
		return "", nil, false
	}
	if where, store := c.credential(src, resolved, info); where != "" && (store || !public) {
		c.warn("%s is %s; it is not copied to the VM", name, where)
		return "", nil, false
	}
	if prev, ok := c.dests[dest]; ok {
		if !os.SameFile(prev.info, info) {
			c.warn("%s is not copied to the VM: %s already takes its path there", name, prev.src)
		}
		return "", nil, false
	}
	data, ok := readSame(resolved, info)
	if !ok {
		c.warn("%s is not a regular file of at most %d bytes; it is not copied to the VM", name, maxConfigSize)
		return "", nil, false
	}
	c.dests[dest] = claim{src: src, info: info}
	return dest, data, true
}

// credential returns why the file src, which resolves to resolved and is
// described by info, must not be copied: it is a Git credential store,
// reported as store, it is or links to a file in a credentials directory, or
// src or resolved lies in one. Files and directories are compared by
// identity, so symbolic links, hard links and the letter case of names
// cannot hide them.
func (c *gitConfigCopier) credential(src, resolved string, info fs.FileInfo) (where string, store bool) {
	for _, s := range c.stores {
		if os.SameFile(s, info) {
			return "a Git credential store", true
		}
	}
	for _, f := range c.files {
		if os.SameFile(f.info, info) {
			return "in ~/" + f.dir, false
		}
	}
	for _, p := range []string{src, resolved} {
		for d := filepath.Dir(p); ; d = filepath.Dir(d) {
			if di, err := os.Stat(d); err == nil { //nolint:gosec // G703: Git configuration path
				for _, cd := range c.dirs {
					if os.SameFile(cd.info, di) {
						return "in ~/" + cd.dir, false
					}
				}
			}
			if d == filepath.Dir(d) {
				break
			}
		}
	}
	return "", false
}

// readSame reads the regular file path if it is the file info describes and
// holds at most maxConfigSize bytes. path is opened without following a
// symbolic link or blocking, and the opened file is compared with info, so
// a path swapped after the checks cannot supply other content.
func readSame(path string, info fs.FileInfo) ([]byte, bool) {
	f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NONBLOCK|syscall.O_NOFOLLOW, 0) //nolint:gosec // G304: path named by the Git configuration
	if err != nil {
		return nil, false
	}
	defer func() { _ = f.Close() }() // read-only
	opened, err := f.Stat()
	if err != nil || !opened.Mode().IsRegular() || !os.SameFile(info, opened) || opened.Size() > maxConfigSize {
		return nil, false
	}
	data, err := io.ReadAll(io.LimitReader(f, maxConfigSize+1))
	if err != nil || int64(len(data)) > maxConfigSize {
		return nil, false
	}
	return data, true
}

// publicKey returns the first SSH public key in data, serialized on its own,
// so nothing else in the file reaches the VM.
func publicKey(data []byte) ([]byte, bool) {
	key, _, _, _, err := ssh.ParseAuthorizedKey(data)
	if err != nil {
		return nil, false
	}
	return ssh.MarshalAuthorizedKey(key), true
}

// allowedSigners returns the lines of an SSH allowed signers file (see
// ssh-keygen(1)) rebuilt from their principals, options and serialized public
// keys, without comments. It reports false when the file has no signer or a
// line that is not blank, a comment or a signer with known options.
func allowedSigners(data []byte) ([]byte, bool) {
	var out []byte
	for line := range strings.Lines(string(data)) {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		end := strings.IndexAny(line, " \t")
		if strings.HasPrefix(line, `"`) {
			q := strings.IndexByte(line[1:], '"')
			if q < 0 {
				return nil, false
			}
			end = q + 2
		}
		if end <= 0 || end >= len(line) {
			return nil, false
		}
		key, _, options, rest, err := ssh.ParseAuthorizedKey([]byte(line[end:]))
		if err != nil || len(rest) != 0 {
			return nil, false
		}
		out = append(out, line[:end]...)
		sep := " "
		for _, o := range options {
			if o != "cert-authority" && !strings.HasPrefix(o, "namespaces=") &&
				!strings.HasPrefix(o, "valid-after=") && !strings.HasPrefix(o, "valid-before=") {
				return nil, false
			}
			out = append(out, sep+o...)
			sep = ","
		}
		out = append(out, ' ')
		out = append(out, ssh.MarshalAuthorizedKey(key)...)
	}
	return out, out != nil
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
