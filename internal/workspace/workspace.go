package workspace

import (
	"cmp"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"

	"github.com/seznam/jailoc/internal/agent"
	"github.com/seznam/jailoc/internal/config"
)

// ErrNoMatch is returned by ResolveFromCWD when no workspace has a path
// matching the given directory. Callers can use errors.Is to distinguish
// this from real configuration/resolution failures.
var ErrNoMatch = errors.New("no matching workspace")

// BasePort numbers the workspaces: PortForWorkspace returns BasePort plus the
// workspace's alphabetical index.
const BasePort = 4096

type Resolved struct {
	Name  string
	Paths []string
	// Agent is the harness the workspace runs.
	Agent agent.Harness
	// AgentInstall adds the built-in image layer that installs Agent.
	AgentInstall bool
	// AgentArgs are appended to every start of Agent.
	AgentArgs []string
	// AgentProfile is where Agent keeps its state: nil per workspace, see
	// config.Profile otherwise.
	AgentProfile    *config.Profile
	Mounts          []string
	Snapshots       []string // read-only snapshots (host:container:ro)
	Port            int
	AllowedHosts    []string
	AllowedNetworks []string
	BuildContext    string
	Dockerfile      string
	Image           string
	Env             []string
	SSHAuthSock     bool
	GitConfig       bool
	Secrets         []ResolvedSecret
	CPU             float64
	Memory          string
	EnableDocker    bool
	// IsolatedBuild builds the image of a backend "vm" workspace in its own
	// builder VM instead of the builder shared by all workspaces.
	IsolatedBuild bool
	// ProtectGit makes .git/hooks and .git/config of the repositories in
	// a backend "vm" workspace read-only in the VM and disables sudo there.
	ProtectGit bool
	// Ports are guest ports published on host 127.0.0.1 (backend "vm").
	Ports           []config.PortForward
	FilteredDNS     bool
	DNSUpstream     string
	DNSBlockedZones []string
	CABundle        config.CABundle
}

type SecretKind int

const (
	SecretKindEnv SecretKind = iota
	SecretKindFile
)

// ResolvedSecret is a secret after the defaults+workspace merge. Exactly one
// From* field is set.
type ResolvedSecret struct {
	Name        string
	Kind        SecretKind
	FromEnv     string
	FromFile    string
	FromCommand string
}

func Resolve(cfg *config.Config, name string) (*Resolved, error) {
	slog.Debug("resolving workspace", "name", name)

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

	h, settings, err := resolveAgent(cfg, ws)
	if err != nil {
		return nil, fmt.Errorf("workspace %s: %w", name, err)
	}
	mergedMounts, err := config.MergeMounts(h.DefaultMounts(), settings.Mounts, cfg.Defaults.Mounts, ws.Mounts)
	if err != nil {
		return nil, fmt.Errorf("merge mounts for workspace %s: %w", name, err)
	}

	mergedSecrets := mergeSecrets(cfg.Defaults.Secrets, ws.Secrets)

	r := &Resolved{
		Name:            name,
		Paths:           paths,
		Agent:           h,
		AgentInstall:    settings.Install == nil || *settings.Install,
		AgentArgs:       settings.Args,
		AgentProfile:    settings.Profile,
		Mounts:          mergedMounts,
		Port:            PortForWorkspace(cfg, name),
		AllowedHosts:    ws.AllowedHosts,
		AllowedNetworks: ws.AllowedNetworks,
		BuildContext:    buildContext,
		Dockerfile:      ws.Dockerfile,
		Image:           ws.Image,
		Env:             mergedEnv,
		Secrets:         mergedSecrets,
		SSHAuthSock:     boolWithOverride(cfg.Defaults.SSHAuthSock, ws.SSHAuthSock),
		GitConfig:       boolPtrWithDefault(cfg.Defaults.GitConfig, ws.GitConfig, true),
		CPU:             floatWithDefault(cfg.Defaults.CPU, ws.CPU, 2.0),
		Memory:          stringWithDefault(cfg.Defaults.Memory, ws.Memory, "4g"),
		EnableDocker:    boolPtrWithDefault(cfg.Defaults.EnableDocker, ws.EnableDocker, true),
		IsolatedBuild:   boolPtrWithDefault(cfg.Defaults.IsolatedBuild, ws.IsolatedBuild, false),
		ProtectGit:      boolPtrWithDefault(cfg.Defaults.ProtectGit, ws.ProtectGit, false),
		FilteredDNS:     boolPtrWithDefault(cfg.Defaults.FilteredDNS, ws.FilteredDNS, false),
		DNSUpstream:     ws.DNSUpstream,
		DNSBlockedZones: ws.DNSBlockedZones,
		CABundle:        caBundleWithDefault(cfg.Defaults.CABundle, ws.CABundle),
	}
	if r.DNSUpstream == "" {
		r.DNSUpstream = cfg.Defaults.DNSUpstream
	}
	if r.DNSBlockedZones == nil {
		r.DNSBlockedZones = cfg.Defaults.DNSBlockedZones
	}
	if r.ProtectGit {
		r.EnableDocker = false
	}
	r.Ports = config.MergePorts(cfg.Defaults.Ports, ws.Ports)
	r.AllowedHosts = mergeUnique(cfg.Defaults.AllowedHosts, ws.AllowedHosts)
	r.AllowedNetworks = mergeUnique(cfg.Defaults.AllowedNetworks, ws.AllowedNetworks)
	snapshots, err := config.MergeMounts(cfg.Defaults.Snapshots, ws.Snapshots)
	if err != nil {
		return nil, fmt.Errorf("merge snapshots for workspace %s: %w", name, err)
	}
	r.Snapshots = snapshots

	slog.Debug("workspace resolved", "name", name, "port", r.Port, "paths", len(paths))

	return r, nil
}

