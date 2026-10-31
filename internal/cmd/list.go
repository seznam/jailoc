package cmd

import (
	"context"
	"fmt"
	"io"
	"os"
	"slices"
	"strings"
	"sync"
	"text/tabwriter"
	"time"

	"github.com/seznam/jailoc/internal/config"
	"github.com/seznam/jailoc/internal/workspace"
	"github.com/spf13/cobra"
)

// listSessionsTimeout bounds how long list waits for the sessions of one
// running workspace VM.
const listSessionsTimeout = 5 * time.Second

var listCmd = &cobra.Command{
	Use:     "list",
	Aliases: []string{"ls"},
	Short:   "List configured workspaces and their status",
	Args:    cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		cfg, err := config.Load()
		if err != nil {
			return fmt.Errorf("load config: %w", err)
		}
		current := ""
		if cwd, err := os.Getwd(); err == nil {
			if ws, _, err := workspace.ResolveFromCWD(cfg, cwd); err == nil {
				current = ws.Name
			}
		}
		home, _ := os.UserHomeDir() // without a home directory, paths are shown in full
		return listWorkspaces(cmd.Context(), os.Stdout, cfg, current, home)
	},
}

// workspaceRow is one line of jailoc list.
type workspaceRow struct {
	name, agent, state, sessions, paths string
}

// listWorkspaces prints every configured workspace with its agent, VM state,
// agent sessions and paths; current marks the workspace of the working
// directory.
func listWorkspaces(ctx context.Context, w io.Writer, cfg *config.Config, current, home string) error {
	names := make([]string, 0, len(cfg.Workspaces))
	for name := range cfg.Workspaces {
		names = append(names, name)
	}
	slices.Sort(names)

	rows := make([]workspaceRow, len(names))
	var wg sync.WaitGroup
	for i, name := range names {
		wg.Go(func() { rows[i] = workspaceListRow(ctx, cfg, name, home) })
	}
	wg.Wait()

	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	_, _ = fmt.Fprintln(tw, "  WORKSPACE\tAGENT\tSTATE\tSESSIONS\tPATHS")
	for _, r := range rows {
		mark := " "
		if r.name == current {
			mark = "*"
		}
		_, _ = fmt.Fprintf(tw, "%s %s\t%s\t%s\t%s\t%s\n", mark, r.name, r.agent, r.state, r.sessions, r.paths)
	}
	if err := tw.Flush(); err != nil {
		return fmt.Errorf("print workspaces: %w", err)
	}
	return nil
}

func workspaceListRow(ctx context.Context, cfg *config.Config, name, home string) workspaceRow {
	r := workspaceRow{name: name, agent: "-", state: "-", sessions: "-", paths: "-"}
	ws, err := workspace.Resolve(cfg, name)
	if err != nil {
		r.state = "invalid: " + err.Error()
		return r
	}
	r.agent = ws.Agent.Name
	if len(ws.Paths) > 0 {
		paths := make([]string, len(ws.Paths))
		for i, p := range ws.Paths {
			paths[i] = shortenHome(p, home)
		}
		r.paths = strings.Join(paths, ", ")
	}

	ctx, cancel := context.WithTimeout(ctx, listSessionsTimeout)
	defer cancel()
	running, total, attached, err := workspaceStatus(ctx, ws)
	switch {
	case err != nil && !running:
		r.state = "unknown"
		return r
	case !running:
		r.state = "stopped"
		return r
	}
	r.state = "running"
	if err != nil {
		r.sessions = "?"
		return r
	}
	r.sessions = fmt.Sprint(total)
	if attached > 0 {
		r.sessions = fmt.Sprintf("%d (%d attached)", total, attached)
	}
	return r
}

// shortenHome replaces the home directory prefix of path with ~.
func shortenHome(path, home string) string {
	if home == "" {
		return path
	}
	if path == home {
		return "~"
	}
	if rest, ok := strings.CutPrefix(path, home+string(os.PathSeparator)); ok {
		return "~" + string(os.PathSeparator) + rest
	}
	return path
}

func init() {
	rootCmd.AddCommand(listCmd)
}
