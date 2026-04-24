package config

import (
	"fmt"
	"math"
	"net"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/BurntSushi/toml"
	"github.com/fatih/color"
)

const (
	defaultConfigContent = `# jailoc configuration
# See: https://github.com/seznam/jailoc

# Access mode: "remote" (host opencode attach), "exec" (docker exec opencode), or "" (auto-detect)
# mode = ""
# password_mode = "auto"

[base]
# dockerfile = ""

[defaults]
# image = ""
# env = ["KEY=VALUE"]
# env_file = ["/path/to/.env"]
# mounts = ["~/.config/opencode:/home/agent/.config/opencode:ro"]
# allowed_hosts = ["example.com"]
# allowed_networks = ["10.0.0.0/8"]
# ssh_auth_sock = false
# git_config = true
# expose_port = true
# enable_docker = true
# cpu = 2.0
# memory = "4g"

[workspaces.default]
paths = []
# image = ""
# allowed_hosts = []
# allowed_networks = []
# env = ["KEY=VALUE"]
# env_file = ["/path/to/.env"]
# mounts = ["~/.config/opencode:/home/agent/.config/opencode:ro"]
# build_context = ""
# dockerfile = ""
# ssh_auth_sock = false
# git_config = true
# expose_port = true
# enable_docker = true
# cpu = 2.0
# memory = "4g"
`
)

const (
	ModeRemote = "remote"
	ModeExec   = "exec"
)

var workspaceNameRe = regexp.MustCompile(`^[a-z0-9-]+$`)

var validMemory = regexp.MustCompile(`^[1-9][0-9]*[kmgKMG]?$`)

var reservedEnvKeys = map[string]bool{
	"OPENCODE_LOG":             true,
	"OPENCODE_SERVER_PASSWORD": true,
	"DOCKER_HOST":              true,
	"DOCKER_TLS_CERTDIR":       true,
	"DOCKER_CERT_PATH":         true,
	"DOCKER_TLS_VERIFY":        true,
	"SSH_AUTH_SOCK":            true,
	"JAILOC":                   true,
	"JAILOC_WORKSPACE":         true,
}

var forbiddenMountPrefixes = []string{
	"/home/agent",
	"/usr",
	"/etc",
	"/var",
	"/bin",
	"/sbin",
	"/lib",
	"/lib64",
	"/opt",
	"/root",
	"/proc",
	"/sys",
	"/dev",
	"/run",
	"/tmp",
	"/certs",
}

// forbiddenMountContainerPrefixes lists container-side mount destinations that must not be overridden.
// Unlike forbiddenMountPrefixes (used for workspace paths), this does NOT include /home/agent,
// because mounts intentionally target /home/agent/... subpaths (e.g. the default OC mounts).
var forbiddenMountContainerPrefixes = []string{
	"/",
	"/usr",
	"/etc",
	"/var",
	"/bin",
	"/sbin",
	"/lib",
	"/lib64",
	"/opt",
	"/root",
	"/proc",
	"/sys",
	"/dev",
	"/run",
	"/tmp",
	"/certs",
}

var forbiddenMountHostPaths = []string{
	"/",
	"/boot",
	"/dev",
	"/etc",
	"/private",
	"/proc",
	"/sys",
	"/run",
	"/var",
	"~/.ssh",
	"~/.gnupg",
	"~/.aws",
}

var DefaultMounts = []string{
	"~/.config/opencode:/home/agent/.config/opencode:ro",
	"~/.opencode:/home/agent/.opencode:ro",
	"~/.claude/transcripts:/home/agent/.claude/transcripts:rw",
	"~/.agents:/home/agent/.agents:ro",
}

type Mount struct {
	Host      string
	Container string
	Mode      string
}

type Config struct {
	Mode         string               `toml:"mode"`
	PasswordMode string               `toml:"password_mode"`
	Base         BaseConfig           `toml:"base"`
	Defaults     Defaults             `toml:"defaults"`
	Workspaces   map[string]Workspace `toml:"workspaces"`
}

