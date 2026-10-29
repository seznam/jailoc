//go:build darwin || linux

package cmd

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/seznam/jailoc/internal/agent"
	"github.com/seznam/jailoc/internal/config"
	"github.com/seznam/jailoc/internal/guest"
	"github.com/seznam/jailoc/internal/vm"
	"github.com/seznam/jailoc/internal/workspace"
)

func TestVMMemoryMiB(t *testing.T) {
	t.Parallel()
	tests := []struct {
		limit   string
		want    uint32
		wantErr bool
	}{
		{limit: "4g", want: 4096},
		{limit: "512m", want: 512},
		{limit: "1536M", want: 1536},
		{limit: "1k", wantErr: true},
		{limit: "511m", wantErr: true},
		{limit: "1073741824", want: 1024},
		{limit: "", wantErr: true},
		{limit: "0g", wantErr: true},
		{limit: "g", wantErr: true},
		{limit: "99999999g", wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.limit, func(t *testing.T) {
			t.Parallel()
			got, err := vmMemoryMiB(tt.limit)
			if (err != nil) != tt.wantErr {
				t.Fatalf("vmMemoryMiB(%q) error = %v, wantErr %v", tt.limit, err, tt.wantErr)
			}
			if !tt.wantErr && got != tt.want {
				t.Fatalf("vmMemoryMiB(%q) = %d, want %d", tt.limit, got, tt.want)
			}
		})
	}
}

func TestVMPolicy(t *testing.T) {
	t.Parallel()
	ws := &workspace.Resolved{
		Name:            "w",
		AllowedNetworks: []string{"10.1.0.0/16", "10.1.0.5/16", "fd00::/8"},
		AllowedHosts:    []string{"192.168.1.10", "10.1.0.0", "fd12::7"},
	}
	data, err := vmPolicy(context.Background(), ws)
	if err != nil {
		t.Fatalf("vmPolicy: %v", err)
	}
	var got struct {
		Exceptions []vmException `json:"exceptions"`
	}
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("unmarshal %s: %v", data, err)
	}
	want := []vmException{
		{CIDR: "10.1.0.0/16", Proto: "any"},
		{CIDR: "fd00::/8", Proto: "any"},
		{CIDR: "192.168.1.10/32", Proto: "any"},
		{CIDR: "10.1.0.0/32", Proto: "any"},
		{CIDR: "fd12::7/128", Proto: "any"},
	}
	if !slices.Equal(got.Exceptions, want) {
		t.Fatalf("exceptions = %v, want %v", got.Exceptions, want)
	}
}

func TestVMPolicyFilteredDNS(t *testing.T) {
	t.Parallel()
	for _, filtered := range []bool{false, true} {
		ws := &workspace.Resolved{Name: "w", FilteredDNS: filtered, DNSUpstream: "10.0.0.53", DNSBlockedZones: []string{"corp.example"}}
		data, err := vmPolicy(context.Background(), ws)
		if err != nil {
			t.Fatalf("vmPolicy: %v", err)
		}
		var got struct {
			DNSUpstreams    []string `json:"dns_upstreams"`
			DNSBlockedZones []string `json:"dns_blocked_zones"`
		}
		if err := json.Unmarshal(data, &got); err != nil {
			t.Fatalf("unmarshal %s: %v", data, err)
		}
		if filtered && (!slices.Equal(got.DNSUpstreams, []string{"10.0.0.53"}) || !slices.Equal(got.DNSBlockedZones, []string{"corp.example"})) {
			t.Fatalf("filtered policy = %s", data)
		}
		if !filtered && (got.DNSUpstreams != nil || got.DNSBlockedZones != nil) {
			t.Fatalf("unfiltered policy names DNS settings: %s", data)
		}
	}
}

