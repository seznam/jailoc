//go:build darwin || linux

package cmd

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/fatih/color"
	"github.com/spf13/cobra"
	"golang.org/x/term"

	"github.com/seznam/jailoc/internal/compose"
	"github.com/seznam/jailoc/internal/config"
	"github.com/seznam/jailoc/internal/docker"
)

var (
	migrateDryRun bool
	migrateYes    bool
)

var migrateCmd = &cobra.Command{
	Use:   "migrate",
	Short: `Move all workspaces to the VM backend (backend = "vm")`,
	Long: `Rewrite config.toml so that every workspace uses backend = "vm".

For each Docker workspace, migrate asks how to carry over every mount (a live
directory mount, or a read-only copy), removes settings the VM backend does not
support after confirmation, stops running Docker workspaces, and copies the
OpenCode data and cache volumes into the VM's persistent home directories.
The previous config.toml is kept as a backup; comments in it are not carried
over.`,
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		interactive := term.IsTerminal(int(os.Stdin.Fd())) //nolint:gosec // G115: uintptr→int is safe for file descriptors
		if !interactive && !migrateYes {
			return errors.New("migrate asks questions; run it in a terminal or pass --yes to accept the defaults")
		}
		p := &prompter{in: bufio.NewReader(os.Stdin), out: os.Stdout, yes: migrateYes}
		return runMigrate(cmd.Context(), config.ConfigPath(), p)
	},
}

func init() {
	migrateCmd.Flags().BoolVar(&migrateDryRun, "dry-run", false, "show the migrated configuration without changing anything")
	migrateCmd.Flags().BoolVarP(&migrateYes, "yes", "y", false, "accept the default answer to every question")
	rootCmd.AddCommand(migrateCmd)
}

// prompter asks multiple-choice and yes/no questions; with yes set it
// answers every question with its default.
type prompter struct {
	in  *bufio.Reader
	out io.Writer
	yes bool
}

// choose returns the index of the chosen option. Options are matched by
// their full name or first letter; an empty answer picks def.
func (p *prompter) choose(question string, options []string, def int) (int, error) {
	labels := make([]string, len(options))
	for i, o := range options {
		labels[i] = o
		if i == def {
			labels[i] = strings.ToUpper(o[:1]) + o[1:]
		}
	}
	prompt := fmt.Sprintf("%s [%s] ", question, strings.Join(labels, "/"))
	for {
		_, _ = fmt.Fprint(p.out, prompt)
		if p.yes {
			_, _ = fmt.Fprintln(p.out, options[def])
			return def, nil
		}
		line, err := p.in.ReadString('\n')
		answer := strings.ToLower(strings.TrimSpace(line))
		if answer == "" && err != nil {
			return 0, fmt.Errorf("read answer: %w", err)
		}
		if answer == "" {
			return def, nil
		}
		for i, o := range options {
			if answer == o || answer == o[:1] {
				return i, nil
			}
		}
		_, _ = fmt.Fprintf(p.out, "  answer one of: %s\n", strings.Join(options, ", "))
	}
}

func (p *prompter) confirm(question string, def bool) (bool, error) {
	options := []string{"yes", "no"}
	d := 1
	if def {
		d = 0
	}
	i, err := p.choose(question, options, d)
	return i == 0, err
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
	if cfg.Mode == config.ModeRemote {
		out = append(out, incompatibility{`mode = "remote"`, "sessions run inside the VM (exec mode)", func() { cfg.Mode = "" }})
	}
	if d.SSHAuthSock {
		out = append(out, incompatibility{"ssh_auth_sock = true", "the SSH agent socket cannot cross into the VM; Git over SSH stops working", func() { d.SSHAuthSock = false }})
	}
	if d.FilteredDNS != nil && *d.FilteredDNS {
		out = append(out, incompatibility{"filtered_dns = true", "DNS goes through the VM network broker", func() { d.FilteredDNS = nil }})
	}
	if d.EnableDocker != nil && *d.EnableDocker {
		out = append(out, incompatibility{"enable_docker = true", "the agent has no Docker daemon in the VM", func() { d.EnableDocker = nil }})
	}
	if d.ExposePort != nil && *d.ExposePort {
		out = append(out, incompatibility{"expose_port = true", "sessions run over SSH; there is no published port", func() { d.ExposePort = nil }})
	}
	return out
}

func workspaceIncompatibilities(ws *config.Workspace) []incompatibility {
	var out []incompatibility
	if ws.SSHAuthSock != nil && *ws.SSHAuthSock {
		out = append(out, incompatibility{"ssh_auth_sock = true", "the SSH agent socket cannot cross into the VM; Git over SSH stops working", func() { ws.SSHAuthSock = nil }})
	}
	if ws.FilteredDNS != nil && *ws.FilteredDNS {
		out = append(out, incompatibility{"filtered_dns = true", "DNS goes through the VM network broker", func() { ws.FilteredDNS = nil }})
	}
	if ws.EnableDocker != nil && *ws.EnableDocker {
		out = append(out, incompatibility{"enable_docker = true", "the agent has no Docker daemon in the VM", func() { ws.EnableDocker = nil }})
	}
	if ws.ExposePort != nil && *ws.ExposePort {
		out = append(out, incompatibility{"expose_port = true", "sessions run over SSH; there is no published port", func() { ws.ExposePort = nil }})
	}
	return out
}

