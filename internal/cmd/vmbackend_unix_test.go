//go:build darwin || linux

package cmd

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

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
		AllowedHosts:    []string{"192.168.1.10", "10.1.0.0"},
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
		{CIDR: "192.168.1.10/32", Proto: "any"},
		{CIDR: "10.1.0.0/32", Proto: "any"},
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

	cacheDir := filepath.Join(home, ".cache", "jailoc", "w")
	ws := &workspace.Resolved{
		Name:  "w",
		Paths: []string{project},
		Mounts: []string{
			tools + ":/opt/tools:ro",
			project + ":" + project + ":ro",
			"~/created:/opt/created",
			"~/data:" + guestOpenCodeDir,
		},
	}
	shares, err := vmShares(ws, cacheDir)
	if err != nil {
		t.Fatalf("vmShares: %v", err)
	}
	want := []vm.Share{
		{HostPath: filepath.Join(home, ".config", "jailoc", "workspaces", "w"), GuestPath: guestConfigDir, ReadOnly: true},
		{HostPath: cacheDir, GuestPath: guestCacheDir, ReadOnly: true},
		{HostPath: filepath.Join(cacheDir, "tui-plugin"), GuestPath: tuiPluginContainerDir, ReadOnly: true},
		{HostPath: filepath.Join(vmStateDir("w"), "home", "cache"), GuestPath: guestUserCache},
		{HostPath: filepath.Join(vmStateDir("w"), "home", "opencode-state"), GuestPath: guestOpenCodeState},
		{HostPath: filepath.Join(home, "data"), GuestPath: guestOpenCodeDir},
		{HostPath: filepath.Join(home, "created"), GuestPath: "/opt/created"},
		{HostPath: tools, GuestPath: "/opt/tools", ReadOnly: true},
		{HostPath: project, GuestPath: project},
	}
	slices.SortFunc(want, func(a, b vm.Share) int { return strings.Compare(a.GuestPath, b.GuestPath) })
	if !slices.Equal(shares, want) {
		t.Fatalf("shares =\n%+v\nwant\n%+v", shares, want)
	}
	for _, dir := range []string{filepath.Join(home, "created"), filepath.Join(vmStateDir("w"), "home", "cache"), filepath.Join(vmStateDir("w"), "home", "opencode-state")} {
		if info, err := os.Stat(dir); err != nil || !info.IsDir() {
			t.Fatalf("directory %s not created: %v", dir, err)
		}
	}
	if _, err := os.Stat(filepath.Join(vmStateDir("w"), "home", "opencode-data")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("opencode-data created although a mount covers it: %v", err)
	}

	ws.Mounts = []string{file + ":/opt/file.txt:ro"}
	if _, err := vmShares(ws, cacheDir); err == nil {
		t.Fatal("vmShares accepted a file mount")
	}
}
