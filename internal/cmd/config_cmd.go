package cmd

import (
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/fatih/color"
	"github.com/seznam/jailoc/internal/agent"
	"github.com/seznam/jailoc/internal/config"
	"github.com/seznam/jailoc/internal/workspace"
	"github.com/spf13/cobra"
)

var configCmd = &cobra.Command{
	Use:   "config",
	Short: "Show or edit jailoc configuration",
	RunE:  runConfig,
}

func runConfig(cmd *cobra.Command, args []string) error {
	ctx := cmd.Context()

	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}
	printDeprecationWarnings(cfg)

	// Sort workspace names alphabetically
	names := make([]string, 0, len(cfg.Workspaces))
	for name := range cfg.Workspaces {
		names = append(names, name)
	}
	sort.Strings(names)

	// Print global settings
	mode := cfg.Mode
	if mode == "" {
		mode = "(auto-detect)"
	}
	_, _ = color.New(color.FgCyan).Fprintf(os.Stdout, "Mode: %s\n", mode)

	baseDockerfile := cfg.Base.Dockerfile
	if baseDockerfile == "" {
		baseDockerfile = "(embedded)"
	}
	_, _ = color.New(color.FgCyan).Fprintf(os.Stdout, "Base Dockerfile: %s\n", baseDockerfile)

	defaultsImage := cfg.Defaults.Image
	if defaultsImage == "" {
		defaultsImage = "(not set)"
	}
	_, _ = color.New(color.FgCyan).Fprintf(os.Stdout, "Defaults Image: %s\n", defaultsImage)

	defaultsAgent := cfg.Defaults.Agent
	if defaultsAgent == "" {
		defaultsAgent = "(not set, default: " + agent.Default + ")"
	}
	_, _ = color.New(color.FgCyan).Fprintf(os.Stdout, "Defaults Agent: %s\n", defaultsAgent)

	_, _ = color.New(color.FgCyan, color.Bold).Fprintf(os.Stdout, "Defaults Allowed Hosts:\n")
	if len(cfg.Defaults.AllowedHosts) == 0 {
		_, _ = color.New(color.FgHiBlack).Fprintf(os.Stdout, "  (none)\n")
	} else {
		for _, host := range cfg.Defaults.AllowedHosts {
			_, _ = fmt.Fprintf(os.Stdout, "  - %s\n", host)
		}
	}

	_, _ = color.New(color.FgCyan, color.Bold).Fprintf(os.Stdout, "Defaults Allowed Networks:\n")
	if len(cfg.Defaults.AllowedNetworks) == 0 {
		_, _ = color.New(color.FgHiBlack).Fprintf(os.Stdout, "  (none)\n")
	} else {
		for _, network := range cfg.Defaults.AllowedNetworks {
			_, _ = fmt.Fprintf(os.Stdout, "  - %s\n", network)
		}
	}

	_, _ = color.New(color.FgCyan, color.Bold).Fprintf(os.Stdout, "Defaults Network Rules:\n")
	printNetworkRules(cfg.Defaults.Network.Get().Rules, "  ")

	_, _ = color.New(color.FgCyan, color.Bold).Fprintf(os.Stdout, "Defaults Env:\n")
	if len(cfg.Defaults.Env) == 0 {
		_, _ = color.New(color.FgHiBlack).Fprintf(os.Stdout, "  (none)\n")
	} else {
		for _, env := range cfg.Defaults.Env {
			_, _ = fmt.Fprintf(os.Stdout, "  - %s\n", env)
		}
	}

	_, _ = color.New(color.FgCyan, color.Bold).Fprintf(os.Stdout, "Defaults Env Files:\n")
	if len(cfg.Defaults.EnvFile) == 0 {
		_, _ = color.New(color.FgHiBlack).Fprintf(os.Stdout, "  (none)\n")
	} else {
		for _, f := range cfg.Defaults.EnvFile {
			_, _ = fmt.Fprintf(os.Stdout, "  - %s\n", f)
		}
	}

	_, _ = color.New(color.FgCyan, color.Bold).Fprintf(os.Stdout, "Defaults Secrets:\n")
	printSecrets(cfg.Defaults.Secrets, "  ")

	defaultsCPU := "(not set, default: 2.0)"
	if cfg.Defaults.CPU != nil {
		defaultsCPU = fmt.Sprintf("%g", *cfg.Defaults.CPU)
	}
	_, _ = color.New(color.FgCyan).Fprintf(os.Stdout, "Defaults CPU: %s\n", defaultsCPU)

	defaultsMemory := "(not set, default: 4g)"
	if cfg.Defaults.Memory != nil {
		defaultsMemory = *cfg.Defaults.Memory
	}
	_, _ = color.New(color.FgCyan).Fprintf(os.Stdout, "Defaults Memory: %s\n", defaultsMemory)

	defaultsDockerDisk := "(not set, default: 20g)"
	if cfg.Defaults.DockerDisk != nil {
		defaultsDockerDisk = *cfg.Defaults.DockerDisk
	}
	_, _ = color.New(color.FgCyan).Fprintf(os.Stdout, "Defaults Docker Disk: %s\n", defaultsDockerDisk)

	defaultsTimeZone := "(not set, default: host time zone)"
	if cfg.Defaults.TimeZone != nil {
		defaultsTimeZone = *cfg.Defaults.TimeZone
	}
	_, _ = color.New(color.FgCyan).Fprintf(os.Stdout, "Defaults Time Zone: %s\n", defaultsTimeZone)

	_, _ = fmt.Fprintf(os.Stdout, "\n")

	// Print each workspace
	for _, name := range names {
		ws := cfg.Workspaces[name]

		_, _ = color.New(color.FgCyan, color.Bold).Fprintf(os.Stdout, "Workspace: %s\n", name)

		wsAgent := ws.Agent
		if wsAgent == "" {
			wsAgent = "(not set)"
		}
		_, _ = color.New(color.FgCyan).Fprintf(os.Stdout, "  Agent: %s\n", wsAgent)

		_, _ = color.New(color.FgCyan).Fprintf(os.Stdout, "  Paths:\n")
		if len(ws.Paths) == 0 {
			_, _ = color.New(color.FgHiBlack).Fprintf(os.Stdout, "    (none)\n")
		} else {
			for _, path := range ws.Paths {
				_, _ = fmt.Fprintf(os.Stdout, "    - %s\n", path)
			}
		}

		_, _ = color.New(color.FgCyan).Fprintf(os.Stdout, "  Allowed Hosts:\n")
		if len(ws.AllowedHosts) == 0 {
			_, _ = color.New(color.FgHiBlack).Fprintf(os.Stdout, "    (none)\n")
		} else {
			for _, host := range ws.AllowedHosts {
				_, _ = fmt.Fprintf(os.Stdout, "    - %s\n", host)
			}
		}

		_, _ = color.New(color.FgCyan).Fprintf(os.Stdout, "  Allowed Networks:\n")
		if len(ws.AllowedNetworks) == 0 {
			_, _ = color.New(color.FgHiBlack).Fprintf(os.Stdout, "    (none)\n")
		} else {
			for _, network := range ws.AllowedNetworks {
				_, _ = fmt.Fprintf(os.Stdout, "    - %s\n", network)
			}
		}

		_, _ = color.New(color.FgCyan).Fprintf(os.Stdout, "  Network Rules:\n")
		printNetworkRules(ws.Network.Get().Rules, "    ")

		buildContext := ws.BuildContext
		if buildContext == "" {
			buildContext = "(none)"
		}
		_, _ = color.New(color.FgCyan).Fprintf(os.Stdout, "  Build Context: %s\n", buildContext)

		wsImage := ws.Image
		if wsImage == "" {
			wsImage = "(not set)"
		}
		_, _ = color.New(color.FgCyan).Fprintf(os.Stdout, "  Image: %s\n", wsImage)

		wsDockerfile := ws.Dockerfile
		if wsDockerfile == "" {
			wsDockerfile = "(not set)"
		}
		_, _ = color.New(color.FgCyan).Fprintf(os.Stdout, "  Dockerfile: %s\n", wsDockerfile)

		_, _ = color.New(color.FgCyan).Fprintf(os.Stdout, "  Env:\n")
		if len(ws.Env) == 0 {
			_, _ = color.New(color.FgHiBlack).Fprintf(os.Stdout, "    (none)\n")
		} else {
			for _, env := range ws.Env {
				_, _ = fmt.Fprintf(os.Stdout, "    - %s\n", env)
			}
		}

		_, _ = color.New(color.FgCyan).Fprintf(os.Stdout, "  Env Files:\n")
		if len(ws.EnvFile) == 0 {
			_, _ = color.New(color.FgHiBlack).Fprintf(os.Stdout, "    (none)\n")
		} else {
			for _, f := range ws.EnvFile {
				_, _ = fmt.Fprintf(os.Stdout, "    - %s\n", f)
			}
		}

		_, _ = color.New(color.FgCyan).Fprintf(os.Stdout, "  Secrets:\n")
		printSecrets(ws.Secrets, "    ")

		wsCPU := "(not set)"
		if ws.CPU != nil {
			wsCPU = fmt.Sprintf("%g", *ws.CPU)
		}
		_, _ = color.New(color.FgCyan).Fprintf(os.Stdout, "  CPU: %s\n", wsCPU)

		wsMemory := "(not set)"
		if ws.Memory != nil {
			wsMemory = *ws.Memory
		}
		_, _ = color.New(color.FgCyan).Fprintf(os.Stdout, "  Memory: %s\n", wsMemory)

		wsDockerDisk := "(not set)"
		if ws.DockerDisk != nil {
			wsDockerDisk = *ws.DockerDisk
		}
		_, _ = color.New(color.FgCyan).Fprintf(os.Stdout, "  Docker Disk: %s\n", wsDockerDisk)

		wsTimeZone := "(not set)"
		if ws.TimeZone != nil {
			wsTimeZone = *ws.TimeZone
		}
		_, _ = color.New(color.FgCyan).Fprintf(os.Stdout, "  Time Zone: %s\n", wsTimeZone)

		_, _ = fmt.Fprintf(os.Stdout, "\n")
	}

	_ = ctx // silence unused variable warning
	return nil
}

func printDeprecationWarnings(cfg *config.Config) {
	for _, w := range config.DeprecationWarnings(cfg) {
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
		_, _ = fmt.Fprintf(os.Stdout, "%s- %s%s %s (%s%s)\n", indent, prefix, action, strings.Join(targets, ", "), proto, ports)
	}
}

// printSecrets writes one [<scope>.secrets] block as source references.
// Ordering comes from workspace.FlattenSecrets, the single definition of
// secret order.
func printSecrets(secrets config.Secrets, indent string) {
	resolved := workspace.FlattenSecrets(secrets)
	if len(resolved) == 0 {
		_, _ = color.New(color.FgHiBlack).Fprintf(os.Stdout, "%s(none)\n", indent)
		return
	}
	for _, secret := range resolved {
		_, _ = fmt.Fprintf(os.Stdout, "%s- %s\n", indent, secretReference(secret))
	}
}

func init() {
	rootCmd.AddCommand(configCmd)
}
