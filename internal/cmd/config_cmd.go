package cmd

import (
	"cmp"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"github.com/fatih/color"
	"github.com/seznam/jailoc/internal/config"
	"github.com/seznam/jailoc/internal/workspace"
	"github.com/spf13/cobra"
)

var configCmd = &cobra.Command{
	Use:   "config [workspace]",
	Short: "Show resolved configuration for a workspace",
	Args:  cobra.MaximumNArgs(1),
	RunE:  runConfig,
}

func runConfig(cmd *cobra.Command, args []string) error {
	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}
	printConfigWarnings(cfg)

	wss, err := targetWorkspaces(cfg, args)
	if err != nil {
		return err
	}
	first := true
	return forEachWorkspace(cmd.Context(), wss, func(ws *workspace.Resolved) error {
		if !first {
			_, _ = fmt.Fprintln(os.Stdout)
		}
		first = false
		printWorkspaceConfig(cfg, ws)
		return nil
	})
}

func printWorkspaceConfig(cfg *config.Config, ws *workspace.Resolved) {
	cyanBold := color.New(color.FgCyan, color.Bold)
	cyan := color.New(color.FgCyan)

	_, _ = cyanBold.Fprintf(os.Stdout, "Workspace: %s\n", ws.Name)
	_, _ = cyan.Fprintf(os.Stdout, "  Agent: %s\n", agentLabel(ws))
	_, _ = cyan.Fprintf(os.Stdout, "  Profile: %s\n", resolvedProfileLabel(ws))
	printStringList("  Paths", ws.Paths, "    ")
	printStringList("  Mounts", ws.Mounts, "    ")
	printStringList("  Snapshots", slices.DeleteFunc(slices.Clone(ws.Snapshots), func(s string) bool { return strings.HasPrefix(s, ":") }), "    ")
	_, _ = cyan.Fprintf(os.Stdout, "  Image: %s\n", resolvedImageLabel(cfg, ws))
	_, _ = cyan.Fprintf(os.Stdout, "  Dockerfile: %s\n", cmp.Or(ws.Dockerfile, "(none)"))
	_, _ = cyan.Fprintf(os.Stdout, "  Build Context: %s\n", cmp.Or(ws.BuildContext, "(none)"))
	printStringList("  Env", ws.Env, "    ")
	_, _ = cyan.Fprintf(os.Stdout, "  Secrets:\n")
	printResolvedSecrets(ws.Secrets, "    ")
	_, _ = cyan.Fprintf(os.Stdout, "  Network:\n")
	_, _ = cyan.Fprintf(os.Stdout, "    IPv6: %t\n", ws.IPv6)
	_, _ = cyan.Fprintf(os.Stdout, "    DNS: %s\n", cmp.Or(ws.DNSUpstream, "(default)"))
	printStringList("    Internal zones", ws.InternalZones, "      ")
	_, _ = cyan.Fprintf(os.Stdout, "    Rules:\n")
	printNetworkRules(ws.NetworkRules, "      ")
	_, _ = cyan.Fprintf(os.Stdout, "    Upstream Proxy:\n")
	printUpstreamProxy(ws.UpstreamProxy, "      ")
	_, _ = cyan.Fprintf(os.Stdout, "  Ports:\n")
	printPortForwards(ws.Ports, "    ")
	_, _ = cyan.Fprintf(os.Stdout, "  Host Ports:\n")
	printHostPortTargets(ws.HostPorts, "    ")
	_, _ = cyan.Fprintf(os.Stdout, "  CPU: %g\n", ws.CPU)
	_, _ = cyan.Fprintf(os.Stdout, "  Memory: %s\n", ws.Memory)
	_, _ = cyan.Fprintf(os.Stdout, "  Docker Disk: %s\n", ws.DockerDisk)
	_, _ = cyan.Fprintf(os.Stdout, "  State Disk: %s\n", ws.StateDisk)
	_, _ = cyan.Fprintf(os.Stdout, "  Cache Disk: %s\n", ws.CacheDisk)
	_, _ = cyan.Fprintf(os.Stdout, "  Tmp: %s\n", resolvedTmpLabel(ws))
	_, _ = cyan.Fprintf(os.Stdout, "  Time Zone: %s\n", cmp.Or(ws.TimeZone, "(host time zone)"))
	_, _ = cyan.Fprintf(os.Stdout, "  SSH Agent: %t\n", ws.SSHAuthSock)
	_, _ = cyan.Fprintf(os.Stdout, "  GPG Agent: %t\n", ws.GPGAgent)
	_, _ = cyan.Fprintf(os.Stdout, "  Git Config: %t\n", ws.GitConfig)
	_, _ = cyan.Fprintf(os.Stdout, "  Import Agent Config: %t\n", ws.ImportAgentConfig)
	_, _ = cyan.Fprintf(os.Stdout, "  CA Bundle: %s\n", caBundleLabel(ws.CABundle))
	_, _ = cyan.Fprintf(os.Stdout, "  Enable Docker: %t\n", ws.EnableDocker)
	_, _ = cyan.Fprintf(os.Stdout, "  Proxy:\n")
	printProxyRoutes(ws.Name, ws.Proxy, ws.ProxyPorts.HTTPS, "    ")
	_, _ = cyan.Fprintf(os.Stdout, "  Protect Git: %t\n", ws.ProtectGit)
	_, _ = cyan.Fprintf(os.Stdout, "  Isolated Build: %t\n", ws.IsolatedBuild)
	_, _ = cyan.Fprintf(os.Stdout, "  Notifications: %s\n", ws.Notifications)
	_, _ = cyan.Fprintf(os.Stdout, "  Computed Paths:\n")
	_, _ = cyan.Fprintf(os.Stdout, "    Config File: %s\n", config.ConfigPath())
	_, _ = cyan.Fprintf(os.Stdout, "    Workspace Config Dir: %s\n", filepath.Join(config.ConfigDir(), "workspaces", ws.Name))
	_, _ = cyan.Fprintf(os.Stdout, "    Workspace Cache Dir: %s\n", workspaceCacheDir(ws.Name))
	_, _ = cyan.Fprintf(os.Stdout, "    VM State Dir: %s\n", vmStateDir(ws.Name))
	_, _ = cyan.Fprintf(os.Stdout, "    Profile Dir: %s\n", computedProfileDir(ws))
}

