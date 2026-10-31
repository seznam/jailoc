//go:build darwin || linux

package cmd

import (
	"context"
	"errors"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/seznam/jailoc/internal/agent"
	"github.com/seznam/jailoc/internal/config"
	"github.com/seznam/jailoc/internal/guest"
	"github.com/seznam/jailoc/internal/guestbin"
	"github.com/seznam/jailoc/internal/vm"
	"github.com/seznam/jailoc/internal/workspace"
)

func TestUseHostDocker(t *testing.T) {
	t.Parallel()
	unusable := errors.New("docker daemon architecture is x86_64, VMs need aarch64")
	tests := []struct {
		name     string
		builder  string
		isolated bool
		hostErr  error
		want     bool
		wantErr  bool
	}{
		{name: "auto default uses host", builder: "", want: true},
		{name: "auto uses host", builder: config.BuilderAuto, want: true},
		{name: "auto falls back to vm", builder: config.BuilderAuto, hostErr: unusable},
		{name: "docker uses host", builder: config.BuilderDocker, want: true},
		{name: "docker without host fails", builder: config.BuilderDocker, hostErr: unusable, wantErr: true},
		{name: "vm never uses host", builder: config.BuilderVM},
		{name: "isolated build uses its vm", builder: config.BuilderDocker, isolated: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, err := useHostDocker(tt.builder, tt.isolated, tt.hostErr)
			if (err != nil) != tt.wantErr || got != tt.want {
				t.Fatalf("useHostDocker = %v, %v; want %v, error %v", got, err, tt.want, tt.wantErr)
			}
		})
	}
}

func TestWorkspaceSpecWorkDir(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name  string
		paths []string
		want  string
	}{
		{name: "no paths uses guest home", want: "/home/agent"},
		{name: "paths use first", paths: []string{"/work/first", "/work/second"}, want: "/work/first"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			ws := &workspace.Resolved{Name: "test", Paths: tt.paths, CPU: 2, Memory: "2G"}
			spec, err := workspaceSpec(t.Context(), ws, vmRuntimeFiles{}, "image", nil, nil)
			if err != nil {
				t.Fatalf("workspaceSpec: %v", err)
			}
			if spec.Guest.WorkDir != tt.want {
				t.Errorf("WorkDir = %q, want %q", spec.Guest.WorkDir, tt.want)
			}
		})
	}
}

func TestSecretsPayload(t *testing.T) {
	t.Parallel()
	ws := &workspace.Resolved{
		Name: "ws",
		Env:  []string{"PLAIN=1"},
		Secrets: []workspace.ResolvedSecret{
			{Name: "TOKEN", Kind: workspace.SecretKindEnv, FromCommand: "echo env-value"},
			{Name: "netrc", Kind: workspace.SecretKindFile, FromCommand: "echo file-value"},
		},
	}
	spec := vm.Spec{SSHAgent: "/tmp/agent.sock", Guest: &guest.Config{CABundle: "/ca.pem"}}
	got, err := secretsPayload(context.Background(), ws, spec)
	if err != nil {
		t.Fatalf("secretsPayload: %v", err)
	}
	wantEnv := []string{"PLAIN=1", "SSH_AUTH_SOCK=" + guest.SSHAgentSocket, "NODE_USE_SYSTEM_CA=1", "TOKEN=env-value"}
	if !slices.Equal(got.Env, wantEnv) {
		t.Errorf("Env = %q, want %q", got.Env, wantEnv)
	}
	if len(got.Files) != 1 || string(got.Files["netrc"]) != "file-value\n" {
		t.Errorf("Files = %q, want only netrc with the command output", got.Files)
	}
}

