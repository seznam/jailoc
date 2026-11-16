package guest

import (
	"path"
	"slices"
	"strings"
)

// cacheVar is a tool's environment variable that moves a pure download
// cache out of its default location below the home directory. The defaults
// sit on the root overlay, whose writes stay in memory and are lost on
// restart, so sessions point them into the persistent cache directory.
type cacheVar struct {
	name   string
	subdir string
	// aliases are other names the tool reads for the same setting.
	aliases []string
}

// cacheVars are the tool caches outside ~/.cache that only hold downloads
// the tool can fetch again. Tools that keep configuration or installed
// binaries next to their cache (Cargo, Gradle, Maven, rustup) are left out,
// and so is the pnpm store, which must share a filesystem with node_modules.
var cacheVars = []cacheVar{
	{name: "GOMODCACHE", subdir: "go-mod"},
	{name: "NPM_CONFIG_CACHE", subdir: "npm", aliases: []string{"npm_config_cache"}},
	{name: "BUN_INSTALL_CACHE_DIR", subdir: "bun"},
	{name: "NUGET_PACKAGES", subdir: "nuget/packages"},
	{name: "NUGET_HTTP_CACHE_PATH", subdir: "nuget/v3-cache"},
}

// cacheEnv returns the cacheVars entries below cacheDir that env does not
// set, so an image or configured value wins. It returns nil without a
// cacheDir.
func cacheEnv(cacheDir string, env []string) []string {
	if cacheDir == "" {
		return nil
	}
	set := make(map[string]bool, len(env))
	for _, kv := range env {
		name, _, _ := strings.Cut(kv, "=")
		set[name] = true
	}
	var out []string
	for _, v := range cacheVars {
		if set[v.name] || slices.ContainsFunc(v.aliases, func(n string) bool { return set[n] }) {
			continue
		}
		out = append(out, v.name+"="+path.Join(cacheDir, v.subdir))
	}
	return out
}