// agentLabel names the agent of the workspace, the package or release
// version jailoc installs and the arguments added to every start.
func agentLabel(ws *workspace.Resolved) string {
	label := ws.Agent.Name
	switch {
	case !ws.AgentInstall:
		label += " (installed by the image)"
	case ws.Agent.Package != "":
		label += " (" + ws.Agent.Package + "@" + cmp.Or(ws.Agent.Version, "latest") + ")"
	case ws.Agent.Release != nil:
		label += " (" + ws.Agent.Release.Repo + "@" + ws.Agent.Version + ")"
	}
	if len(ws.AgentArgs) > 0 {
		label += ", args: " + strings.Join(ws.AgentArgs, " ")
	}
	return label
}

func resolvedProfileLabel(ws *workspace.Resolved) string {
	if name := workspaceProfile(ws); name != "" {
		return name
	}
	if ws.AgentProfile != nil && ws.AgentProfile.Ephemeral() {
		return "none (ephemeral)"
	}
	return "workspace"
}

func resolvedImageLabel(cfg *config.Config, ws *workspace.Resolved) string {
	if ws.Image != "" {
		return ws.Image
	}
	if cfg.Defaults.Image != "" {
		return cfg.Defaults.Image
	}
	if cfg.Base.Dockerfile != "" {
		return "(base dockerfile: " + cfg.Base.Dockerfile + ")"
	}
	return "(embedded base)"
}

func caBundleLabel(b config.CABundle) string {
	if path, ok := b.Path(); ok {
		return path
	}
	if b.Automatic() {
		return "auto"
	}
	return "false"
}

func computedProfileDir(ws *workspace.Resolved) string {
	overrides := map[string]string{}
	for _, p := range ws.Paths {
		overrides[filepath.Clean(p)] = filepath.Clean(p)
	}
	for _, spec := range ws.Mounts {
		m, err := config.ParseMount(spec)
		if err != nil || m.Host == "" {
			continue
		}
		host := m.Host
		if expanded, err := config.ExpandPath(host); err == nil {
			host = expanded
		}
		overrides[filepath.Clean(m.Container)] = filepath.Clean(host)
	}
	name := workspaceProfile(ws)
	ephemeral := ws.AgentProfile != nil && ws.AgentProfile.Ephemeral()
	// Same file as vm.StateDiskPath; this file builds on hosts without VMs.
	stateDisk := filepath.Join(vmStateDir(ws.Name), "state.img")
	fallback := stateDisk
	if name != "" {
		fallback = filepath.Join(profilesDir(), name)
	} else if ephemeral {
		fallback = stateDisk + ":ephemeral (ephemeral)"
	}
	if len(ws.Agent.State) == 0 {
		return fallback
	}
	sources := make([]string, 0, len(ws.Agent.State))
	anyOverridden := false
	for _, d := range ws.Agent.State {
		if host, ok := overrides[filepath.Clean(d.Path)]; ok {
			anyOverridden = true
			sources = append(sources, host)
			continue
		}
		switch {
		case name != "":
			sources = append(sources, filepath.Join(profilesDir(), name, d.Name))
		case ephemeral:
			sources = append(sources, stateDisk+":ephemeral/"+d.Name+" (ephemeral)")
		default:
			sources = append(sources, stateDisk+":"+d.Name)
		}
	}
	if !anyOverridden {
		return fallback
	}
	return strings.Join(sources, ", ")
}

