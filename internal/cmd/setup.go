package cmd

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"github.com/charmbracelet/huh"
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
setup walks through global defaults (coding agent, model providers, Git/SSH/GPG
forwarding, network policy, Docker in the VM, CPU/memory limits) and configures
the initial workspace.

When run with an existing customized config.toml, setup adds or updates a
workspace interactively.`,
	Args: cobra.MaximumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		interactive := term.IsTerminal(int(os.Stdin.Fd())) //nolint:gosec // G115: Fd fits int on 64-bit platforms
		if !interactive && !setupYes && !isAccessibleEnv() {
			return errors.New("setup asks questions; run it in a terminal or pass --yes to accept the defaults")
		}
		targetDir, err := resolveTargetPath(args)
		if err != nil {
			return fmt.Errorf("resolve target directory: %w", err)
		}
		ctx := cmd.Context()
		res, err := runSetup(ctx, newStdinPromptIO(setupYes), setupOptions{
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

var setupAgentOptions = []string{"opencode", "omo", "claude", "copilot", "agy", "opencode1"}

func agentSelectOptions(includeInherit bool) []huh.Option[string] {
	var opts []huh.Option[string]
	if includeInherit {
		opts = append(opts, huh.NewOption("inherit (use [defaults] agent)", "inherit"))
	}
	for _, name := range setupAgentOptions {
		label := name
		if h, ok := agent.Lookup(name); ok && h.Title != "" {
			label = fmt.Sprintf("%s (%s)", name, h.Title)
		}
		opts = append(opts, huh.NewOption(label, name))
	}
	return opts
}

// firstTimeAnswers holds the answers for first-time onboarding.
type firstTimeAnswers struct {
	agent        string
	providers    []string
	gitConfig    bool
	sshAuthSock  bool
	gpgAgent     bool
	networkMode  string // "dev-deps" or "internet"
	extraHosts   string
	enableDocker bool
	cpu          string
	memory       string
	wsName       string
	wsDir        string
	confirmBroad bool
	save         bool
	startNow     bool
}

func defaultFirstTimeAnswers(opts setupOptions) firstTimeAnswers {
	defAgent := detectPreferredAgent()
	if !slices.Contains(setupAgentOptions, defAgent) {
		defAgent = agent.Default
	}
	var providers []string
	if h, ok := agent.Lookup("opencode"); ok {
		providers = h.ProviderNames()
	}
	return firstTimeAnswers{
		agent:        defAgent,
		providers:    providers,
		gitConfig:    true,
		sshAuthSock:  os.Getenv("SSH_AUTH_SOCK") != "",
		gpgAgent:     false,
		networkMode:  "dev-deps",
		extraHosts:   "",
		enableDocker: false,
		cpu:          "2",
		memory:       "4g",
		wsName:       seededWorkspaceName(opts, "default"),
		wsDir:        opts.targetDir,
		save:         true,
		startNow:     false,
	}
}

func (a firstTimeAnswers) toOptions() (config.FirstTimeOptions, string, error) {
	if err := validatePositiveFloat(a.cpu); err != nil {
		return config.FirstTimeOptions{}, "", err
	}
	cpuVal, _ := strconv.ParseFloat(strings.TrimSpace(a.cpu), 64)
	if err := config.ValidateMemoryFormat(a.memory); err != nil {
		return config.FirstTimeOptions{}, "", err
	}
	if err := config.ValidateWorkspaceName(a.wsName); err != nil {
		return config.FirstTimeOptions{}, "", err
	}
	wsDir, err := normalizeSetupDir(a.wsDir)
	if err != nil {
		return config.FirstTimeOptions{}, "", err
	}
	if isBroadPath(wsDir) && !a.confirmBroad {
		return config.FirstTimeOptions{}, "", fmt.Errorf("%q is a very broad path and sharing it was not confirmed; run setup again and pick a narrower directory", wsDir)
	}
	allowInternet := a.networkMode == "internet"
	var extraHosts []string
	if !allowInternet {
		if err := validateHostsCSV(a.extraHosts); err != nil {
			return config.FirstTimeOptions{}, "", err
		}
		extraHosts = splitCSV(a.extraHosts)
	}
	var providers []string
	if h, ok := agent.Lookup(a.agent); ok && h.ExplicitEndpoints {
		providers = normalizeProviders(h, a.providers)
	}
	return config.FirstTimeOptions{
		Agent:         a.agent,
		Providers:     providers,
		GitConfig:     a.gitConfig,
		SSHAuthSock:   a.sshAuthSock,
		GPGAgent:      a.gpgAgent,
		EnableDocker:  a.enableDocker,
		CPU:           cpuVal,
		Memory:        strings.TrimSpace(a.memory),
		AllowInternet: allowInternet,
		ExtraHosts:    extraHosts,
		WorkspaceName: a.wsName,
		WorkspacePath: wsDir,
	}, wsDir, nil
}

func (a firstTimeAnswers) summary(configPath string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "  Config file:   %s\n", configPath)
	fmt.Fprintf(&b, "  Agent:         %s\n", a.agent)
	if h, ok := agent.Lookup(a.agent); ok && h.ExplicitEndpoints {
		p := normalizeProviders(h, a.providers)
		switch {
		case p == nil:
			fmt.Fprintf(&b, "  Providers:     all (%s)\n", strings.Join(h.ProviderNames(), ", "))
		case len(p) == 0:
			fmt.Fprintf(&b, "  Providers:     none (models.dev only)\n")
		default:
			fmt.Fprintf(&b, "  Providers:     %s\n", strings.Join(p, ", "))
		}
	}
	fmt.Fprintf(&b, "  Forwarding:    git_config=%t, ssh_auth_sock=%t, gpg_agent=%t\n", a.gitConfig, a.sshAuthSock, a.gpgAgent)
	netSummary := a.networkMode
	if a.networkMode != "internet" {
		if hosts := splitCSV(a.extraHosts); len(hosts) > 0 {
			netSummary += " + " + strings.Join(hosts, ", ")
		}
	}
	fmt.Fprintf(&b, "  Network:       %s\n", netSummary)
	fmt.Fprintf(&b, "  VM resources:  %s CPUs, %s memory, docker=%t\n", strings.TrimSpace(a.cpu), strings.TrimSpace(a.memory), a.enableDocker)
	dir := a.wsDir
	if norm, err := normalizeSetupDir(a.wsDir); err == nil {
		dir = norm
	}
	fmt.Fprintf(&b, "  Workspace:     %s -> %s", a.wsName, dir)
	if isBroadPath(dir) && !a.confirmBroad {
		fmt.Fprintf(&b, "\n  WARNING: sharing %q was not confirmed; go back with Shift+Tab and pick a narrower directory, or nothing is written.", dir)
	}
	return b.String()
}

// workspaceSetupAnswers holds the answers for adding or updating a workspace.
type workspaceSetupAnswers struct {
	name          string
	confirmUpdate bool
	dir           string
	confirmBroad  bool
	agent         string // "inherit" or agent name
	providers     []string
	docker        string // "inherit", "yes", "no"
	networkMode   string // "inherit", "internet", "custom"
	extraHosts    string
	save          bool
	startNow      bool
}

func defaultWorkspaceSetupAnswers(opts setupOptions) workspaceSetupAnswers {
	var providers []string
	if h, ok := agent.Lookup("opencode"); ok {
		providers = h.ProviderNames()
	}
	return workspaceSetupAnswers{
		name:          seededWorkspaceName(opts, config.SanitizeWorkspaceName(filepath.Base(opts.targetDir))),
		confirmUpdate: true,
		dir:           opts.targetDir,
		agent:         "inherit",
		providers:     providers,
		docker:        "inherit",
		networkMode:   "inherit",
		extraHosts:    "",
		save:          true,
		startNow:      false,
	}
}

func (a workspaceSetupAnswers) toOptions(raw []byte) (config.WorkspaceOptions, string, error) {
	if err := config.ValidateWorkspaceName(a.name); err != nil {
		return config.WorkspaceOptions{}, "", err
	}
	wsDir, err := normalizeSetupDir(a.dir)
	if err != nil {
		return config.WorkspaceOptions{}, "", err
	}
	if isBroadPath(wsDir) && !a.confirmBroad {
		return config.WorkspaceOptions{}, "", fmt.Errorf("%q is a very broad path and sharing it was not confirmed; run setup again and pick a narrower directory", wsDir)
	}
	wsAgent := ""
	if a.agent != "" && a.agent != "inherit" {
		wsAgent = a.agent
	}
	var enableDocker *bool
	switch a.docker {
	case "yes":
		v := true
		enableDocker = &v
	case "no":
		v := false
		enableDocker = &v
	}
	allowInternet := a.networkMode == "internet"
	var extraHosts []string
	if a.networkMode == "custom" {
		if err := validateHostsCSV(a.extraHosts); err != nil {
			return config.WorkspaceOptions{}, "", err
		}
		extraHosts = splitCSV(a.extraHosts)
	}
	wsOpts := config.WorkspaceOptions{
		Name:          a.name,
		Paths:         []string{wsDir},
		Agent:         wsAgent,
		EnableDocker:  enableDocker,
		AllowInternet: allowInternet,
		ExtraHosts:    extraHosts,
	}
	if h, seed, err := config.SeedAgent(raw, wsOpts); err != nil {
		return config.WorkspaceOptions{}, "", err
	} else if seed {
		wsOpts.Providers = normalizeProviders(h, a.providers)
	}
	return wsOpts, wsDir, nil
}

func (a workspaceSetupAnswers) summary(configPath string, raw []byte) string {
	var b strings.Builder
	dir := a.dir
	if norm, err := normalizeSetupDir(a.dir); err == nil {
		dir = norm
	}
	fmt.Fprintf(&b, "  Config file:   %s\n", configPath)
	fmt.Fprintf(&b, "  Workspace:     %s -> %s\n", a.name, dir)
	if isBroadPath(dir) && !a.confirmBroad {
		fmt.Fprintf(&b, "  WARNING: sharing %q was not confirmed; go back with Shift+Tab and pick a narrower directory, or nothing is written.\n", dir)
	}
	fmt.Fprintf(&b, "  Agent:         %s\n", cmp.Or(a.agent, "inherit"))
	probe := config.WorkspaceOptions{Name: a.name}
	if a.agent != "inherit" {
		probe.Agent = a.agent
	}
	if h, seed, err := config.SeedAgent(raw, probe); err == nil && seed {
		p := normalizeProviders(h, a.providers)
		switch {
		case p == nil:
			fmt.Fprintf(&b, "  Providers:     all (%s)\n", strings.Join(h.ProviderNames(), ", "))
		case len(p) == 0:
			fmt.Fprintf(&b, "  Providers:     none (models.dev only)\n")
		default:
			fmt.Fprintf(&b, "  Providers:     %s\n", strings.Join(p, ", "))
		}
	}
	fmt.Fprintf(&b, "  Docker in VM:  %s\n", cmp.Or(a.docker, "inherit"))
	netSummary := cmp.Or(a.networkMode, "inherit")
	if a.networkMode == "custom" {
		if hosts := splitCSV(a.extraHosts); len(hosts) > 0 {
			netSummary += " (" + strings.Join(hosts, ", ") + ")"
		}
	}
	fmt.Fprintf(&b, "  Network:       %s", netSummary)
	return b.String()
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

func runSetup(ctx context.Context, p *promptIO, opts setupOptions) (setupResult, error) {
	if err := ctx.Err(); err != nil {
		return setupResult{}, err
	}
	isDefault, err := config.IsDefaultFile(opts.configPath)
	if err != nil {
		return setupResult{}, err
	}
	if isDefault {
		return runFirstTimeSetup(ctx, p, opts)
	}
	return runWorkspaceSetup(ctx, p, opts)
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

func runFirstTimeSetup(ctx context.Context, p *promptIO, opts setupOptions) (setupResult, error) {
	ans := defaultFirstTimeAnswers(opts)
	out := p.writer()

	if p != nil && p.yes {
		wsDir, err := normalizeSetupDir(ans.wsDir)
		if err != nil {
			return setupResult{}, err
		}
		if isBroadPath(wsDir) {
			return setupResult{}, fmt.Errorf("%q is a very broad path; run setup interactively or pass a narrower directory", wsDir)
		}
		return applyFirstTimeSetup(ctx, p, opts, ans)
	}

	_, _ = color.New(color.FgCyan, color.Bold).Fprintf(out, "Welcome to jailoc! Let's configure %s.\n\n", opts.configPath)

	steps := buildFirstTimeForm(&ans, opts)
	if err := p.runForm(ctx, steps); err != nil {
		return setupResult{}, err
	}
	return applyFirstTimeSetup(ctx, p, opts, ans)
}

func buildFirstTimeForm(ans *firstTimeAnswers, opts setupOptions) formSteps {
	opencodeHarness, _ := agent.Lookup("opencode")

	agentStep := formStep{fields: []huh.Field{
		huh.NewSelect[string]().
			Title("Default coding agent").
			Description("jailoc installs this agent in the VM and starts it in each session.\nIts host configuration is copied into the VM read-only.").
			Options(agentSelectOptions(false)...).
			Value(&ans.agent),
	}}

	providersStep := formStep{
		fields: []huh.Field{providerMultiSelect("Model providers", opencodeHarness, &ans.providers)},
		hidden: func() bool {
			h, ok := agent.Lookup(ans.agent)
			return !ok || !h.ExplicitEndpoints
		},
	}

	forwardingStep := formStep{fields: []huh.Field{
		huh.NewConfirm().
			Title("Copy host Git configuration into the VM (git_config)?").
			Description("A read-only copy, so commits in the VM use your name and email.").
			Value(&ans.gitConfig),
		huh.NewConfirm().
			Title("Forward host SSH agent into the VM (ssh_auth_sock)?").
			Description("Lets SSH and Git in the VM use your keys and ~/.ssh/known_hosts.\nKeys stay on the host, but processes in the VM can use them.").
			Value(&ans.sshAuthSock),
		huh.NewConfirm().
			Title("Forward host GPG agent into the VM (gpg_agent)?").
			Description("Lets Git in the VM sign commits and tags with your GPG keys.\nKeys stay on the host, but processes in the VM can sign and decrypt with them.").
			Value(&ans.gpgAgent),
	}}

	networkStep := formStep{fields: []huh.Field{
		huh.NewSelect[string]().
			Title("Default network egress policy").
			Description("\"dev-deps\" allows selected development services, \"internet\" all public hosts.\nPrivate networks stay blocked unless you allow them explicitly.").
			Options(
				huh.NewOption("dev-deps (selected development services)", "dev-deps"),
				huh.NewOption("internet (all public hosts)", "internet"),
			).
			Value(&ans.networkMode),
	}}

	extraHostsStep := formStep{fields: []huh.Field{
		huh.NewInput().
			Title("Extra allowed hostnames or CIDRs (comma-separated, optional)").
			Description("Other hosts the agent needs, such as gitlab.corp.example or 10.20.0.0/16.").
			Placeholder("gitlab.corp.example, 10.20.0.0/16").
			Validate(validateHostsCSV).
			Value(&ans.extraHosts),
	}, hidden: func() bool {
		return ans.networkMode == "internet"
	}}

	vmStep := formStep{fields: []huh.Field{
		huh.NewConfirm().
			Title("Enable Docker inside the VM by default (enable_docker)?").
			Description("Containers run in the VM's own Docker; the host Docker socket is not shared.").
			Value(&ans.enableDocker),
		huh.NewInput().
			Title("Default VM CPUs").
			Description("Virtual CPUs per VM, rounded up to a whole number.").
			Validate(validatePositiveFloat).
			Value(&ans.cpu),
		huh.NewInput().
			Title("Default VM memory").
			Description("RAM limit per VM, not memory reserved on the host.\nAllocated as needed; freed pages and unused file cache return to the host.").
			Validate(config.ValidateMemoryFormat).
			Value(&ans.memory),
	}}

	workspaceStep := formStep{fields: []huh.Field{
		huh.NewInput().
			Title("Initial workspace name").
			Description("Used in commands such as \"jailoc up <name>\".").
			Validate(config.ValidateWorkspaceName).
			Value(&ans.wsName),
		huh.NewInput().
			Title("Initial workspace directory path").
			Description("Shared read-write with the VM at the same path.\nThe agent can modify any file in it.").
			Validate(validateWorkspaceDir).
			Value(&ans.wsDir),
	}}

	broadTitle := func() string { return broadPathTitle(ans.wsDir) }
	broadConfirm := huh.NewConfirm().
		TitleFunc(broadTitle, &ans.wsDir).
		Description("Choose No and press Shift+Tab to pick a narrower directory.").
		Value(&ans.confirmBroad)
	broadStep := formStep{
		fields:  []huh.Field{broadConfirm},
		hidden:  func() bool { return !isBroadSetupDir(ans.wsDir) },
		refresh: func() { broadConfirm.Title(broadTitle()) },
	}

	steps := formSteps{
		agentStep,
		providersStep,
		forwardingStep,
		networkStep,
		extraHostsStep,
		vmStep,
		workspaceStep,
		broadStep,
	}

	if !opts.dryRun {
		summary := func() string { return ans.summary(opts.configPath) }
		reviewNote := huh.NewNote().
			Title("Review configuration").
			DescriptionFunc(func() string { return escapeNoteMarkup(summary()) }, []any{
				&ans.agent, &ans.providers, &ans.gitConfig, &ans.sshAuthSock, &ans.gpgAgent,
				&ans.networkMode, &ans.extraHosts, &ans.enableDocker, &ans.cpu, &ans.memory,
				&ans.wsName, &ans.wsDir, &ans.confirmBroad,
			})
		steps = append(steps, formStep{
			fields: []huh.Field{
				reviewNote,
				huh.NewConfirm().
					Title(fmt.Sprintf("Write configuration to %s?", opts.configPath)).
					Value(&ans.save),
			},
			refresh: func() { reviewNote.Description(summary()) },
		})
	}

	return steps
}

func applyFirstTimeSetup(ctx context.Context, p *promptIO, opts setupOptions, ans firstTimeAnswers) (setupResult, error) {
	firstOpts, wsDir, err := ans.toOptions()
	if err != nil {
		return setupResult{}, fmt.Errorf("build configuration: %w", err)
	}
	rendered, err := config.RenderFirstTime(firstOpts)
	if err != nil {
		return setupResult{}, fmt.Errorf("build configuration: %w", err)
	}
	return finishSetup(ctx, p, opts, rendered, setupResult{name: ans.wsName, dir: wsDir}, ans.save,
		fmt.Sprintf("Wrote %s (workspace %q -> %s)\n", opts.configPath, ans.wsName, wsDir))
}

func runWorkspaceSetup(ctx context.Context, p *promptIO, opts setupOptions) (setupResult, error) {
	cfg, err := config.LoadFrom(opts.configPath)
	if err != nil {
		return setupResult{}, err
	}
	raw, err := os.ReadFile(opts.configPath) //nolint:gosec // G304: configPath is the jailoc config file
	if err != nil {
		return setupResult{}, fmt.Errorf("read config %q: %w", opts.configPath, err)
	}

	ans := defaultWorkspaceSetupAnswers(opts)
	out := p.writer()

	if p != nil && p.yes {
		wsDir, err := normalizeSetupDir(ans.dir)
		if err != nil {
			return setupResult{}, err
		}
		if isBroadPath(wsDir) {
			return setupResult{}, fmt.Errorf("%q is a very broad path; run setup interactively or pass a narrower directory", wsDir)
		}
		return applyWorkspaceSetup(ctx, p, opts, raw, ans)
	}

	_, _ = color.New(color.FgCyan, color.Bold).Fprintf(out, "Existing configuration found at %s. Let's configure a workspace.\n\n", opts.configPath)

	steps := buildWorkspaceSetupForm(&ans, opts, cfg, raw)
	if err := p.runForm(ctx, steps); err != nil {
		return setupResult{}, err
	}
	if existing, exists := cfg.Workspaces[ans.name]; exists && len(existing.Paths) > 0 && !ans.confirmUpdate {
		_, _ = fmt.Fprintln(out, "Nothing changed.")
		return setupResult{}, nil
	}
	return applyWorkspaceSetup(ctx, p, opts, raw, ans)
}

func buildWorkspaceSetupForm(ans *workspaceSetupAnswers, opts setupOptions, cfg *config.Config, raw []byte) formSteps {
	opencodeHarness, _ := agent.Lookup("opencode")

	isExisting := func() bool {
		existing, exists := cfg.Workspaces[ans.name]
		return exists && len(existing.Paths) > 0
	}
	skippedUpdate := func() bool {
		return isExisting() && !ans.confirmUpdate
	}

	nameStep := formStep{fields: []huh.Field{
		huh.NewInput().
			Title("Workspace name").
			Description("Used in commands such as \"jailoc up <name>\".").
			Validate(config.ValidateWorkspaceName).
			Value(&ans.name),
	}}

	overwriteTitle := func() string {
		return fmt.Sprintf("Workspace %q already exists. Update it?", ans.name)
	}
	overwriteConfirm := huh.NewConfirm().
		TitleFunc(overwriteTitle, &ans.name).
		Value(&ans.confirmUpdate)
	overwriteStep := formStep{
		fields:  []huh.Field{overwriteConfirm},
		hidden:  func() bool { return !isExisting() },
		refresh: func() { overwriteConfirm.Title(overwriteTitle()) },
	}

	dirStep := formStep{fields: []huh.Field{
		huh.NewInput().
			Title("Workspace directory path").
			Description("Shared read-write with the VM at the same path.\nThe agent can modify any file in it.").
			Validate(validateWorkspaceDir).
			Value(&ans.dir),
	}, hidden: skippedUpdate}

	broadTitle := func() string { return broadPathTitle(ans.dir) }
	broadConfirm := huh.NewConfirm().
		TitleFunc(broadTitle, &ans.dir).
		Description("Choose No and press Shift+Tab to pick a narrower directory.").
		Value(&ans.confirmBroad)
	broadStep := formStep{
		fields:  []huh.Field{broadConfirm},
		hidden:  func() bool { return skippedUpdate() || !isBroadSetupDir(ans.dir) },
		refresh: func() { broadConfirm.Title(broadTitle()) },
	}

	optionsStep := formStep{fields: []huh.Field{
		huh.NewSelect[string]().
			Title("Workspace coding agent").
			Description("\"inherit\" uses the [defaults] agent.").
			Options(agentSelectOptions(true)...).
			Value(&ans.agent),
		huh.NewSelect[string]().
			Title("Enable Docker in this workspace").
			Description("Containers run in the VM's own Docker; the host Docker socket is not shared.\n\"inherit\" uses the [defaults] setting.").
			Options(
				huh.NewOption("inherit (use [defaults] setting)", "inherit"),
				huh.NewOption("yes", "yes"),
				huh.NewOption("no", "no"),
			).
			Value(&ans.docker),
		huh.NewSelect[string]().
			Title("Workspace network policy").
			Description("\"inherit\" uses the [defaults] rules.\n\"internet\" adds all public hosts to them, \"custom\" the hosts you list.").
			Options(
				huh.NewOption("inherit (use [defaults] rules)", "inherit"),
				huh.NewOption("internet (all public hosts)", "internet"),
				huh.NewOption("custom (hosts/CIDRs you list)", "custom"),
			).
			Value(&ans.networkMode),
	}, hidden: skippedUpdate}

	extraHostsStep := formStep{fields: []huh.Field{
		huh.NewInput().
			Title("Allowed hostnames or CIDRs for this workspace (comma-separated)").
			Description("Other hosts the agent needs, such as *.corp.example or 10.20.0.0/16.").
			Placeholder("*.corp.example, 10.20.0.0/16").
			Validate(validateHostsCSV).
			Value(&ans.extraHosts),
	}, hidden: func() bool {
		return skippedUpdate() || ans.networkMode != "custom"
	}}

	providersStep := formStep{
		fields: []huh.Field{providerMultiSelect("Model providers", opencodeHarness, &ans.providers)},
		hidden: func() bool {
			if skippedUpdate() {
				return true
			}
			probe := config.WorkspaceOptions{Name: ans.name}
			if ans.agent != "inherit" {
				probe.Agent = ans.agent
			}
			_, seed, err := config.SeedAgent(raw, probe)
			return err != nil || !seed
		},
	}

	steps := formSteps{
		nameStep,
		overwriteStep,
		dirStep,
		broadStep,
		optionsStep,
		extraHostsStep,
		providersStep,
	}

	if !opts.dryRun {
		summary := func() string { return ans.summary(opts.configPath, raw) }
		reviewNote := huh.NewNote().
			Title("Review workspace").
			DescriptionFunc(func() string { return escapeNoteMarkup(summary()) }, []any{
				&ans.name, &ans.dir, &ans.confirmBroad, &ans.agent, &ans.providers,
				&ans.docker, &ans.networkMode, &ans.extraHosts,
			})
		saveTitle := func() string {
			return fmt.Sprintf("Save workspace %q to %s?", ans.name, opts.configPath)
		}
		saveConfirm := huh.NewConfirm().
			TitleFunc(saveTitle, &ans.name).
			Value(&ans.save)
		steps = append(steps, formStep{
			fields: []huh.Field{reviewNote, saveConfirm},
			hidden: skippedUpdate,
			refresh: func() {
				reviewNote.Description(summary())
				saveConfirm.Title(saveTitle())
			},
		})
	}

	return steps
}

func applyWorkspaceSetup(ctx context.Context, p *promptIO, opts setupOptions, raw []byte, ans workspaceSetupAnswers) (setupResult, error) {
	wsOpts, wsDir, err := ans.toOptions(raw)
	if err != nil {
		return setupResult{}, fmt.Errorf("update configuration: %w", err)
	}
	rendered, err := config.RenderWorkspace(raw, wsOpts)
	if err != nil {
		return setupResult{}, fmt.Errorf("update configuration: %w", err)
	}
	return finishSetup(ctx, p, opts, rendered, setupResult{name: ans.name, dir: wsDir}, ans.save,
		fmt.Sprintf("Saved workspace %q (%s) in %s\n", ans.name, wsDir, opts.configPath))
}

func finishSetup(ctx context.Context, p *promptIO, opts setupOptions, rendered []byte, res setupResult, save bool, savedMsg string) (setupResult, error) {
	out := p.writer()
	if opts.dryRun {
		_, _ = fmt.Fprintf(out, "\nconfig.toml (dry run, nothing written):\n\n%s", rendered)
		return setupResult{}, nil
	}
	if !save {
		_, _ = fmt.Fprintln(out, "Nothing changed.")
		return setupResult{}, nil
	}
	if err := ctx.Err(); err != nil {
		return setupResult{}, err
	}
	if err := config.WriteBytes(opts.configPath, rendered); err != nil {
		return setupResult{}, err
	}
	_, _ = color.New(color.FgGreen).Fprint(out, savedMsg)

	if !opts.askStart || (p != nil && p.yes) {
		return res, nil
	}
	startNow, err := p.confirm(ctx, fmt.Sprintf("Start workspace %q now?", res.name), "", false)
	if err != nil {
		return setupResult{}, err
	}
	res.startNow = startNow
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
	p := newStdinPromptIO(false)
	runWizard, err := p.confirm(ctx, "No customized jailoc configuration found. Run `jailoc setup` wizard now?", "", true)
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

// isBroadSetupDir reports whether dir resolves to a path that shares a whole
// directory tree (for example / or $HOME) with the VM.
func isBroadSetupDir(dir string) bool {
	norm, err := normalizeSetupDir(dir)
	return err == nil && isBroadPath(norm)
}

func broadPathTitle(dir string) string {
	if norm, err := normalizeSetupDir(dir); err == nil {
		dir = norm
	}
	return fmt.Sprintf("WARNING: %q is a very broad path — this will share your entire directory tree with the VM. Are you sure?", dir)
}
