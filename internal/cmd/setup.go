package cmd

import (
	"bufio"
	"cmp"
	"context"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

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
		ctx := cmd.Context()
		res, err := runSetup(ctx, newStdinPrompter(setupYes), setupOptions{
			configPath: config.ConfigPath(),
			targetDir:  targetDir,
			dryRun:     setupDryRun,
			askStart:   true,
		})
		if err != nil || !res.startNow {
			return err
		}
		cfg, err := config.Load()
		if err != nil {
			return fmt.Errorf("load config after setup: %w", err)
		}
		ws, err := workspace.Resolve(cfg, res.name)
		if err != nil {
			return fmt.Errorf("resolve workspace %q: %w", res.name, err)
		}
		detachKeys, err := config.ParseDetachKeys(cmp.Or(detachKeysFlag, cfg.DetachKeys, config.DefaultDetachKeys))
		if err != nil {
			return err
		}
		if err := runUpVM(ctx, cfg, ws); err != nil {
			return err
		}
		return runSessionVM(ctx, cfg, ws, vmSessionOptions{
			dir: res.dir, detach: detachKeys,
		})
	},
}

func init() {
	setupCmd.Flags().BoolVar(&setupDryRun, "dry-run", false, "show the generated configuration without writing it")
	setupCmd.Flags().BoolVarP(&setupYes, "yes", "y", false, "accept the default answer to every question")
	rootCmd.AddCommand(setupCmd)
}

type setupOptions struct {
	configPath string
	targetDir  string
	// defaultName seeds the workspace name question; empty falls back to
	// --workspace, then to the mode's own default.
	defaultName string
	dryRun      bool
	// askStart asks whether to start the workspace after saving. Callers
	// that start a workspace themselves leave it off.
	askStart bool
}

type setupResult struct {
	name     string
	dir      string
	startNow bool
}

type wizardPrompter struct {
	ctx       context.Context
	in        *bufio.Reader
	out       io.Writer
	yes       bool
	explained bool
	// interrupt sets a read deadline on the input to unblock a pending read
	// when ctx is cancelled; nil leaves the read running.
	interrupt func(time.Time) error
}

func newStdinPrompter(yes bool) *wizardPrompter {
	return &wizardPrompter{
		in:        bufio.NewReader(os.Stdin),
		out:       os.Stdout,
		yes:       yes,
		interrupt: os.Stdin.SetReadDeadline,
	}
}

func (p *wizardPrompter) explain(lines ...string) {
	if p.yes || len(lines) == 0 {
		return
	}
	if p.explained {
		_, _ = fmt.Fprintln(p.out)
	}
	p.explained = true
	faint := color.New(color.Faint)
	for _, line := range lines {
		_, _ = faint.Fprintf(p.out, "  %s\n", line)
	}
}

func (p *wizardPrompter) readLine() (string, error) {
	type result struct {
		line string
		err  error
	}
	ch := make(chan result, 1)
	go func() {
		line, err := p.in.ReadString('\n')
		ch <- result{line, err}
	}()
	select {
	case <-p.ctx.Done():
		if p.interrupt != nil && p.interrupt(time.Now()) == nil {
			<-ch
			_ = p.interrupt(time.Time{})
		}
		return "", p.ctx.Err()
	case r := <-ch:
		return r.line, r.err
	}
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
		if err := p.ctx.Err(); err != nil {
			return 0, err
		}
		_, _ = fmt.Fprint(p.out, prompt)
		if p.yes {
			_, _ = fmt.Fprintln(p.out, options[def])
			return def, nil
		}
		line, err := p.readLine()
		if p.ctx.Err() != nil {
			return 0, p.ctx.Err()
		}
		answer := strings.ToLower(strings.TrimSpace(line))
		if answer == "" && err != nil {
			return 0, fmt.Errorf("read answer: %w", err)
		}
		if answer == "" {
			return def, nil
		}
		if i, ok := matchOption(answer, options); ok {
			return i, nil
		}
		_, _ = fmt.Fprintf(p.out, "  answer one of: %s\n", strings.Join(options, ", "))
	}
}

