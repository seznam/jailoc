//go:build darwin || linux

package cmd

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"
	"unicode"

	"github.com/charmbracelet/huh"
	"github.com/fatih/color"
	"github.com/spf13/cobra"
	"golang.org/x/term"

	"github.com/seznam/jailoc/internal/agent"
	"github.com/seznam/jailoc/internal/config"
	"github.com/seznam/jailoc/internal/docker"
	"github.com/seznam/jailoc/internal/vm"
)

var (
	migrateDryRun bool
	migrateYes    bool
)

var migrateCmd = &cobra.Command{
	Use:   "migrate",
	Short: "Migrate an older jailoc configuration to the current format",
	Long: `Rewrite an older config.toml: a jailoc v1 configuration for the VM backend
(backend = "vm"), and deprecated network settings into the network table and
rules.

For each Docker workspace, migrate asks how to carry over every mount (a live
directory mount, or a read-only snapshot), removes v1 settings (mode, password_mode,
expose_port) after confirmation, stops running Docker workspaces, and copies the
OpenCode data and cache volumes into the VM's persistent home directories.
It moves allowed_hosts, allowed_networks, filtered_dns, dns_upstream and
dns_blocked_zones of [defaults] and every workspace into their network table
and [[*.network.rules]]. When no scope sets network.agent_endpoints, it asks
which model providers OpenCode and OmO may reach and adds their endpoints there.
The previous config.toml is kept as a backup; comments in it are not carried
over.

When allow rules name hosts but no internal_zones are set, migrate also offers
to add internal_zones to [defaults.network]; enter only zones you control.`,
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		interactive := term.IsTerminal(int(os.Stdin.Fd())) //nolint:gosec // G115: uintptr→int is safe for file descriptors
		if !interactive && !migrateYes && !isAccessibleEnv() {
			return errors.New("migrate asks questions; run it in a terminal or pass --yes to accept the defaults")
		}
		p := newMigratePrompter(migrateYes)
		return runMigrate(cmd.Context(), config.ConfigPath(), p)
	},
}

func init() {
	migrateCmd.Flags().BoolVar(&migrateDryRun, "dry-run", false, "show the migrated configuration without changing anything")
	migrateCmd.Flags().BoolVarP(&migrateYes, "yes", "y", false, "accept the default answer to every question")
	rootCmd.AddCommand(migrateCmd)
}

// migratePrompter drives migration forms and confirms, plus optional DNS
// resolver overrides for legacy alias checks.
type migratePrompter struct {
	*promptIO
	// resolver looks up legacy allowed_hosts names; nil uses hostResolver.
	resolver legacyResolver
}

func newMigratePrompter(yes bool) *migratePrompter {
	return &migratePrompter{
		promptIO: newStdinPromptIO(yes),
	}
}

// migrationAnswers holds the user's choices for planning a migration.
type migrationAnswers struct {
	// isV1Docker answers whether an unset-backend config with only deprecated
	// network settings is a jailoc v1 Docker configuration.
	isV1Docker bool
	// removeIncompatible maps scope label ("[defaults]", "[workspaces.x]") to
	// whether incompatible v1 settings should be removed.
	removeIncompatible map[string]bool
	// mountChoices maps scope label to the per-mount action ("mount",
	// "snapshot", "drop") for mounts with a host source.
	mountChoices map[string][]string
	// blockedZonesTarget is "workspaces" or "defaults" when both default
	// dns_blocked_zones and workspace overrides exist.
	blockedZonesTarget string
	// providers lists the selected model providers for multi-provider agents;
	// nil or all providers selects every provider.
	providers []string
	// internalZones is the comma- or space-separated internal_zones input.
	internalZones string
}

func defaultMigrationAnswers(cfg *config.Config, stat func(string) (fs.FileInfo, error)) (migrationAnswers, error) {
	ans := migrationAnswers{
		isV1Docker:         false,
		removeIncompatible: map[string]bool{},
		mountChoices:       map[string][]string{},
		blockedZonesTarget: "workspaces",
	}
	if len(defaultsIncompatibilities(cfg)) > 0 {
		ans.removeIncompatible["[defaults]"] = true
	}
	defaultChoices, err := defaultMountChoices("[defaults]", cfg.Defaults.Mounts, stat)
	if err != nil {
		return migrationAnswers{}, err
	}
	if len(defaultChoices) > 0 {
		ans.mountChoices["[defaults]"] = defaultChoices
	}
	for _, name := range slices.Sorted(maps.Keys(cfg.Workspaces)) {
		ws := cfg.Workspaces[name]
		label := fmt.Sprintf("[workspaces.%s]", name)
		if len(workspaceIncompatibilities(&ws)) > 0 {
			ans.removeIncompatible[label] = true
		}
		choices, err := defaultMountChoices(label, ws.Mounts, stat)
		if err != nil {
			return migrationAnswers{}, err
		}
		if len(choices) > 0 {
			ans.mountChoices[label] = choices
		}
	}
	if h, ok := needsAgentEndpointsPrompt(cfg); ok {
		ans.providers = h.ProviderNames()
	}
	return ans, nil
}

func defaultMountChoices(label string, mounts []string, stat func(string) (fs.FileInfo, error)) ([]string, error) {
	var choices []string
	for _, spec := range mounts {
		mt, err := config.ParseMount(spec)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", label, err)
		}
		if mt.Host == "" {
			continue
		}
		host, err := config.ExpandPath(mt.Host)
		if err != nil {
			return nil, fmt.Errorf("%s: expand %q: %w", label, mt.Host, err)
		}
		if info, err := stat(host); err == nil && !info.IsDir() {
			choices = append(choices, "snapshot")
		} else {
			choices = append(choices, "mount")
		}
	}
	return choices, nil
}

// migration is the outcome of planning: the rewritten configuration and the
// workspaces that move from Docker to the VM backend.
type migration struct {
	cfg        *config.Config
	workspaces []string
	changes    []string
}

