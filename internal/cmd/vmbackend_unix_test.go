//go:build darwin || linux

package cmd

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
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
		{limit: "4g", want: 4096 + vmMemoryOverhead},
		{limit: "512m", want: 512 + vmMemoryOverhead},
		{limit: "1536M", want: 1536 + vmMemoryOverhead},
		{limit: "1k", want: 1 + vmMemoryOverhead},
		{limit: "1073741824", want: 1024 + vmMemoryOverhead},
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

	ws := &workspace.Resolved{
		Name:  "w",
		Paths: []string{project},
		Mounts: []string{
			tools + ":/opt/tools:ro",
			project + ":/opt/project:ro",
			"~/created:/opt/created",
		},
	}
	shares, err := vmShares(ws)
	if err != nil {
		t.Fatalf("vmShares: %v", err)
	}
	want := []vm.Share{
		{HostPath: filepath.Join(home, ".cache", "jailoc", "w"), ReadOnly: true},
		{HostPath: filepath.Join(home, ".config", "jailoc", "workspaces", "w"), ReadOnly: true},
		{HostPath: filepath.Join(home, "created")},
		{HostPath: project},
		{HostPath: tools, ReadOnly: true},
	}
	if !slices.Equal(shares, want) {
		t.Fatalf("shares = %+v, want %+v", shares, want)
	}
	if info, err := os.Stat(filepath.Join(home, "created")); err != nil || !info.IsDir() {
		t.Fatalf("missing mount source not created: %v", err)
	}

	ws.Mounts = []string{file + ":/opt/file.txt:ro"}
	if _, err := vmShares(ws); err == nil {
		t.Fatal("vmShares accepted a file mount")
	}
}
