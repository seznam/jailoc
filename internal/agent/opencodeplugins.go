package agent

import (
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
)

// openCode1Installs returns the npm plugins that OpenCode 1 installs before
// its TUI starts: the registry plugins of its configuration that are not in
// its package cache yet. env is the agent's environment and dir its working
// directory.
//
// It follows the configuration sources and the package cache of OpenCode
// 1.19.5: the global config.json, opencode.json and opencode.jsonc,
// OPENCODE_CONFIG, opencode.json(c) and .opencode directories from dir up
// to the git root, ~/.opencode, OPENCODE_CONFIG_DIR and
// OPENCODE_CONFIG_CONTENT. A plugin list in a later global file replaces the
// lists before it; the other sources add to the list, and later sources
// override plugins of the same name. Unreadable, invalid or special files are
// skipped: the result is a hint, never an error.
func openCode1Installs(env []string, dir string) []string {
	vars := make(map[string]string, len(env))
	for _, kv := range env {
		if k, v, ok := strings.Cut(kv, "="); ok {
			vars[k] = v
		}
	}
	home := vars["HOME"]
	configHome := xdgDir(vars["XDG_CONFIG_HOME"], home, ".config")
	cacheHome := xdgDir(vars["XDG_CACHE_HOME"], home, ".cache")
	if cacheHome == "" {
		return nil
	}

	var specs []string
	addFile := func(path string) {
		if b, ok := readConfig(path); ok {
			list, _ := configPlugins(b)
			specs = append(specs, list...)
		}
	}
	global := ""
	if configHome != "" {
		global = filepath.Join(configHome, "opencode")
		for _, name := range []string{"config.json", "opencode.json", "opencode.jsonc"} {
			b, ok := readConfig(filepath.Join(global, name))
			if !ok {
				continue
			}
			if list, ok := configPlugins(b); ok {
				specs = list
			}
		}
	}
	// OpenCode runs in dir, so relative paths in its environment start there.
	resolve := func(p string) string {
		if p != "" && !filepath.IsAbs(p) {
			return filepath.Join(dir, p)
		}
		return p
	}
	if f := resolve(vars["OPENCODE_CONFIG"]); f != "" {
		addFile(f)
	}
	project := !envTrue(vars["OPENCODE_DISABLE_PROJECT_CONFIG"])
	up := projectDirs(dir)
	if project {
		for _, d := range slices.Backward(up) {
			addFile(filepath.Join(d, "opencode.json"))
			addFile(filepath.Join(d, "opencode.jsonc"))
		}
	}

	dirs := []string{global}
	if project {
		for _, d := range up {
			dirs = append(dirs, filepath.Join(d, ".opencode"))
		}
	}
	if home != "" {
		dirs = append(dirs, filepath.Join(home, ".opencode"))
	}
	configDir := resolve(vars["OPENCODE_CONFIG_DIR"])
	if configDir != "" {
		dirs = append(dirs, configDir)
	}
	seen := map[string]bool{}
	for _, d := range dirs {
		if d == "" || seen[d] {
			continue
		}
		seen[d] = true
		if strings.HasSuffix(d, ".opencode") || d == configDir {
			addFile(filepath.Join(d, "opencode.json"))
			addFile(filepath.Join(d, "opencode.jsonc"))
		}
	}
	if content := vars["OPENCODE_CONFIG_CONTENT"]; content != "" {
		list, _ := configPlugins([]byte(content))
		specs = append(specs, list...)
	}

	// The last plugin of a name wins.
	type plugin struct{ pkg, name string }
	var plugins []plugin
	for _, spec := range specs {
		pkg, name, ok := registryPlugin(spec)
		if !ok {
			continue
		}
		if i := slices.IndexFunc(plugins, func(p plugin) bool { return p.name == name }); i >= 0 {
			plugins = slices.Delete(plugins, i, i+1)
		}
		plugins = append(plugins, plugin{pkg, name})
	}
	var installs []string
	for _, p := range plugins {
		installed := filepath.Join(cacheHome, "opencode", "packages", p.pkg, "node_modules", p.name)
		if _, err := os.Stat(installed); err != nil {
			installs = append(installs, p.pkg)
		}
	}
	return installs
}

// xdgDir returns the XDG base directory set in value, or name in home.
func xdgDir(value, home, name string) string {
	if value != "" {
		return value
	}
	if home == "" {
		return ""
	}
	return filepath.Join(home, name)
}

// envTrue reports whether an OpenCode flag variable is set.
func envTrue(v string) bool {
	return strings.EqualFold(v, "true") || v == "1"
}

