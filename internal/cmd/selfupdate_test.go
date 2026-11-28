package cmd

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/seznam/jailoc/internal/config"
	"github.com/seznam/jailoc/internal/update"
	"github.com/seznam/jailoc/internal/vm"
	"github.com/spf13/cobra"
)

type selfUpdateCall struct {
	current string
	opts    update.Options
}

// execSelfUpdate runs "self-update <args>" under a throwaway parent that has a
// bool --version like the real root, and returns stdout, stderr and the calls.
func execSelfUpdate(t *testing.T, run selfUpdateFunc, args ...string) (stdout, stderr string, err error) {
	t.Helper()

	parent := &cobra.Command{Use: "jailoc", Version: "test", SilenceUsage: true, SilenceErrors: true}
	parent.AddCommand(newSelfUpdateCmdWith(run))
	var out, errOut bytes.Buffer
	parent.SetOut(&out)
	parent.SetErr(&errOut)
	parent.SetArgs(append([]string{"self-update"}, args...))
	err = parent.ExecuteContext(context.Background())
	return out.String(), errOut.String(), err
}

func recordingRun(rep update.Report, runErr error, calls *[]selfUpdateCall) selfUpdateFunc {
	return func(_ context.Context, current string, opts update.Options) (update.Report, error) {
		*calls = append(*calls, selfUpdateCall{current: current, opts: opts})
		return rep, runErr
	}
}

func TestSelfUpdateRegistered(t *testing.T) {
	cmd, _, err := rootCmd.Find([]string{"self-update"})
	if err != nil {
		t.Fatalf("find: %v", err)
	}
	if cmd.Name() != "self-update" {
		t.Fatalf("found %q, want self-update", cmd.Name())
	}
	for _, name := range []string{"check", "version", "no-cooldown", "force"} {
		if cmd.Flags().Lookup(name) == nil {
			t.Errorf("missing flag --%s", name)
		}
	}
	if got := cmd.Flags().Lookup("version").Value.Type(); got != "string" {
		t.Errorf("--version type = %s, want string", got)
	}
	if n := countFlagLines(cmd); n != 4 {
		t.Errorf("flags = %d, want exactly check, version, no-cooldown, force:\n%s", n, cmd.Flags().FlagUsages())
	}
	if cmd.Hidden {
		t.Error("command must not be hidden")
	}
}

func countFlagLines(cmd *cobra.Command) int {
	n := 0
	for _, line := range strings.Split(cmd.LocalFlags().FlagUsages(), "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "-") {
			n++
		}
	}
	return n
}

func TestSelfUpdateFlagWiring(t *testing.T) {
	tests := []struct {
		name string
		args []string
		want update.Options
	}{
		{"no args", nil, update.Options{}},
		{"check", []string{"--check"}, update.Options{Check: true}},
		{"version", []string{"--version", "v1.2.3"}, update.Options{Version: "v1.2.3"}},
		{"no-cooldown", []string{"--no-cooldown"}, update.Options{NoCooldown: true}},
		{"force", []string{"--force"}, update.Options{Force: true}},
		{"all", []string{"--check", "--version=1.2.3", "--no-cooldown", "--force"}, update.Options{Check: true, Version: "1.2.3", NoCooldown: true, Force: true}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("HOME", t.TempDir())
			prev := appVersion
			appVersion = "v1.0.0"
			t.Cleanup(func() { appVersion = prev })

			var calls []selfUpdateCall
			_, _, err := execSelfUpdate(t, recordingRun(update.Report{Current: "v1.0.0"}, nil, &calls), tt.args...)
			if err != nil {
				t.Fatalf("execute: %v", err)
			}
			if len(calls) != 1 {
				t.Fatalf("calls = %d, want 1", len(calls))
			}
			if calls[0].current != "v1.0.0" {
				t.Errorf("current = %q, want v1.0.0", calls[0].current)
			}
			if calls[0].opts != tt.want {
				t.Errorf("opts = %+v, want %+v", calls[0].opts, tt.want)
			}
		})
	}
}

