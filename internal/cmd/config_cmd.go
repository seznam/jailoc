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

	// Print image info
	fmt.Fprintf(os.Stdout, "Image: %s:%s\n\n", cfg.Image.Repository, appVersion)

	// Sort workspace names alphabetically
	names := make([]string, 0, len(cfg.Workspaces))
	for name := range cfg.Workspaces {
		names = append(names, name)
	}
	sort.Strings(names)

	// Print each workspace
	for _, name := range names {
		ws := cfg.Workspaces[name]

		fmt.Fprintf(os.Stdout, "Workspace: %s\n", name)

		// Paths
		fmt.Fprintf(os.Stdout, "  Paths:\n")
		if len(ws.Paths) == 0 {
			fmt.Fprintf(os.Stdout, "    (none)\n")
		} else {
			for _, path := range ws.Paths {
				fmt.Fprintf(os.Stdout, "    - %s\n", path)
			}
		}

		// Allowed Hosts
		fmt.Fprintf(os.Stdout, "  Allowed Hosts:\n")
		if len(ws.AllowedHosts) == 0 {
			fmt.Fprintf(os.Stdout, "    (none)\n")
		} else {
			for _, host := range ws.AllowedHosts {
				fmt.Fprintf(os.Stdout, "    - %s\n", host)
			}
		}

		// Allowed Networks
		fmt.Fprintf(os.Stdout, "  Allowed Networks:\n")
		if len(ws.AllowedNetworks) == 0 {
			fmt.Fprintf(os.Stdout, "    (none)\n")
		} else {
			for _, network := range ws.AllowedNetworks {
				fmt.Fprintf(os.Stdout, "    - %s\n", network)
			}
		}

		// Build Context
		buildContext := ws.BuildContext
		if buildContext == "" {
			buildContext = "(none)"
		}
		fmt.Fprintf(os.Stdout, "  Build Context: %s\n", buildContext)

		fmt.Fprintf(os.Stdout, "\n")
	}

	_ = ctx // silence unused variable warning
	return nil
}

func init() {
	rootCmd.AddCommand(configCmd)
}