type BaseConfig struct {
	Dockerfile string `toml:"dockerfile"`
}

type Defaults struct {
	Env             []string `toml:"env"`
	EnvFile         []string `toml:"env_file"`
	Mounts          []string `toml:"mounts"`
	AllowedHosts    []string `toml:"allowed_hosts"`
	AllowedNetworks []string `toml:"allowed_networks"`
	Image           string   `toml:"image"`
	SSHAuthSock     bool     `toml:"ssh_auth_sock"`
	GitConfig       *bool    `toml:"git_config"`
	CPU             *float64 `toml:"cpu"`
	Memory          *string  `toml:"memory"`
	ExposePort      *bool    `toml:"expose_port"`
	EnableDocker    *bool    `toml:"enable_docker"`
}

type Workspace struct {
	Paths           []string `toml:"paths"`
	Mounts          []string `toml:"mounts"`
	AllowedHosts    []string `toml:"allowed_hosts"`
	AllowedNetworks []string `toml:"allowed_networks"`
	Env             []string `toml:"env"`
	EnvFile         []string `toml:"env_file"`
	BuildContext    string   `toml:"build_context"`
	Dockerfile      string   `toml:"dockerfile"`
	Image           string   `toml:"image"`
	SSHAuthSock     *bool    `toml:"ssh_auth_sock"`
	GitConfig       *bool    `toml:"git_config"`
	CPU             *float64 `toml:"cpu"`
	Memory          *string  `toml:"memory"`
	ExposePort      *bool    `toml:"expose_port"`
	EnableDocker    *bool    `toml:"enable_docker"`
}

func ConfigDir() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return "~/.config/jailoc/"
	}

	return home + "/.config/jailoc/"
}

func ConfigPath() string {
	return ConfigDir() + "config.toml"
}

func Load() (*Config, error) {
	return loadFrom(ConfigPath(), true)
}

func LoadFrom(path string) (*Config, error) {
	return loadFrom(path, false)
}

func loadFrom(path string, defaultPath bool) (*Config, error) {
	if _, err := os.Stat(path); err != nil {
		if os.IsNotExist(err) {
			if defaultPath {
				if err := CreateDefault(); err != nil {
					return nil, fmt.Errorf("create default config: %w", err)
				}
			} else {
				if err := createDefaultAt(path); err != nil {
					return nil, fmt.Errorf("create default config at %q: %w", path, err)
				}
			}

			cfg, err := decode(path)
			if err != nil {
				return nil, fmt.Errorf("decode auto-created config at %q: %w", path, err)
			}

			return cfg, nil
		}

		return nil, fmt.Errorf("stat config %q: %w", path, err)
	}

	cfg, err := decode(path)
	if err != nil {
		return nil, fmt.Errorf("decode config at %q: %w", path, err)
	}

	if err := Validate(cfg); err != nil {
		return nil, fmt.Errorf("validate config at %q: %w", path, err)
	}

	return cfg, nil
}

func decode(path string) (*Config, error) {
	var cfg Config
	if _, err := toml.DecodeFile(path, &cfg); err != nil {
		return nil, fmt.Errorf("parse TOML %q: %w", path, err)
	}

	if cfg.Workspaces == nil {
		cfg.Workspaces = map[string]Workspace{}
	}

	return &cfg, nil
}

func CreateDefault() error {
	return createDefaultAt(ConfigPath())
}

func createDefaultAt(path string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		return fmt.Errorf("create config directory for %q: %w", path, err)
	}

	if err := os.WriteFile(path, []byte(defaultConfigContent), 0o600); err != nil {
		return fmt.Errorf("write default config to %q: %w", path, err)
	}

	return nil
}