func TestSelfUpdateVersionFlagDoesNotCollideWithRoot(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	var calls []selfUpdateCall
	parent := &cobra.Command{Use: "jailoc", Version: "v9.9.9 (commit: x, built: y)", SilenceUsage: true}
	parent.AddCommand(newSelfUpdateCmdWith(recordingRun(update.Report{}, nil, &calls)))
	var out bytes.Buffer
	parent.SetOut(&out)

	parent.SetArgs([]string{"--version"})
	if err := parent.Execute(); err != nil {
		t.Fatalf("root --version: %v", err)
	}
	if !strings.Contains(out.String(), "v9.9.9") {
		t.Errorf("root --version output = %q", out.String())
	}
	if len(calls) != 0 {
		t.Fatalf("root --version ran the updater")
	}

	parent.SetArgs([]string{"self-update", "--version", "v1.2.3"})
	if err := parent.Execute(); err != nil {
		t.Fatalf("self-update --version: %v", err)
	}
	if len(calls) != 1 || calls[0].opts.Version != "v1.2.3" {
		t.Fatalf("calls = %+v, want one call with Version v1.2.3", calls)
	}
}

func TestSelfUpdateRejectsArgs(t *testing.T) {
	var calls []selfUpdateCall
	_, _, err := execSelfUpdate(t, recordingRun(update.Report{}, nil, &calls), "extra")
	if err == nil {
		t.Fatal("want error for positional argument")
	}
	if len(calls) != 0 {
		t.Fatal("updater ran despite invalid args")
	}
}

func TestSelfUpdateReturnsUpdaterError(t *testing.T) {
	var calls []selfUpdateCall
	boom := errors.New("boom")
	_, _, err := execSelfUpdate(t, recordingRun(update.Report{}, boom, &calls))
	if !errors.Is(err, boom) {
		t.Fatalf("err = %v, want wrapping %v", err, boom)
	}
}

