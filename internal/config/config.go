package config

import (
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/BurntSushi/toml"
)

const (
	defaultImageRepository = "ghcr.io/seznam/jailoc"
	defaultConfigContent   = `# jailoc configuration
# See: https://github.com/seznam/jailoc

# Access mode: "remote" (host opencode attach), "exec" (docker exec opencode), or "" (auto-detect)
# mode = ""

[image]
# repository = "ghcr.io/seznam/jailoc"  # default registry

[workspaces.default]
paths = []
# allowed_hosts = []
# allowed_networks = []
# build_context = ""
`
)

const (
	ModeRemote = "remote"
	ModeExec   = "exec"
)

var workspaceNameRe = regexp.MustCompile(`^[a-z0-9-]+$`)

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
	"/workspace",
}

type Config struct {
	Mode       string               `toml:"mode"`
	Image      ImageConfig          `toml:"image"`
	Workspaces map[string]Workspace `toml:"workspaces"`
}

type ImageConfig struct {
	Repository string `toml:"repository"`
}

type Workspace struct {
	Paths           []string `toml:"paths"`
	AllowedHosts    []string `toml:"allowed_hosts"`
	AllowedNetworks []string `toml:"allowed_networks"`
	BuildContext    string   `toml:"build_context"`
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

	if cfg.Image.Repository == "" {
		cfg.Image.Repository = defaultImageRepository
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

func Validate(cfg *Config) error {
	if cfg == nil {
		return fmt.Errorf("config is nil")
	}

	if cfg.Workspaces == nil {
		cfg.Workspaces = map[string]Workspace{}
	}

	if cfg.Image.Repository == "" {
		cfg.Image.Repository = defaultImageRepository
	}

	if cfg.Mode != "" && cfg.Mode != ModeRemote && cfg.Mode != ModeExec {
		return fmt.Errorf("invalid mode %q: must be %q, %q, or empty (auto-detect)", cfg.Mode, ModeRemote, ModeExec)
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

func WorkspaceDockerfilePath(workspace string) string {
	return ConfigDir() + workspace + ".Dockerfile"
}

func BaseDockerfileOverridePath() string {
	return ConfigDir() + "Dockerfile"
}

func AllowedHostsFileContent(workspace string, cfg *Config) string {
	if cfg == nil {
		return ""
	}

	ws, ok := cfg.Workspaces[workspace]
	if !ok || len(ws.AllowedHosts) == 0 {
		return ""
	}

	return strings.Join(ws.AllowedHosts, "\n") + "\n"
}

func AllowedNetworksFileContent(workspace string, cfg *Config) string {
	if cfg == nil {
		return ""
	}

	ws, ok := cfg.Workspaces[workspace]
	if !ok || len(ws.AllowedNetworks) == 0 {
		return ""
	}

	return strings.Join(ws.AllowedNetworks, "\n") + "\n"
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

	return nil
}