// incompatibility is a setting the VM backend rejects or ignores.
type incompatibility struct {
	setting, reason string
	clear           func()
}

func defaultsIncompatibilities(cfg *config.Config) []incompatibility {
	d := &cfg.Defaults
	var out []incompatibility
	if cfg.Mode != "" {
		out = append(out, incompatibility{"mode", "sessions run inside the VM", func() { cfg.Mode = "" }})
	}
	if cfg.PasswordMode != "" {
		out = append(out, incompatibility{"password_mode", "the VM does not use a server password", func() { cfg.PasswordMode = "" }})
	}
	if d.ExposePort != nil {
		out = append(out, incompatibility{"expose_port", "sessions run over SSH; there is no published port", func() { d.ExposePort = nil }})
	}
	return out
}

func workspaceIncompatibilities(ws *config.Workspace) []incompatibility {
	var out []incompatibility
	if ws.ExposePort != nil {
		out = append(out, incompatibility{"expose_port", "sessions run over SSH; there is no published port", func() { ws.ExposePort = nil }})
	}
	return out
}

// planMigration asks any needed migration questions via p and applies the
// resulting migrationAnswers to cfg in place.
func planMigration(ctx context.Context, cfg *config.Config, p *migratePrompter, stat func(string) (fs.FileInfo, error)) (*migration, error) {
	ans, err := defaultMigrationAnswers(cfg, stat)
	if err != nil {
		return nil, err
	}
	if p == nil || !p.yes {
		form := buildMigrationForm(cfg, &ans, stat)
		if form != nil {
			if err := p.runForm(ctx, form); err != nil {
				return nil, err
			}
		}
	}
	return applyMigrationAnswers(cfg, ans, stat)
}

// buildMigrationForm builds the dynamic huh form for migration questions, or
// returns nil when no interactive questions are needed.
func buildMigrationForm(cfg *config.Config, ans *migrationAnswers, stat func(string) (fs.FileInfo, error)) *huh.Form {
	names := slices.Sorted(maps.Keys(cfg.Workspaces))
	var groups []*huh.Group

	askV1 := needsV1DockerQuestion(cfg, names)
	if askV1 {
		groups = append(groups, huh.NewGroup(
			huh.NewConfirm().
				Title("The configuration names no backend. Is it a jailoc v1 (Docker) configuration whose workspaces should move to the VM backend?").
				Value(&ans.isV1Docker),
		))
	}

	isDockerActive := func() bool {
		if !askV1 {
			return true
		}
		return ans.isV1Docker
	}
	isDockerWorkspace := func(name string) bool {
		if !isDockerActive() {
			return false
		}
		return cmp.Or(cfg.Workspaces[name].Backend, cfg.Defaults.Backend) != config.BackendVM
	}
	hasAnyDockerWorkspace := func() bool {
		for _, name := range names {
			if isDockerWorkspace(name) {
				return true
			}
		}
		return false
	}

	if items := defaultsIncompatibilities(cfg); len(items) > 0 {
		groups = append(groups, incompatibilityGroup("[defaults]", items, ans))
	}
	if g := mountGroup("[defaults]", cfg.Defaults.Mounts, stat, ans, func() bool { return !hasAnyDockerWorkspace() }); g != nil {
		groups = append(groups, g)
	}

	for _, name := range names {
		wsName := name
		ws := cfg.Workspaces[wsName]
		label := fmt.Sprintf("[workspaces.%s]", wsName)
		hideWs := func() bool { return !isDockerWorkspace(wsName) }
		if items := workspaceIncompatibilities(&ws); len(items) > 0 {
			g := incompatibilityGroup(label, items, ans).WithHideFunc(hideWs)
			groups = append(groups, g)
		}
		if g := mountGroup(label, ws.Mounts, stat, ans, hideWs); g != nil {
			groups = append(groups, g)
		}
	}

	defaultZones, defaultZonesSrc := effectiveBlockedZones(cfg.Defaults.Network, cfg.Defaults.LegacyDNSBlockedZones, "[defaults]")
	var overridingWorkspaces []string
	for _, name := range names {
		ws := cfg.Workspaces[name]
		if ws.Network.Get().DNSBlockedZones != nil || ws.LegacyDNSBlockedZones != nil {
			overridingWorkspaces = append(overridingWorkspaces, name)
		}
	}
	if len(overridingWorkspaces) > 0 && len(blockedZonesToDeny(defaultZones)) > 0 {
		desc := fmt.Sprintf("%s has dns_blocked_zones, and workspace(s) %s override dns_blocked_zones.",
			defaultZonesSrc, strings.Join(overridingWorkspaces, ", "))
		groups = append(groups, huh.NewGroup(
			huh.NewSelect[string]().
				Title("Where should default dns_blocked_zones move?").
				Description(desc).
				Options(
					huh.NewOption("workspaces (move only into inheriting workspaces; preserves overrides)", "workspaces"),
					huh.NewOption("defaults (keep in [[defaults.network.rules]] for all workspaces)", "defaults"),
				).
				Value(&ans.blockedZonesTarget),
		))
	}

	if h, ok := needsAgentEndpointsPrompt(cfg); ok {
		groups = append(groups, huh.NewGroup(
			providerMultiSelect("OpenCode and OmO model providers (agent_endpoints)", h, &ans.providers),
		))
	}

	if needsInternalZonesAfterNetworkMove(cfg) {
		desc := strings.Join([]string{
			"Allow rules name hosts, but no internal_zones are set.",
			"internal_zones lists DNS zones you control and trust; only add zones you control.",
			"Names in a zone and its subdomains may resolve to private addresses; outside the zones, private DNS answers are filtered to prevent DNS rebinding.",
			"A zone alone grants no access — a matching allow rule is still needed.",
			"The zones are written to [defaults.network] internal_zones and apply to every workspace.",
		}, "\n")
		groups = append(groups, huh.NewGroup(
			huh.NewInput().
				Title("Internal zones (comma or space separated, empty to skip)").
				Description(desc).
				Placeholder("corp.example, lab.example").
				Validate(validateInternalZonesInput).
				Value(&ans.internalZones),
		))
	}

	if len(groups) == 0 {
		return nil
	}
	return huh.NewForm(groups...)
}