func TestSelfUpdateDoesNotLoadConfig(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	broken := filepath.Join(home, "broken.toml")
	if err := os.WriteFile(broken, []byte("this is = = not toml ["), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv(config.PathEnv, broken)
	config.SetPath("")
	t.Cleanup(func() { config.SetPath("") })

	for _, tc := range []struct {
		name  string
		write bool
	}{{"missing", false}, {"broken", true}} {
		t.Run(tc.name, func(t *testing.T) {
			if !tc.write {
				t.Setenv(config.PathEnv, filepath.Join(home, "does-not-exist.toml"))
			}
			var calls []selfUpdateCall
			if _, _, err := execSelfUpdate(t, recordingRun(update.Report{Current: "v1.0.0"}, nil, &calls)); err != nil {
				t.Fatalf("execute: %v", err)
			}
			if len(calls) != 1 {
				t.Fatalf("calls = %d, want 1", len(calls))
			}
		})
	}
}

func TestSelfUpdateOutput(t *testing.T) {
	now := time.Date(2026, 11, 27, 12, 0, 0, 0, time.UTC)
	tests := []struct {
		name     string
		rep      update.Report
		opts     update.Options
		contains []string
		absent   []string
	}{
		{
			name:     "up to date",
			rep:      update.Report{Current: "v1.0.0"},
			contains: []string{"jailoc v1.0.0 is up to date"},
			absent:   []string{"Updated", "cooldown"},
		},
		{
			name:     "already at target",
			rep:      update.Report{Current: "v1.0.0", Target: "v1.0.0"},
			contains: []string{"already at v1.0.0"},
			absent:   []string{"Update available"},
		},
		{
			name:     "already at target without v prefix",
			rep:      update.Report{Current: "1.0.0", Target: "v1.0.0"},
			contains: []string{"already at v1.0.0"},
		},
		{
			name:     "check finds update",
			rep:      update.Report{Current: "v1.0.0", Target: "v1.1.0", PublishedAt: now.Add(-72 * time.Hour)},
			opts:     update.Options{Check: true},
			contains: []string{"Update available: v1.0.0 -> v1.1.0", "published 3 days ago"},
			absent:   []string{"Updated jailoc"},
		},
		{
			name:     "check same version",
			rep:      update.Report{Current: "v1.0.0", Target: "v1.0.0"},
			opts:     update.Options{Check: true},
			contains: []string{"already at v1.0.0"},
		},
		{
			name:     "pending cooldown",
			rep:      update.Report{Current: "v1.0.0", Pending: "v1.2.0", PublishedAt: now.Add(-2 * time.Hour)},
			contains: []string{"No release is eligible yet", "v1.2.0", "--no-cooldown"},
			absent:   []string{"up to date"},
		},
		{
			name:     "no target without pending is up to date",
			rep:      update.Report{Current: "v1.0.0"},
			opts:     update.Options{Check: true},
			contains: []string{"jailoc v1.0.0 is up to date"},
			absent:   []string{"No release is eligible yet"},
		},
		{
			name:     "explicit version check says selected release",
			rep:      update.Report{Current: "v1.2.0", Target: "v1.1.0", PublishedAt: now.Add(-72 * time.Hour)},
			opts:     update.Options{Check: true, Version: "v1.1.0"},
			contains: []string{"Selected release: v1.2.0 -> v1.1.0", "published 3 days ago"},
			absent:   []string{"Update available"},
		},
		{
			name:     "explicit version same as current",
			rep:      update.Report{Current: "v1.1.0", Target: "v1.1.0"},
			opts:     update.Options{Check: true, Version: "v1.1.0"},
			contains: []string{"already at v1.1.0"},
			absent:   []string{"Selected release"},
		},
		{
			name:     "check shows installation and path",
			rep:      update.Report{Current: "v1.0.0", Target: "v1.1.0", Installation: "homebrew", Path: "/opt/homebrew/bin/jailoc"},
			opts:     update.Options{Check: true},
			contains: []string{"Update available: v1.0.0 -> v1.1.0", "Installation: homebrew", "/opt/homebrew/bin/jailoc"},
		},
		{
			name:     "check up to date still shows installation",
			rep:      update.Report{Current: "v1.0.0", Installation: "release", Path: "/usr/local/bin/jailoc"},
			opts:     update.Options{Check: true},
			contains: []string{"up to date", "Installation: release", "/usr/local/bin/jailoc"},
		},
		{
			name:   "installation hidden outside check",
			rep:    update.Report{Current: "v1.0.0", Target: "v1.1.0", Installation: "homebrew", Path: "/opt/homebrew/bin/jailoc"},
			absent: []string{"Installation:"},
		},
		{
			name:     "pending next to target",
			rep:      update.Report{Current: "v1.0.0", Target: "v1.1.0", Pending: "v1.2.0"},
			opts:     update.Options{Check: true},
			contains: []string{"Update available: v1.0.0 -> v1.1.0", "v1.2.0", "--no-cooldown"},
		},
		{
			name: "installed without signature",
			rep: update.Report{
				Installed: true, Current: "v1.0.0", Target: "v1.1.0",
				Path: "/usr/local/bin/jailoc", PublishedAt: now.Add(-5 * time.Hour),
			},
			contains: []string{
				"Updated jailoc v1.0.0 -> v1.1.0", "published 5 hours ago",
				"/usr/local/bin/jailoc", "checksum", "not verified locally",
			},
			absent: []string{"Signature: verified"},
		},
		{
			name: "installed with signature",
			rep: update.Report{
				Installed: true, Current: "v1.0.0", Target: "v1.1.0",
				Path: "/usr/local/bin/jailoc", VerifiedSignature: true,
			},
			contains: []string{"Signature: verified"},
			absent:   []string{"not verified locally"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var out bytes.Buffer
			writeSelfUpdateReport(&out, tt.rep, tt.opts, now)
			got := out.String()
			for _, want := range tt.contains {
				if !strings.Contains(got, want) {
					t.Errorf("output missing %q:\n%s", want, got)
				}
			}
			for _, bad := range tt.absent {
				if strings.Contains(got, bad) {
					t.Errorf("output contains %q:\n%s", bad, got)
				}
			}
		})
	}
}

func writeVMState(t *testing.T, home, workspace string, st vm.State) {
	t.Helper()
	dir := filepath.Join(home, ".cache", "jailoc-vm", "workspaces", workspace)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(st)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(vm.StateFile(dir), data, 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestSelfUpdateRunningWorkspaceNote(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	writeVMState(t, home, "alpha", vm.State{PID: os.Getpid(), Ready: true})
	writeVMState(t, home, "stopped", vm.State{PID: os.Getpid(), Ready: false})
	writeVMState(t, home, "dead", vm.State{PID: 0, Ready: true})

	installed := update.Report{Installed: true, Current: "v1.0.0", Target: "v1.1.0", Path: "/x/jailoc"}
	var calls []selfUpdateCall

	stdout, stderr, err := execSelfUpdate(t, recordingRun(installed, nil, &calls))
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if !strings.Contains(stderr, "Updated executable, but") || !strings.Contains(stderr, "alpha") || !strings.Contains(stderr, "jailoc restart") {
		t.Errorf("stderr = %q, want running-workspace note naming alpha", stderr)
	}
	if strings.Contains(stderr, "stopped") || strings.Contains(stderr, "dead") {
		t.Errorf("stderr lists non-running workspaces: %q", stderr)
	}
	if strings.Contains(stdout, "Updated executable, but") {
		t.Errorf("warning leaked to stdout: %q", stdout)
	}

	// Nothing installed: no scan, no note.
	_, stderr, err = execSelfUpdate(t, recordingRun(update.Report{Current: "v1.0.0"}, nil, &calls), "--check")
	if err != nil {
		t.Fatalf("execute check: %v", err)
	}
	if stderr != "" {
		t.Errorf("stderr = %q, want empty when nothing installed", stderr)
	}
}

func TestSelfUpdateNoWorkspacesNoNote(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	var calls []selfUpdateCall
	_, stderr, err := execSelfUpdate(t, recordingRun(update.Report{Installed: true, Current: "v1", Target: "v2"}, nil, &calls))
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if stderr != "" {
		t.Errorf("stderr = %q, want empty", stderr)
	}
}

func TestSelfUpdateScanErrorIsWarning(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	dir := filepath.Join(home, ".cache", "jailoc-vm", "workspaces", "bad")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(vm.StateFile(dir), []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	writeVMState(t, home, "alpha", vm.State{PID: os.Getpid(), Ready: true})

	var calls []selfUpdateCall
	_, stderr, err := execSelfUpdate(t, recordingRun(update.Report{Installed: true, Current: "v1", Target: "v2"}, nil, &calls))
	if err != nil {
		t.Fatalf("scan error must not fail the command: %v", err)
	}
	if !strings.Contains(stderr, "Updated executable, but") || !strings.Contains(stderr, "bad") {
		t.Errorf("stderr = %q, want warning mentioning the unreadable workspace", stderr)
	}
	if !strings.Contains(stderr, "alpha") {
		t.Errorf("stderr = %q, want running workspace alpha still reported", stderr)
	}
}

func TestSelfUpdateReportWarningGoesToStderr(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	const warn = "release has no signature; only the GitHub-reported checksum was verified"

	tests := []struct {
		name       string
		rep        update.Report
		args       []string
		wantStdout []string
	}{
		{
			name:       "check",
			rep:        update.Report{Current: "v1.0.0", Target: "v1.1.0", Warning: warn},
			args:       []string{"--check"},
			wantStdout: []string{"Update available"},
		},
		{
			name:       "installed with post-rename warning",
			rep:        update.Report{Installed: true, Current: "v1.0.0", Target: "v1.1.0", Path: "/x/jailoc", Warning: warn},
			wantStdout: []string{"Updated jailoc v1.0.0 -> v1.1.0", "/x/jailoc"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var calls []selfUpdateCall
			stdout, stderr, err := execSelfUpdate(t, recordingRun(tt.rep, nil, &calls), tt.args...)
			if err != nil {
				t.Fatalf("execute: %v", err)
			}
			if !strings.Contains(stderr, warn) {
				t.Errorf("stderr = %q, want warning %q", stderr, warn)
			}
			if strings.Contains(stdout, warn) {
				t.Errorf("warning leaked to stdout: %q", stdout)
			}
			for _, want := range tt.wantStdout {
				if !strings.Contains(stdout, want) {
					t.Errorf("stdout missing %q:\n%s", want, stdout)
				}
			}
		})
	}
}

func TestSelfUpdateInstalledWarningKeepsRestartNote(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	writeVMState(t, home, "alpha", vm.State{PID: os.Getpid(), Ready: true})
	const warn = "could not sync directory after rename"

	var calls []selfUpdateCall
	rep := update.Report{Installed: true, Current: "v1.0.0", Target: "v1.1.0", Path: "/x/jailoc", Warning: warn}
	stdout, stderr, err := execSelfUpdate(t, recordingRun(rep, nil, &calls))
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if !strings.Contains(stdout, "Updated jailoc v1.0.0 -> v1.1.0") {
		t.Errorf("stdout = %q, want installed report", stdout)
	}
	if !strings.Contains(stderr, warn) {
		t.Errorf("stderr = %q, want warning", stderr)
	}
	if !strings.Contains(stderr, "alpha") || !strings.Contains(stderr, "jailoc restart") {
		t.Errorf("stderr = %q, want restart note naming alpha", stderr)
	}
}

func TestSelfUpdateNoWarningNoStderr(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	var calls []selfUpdateCall
	_, stderr, err := execSelfUpdate(t, recordingRun(update.Report{Current: "v1.0.0", Target: "v1.1.0"}, nil, &calls), "--check")
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if stderr != "" {
		t.Errorf("stderr = %q, want empty", stderr)
	}
}
