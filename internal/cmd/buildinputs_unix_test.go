//go:build darwin || linux

package cmd

import (
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/seznam/jailoc/internal/config"
	"github.com/seznam/jailoc/internal/vm"
	"github.com/seznam/jailoc/internal/workspace"
)

func TestEffectiveBuildInputs(t *testing.T) {
	t.Parallel()
	const (
		wsDockerfile   = "/work/build/overlay.Dockerfile"
		baseDockerfile = "/trusted/base.Dockerfile"
		context        = "/trusted/context"
		url            = "https://example.com/overlay.Dockerfile"
	)
	tests := []struct {
		name          string
		defaultsImage string
		base          string
		ws            workspace.Resolved
		want          []buildInput
	}{
		{
			name: "workspace image builds nothing",
			base: baseDockerfile,
			ws:   workspace.Resolved{Image: "registry.example.com/ws:1"},
		},
		{
			name:          "default image without a Dockerfile builds nothing",
			defaultsImage: "registry.example.com/default:1",
			base:          baseDockerfile,
			ws:            workspace.Resolved{BuildContext: context},
		},
		{
			name:          "default image overlay ignores the base Dockerfile",
			defaultsImage: "registry.example.com/default:1",
			base:          baseDockerfile,
			ws:            workspace.Resolved{Dockerfile: wsDockerfile},
			want: []buildInput{
				{kind: inputDockerfile, path: wsDockerfile},
				{kind: inputContext, path: "/work/build", dir: true},
			},
		},
		{
			name: "local Dockerfile defaults the context to its directory",
			ws:   workspace.Resolved{Dockerfile: wsDockerfile},
			want: []buildInput{
				{kind: inputDockerfile, path: wsDockerfile},
				{kind: inputContext, path: "/work/build", dir: true},
			},
		},
		{
			name: "explicit context replaces the default",
			ws:   workspace.Resolved{Dockerfile: wsDockerfile, BuildContext: context},
			want: []buildInput{
				{kind: inputDockerfile, path: wsDockerfile},
				{kind: inputContext, path: context, dir: true},
			},
		},
		{
			name: "URL Dockerfile without a context reads nothing local",
			base: "https://example.com/base.Dockerfile",
			ws:   workspace.Resolved{Dockerfile: url},
		},
		{
			name: "URL Dockerfile with a context",
			ws:   workspace.Resolved{Dockerfile: url, BuildContext: context},
			want: []buildInput{{kind: inputContext, path: context, dir: true}},
		},
		{
			name: "base Dockerfile without an overlay ignores the context",
			base: baseDockerfile,
			ws:   workspace.Resolved{BuildContext: context},
			want: []buildInput{{kind: inputBaseDockerfile, path: baseDockerfile}},
		},
		{
			name: "base Dockerfile and overlay",
			base: "  " + baseDockerfile + " ",
			ws:   workspace.Resolved{Dockerfile: wsDockerfile, BuildContext: "/trusted/context/"},
			want: []buildInput{
				{kind: inputBaseDockerfile, path: baseDockerfile},
				{kind: inputDockerfile, path: wsDockerfile},
				{kind: inputContext, path: context, dir: true},
			},
		},
		{
			name: "blank Dockerfile builds no overlay",
			ws:   workspace.Resolved{Dockerfile: " ", BuildContext: context},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			cfg := &config.Config{Base: config.BaseConfig{Dockerfile: tt.base}}
			cfg.Defaults.Image = tt.defaultsImage
			got, err := effectiveBuildInputs(cfg, &tt.ws)
			if err != nil {
				t.Fatal(err)
			}
			if !slices.Equal(got, tt.want) {
				t.Errorf("effectiveBuildInputs() = %+v\nwant                   %+v", got, tt.want)
			}
		})
	}
}

func TestEffectiveBuildInputsInvalidSource(t *testing.T) {
	t.Parallel()
	_, err := effectiveBuildInputs(&config.Config{}, &workspace.Resolved{Dockerfile: "relative/Dockerfile"})
	if err == nil {
		t.Fatal("effectiveBuildInputs() = nil error, want an unsupported source error")
	}
	assertContains(t, err.Error(), "relative/Dockerfile")
}