func validateDockerfileSource(value, fieldName string) error {
	// Empty string is allowed (optional field)
	if value == "" {
		return nil
	}

	// Absolute local paths starting with / are allowed
	if strings.HasPrefix(value, "/") {
		return nil
	}

	// Tilde paths (home directory) are allowed
	if strings.HasPrefix(value, "~") {
		return nil
	}

	// HTTP and HTTPS URLs are allowed
	if strings.HasPrefix(value, "http://") || strings.HasPrefix(value, "https://") {
		u, err := url.Parse(value)
		if err != nil || u.Host == "" {
			scheme := ""
			if err == nil {
				scheme = u.Scheme
			}
			return fmt.Errorf("%s: invalid URL %q, scheme must be http or https (got %q)", fieldName, value, scheme)
		}
		return nil
	}

	// Anything else is invalid
	return fmt.Errorf("%s: must be an absolute path (/..., ~/...) or HTTP(S) URL, got %q", fieldName, value)
}

func validateEnvEntries(entries []string, context string) error {
	for _, entry := range entries {
		key, _, hasEq := strings.Cut(entry, "=")
		if !hasEq {
			return fmt.Errorf("%s: invalid env entry %q: must be in KEY=VALUE format", context, entry)
		}
		if key == "" {
			return fmt.Errorf("%s: invalid env entry %q: key must not be empty", context, entry)
		}
		if reservedEnvKeys[key] {
			return fmt.Errorf("%s: env key %q is reserved and cannot be overridden", context, key)
		}
	}
	return nil
}

func validateEnvFiles(paths []string, context string) error {
	for _, p := range paths {
		if !strings.HasPrefix(p, "/") {
			return fmt.Errorf("%s: env_file path %q must be absolute (start with /)", context, p)
		}
		if _, err := os.Stat(p); err != nil {
			if os.IsNotExist(err) {
				return fmt.Errorf("%s: env_file %q does not exist", context, p)
			}
			return fmt.Errorf("%s: env_file %q: %w", context, p, err)
		}
		entries, err := ParseEnvFile(p)
		if err != nil {
			return fmt.Errorf("%s: %w", context, err)
		}
		if err := validateEnvEntries(entries, fmt.Sprintf("%s: env_file %q", context, p)); err != nil {
			return err
		}
	}
	return nil
}

func ParseMount(spec string) (Mount, error) {
	parts := strings.Split(spec, ":")
	if len(parts) < 2 || len(parts) > 3 {
		return Mount{}, fmt.Errorf("invalid mount spec %q: expected host:container[:mode]", spec)
	}

	host := parts[0]
	container := parts[1]
	mode := "rw"
	if len(parts) == 3 {
		mode = parts[2]
	}

	if mode != "ro" && mode != "rw" {
		return Mount{}, fmt.Errorf("invalid mount spec %q: mode must be ro or rw", spec)
	}

	if host != "" {
		expandedHost, err := ExpandPath(host)
		if err != nil {
			return Mount{}, fmt.Errorf("expand mount host path %q: %w", host, err)
		}
		if !strings.HasPrefix(expandedHost, "/") {
			return Mount{}, fmt.Errorf("invalid mount spec %q: host path %q must be absolute (start with / or ~)", spec, host)
		}
	}

	expandedContainer, err := ExpandPath(container)
	if err != nil {
		return Mount{}, fmt.Errorf("expand mount container path %q: %w", container, err)
	}
	if !strings.HasPrefix(expandedContainer, "/") {
		return Mount{}, fmt.Errorf("invalid mount spec %q: container path %q must be absolute (start with /)", spec, container)
	}

	return Mount{Host: host, Container: container, Mode: mode}, nil
}

func validateMountHostPath(host string, context string) error {
	if host == "" {
		return nil
	}

	expandedHost, err := ExpandPath(host)
	if err != nil {
		return fmt.Errorf("%s: expand mount host path %q: %w", context, host, err)
	}
	cleanHost := filepath.Clean(expandedHost)

	for _, forbidden := range forbiddenMountHostPaths {
		expandedForbidden, err := ExpandPath(forbidden)
		if err != nil {
			return fmt.Errorf("%s: expand forbidden mount host path %q: %w", context, forbidden, err)
		}

		cleanForbidden := filepath.Clean(expandedForbidden)
		forbiddenPrefix := cleanForbidden + string(os.PathSeparator)
		if cleanHost == cleanForbidden || strings.HasPrefix(cleanHost, forbiddenPrefix) {
			return fmt.Errorf("%s: mount host path %q is forbidden", context, cleanHost)
		}
	}

	return nil
}