// planMigration rewrites cfg in place for the VM backend, asking how to
// carry over mounts and whether to remove incompatible settings.
func planMigration(cfg *config.Config, p *prompter, stat func(string) (fs.FileInfo, error)) (*migration, error) {
	m := &migration{cfg: cfg}
	names := make([]string, 0, len(cfg.Workspaces))
	for name := range cfg.Workspaces {
		names = append(names, name)
	}
	slices.Sort(names)
	for _, name := range names {
		if config.EffectiveBackend(cfg.Defaults.Backend, cfg.Workspaces[name].Backend) != config.BackendVM {
			m.workspaces = append(m.workspaces, name)
		}
	}

	if err := resolveIncompatibilities(p, "[defaults]", defaultsIncompatibilities(cfg), m); err != nil {
		return nil, err
	}
	if err := carryMounts(p, "[defaults]", &cfg.Defaults.Mounts, &cfg.Defaults.Copies, stat, m); err != nil {
		return nil, err
	}
	for _, name := range m.workspaces {
		ws := cfg.Workspaces[name]
		label := fmt.Sprintf("[workspaces.%s]", name)
		if err := resolveIncompatibilities(p, label, workspaceIncompatibilities(&ws), m); err != nil {
			return nil, err
		}
		if err := carryMounts(p, label, &ws.Mounts, &ws.Copies, stat, m); err != nil {
			return nil, err
		}
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
	return m, nil
}

func resolveIncompatibilities(p *prompter, label string, items []incompatibility, m *migration) error {
	if len(items) == 0 {
		return nil
	}
	_, _ = fmt.Fprintf(p.out, "\n%s has settings the VM backend does not support:\n", label)
	for _, it := range items {
		_, _ = fmt.Fprintf(p.out, "  %s: %s\n", it.setting, it.reason)
	}
	ok, err := p.confirm("Remove them?", true)
	if err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("%s: the VM backend needs these settings removed; edit config.toml or answer yes", label)
	}
	for _, it := range items {
		it.clear()
		m.changes = append(m.changes, fmt.Sprintf("%s: remove %s", label, it.setting))
	}
	return nil
}

// carryMounts asks for every mount with a host source whether it stays a
// live mount (directories only) or becomes a read-only copy, or is dropped.
func carryMounts(p *prompter, label string, mounts, copies *[]string, stat func(string) (fs.FileInfo, error), m *migration) error {
	var kept []string
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
		copySpec := mt.Host + ":" + mt.Container
		desc := fmt.Sprintf("%s mount %s -> %s (%s)", label, mt.Host, mt.Container, mt.Mode)
		var options []string
		if isFile {
			_, _ = fmt.Fprintf(p.out, "\n%s is a file; the VM shares directories only.\n", desc)
			_, _ = fmt.Fprintln(p.out, "  copy: read-only snapshot taken at every VM start")
			_, _ = fmt.Fprintln(p.out, "  drop: remove the mount")
			options = []string{"copy", "drop"}
		} else {
			_, _ = fmt.Fprintf(p.out, "\n%s\n", desc)
			_, _ = fmt.Fprintln(p.out, "  mount: live, two-way share of the directory")
			if mt.Mode == "rw" {
				_, _ = fmt.Fprintln(p.out, "  copy: read-only snapshot taken at every VM start; workspace writes no longer reach the host")
			} else {
				_, _ = fmt.Fprintln(p.out, "  copy: read-only snapshot taken at every VM start")
			}
			_, _ = fmt.Fprintln(p.out, "  drop: remove the mount")
			options = []string{"mount", "copy", "drop"}
		}
		i, err := p.choose("Keep as", options, 0)
		if err != nil {
			return err
		}
		switch options[i] {
		case "mount":
			kept = append(kept, spec)
		case "copy":
			*copies = append(*copies, copySpec)
			m.changes = append(m.changes, fmt.Sprintf("%s: mount %s -> copy %s", label, spec, copySpec))
		case "drop":
			m.changes = append(m.changes, fmt.Sprintf("%s: drop mount %s", label, spec))
		}
	}
	*mounts = kept
	return nil
}

// volumeTarget is a Docker volume of a workspace and the persistent VM home
// directory it is copied to.
type volumeTarget struct {
	workspace, volume, dst, guestPath string
}