func TestStatusRestartChanges(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	guestBinary, err := guestbin.Path(filepath.Join(vmRootDir(), "guest"))
	if err != nil {
		guestBinary = ""
	}
	ws := &workspace.Resolved{Name: "ws", Agent: agent.Harness{Name: "opencode"}, CPU: 2, Memory: "4g", ProtectGit: true}
	matching := vm.Spec{Agent: "opencode", CPUs: 2, MemoryMiB: 4096, GuestBinary: guestBinary, Guest: &guest.Config{NoPrivilegeEscalation: true}}
	if got := statusRestartChanges(ws, matching); len(got) != 0 {
		t.Errorf("matching spec: got %v, want no changes", got)
	}
	if got := statusRestartChanges(ws, vm.Spec{GuestBinary: guestBinary, Guest: &guest.Config{NoPrivilegeEscalation: true}, CPUs: 2, MemoryMiB: 4096}); len(got) != 0 {
		t.Errorf("spec without agent: got %v, want no changes (opencode is the default)", got)
	}
	changed := vm.Spec{Agent: "claude", CPUs: 4, MemoryMiB: 2048, Docker: true, Profile: "work", GuestBinary: guestBinary, Guest: &guest.Config{}}
	want := []string{"profile", "cpu", "memory", "enable_docker", "protect_git", "agent"}
	if got := statusRestartChanges(ws, changed); !slices.Equal(got, want) {
		t.Errorf("changed spec: got %v, want %v", got, want)
	}
	if guestBinary != "" {
		matching.GuestBinary = "/old/jailoc-guest"
		if got := statusRestartChanges(ws, matching); !slices.Equal(got, []string{"the upgraded jailoc guest"}) {
			t.Errorf("stale guest: got %v", got)
		}
	}
}

func TestConfigLabel(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	ws := &workspace.Resolved{Name: "ws", Agent: agent.Harness{Name: "opencode"}, CPU: 2, Memory: "4g"}
	guestBinary, err := guestbin.Path(filepath.Join(vmRootDir(), "guest"))
	if err != nil {
		guestBinary = ""
	}
	running := vm.Spec{Agent: "opencode", CPUs: 2, MemoryMiB: 4096, GuestBinary: guestBinary}
	if got := configLabel(ws, running, false); got != "up to date" {
		t.Errorf("unchanged: got %q", got)
	}
	if got, want := configLabel(ws, running, true), "changed since the VM started; `jailoc reload ws` applies it"; got != want {
		t.Errorf("changed: got %q, want %q", got, want)
	}
	running.CPUs = 1
	if got, want := configLabel(ws, running, true), "`jailoc restart ws` needed for cpu; this ends running sessions"; got != want {
		t.Errorf("restart: got %q, want %q", got, want)
	}
}

func TestStatusLabels(t *testing.T) {
	t.Parallel()
	stopped := time.Date(2026, 10, 31, 9, 0, 0, 0, time.UTC)
	tests := []struct {
		name string
		got  string
		want string
	}{
		{"never started", stoppedLabel(nil), "stopped (never started)"},
		{"killed", stoppedLabel(&vm.State{PID: 42, Ready: true}), "stopped (supervisor pid 42 exited unexpectedly)"},
		{"stopped", stoppedLabel(&vm.State{Updated: stopped}), "stopped since " + stopped.Local().Format(time.RFC3339)},
		{"uptime seconds", formatUptime(30 * time.Second), "less than a minute"},
		{"uptime minutes", formatUptime(5*time.Minute + 20*time.Second), "5m"},
		{"uptime hours", formatUptime(26*time.Hour + 3*time.Minute), "26h3m"},
		{"resources", resourcesLabel(vm.Spec{CPUs: 2, MemoryMiB: 4096}), "2 CPUs, 4096 MiB memory"},
		{"resources docker", resourcesLabel(vm.Spec{CPUs: 2, MemoryMiB: 4096, Docker: true}), "2 CPUs, 4096 MiB memory, 20 GiB Docker disk"},
		{"sessions", sessionsLabel(2, 0), "2"},
		{"sessions attached", sessionsLabel(2, 1), "2 (1 attached)"},
		{"agent", agentLabel(&workspace.Resolved{Agent: agent.Harness{Name: "claude", Package: "@anthropic-ai/claude-code", Version: "2.1.312"}, AgentInstall: true}), "claude (@anthropic-ai/claude-code@2.1.312)"},
		{"agent args", agentLabel(&workspace.Resolved{Agent: agent.Harness{Name: "omo", Package: "omo-ai"}, AgentInstall: true, AgentArgs: []string{"--model", "x"}}), "omo (omo-ai@latest), args: --model x"},
		{"agent from image", agentLabel(&workspace.Resolved{Agent: agent.Harness{Name: "opencode", Package: "@opencode/cli"}}), "opencode (installed by the image)"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if tt.got != tt.want {
				t.Errorf("got %q, want %q", tt.got, tt.want)
			}
		})
	}
}
