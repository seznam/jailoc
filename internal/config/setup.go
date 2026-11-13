package config

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strconv"
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

// templateWorkspaceHeader starts the workspace section of defaultConfigContent.
const templateWorkspaceHeader = "[workspaces.default]"

// Comments written next to the settings the wizard can change.
const (
	agentComment  = "opencode (OpenCode 2), opencode1, opencode2, claude, copilot, omo, agy or an agent defined under agents"
	dockerComment = "run an isolated Docker daemon inside the VM"
)

// templateOption is a commented template setting to uncomment with a new value.
type templateOption struct {
	key   string
	value string
}

// splitTemplate divides defaultConfigContent into the lines before the workspace section and the workspace section.
func splitTemplate() (head, workspace []string) {
	lines := strings.Split(defaultConfigContent, "\n")
	for i, line := range lines {
		if line == templateWorkspaceHeader {
			return lines[:i:i], lines[i:]
		}
	}
	return lines, nil
}

// activateOption uncomments the template line of key, sets value and keeps the template's trailing comment.
func activateOption(lines []string, key, value string) error {
	prefix := "# " + key + " = "
	for i, line := range lines {
		if !strings.HasPrefix(line, prefix) {
			continue
		}
		_, comment, _ := strings.Cut(strings.TrimPrefix(line, prefix), "  #")
		lines[i] = key + " = " + value + "  #" + comment
		return nil
	}
	return fmt.Errorf("template has no %q option", key)
}

// replaceLine replaces the first line starting with prefix.
func replaceLine(lines []string, prefix, replacement string) error {
	for i, line := range lines {
		if strings.HasPrefix(line, prefix) {
			lines[i] = replacement
			return nil
		}
	}
	return fmt.Errorf("template has no line starting with %q", prefix)
}

func indexLine(lines []string, want string) int {
	for i, line := range lines {
		if line == want {
			return i
		}
	}
	return -1
}

// renameWorkspace points the template workspace section at the workspace called name.
func renameWorkspace(lines []string, name string) []string {
	out := make([]string, len(lines))
	for i, line := range lines {
		out[i] = strings.ReplaceAll(line, "[workspaces.default", "[workspaces."+name)
	}
	return out
}

// tomlArray renders values as a single-line TOML string array.
func tomlArray(values []string) string {
	quoted := make([]string, len(values))
	for i, v := range values {
		quoted[i] = strconv.Quote(v)
	}
	return "[" + strings.Join(quoted, ", ") + "]"
}

// formatCPU renders a float as TOML, keeping a fractional part so 4 becomes 4.0.
func formatCPU(cpu float64) string {
	s := strconv.FormatFloat(cpu, 'f', -1, 64)
	if !strings.Contains(s, ".") {
		s += ".0"
	}
	return s
}

func firstTimeDefaults(opts FirstTimeOptions) []templateOption {
	var overrides []templateOption
	if opts.Agent != "" {
		overrides = append(overrides, templateOption{"agent", strconv.Quote(opts.Agent)})
	}
	if opts.SSHAuthSock {
		overrides = append(overrides, templateOption{"ssh_auth_sock", "true"})
	}
	if opts.GPGAgent {
		overrides = append(overrides, templateOption{"gpg_agent", "true"})
	}
	if !opts.GitConfig {
		overrides = append(overrides, templateOption{"git_config", "false"})
	}
	if opts.EnableDocker {
		overrides = append(overrides, templateOption{"enable_docker", "true"})
	}
	if opts.CPU > 0 && opts.CPU != 2.0 {
		overrides = append(overrides, templateOption{"cpu", formatCPU(opts.CPU)})
	}
	if opts.Memory != "" && !strings.EqualFold(opts.Memory, "4g") {
		overrides = append(overrides, templateOption{"memory", strconv.Quote(opts.Memory)})
	}
	return overrides
}

// applyDefaultRules swaps the dev-deps rule for the internet preset, or appends a custom rule after it.
func applyDefaultRules(head []string, opts FirstTimeOptions) ([]string, error) {
	start := indexLine(head, "[[defaults.network.rules]]")
	if start < 0 {
		return nil, errors.New("template has no dev-deps rule")
	}
	end := start + 1
	for end < len(head) && head[end] != "]" {
		end++
	}
	if end == len(head) {
		return nil, errors.New("template dev-deps rule is not closed")
	}
	switch {
	case opts.AllowInternet:
		internet := []string{
			"[[defaults.network.rules]]",
			`name = "internet"`,
			`allow = ["*"]  # explicit opt-in to the whole public internet`,
		}
		return slices.Concat(head[:start:start], internet, head[end+1:]), nil
	case len(opts.ExtraHosts) > 0:
		custom := []string{
			"",
			"[[defaults.network.rules]]",
			`name = "custom"`,
			"allow = " + tomlArray(opts.ExtraHosts),
		}
		return slices.Concat(head[:end+1], custom, head[end+1:]), nil
	default:
		return head, nil
	}
}

func RenderFirstTime(opts FirstTimeOptions) ([]byte, error) {
	head, workspace := splitTemplate()
	for _, o := range firstTimeDefaults(opts) {
		if err := activateOption(head, o.key, o.value); err != nil {
			return nil, fmt.Errorf("set defaults.%s: %w", o.key, err)
		}
	}
	head, err := applyDefaultRules(head, opts)
	if err != nil {
		return nil, fmt.Errorf("set defaults.network.rules: %w", err)
	}

	wsName := opts.WorkspaceName
	if wsName == "" {
		wsName = "default"
	}
	var paths []string
	if opts.WorkspacePath != "" {
		paths = []string{opts.WorkspacePath}
	}
	workspace = renameWorkspace(workspace, wsName)
	if err := replaceLine(workspace, "paths = ", "paths = "+tomlArray(paths)); err != nil {
		return nil, fmt.Errorf("set workspaces.%s.paths: %w", wsName, err)
	}

	out := strings.Join(slices.Concat(head, workspace), "\n")
	if err := validateBytes([]byte(out)); err != nil {
		return nil, err
	}
	return []byte(out), nil
}

