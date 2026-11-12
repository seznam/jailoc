package cmd

import (
	"bufio"
	"cmp"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"github.com/fatih/color"
	"github.com/seznam/jailoc/internal/agent"
	"github.com/seznam/jailoc/internal/config"
	"github.com/seznam/jailoc/internal/workspace"
	"github.com/spf13/cobra"
	"golang.org/x/term"
)

var (
	setupDryRun bool
	setupYes    bool
)

var setupCmd = &cobra.Command{
	Use:     "setup [path]",
	Aliases: []string{"wizard"},
	Short:   "Interactive onboarding and workspace setup wizard",
	Long: `Configure jailoc interactively.

When run for the first time (no config.toml or still unmodified defaults),
setup walks through global defaults (coding agent, Git/SSH/GPG forwarding,
network policy, Docker in the VM, CPU/memory limits) and configures the
initial workspace.

When run with an existing customized config.toml, setup adds or updates a
workspace interactively.`,
	Args: cobra.MaximumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		interactive := term.IsTerminal(int(os.Stdin.Fd())) //nolint:gosec // G115: Fd fits int on 64-bit platforms
		if !interactive && !setupYes {
			return errors.New("setup asks questions; run it in a terminal or pass --yes to accept the defaults")
		}
		targetDir, err := resolveTargetPath(args)
		if err != nil {
			return fmt.Errorf("resolve target directory: %w", err)
		}
		w := &wizardPrompter{in: bufio.NewReader(os.Stdin), out: os.Stdout, yes: setupYes}
		wsName, startNow, err := runSetup(cmd.Context(), config.ConfigPath(), targetDir, w, setupDryRun)
		if err != nil || !startNow || setupDryRun {
			return err
		}
		cfg, err := config.Load()
		if err != nil {
			return fmt.Errorf("load config after setup: %w", err)
		}
		ws, err := workspace.Resolve(cfg, wsName)
		if err != nil {
			return fmt.Errorf("resolve workspace %q: %w", wsName, err)
		}
		detachKeys, err := config.ParseDetachKeys(cmp.Or(detachKeysFlag, cfg.DetachKeys, config.DefaultDetachKeys))
		if err != nil {
			return err
		}
		return runSessionVM(cmd.Context(), cfg, ws, vmSessionOptions{
			dir: targetDir, detach: detachKeys,
		})
	},
}

func init() {
	setupCmd.Flags().BoolVar(&setupDryRun, "dry-run", false, "show the generated configuration without writing it")
	setupCmd.Flags().BoolVarP(&setupYes, "yes", "y", false, "accept the default answer to every question")
	rootCmd.AddCommand(setupCmd)
}

type wizardPrompter struct {
	in  *bufio.Reader
	out io.Writer
	yes bool
}

func (p *wizardPrompter) choose(question string, options []string, def int) (int, error) {
	labels := make([]string, len(options))
	for i, o := range options {
		labels[i] = o
		if i == def && len(o) > 0 {
			labels[i] = strings.ToUpper(o[:1]) + o[1:]
		}
	}
	prompt := fmt.Sprintf("%s [%s] ", question, strings.Join(labels, "/"))
	for {
		_, _ = fmt.Fprint(p.out, prompt)
		if p.yes {
			_, _ = fmt.Fprintln(p.out, options[def])
			return def, nil
		}
		line, err := p.in.ReadString('\n')
		answer := strings.ToLower(strings.TrimSpace(line))
		if answer == "" && err != nil {
			return 0, fmt.Errorf("read answer: %w", err)
		}
		if answer == "" {
			return def, nil
		}
		for i, o := range options {
			low := strings.ToLower(o)
			if answer == low || (len(low) > 0 && answer == low[:1]) {
				return i, nil
			}
		}
		_, _ = fmt.Fprintf(p.out, "  answer one of: %s\n", strings.Join(options, ", "))
	}
}

func (p *wizardPrompter) confirm(question string, def bool) (bool, error) {
	options := []string{"yes", "no"}
	d := 1
	if def {
		d = 0
	}
	i, err := p.choose(question, options, d)
	return i == 0, err
}

