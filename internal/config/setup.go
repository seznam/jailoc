package config

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/BurntSushi/toml"
	"github.com/pelletier/go-toml/v2/unstable/edit"
)

func IsDefaultFile(path string) (bool, error) {
	raw, err := os.ReadFile(path) //nolint:gosec // G304: path is the jailoc config file
	if err != nil {
		if os.IsNotExist(err) {
			return true, nil
		}
		return false, fmt.Errorf("read config %q: %w", path, err)
	}
	normalize := func(s string) string {
		return strings.TrimSpace(strings.ReplaceAll(s, "\r\n", "\n"))
	}
	return normalize(string(raw)) == normalize(defaultConfigContent), nil
}

func ValidateWorkspaceName(name string) error {
	if !workspaceNameRe.MatchString(name) {
		return fmt.Errorf("workspace name %q is invalid: must match [a-z0-9-]+", name)
	}
	return nil
}

func SanitizeWorkspaceName(raw string) string {
	s := strings.ToLower(strings.TrimSpace(raw))
	var b strings.Builder
	lastDash := false
	for _, r := range s {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
			lastDash = false
		} else if !lastDash && b.Len() > 0 {
			b.WriteByte('-')
			lastDash = true
		}
	}
	out := strings.Trim(b.String(), "-")
	if out == "" {
		return "default"
	}
	return out
}

func ValidateMemoryFormat(mem string) error {
	if !validMemory.MatchString(mem) {
		return fmt.Errorf("invalid memory format %q: must be a positive integer optionally followed by k, m, or g (e.g. \"4g\", \"512m\")", mem)
	}
	return nil
}

type FirstTimeOptions struct {
	Agent         string
	GitConfig     bool
	SSHAuthSock   bool
	GPGAgent      bool
	EnableDocker  bool
	CPU           float64
	Memory        string
	AllowInternet bool
	ExtraHosts    []string
	WorkspaceName string
	WorkspacePath string
}

type WorkspaceOptions struct {
	Name          string
	Paths         []string
	Agent         string
	EnableDocker  *bool
	AllowInternet bool
	ExtraHosts    []string
}

func RenderFirstTime(opts FirstTimeOptions) ([]byte, error) {
	doc, err := edit.Parse([]byte(defaultConfigContent))
	if err != nil {
		return nil, fmt.Errorf("parse default config template: %w", err)
	}

	if opts.Agent != "" {
		if err := doc.Set([]string{"defaults", "agent"}, opts.Agent); err != nil {
			return nil, fmt.Errorf("set defaults.agent: %w", err)
		}
	}
	if !opts.GitConfig {
		if err := doc.Set([]string{"defaults", "git_config"}, false); err != nil {
			return nil, fmt.Errorf("set defaults.git_config: %w", err)
		}
	}
	if opts.SSHAuthSock {
		if err := doc.Set([]string{"defaults", "ssh_auth_sock"}, true); err != nil {
			return nil, fmt.Errorf("set defaults.ssh_auth_sock: %w", err)
		}
	}
	if opts.GPGAgent {
		if err := doc.Set([]string{"defaults", "gpg_agent"}, true); err != nil {
			return nil, fmt.Errorf("set defaults.gpg_agent: %w", err)
		}
	}
	if opts.EnableDocker {
		if err := doc.Set([]string{"defaults", "enable_docker"}, true); err != nil {
			return nil, fmt.Errorf("set defaults.enable_docker: %w", err)
		}
	}
	if opts.CPU > 0 && opts.CPU != 2.0 {
		if err := doc.Set([]string{"defaults", "cpu"}, opts.CPU); err != nil {
			return nil, fmt.Errorf("set defaults.cpu: %w", err)
		}
	}
	if opts.Memory != "" && !strings.EqualFold(opts.Memory, "4g") {
		if err := doc.Set([]string{"defaults", "memory"}, opts.Memory); err != nil {
			return nil, fmt.Errorf("set defaults.memory: %w", err)
		}
	}

	if opts.AllowInternet {
		doc.Delete([]string{"defaults", "network", "rules"})
		rules := []map[string]any{
			{
				"name":  "internet",
				"allow": []string{"*"},
			},
		}
		if err := doc.Set([]string{"defaults", "network", "rules"}, rules); err != nil {
			return nil, fmt.Errorf("set defaults.network.rules: %w", err)
		}
	} else if len(opts.ExtraHosts) > 0 {
		var parsed Config
		if _, err := toml.Decode(defaultConfigContent, &parsed); err != nil {
			return nil, fmt.Errorf("decode default network rules: %w", err)
		}
		doc.Delete([]string{"defaults", "network", "rules"})
		var rules []map[string]any
		for _, r := range parsed.Defaults.Network.Get().Rules {
			rules = append(rules, map[string]any{
				"name":  r.Name,
				"allow": r.Allow,
			})
		}
		rules = append(rules, map[string]any{
			"name":  "custom",
			"allow": opts.ExtraHosts,
		})
		if err := doc.Set([]string{"defaults", "network", "rules"}, rules); err != nil {
			return nil, fmt.Errorf("set defaults.network.rules: %w", err)
		}
	}

	wsName := opts.WorkspaceName
	if wsName == "" {
		wsName = "default"
	}
	var paths []string
	if opts.WorkspacePath != "" {
		paths = []string{opts.WorkspacePath}
	}
	if wsName == "default" {
		if len(paths) > 0 {
			if err := doc.Set([]string{"workspaces", "default", "paths"}, paths); err != nil {
				return nil, fmt.Errorf("set workspaces.default.paths: %w", err)
			}
		}
	} else {
		if err := doc.Set([]string{"workspaces", wsName}, map[string]any{
			"paths": paths,
		}); err != nil {
			return nil, fmt.Errorf("set workspaces.%s: %w", wsName, err)
		}
	}

	out := doc.Bytes()
	if err := validateBytes(out); err != nil {
		return nil, err
	}
	return out, nil
}

