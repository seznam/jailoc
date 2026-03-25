package cmd

import (
	"context"
	"fmt"
	"os"
	"sort"

	"github.com/spf13/cobra"
	"github.com/seznam/jailoc/internal/config"
)

var configCmd = &cobra.Command{
	Use:   "config",
	Short: "Show or edit jailoc configuration",
	RunE:  runConfig,
}

func runConfig(cmd *cobra.Command, args []string) error {
	ctx := context.Background()

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
	_, _ = fmt.Fprintf(os.Stdout, "Mode: %s\n", mode)

	baseDockerfile := cfg.Base.Dockerfile
	if baseDockerfile == "" {
		baseDockerfile = "(embedded)"
	}
	_, _ = fmt.Fprintf(os.Stdout, "Base Dockerfile: %s\n", baseDockerfile)

	defaultsImage := cfg.Defaults.Image
	if defaultsImage == "" {
		defaultsImage = "(not set)"
	}
	_, _ = fmt.Fprintf(os.Stdout, "Defaults Image: %s\n", defaultsImage)

	_, _ = fmt.Fprintf(os.Stdout, "Defaults Allowed Hosts:\n")
	if len(cfg.Defaults.AllowedHosts) == 0 {
		_, _ = fmt.Fprintf(os.Stdout, "  (none)\n")
	} else {
		for _, host := range cfg.Defaults.AllowedHosts {
			_, _ = fmt.Fprintf(os.Stdout, "  - %s\n", host)
		}
	}

	_, _ = fmt.Fprintf(os.Stdout, "Defaults Allowed Networks:\n")
	if len(cfg.Defaults.AllowedNetworks) == 0 {
		_, _ = fmt.Fprintf(os.Stdout, "  (none)\n")
	} else {
		for _, network := range cfg.Defaults.AllowedNetworks {
			_, _ = fmt.Fprintf(os.Stdout, "  - %s\n", network)
		}
	}

	_, _ = fmt.Fprintf(os.Stdout, "Defaults Env:\n")
	if len(cfg.Defaults.Env) == 0 {
		_, _ = fmt.Fprintf(os.Stdout, "  (none)\n")
	} else {
		for _, env := range cfg.Defaults.Env {
			_, _ = fmt.Fprintf(os.Stdout, "  - %s\n", env)
		}
	}

	_, _ = fmt.Fprintf(os.Stdout, "Defaults Env Files:\n")
	if len(cfg.Defaults.EnvFile) == 0 {
		_, _ = fmt.Fprintf(os.Stdout, "  (none)\n")
	} else {
		for _, f := range cfg.Defaults.EnvFile {
			_, _ = fmt.Fprintf(os.Stdout, "  - %s\n", f)
		}
	}

	_, _ = fmt.Fprintf(os.Stdout, "\n")

	// Print each workspace
	for _, name := range names {
		ws := cfg.Workspaces[name]

		_, _ = fmt.Fprintf(os.Stdout, "Workspace: %s\n", name)

		_, _ = fmt.Fprintf(os.Stdout, "  Paths:\n")
		if len(ws.Paths) == 0 {
			_, _ = fmt.Fprintf(os.Stdout, "    (none)\n")
		} else {
			for _, path := range ws.Paths {
				_, _ = fmt.Fprintf(os.Stdout, "    - %s\n", path)
			}
		}

		_, _ = fmt.Fprintf(os.Stdout, "  Allowed Hosts:\n")
		if len(ws.AllowedHosts) == 0 {
			_, _ = fmt.Fprintf(os.Stdout, "    (none)\n")
		} else {
			for _, host := range ws.AllowedHosts {
				_, _ = fmt.Fprintf(os.Stdout, "    - %s\n", host)
			}
		}

		_, _ = fmt.Fprintf(os.Stdout, "  Allowed Networks:\n")
		if len(ws.AllowedNetworks) == 0 {
			_, _ = fmt.Fprintf(os.Stdout, "    (none)\n")
		} else {
			for _, network := range ws.AllowedNetworks {
				_, _ = fmt.Fprintf(os.Stdout, "    - %s\n", network)
			}
		}

		buildContext := ws.BuildContext
		if buildContext == "" {
			buildContext = "(none)"
		}
		_, _ = fmt.Fprintf(os.Stdout, "  Build Context: %s\n", buildContext)

		wsImage := ws.Image
		if wsImage == "" {
			wsImage = "(not set)"
		}
		_, _ = fmt.Fprintf(os.Stdout, "  Image: %s\n", wsImage)

		wsDockerfile := ws.Dockerfile
		if wsDockerfile == "" {
			wsDockerfile = "(not set)"
		}
		_, _ = fmt.Fprintf(os.Stdout, "  Dockerfile: %s\n", wsDockerfile)

		_, _ = fmt.Fprintf(os.Stdout, "  Env:\n")
		if len(ws.Env) == 0 {
			_, _ = fmt.Fprintf(os.Stdout, "    (none)\n")
		} else {
			for _, env := range ws.Env {
				_, _ = fmt.Fprintf(os.Stdout, "    - %s\n", env)
			}
		}

		_, _ = fmt.Fprintf(os.Stdout, "  Env Files:\n")
		if len(ws.EnvFile) == 0 {
			_, _ = fmt.Fprintf(os.Stdout, "    (none)\n")
		} else {
			for _, f := range ws.EnvFile {
				_, _ = fmt.Fprintf(os.Stdout, "    - %s\n", f)
			}
		}

		_, _ = fmt.Fprintf(os.Stdout, "\n")
	}

	_ = ctx // silence unused variable warning
	return nil
}

func init() {
	rootCmd.AddCommand(configCmd)
}
