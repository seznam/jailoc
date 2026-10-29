package config

import (
	"bytes"
	"cmp"
	"fmt"
	"log/slog"
	"maps"
	"math"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"

	"github.com/BurntSushi/toml"
	"github.com/compose-spec/compose-go/v2/format"
	"github.com/seznam/jailoc/internal/agent"
)

const (
	defaultConfigContent = `# jailoc configuration
# See: https://github.com/seznam/jailoc

# Keys that detach from an agent session, like tmux: Ctrl-b, then d.
# detach_keys = "ctrl-b,d"

[base]
# dockerfile = ""

[defaults]
# agent = "opencode"  # opencode, claude, copilot, omo or an agent defined under agents
# image = ""
# env = ["KEY=VALUE"]
# env_file = ["/path/to/.env"]
# mounts = ["~/.config/opencode:/home/agent/.config/opencode:rw"]
# allowed_hosts = ["example.com"]
# allowed_networks = ["10.0.0.0/8"]
# ssh_auth_sock = false
# git_config = true
# enable_docker = true
# filtered_dns = false
# dns_upstream = "10.20.30.53"
# dns_blocked_zones = ["corp.example.com"]
# ca_bundle = true  # true: auto-detect SSL_CERT_FILE/NIX_SSL_CERT_FILE; false: disabled; string: custom bundle path
# cpu = 2.0
# memory = "4g"

# [defaults.secrets.env.MY_TOKEN]
#   from_env = "HOST_ENV_VAR"   # exported in the VM as env var MY_TOKEN
# [defaults.secrets.file.MY_FILE]
#   from_file = "/path/to/secret"  # written to /run/secrets/MY_FILE
# [defaults.agents.claude]
#   install = true      # false: the image provides the agent binary
#   version = "2.1.312" # npm version installed instead of the pinned one
#   args = []           # appended to every agent start
#   mounts = [":/home/agent/.agents"]  # merged over the agent's built-in mounts; ":/path" removes one
#   profile = "work"    # state shared across workspaces, one running VM at a time; false: emptied on every start
# [defaults.agents.claude-opus]  # a custom agent: agent = "claude-opus"
#   from = "claude"     # built-in agent it is derived from
#   args = ["--model", "opus"]

[workspaces.default]
paths = []
# agent = "opencode"
# image = ""
# allowed_hosts = []
# allowed_networks = []
# env = ["KEY=VALUE"]
# env_file = ["/path/to/.env"]
# mounts = ["~/.config/opencode:/home/agent/.config/opencode:rw"]
# build_context = ""
# dockerfile = ""
# ssh_auth_sock = false
# git_config = true
# enable_docker = true
# filtered_dns = false
# dns_upstream = "10.20.30.53"
# dns_blocked_zones = ["corp.example.com"]
# ca_bundle = true  # omitted: inherit defaults; true/false/string: override
# cpu = 2.0
# memory = "4g"

# [workspaces.default.secrets.env.MY_TOKEN]
#   from_env = "HOST_ENV_VAR"   # exported in the VM as env var MY_TOKEN
# [workspaces.default.secrets.env.GH_TOKEN]
#   from_command = "gh auth token"  # run on the host; its output becomes GH_TOKEN
# [workspaces.default.secrets.file.MY_FILE]
#   from_file = "/path/to/secret"  # written to /run/secrets/MY_FILE
`
)

// Workspace backends. BackendVM boots the workspace image as a per-workspace
// virtual machine behind the jailoc network broker and runs the agent directly
// in it; it is the only backend. BackendDocker is the jailoc v1 Docker Compose
// backend: configurations naming it are rejected with a pointer to
// `jailoc migrate`.
const (
	BackendDocker = "docker"
	BackendVM     = "vm"
)

var workspaceNameRe = regexp.MustCompile(`^[a-z0-9-]+$`)

var validMemory = regexp.MustCompile(`^[1-9][0-9]*[kmgKMG]?$`)

// secretNameRe excludes dots so a secret name can never be confused with a path
// segment when it becomes /run/secrets/<name> in the guest.
var secretNameRe = regexp.MustCompile(`^[a-zA-Z0-9_-]+$`)

