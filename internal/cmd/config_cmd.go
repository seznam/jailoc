package cmd

import (
	"fmt"
	"os"
	"sort"

	"github.com/fatih/color"
	"github.com/seznam/jailoc/internal/config"
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

	_, _ = fmt.Fprintf(os.Stdout, "\n")

	// Print each workspace
	for _, name := range names {
		ws := cfg.Workspaces[name]

		_, _ = color.New(color.FgCyan, color.Bold).Fprintf(os.Stdout, "Workspace: %s\n", name)

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

		_, _ = fmt.Fprintf(os.Stdout, "\n")
	}

	_ = ctx // silence unused variable warning
	return nil
}

func init() {
	rootCmd.AddCommand(configCmd)
}