func formatMount(m Mount) string {
	return fmt.Sprintf("%s:%s:%s", m.Host, m.Container, m.Mode)
}

func MergeMounts(layers ...[]string) ([]string, error) {
	if len(layers) == 0 {
		return nil, nil
	}

	orderedContainers := make([]string, 0)
	seenContainers := make(map[string]bool)
	byContainer := make(map[string]Mount)

	for _, layer := range layers {
		for _, spec := range layer {
			m, err := ParseMount(spec)
			if err != nil {
				return nil, fmt.Errorf("parse mount spec %q: %w", spec, err)
			}

			expandedHost := m.Host
			if m.Host != "" {
				expanded, err := ExpandPath(m.Host)
				if err != nil {
					return nil, fmt.Errorf("expand mount host path %q: %w", m.Host, err)
				}
				expandedHost = expanded
			}

			if !seenContainers[m.Container] {
				seenContainers[m.Container] = true
				orderedContainers = append(orderedContainers, m.Container)
			}

			if m.Host == "" {
				delete(byContainer, m.Container)
				continue
			}

			byContainer[m.Container] = Mount{
				Host:      expandedHost,
				Container: m.Container,
				Mode:      m.Mode,
			}
		}
	}

	merged := make([]string, 0, len(byContainer))
	for _, container := range orderedContainers {
		m, ok := byContainer[container]
		if !ok {
			continue
		}
		merged = append(merged, formatMount(m))
	}

	return merged, nil
}

