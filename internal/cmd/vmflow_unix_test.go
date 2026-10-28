//go:build darwin || linux

package cmd

import (
	"context"
	"errors"
	"slices"
	"testing"

	"github.com/seznam/jailoc/internal/config"
	"github.com/seznam/jailoc/internal/guest"
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
