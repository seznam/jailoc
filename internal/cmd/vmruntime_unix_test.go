//go:build darwin || linux

package cmd

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/seznam/jailoc/internal/config"
	"github.com/seznam/jailoc/internal/guestbin"
	"github.com/seznam/jailoc/internal/vm"
)

func TestSelectVMRuntime(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name         string
		cfgRunner    string
		cfgLibDir    string
		pkgRunner    string
		pkgLibDir    string
		wantRunner   string
		wantLibDir   string
		wantEmbedded bool
	}{
		{
			name:       "config runner wins over package",
			cfgRunner:  "/cfg/runner",
			cfgLibDir:  "/cfg/lib",
			pkgRunner:  "/pkg/runner",
			pkgLibDir:  "/pkg/lib",
			wantRunner: "/cfg/runner",
			wantLibDir: "/cfg/lib",
		},
		{
			name:       "config runner without package values",
			cfgRunner:  "/cfg/runner",
			cfgLibDir:  "/cfg/lib",
			wantRunner: "/cfg/runner",
			wantLibDir: "/cfg/lib",
		},
		{
			name:       "config runner with package runner only ignores package lib dir",
			cfgRunner:  "/cfg/runner",
			cfgLibDir:  "/cfg/lib",
			pkgRunner:  "/pkg/runner",
			wantRunner: "/cfg/runner",
			wantLibDir: "/cfg/lib",
		},
		{
			name:       "package runner and lib dir",
			pkgRunner:  "/pkg/runner",
			pkgLibDir:  "/pkg/lib",
			wantRunner: "/pkg/runner",
			wantLibDir: "/pkg/lib",
		},
		{
			name:       "package runner without lib dir keeps empty lib dir",
			pkgRunner:  "/pkg/runner",
			wantRunner: "/pkg/runner",
			wantLibDir: "",
		},
		{
			name:       "config lib dir alone is ignored in favor of package runner",
			cfgLibDir:  "/cfg/lib",
			pkgRunner:  "/pkg/runner",
			pkgLibDir:  "/pkg/lib",
			wantRunner: "/pkg/runner",
			wantLibDir: "/pkg/lib",
		},
		{
			name:         "nothing set uses embedded",
			wantEmbedded: true,
		},
		{
			name:         "package lib dir without package runner uses embedded",
			pkgLibDir:    "/pkg/lib",
			wantEmbedded: true,
		},
		{
			name:         "config lib dir alone uses embedded",
			cfgLibDir:    "/cfg/lib",
			wantEmbedded: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			cfg := &config.Config{VM: config.VMConfig{Runner: tt.cfgRunner, LibDir: tt.cfgLibDir}}
			runner, libDir, embedded := selectVMRuntime(cfg, tt.pkgRunner, tt.pkgLibDir)
			if embedded != tt.wantEmbedded {
				t.Fatalf("embedded = %v, want %v", embedded, tt.wantEmbedded)
			}
			if runner != tt.wantRunner {
				t.Errorf("runner = %q, want %q", runner, tt.wantRunner)
			}
			if libDir != tt.wantLibDir {
				t.Errorf("libDir = %q, want %q", libDir, tt.wantLibDir)
			}
		})
	}
}

func TestVMRuntimeWithPackageRunner(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	dir := t.TempDir()
	runner := filepath.Join(dir, "runner")
	if err := os.WriteFile(runner, []byte("#!/bin/sh\n"), 0o700); err != nil { //nolint:gosec // G306: test fixture
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, guestbin.FileName), []byte("guest"), 0o700); err != nil { //nolint:gosec // G306: test fixture
		t.Fatal(err)
	}
	pkgLibDir := filepath.Join(dir, "lib")

	rt, err := vmRuntimeWith(&config.Config{}, runner, pkgLibDir)
	if err != nil {
		t.Fatalf("vmRuntimeWith: %v", err)
	}
	if rt.runner != runner {
		t.Errorf("runner = %q, want %q", rt.runner, runner)
	}
	if rt.libDir != pkgLibDir {
		t.Errorf("libDir = %q, want %q", rt.libDir, pkgLibDir)
	}
}

func TestVMRuntimeWithMissingPackageRunner(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	runner := filepath.Join(t.TempDir(), "missing-runner")

	_, err := vmRuntimeWith(&config.Config{}, runner, "")
	if err == nil {
		t.Fatal("expected error for missing package runner")
	}
	assertContains(t, err.Error(), runner)
	assertContains(t, err.Error(), "[vm]")
	if !errors.Is(err, os.ErrNotExist) {
		t.Errorf("expected error to wrap os.ErrNotExist, got %v", err)
	}
}

func TestVMRuntimeWithEmbeddedBranch(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	_, err := vmRuntimeWith(&config.Config{}, "", "")
	if err == nil {
		t.Skip("a VM runtime is embedded in this build; the not-embedded error cannot be observed")
	}
	if !errors.Is(err, vm.ErrRuntimeNotEmbedded) {
		t.Fatalf("expected vm.ErrRuntimeNotEmbedded, got %v", err)
	}
}