func Validate(cfg *Config) error {
	if cfg == nil {
		return fmt.Errorf("config is nil")
	}

	if cfg.Workspaces == nil {
		cfg.Workspaces = map[string]Workspace{}
	}

	if cfg.Mode != "" && cfg.Mode != ModeRemote && cfg.Mode != ModeExec {
		return fmt.Errorf("invalid mode %q: must be %q, %q, or empty (auto-detect)", cfg.Mode, ModeRemote, ModeExec)
	}
	validPasswordModes := map[string]bool{"": true, "auto": true, "env": true, "keyring": true, "file": true}
	if !validPasswordModes[cfg.PasswordMode] {
		return fmt.Errorf("invalid password_mode %q: must be \"auto\", \"env\", \"keyring\", \"file\", or empty (auto)", cfg.PasswordMode)
	}

	if err := validateDockerfileSource(cfg.Base.Dockerfile, "base dockerfile"); err != nil {
		return err
	}

	if strings.HasPrefix(cfg.Base.Dockerfile, "~") {
		expanded, err := ExpandPath(cfg.Base.Dockerfile)
		if err != nil {
			return fmt.Errorf("expand base dockerfile %q: %w", cfg.Base.Dockerfile, err)
		}
		cfg.Base.Dockerfile = expanded
	}

	if err := validateEnvEntries(cfg.Defaults.Env, "defaults"); err != nil {
		return err
	}

	for i, p := range cfg.Defaults.EnvFile {
		expanded, err := ExpandPath(p)
		if err != nil {
			return fmt.Errorf("defaults: expand env_file path %q: %w", p, err)
		}
		cfg.Defaults.EnvFile[i] = expanded
	}

	if err := validateEnvFiles(cfg.Defaults.EnvFile, "defaults"); err != nil {
		return err
	}

	for i, spec := range cfg.Defaults.Mounts {
		m, err := ParseMount(spec)
		if err != nil {
			return fmt.Errorf("defaults: %w", err)
		}

		expandedHost, err := ExpandPath(m.Host)
		if err != nil {
			return fmt.Errorf("defaults: expand mount host path %q: %w", m.Host, err)
		}
		expandedContainer, err := ExpandPath(m.Container)
		if err != nil {
			return fmt.Errorf("defaults: expand mount container path %q: %w", m.Container, err)
		}

		m.Host = expandedHost
		m.Container = filepath.Clean(expandedContainer)
		if !strings.HasPrefix(m.Container, "/") {
			return fmt.Errorf("defaults: mount container path %q must be absolute (start with /)", m.Container)
		}

		for _, prefix := range forbiddenMountContainerPrefixes {
			if m.Container == prefix || strings.HasPrefix(m.Container, prefix+"/") {
				return fmt.Errorf("defaults: mount container path %q is not allowed (conflicts with container-internal directory %q)", m.Container, prefix)
			}
		}

		if err := validateMountHostPath(m.Host, "defaults"); err != nil {
			return err
		}

		cfg.Defaults.Mounts[i] = formatMount(m)
	}

	if cfg.Defaults.CPU != nil && (*cfg.Defaults.CPU <= 0 || math.IsNaN(*cfg.Defaults.CPU) || math.IsInf(*cfg.Defaults.CPU, 0)) {
		return fmt.Errorf("defaults: cpu must be a finite number greater than 0")
	}
	if cfg.Defaults.Memory != nil && !validMemory.MatchString(*cfg.Defaults.Memory) {
		return fmt.Errorf("defaults: invalid memory format %q: must be a positive integer optionally followed by k, m, or g (e.g. \"4g\", \"512m\")", *cfg.Defaults.Memory)
	}

	names := make([]string, 0, len(cfg.Workspaces))
	for name := range cfg.Workspaces {
		names = append(names, name)
	}
	sort.Strings(names)

	pathOwners := map[string]string{}

	for _, name := range names {
		if !workspaceNameRe.MatchString(name) {
			return fmt.Errorf("workspace name %q is invalid: must match [a-z0-9-]+", name)
		}

		ws := cfg.Workspaces[name]
		if err := expandPaths(&ws); err != nil {
			return fmt.Errorf("workspace %q: expand paths: %w", name, err)
		}

		for _, p := range ws.Paths {
			if strings.TrimSpace(p) == "" {
				return fmt.Errorf("workspace %q has empty path value %q", name, p)
			}

			for _, prefix := range forbiddenMountPrefixes {
				if p == prefix || strings.HasPrefix(p, prefix+"/") {
					return fmt.Errorf("workspace %q: path %q conflicts with container-internal directory %q", name, p, prefix)
				}
			}

			if owner, ok := pathOwners[p]; ok && owner != name {
				_, _ = color.New(color.FgYellow).Fprintf(os.Stderr, "warning: path %q is configured in multiple workspaces: %q and %q\n", p, owner, name)
			} else {
				pathOwners[p] = name
			}
		}

		for _, spec := range ws.Mounts {
			m, err := ParseMount(spec)
			if err != nil {
				return fmt.Errorf("workspace %q: %w", name, err)
			}

			if err := validateMountHostPath(m.Host, fmt.Sprintf("workspace %q", name)); err != nil {
				return err
			}

			expandedContainer, err := ExpandPath(m.Container)
			if err != nil {
				return fmt.Errorf("workspace %q: expand mount container path %q: %w", name, m.Container, err)
			}
			cleanedContainer := filepath.Clean(expandedContainer)
			if !strings.HasPrefix(cleanedContainer, "/") {
				return fmt.Errorf("workspace %q: mount container path %q must be absolute (start with /)", name, cleanedContainer)
			}

			for _, prefix := range forbiddenMountContainerPrefixes {
				if cleanedContainer == prefix || strings.HasPrefix(cleanedContainer, prefix+"/") {
					return fmt.Errorf("workspace %q: mount container path %q is not allowed (conflicts with container-internal directory %q)", name, cleanedContainer, prefix)
				}
			}
		}

		for _, cidr := range ws.AllowedNetworks {
			if _, _, err := net.ParseCIDR(cidr); err != nil {
				return fmt.Errorf("workspace %q: invalid CIDR %q: %w", name, cidr, err)
			}
		}

		if err := validateDockerfileSource(ws.Dockerfile, fmt.Sprintf("workspace %q dockerfile", name)); err != nil {
			return err
		}

		if ws.Image != "" && strings.TrimSpace(ws.Image) == "" {
			return fmt.Errorf("workspace %q: \"image\" must not be empty or whitespace-only", name)
		}

		if ws.Image != "" && ws.Dockerfile != "" {
			return fmt.Errorf("workspace %q: cannot set both \"image\" and \"dockerfile\"", name)
		}
		if ws.Image != "" && ws.BuildContext != "" {
			return fmt.Errorf("workspace %q: cannot set both \"image\" and \"build_context\"", name)
		}

		wsContext := fmt.Sprintf("workspace %q", name)
		if err := validateEnvEntries(ws.Env, wsContext); err != nil {
			return err
		}
		if err := validateEnvFiles(ws.EnvFile, wsContext); err != nil {
			return err
		}

		if ws.CPU != nil && (*ws.CPU <= 0 || math.IsNaN(*ws.CPU) || math.IsInf(*ws.CPU, 0)) {
			return fmt.Errorf("workspace %q: cpu must be a finite number greater than 0", name)
		}
		if ws.Memory != nil && !validMemory.MatchString(*ws.Memory) {
			return fmt.Errorf("workspace %q: invalid memory format %q: must be a positive integer optionally followed by k, m, or g (e.g. \"4g\", \"512m\")", name, *ws.Memory)
		}

		cfg.Workspaces[name] = ws
	}

	return nil
}

