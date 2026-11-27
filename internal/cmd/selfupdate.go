package cmd

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	goversion "github.com/hashicorp/go-version"
	"github.com/seznam/jailoc/internal/update"
	"github.com/spf13/cobra"
)

const selfUpdateName = "self-update"

// selfUpdateFunc is the updater the command drives; tests pass their own.
type selfUpdateFunc func(ctx context.Context, current string, opts update.Options) (update.Report, error)

func newSelfUpdateCmd() *cobra.Command {
	return newSelfUpdateCmdWith(update.SelfUpdate)
}

func newSelfUpdateCmdWith(run selfUpdateFunc) *cobra.Command {
	var opts update.Options
	cmd := &cobra.Command{
		Use:   selfUpdateName,
		Short: "Update the jailoc executable to the latest release",
		Long: "Download the latest eligible jailoc release from GitHub and replace the running executable.\n" +
			"Running workspaces keep using the previous version until restarted.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			rep, err := run(cmd.Context(), appVersion, opts)
			if err != nil {
				return fmt.Errorf("self-update: %w", err)
			}
			writeSelfUpdateReport(cmd.OutOrStdout(), rep, opts, time.Now())
			if rep.Warning != "" {
				_, _ = fmt.Fprintf(cmd.ErrOrStderr(), "Warning: %s\n", rep.Warning)
			}
			if rep.Installed {
				warnRunningWorkspaces(cmd.ErrOrStderr(), filepath.Join(vmRootDir(), "workspaces"))
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&opts.Check, "check", false, "only check for an update, do not install it")
	cmd.Flags().StringVar(&opts.Version, "version", "", "install this release tag instead of the latest eligible one")
	cmd.Flags().BoolVar(&opts.NoCooldown, "no-cooldown", false, "ignore the cooldown for recently published releases")
	cmd.Flags().BoolVar(&opts.Force, "force", false, "install even if the executable is not a plain release install")
	return cmd
}

func init() {
	rootCmd.AddCommand(newSelfUpdateCmd())
}

// writeSelfUpdateReport prints what the updater did or found. It states only
// the assurances the report carries.
func writeSelfUpdateReport(w io.Writer, rep update.Report, opts update.Options, now time.Time) {
	published := ""
	if !rep.PublishedAt.IsZero() {
		published = " (published " + formatAge(now.Sub(rep.PublishedAt)) + ")"
	}

	switch {
	case rep.Installed:
		_, _ = fmt.Fprintf(w, "Updated jailoc %s -> %s%s\n", rep.Current, rep.Target, published)
		_, _ = fmt.Fprintf(w, "  Executable: %s\n", rep.Path)
		_, _ = fmt.Fprintln(w, "  Integrity: archive checksum matched the GitHub-reported release digest")
		if rep.VerifiedSignature {
			_, _ = fmt.Fprintln(w, "  Signature: verified")
		} else {
			_, _ = fmt.Fprintln(w, "  Provenance: reported by GitHub, not verified locally")
		}
	case rep.Target == "" && rep.Pending != "":
		_, _ = fmt.Fprintf(w, "No release is eligible yet; jailoc %s stays installed.\n", rep.Current)
	case rep.Target == "":
		_, _ = fmt.Fprintf(w, "jailoc %s is up to date.\n", rep.Current)
	case sameVersion(rep.Current, rep.Target):
		_, _ = fmt.Fprintf(w, "jailoc %s is already at %s.\n", rep.Current, rep.Target)
	default:
		label := "Update available"
		if opts.Version != "" {
			label = "Selected release"
		}
		_, _ = fmt.Fprintf(w, "%s: %s -> %s%s\n", label, rep.Current, rep.Target, published)
		if !opts.Check {
			_, _ = fmt.Fprintln(w, "  Not installed.")
		}
	}

	if opts.Check && !rep.Installed {
		if rep.Installation != "" {
			_, _ = fmt.Fprintf(w, "  Installation: %s\n", rep.Installation)
		}
		if rep.Path != "" {
			_, _ = fmt.Fprintf(w, "  Executable: %s\n", rep.Path)
		}
	}

	if rep.Pending != "" {
		_, _ = fmt.Fprintf(w, "Release %s is newer but still within the cooldown; use --no-cooldown to install it now.\n", rep.Pending)
	}
}

// sameVersion compares release tags ignoring a leading "v"; non-version
// strings such as "dev" compare as plain text.
func sameVersion(a, b string) bool {
	va, errA := goversion.NewVersion(a)
	vb, errB := goversion.NewVersion(b)
	if errA == nil && errB == nil {
		return va.Equal(vb)
	}
	return a == b
}

func formatAge(d time.Duration) string {
	switch {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		return pluralAgo(int(d/time.Minute), "minute")
	case d < 48*time.Hour:
		return pluralAgo(int(d/time.Hour), "hour")
	default:
		return pluralAgo(int(d/(24*time.Hour)), "day")
	}
}

func pluralAgo(n int, unit string) string {
	if n == 1 {
		return "1 " + unit + " ago"
	}
	return fmt.Sprintf("%d %ss ago", n, unit)
}

// warnRunningWorkspaces tells the user which running workspace VMs still use
// the previous version. Failures are warnings: the update itself succeeded.
func warnRunningWorkspaces(w io.Writer, workspacesDir string) {
	running, failed, err := scanRunningWorkspaces(workspacesDir)
	if err != nil {
		_, _ = fmt.Fprintf(w, "Updated executable, but could not check running workspaces: %v\n", err)
	}
	if len(failed) > 0 {
		_, _ = fmt.Fprintf(w, "Updated executable, but could not read the state of workspaces: %s\n", strings.Join(failed, ", "))
	}
	if len(running) > 0 {
		_, _ = fmt.Fprintf(w, "Updated executable, but running workspaces still use the previous version: %s. Run `jailoc restart --all` to restart them.\n", strings.Join(running, ", "))
	}
}

// scanRunningWorkspaces lists workspaces with a live VM and, separately, those
// whose state could not be read. A missing directory means none are running.
func scanRunningWorkspaces(workspacesDir string) (running, failed []string, err error) {
	entries, err := os.ReadDir(workspacesDir)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil, nil
	}
	if err != nil {
		return nil, nil, fmt.Errorf("read %s: %w", workspacesDir, err)
	}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		st, stErr := runningVM(filepath.Join(workspacesDir, e.Name()))
		switch {
		case stErr != nil:
			failed = append(failed, e.Name())
		case st != nil:
			running = append(running, e.Name())
		}
	}
	sort.Strings(running)
	sort.Strings(failed)
	return running, failed, nil
}