func (p *wizardPrompter) input(question, def string, validate func(string) error) (string, error) {
	prompt := fmt.Sprintf("%s: ", question)
	if def != "" {
		prompt = fmt.Sprintf("%s [%s]: ", question, def)
	}
	for {
		_, _ = fmt.Fprint(p.out, prompt)
		if p.yes {
			_, _ = fmt.Fprintln(p.out, def)
			if validate != nil {
				if err := validate(def); err != nil {
					return "", err
				}
			}
			return def, nil
		}
		line, err := p.in.ReadString('\n')
		answer := strings.TrimSpace(line)
		if answer == "" && err != nil {
			return "", fmt.Errorf("read input: %w", err)
		}
		if answer == "" {
			answer = def
		}
		if validate != nil {
			if err := validate(answer); err != nil {
				_, _ = fmt.Fprintf(p.out, "  %v\n", err)
				continue
			}
		}
		return answer, nil
	}
}

func detectPreferredAgent() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return agent.Default
	}
	candidates := []struct {
		name string
		path string
	}{
		{"opencode", filepath.Join(home, ".config", "opencode")},
		{"omo", filepath.Join(home, ".omo")},
		{"claude", filepath.Join(home, ".claude")},
		{"copilot", filepath.Join(home, ".copilot")},
		{"agy", filepath.Join(home, ".gemini")},
	}
	for _, c := range candidates {
		if _, err := os.Stat(c.path); err == nil {
			return c.name
		}
	}
	return agent.Default
}

func runSetup(ctx context.Context, configPath, targetDir string, p *wizardPrompter, dryRun bool) (string, bool, error) {
	if err := ctx.Err(); err != nil {
		return "", false, err
	}
	isDefault, err := config.IsDefaultFile(configPath)
	if err != nil {
		return "", false, err
	}
	if isDefault {
		return runFirstTimeSetup(configPath, targetDir, p, dryRun)
	}
	return runWorkspaceSetup(configPath, targetDir, p, dryRun)
}

func runFirstTimeSetup(configPath, targetDir string, p *wizardPrompter, dryRun bool) (string, bool, error) {
	_, _ = color.New(color.FgCyan, color.Bold).Fprintf(p.out, "Welcome to jailoc! Let's configure %s.\n\n", configPath)

	agentOptions := []string{"opencode", "omo", "claude", "copilot", "agy", "opencode1"}
	defAgent := detectPreferredAgent()
	defAgentIdx := max(slices.Index(agentOptions, defAgent), 0)
	agentIdx, err := p.choose("Default coding agent", agentOptions, defAgentIdx)
	if err != nil {
		return "", false, err
	}
	chosenAgent := agentOptions[agentIdx]

	gitCfg, err := p.confirm("Copy host Git configuration into the VM (git_config)?", true)
	if err != nil {
		return "", false, err
	}

	sshDef := os.Getenv("SSH_AUTH_SOCK") != ""
	sshAuth, err := p.confirm("Forward host SSH agent into the VM (ssh_auth_sock)?", sshDef)
	if err != nil {
		return "", false, err
	}

	gpgAgent, err := p.confirm("Forward host GPG agent into the VM (gpg_agent)?", false)
	if err != nil {
		return "", false, err
	}

	netIdx, err := p.choose("Default network egress policy", []string{"dev-deps", "internet"}, 0)
	if err != nil {
		return "", false, err
	}
	allowInternet := netIdx == 1

	var extraHosts []string
	if !allowInternet {
		hostsInput, err := p.input("Extra allowed hostnames or CIDRs (comma-separated, optional)", "", validateHostsCSV)
		if err != nil {
			return "", false, err
		}
		extraHosts = splitCSV(hostsInput)
	}

	enableDocker, err := p.confirm("Enable Docker inside the VM by default (enable_docker)?", false)
	if err != nil {
		return "", false, err
	}

	cpuStr, err := p.input("Default VM CPUs", "2", validatePositiveFloat)
	if err != nil {
		return "", false, err
	}
	cpuVal, _ := strconv.ParseFloat(cpuStr, 64)

	memVal, err := p.input("Default VM memory", "4g", config.ValidateMemoryFormat)
	if err != nil {
		return "", false, err
	}

	defWSName := "default"
	if workspaceExplicit && workspaceFlag != "" {
		defWSName = workspaceFlag
	}
	wsName, err := p.input("Initial workspace name", defWSName, config.ValidateWorkspaceName)
	if err != nil {
		return "", false, err
	}

	wsPath, err := p.input("Initial workspace directory path", targetDir, validateWorkspaceDir)
	if err != nil {
		return "", false, err
	}
	resolvedWSPath, err := normalizeSetupDir(wsPath)
	if err != nil {
		return "", false, err
	}

	opts := config.FirstTimeOptions{
		Agent:         chosenAgent,
		GitConfig:     gitCfg,
		SSHAuthSock:   sshAuth,
		GPGAgent:      gpgAgent,
		EnableDocker:  enableDocker,
		CPU:           cpuVal,
		Memory:        memVal,
		AllowInternet: allowInternet,
		ExtraHosts:    extraHosts,
		WorkspaceName: wsName,
		WorkspacePath: resolvedWSPath,
	}

	rendered, err := config.RenderFirstTime(opts)
	if err != nil {
		return "", false, fmt.Errorf("build configuration: %w", err)
	}

	if dryRun {
		_, _ = fmt.Fprintf(p.out, "\nGenerated config.toml (dry run, nothing written):\n\n%s", rendered)
		return wsName, false, nil
	}

	ok, err := p.confirm(fmt.Sprintf("\nWrite configuration to %s?", configPath), true)
	if err != nil || !ok {
		if err == nil {
			_, _ = fmt.Fprintln(p.out, "Nothing changed.")
		}
		return "", false, err
	}

	if err := config.WriteBytes(configPath, rendered); err != nil {
		return "", false, err
	}
	_, _ = color.New(color.FgGreen).Fprintf(p.out, "Wrote %s (workspace %q -> %s)\n", configPath, wsName, resolvedWSPath)

	if p.yes {
		return wsName, false, nil
	}
	startNow, err := p.confirm(fmt.Sprintf("Start workspace %q now?", wsName), false)
	if err != nil {
		return wsName, false, err
	}
	return wsName, startNow, nil
}