func vmRootDir() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return filepath.Join(os.TempDir(), "jailoc-vm")
	}
	return filepath.Join(home, ".cache", "jailoc-vm")
}

func vmStateDir(name string) string { return filepath.Join(vmRootDir(), "workspaces", name) }

// profilesDir holds the named agent profiles shared by workspaces.
func profilesDir() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return filepath.Join(os.TempDir(), "jailoc-profiles")
	}
	return filepath.Join(home, ".local", "share", "jailoc", "profiles")
}

// workspaceProfile is the named profile of the workspace's agent, or "".
func workspaceProfile(ws *workspace.Resolved) string {
	if ws.AgentProfile == nil {
		return ""
	}
	name, _ := ws.AgentProfile.Name()
	return name
}

func printStringList(header string, items []string, indent string) {
	_, _ = color.New(color.FgCyan).Fprintf(os.Stdout, "%s:\n", header)
	if len(items) == 0 {
		_, _ = color.New(color.FgHiBlack).Fprintf(os.Stdout, "%s(none)\n", indent)
		return
	}
	for _, item := range items {
		_, _ = fmt.Fprintf(os.Stdout, "%s- %s\n", indent, item)
	}
}

func printPortForwards(ports []config.PortForward, indent string) {
	if len(ports) == 0 {
		_, _ = color.New(color.FgHiBlack).Fprintf(os.Stdout, "%s(none)\n", indent)
		return
	}
	for _, p := range ports {
		_, _ = fmt.Fprintf(os.Stdout, "%s- 127.0.0.1:%d -> %d\n", indent, p.Host, p.Guest)
	}
}

func printHostPortTargets(ports []config.HostPortTarget, indent string) {
	if len(ports) == 0 {
		_, _ = color.New(color.FgHiBlack).Fprintf(os.Stdout, "%s(none)\n", indent)
		return
	}
	for _, p := range ports {
		target := fmt.Sprintf("127.0.0.1:%d", p.HostPort)
		if p.Socket != "" {
			target = p.Socket
		}
		_, _ = fmt.Fprintf(os.Stdout, "%s- %d -> %s\n", indent, p.Guest, target)
	}
}

// proxyHostSuffix is the DNS zone of the workspace proxy routes: a route is served
// at https://<route>.<workspace>.jailoc.local.
const proxyHostSuffix = "jailoc.local"

func printProxyRoutes(name string, routes []workspace.ProxyRoute, httpsPort int, indent string) {
	if len(routes) == 0 {
		_, _ = color.New(color.FgHiBlack).Fprintf(os.Stdout, "%s(none)\n", indent)
		return
	}
	for _, r := range routes {
		url := strings.TrimSuffix(proxyURL(r.Name+"."+name+"."+proxyHostSuffix, httpsPort), "/")
		if r.Auth {
			_, _ = fmt.Fprintf(os.Stdout, "%s- %s -> %d (auth)\n", indent, url, r.Port)
		} else {
			_, _ = fmt.Fprintf(os.Stdout, "%s- %s -> %d\n", indent, url, r.Port)
		}
	}
}

// proxyOrigin is the origin the proxy accepts for host: the port is left out
// when it is the default HTTPS port (or unknown), as browsers do.
func proxyOrigin(host string, httpsPort int) string {
	if httpsPort == 0 || httpsPort == config.DefaultProxyHTTPSPort {
		return "https://" + host
	}
	return "https://" + net.JoinHostPort(host, strconv.Itoa(httpsPort))
}

// proxyURL is the address of the root page of host.
func proxyURL(host string, httpsPort int) string { return proxyOrigin(host, httpsPort) + "/" }

