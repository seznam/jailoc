//go:build darwin || linux

package cmd

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/seznam/jailoc/internal/config"
	"github.com/seznam/jailoc/internal/guest"
	"github.com/seznam/jailoc/internal/vm"
	"github.com/seznam/jailoc/internal/workspace"
)

func TestVMPolicyInjectsAdminPolicyDeny(t *testing.T) {
	t.Parallel()

	ws := &workspace.Resolved{
		Name: "w",
		NetworkRules: []config.NetworkRule{
			{Name: "gh", Allow: []string{"github.com"}},
		},
		PolicyDeny: []string{"pastebin.com", "*.ngrok.io"},
	}

	for _, tc := range []struct {
		name  string
		build func(context.Context, *workspace.Resolved) ([]byte, error)
	}{
		{"workspace", vmPolicy},
		{"builder", vmBuilderPolicy},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			data, err := tc.build(context.Background(), ws)
			if err != nil {
				t.Fatalf("build policy: %v", err)
			}
			var decoded struct {
				Deny []struct {
					Name  string   `json:"name"`
					Proto string   `json:"proto"`
					Names []string `json:"names"`
				} `json:"deny"`
			}
			if err := json.Unmarshal(data, &decoded); err != nil {
				t.Fatalf("unmarshal policy: %v", err)
			}
			found := false
			for _, d := range decoded.Deny {
				if d.Name == "policy" && d.Proto == "any" && len(d.Names) == 2 {
					found = true
				}
			}
			if !found {
				t.Fatalf("policy deny rule missing from broker policy: %s", string(data))
			}
		})
	}
}

func TestCheckRunningVMPolicy(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		policy  *config.Policy
		running vm.Spec
		wantErr string
	}{
		{
			name:    "no policy allows reload",
			policy:  nil,
			running: vm.Spec{Docker: true, Guest: &guest.Config{}},
		},
		{
			name:    "forbid_docker rejects running VM with docker",
			policy:  &config.Policy{Path: "/etc/jailoc/policy.toml", Workspace: config.PolicyWorkspace{ForbidDocker: true}},
			running: vm.Spec{Docker: true, Guest: &guest.Config{}},
			wantErr: "enable_docker is active on the running VM",
		},
		{
			name:    "require_protect_git rejects running VM without NoPrivilegeEscalation",
			policy:  &config.Policy{Path: "/etc/jailoc/policy.toml", Workspace: config.PolicyWorkspace{RequireProtectGit: true}},
			running: vm.Spec{Guest: &guest.Config{NoPrivilegeEscalation: false}},
			wantErr: "protect_git is disabled on the running VM",
		},
		{
			name:    "forbid_ipv6 rejects running VM with ipv6",
			policy:  &config.Policy{Path: "/etc/jailoc/policy.toml", Network: config.PolicyNetwork{ForbidIPv6: true}},
			running: vm.Spec{GuestProtocol: 3, Guest: &guest.Config{IPv6: true}},
			wantErr: "network.ipv6 is enabled on the running VM",
		},
		{
			name:    "forbid_rw_mounts rejects running VM with rw share",
			policy:  &config.Policy{Path: "/etc/jailoc/policy.toml", Filesystem: config.PolicyFilesystem{ForbidRWMounts: true}},
			running: vm.Spec{Guest: &guest.Config{}, Shares: []vm.Share{{HostPath: "/work/repo", GuestPath: "/work/repo", ReadOnly: false}}},
			wantErr: "read-write share",
		},
		{
			name: "compliant running VM succeeds",
			policy: &config.Policy{
				Path:       "/etc/jailoc/policy.toml",
				Workspace:  config.PolicyWorkspace{ForbidDocker: true, RequireProtectGit: true},
				Network:    config.PolicyNetwork{ForbidIPv6: true},
				Filesystem: config.PolicyFilesystem{ForbidRWMounts: true},
			},
			running: vm.Spec{
				GuestProtocol: 3,
				Docker:        false,
				Guest:         &guest.Config{NoPrivilegeEscalation: true, IPv6: false},
				Shares:        []vm.Share{{HostPath: "/work/repo", GuestPath: "/work/repo", ReadOnly: true}},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			ws := &workspace.Resolved{Name: "w", Policy: tt.policy}
			err := checkRunningVMPolicy(ws, tt.running)
			if tt.wantErr == "" {
				if err != nil {
					t.Fatalf("checkRunningVMPolicy() unexpected error: %v", err)
				}
				return
			}
			if err == nil {
				t.Fatalf("checkRunningVMPolicy() succeeded, want error containing %q", tt.wantErr)
			}
			if !strings.Contains(err.Error(), tt.wantErr) || !strings.Contains(err.Error(), "jailoc restart w") {
				t.Fatalf("checkRunningVMPolicy() error = %q, want %q and restart hint", err.Error(), tt.wantErr)
			}
		})
	}
}