func runWorkspaceSetup(configPath, targetDir string, p *wizardPrompter, dryRun bool) (string, bool, error) {
	cfg, err := config.LoadFrom(configPath)
	if err != nil {
		return "", false, err
	}
	raw, err := os.ReadFile(configPath) //nolint:gosec // G304: configPath is the jailoc config file
	if err != nil {
		return "", false, fmt.Errorf("read config %q: %w", configPath, err)
	}

	_, _ = color.New(color.FgCyan, color.Bold).Fprintf(p.out, "Existing configuration found at %s. Let's configure a workspace.\n\n", configPath)

	defName := config.SanitizeWorkspaceName(filepath.Base(targetDir))
	if workspaceExplicit && workspaceFlag != "" {
		defName = workspaceFlag
	}
	wsName, err := p.input("Workspace name", defName, config.ValidateWorkspaceName)
	if err != nil {
		return "", false, err
	}

	if existing, exists := cfg.Workspaces[wsName]; exists && len(existing.Paths) > 0 {
		overwrite, err := p.confirm(fmt.Sprintf("Workspace %q already exists. Update it?", wsName), true)
		if err != nil || !overwrite {
			if err == nil {
				_, _ = fmt.Fprintln(p.out, "Nothing changed.")
			}
			return "", false, err
		}
	}

	wsPath, err := p.input("Workspace directory path", targetDir, validateWorkspaceDir)
	if err != nil {
		return "", false, err
	}
	resolvedWSPath, err := normalizeSetupDir(wsPath)
	if err != nil {
		return "", false, err
	}

	agentOptions := []string{"inherit", "opencode", "omo", "claude", "copilot", "agy", "opencode1"}
	agentIdx, err := p.choose("Workspace coding agent", agentOptions, 0)
	if err != nil {
		return "", false, err
	}
	wsAgent := ""
	if agentIdx > 0 {
		wsAgent = agentOptions[agentIdx]
	}

	dockerIdx, err := p.choose("Enable Docker in this workspace", []string{"inherit", "yes", "no"}, 0)
	if err != nil {
		return "", false, err
	}
	var enableDocker *bool
	switch dockerIdx {
	case 1:
		v := true
		enableDocker = &v
	case 2:
		v := false
		enableDocker = &v
	}

	netIdx, err := p.choose("Workspace network policy", []string{"inherit", "internet", "custom"}, 0)
	if err != nil {
		return "", false, err
	}
	allowInternet := netIdx == 1
	var extraHosts []string
	if netIdx == 2 {
		hostsInput, err := p.input("Allowed hostnames or CIDRs for this workspace (comma-separated)", "", validateHostsCSV)
		if err != nil {
			return "", false, err
		}
		extraHosts = splitCSV(hostsInput)
	}

	opts := config.WorkspaceOptions{
		Name:          wsName,
		Paths:         []string{resolvedWSPath},
		Agent:         wsAgent,
		EnableDocker:  enableDocker,
		AllowInternet: allowInternet,
		ExtraHosts:    extraHosts,
	}

	rendered, err := config.RenderWorkspace(raw, opts)
	if err != nil {
		return "", false, fmt.Errorf("update configuration: %w", err)
	}

	if dryRun {
		_, _ = fmt.Fprintf(p.out, "\nUpdated config.toml (dry run, nothing written):\n\n%s", rendered)
		return wsName, false, nil
	}

	ok, err := p.confirm(fmt.Sprintf("\nSave workspace %q to %s?", wsName, configPath), true)
	if err != nil || !ok {
		if err == nil {
			_, _ = fmt.Fprintln(p.out, "Nothing changed.")
		}
		return "", false, err
	}

	if err := config.WriteBytes(configPath, rendered); err != nil {
		return "", false, err
	}
	_, _ = color.New(color.FgGreen).Fprintf(p.out, "Saved workspace %q (%s) in %s\n", wsName, resolvedWSPath, configPath)

	if p.yes {
		return wsName, false, nil
	}
	startNow, err := p.confirm(fmt.Sprintf("Start workspace %q now?", wsName), false)
	if err != nil {
		return wsName, false, err
	}
	return wsName, startNow, nil
}