func TestVMShares(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	project := filepath.Join(home, "project")
	tools := filepath.Join(home, "tools")
	file := filepath.Join(home, "file.txt")
	for _, d := range []string{project, tools} {
		if err := os.MkdirAll(d, 0o750); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(file, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}

	oc, _ := agent.Lookup("opencode")
	guestOpenCodeDir, guestOpenCodeState := oc.State[0].Path, oc.State[1].Path
	cacheDir := filepath.Join(home, ".cache", "jailoc", "w")
	ws := &workspace.Resolved{
		Name:  "w",
		Paths: []string{project},
		Agent: oc,
		Mounts: []string{
			tools + ":/opt/tools:ro",
			project + ":" + project + ":ro",
			"~/created:/opt/created",
			"~/data:" + guestOpenCodeDir,
		},
	}
	shares, err := vmShares(ws)
	if err != nil {
		t.Fatalf("vmShares: %v", err)
	}
	want := []vm.Share{
		{HostPath: filepath.Join(home, "data"), GuestPath: guestOpenCodeDir},
		{HostPath: filepath.Join(home, "created"), GuestPath: "/opt/created"},
		{HostPath: tools, GuestPath: "/opt/tools", ReadOnly: true},
		{HostPath: project, GuestPath: project},
	}
	slices.SortFunc(want, func(a, b vm.Share) int { return strings.Compare(a.GuestPath, b.GuestPath) })
	if !slices.Equal(shares, want) {
		t.Fatalf("shares =\n%+v\nwant\n%+v", shares, want)
	}
	if info, err := os.Stat(filepath.Join(home, "created")); err != nil || !info.IsDir() {
		t.Fatalf("mount source not created: %v", err)
	}

	if err := os.MkdirAll(filepath.Join(cacheDir, "tui-plugin"), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cacheDir, "tui-container.json"), []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	binds, err := vmGuestData(ws, cacheDir, shares, true)
	if err != nil {
		t.Fatalf("vmGuestData: %v", err)
	}
	wantBinds := []guest.Bind{
		{Source: guest.HostDir + "/config", Target: guestConfigDir, ReadOnly: true},
		{Source: guest.HostDir + "/cache", Target: guestCacheDir, ReadOnly: true},
		{Source: guest.HostDir + "/cache/tui-plugin", Target: tuiPluginContainerDir, ReadOnly: true},
		{Source: guest.HostDir + "/home/opencode-state", Target: guestOpenCodeState},
		{Source: guest.HostDir + "/home/cache", Target: guestUserCache},
	}
	if !slices.Equal(binds, wantBinds) {
		t.Fatalf("binds =\n%+v\nwant\n%+v", binds, wantBinds)
	}
	data := vm.GuestDataDir(vmStateDir("w"))
	if b, err := os.ReadFile(filepath.Join(data, "cache", "tui-container.json")); err != nil || string(b) != "{}" { //nolint:gosec // G304: test file
		t.Fatalf("cache not copied: %q %v", b, err)
	}
	if _, err := os.Stat(filepath.Join(data, "home", "opencode-data")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("opencode-data created although a mount covers it: %v", err)
	}

	ws.Agent, _ = agent.Lookup("claude")
	binds, err = vmGuestData(ws, cacheDir, shares, true)
	if err != nil {
		t.Fatalf("vmGuestData: %v", err)
	}
	wantBinds = []guest.Bind{
		{Source: guest.HostDir + "/config", Target: guestConfigDir, ReadOnly: true},
		{Source: guest.HostDir + "/cache", Target: guestCacheDir, ReadOnly: true},
		{Source: guest.HostDir + "/home/claude", Target: "/home/agent/.claude"},
		{Source: guest.HostDir + "/home/cache", Target: guestUserCache},
	}
	if !slices.Equal(binds, wantBinds) {
		t.Fatalf("claude binds =\n%+v\nwant\n%+v", binds, wantBinds)
	}

	ws.Mounts = []string{file + ":/opt/file.txt:ro"}
	if _, err := vmShares(ws); err == nil {
		t.Fatal("vmShares accepted a file mount")
	}
}

func TestVMAgentProfile(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	oc, _ := agent.Lookup("opencode")
	dataDir, stateDir := oc.State[0].Path, oc.State[1].Path
	cacheDir := filepath.Join(home, ".cache", "jailoc", "w")
	profile := config.NamedProfile("work")
	ws := &workspace.Resolved{
		Name:         "w",
		Agent:        oc,
		AgentProfile: &profile,
		Mounts:       []string{"~/data:" + dataDir},
	}

	shares, err := vmShares(ws)
	if err != nil {
		t.Fatalf("vmShares: %v", err)
	}
	profileState := filepath.Join(home, ".local", "share", "jailoc", "profiles", "work", "opencode-state")
	want := []vm.Share{
		{HostPath: filepath.Join(home, "data"), GuestPath: dataDir},
		{HostPath: profileState, GuestPath: stateDir},
	}
	if !slices.Equal(shares, want) {
		t.Fatalf("shares =\n%+v\nwant\n%+v", shares, want)
	}
	binds, err := vmGuestData(ws, cacheDir, shares, true)
	if err != nil {
		t.Fatalf("vmGuestData: %v", err)
	}
	wantBinds := []guest.Bind{
		{Source: guest.HostDir + "/config", Target: guestConfigDir, ReadOnly: true},
		{Source: guest.HostDir + "/cache", Target: guestCacheDir, ReadOnly: true},
		{Source: guest.HostDir + "/cache/tui-plugin", Target: tuiPluginContainerDir, ReadOnly: true},
		{Source: guest.HostDir + "/home/cache", Target: guestUserCache},
	}
	if !slices.Equal(binds, wantBinds) {
		t.Fatalf("named profile binds =\n%+v\nwant\n%+v", binds, wantBinds)
	}

	ephemeral := config.EphemeralProfile()
	ws.AgentProfile = &ephemeral
	ws.Mounts = nil
	if shares, err = vmShares(ws); err != nil || len(shares) != 0 {
		t.Fatalf("ephemeral profile shares = %+v, %v", shares, err)
	}
	if binds, err = vmGuestData(ws, cacheDir, nil, true); err != nil {
		t.Fatalf("vmGuestData: %v", err)
	}
	wantBinds = []guest.Bind{
		{Source: guest.HostDir + "/config", Target: guestConfigDir, ReadOnly: true},
		{Source: guest.HostDir + "/cache", Target: guestCacheDir, ReadOnly: true},
		{Source: guest.HostDir + "/cache/tui-plugin", Target: tuiPluginContainerDir, ReadOnly: true},
		{Source: guest.HostDir + "/ephemeral/opencode-data", Target: dataDir},
		{Source: guest.HostDir + "/ephemeral/opencode-state", Target: stateDir},
		{Source: guest.HostDir + "/home/cache", Target: guestUserCache},
	}
	if !slices.Equal(binds, wantBinds) {
		t.Fatalf("ephemeral binds =\n%+v\nwant\n%+v", binds, wantBinds)
	}
	leftover := filepath.Join(vm.GuestDataDir(vmStateDir("w")), "ephemeral", "opencode-data", "session.json")
	if err := os.WriteFile(leftover, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := vmGuestData(ws, cacheDir, nil, false); err != nil {
		t.Fatalf("vmGuestData reload: %v", err)
	}
	if _, err := os.Stat(leftover); err != nil {
		t.Fatalf("reload emptied ephemeral state: %v", err)
	}
	if _, err := vmGuestData(ws, cacheDir, nil, true); err != nil {
		t.Fatalf("vmGuestData start: %v", err)
	}
	if _, err := os.Stat(leftover); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("start kept ephemeral state: %v", err)
	}
}

func TestClaimProfile(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	profile := config.NamedProfile("work")
	ws := func(name string) *workspace.Resolved {
		return &workspace.Resolved{Name: name, AgentProfile: &profile}
	}
	lock := filepath.Join(profilesDir(), "work", "lock")

	if err := claimProfile(ws("a")); err != nil {
		t.Fatalf("claim: %v", err)
	}
	if b, _ := os.ReadFile(lock); string(b) != "a\n" { //nolint:gosec // G304: test file
		t.Fatalf("lock = %q, want a", b)
	}
	// The VM of a does not run: its claim is stale.
	if err := claimProfile(ws("b")); err != nil {
		t.Fatalf("take over stale claim: %v", err)
	}
	if err := releaseProfile("a", "work"); err != nil {
		t.Fatalf("release by non-owner: %v", err)
	}
	if b, _ := os.ReadFile(lock); string(b) != "b\n" { //nolint:gosec // G304: test file
		t.Fatalf("lock after foreign release = %q, want b", b)
	}
	if err := releaseProfile("b", "work"); err != nil {
		t.Fatalf("release: %v", err)
	}
	if b, _ := os.ReadFile(lock); len(b) != 0 { //nolint:gosec // G304: test file
		t.Fatalf("lock after release = %q", b)
	}

	// A running VM of b with the profile holds it.
	state := vmStateDir("b")
	if err := vm.WriteSpec(vm.Spec{StateDir: state, Workspace: "b", Profile: "work"}); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(vm.StateFile(state), fmt.Appendf(nil, `{"pid":%d,"ready":true}`, os.Getpid()), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := claimProfile(ws("b")); err != nil {
		t.Fatalf("claim b: %v", err)
	}
	err := claimProfile(ws("a"))
	assertContains(t, fmt.Sprint(err), `profile "work" is in use by running workspace "b"`)
}