func migrationVolumes(cfg *config.Config, names []string) ([]volumeTarget, error) {
	var out []volumeTarget
	for _, name := range names {
		mounts, err := config.MergeMounts(config.DefaultMounts, cfg.Defaults.Mounts, cfg.Workspaces[name].Mounts)
		if err != nil {
			return nil, fmt.Errorf("workspace %q: %w", name, err)
		}
		for _, v := range []struct{ key, home, guestPath string }{
			{"opencode-data-" + name, "opencode-data", guestOpenCodeDir},
			{"opencode-cache-" + name, "cache", guestUserCache},
		} {
			if compose.MountsContainTarget(mounts, v.guestPath) {
				continue
			}
			out = append(out, volumeTarget{
				workspace: name,
				volume:    docker.VolumeName(name, v.key),
				dst:       filepath.Join(vmStateDir(name), "home", v.home),
				guestPath: v.guestPath,
			})
		}
	}
	return out, nil
}

func runMigrate(ctx context.Context, path string, p *prompter) error {
	cfg, err := config.ReadRaw(path)
	if err != nil {
		return fmt.Errorf("read config: %w", err)
	}
	m, err := planMigration(cfg, p, os.Stat)
	if err != nil {
		return err
	}
	if len(m.changes) == 0 {
		_, _ = color.New(color.FgGreen).Fprintln(p.out, `All workspaces already use backend "vm"; nothing to migrate.`)
		return nil
	}
	if err := config.ValidateCopy(cfg); err != nil {
		return fmt.Errorf("migrated configuration is invalid: %w", err)
	}

	_, _ = fmt.Fprintln(p.out, "\nChanges to config.toml:")
	for _, c := range m.changes {
		_, _ = fmt.Fprintf(p.out, "  %s\n", c)
	}
	if migrateDryRun {
		data, err := config.Encode(cfg)
		if err != nil {
			return err
		}
		_, _ = fmt.Fprintf(p.out, "\nMigrated config.toml (dry run, nothing changed):\n\n%s", data)
		return nil
	}
	if ok, err := p.confirm("\nApply the migration?", true); err != nil || !ok {
		if err == nil {
			_, _ = fmt.Fprintln(p.out, "Nothing changed.")
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
	_, _ = color.New(color.FgGreen).Fprintf(p.out, "\nMigrated config.toml (previous version: %s).\n", backup)
	_, _ = fmt.Fprintln(p.out, "Start a workspace with `jailoc up <workspace>`; the first start builds the image in a VM.")
	return nil
}

// migrateDockerState stops running Docker workspaces and copies their
// OpenCode volumes into the VM home directories.
func migrateDockerState(ctx context.Context, cfg *config.Config, names []string, p *prompter) error {
	if err := docker.Ping(ctx); err != nil {
		_, _ = color.New(color.FgYellow).Fprintf(p.out, "\nDocker is not reachable (%v).\nRunning workspaces cannot be checked and OpenCode data in Docker volumes is not copied.\n", err)
		ok, err := p.confirm("Continue without Docker?", true)
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
		composePath := filepath.Join(ComposeCacheDir(name), "docker-compose.yml")
		if _, err := os.Stat(composePath); err != nil {
			continue
		}
		up, err := docker.NewClient(composePath, "", name).IsRunning(ctx)
		if err != nil {
			return fmt.Errorf("check workspace %q: %w", name, err)
		}
		if up {
			running = append(running, name)
		}
	}
	if len(running) > 0 {
		ok, err := p.confirm(fmt.Sprintf("\nStop the running Docker workspaces %s?", strings.Join(running, ", ")), true)
		if err != nil {
			return err
		}
		if !ok {
			return errors.New("stop the running workspaces first (jailoc down), then run migrate again")
		}
		for _, name := range running {
			composePath := filepath.Join(ComposeCacheDir(name), "docker-compose.yml")
			_, _ = color.New(color.FgCyan).Fprintf(p.out, "Stopping workspace %s...\n", name)
			if err := docker.NewClient(composePath, "", name).Down(ctx); err != nil {
				return fmt.Errorf("stop workspace %q: %w", name, err)
			}
		}
	}

	targets, err := migrationVolumes(cfg, names)
	if err != nil {
		return err
	}
	for _, t := range targets {
		if _, err := os.Stat(t.dst); err == nil {
			_, _ = fmt.Fprintf(p.out, "%s: %s already has VM data; keeping it\n", t.workspace, t.guestPath)
			continue
		}
		tmp := t.dst + ".migrating"
		if err := os.RemoveAll(tmp); err != nil {
			return fmt.Errorf("workspace %q: clear %s: %w", t.workspace, tmp, err)
		}
		if err := os.MkdirAll(filepath.Dir(t.dst), 0o700); err != nil {
			return fmt.Errorf("workspace %q: create %s: %w", t.workspace, filepath.Dir(t.dst), err)
		}
		copied, err := docker.ExportVolume(ctx, t.volume, tmp)
		if err != nil {
			return fmt.Errorf("workspace %q: copy %s: %w", t.workspace, t.volume, err)
		}
		if copied {
			if err := os.Rename(tmp, t.dst); err != nil {
				return fmt.Errorf("workspace %q: install %s: %w", t.workspace, t.dst, err)
			}
			_, _ = fmt.Fprintf(p.out, "%s: copied Docker volume %s to %s\n", t.workspace, t.volume, t.guestPath)
		}
	}
	return nil
}