var lookPath = exec.LookPath

// opencodeBinaries is the ordered list of binary names to search for on PATH.
var opencodeBinaries = []string{"opencode", "opencode-cli"}

// ResolveBinary returns the first opencode binary found on PATH.
// It checks "opencode" first, then "opencode-cli" as a fallback.
// Returns ("", error) if neither is found.
func ResolveBinary() (string, error) {
	for _, name := range opencodeBinaries {
		if path, err := lookPath(name); err == nil {
			return path, nil
		}
	}
	return "", fmt.Errorf("neither opencode nor opencode-cli found on PATH")
}

// ResolveMode returns the effective access mode.
// If configured is non-empty, it is returned directly.
// Otherwise auto-detect: returns ModeRemote if opencode or opencode-cli is on PATH, ModeExec otherwise.
func ResolveMode(configured string) string {
	if configured != "" {
		return configured
	}
	if _, err := ResolveBinary(); err == nil {
		return ModeRemote
	}
	return ModeExec
}

func AddPath(workspace, path string) error {
	configPath := ConfigPath()
	cfg, err := decode(configPath)
	if err != nil {
		return fmt.Errorf("load config for AddPath: %w", err)
	}
	if cfg.Workspaces == nil {
		cfg.Workspaces = map[string]Workspace{}
	}

	ws, ok := cfg.Workspaces[workspace]
	if !ok {
		return fmt.Errorf("workspace %q does not exist", workspace)
	}

	ws.Paths = append(ws.Paths, path)
	cfg.Workspaces[workspace] = ws

	f, err := os.Create(configPath) //nolint:gosec // configPath is derived from ConfigPath(), not user input
	if err != nil {
		return fmt.Errorf("open config for write: %w", err)
	}
	defer func() { _ = f.Close() }()

	if err := toml.NewEncoder(f).Encode(cfg); err != nil {
		return fmt.Errorf("encode updated config: %w", err)
	}

	return nil
}

func WriteAllowedFiles(workspace string, cfg *Config) error {
	if cfg == nil {
		return nil
	}
	if _, ok := cfg.Workspaces[workspace]; !ok {
		return nil
	}

	dir := filepath.Join(ConfigDir(), "workspaces", workspace)
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return fmt.Errorf("create workspace config directory %q: %w", dir, err)
	}

	hostsPath := filepath.Join(dir, "allowed-hosts")
	if content := AllowedHostsFileContent(workspace, cfg); content != "" {
		if err := os.WriteFile(hostsPath, []byte(content), 0o600); err != nil {
			return fmt.Errorf("write allowed-hosts file: %w", err)
		}
	} else {
		if err := os.Remove(hostsPath); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("remove stale allowed-hosts file: %w", err)
		}
	}

	networksPath := filepath.Join(dir, "allowed-networks")
	if content := AllowedNetworksFileContent(workspace, cfg); content != "" {
		if err := os.WriteFile(networksPath, []byte(content), 0o600); err != nil {
			return fmt.Errorf("write allowed-networks file: %w", err)
		}
	} else {
		if err := os.Remove(networksPath); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("remove stale allowed-networks file: %w", err)
		}
	}

	return nil
}

