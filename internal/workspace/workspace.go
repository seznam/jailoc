package workspace

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/seznam/jailoc/internal/config"
)

// BasePort is the internal port that opencode serve binds to inside the container.
// Host-side ports are assigned as BasePort + alphabetical workspace index.
const BasePort = 4096

type Resolved struct {
	Name            string
	Paths           []string
	Port            int
	AllowedHosts    []string
	AllowedNetworks []string
	BuildContext    string
	Dockerfile      string
	Image           string
	Env             []string
}

func Resolve(cfg *config.Config, name string) (*Resolved, error) {
	if cfg == nil {
		return nil, fmt.Errorf("workspace %q not found", name)
	}

	ws, ok := cfg.Workspaces[name]
	if !ok {
		return nil, fmt.Errorf("workspace %q not found", name)
	}

	paths := make([]string, 0, len(ws.Paths))
	for _, p := range ws.Paths {
		expanded, err := expandPath(p)
		if err != nil {
			return nil, err
		}
		abs, err := filepath.Abs(expanded)
		if err != nil {
			return nil, fmt.Errorf("resolve absolute path %q: %w", expanded, err)
		}
		paths = append(paths, abs)
	}

	buildContext := ""
	if ws.BuildContext != "" {
		expanded, err := expandPath(ws.BuildContext)
		if err != nil {
			return nil, err
		}
		abs, err := filepath.Abs(expanded)
		if err != nil {
			return nil, fmt.Errorf("resolve absolute path %q: %w", expanded, err)
		}
		buildContext = abs
	}

	mergedEnv := make([]string, 0, len(cfg.Defaults.Env)+len(ws.Env))
	mergedEnv = append(mergedEnv, cfg.Defaults.Env...)

	allEnvFiles := make([]string, 0, len(cfg.Defaults.EnvFile)+len(ws.EnvFile))
	seenFiles := make(map[string]bool, len(cfg.Defaults.EnvFile)+len(ws.EnvFile))
	for _, f := range append(cfg.Defaults.EnvFile, ws.EnvFile...) {
		if !seenFiles[f] {
			seenFiles[f] = true
			allEnvFiles = append(allEnvFiles, f)
		}
	}
	for _, envFile := range allEnvFiles {
		entries, err := config.ParseEnvFile(envFile)
		if err != nil {
			return nil, fmt.Errorf("resolving env for workspace %s: %w", name, err)
		}
		mergedEnv = append(mergedEnv, entries...)
	}

	mergedEnv = append(mergedEnv, ws.Env...)
	mergedEnv = dedupEnvByKeyLastWins(mergedEnv)

	return &Resolved{
		Name:            name,
		Paths:           paths,
		Port:            PortForWorkspace(cfg, name),
		AllowedHosts:    ws.AllowedHosts,
		AllowedNetworks: ws.AllowedNetworks,
		BuildContext:    buildContext,
		Dockerfile:      ws.Dockerfile,
		Image:           ws.Image,
		Env:             mergedEnv,
	}, nil
}

func dedupEnvByKeyLastWins(entries []string) []string {
	if len(entries) == 0 {
		return nil
	}

	orderedKeys := make([]string, 0, len(entries))
	seenKeys := make(map[string]bool, len(entries))
	latestByKey := make(map[string]string, len(entries))

	for _, entry := range entries {
		key, _, _ := strings.Cut(entry, "=")
		if !seenKeys[key] {
			orderedKeys = append(orderedKeys, key)
			seenKeys[key] = true
		}
		latestByKey[key] = entry
	}

	result := make([]string, 0, len(orderedKeys))
	for _, key := range orderedKeys {
		result = append(result, latestByKey[key])
	}

	return result
}

func ResolveFromCWD(cfg *config.Config, cwd string) (*Resolved, string, error) {
	for _, name := range workspaceNames(cfg) {
		resolved, err := Resolve(cfg, name)
		if err != nil {
			return nil, "", err
		}
		for _, p := range resolved.Paths {
			if pathMatchesCWD(p, cwd) {
				return resolved, p, nil
			}
		}
	}

	return nil, "", fmt.Errorf("no workspace matches current directory %q", cwd)
}

func PortForWorkspace(cfg *config.Config, name string) int {
	names := workspaceNames(cfg)
	for i, wsName := range names {
		if wsName == name {
			return BasePort + i
		}
	}
	return -1
}

func MatchesCWD(ws *Resolved, cwd string) bool {
	if ws == nil {
		return false
	}
	for _, p := range ws.Paths {
		if pathMatchesCWD(p, cwd) {
			return true
		}
	}
	return false
}

func pathMatchesCWD(base, cwd string) bool {
	if base == "" {
		return false
	}
	sep := string(filepath.Separator)
	p := base
	if !strings.HasSuffix(p, sep) {
		p += sep
	}
	return strings.HasPrefix(cwd+sep, p) || cwd == strings.TrimSuffix(p, sep)
}

func workspaceNames(cfg *config.Config) []string {
	if cfg == nil {
		return nil
	}
	names := make([]string, 0, len(cfg.Workspaces))
	for name := range cfg.Workspaces {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

func expandPath(path string) (string, error) {
	if !strings.HasPrefix(path, "~") {
		return path, nil
	}

	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("expand home dir: %w", err)
	}

	return filepath.Join(home, path[1:]), nil
}