// resolveAgent returns the harness of ws, its own agent, the default one or
// agent.Default, and its settings. A custom agent is the built-in agent it
// is derived from under its own name. Each setting comes from the first
// agents table that sets it: the workspace's and the defaults' table of the
// agent, then those of the built-in agent a custom agent is derived from.
// The harness installs the version the settings select. Mounts are the
// tables' mounts in merge order: the built-in agent's defaults and
// workspace tables, then a custom agent's own. Profile is only taken from
// the agent's own tables.
func resolveAgent(cfg *config.Config, ws config.Workspace) (agent.Harness, config.AgentSettings, error) {
	name := cmp.Or(ws.Agent, cfg.Defaults.Agent, agent.Default)
	tables := []config.AgentSettings{ws.Agents[name], cfg.Defaults.Agents[name]}
	h, ok := agent.Lookup(name)
	if !ok {
		from := cmp.Or(tables[0].From, tables[1].From)
		if h, ok = agent.Lookup(from); !ok {
			return agent.Harness{}, config.AgentSettings{}, fmt.Errorf("unknown agent %q: must be one of %s or a custom agent defined under agents", name, strings.Join(agent.Names(), ", "))
		}
		h.Name = name
		tables = append(tables, ws.Agents[from], cfg.Defaults.Agents[from])
	}
	var settings config.AgentSettings
	for _, t := range tables {
		if settings.Install == nil {
			settings.Install = t.Install
		}
		if settings.Args == nil {
			settings.Args = t.Args
		}
		settings.Version = cmp.Or(settings.Version, t.Version)
	}
	for _, t := range slices.Backward(tables) {
		settings.Mounts = append(settings.Mounts, t.Mounts...)
	}
	settings.Profile = cmp.Or(tables[0].Profile, tables[1].Profile)
	h.Version = cmp.Or(settings.Version, h.Version)
	return h, settings, nil
}

// mergeUnique returns a followed by the entries of b not already present.
func mergeUnique(a, b []string) []string {
	out := make([]string, 0, len(a)+len(b))
	seen := make(map[string]bool, len(a)+len(b))
	for _, v := range append(append([]string{}, a...), b...) {
		if !seen[v] {
			seen[v] = true
			out = append(out, v)
		}
	}
	return out
}

// FlattenSecrets converts a single [<scope>.secrets] block into the ordering
// Resolve produces. Callers that display one scope in isolation (jailoc config)
// share that ordering, so the codebase has exactly one definition of secret
// order.
func FlattenSecrets(secrets config.Secrets) []ResolvedSecret {
	collected := make(map[string]ResolvedSecret, len(secrets.Env)+len(secrets.File))
	collectSecrets(collected, secrets)

	return sortedSecrets(collected)
}

func mergeSecrets(defaults, overrides config.Secrets) []ResolvedSecret {
	merged := make(map[string]ResolvedSecret, len(defaults.Env)+len(defaults.File)+len(overrides.Env)+len(overrides.File))
	collectSecrets(merged, defaults)
	collectSecrets(merged, overrides)

	return sortedSecrets(merged)
}

// collectSecrets folds one scope's secrets into dst, keyed by name. A later
// call overwrites an earlier one: that is how a workspace entry replaces the
// default of the same name, including when the two use different destination
// sub-tables. Within a single scope a name cannot appear in both sub-tables —
// config.validateSecretsBlock rejects that before Resolve runs.
func collectSecrets(dst map[string]ResolvedSecret, secrets config.Secrets) {
	for name, secret := range secrets.Env {
		dst[name] = ResolvedSecret{Name: name, Kind: SecretKindEnv, FromEnv: secret.FromEnv, FromFile: secret.FromFile, FromCommand: secret.FromCommand}
	}
	for name, secret := range secrets.File {
		dst[name] = ResolvedSecret{Name: name, Kind: SecretKindFile, FromEnv: secret.FromEnv, FromFile: secret.FromFile, FromCommand: secret.FromCommand}
	}
}

// sortedSecrets sorts by Name across both destination sub-tables, so callers
// can use Resolved.Secrets without re-sorting.
func sortedSecrets(secrets map[string]ResolvedSecret) []ResolvedSecret {
	if len(secrets) == 0 {
		return nil
	}

	names := make([]string, 0, len(secrets))
	for name := range secrets {
		names = append(names, name)
	}
	sort.Strings(names)

	resolved := make([]ResolvedSecret, 0, len(names))
	for _, name := range names {
		resolved = append(resolved, secrets[name])
	}

	return resolved
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
	// best are the workspaces whose matching path is the deepest.
	var best []string
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
			if err != nil || !pathMatchesCWD(p, cwd) {
				continue
			}
			switch segments := pathSegments(p); {
			case len(best) == 0 || segments > bestSegments:
				best, bestMatchedPath, bestSegments = []string{name}, p, segments
			case segments == bestSegments && !slices.Contains(best, name):
				best = append(best, name)
			}
		}
	}

	switch {
	case len(best) == 0:
		return nil, "", fmt.Errorf("%w: directory %q", ErrNoMatch, cwd)
	case len(best) > 1:
		return nil, "", fmt.Errorf("directory %s matches workspaces %s; choose one with -w", cwd, strings.Join(best, ", "))
	}

	resolved, err := Resolve(cfg, best[0])
	if err != nil {
		return nil, "", fmt.Errorf("resolve workspace %q: %w", best[0], err)
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

func caBundleWithDefault(defaultVal, override *config.CABundle) config.CABundle {
	if override != nil {
		return *override
	}
	if defaultVal != nil {
		return *defaultVal
	}
	return config.AutomaticCABundle()
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