// applyWorkspaceRules adds the active network rules of a new workspace and keeps the commented internet example when it is not used.
func applyWorkspaceRules(lines []string, opts WorkspaceOptions) ([]string, error) {
	header := "[[workspaces." + opts.Name + ".network.rules]]"
	var rules []string
	if opts.AllowInternet {
		rules = append(rules, header, `name = "internet"`, `allow = ["*"]  # explicit opt-in to the whole public internet`)
	}
	if len(opts.ExtraHosts) > 0 {
		rules = append(rules, header, `name = "custom"`, "allow = "+tomlArray(opts.ExtraHosts))
	}
	if len(rules) == 0 {
		return lines, nil
	}
	at := indexLine(lines, "# "+header)
	if at < 0 {
		return nil, errors.New("template has no workspace network rule")
	}
	if opts.AllowInternet {
		return slices.Concat(lines[:at:at], rules, lines[at+3:]), nil
	}
	return slices.Concat(lines[:at:at], rules, lines[at:]), nil
}

// newWorkspaceSection renders a complete [workspaces.<name>] section from the template.
func newWorkspaceSection(opts WorkspaceOptions) (string, error) {
	_, template := splitTemplate()
	lines := renameWorkspace(template, opts.Name)
	if err := replaceLine(lines, "paths = ", "paths = "+tomlArray(opts.Paths)); err != nil {
		return "", err
	}
	var overrides []templateOption
	if opts.Agent != "" {
		overrides = append(overrides, templateOption{"agent", strconv.Quote(opts.Agent)})
	}
	if opts.EnableDocker != nil {
		overrides = append(overrides, templateOption{"enable_docker", strconv.FormatBool(*opts.EnableDocker)})
	}
	for _, o := range overrides {
		if err := activateOption(lines, o.key, o.value); err != nil {
			return "", err
		}
	}
	lines, err := applyWorkspaceRules(lines, opts)
	if err != nil {
		return "", err
	}
	return strings.Join(lines, "\n"), nil
}

func appendWorkspace(raw []byte, opts WorkspaceOptions) ([]byte, error) {
	section, err := newWorkspaceSection(opts)
	if err != nil {
		return nil, fmt.Errorf("add workspace %q: %w", opts.Name, err)
	}
	text := string(raw)
	if text != "" && !strings.HasSuffix(text, "\n") {
		text += "\n"
	}
	return []byte(text + "\n" + section), nil
}

// updateWorkspace rewrites an existing workspace, keeping unrelated settings, and comments the agent and enable_docker lines.
func updateWorkspace(doc *edit.Document, opts WorkspaceOptions) ([]byte, error) {
	wsKey := []string{"workspaces", opts.Name}
	if err := doc.Set(append(wsKey, "paths"), opts.Paths); err != nil {
		return nil, fmt.Errorf("set workspace %q paths: %w", opts.Name, err)
	}
	if opts.Agent != "" {
		if err := doc.Set(append(wsKey, "agent"), opts.Agent); err != nil {
			return nil, fmt.Errorf("set workspace %q agent: %w", opts.Name, err)
		}
		if err := doc.SetTrailingComment(append(wsKey, "agent"), agentComment); err != nil {
			return nil, fmt.Errorf("comment workspace %q agent: %w", opts.Name, err)
		}
	} else {
		doc.Delete(append(wsKey, "agent"))
	}
	if opts.EnableDocker != nil {
		if err := doc.Set(append(wsKey, "enable_docker"), *opts.EnableDocker); err != nil {
			return nil, fmt.Errorf("set workspace %q enable_docker: %w", opts.Name, err)
		}
		if err := doc.SetTrailingComment(append(wsKey, "enable_docker"), dockerComment); err != nil {
			return nil, fmt.Errorf("comment workspace %q enable_docker: %w", opts.Name, err)
		}
	} else {
		doc.Delete(append(wsKey, "enable_docker"))
	}
	doc.Delete(append(wsKey, "network", "rules"))
	if opts.AllowInternet || len(opts.ExtraHosts) > 0 {
		var rules []map[string]any
		if opts.AllowInternet {
			rules = append(rules, map[string]any{"name": "internet", "allow": []string{"*"}})
		}
		if len(opts.ExtraHosts) > 0 {
			rules = append(rules, map[string]any{"name": "custom", "allow": opts.ExtraHosts})
		}
		if err := doc.Set(append(wsKey, "network", "rules"), rules); err != nil {
			return nil, fmt.Errorf("set workspace %q network.rules: %w", opts.Name, err)
		}
	}
	return doc.Bytes(), nil
}

func RenderWorkspace(raw []byte, opts WorkspaceOptions) ([]byte, error) {
	if err := ValidateWorkspaceName(opts.Name); err != nil {
		return nil, err
	}
	doc, err := edit.Parse(raw)
	if err != nil {
		return nil, fmt.Errorf("parse config: %w", err)
	}

	var out []byte
	if doc.Has([]string{"workspaces", opts.Name}) {
		out, err = updateWorkspace(doc, opts)
	} else {
		out, err = appendWorkspace(raw, opts)
	}
	if err != nil {
		return nil, err
	}
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