func TestCheckBuildInputs(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	work := filepath.Join(dir, "work")
	trusted := filepath.Join(dir, "trusted")
	for _, d := range []string{filepath.Join(work, "build"), filepath.Join(trusted, "ctx"), filepath.Join(dir, "hard")} {
		if err := os.MkdirAll(d, 0o750); err != nil {
			t.Fatal(err)
		}
	}
	for _, f := range []string{filepath.Join(work, "build", "Dockerfile"), filepath.Join(trusted, "Dockerfile"), filepath.Join(dir, "hard", "Dockerfile")} {
		if err := os.WriteFile(f, []byte("FROM scratch\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	links := [][2]string{
		{trusted, filepath.Join(dir, "trusted-alias")},
		{work, filepath.Join(dir, "work-alias")},
		// A link the workspace can repoint, reached from outside the share.
		{trusted, filepath.Join(work, "escape")},
		{filepath.Join(work, "escape"), filepath.Join(dir, "via")},
	}
	for _, l := range links {
		if err := os.Symlink(l[0], l[1]); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Link(filepath.Join(dir, "hard", "Dockerfile"), filepath.Join(work, "hardcopy")); err != nil {
		t.Fatal(err)
	}
	rw := func(host string) workspaceShare {
		return workspaceShare{workspace: "app", share: vm.Share{HostPath: host, GuestPath: host}}
	}
	file := func(p string) buildInput { return buildInput{kind: inputDockerfile, path: p} }
	ctx := func(p string) buildInput { return buildInput{kind: inputContext, path: p, dir: true} }

	tests := []struct {
		name    string
		inputs  []buildInput
		exports []workspaceShare
		wantErr string
	}{
		{"context equals a read-write share", []buildInput{ctx(work)}, []workspaceShare{rw(work)}, "lies in the read-write share"},
		{"Dockerfile inside a read-write share", []buildInput{file(filepath.Join(work, "build", "Dockerfile"))}, []workspaceShare{rw(work)}, "lies in the read-write share"},
		{"context contains a read-write share", []buildInput{ctx(dir)}, []workspaceShare{rw(work)}, "contains the read-write share"},
		{"Dockerfile beside a read-write share", []buildInput{file(filepath.Join(trusted, "Dockerfile"))}, []workspaceShare{rw(filepath.Join(trusted, "ctx"))}, ""},
		{"read-only share of the inputs", []buildInput{ctx(work), file(filepath.Join(work, "build", "Dockerfile"))}, []workspaceShare{{workspace: "app", share: vm.Share{HostPath: work, ReadOnly: true}}}, ""},
		{"inputs outside read-write shares", []buildInput{file(filepath.Join(trusted, "Dockerfile")), ctx(filepath.Join(trusted, "ctx"))}, []workspaceShare{rw(filepath.Join(work, "build"))}, ""},
		{"share reaches the context through an alias", []buildInput{ctx(filepath.Join(trusted, "ctx"))}, []workspaceShare{rw(filepath.Join(dir, "trusted-alias"))}, "lies in the read-write share"},
		{"input reaches a share through an alias", []buildInput{ctx(filepath.Join(dir, "work-alias", "build"))}, []workspaceShare{rw(work)}, "lies in the read-write share"},
		{"input runs through a link inside a read-write share", []buildInput{file(filepath.Join(dir, "via", "Dockerfile"))}, []workspaceShare{rw(work)}, "symbolic link"},
		{"share of another workspace", []buildInput{file(filepath.Join(work, "build", "Dockerfile"))}, []workspaceShare{{workspace: "other", share: vm.Share{HostPath: work}}}, `workspace "other"`},
		{"missing context inside a read-write share", []buildInput{ctx(filepath.Join(work, "missing", "ctx"))}, []workspaceShare{rw(work)}, "lies in the read-write share"},
		{"hard-linked Dockerfile", []buildInput{file(filepath.Join(dir, "hard", "Dockerfile"))}, []workspaceShare{rw(work)}, "hard link"},
		{"hard-linked Dockerfile without read-write shares", []buildInput{file(filepath.Join(dir, "hard", "Dockerfile"))}, []workspaceShare{{workspace: "app", share: vm.Share{HostPath: work, ReadOnly: true}}}, ""},
		{"unresolvable read-write share", []buildInput{file(filepath.Join(trusted, "Dockerfile"))}, []workspaceShare{rw(filepath.Join(dir, "gone"))}, "resolve the read-write share"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			err := checkBuildInputs(tt.inputs, tt.exports)
			if tt.wantErr == "" {
				if err != nil {
					t.Fatal(err)
				}
				return
			}
			if err == nil {
				t.Fatalf("checkBuildInputs() = nil, want %q", tt.wantErr)
			}
			assertContains(t, err.Error(), tt.wantErr)
		})
	}
}

func TestCheckBuildInputsReportsEveryConflict(t *testing.T) {
	t.Parallel()
	work := t.TempDir()
	dockerfile := filepath.Join(work, "Dockerfile")
	if err := os.WriteFile(dockerfile, []byte("FROM scratch\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	inputs := []buildInput{{kind: inputDockerfile, path: dockerfile}, {kind: inputContext, path: work, dir: true}}
	exports := []workspaceShare{{workspace: "app", share: vm.Share{HostPath: work}}, {workspace: "other", share: vm.Share{HostPath: work}}}
	err := checkBuildInputs(inputs, exports)
	if err == nil {
		t.Fatal("checkBuildInputs() = nil, want conflicts")
	}
	for _, want := range []string{
		"Dockerfile " + dockerfile,
		"build context " + work,
		`workspace "app"`,
		`workspace "other"`,
		"mount them read-only",
	} {
		assertContains(t, err.Error(), want)
	}
}