func maybeOfferFirstTimeSetup(ctx context.Context, targetDir string) error {
	if !term.IsTerminal(int(os.Stdin.Fd())) || !term.IsTerminal(int(os.Stdout.Fd())) { //nolint:gosec // G115: Fd fits int on 64-bit platforms
		return nil
	}
	configPath := config.ConfigPath()
	isDefault, err := config.IsDefaultFile(configPath)
	if err != nil || !isDefault {
		return nil
	}
	w := &wizardPrompter{in: bufio.NewReader(os.Stdin), out: os.Stdout}
	runWizard, err := w.confirm("No customized jailoc configuration found. Run `jailoc setup` wizard now?", true)
	if err != nil || !runWizard {
		return err
	}
	wsName, _, err := runFirstTimeSetup(configPath, targetDir, w, false)
	if err != nil {
		return err
	}
	if !workspaceExplicit && wsName != "" {
		workspaceFlag = wsName
	}
	return nil
}

func validatePositiveFloat(s string) error {
	v, err := strconv.ParseFloat(strings.TrimSpace(s), 64)
	if err != nil || v <= 0 {
		return fmt.Errorf("CPU must be a positive number (e.g. 2 or 4)")
	}
	return nil
}

func validateHostsCSV(s string) error {
	for _, item := range splitCSV(s) {
		if _, _, err := config.ParseRuleTarget(item); err != nil {
			return err
		}
	}
	return nil
}

func splitCSV(s string) []string {
	var out []string
	for _, part := range strings.Split(s, ",") {
		trimmed := strings.TrimSpace(part)
		if trimmed != "" {
			out = append(out, trimmed)
		}
	}
	return out
}

func validateWorkspaceDir(p string) error {
	_, err := normalizeSetupDir(p)
	return err
}

func normalizeSetupDir(p string) (string, error) {
	if strings.TrimSpace(p) == "" {
		return "", errors.New("directory path must not be empty")
	}
	expanded, err := config.ExpandPath(strings.TrimSpace(p))
	if err != nil {
		return "", err
	}
	abs, err := filepath.Abs(expanded)
	if err != nil {
		return "", fmt.Errorf("resolve path %q: %w", expanded, err)
	}
	info, err := os.Stat(abs)
	if err != nil {
		if os.IsNotExist(err) {
			return "", fmt.Errorf("path %q does not exist", abs)
		}
		return "", fmt.Errorf("stat path %q: %w", abs, err)
	}
	if !info.IsDir() {
		return "", fmt.Errorf("path %q is not a directory", abs)
	}
	return abs, nil
}