func printConfigWarnings(cfg *config.Config) {
	for _, k := range cfg.UnknownKeys() {
		_, _ = color.New(color.FgYellow).Fprintf(os.Stderr, "WARNING: unknown setting %q in config.toml is ignored\n", k)
	}
	warnings := config.DeprecationWarnings(cfg)
	for _, w := range warnings {
		_, _ = color.New(color.FgYellow).Fprintf(os.Stderr, "WARNING: %s\n", w)
	}
	if len(warnings) > 0 {
		_, _ = color.New(color.FgYellow).Fprintln(os.Stderr, "Run `jailoc migrate` to update deprecated settings in config.toml.")
	}
	for _, w := range workspace.ConfigWarnings(cfg) {
		_, _ = color.New(color.FgYellow).Fprintf(os.Stderr, "WARNING: %s\n", w)
	}
}

func printNetworkRules(rules []config.NetworkRule, indent string) {
	if len(rules) == 0 {
		_, _ = color.New(color.FgHiBlack).Fprintf(os.Stdout, "%s(none)\n", indent)
		return
	}
	for _, r := range rules {
		action := "allow"
		targets := r.Allow
		if len(r.Deny) > 0 {
			action = "deny"
			targets = r.Deny
		}
		prefix := ""
		if r.Name != "" {
			prefix = r.Name + ": "
		}
		proto := r.Proto
		if proto == "" {
			proto = config.RuleProtoTCP
		}
		ports := ""
		if len(r.Ports) > 0 {
			ps := make([]string, len(r.Ports))
			for i, p := range r.Ports {
				ps[i] = string(p)
			}
			ports = " ports=" + strings.Join(ps, ",")
		}
		httpMatch := ""
		if r.HTTP != nil {
			httpMatch = " http"
			if len(r.HTTP.Methods) > 0 {
				httpMatch += " methods=" + strings.Join(r.HTTP.Methods, ",")
			}
			if len(r.HTTP.Paths) > 0 {
				httpMatch += " paths=" + strings.Join(r.HTTP.Paths, ",")
			}
		}
		quiet := ""
		if r.Quiet() {
			quiet = " log=false"
		}
		_, _ = fmt.Fprintf(os.Stdout, "%s- %s%s %s (%s%s%s%s)\n", indent, prefix, action, strings.Join(targets, ", "), proto, ports, httpMatch, quiet)
	}
}

// printSecrets writes one [<scope>.secrets] block as source references.
// Ordering comes from workspace.FlattenSecrets, the single definition of
// secret order.
func printSecrets(secrets config.Secrets, indent string) {
	printResolvedSecrets(workspace.FlattenSecrets(secrets), indent)
}

func printResolvedSecrets(resolved []workspace.ResolvedSecret, indent string) {
	if len(resolved) == 0 {
		_, _ = color.New(color.FgHiBlack).Fprintf(os.Stdout, "%s(none)\n", indent)
		return
	}
	for _, secret := range resolved {
		_, _ = fmt.Fprintf(os.Stdout, "%s- %s\n", indent, secretReference(secret))
	}
}

// printUpstreamProxy prints the upstream proxy of a workspace with its
// password source as a reference, like secretReference: the variable name,
// path or command, never the value.
func printUpstreamProxy(p *config.UpstreamProxy, indent string) {
	if p == nil {
		_, _ = color.New(color.FgHiBlack).Fprintf(os.Stdout, "%s(none)\n", indent)
		return
	}
	if p.URL != "" {
		_, _ = fmt.Fprintf(os.Stdout, "%sURL: %s\n", indent, p.URL)
	} else {
		_, _ = fmt.Fprintf(os.Stdout, "%sCommand: %q\n", indent, p.Command)
	}
	if p.CA != "" {
		_, _ = fmt.Fprintf(os.Stdout, "%sCA: %s\n", indent, p.CA)
	}
	printStringList(indent+"Direct", p.Direct, indent+"  ")
	if a := p.Auth; a != nil {
		ref := ""
		switch {
		case a.FromEnv != "":
			ref = "from_env " + a.FromEnv
		case a.FromFile != "":
			ref = "from_file " + a.FromFile
		default:
			ref = fmt.Sprintf("from_command %q", a.FromCommand)
			if a.TTL != "" {
				ref += ", ttl " + a.TTL
			}
		}
		_, _ = fmt.Fprintf(os.Stdout, "%sAuth: %s (%s)\n", indent, a.Username, ref)
	}
}

func init() {
	addAllFlag(configCmd, "show the resolved configuration of all configured workspaces")
	rootCmd.AddCommand(configCmd)
}