func incompatibilityGroup(label string, items []incompatibility, ans *migrationAnswers) *huh.Group {
	lines := make([]string, len(items))
	for i, it := range items {
		lines[i] = fmt.Sprintf("%s: %s", it.setting, it.reason)
	}
	desc := fmt.Sprintf("%s has settings the VM backend does not support:\n  %s", label, strings.Join(lines, "\n  "))
	val := true
	if cur, ok := ans.removeIncompatible[label]; ok {
		val = cur
	}
	ans.removeIncompatible[label] = val
	return huh.NewGroup(
		huh.NewConfirm().
			Title(fmt.Sprintf("Remove incompatible settings in %s?", label)).
			Description(desc).
			Accessor(&mapBoolAccessor{m: ans.removeIncompatible, k: label}),
	)
}

func mountGroup(label string, mounts []string, stat func(string) (fs.FileInfo, error), ans *migrationAnswers, hideFunc func() bool) *huh.Group {
	var fields []huh.Field
	choiceIdx := 0
	for _, spec := range mounts {
		mt, err := config.ParseMount(spec)
		if err != nil || mt.Host == "" {
			continue
		}
		host, err := config.ExpandPath(mt.Host)
		if err != nil {
			continue
		}
		isFile := false
		if info, err := stat(host); err == nil && !info.IsDir() {
			isFile = true
		}
		title := fmt.Sprintf("%s mount %s -> %s (%s)", label, mt.Host, mt.Container, mt.Mode)
		var desc string
		var opts []huh.Option[string]
		if isFile {
			desc = "This path is a file; the VM shares directories only."
			opts = []huh.Option[string]{
				huh.NewOption("snapshot (read-only copy taken at every VM start)", "snapshot"),
				huh.NewOption("drop (remove the mount)", "drop"),
			}
		} else {
			snapDesc := "snapshot (read-only copy taken at every VM start)"
			if mt.Mode == "rw" {
				snapDesc = "snapshot (read-only copy taken at every VM start; workspace writes no longer reach the host)"
			}
			opts = []huh.Option[string]{
				huh.NewOption("mount (live, two-way share of the directory)", "mount"),
				huh.NewOption(snapDesc, "snapshot"),
				huh.NewOption("drop (remove the mount)", "drop"),
			}
		}
		idx := choiceIdx
		choiceIdx++
		sel := huh.NewSelect[string]().
			Title(title).
			Options(opts...).
			Accessor(&sliceStringAccessor{m: ans.mountChoices, k: label, idx: idx})
		if desc != "" {
			sel.Description(desc)
		}
		fields = append(fields, sel)
	}
	if len(fields) == 0 {
		return nil
	}
	return huh.NewGroup(fields...).WithHideFunc(hideFunc)
}

type mapBoolAccessor struct {
	m map[string]bool
	k string
}

func (a *mapBoolAccessor) Get() bool  { return a.m[a.k] }
func (a *mapBoolAccessor) Set(v bool) { a.m[a.k] = v }

type sliceStringAccessor struct {
	m   map[string][]string
	k   string
	idx int
}

func (a *sliceStringAccessor) Get() string {
	s := a.m[a.k]
	if a.idx < len(s) {
		return s[a.idx]
	}
	return ""
}

func (a *sliceStringAccessor) Set(v string) {
	s := a.m[a.k]
	if a.idx < len(s) {
		s[a.idx] = v
	}
}