// mergeDedup combines two string slices into one, removing duplicates.
// Order is preserved: items from `a` appear first, followed by items from `b` that weren't in `a`.
func mergeDedup(a, b []string) []string {
	if len(a) == 0 {
		return b
	}
	if len(b) == 0 {
		return a
	}

	seen := make(map[string]bool)
	result := make([]string, 0, len(a)+len(b))

	for _, item := range a {
		if !seen[item] {
			seen[item] = true
			result = append(result, item)
		}
	}

	for _, item := range b {
		if !seen[item] {
			seen[item] = true
			result = append(result, item)
		}
	}

	return result
}

func AllowedHostsFileContent(workspace string, cfg *Config) string {
	if cfg == nil {
		return ""
	}

	ws, ok := cfg.Workspaces[workspace]
	if !ok {
		return ""
	}

	merged := mergeDedup(cfg.Defaults.AllowedHosts, ws.AllowedHosts)
	if len(merged) == 0 {
		return ""
	}

	return strings.Join(merged, "\n") + "\n"
}

func AllowedNetworksFileContent(workspace string, cfg *Config) string {
	if cfg == nil {
		return ""
	}

	ws, ok := cfg.Workspaces[workspace]
	if !ok {
		return ""
	}

	merged := mergeDedup(cfg.Defaults.AllowedNetworks, ws.AllowedNetworks)
	if len(merged) == 0 {
		return ""
	}

	return strings.Join(merged, "\n") + "\n"
}

func ExpandPath(path string) (string, error) {
	if !strings.HasPrefix(path, "~") {
		return path, nil
	}

	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolve user home: %w", err)
	}

	return strings.Replace(path, "~", home, 1), nil
}

func expandPaths(ws *Workspace) error {
	for i, p := range ws.Paths {
		expanded, err := ExpandPath(p)
		if err != nil {
			return fmt.Errorf("expand path %q: %w", p, err)
		}
		ws.Paths[i] = expanded
	}

	if ws.BuildContext != "" {
		expanded, err := ExpandPath(ws.BuildContext)
		if err != nil {
			return fmt.Errorf("expand build context %q: %w", ws.BuildContext, err)
		}
		ws.BuildContext = expanded
	}

	if ws.Dockerfile != "" && strings.HasPrefix(ws.Dockerfile, "~") {
		expanded, err := ExpandPath(ws.Dockerfile)
		if err != nil {
			return fmt.Errorf("expand dockerfile path %q: %w", ws.Dockerfile, err)
		}
		ws.Dockerfile = expanded
	}

	for i, p := range ws.EnvFile {
		expanded, err := ExpandPath(p)
		if err != nil {
			return fmt.Errorf("expand env_file path %q: %w", p, err)
		}
		ws.EnvFile[i] = expanded
	}

	for i, spec := range ws.Mounts {
		m, err := ParseMount(spec)
		if err != nil {
			return fmt.Errorf("parse mount %q: %w", spec, err)
		}

		expandedHost, err := ExpandPath(m.Host)
		if err != nil {
			return fmt.Errorf("expand mount host path %q: %w", m.Host, err)
		}
		expandedContainer, err := ExpandPath(m.Container)
		if err != nil {
			return fmt.Errorf("expand mount container path %q: %w", m.Container, err)
		}

		m.Host = expandedHost
		m.Container = filepath.Clean(expandedContainer)
		ws.Mounts[i] = formatMount(m)
	}

	return nil
}