func RenderWorkspace(raw []byte, opts WorkspaceOptions) ([]byte, error) {
	if err := ValidateWorkspaceName(opts.Name); err != nil {
		return nil, err
	}
	doc, err := edit.Parse(raw)
	if err != nil {
		return nil, fmt.Errorf("parse config: %w", err)
	}

	wsKey := []string{"workspaces", opts.Name}
	if !doc.Has(wsKey) {
		wsMap := map[string]any{
			"paths": opts.Paths,
		}
		if opts.Agent != "" {
			wsMap["agent"] = opts.Agent
		}
		if opts.EnableDocker != nil {
			wsMap["enable_docker"] = *opts.EnableDocker
		}
		if opts.AllowInternet || len(opts.ExtraHosts) > 0 {
			var rules []map[string]any
			if opts.AllowInternet {
				rules = append(rules, map[string]any{
					"name":  "internet",
					"allow": []string{"*"},
				})
			}
			if len(opts.ExtraHosts) > 0 {
				rules = append(rules, map[string]any{
					"name":  "custom",
					"allow": opts.ExtraHosts,
				})
			}
			wsMap["network"] = map[string]any{
				"rules": rules,
			}
		}
		if err := doc.Set(wsKey, wsMap); err != nil {
			return nil, fmt.Errorf("add workspace %q: %w", opts.Name, err)
		}
	} else {
		if err := doc.Set(append(wsKey, "paths"), opts.Paths); err != nil {
			return nil, fmt.Errorf("set workspace %q paths: %w", opts.Name, err)
		}
		if opts.Agent != "" {
			if err := doc.Set(append(wsKey, "agent"), opts.Agent); err != nil {
				return nil, fmt.Errorf("set workspace %q agent: %w", opts.Name, err)
			}
		} else {
			doc.Delete(append(wsKey, "agent"))
		}
		if opts.EnableDocker != nil {
			if err := doc.Set(append(wsKey, "enable_docker"), *opts.EnableDocker); err != nil {
				return nil, fmt.Errorf("set workspace %q enable_docker: %w", opts.Name, err)
			}
		} else {
			doc.Delete(append(wsKey, "enable_docker"))
		}
		doc.Delete(append(wsKey, "network", "rules"))
		if opts.AllowInternet || len(opts.ExtraHosts) > 0 {
			var rules []map[string]any
			if opts.AllowInternet {
				rules = append(rules, map[string]any{
					"name":  "internet",
					"allow": []string{"*"},
				})
			}
			if len(opts.ExtraHosts) > 0 {
				rules = append(rules, map[string]any{
					"name":  "custom",
					"allow": opts.ExtraHosts,
				})
			}
			if err := doc.Set(append(wsKey, "network", "rules"), rules); err != nil {
				return nil, fmt.Errorf("set workspace %q network.rules: %w", opts.Name, err)
			}
		}
	}

	out := doc.Bytes()
	if err := validateBytes(out); err != nil {
		return nil, err
	}
	return out, nil
}

func WriteBytes(path string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		return fmt.Errorf("create config directory for %q: %w", path, err)
	}
	targetPath := path
	if resolved, err := filepath.EvalSymlinks(path); err == nil {
		targetPath = resolved
	}
	tmp, err := os.CreateTemp(filepath.Dir(targetPath), ".config-*.toml")
	if err != nil {
		return fmt.Errorf("create temporary config: %w", err)
	}
	defer func() { _ = os.Remove(tmp.Name()) }()

	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("write temporary config: %w", err)
	}
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("chmod temporary config: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close temporary config: %w", err)
	}
	if err := os.Rename(tmp.Name(), targetPath); err != nil {
		return fmt.Errorf("replace config: %w", err)
	}
	return nil
}

func validateBytes(data []byte) error {
	var cfg Config
	if _, err := toml.NewDecoder(bytes.NewReader(data)).Decode(&cfg); err != nil {
		return fmt.Errorf("decode generated config: %w", err)
	}
	if cfg.Workspaces == nil {
		cfg.Workspaces = map[string]Workspace{}
	}
	return Validate(&cfg)
}