// applyMigrationAnswers rewrites cfg in place from ans, without prompting.
func applyMigrationAnswers(cfg *config.Config, ans migrationAnswers, stat func(string) (fs.FileInfo, error)) (*migration, error) {
	m := &migration{cfg: cfg}
	names := slices.Sorted(maps.Keys(cfg.Workspaces))

	docker := true
	if needsV1DockerQuestion(cfg, names) {
		docker = ans.isV1Docker
	}
	for _, name := range names {
		// In jailoc v1 an unset backend meant Docker; only an explicit
		// "vm" marks a workspace that needs no migration.
		if docker && cmp.Or(cfg.Workspaces[name].Backend, cfg.Defaults.Backend) != config.BackendVM {
			m.workspaces = append(m.workspaces, name)
		}
	}

	if err := applyIncompatibilities("[defaults]", defaultsIncompatibilities(cfg), ans.removeIncompatible, m); err != nil {
		return nil, err
	}
	// Default mounts are inherited by workspaces that already use the VM
	// backend, so they are carried over only when Docker workspaces move.
	if len(m.workspaces) > 0 {
		if err := applyCarryMounts("[defaults]", &cfg.Defaults.Mounts, &cfg.Defaults.Snapshots, ans.mountChoices["[defaults]"], stat, m); err != nil {
			return nil, err
		}
	}
	for _, name := range m.workspaces {
		ws := cfg.Workspaces[name]
		label := fmt.Sprintf("[workspaces.%s]", name)
		if err := applyIncompatibilities(label, workspaceIncompatibilities(&ws), ans.removeIncompatible, m); err != nil {
			return nil, err
		}
		if err := applyCarryMounts(label, &ws.Mounts, &ws.Snapshots, ans.mountChoices[label], stat, m); err != nil {
			return nil, err
		}
		cfg.Workspaces[name] = ws
	}

	hadNetworkRules := hasAnyNetworkRules(cfg)
	d := &cfg.Defaults
	defaultZones, defaultZonesSrc := effectiveBlockedZones(d.Network, d.LegacyDNSBlockedZones, "[defaults]")
	var overridingWorkspaces []string
	for _, name := range names {
		ws := cfg.Workspaces[name]
		if ws.Network.Get().DNSBlockedZones != nil || ws.LegacyDNSBlockedZones != nil {
			overridingWorkspaces = append(overridingWorkspaces, name)
		}
	}
	moveDefaultZonesToWorkspaces := false
	if len(overridingWorkspaces) > 0 && len(blockedZonesToDeny(defaultZones)) > 0 {
		moveDefaultZonesToWorkspaces = cmp.Or(ans.blockedZonesTarget, "workspaces") == "workspaces"
	}

	moveNetwork("[defaults]", &d.Network, &d.AllowedHosts, &d.AllowedNetworks, &d.LegacyFilteredDNS, &d.LegacyDNSUpstream, &d.LegacyDNSBlockedZones, !moveDefaultZonesToWorkspaces, nil, m)
	if moveDefaultZonesToWorkspaces && defaultZones != nil {
		m.changes = append(m.changes, fmt.Sprintf("%s: move dns_blocked_zones to inheriting workspaces", defaultZonesSrc))
	}
	for _, name := range names {
		ws := cfg.Workspaces[name]
		var inherited []string
		if moveDefaultZonesToWorkspaces {
			inherited = defaultZones
		}
		moveNetwork(fmt.Sprintf("[workspaces.%s]", name), &ws.Network, &ws.AllowedHosts, &ws.AllowedNetworks, &ws.LegacyFilteredDNS, &ws.LegacyDNSUpstream, &ws.LegacyDNSBlockedZones, true, inherited, m)
		cfg.Workspaces[name] = ws
	}
	if cfg.Defaults.Backend != config.BackendVM {
		cfg.Defaults.Backend = config.BackendVM
		m.changes = append(m.changes, `[defaults]: backend = "vm"`)
	}
	for _, name := range names {
		ws := cfg.Workspaces[name]
		if ws.Backend != "" {
			m.changes = append(m.changes, fmt.Sprintf("[workspaces.%s]: remove backend = %q", name, ws.Backend))
			ws.Backend = ""
			cfg.Workspaces[name] = ws
		}
	}
	// Add an explicit allow = ["*"] rule when the configuration had no
	// [[*.network.rules]] before migration, so existing workspaces keep
	// their public internet access until the user narrows the rules.
	if !hadNetworkRules {
		if d.Network == nil {
			d.Network = &config.Network{}
		}
		d.Network.Rules = append([]config.NetworkRule{{
			Name:  "internet",
			Allow: []string{"*"},
			Proto: config.RuleProtoAny,
		}}, d.Network.Rules...)
		m.changes = append(m.changes, `[[defaults.network.rules]]: add allow = ["*"] (public internet)`)
	}
	if err := applyAgentEndpoints(cfg, names, ans.providers, m); err != nil {
		return nil, err
	}
	if err := applyInternalZones(cfg, ans.internalZones, m); err != nil {
		return nil, err
	}
	return m, nil
}

func needsAgentEndpointsPrompt(cfg *config.Config) (agent.Harness, bool) {
	if cfg.Defaults.Network.Get().AgentEndpoints != nil {
		return agent.Harness{}, false
	}
	names := slices.Sorted(maps.Keys(cfg.Workspaces))
	for _, name := range names {
		if cfg.Workspaces[name].Network.Get().AgentEndpoints != nil {
			return agent.Harness{}, false
		}
	}
	defaultName := cmp.Or(cfg.Defaults.Agent, agent.Default)
	if h := builtinHarness(defaultName, cfg.Defaults.Agents[defaultName].From); h.ExplicitEndpoints {
		return h, true
	}
	for _, name := range names {
		ws := cfg.Workspaces[name]
		agentName := cmp.Or(ws.Agent, defaultName)
		if h := builtinHarness(agentName, cmp.Or(ws.Agents[agentName].From, cfg.Defaults.Agents[agentName].From)); h.ExplicitEndpoints {
			return h, true
		}
	}
	return agent.Harness{}, false
}

// applyAgentEndpoints writes the endpoints of multi-provider agents (OpenCode,
// OmO) into agent_endpoints when no scope sets it, so they keep reaching the
// model and auth endpoints that older jailoc allowed implicitly: the default
// agent's in [defaults.network], and a workspace's own list when its agent has
// other endpoints. Single-provider agents always reach theirs.
func applyAgentEndpoints(cfg *config.Config, names []string, selectedProviders []string, m *migration) error {
	if cfg.Defaults.Network.Get().AgentEndpoints != nil {
		return nil
	}
	for _, name := range names {
		if cfg.Workspaces[name].Network.Get().AgentEndpoints != nil {
			return nil
		}
	}
	endpoints := func(h agent.Harness) ([]string, error) {
		providers := normalizeProviders(h, selectedProviders)
		list, err := h.ProviderEndpoints(providers)
		if err != nil {
			return nil, fmt.Errorf("agent endpoints: %w", err)
		}
		return list, nil
	}

	defaultName := cmp.Or(cfg.Defaults.Agent, agent.Default)
	var defaults []string
	if h := builtinHarness(defaultName, cfg.Defaults.Agents[defaultName].From); h.ExplicitEndpoints {
		list, err := endpoints(h)
		if err != nil {
			return err
		}
		defaults = list
		d := &cfg.Defaults
		if d.Network == nil {
			d.Network = &config.Network{}
		}
		d.Network.AgentEndpoints = defaults
		m.changes = append(m.changes, fmt.Sprintf("[defaults.network]: add agent_endpoints of agent %q", defaultName))
	}
	for _, name := range names {
		ws := cfg.Workspaces[name]
		agentName := cmp.Or(ws.Agent, defaultName)
		h := builtinHarness(agentName, cmp.Or(ws.Agents[agentName].From, cfg.Defaults.Agents[agentName].From))
		if !h.ExplicitEndpoints {
			continue
		}
		list, err := endpoints(h)
		if err != nil {
			return err
		}
		if defaults != nil && slices.Equal(list, defaults) {
			continue
		}
		if ws.Network == nil {
			ws.Network = &config.Network{}
		}
		ws.Network.AgentEndpoints = list
		cfg.Workspaces[name] = ws
		m.changes = append(m.changes, fmt.Sprintf("[workspaces.%s.network]: add agent_endpoints of agent %q", name, agentName))
	}
	return nil
}