// matchOption returns the option equal to answer (case-insensitive) or, for
// a single-letter answer, the only option starting with that letter.
func matchOption(answer string, options []string) (int, bool) {
	for i, o := range options {
		if answer == strings.ToLower(o) {
			return i, true
		}
	}
	if len(answer) != 1 {
		return 0, false
	}
	match := -1
	for i, o := range options {
		if strings.HasPrefix(strings.ToLower(o), answer) {
			if match >= 0 {
				return 0, false
			}
			match = i
		}
	}
	return match, match >= 0
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
		if err := p.ctx.Err(); err != nil {
			return "", err
		}
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
		line, err := p.readLine()
		if p.ctx.Err() != nil {
			return "", p.ctx.Err()
		}
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

// workspaceDir asks for the workspace directory and, like `jailoc add`,
// confirms a path that shares the whole home directory or filesystem. With
// --yes such a path is refused instead of confirmed.
func (p *wizardPrompter) workspaceDir(question, def string) (string, error) {
	for {
		answer, err := p.input(question, def, validateWorkspaceDir)
		if err != nil {
			return "", err
		}
		dir, err := normalizeSetupDir(answer)
		if err != nil {
			return "", err
		}
		if !isBroadPath(dir) {
			return dir, nil
		}
		if p.yes {
			return "", fmt.Errorf("%q is a very broad path; run setup interactively or pass a narrower directory", dir)
		}
		_, _ = color.New(color.FgYellow).Fprintf(p.out, "WARNING: %q is a very broad path — this will share your entire directory tree with the VM.\n", dir)
		ok, err := p.confirm("Are you sure?", false)
		if err != nil {
			return "", err
		}
		if ok {
			return dir, nil
		}
		def = ""
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

func runSetup(ctx context.Context, p *wizardPrompter, opts setupOptions) (setupResult, error) {
	p.ctx = ctx
	if err := ctx.Err(); err != nil {
		return setupResult{}, err
	}
	isDefault, err := config.IsDefaultFile(opts.configPath)
	if err != nil {
		return setupResult{}, err
	}
	if isDefault {
		return runFirstTimeSetup(p, opts)
	}
	return runWorkspaceSetup(p, opts)
}

func seededWorkspaceName(opts setupOptions, fallback string) string {
	if opts.defaultName != "" {
		return opts.defaultName
	}
	if workspaceExplicit && workspaceFlag != "" {
		return workspaceFlag
	}
	return fallback
}

func runFirstTimeSetup(p *wizardPrompter, opts setupOptions) (setupResult, error) {
	_, _ = color.New(color.FgCyan, color.Bold).Fprintf(p.out, "Welcome to jailoc! Let's configure %s.\n\n", opts.configPath)
	p.explained = false

	agentOptions := []string{"opencode", "omo", "claude", "copilot", "agy", "opencode1"}
	defAgent := detectPreferredAgent()
	defAgentIdx := max(slices.Index(agentOptions, defAgent), 0)
	p.explain(
		"Agent installed in the VM and started by \"jailoc\".",
		"Your host config for the agent is copied in read-only (import_agent_config).",
	)
	agentIdx, err := p.choose("Default coding agent", agentOptions, defAgentIdx)
	if err != nil {
		return setupResult{}, err
	}
	chosenAgent := agentOptions[agentIdx]

	p.explain(
		"Copies ~/.gitconfig and ~/.ssh/known_hosts into the VM as read-only snapshots.",
		"Needed for Git author identity and SSH host verification; cannot modify host files.",
	)
	gitCfg, err := p.confirm("Copy host Git configuration into the VM (git_config)?", true)
	if err != nil {
		return setupResult{}, err
	}

	sshDef := os.Getenv("SSH_AUTH_SOCK") != ""
	p.explain(
		"Lets git push/pull over SSH inside the VM using your host keys.",
		"Private keys stay on the host, but processes in the VM can use them while it runs.",
	)
	sshAuth, err := p.confirm("Forward host SSH agent into the VM (ssh_auth_sock)?", sshDef)
	if err != nil {
		return setupResult{}, err
	}

	p.explain(
		"Lets Git sign commits and tags inside the VM using your host GPG keys.",
		"Private keys stay on the host, but the VM can request signatures while running.",
	)
	gpgAgent, err := p.confirm("Forward host GPG agent into the VM (gpg_agent)?", false)
	if err != nil {
		return setupResult{}, err
	}

	p.explain(
		"\"dev-deps\" allows only GitHub/GitLab, package registries, and Linux mirrors.",
		"\"internet\" allows all public IPs. Private/LAN networks are blocked in both modes.",
	)
	netIdx, err := p.choose("Default network egress policy", []string{"dev-deps", "internet"}, 0)
	if err != nil {
		return setupResult{}, err
	}
	allowInternet := netIdx == 1

	var extraHosts []string
	if !allowInternet {
		p.explain(
			"Add internal services or extra hosts the agent needs to reach (e.g. gitlab.corp.example, 10.0.0.0/8).",
			"Leave empty if public package registries and GitHub/GitLab are enough.",
		)
		hostsInput, err := p.input("Extra allowed hostnames or CIDRs (comma-separated, optional)", "", validateHostsCSV)
		if err != nil {
			return setupResult{}, err
		}
		extraHosts = splitCSV(hostsInput)
	}

	p.explain(
		"Runs an isolated Docker daemon inside the VM (never mounts the host Docker socket).",
		"Needed for building containers or testcontainers; increases image size and disk use.",
	)
	enableDocker, err := p.confirm("Enable Docker inside the VM by default (enable_docker)?", false)
	if err != nil {
		return setupResult{}, err
	}

	p.explain("Virtual CPU cores allocated to each workspace VM (takes effect on VM start/restart).")
	cpuStr, err := p.input("Default VM CPUs", "2", validatePositiveFloat)
	if err != nil {
		return setupResult{}, err
	}
	cpuVal, _ := strconv.ParseFloat(cpuStr, 64)

	p.explain("Maximum RAM per workspace VM (e.g. 4g, 8g). Use 8g+ for heavy builds or Docker in the VM.")
	memVal, err := p.input("Default VM memory", "4g", config.ValidateMemoryFormat)
	if err != nil {
		return setupResult{}, err
	}

	p.explain("Short identifier used in CLI commands (e.g. \"jailoc up <name>\") and VM state paths.")
	wsName, err := p.input("Initial workspace name", seededWorkspaceName(opts, "default"), config.ValidateWorkspaceName)
	if err != nil {
		return setupResult{}, err
	}

	p.explain(
		"Host directory shared read-write into the VM at the same path.",
		"The agent can read and modify everything inside; other host directories stay hidden.",
	)
	wsDir, err := p.workspaceDir("Initial workspace directory path", opts.targetDir)
	if err != nil {
		return setupResult{}, err
	}

	rendered, err := config.RenderFirstTime(config.FirstTimeOptions{
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
		WorkspacePath: wsDir,
	})
	if err != nil {
		return setupResult{}, fmt.Errorf("build configuration: %w", err)
	}

	return saveSetup(p, opts, rendered, setupResult{name: wsName, dir: wsDir},
		fmt.Sprintf("\nWrite configuration to %s?", opts.configPath),
		fmt.Sprintf("Wrote %s (workspace %q -> %s)\n", opts.configPath, wsName, wsDir))
}

func runWorkspaceSetup(p *wizardPrompter, opts setupOptions) (setupResult, error) {
	cfg, err := config.LoadFrom(opts.configPath)
	if err != nil {
		return setupResult{}, err
	}
	raw, err := os.ReadFile(opts.configPath) //nolint:gosec // G304: configPath is the jailoc config file
	if err != nil {
		return setupResult{}, fmt.Errorf("read config %q: %w", opts.configPath, err)
	}

	_, _ = color.New(color.FgCyan, color.Bold).Fprintf(p.out, "Existing configuration found at %s. Let's configure a workspace.\n\n", opts.configPath)
	p.explained = false

	defName := seededWorkspaceName(opts, config.SanitizeWorkspaceName(filepath.Base(opts.targetDir)))
	p.explain("Short identifier for this workspace (lowercase letters, digits, hyphens).")
	wsName, err := p.input("Workspace name", defName, config.ValidateWorkspaceName)
	if err != nil {
		return setupResult{}, err
	}

	if existing, exists := cfg.Workspaces[wsName]; exists && len(existing.Paths) > 0 {
		overwrite, err := p.confirm(fmt.Sprintf("Workspace %q already exists. Update it?", wsName), true)
		if err != nil || !overwrite {
			if err == nil {
				_, _ = fmt.Fprintln(p.out, "Nothing changed.")
			}
			return setupResult{}, err
		}
	}

	p.explain("Host directory shared read-write into this workspace's VM. Everything outside stays isolated.")
	wsDir, err := p.workspaceDir("Workspace directory path", opts.targetDir)
	if err != nil {
		return setupResult{}, err
	}

	agentOptions := []string{"inherit", "opencode", "omo", "claude", "copilot", "agy", "opencode1"}
	p.explain("Choose \"inherit\" to use the [defaults] agent, or pick a specific agent for this workspace.")
	agentIdx, err := p.choose("Workspace coding agent", agentOptions, 0)
	if err != nil {
		return setupResult{}, err
	}
	wsAgent := ""
	if agentIdx > 0 {
		wsAgent = agentOptions[agentIdx]
	}

	p.explain(
		"Runs an isolated Docker daemon inside this VM (no host Docker access). \"inherit\" uses [defaults].",
	)
	dockerIdx, err := p.choose("Enable Docker in this workspace", []string{"inherit", "yes", "no"}, 0)
	if err != nil {
		return setupResult{}, err
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

	p.explain(
		"\"inherit\" uses [defaults] rules, \"internet\" allows all public IPs, \"custom\" adds workspace-specific hosts/CIDRs.",
		"Private networks remain blocked unless explicitly listed in custom rules.",
	)
	netIdx, err := p.choose("Workspace network policy", []string{"inherit", "internet", "custom"}, 0)
	if err != nil {
		return setupResult{}, err
	}
	allowInternet := netIdx == 1
	var extraHosts []string
	if netIdx == 2 {
		p.explain("Hostnames, wildcard domains (*.example.com), or CIDRs reachable from this workspace, in addition to [defaults].")
		hostsInput, err := p.input("Allowed hostnames or CIDRs for this workspace (comma-separated)", "", validateHostsCSV)
		if err != nil {
			return setupResult{}, err
		}
		extraHosts = splitCSV(hostsInput)
	}

	rendered, err := config.RenderWorkspace(raw, config.WorkspaceOptions{
		Name:          wsName,
		Paths:         []string{wsDir},
		Agent:         wsAgent,
		EnableDocker:  enableDocker,
		AllowInternet: allowInternet,
		ExtraHosts:    extraHosts,
	})
	if err != nil {
		return setupResult{}, fmt.Errorf("update configuration: %w", err)
	}

	return saveSetup(p, opts, rendered, setupResult{name: wsName, dir: wsDir},
		fmt.Sprintf("\nSave workspace %q to %s?", wsName, opts.configPath),
		fmt.Sprintf("Saved workspace %q (%s) in %s\n", wsName, wsDir, opts.configPath))
}

func saveSetup(p *wizardPrompter, opts setupOptions, rendered []byte, res setupResult, question, saved string) (setupResult, error) {
	if opts.dryRun {
		_, _ = fmt.Fprintf(p.out, "\nconfig.toml (dry run, nothing written):\n\n%s", rendered)
		return setupResult{}, nil
	}

	ok, err := p.confirm(question, true)
	if err != nil || !ok {
		if err == nil {
			_, _ = fmt.Fprintln(p.out, "Nothing changed.")
		}
		return setupResult{}, err
	}
	if err := p.ctx.Err(); err != nil {
		return setupResult{}, err
	}

	if err := config.WriteBytes(opts.configPath, rendered); err != nil {
		return setupResult{}, err
	}
	_, _ = color.New(color.FgGreen).Fprint(p.out, saved)

	if !opts.askStart || p.yes {
		return res, nil
	}
	res.startNow, err = p.confirm(fmt.Sprintf("Start workspace %q now?", res.name), false)
	if err != nil {
		return setupResult{}, err
	}
	return res, nil
}

// maybeOfferFirstTimeSetup offers the setup wizard to a command that is about
// to start a workspace, when the configuration is missing or still the
// default. seedName seeds the workspace name. It returns the workspace name
// and directory the wizard configured, or zero values when it did not run.
func maybeOfferFirstTimeSetup(ctx context.Context, targetDir, seedName string) (setupResult, error) {
	if !term.IsTerminal(int(os.Stdin.Fd())) || !term.IsTerminal(int(os.Stdout.Fd())) { //nolint:gosec // G115: Fd fits int on 64-bit platforms
		return setupResult{}, nil
	}
	configPath := config.ConfigPath()
	isDefault, err := config.IsDefaultFile(configPath)
	if err != nil || !isDefault {
		return setupResult{}, nil
	}
	p := newStdinPrompter(false)
	p.ctx = ctx
	runWizard, err := p.confirm("No customized jailoc configuration found. Run `jailoc setup` wizard now?", true)
	if err != nil || !runWizard {
		return setupResult{}, err
	}
	return runSetup(ctx, p, setupOptions{
		configPath:  configPath,
		targetDir:   targetDir,
		defaultName: seedName,
	})
}

func validatePositiveFloat(s string) error {
	v, err := strconv.ParseFloat(strings.TrimSpace(s), 64)
	if err != nil || math.IsNaN(v) || math.IsInf(v, 0) || v <= 0 {
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
