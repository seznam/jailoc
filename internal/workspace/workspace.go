package workspace

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/seznam/jailoc/internal/config"
)

// ErrNoMatch is returned by ResolveFromCWD when no workspace has a path
// matching the given directory. Callers can use errors.Is to distinguish
// this from real configuration/resolution failures.
var ErrNoMatch = errors.New("no matching workspace")

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
	SSHAuthSock     bool
	GitConfig       bool
	CPU             float64
	Memory          string
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
		SSHAuthSock:     boolWithOverride(cfg.Defaults.SSHAuthSock, ws.SSHAuthSock),
		GitConfig:       boolPtrWithDefault(cfg.Defaults.GitConfig, ws.GitConfig, true),
		CPU:             floatWithDefault(cfg.Defaults.CPU, ws.CPU, 2.0),
		Memory:          stringWithDefault(cfg.Defaults.Memory, ws.Memory, "4g"),
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
	var bestName string
	var bestMatchedPath string
	var bestSegments int

	for _, name := range workspaceNames(cfg) {
		ws := cfg.Workspaces[name]
		for _, raw := range ws.Paths {
			expanded, err := expandPath(raw)
			if err != nil {
				continue
			}
			p, err := filepath.Abs(expanded)
			if err != nil {
				continue
			}
			if pathMatchesCWD(p, cwd) && pathSegments(p) > bestSegments {
				bestName = name
				bestMatchedPath = p
				bestSegments = pathSegments(p)
			}
		}
	}

	if bestName == "" {
		return nil, "", fmt.Errorf("%w: directory %q", ErrNoMatch, cwd)
	}

	resolved, err := Resolve(cfg, bestName)
	if err != nil {
		return nil, "", fmt.Errorf("resolve workspace %q: %w", bestName, err)
	}

	return resolved, bestMatchedPath, nil
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

// pathSegments returns the number of non-empty segments in a filepath.
// For example, "/a/b/c" returns 3, "/a/bb" returns 2, "/" returns 0.
func pathSegments(p string) int {
	trimmed := strings.Trim(p, string(filepath.Separator))
	if trimmed == "" {
		return 0
	}
	return len(strings.Split(trimmed, string(filepath.Separator)))
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

func boolWithOverride(defaultVal bool, override *bool) bool {
	if override != nil {
		return *override
	}
	return defaultVal
}

func boolPtrWithDefault(defaultVal *bool, override *bool, fallback bool) bool {
	if override != nil {
		return *override
	}
	if defaultVal != nil {
		return *defaultVal
	}
	return fallback
}

func floatWithDefault(defaultVal *float64, override *float64, fallback float64) float64 {
	if override != nil {
		return *override
	}
	if defaultVal != nil {
		return *defaultVal
	}
	return fallback
}

func stringWithDefault(defaultVal *string, override *string, fallback string) string {
	if override != nil {
		return *override
	}
	if defaultVal != nil {
		return *defaultVal
	}
	return fallback
}