// builtinHarness returns the built-in agent name, or the built-in agent
// from that a custom agent name is derived from.
func builtinHarness(name, from string) agent.Harness {
	if h, ok := agent.Lookup(name); ok {
		return h
	}
	h, _ := agent.Lookup(from)
	return h
}

// needsInternalZones reports whether an allow rule names a host while no
// internal_zones are set anywhere, so the allowed names cannot resolve to
// private addresses.
func needsInternalZones(cfg *config.Config) bool {
	if len(cfg.Defaults.Network.Get().InternalZones) > 0 {
		return false
	}
	hasName := false
	scan := func(n *config.Network) {
		for _, r := range n.Get().Rules {
			for _, target := range r.Allow {
				if kind, _, err := config.ParseRuleTarget(target); err == nil && kind == "name" {
					hasName = true
				}
			}
		}
	}
	scan(cfg.Defaults.Network)
	for _, ws := range cfg.Workspaces {
		if len(ws.Network.Get().InternalZones) > 0 {
			return false
		}
		scan(ws.Network)
	}
	return hasName
}

// needsInternalZonesAfterNetworkMove checks whether needsInternalZones will be
// true after legacy allowed_hosts are converted into rules and internal_zones.
func needsInternalZonesAfterNetworkMove(cfg *config.Config) bool {
	if len(cfg.Defaults.Network.Get().InternalZones) > 0 {
		return false
	}
	if zones, _ := config.LegacyHostZones(cfg.Defaults.AllowedHosts); len(zones) > 0 {
		return false
	}
	for _, ws := range cfg.Workspaces {
		if len(ws.Network.Get().InternalZones) > 0 {
			return false
		}
		if zones, _ := config.LegacyHostZones(ws.AllowedHosts); len(zones) > 0 {
			return false
		}
	}
	return needsInternalZones(cfg)
}

func validateInternalZonesInput(line string) error {
	fields := strings.FieldsFunc(line, func(r rune) bool { return r == ',' || unicode.IsSpace(r) })
	for _, f := range fields {
		if err := config.ValidateInternalZone(f); err != nil {
			return err
		}
	}
	return nil
}

func parseInternalZonesInput(line string) ([]string, error) {
	fields := strings.FieldsFunc(line, func(r rune) bool { return r == ',' || unicode.IsSpace(r) })
	if len(fields) == 0 {
		return nil, nil
	}
	zones := make([]string, len(fields))
	for i, f := range fields {
		if err := config.ValidateInternalZone(f); err != nil {
			return nil, err
		}
		zones[i] = config.NormalizeInternalZone(f)
	}
	return uniqueStrings(zones), nil
}

func applyInternalZones(cfg *config.Config, rawZones string, m *migration) error {
	if !needsInternalZones(cfg) {
		return nil
	}
	zones, err := parseInternalZonesInput(rawZones)
	if err != nil {
		return err
	}
	if len(zones) == 0 {
		return nil
	}
	if cfg.Defaults.Network == nil {
		cfg.Defaults.Network = &config.Network{}
	}
	cfg.Defaults.Network.InternalZones = zones
	m.changes = append(m.changes, fmt.Sprintf("[defaults.network]: add internal_zones (%s)", strings.Join(zones, ", ")))
	return nil
}

func hasAnyNetworkRules(cfg *config.Config) bool {
	if len(cfg.Defaults.Network.Get().Rules) > 0 {
		return true
	}
	for _, ws := range cfg.Workspaces {
		if len(ws.Network.Get().Rules) > 0 {
			return true
		}
	}
	return false
}

// needsV1DockerQuestion reports whether migrate needs to ask if workspaces
// without backend = "vm" are jailoc v1 Docker workspaces. An unset backend is
// also the VM backend of the current jailoc, so a configuration that only has
// network settings to move and nothing else of jailoc v1 is a v1 one only when
// the user says so.
func needsV1DockerQuestion(cfg *config.Config, names []string) bool {
	if len(defaultsIncompatibilities(cfg)) > 0 || cfg.Defaults.Backend == config.BackendDocker {
		return false
	}
	legacy := hasDeprecatedNetwork(&cfg.Defaults)
	for _, name := range names {
		ws := cfg.Workspaces[name]
		if len(workspaceIncompatibilities(&ws)) > 0 || ws.Backend == config.BackendDocker {
			return false
		}
		legacy = legacy || hasDeprecatedWorkspaceNetwork(&ws)
	}
	if !legacy || cfg.Defaults.Backend == config.BackendVM {
		return false
	}
	return true
}

func hasDeprecatedNetwork(d *config.Defaults) bool {
	n := d.Network.Get()
	return len(d.AllowedHosts) > 0 || len(d.AllowedNetworks) > 0 ||
		len(config.LegacyNetwork(d.LegacyFilteredDNS, d.LegacyDNSUpstream, d.LegacyDNSBlockedZones)) > 0 ||
		n.FilteredDNS != nil || n.DNSUpstream != "" || n.DNSBlockedZones != nil
}

func hasDeprecatedWorkspaceNetwork(ws *config.Workspace) bool {
	n := ws.Network.Get()
	return len(ws.AllowedHosts) > 0 || len(ws.AllowedNetworks) > 0 ||
		len(config.LegacyNetwork(ws.LegacyFilteredDNS, ws.LegacyDNSUpstream, ws.LegacyDNSBlockedZones)) > 0 ||
		n.FilteredDNS != nil || n.DNSUpstream != "" || n.DNSBlockedZones != nil
}

func effectiveBlockedZones(table *config.Network, legacyZones []string, label string) ([]string, string) {
	if table != nil && table.DNSBlockedZones != nil {
		return table.DNSBlockedZones, strings.TrimSuffix(label, "]") + ".network]"
	}
	if legacyZones != nil {
		return legacyZones, label
	}
	return nil, ""
}