// envVarNameRe is the exact POSIX shell identifier grammar. It is stricter than
// validateEnvEntries (which only guards the KEY=VALUE shape) because env
// secret names are exported verbatim into the session environment in the VM.
var envVarNameRe = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// reservedEnvKeys are the session variables jailoc sets itself. The
// variables of the agent harnesses are reserved too, see reservedEnvKey.
var reservedEnvKeys = map[string]bool{
	"NODE_USE_SYSTEM_CA": true,
	"SSH_AUTH_SOCK":      true,
	"JAILOC":             true,
	"JAILOC_WORKSPACE":   true,
}

// reservedEnvKey reports whether jailoc or an agent harness sets key.
func reservedEnvKey(key string) bool {
	if reservedEnvKeys[key] {
		return true
	}
	for _, h := range agent.All() {
		if key == h.TUIConfigEnv {
			return true
		}
		for _, kv := range h.Env {
			if k, _, _ := strings.Cut(kv, "="); k == key {
				return true
			}
		}
	}
	return false
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

// AgentSettings is an [<scope>.agents.<name>] table: settings of the agent
// harness name, used when the workspace runs it. A name that is not a
// built-in agent defines a custom agent derived from the built-in From.
type AgentSettings struct {
	// From is the built-in agent a custom agent is derived from.
	From string `toml:"from,omitempty"`
	// Install adds the built-in image layer that installs the harness;
	// false leaves it to the workspace image. Unset means true.
	Install *bool `toml:"install,omitempty"`
	// Version is the npm version of the harness the image layer installs
	// instead of the version pinned by jailoc.
	Version string `toml:"version,omitempty"`
	// Args are appended to every start of the agent. An empty list is kept
	// when the configuration is written back: it clears the defaults' args,
	// which an omitted list inherits.
	Args []string `toml:"args"`
	// Mounts are merged by guest path over the harness mounts, before the
	// [defaults] and workspace mounts.
	Mounts []string `toml:"mounts,omitempty"`
	// Profile selects where the harness keeps its state. Unset keeps it per
	// workspace; a custom agent does not inherit it from its From.
	Profile *Profile `toml:"profile,omitempty"`
}

type Mount struct {
	Host      string
	Container string
	Mode      string
}

// Secret is one [<scope>.secrets.<env|file>.<NAME>] entry. The sub-table it
// lives in decides the destination; exactly one From* field decides the
// source. FromEnv names the host env var the value is read from, FromFile the
// absolute host path, FromCommand a command run on the host with /bin/sh -c
// whose standard output is the value. The tags carry omitempty so a
// whole-config TOML re-encode never injects empty source keys into a user's
// config.
type Secret struct {
	FromEnv     string `toml:"from_env,omitempty"`
	FromFile    string `toml:"from_file,omitempty"`
	FromCommand string `toml:"from_command,omitempty"`
}

// Secrets is a whole [<scope>.secrets] block. The sub-table a secret lives in
// decides its destination, so env-vs-file is structural, not a validated XOR.
type Secrets struct {
	Env  map[string]Secret `toml:"env,omitempty"`
	File map[string]Secret `toml:"file,omitempty"`
}

type Config struct {
	Mode         string               `toml:"mode,omitempty"`
	PasswordMode string               `toml:"password_mode,omitempty"`
	DetachKeys   string               `toml:"detach_keys,omitempty"`
	Base         BaseConfig           `toml:"base,omitempty"`
	VM           VMConfig             `toml:"vm,omitempty"`
	Defaults     Defaults             `toml:"defaults,omitempty"`
	Workspaces   map[string]Workspace `toml:"workspaces"`
}

// VMConfig locates the host runtime of the vm backend. Empty fields use
// platform defaults.
type VMConfig struct {
	Runner string `toml:"runner,omitempty"`
	LibDir string `toml:"lib_dir,omitempty"`
	// Builder selects where workspace images are built: BuilderAuto (host
	// Docker when usable, otherwise a builder VM), BuilderDocker or
	// BuilderVM. Empty means BuilderAuto.
	Builder string `toml:"builder,omitempty"`
}

// Image builders of the VM backend.
const (
	BuilderAuto   = "auto"
	BuilderDocker = "docker"
	BuilderVM     = "vm"
)

type BaseConfig struct {
	Dockerfile string `toml:"dockerfile,omitempty"`
}

type Defaults struct {
	Backend         string                   `toml:"backend,omitempty"`
	Agent           string                   `toml:"agent,omitempty"`
	Agents          map[string]AgentSettings `toml:"agents,omitempty"`
	Env             []string                 `toml:"env,omitempty"`
	EnvFile         []string                 `toml:"env_file,omitempty"`
	Mounts          []string                 `toml:"mounts,omitempty"`
	Snapshots       []string                 `toml:"snapshots,omitempty"`
	AllowedHosts    []string                 `toml:"allowed_hosts,omitempty"`
	AllowedNetworks []string                 `toml:"allowed_networks,omitempty"`
	Secrets         Secrets                  `toml:"secrets,omitempty"`
	Image           string                   `toml:"image,omitempty"`
	SSHAuthSock     bool                     `toml:"ssh_auth_sock,omitempty"`
	GitConfig       *bool                    `toml:"git_config,omitempty"`
	CPU             *float64                 `toml:"cpu,omitempty"`
	Memory          *string                  `toml:"memory,omitempty"`
	ExposePort      *bool                    `toml:"expose_port,omitempty"`
	EnableDocker    *bool                    `toml:"enable_docker,omitempty"`
	FilteredDNS     *bool                    `toml:"filtered_dns,omitempty"`
	DNSUpstream     string                   `toml:"dns_upstream,omitempty"`
	DNSBlockedZones []string                 `toml:"dns_blocked_zones,omitempty"`
	CABundle        *CABundle                `toml:"ca_bundle,omitempty"`
	IsolatedBuild   *bool                    `toml:"isolated_build,omitempty"`
	ProtectGit      *bool                    `toml:"protect_git,omitempty"`
	Ports           []Port                   `toml:"ports,omitempty"`
}

type Workspace struct {
	Backend         string                   `toml:"backend,omitempty"`
	Agent           string                   `toml:"agent,omitempty"`
	Agents          map[string]AgentSettings `toml:"agents,omitempty"`
	Paths           []string                 `toml:"paths,omitempty"`
	Mounts          []string                 `toml:"mounts,omitempty"`
	Snapshots       []string                 `toml:"snapshots,omitempty"`
	AllowedHosts    []string                 `toml:"allowed_hosts,omitempty"`
	AllowedNetworks []string                 `toml:"allowed_networks,omitempty"`
	Env             []string                 `toml:"env,omitempty"`
	EnvFile         []string                 `toml:"env_file,omitempty"`
	Secrets         Secrets                  `toml:"secrets,omitempty"`
	BuildContext    string                   `toml:"build_context,omitempty"`
	Dockerfile      string                   `toml:"dockerfile,omitempty"`
	Image           string                   `toml:"image,omitempty"`
	SSHAuthSock     *bool                    `toml:"ssh_auth_sock,omitempty"`
	GitConfig       *bool                    `toml:"git_config,omitempty"`
	CPU             *float64                 `toml:"cpu,omitempty"`
	Memory          *string                  `toml:"memory,omitempty"`
	ExposePort      *bool                    `toml:"expose_port,omitempty"`
	EnableDocker    *bool                    `toml:"enable_docker,omitempty"`
	FilteredDNS     *bool                    `toml:"filtered_dns,omitempty"`
	DNSUpstream     string                   `toml:"dns_upstream,omitempty"`
	DNSBlockedZones []string                 `toml:"dns_blocked_zones,omitempty"`
	CABundle        *CABundle                `toml:"ca_bundle,omitempty"`
	IsolatedBuild   *bool                    `toml:"isolated_build,omitempty"`
	ProtectGit      *bool                    `toml:"protect_git,omitempty"`
	Ports           []Port                   `toml:"ports,omitempty"`
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
	configPath := ConfigPath()
	slog.Debug("loading config", "path", configPath)

	cfg, err := loadFrom(configPath, true)
	if err != nil {
		return nil, err
	}

	slog.Debug("config loaded", "workspaces", len(cfg.Workspaces))

	return cfg, nil
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

// ReadRaw decodes path without validation or path expansion, for tools
// that rewrite the configuration.
func ReadRaw(path string) (*Config, error) {
	return decode(path)
}

// Encode returns cfg as TOML.
func Encode(cfg *Config) ([]byte, error) {
	var buf bytes.Buffer
	if err := toml.NewEncoder(&buf).Encode(cfg); err != nil {
		return nil, fmt.Errorf("encode config: %w", err)
	}
	return buf.Bytes(), nil
}

// WriteRaw encodes cfg to path atomically. Comments in the previous file are
// not preserved.
func WriteRaw(path string, cfg *Config) error {
	data, err := Encode(cfg)
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".config-*.toml")
	if err != nil {
		return fmt.Errorf("create temporary config: %w", err)
	}
	defer func() { _ = os.Remove(tmp.Name()) }()
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("write config: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("write config: %w", err)
	}
	if err := os.Rename(tmp.Name(), path); err != nil {
		return fmt.Errorf("replace config: %w", err)
	}
	return nil
}

// ValidateCopy checks that cfg survives an encode/decode round trip and
// passes validation, without modifying cfg.
func ValidateCopy(cfg *Config) error {
	var buf bytes.Buffer
	if err := toml.NewEncoder(&buf).Encode(cfg); err != nil {
		return fmt.Errorf("encode config: %w", err)
	}
	var copied Config
	if _, err := toml.Decode(buf.String(), &copied); err != nil {
		return fmt.Errorf("decode config: %w", err)
	}
	if copied.Workspaces == nil {
		copied.Workspaces = map[string]Workspace{}
	}
	return Validate(&copied)
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

	// Absolute local paths are allowed
	if filepath.IsAbs(value) {
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
	return fmt.Errorf("%s: must be an absolute path, ~ home path, or HTTP(S) URL, got %q", fieldName, value)
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
		if reservedEnvKey(key) {
			return fmt.Errorf("%s: env key %q is reserved and cannot be overridden", context, key)
		}
	}
	return nil
}

func validateEnvFiles(paths []string, context string) error {
	for _, p := range paths {
		if !filepath.IsAbs(p) {
			return fmt.Errorf("%s: env_file path %q must be absolute", context, p)
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

func validateEnvVarName(name string) error {
	if !envVarNameRe.MatchString(name) {
		return fmt.Errorf("invalid environment variable name %q: must match ^[A-Za-z_][A-Za-z0-9_]*$", name)
	}
	return nil
}

func validateSecretFile(path string) error {
	if !filepath.IsAbs(path) {
		return fmt.Errorf("file path %q must be absolute", path)
	}
	return nil
}

// reservedSecretEnvName reports env var names an env secret must never claim:
// jailoc sets them itself, and HOME or PATH secrets would break every session.
func reservedSecretEnvName(name string) bool {
	return reservedEnvKey(name) || name == "HOME" || name == "PATH"
}

type secretDest int

const (
	destEnv secretDest = iota
	destFile
)

func validateSecret(name string, secret Secret, dest secretDest, context string) error {
	destLabel := "file"
	if dest == destEnv {
		destLabel = "env"
		if err := validateEnvVarName(name); err != nil {
			return fmt.Errorf("%s: env secret %q: %w", context, name, err)
		}
		if reservedSecretEnvName(name) {
			return fmt.Errorf("%s: env secret %q: name is reserved and cannot be overridden", context, name)
		}
	} else if !secretNameRe.MatchString(name) {
		return fmt.Errorf("%s: file secret %q: invalid name: must match [a-zA-Z0-9_-]+", context, name)
	}

	sources := 0
	for _, s := range []string{secret.FromEnv, secret.FromFile, secret.FromCommand} {
		if s != "" {
			sources++
		}
	}
	if sources != 1 {
		return fmt.Errorf("%s: %s secret %q: exactly one of \"from_env\", \"from_file\" or \"from_command\" must be set", context, destLabel, name)
	}
	if secret.FromCommand != "" && strings.TrimSpace(secret.FromCommand) == "" {
		return fmt.Errorf("%s: %s secret %q: \"from_command\" must not be blank", context, destLabel, name)
	}
	if secret.FromEnv != "" {
		if err := validateEnvVarName(secret.FromEnv); err != nil {
			return fmt.Errorf("%s: %s secret %q: \"from_env\": %w", context, destLabel, name, err)
		}
	}
	if secret.FromFile != "" {
		if err := validateSecretFile(secret.FromFile); err != nil {
			return fmt.Errorf("%s: %s secret %q: %w", context, destLabel, name, err)
		}
	}
	return nil
}

func validateSecretsBlock(secrets Secrets, context string) error {
	for _, name := range slices.Sorted(maps.Keys(secrets.Env)) {
		if _, ok := secrets.File[name]; ok {
			return fmt.Errorf("%s: secret %q: declared in both \"secrets.env\" and \"secrets.file\": a name may live in only one", context, name)
		}
	}

	for _, name := range slices.Sorted(maps.Keys(secrets.Env)) {
		if err := validateSecret(name, secrets.Env[name], destEnv, context); err != nil {
			return err
		}
	}

	for _, name := range slices.Sorted(maps.Keys(secrets.File)) {
		if err := validateSecret(name, secrets.File[name], destFile, context); err != nil {
			return err
		}
	}

	return nil
}

func expandSecretsBlockFiles(secrets *Secrets) error {
	if secrets == nil {
		return nil
	}

	for _, secretMap := range []map[string]Secret{secrets.Env, secrets.File} {
		for _, name := range slices.Sorted(maps.Keys(secretMap)) {
			secret := secretMap[name]
			if secret.FromFile == "" {
				continue
			}
			expanded, err := ExpandPath(secret.FromFile)
			if err != nil {
				return fmt.Errorf("expand secret file %q: %w", secret.FromFile, err)
			}
			secret.FromFile = expanded
			secretMap[name] = secret
		}
	}

	return nil
}

func ParseMount(spec string) (Mount, error) {
	// Handle removal specs (e.g. ":/container" or ":/container:ro").
	// format.ParseVolume rejects empty source sections.
	if strings.HasPrefix(spec, ":") {
		return parseRemovalMount(spec)
	}

	vol, err := format.ParseVolume(spec)
	if err != nil {
		return Mount{}, fmt.Errorf("invalid mount spec %q: %w", spec, err)
	}

	// ParseVolume accepts single-component specs as anonymous volumes;
	// jailoc requires both host and container.
	if vol.Source == "" || vol.Target == "" {
		return Mount{}, fmt.Errorf("invalid mount spec %q: expected host:container[:mode]", spec)
	}

	host := vol.Source
	container := vol.Target

	// ParseVolume silently drops unknown mode options. Re-validate from the
	// raw spec to reject anything other than ro/rw.
	mode := rawMode(spec, host, container)
	if mode == "" {
		mode = "rw"
	}
	if mode != "ro" && mode != "rw" {
		return Mount{}, fmt.Errorf("invalid mount spec %q: mode must be ro or rw", spec)
	}

	if host != "" {
		expandedHost, err := ExpandPath(host)
		if err != nil {
			return Mount{}, fmt.Errorf("expand mount host path %q: %w", host, err)
		}
		if !filepath.IsAbs(expandedHost) {
			return Mount{}, fmt.Errorf("invalid mount spec %q: host path %q must be an absolute path or ~ home path", spec, host)
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

// parseRemovalMount handles mount specs with empty host (e.g. ":/container")
// that override an inherited mount by removing it.
func parseRemovalMount(spec string) (Mount, error) {
	rest := spec[1:]
	parts := strings.SplitN(rest, ":", 2)
	container := parts[0]
	mode := "rw"
	if len(parts) == 2 {
		mode = parts[1]
	}

	if mode != "ro" && mode != "rw" {
		return Mount{}, fmt.Errorf("invalid mount spec %q: mode must be ro or rw", spec)
	}

	expandedContainer, err := ExpandPath(container)
	if err != nil {
		return Mount{}, fmt.Errorf("expand mount container path %q: %w", container, err)
	}
	if !strings.HasPrefix(expandedContainer, "/") {
		return Mount{}, fmt.Errorf("invalid mount spec %q: container path %q must be absolute (start with /)", spec, container)
	}

	return Mount{Host: "", Container: container, Mode: mode}, nil
}

// rawMode extracts the raw mode string from a mount spec, given the source and
// target parsed by format.ParseVolume. Returns "" if no mode was specified.
func rawMode(spec, source, target string) string {
	prefix := source + ":" + target
	if !strings.HasPrefix(spec, prefix) {
		return ""
	}
	rest := spec[len(prefix):]
	if len(rest) > 0 && rest[0] == ':' {
		return rest[1:]
	}
	return ""
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

	switch {
	case cfg.Mode != "":
		return v1Setting("mode")
	case cfg.PasswordMode != "":
		return v1Setting("password_mode")
	case cfg.Defaults.ExposePort != nil:
		return v1Setting("defaults: expose_port")
	}
	if cfg.DetachKeys != "" {
		if _, err := ParseDetachKeys(cfg.DetachKeys); err != nil {
			return err
		}
	}

	if err := validateAgent(cfg.Defaults.Agent, cfg.Defaults.Agents, nil); err != nil {
		return fmt.Errorf("defaults: %w", err)
	}
	if err := validateBackend(cfg.Defaults.Backend); err != nil {
		return fmt.Errorf("defaults: %w", err)
	}
	switch cfg.VM.Builder {
	case "", BuilderAuto, BuilderDocker, BuilderVM:
	default:
		return fmt.Errorf("vm: invalid builder %q: must be %q, %q, %q, or empty (%s)", cfg.VM.Builder, BuilderAuto, BuilderDocker, BuilderVM, BuilderAuto)
	}
	for _, p := range []*string{&cfg.VM.Runner, &cfg.VM.LibDir} {
		if *p == "" {
			continue
		}
		expanded, err := ExpandPath(*p)
		if err != nil {
			return fmt.Errorf("vm: expand path %q: %w", *p, err)
		}
		if !filepath.IsAbs(expanded) {
			return fmt.Errorf("vm: path %q must be absolute", *p)
		}
		*p = expanded
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

	if err := expandSecretsBlockFiles(&cfg.Defaults.Secrets); err != nil {
		return fmt.Errorf("defaults: %w", err)
	}

	if err := validateSecretsBlock(cfg.Defaults.Secrets, "defaults"); err != nil {
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

	for i, spec := range cfg.Defaults.Snapshots {
		normalized, err := normalizeSnapshot(spec, "defaults")
		if err != nil {
			return err
		}
		cfg.Defaults.Snapshots[i] = normalized
	}
	if err := validatePorts(cfg.Defaults.Ports); err != nil {
		return fmt.Errorf("defaults: %w", err)
	}

	if cfg.Defaults.CPU != nil && (*cfg.Defaults.CPU <= 0 || math.IsNaN(*cfg.Defaults.CPU) || math.IsInf(*cfg.Defaults.CPU, 0)) {
		return fmt.Errorf("defaults: cpu must be a finite number greater than 0")
	}
	if cfg.Defaults.Memory != nil && !validMemory.MatchString(*cfg.Defaults.Memory) {
		return fmt.Errorf("defaults: invalid memory format %q: must be a positive integer optionally followed by k, m, or g (e.g. \"4g\", \"512m\")", *cfg.Defaults.Memory)
	}
	if err := validateDNS(cfg.Defaults.DNSUpstream, cfg.Defaults.DNSBlockedZones); err != nil {
		return fmt.Errorf("defaults: %w", err)
	}

	names := make([]string, 0, len(cfg.Workspaces))
	for name := range cfg.Workspaces {
		names = append(names, name)
	}
	sort.Strings(names)

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
		if err := validateSecretsBlock(ws.Secrets, wsContext); err != nil {
			return err
		}

		if ws.CPU != nil && (*ws.CPU <= 0 || math.IsNaN(*ws.CPU) || math.IsInf(*ws.CPU, 0)) {
			return fmt.Errorf("workspace %q: cpu must be a finite number greater than 0", name)
		}
		if ws.Memory != nil && !validMemory.MatchString(*ws.Memory) {
			return fmt.Errorf("workspace %q: invalid memory format %q: must be a positive integer optionally followed by k, m, or g (e.g. \"4g\", \"512m\")", name, *ws.Memory)
		}
		if err := validateDNS(ws.DNSUpstream, ws.DNSBlockedZones); err != nil {
			return fmt.Errorf("workspace %q: %w", name, err)
		}
		enabled := cfg.Defaults.FilteredDNS != nil && *cfg.Defaults.FilteredDNS
		if ws.FilteredDNS != nil {
			enabled = *ws.FilteredDNS
		}
		if enabled && ws.DNSUpstream == "" && cfg.Defaults.DNSUpstream == "" {
			return fmt.Errorf("workspace %q: dns_upstream is required when filtered_dns is enabled", name)
		}
		if enabled && len(ws.DNSBlockedZones) == 0 && len(cfg.Defaults.DNSBlockedZones) == 0 {
			return fmt.Errorf("workspace %q: dns_blocked_zones is required when filtered_dns is enabled", name)
		}
		if enabled {
			zones := ws.DNSBlockedZones
			if zones == nil {
				zones = cfg.Defaults.DNSBlockedZones
			}
			for _, host := range mergeDedup(cfg.Defaults.AllowedHosts, ws.AllowedHosts) {
				normalizedHost := strings.TrimSuffix(strings.ToLower(host), ".")
				for _, zone := range zones {
					normalizedZone := strings.TrimSuffix(strings.ToLower(zone), ".")
					if normalizedHost == normalizedZone || strings.HasSuffix(normalizedHost, "."+normalizedZone) {
						return fmt.Errorf("workspace %q: allowed_hosts entry %q conflicts with dns_blocked_zones entry %q", name, host, zone)
					}
				}
			}
		}

		if ws.ExposePort != nil {
			return fmt.Errorf("workspace %q: %w", name, v1Setting("expose_port"))
		}
		if err := validateAgent(ws.Agent, ws.Agents, cfg.Defaults.Agents); err != nil {
			return fmt.Errorf("workspace %q: %w", name, err)
		}
		if err := validateBackend(ws.Backend); err != nil {
			return fmt.Errorf("workspace %q: %w", name, err)
		}
		for i, spec := range ws.Snapshots {
			normalized, err := normalizeSnapshot(spec, fmt.Sprintf("workspace %q", name))
			if err != nil {
				return err
			}
			ws.Snapshots[i] = normalized
		}
		if err := validatePorts(ws.Ports); err != nil {
			return fmt.Errorf("workspace %q: %w", name, err)
		}
		if explicitTrue(cfg.Defaults.EnableDocker, ws.EnableDocker) && explicitTrue(cfg.Defaults.ProtectGit, ws.ProtectGit) {
			return fmt.Errorf("workspace %q: enable_docker cannot be combined with protect_git: Docker access is root access in the VM", name)
		}

		cfg.Workspaces[name] = ws
	}

	return nil
}

// normalizeSnapshot validates a snapshots entry "host:container" (":ro"
// accepted, ":rw" rejected because snapshots are read-only) or a removal entry
// ":container", and returns it as "host:container:ro" with expanded paths.
func normalizeSnapshot(spec, context string) (string, error) {
	if strings.HasSuffix(spec, ":rw") {
		return "", fmt.Errorf("%s: snapshot %q: snapshots are read-only; remove \":rw\" or use mounts", context, spec)
	}
	m, err := ParseMount(spec)
	if err != nil {
		return "", fmt.Errorf("%s: snapshot %q: %w", context, spec, err)
	}
	if err := validateMountHostPath(m.Host, context); err != nil {
		return "", err
	}
	host, err := ExpandPath(m.Host)
	if err != nil {
		return "", fmt.Errorf("%s: expand snapshot host path %q: %w", context, m.Host, err)
	}
	container, err := ExpandPath(m.Container)
	if err != nil {
		return "", fmt.Errorf("%s: expand snapshot destination %q: %w", context, m.Container, err)
	}
	container = filepath.Clean(container)
	for _, prefix := range forbiddenMountContainerPrefixes {
		if container == prefix || strings.HasPrefix(container, prefix+"/") {
			return "", fmt.Errorf("%s: snapshot destination %q is not allowed (conflicts with system directory %q)", context, container, prefix)
		}
	}
	return formatMount(Mount{Host: host, Container: container, Mode: "ro"}), nil
}

func validateBackend(backend string) error {
	switch backend {
	case "", BackendVM:
		return nil
	case BackendDocker:
		return v1Setting(`backend = "docker"`)
	}
	return fmt.Errorf("invalid backend %q: must be %q or empty", backend, BackendVM)
}

var (
	agentNameRe = regexp.MustCompile(`^[a-z0-9-]+$`)
	// agentVersionRe matches exact npm versions. Dist-tags such as latest
	// are rejected: the image layer is rebuilt only when its Dockerfile,
	// which holds the version, changes.
	agentVersionRe = regexp.MustCompile(`^[0-9]+\.[0-9]+\.[0-9]+(?:-[0-9A-Za-z.-]+)?(?:\+[0-9A-Za-z.-]+)?$`)
)

// validateAgent checks the agent of a scope and its agents tables. A key
// that is not a built-in agent defines a custom agent: its from, set in
// agents or in inherited (the defaults' tables of a workspace), names a
// built-in agent. name must be a built-in agent or a custom agent defined
// in either.
func validateAgent(name string, agents, inherited map[string]AgentSettings) error {
	builtins := strings.Join(agent.Names(), ", ")
	for _, key := range slices.Sorted(maps.Keys(agents)) {
		s := agents[key]
		if _, ok := agent.Lookup(key); ok {
			if s.From != "" {
				return fmt.Errorf("agents.%s: from is only for custom agents; %s is a built-in agent", key, key)
			}
		} else {
			if !agentNameRe.MatchString(key) {
				return fmt.Errorf("agents: invalid agent name %q: must match [a-z0-9-]+", key)
			}
			from := cmp.Or(s.From, inherited[key].From)
			if from == "" {
				return fmt.Errorf("agents.%s: custom agent needs from, the built-in agent it is derived from (%s)", key, builtins)
			}
			if _, ok := agent.Lookup(from); !ok {
				return fmt.Errorf("agents.%s: from %q is not a built-in agent: must be one of %s", key, from, builtins)
			}
		}
		if s.Version != "" && !agentVersionRe.MatchString(s.Version) {
			return fmt.Errorf("agents.%s: invalid version %q: must be an exact npm version such as 1.2.3", key, s.Version)
		}
		if s.Profile != nil {
			if name, ok := s.Profile.Name(); ok && !agentNameRe.MatchString(name) {
				return fmt.Errorf("agents.%s: invalid profile name %q: must match [a-z0-9-]+", key, name)
			}
		}
		if err := validateAgentMounts(s.Mounts); err != nil {
			return fmt.Errorf("agents.%s: %w", key, err)
		}
	}
	if name == "" {
		return nil
	}
	if _, ok := agent.Lookup(name); ok {
		return nil
	}
	if _, ok := agents[name]; ok {
		return nil
	}
	if _, ok := inherited[name]; ok {
		return nil
	}
	return fmt.Errorf("invalid agent %q: must be one of %s or a custom agent defined under agents", name, builtins)
}

// validateAgentMounts checks the mounts of an agents table like workspace
// mounts: ":/guest" entries remove an inherited mount.
func validateAgentMounts(mounts []string) error {
	for _, spec := range mounts {
		m, err := ParseMount(spec)
		if err != nil {
			return fmt.Errorf("mounts: %w", err)
		}
		if err := validateMountHostPath(m.Host, "mounts"); err != nil {
			return err
		}
		container, err := ExpandPath(m.Container)
		if err != nil {
			return fmt.Errorf("mounts: expand mount container path %q: %w", m.Container, err)
		}
		container = filepath.Clean(container)
		for _, prefix := range forbiddenMountContainerPrefixes {
			if container == prefix || strings.HasPrefix(container, prefix+"/") {
				return fmt.Errorf("mounts: mount container path %q is not allowed (conflicts with container-internal directory %q)", container, prefix)
			}
		}
	}
	return nil
}

// v1Setting reports a setting of the jailoc v1 Docker backend, which
// `jailoc migrate` rewrites for the VM.
func v1Setting(setting string) error {
	return fmt.Errorf("%s is a jailoc v1 setting; run `jailoc migrate` to update the config", setting)
}

// explicitTrue reports whether a boolean option is set to true, the workspace
// value taking precedence over the defaults.
func explicitTrue(defaults, workspace *bool) bool {
	if workspace != nil {
		return *workspace
	}
	return defaults != nil && *defaults
}

var dnsLabelRe = regexp.MustCompile(`^[a-zA-Z0-9](?:[a-zA-Z0-9-]*[a-zA-Z0-9])?$`)

func validateDNS(upstream string, zones []string) error {
	if upstream != "" {
		ip := net.ParseIP(upstream)
		if ip == nil || ip.To4() == nil || ip.IsUnspecified() || ip.IsLoopback() || ip.IsMulticast() || ip.IsLinkLocalUnicast() {
			return fmt.Errorf("dns_upstream must be a routable IPv4 address, got %q", upstream)
		}
	}
	for _, zone := range zones {
		if len(zone) > 253 || zone == "" || zone == "." || strings.ContainsAny(zone, " \t\r\n{}") {
			return fmt.Errorf("invalid dns_blocked_zones entry %q", zone)
		}
		for _, label := range strings.Split(strings.TrimSuffix(zone, "."), ".") {
			if !dnsLabelRe.MatchString(label) || len(label) > 63 {
				return fmt.Errorf("invalid dns_blocked_zones entry %q", zone)
			}
		}
	}
	return nil
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

	if err := expandSecretsBlockFiles(&ws.Secrets); err != nil {
		return err
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
