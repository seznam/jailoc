package config

import (
	"fmt"
	"net"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/BurntSushi/toml"
)

const (
	defaultConfigContent = `# jailoc configuration
# See: https://github.com/seznam/jailoc

# Access mode: "remote" (host opencode attach), "exec" (docker exec opencode), or "" (auto-detect)
# mode = ""

[base]
# dockerfile = ""

[defaults]
# image = ""
# env = ["KEY=VALUE"]
# env_file = ["/path/to/.env"]
# allowed_hosts = ["example.com"]
# allowed_networks = ["10.0.0.0/8"]

[workspaces.default]
paths = []
# image = ""
# allowed_hosts = []
# allowed_networks = []
# env = ["KEY=VALUE"]
# env_file = ["/path/to/.env"]
# build_context = ""
# dockerfile = ""
`
)

const (
	ModeRemote = "remote"
	ModeExec   = "exec"
)

var workspaceNameRe = regexp.MustCompile(`^[a-z0-9-]+$`)

var reservedEnvKeys = map[string]bool{
	"OPENCODE_LOG":             true,
	"OPENCODE_SERVER_PASSWORD": true,
	"DOCKER_HOST":              true,
	"DOCKER_TLS_CERTDIR":       true,
	"DOCKER_CERT_PATH":         true,
	"DOCKER_TLS_VERIFY":        true,
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

type Config struct {
	Mode       string               `toml:"mode"`
	Base       BaseConfig           `toml:"base"`
	Defaults   Defaults             `toml:"defaults"`
	Workspaces map[string]Workspace `toml:"workspaces"`
}

type BaseConfig struct {
	Dockerfile string `toml:"dockerfile"`
}

type Defaults struct {
	Env             []string `toml:"env"`
	EnvFile         []string `toml:"env_file"`
	AllowedHosts    []string `toml:"allowed_hosts"`
	AllowedNetworks []string `toml:"allowed_networks"`
	Image           string   `toml:"image"`
}

type Workspace struct {
	Paths           []string `toml:"paths"`
	AllowedHosts    []string `toml:"allowed_hosts"`
	AllowedNetworks []string `toml:"allowed_networks"`
	Env             []string `toml:"env"`
	EnvFile         []string `toml:"env_file"`
	BuildContext    string   `toml:"build_context"`
	Dockerfile      string   `toml:"dockerfile"`
	Image           string   `toml:"image"`
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
		if !strings.HasPrefix(p, "/") && !strings.HasPrefix(p, "~") {
			return fmt.Errorf("%s: env_file path %q must be absolute (start with / or ~)", context, p)
		}
		if _, err := os.Stat(p); err != nil {
			return fmt.Errorf("%s: env_file %q does not exist", context, p)
		}
	}
	return nil
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
				fmt.Fprintf(os.Stderr, "warning: path %q is configured in multiple workspaces: %q and %q\n", p, owner, name)
			} else {
				pathOwners[p] = name
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

		if ws.Image != "" && ws.Dockerfile != "" {
			return fmt.Errorf("workspace %q: cannot set both \"image\" and \"dockerfile\"", name)
		}
		if ws.Image != "" && ws.BuildContext != "" {
			return fmt.Errorf("workspace %q: cannot set both \"image\" and \"build_context\"", name)
		}
		if strings.TrimSpace(ws.Image) == "" && ws.Image != "" {
			return fmt.Errorf("workspace %q: \"image\" must not be empty", name)
		}

		wsContext := fmt.Sprintf("workspace %q", name)
		if err := validateEnvEntries(ws.Env, wsContext); err != nil {
			return err
		}
		if err := validateEnvFiles(ws.EnvFile, wsContext); err != nil {
			return err
		}

		cfg.Workspaces[name] = ws
	}

	return nil
}

var lookPath = exec.LookPath

// ResolveMode returns the effective access mode.
// If configured is non-empty, it is returned directly.
// Otherwise auto-detect: returns ModeRemote if opencode is on PATH, ModeExec otherwise.
func ResolveMode(configured string) string {
	if configured != "" {
		return configured
	}
	if _, err := lookPath("opencode"); err == nil {
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
	dir := ConfigDir()
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return fmt.Errorf("create config directory %q: %w", dir, err)
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

	return nil
}