// moveNetwork rewrites legacy and deprecated network settings of a [defaults]
// or workspace table into its network table and [[*.network.rules]]. A value
// already set in the new field wins.
func moveNetwork(label string, table **config.Network, allowedHosts, allowedNetworks *[]string, legacyFilteredDNS **bool, legacyUpstream *string, legacyZones *[]string, emitZones bool, inheritedZones []string, m *migration) {
	netName := strings.TrimSuffix(label, "]") + ".network]"
	rulesName := "[" + strings.TrimSuffix(label, "]") + ".network.rules]]"
	ensureTable := func() *config.Network {
		if *table == nil {
			*table = &config.Network{}
		}
		return *table
	}

	if *legacyFilteredDNS != nil {
		*legacyFilteredDNS = nil
		if *table != nil && (*table).FilteredDNS != nil {
			(*table).FilteredDNS = nil
		}
		m.changes = append(m.changes, fmt.Sprintf("%s: remove filtered_dns (DNS filtering is always on)", label))
	} else if *table != nil && (*table).FilteredDNS != nil {
		(*table).FilteredDNS = nil
		m.changes = append(m.changes, fmt.Sprintf("%s: remove filtered_dns (DNS filtering is always on)", netName))
	}

	if *table != nil && (*table).DNSUpstream != "" {
		n := *table
		if n.DNS == "" {
			n.DNS = n.DNSUpstream
			m.changes = append(m.changes, fmt.Sprintf("%s: move dns_upstream to dns", netName))
		} else {
			m.changes = append(m.changes, fmt.Sprintf("%s: remove dns_upstream (dns is set)", netName))
		}
		n.DNSUpstream = ""
	}
	if *legacyUpstream != "" {
		n := ensureTable()
		if n.DNS == "" {
			n.DNS = *legacyUpstream
			m.changes = append(m.changes, fmt.Sprintf("%s: move dns_upstream to %s dns", label, netName))
		} else {
			m.changes = append(m.changes, fmt.Sprintf("%s: remove dns_upstream (set in %s)", label, netName))
		}
		*legacyUpstream = ""
	}

	if len(*allowedHosts) > 0 {
		n := ensureTable()
		n.Rules = append(n.Rules, config.NetworkRule{
			Name:  "allowed-hosts",
			Allow: uniqueStrings(*allowedHosts),
			Proto: config.RuleProtoAny,
		})
		// allowed_hosts opened private addresses its names resolved to; keep
		// that by trusting the names for private answers.
		zones, skipped := config.LegacyHostZones(*allowedHosts)
		*allowedHosts = nil
		m.changes = append(m.changes, fmt.Sprintf("%s: move allowed_hosts to %s", label, rulesName))
		if added := newStrings(n.InternalZones, zones); len(added) > 0 {
			n.InternalZones = append(n.InternalZones, added...)
			m.changes = append(m.changes, fmt.Sprintf("%s: add allowed_hosts names to %s internal_zones (%s)", label, netName, strings.Join(added, ", ")))
		}
		if len(skipped) > 0 {
			m.changes = append(m.changes, fmt.Sprintf("%s: %s cannot be internal zones; their names no longer resolve to private addresses", label, strings.Join(skipped, ", ")))
		}
	}
	if len(*allowedNetworks) > 0 {
		n := ensureTable()
		n.Rules = append(n.Rules, config.NetworkRule{
			Name:  "allowed-networks",
			Allow: uniqueStrings(*allowedNetworks),
			Proto: config.RuleProtoAny,
		})
		*allowedNetworks = nil
		m.changes = append(m.changes, fmt.Sprintf("%s: move allowed_networks to %s", label, rulesName))
	}

	moveZones := func(srcLabel string, zones []string) {
		if !emitZones {
			return
		}
		deny := blockedZonesToDeny(zones)
		if len(deny) > 0 {
			n := ensureTable()
			n.Rules = append(n.Rules, config.NetworkRule{
				Name:  "blocked-zones",
				Deny:  deny,
				Proto: config.RuleProtoAny,
			})
			m.changes = append(m.changes, fmt.Sprintf("%s: move dns_blocked_zones to %s", srcLabel, rulesName))
		} else {
			m.changes = append(m.changes, fmt.Sprintf("%s: remove dns_blocked_zones", srcLabel))
		}
	}
	hasOwnZones := false
	if *table != nil && (*table).DNSBlockedZones != nil {
		hasOwnZones = true
		zones := (*table).DNSBlockedZones
		(*table).DNSBlockedZones = nil
		moveZones(netName, zones)
		if *legacyZones != nil {
			*legacyZones = nil
			m.changes = append(m.changes, fmt.Sprintf("%s: remove dns_blocked_zones (set in %s)", label, netName))
		}
	} else if *legacyZones != nil {
		hasOwnZones = true
		zones := *legacyZones
		*legacyZones = nil
		moveZones(label, zones)
	}
	if !hasOwnZones && len(inheritedZones) > 0 {
		if deny := blockedZonesToDeny(inheritedZones); len(deny) > 0 {
			n := ensureTable()
			n.Rules = append(n.Rules, config.NetworkRule{
				Name:  "blocked-zones",
				Deny:  deny,
				Proto: config.RuleProtoAny,
			})
		}
	}
	if *table != nil {
		n := *table
		if n.IPv6 == nil && n.DNS == "" && len(n.Rules) == 0 && len(n.InternalZones) == 0 && n.AgentEndpoints == nil && n.FilteredDNS == nil && n.DNSUpstream == "" && n.DNSBlockedZones == nil {
			*table = nil
		}
	}
}

// legacyHostScope is the allowed_hosts of [defaults] or a workspace before
// migration.
type legacyHostScope struct {
	label, workspace string
	hosts            []string
}