// projectDirs returns dir and its parents up to the git root, or up to the
// filesystem root outside a repository, nearest first.
func projectDirs(dir string) []string {
	if dir == "" {
		return nil
	}
	var dirs []string
	for d := filepath.Clean(dir); ; {
		dirs = append(dirs, d)
		if _, err := os.Stat(filepath.Join(d, ".git")); err == nil {
			return dirs
		}
		parent := filepath.Dir(d)
		if parent == d {
			return dirs
		}
		d = parent
	}
}

// configPlugins returns the plugin specifiers of an OpenCode configuration,
// strings or [specifier, options] pairs, and whether it sets the plugin
// field at all.
func configPlugins(jsonc []byte) (specs []string, ok bool) {
	var cfg struct {
		Plugin []json.RawMessage `json:"plugin"`
	}
	if err := json.Unmarshal(stripJSONC(jsonc), &cfg); err != nil {
		return nil, false
	}
	for _, raw := range cfg.Plugin {
		var spec string
		if json.Unmarshal(raw, &spec) != nil {
			var pair []json.RawMessage
			if json.Unmarshal(raw, &pair) != nil || len(pair) == 0 || json.Unmarshal(pair[0], &spec) != nil {
				continue
			}
		}
		specs = append(specs, spec)
	}
	return specs, cfg.Plugin != nil
}

// maxConfigSize bounds the configuration files read for the hint.
const maxConfigSize = 1 << 20

// readConfig reads a regular file of at most maxConfigSize bytes. It opens
// without blocking and skips anything else, so a FIFO or device in a
// configuration path cannot hold up the session.
func readConfig(path string) ([]byte, bool) {
	f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NONBLOCK, 0) //nolint:gosec // G304: OpenCode's own configuration files
	if err != nil {
		return nil, false
	}
	defer func() { _ = f.Close() }()
	fi, err := f.Stat()
	if err != nil || !fi.Mode().IsRegular() || fi.Size() > maxConfigSize {
		return nil, false
	}
	b, err := io.ReadAll(io.LimitReader(f, maxConfigSize))
	return b, err == nil
}

// registryPlugin returns the package OpenCode installs for an npm registry
// plugin (name, name@version, @scope/name, @scope/name@version), with
// @latest for a bare name, and the package name. Paths, URLs, git and alias
// specifiers return ok false.
func registryPlugin(spec string) (pkg, name string, ok bool) {
	invalid := func(r rune) bool { return r <= ' ' || r == 0x7f || strings.ContainsRune(":#\\", r) }
	if spec == "" || strings.ContainsFunc(spec, invalid) || strings.HasPrefix(spec, ".") || strings.HasPrefix(spec, "/") || strings.HasPrefix(spec, "~") {
		return "", "", false
	}
	scope, rest := "", spec
	if after, found := strings.CutPrefix(spec, "@"); found {
		s, r, slash := strings.Cut(after, "/")
		if !slash || s == "" {
			return "", "", false
		}
		scope, rest = "@"+s+"/", r
	}
	base, version, versioned := strings.Cut(rest, "@")
	if base == "" || strings.Contains(base, "/") || versioned && (version == "" || strings.Contains(version, "/")) {
		return "", "", false
	}
	name = scope + base
	if !versioned {
		return name + "@latest", name, true
	}
	return spec, name, true
}

// stripJSONC removes the comments and trailing commas of JSONC, leaving
// strings intact.
func stripJSONC(b []byte) []byte {
	out := make([]byte, 0, len(b))
	for i := 0; i < len(b); i++ {
		c := b[i]
		switch {
		case c == '"':
			j := i + 1
			for j < len(b) && b[j] != '"' {
				if b[j] == '\\' {
					j++
				}
				j++
			}
			j = min(j, len(b)-1)
			out = append(out, b[i:j+1]...)
			i = j
		case c == '/' && i+1 < len(b) && b[i+1] == '/':
			for i < len(b) && b[i] != '\n' {
				i++
			}
			i--
		case c == '/' && i+1 < len(b) && b[i+1] == '*':
			end := strings.Index(string(b[i+2:]), "*/")
			if end < 0 {
				return out
			}
			i += end + 3
		default:
			out = append(out, c)
		}
	}
	// Trailing commas: a comma followed by whitespace and a closing bracket.
	res := make([]byte, 0, len(out))
	inString := false
	for i := 0; i < len(out); i++ {
		c := out[i]
		switch {
		case inString:
			if c == '\\' && i+1 < len(out) {
				res = append(res, c, out[i+1])
				i++
				continue
			}
			inString = c != '"'
		case c == '"':
			inString = true
		case c == ',':
			j := i + 1
			for j < len(out) && strings.IndexByte(" \t\r\n", out[j]) >= 0 {
				j++
			}
			if j < len(out) && (out[j] == '}' || out[j] == ']') {
				continue
			}
		}
		res = append(res, c)
	}
	return res
}