func legacyHostScopes(cfg *config.Config) []legacyHostScope {
	var out []legacyHostScope
	if len(cfg.Defaults.AllowedHosts) > 0 {
		out = append(out, legacyHostScope{label: "[defaults]", hosts: slices.Clone(cfg.Defaults.AllowedHosts)})
	}
	for _, name := range slices.Sorted(maps.Keys(cfg.Workspaces)) {
		if hosts := cfg.Workspaces[name].AllowedHosts; len(hosts) > 0 {
			out = append(out, legacyHostScope{label: fmt.Sprintf("[workspaces.%s]", name), workspace: name, hosts: slices.Clone(hosts)})
		}
	}
	return out
}

func uniqueStrings(in []string) []string {
	out := make([]string, 0, len(in))
	seen := make(map[string]bool, len(in))
	for _, s := range in {
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	return out
}

// newStrings returns the entries of add that are not in have, without
// duplicates.
func newStrings(have, add []string) []string {
	var out []string
	for _, s := range add {
		if !slices.Contains(have, s) && !slices.Contains(out, s) {
			out = append(out, s)
		}
	}
	return out
}

func blockedZonesToDeny(zones []string) []string {
	var deny []string
	seen := make(map[string]bool, len(zones)*2)
	add := func(s string) {
		if !seen[s] {
			seen[s] = true
			deny = append(deny, s)
		}
	}
	for _, z := range zones {
		z = strings.TrimSuffix(strings.ToLower(strings.TrimSpace(z)), ".")
		if z != "" {
			add(z)
			add("*." + z)
		}
	}
	return deny
}

func applyIncompatibilities(label string, items []incompatibility, decisions map[string]bool, m *migration) error {
	if len(items) == 0 {
		return nil
	}
	remove, ok := decisions[label]
	if !ok {
		remove = true
	}
	if !remove {
		return fmt.Errorf("%s: the VM backend needs these settings removed; edit config.toml or answer yes", label)
	}
	for _, it := range items {
		it.clear()
		m.changes = append(m.changes, fmt.Sprintf("%s: remove %s", label, it.setting))
	}
	return nil
}

// applyCarryMounts applies the chosen action ("mount", "snapshot", "drop") for
// every mount with a host source.
func applyCarryMounts(label string, mounts, snapshots *[]string, choices []string, stat func(string) (fs.FileInfo, error), m *migration) error {
	var kept []string
	choiceIdx := 0
	for _, spec := range *mounts {
		mt, err := config.ParseMount(spec)
		if err != nil {
			return fmt.Errorf("%s: %w", label, err)
		}
		if mt.Host == "" {
			kept = append(kept, spec)
			continue
		}
		host, err := config.ExpandPath(mt.Host)
		if err != nil {
			return fmt.Errorf("%s: expand %q: %w", label, mt.Host, err)
		}
		isFile := false
		if info, err := stat(host); err == nil && !info.IsDir() {
			isFile = true
		}
		action := "mount"
		if isFile {
			action = "snapshot"
		}
		if choiceIdx < len(choices) && choices[choiceIdx] != "" {
			action = choices[choiceIdx]
		}
		choiceIdx++
		snapshotSpec := mt.Host + ":" + mt.Container
		switch action {
		case "mount":
			if isFile {
				return fmt.Errorf("%s: mount %s is a file; choose snapshot or drop", label, spec)
			}
			kept = append(kept, spec)
		case "snapshot":
			*snapshots = append(*snapshots, snapshotSpec)
			m.changes = append(m.changes, fmt.Sprintf("%s: mount %s -> snapshot %s", label, spec, snapshotSpec))
		case "drop":
			m.changes = append(m.changes, fmt.Sprintf("%s: drop mount %s", label, spec))
		default:
			return fmt.Errorf("%s: unknown mount action %q", label, action)
		}
	}
	*mounts = kept
	return nil
}

// v1OpenCodeData is the guest path of the OpenCode data that jailoc v1 kept in
// the opencode-data-<workspace> volume.
const v1OpenCodeData = "/home/agent/.local/share/opencode"

// volumeTarget is a v1 volume to copy into dst, a path relative to the
// guest data directory of the workspace.
type volumeTarget struct {
	workspace, volume, dst, guestPath string
}

func migrationVolumes(cfg *config.Config, names []string) ([]volumeTarget, error) {
	// jailoc v1 ran OpenCode 1 only.
	v1Agent, _ := agent.Lookup("opencode1")
	var out []volumeTarget
	for _, name := range names {
		mounts, err := config.MergeMounts(v1Agent.DefaultMounts(), cfg.Defaults.Mounts, cfg.Workspaces[name].Mounts)
		if err != nil {
			return nil, fmt.Errorf("workspace %q: %w", name, err)
		}
		for _, v := range []struct{ key, home, guestPath string }{
			{"opencode-data-" + name, "opencode-data", v1OpenCodeData},
			{"opencode-cache-" + name, "cache", guestUserCache},
		} {
			if config.MountsContainTarget(mounts, v.guestPath) {
				continue
			}
			out = append(out, volumeTarget{
				workspace: name,
				volume:    docker.VolumeName(name, v.key),
				dst:       filepath.Join(guestDataHome, v.home),
				guestPath: v.guestPath,
			})
		}
	}
	return out, nil
}

func runMigrate(ctx context.Context, path string, p *migratePrompter) error {
	if p == nil {
		p = newMigratePrompter(false)
	}
	out := p.writer()

	cfg, err := config.ReadRaw(path)
	if err != nil {
		return fmt.Errorf("read config: %w", err)
	}
	legacyHosts := legacyHostScopes(cfg)
	m, err := planMigration(ctx, cfg, p, os.Stat)
	if err != nil {
		return err
	}
	if len(m.changes) == 0 && len(cfg.UnknownKeys()) == 0 {
		_, _ = color.New(color.FgGreen).Fprintln(out, "The configuration is up to date; nothing to migrate.")
		return nil
	}
	if err := config.ValidateCopy(cfg); err != nil {
		return fmt.Errorf("migrated configuration is invalid: %w", err)
	}

	_, _ = fmt.Fprintln(out, "\nChanges to config.toml:")
	for _, c := range m.changes {
		_, _ = fmt.Fprintf(out, "  %s\n", c)
	}
	for _, k := range cfg.UnknownKeys() {
		_, _ = color.New(color.FgYellow).Fprintf(out, "  remove unknown setting %q\n", k)
	}
	resolver := p.resolver
	if resolver == nil {
		resolver = hostResolver
	}
	for _, s := range legacyHosts {
		zones := cfg.Defaults.Network.Get().InternalZones
		if s.workspace != "" {
			zones = slices.Concat(zones, cfg.Workspaces[s.workspace].Network.Get().InternalZones)
		}
		for _, w := range legacyAliasWarnings(ctx, resolver, s.hosts, zones) {
			_, _ = color.New(color.FgYellow).Fprintf(out, "  warning: %s: %s\n", s.label, w)
		}
	}
	if migrateDryRun {
		data, err := config.Encode(cfg)
		if err != nil {
			return err
		}
		_, _ = fmt.Fprintf(out, "\nMigrated config.toml (dry run, nothing changed):\n\n%s", data)
		return nil
	}
	if ok, err := p.confirm(ctx, "Apply the migration?", "", true); err != nil || !ok {
		if err == nil {
			_, _ = fmt.Fprintln(out, "Nothing changed.")
		}
		return err
	}

	if len(m.workspaces) > 0 {
		if err := migrateDockerState(ctx, cfg, m.workspaces, p); err != nil {
			return err
		}
	}

	backup := path + ".pre-vm-migration"
	if _, err := os.Stat(backup); err == nil {
		backup += "." + time.Now().Format("20060102-150405")
	}
	data, err := os.ReadFile(path) //nolint:gosec // G304: path is the jailoc config file
	if err != nil {
		return fmt.Errorf("read config for backup: %w", err)
	}
	if err := os.WriteFile(backup, data, 0o600); err != nil { //nolint:gosec // G703: backup path derives from the jailoc config path
		return fmt.Errorf("write config backup: %w", err)
	}
	if err := config.WriteRaw(path, cfg); err != nil {
		return err
	}
	_, _ = color.New(color.FgGreen).Fprintf(out, "\nMigrated config.toml (previous version: %s).\n", backup)
	if len(m.workspaces) > 0 {
		_, _ = fmt.Fprintln(out, "Start a workspace with `jailoc up <workspace>`; the first start builds the image in a VM.")
	}
	return nil
}

// migrateDockerState stops running Docker workspaces and copies their
// OpenCode volumes into the VM home directories.
func migrateDockerState(ctx context.Context, cfg *config.Config, names []string, p *migratePrompter) error {
	out := p.writer()
	if err := docker.Ping(ctx); err != nil {
		_, _ = color.New(color.FgYellow).Fprintf(out, "\nDocker is not reachable (%v).\nRunning workspaces cannot be checked and OpenCode data in Docker volumes is not copied.\n", err)
		ok, err := p.confirm(ctx, "Continue without Docker?", "", true)
		if err != nil {
			return err
		}
		if !ok {
			return errors.New("migration cancelled")
		}
		return nil
	}

	var running []string
	for _, name := range names {
		containers, err := docker.RunningComposeContainers(ctx, name)
		if err != nil {
			return fmt.Errorf("check workspace %q: %w", name, err)
		}
		if len(containers) > 0 {
			running = append(running, name)
		}
	}
	if len(running) > 0 {
		ok, err := p.confirm(ctx, fmt.Sprintf("Stop the running Docker workspaces %s?", strings.Join(running, ", ")), "", true)
		if err != nil {
			return err
		}
		if !ok {
			return errors.New("stop the running workspaces first (jailoc down), then run migrate again")
		}
		for _, name := range running {
			_, _ = color.New(color.FgCyan).Fprintf(out, "Stopping workspace %s...\n", name)
			if err := docker.StopComposeContainers(ctx, name); err != nil {
				return fmt.Errorf("stop workspace %q: %w", name, err)
			}
		}
	}

	targets, err := migrationVolumes(cfg, names)
	if err != nil {
		return err
	}
	for _, t := range targets {
		if err := migrateVolume(ctx, p, t); err != nil {
			return err
		}
	}
	return nil
}

// migrateVolume copies the volume of t into the guest data directory of
// its workspace. The directory is shared with the VM, so every step goes
// through an os.Root and no link in it leads a write elsewhere.
func migrateVolume(ctx context.Context, p *migratePrompter, t volumeTarget) error {
	out := p.writer()
	root, err := vm.OpenGuestData(vmStateDir(t.workspace))
	if err != nil {
		return fmt.Errorf("workspace %q: %w", t.workspace, err)
	}
	defer func() { _ = root.Close() }()
	if _, err := root.Lstat(t.dst); err == nil {
		_, _ = fmt.Fprintf(out, "%s: %s already has VM data; keeping it\n", t.workspace, t.guestPath)
		return nil
	}
	tmp := t.dst + ".migrating"
	if err := root.RemoveAll(tmp); err != nil {
		return fmt.Errorf("workspace %q: clear %s: %w", t.workspace, tmp, err)
	}
	if err := root.MkdirAll(filepath.Dir(t.dst), 0o700); err != nil {
		return fmt.Errorf("workspace %q: create %s: %w", t.workspace, filepath.Dir(t.dst), err)
	}
	copied, err := docker.ExportVolume(ctx, t.volume, root, tmp)
	if err != nil {
		return fmt.Errorf("workspace %q: copy %s: %w", t.workspace, t.volume, err)
	}
	if !copied {
		return nil
	}
	if err := root.Rename(tmp, t.dst); err != nil {
		return fmt.Errorf("workspace %q: install %s: %w", t.workspace, t.dst, err)
	}
	_, _ = fmt.Fprintf(out, "%s: copied Docker volume %s to %s\n", t.workspace, t.volume, t.guestPath)
	return nil
}
