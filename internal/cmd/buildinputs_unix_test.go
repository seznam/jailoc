//go:build darwin || linux

package cmd

import (
	"bytes"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"syscall"
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
			ws:   workspace.Resolved{Dockerfile: wsDockerfile, BuildContext: context + "/"},
			want: []buildInput{
				{kind: inputBaseDockerfile, path: baseDockerfile},
				{kind: inputDockerfile, path: wsDockerfile},
				{kind: inputContext, path: context + "/", dir: true},
			},
		},
		{
			name: "workspace paths stay as the build opens them",
			ws:   workspace.Resolved{Dockerfile: "/work/link/../overlay.Dockerfile", BuildContext: "/trusted/context/ "},
			want: []buildInput{
				{kind: inputDockerfile, path: "/work/link/../overlay.Dockerfile"},
				{kind: inputContext, path: "/trusted/context/ ", dir: true},
			},
		},
		{
			name: "default context is the lexical directory of the Dockerfile",
			ws:   workspace.Resolved{Dockerfile: "/work/link/../overlay.Dockerfile"},
			want: []buildInput{
				{kind: inputDockerfile, path: "/work/link/../overlay.Dockerfile"},
				{kind: inputContext, path: "/work", dir: true},
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

// TestEffectiveBuildInputsKeepsRawPaths pins that the paths the build reads
// are reported exactly as the builder receives them: the base Dockerfile
// trimmed, the workspace Dockerfile and context raw, the implicit context
// filepath.Dir of the raw Dockerfile. Cleaning "link/.." lexically would
// name a different file than the one the build opens.
func TestEffectiveBuildInputsKeepsRawPaths(t *testing.T) {
	t.Parallel()
	const (
		base       = "/trusted/link/../base.Dockerfile"
		dockerfile = "/work/link/../build/overlay.Dockerfile"
		buildCtx   = "/trusted/link/../context"
	)
	tests := []struct {
		name string
		base string
		ws   workspace.Resolved
		want []buildInput
	}{
		{
			name: "base Dockerfile is trimmed but not cleaned",
			base: "  " + base + "\t ",
			want: []buildInput{{kind: inputBaseDockerfile, path: base}},
		},
		{
			name: "workspace Dockerfile and context stay raw including trailing whitespace",
			ws:   workspace.Resolved{Dockerfile: dockerfile + " ", BuildContext: buildCtx + " "},
			want: []buildInput{
				{kind: inputDockerfile, path: dockerfile + " "},
				{kind: inputContext, path: buildCtx + " ", dir: true},
			},
		},
		{
			name: "workspace Dockerfile with link and parent stays raw",
			ws:   workspace.Resolved{Dockerfile: dockerfile, BuildContext: buildCtx},
			want: []buildInput{
				{kind: inputDockerfile, path: dockerfile},
				{kind: inputContext, path: buildCtx, dir: true},
			},
		},
		{
			name: "implicit context is the directory of the raw Dockerfile",
			ws:   workspace.Resolved{Dockerfile: dockerfile},
			want: []buildInput{
				{kind: inputDockerfile, path: dockerfile},
				{kind: inputContext, path: "/work/build", dir: true},
			},
		},
		{
			// The builder takes filepath.Dir of the raw value, whose last
			// element here is the file " ". Trimming first would read "/work".
			name: "implicit context of a Dockerfile named by whitespace",
			ws:   workspace.Resolved{Dockerfile: "/work/build/ "},
			want: []buildInput{
				{kind: inputDockerfile, path: "/work/build/ "},
				{kind: inputContext, path: "/work/build", dir: true},
			},
		},
		{
			name: "blank base and blank context change nothing",
			base: " \t ",
			ws:   workspace.Resolved{Dockerfile: dockerfile, BuildContext: "\t  "},
			want: []buildInput{
				{kind: inputDockerfile, path: dockerfile},
				{kind: inputContext, path: "/work/build", dir: true},
			},
		},
		{
			name: "blank Dockerfile builds no overlay and ignores the context",
			base: " ",
			ws:   workspace.Resolved{Dockerfile: " \t", BuildContext: buildCtx},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			cfg := &config.Config{Base: config.BaseConfig{Dockerfile: tt.base}}
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

func TestCheckBuildInputs(t *testing.T) {
	t.Parallel()
	// The errors name the real paths of detours, and t.TempDir is under a
	// symbolic link on macOS.
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	work := filepath.Join(dir, "work")
	trusted := filepath.Join(dir, "trusted")
	for _, d := range []string{filepath.Join(work, "build"), filepath.Join(trusted, "ctx"), filepath.Join(dir, "hard"), filepath.Join(dir, "linked", "sub")} {
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
		// Outside every share, ".." after it goes up from its target.
		{filepath.Join(work, "build"), filepath.Join(trusted, "up")},
		// Dangling until the guest creates the target.
		{filepath.Join(work, "later", "Dockerfile"), filepath.Join(trusted, "later.Dockerfile")},
	}
	for _, l := range links {
		if err := os.Symlink(l[0], l[1]); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Link(filepath.Join(dir, "hard", "Dockerfile"), filepath.Join(work, "hardcopy")); err != nil {
		t.Fatal(err)
	}
	// A context file whose other name lies in a read-write share.
	if err := os.WriteFile(filepath.Join(dir, "linked", "sub", "script.sh"), []byte("true\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Link(filepath.Join(dir, "linked", "sub", "script.sh"), filepath.Join(work, "script.sh")); err != nil {
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
		{"hard-linked file in a build context", []buildInput{ctx(filepath.Join(dir, "linked"))}, []workspaceShare{rw(work)}, "has other hard links"},
		{"hard-linked file in a build context without read-write shares", []buildInput{ctx(filepath.Join(dir, "linked"))}, []workspaceShare{{workspace: "app", share: vm.Share{HostPath: work, ReadOnly: true}}}, ""},
		{"missing read-write share beside the inputs", []buildInput{file(filepath.Join(trusted, "Dockerfile"))}, []workspaceShare{rw(filepath.Join(dir, "gone"))}, ""},
		{"missing read-write share inside a build context", []buildInput{ctx(trusted)}, []workspaceShare{rw(filepath.Join(trusted, "gone"))}, "contains the read-write share"},
		{"unresolvable read-write share", []buildInput{file(filepath.Join(trusted, "Dockerfile"))}, []workspaceShare{rw(filepath.Join(trusted, "Dockerfile", "sub"))}, "resolve the read-write share"},
		{"dot-dot after a link leads into a read-write share", []buildInput{file(trusted + "/up/../Dockerfile")}, []workspaceShare{rw(work)}, "Dockerfile " + trusted + "/up/../Dockerfile" + ` of workspace "app" lies in the read-write share`},
		{"dangling link into a read-write share", []buildInput{file(filepath.Join(trusted, "later.Dockerfile"))}, []workspaceShare{rw(work)}, "lies in the read-write share"},
		{"dot-dot after a missing component", []buildInput{file(trusted + "/nothing/../Dockerfile")}, []workspaceShare{rw(work)}, "does not exist and the path goes up from it"},
		{"dot-dot detours through a directory inside a read-write share", []buildInput{file(work + "/build/../../trusted/Dockerfile")}, []workspaceShare{rw(work)}, "runs through " + `"` + filepath.Join(work, "build") + `" inside the read-write share`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			err := checkBuildInputs("app", tt.inputs, tt.exports)
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
	err := checkBuildInputs("app", inputs, exports)
	if err == nil {
		t.Fatal("checkBuildInputs() = nil, want conflicts")
	}
	for _, want := range []string{
		"Dockerfile " + dockerfile + ` of workspace "app" lies in the read-write share ` + work + ` of workspace "app"`,
		"build context " + work + ` of workspace "app" lies in the read-write share ` + work + ` of workspace "other"`,
	} {
		assertContains(t, err.Error(), want)
	}
}

// TestBuildInputsOfLinkedPaths drives raw build input paths that contain
// symbolic links, "..", or dangling targets through effectiveBuildInputs and
// checkBuildInputs, and through checkBuildInputs alone. Every raw path is
// built by concatenation so that nothing cleans it before the build opens it.
func TestBuildInputsOfLinkedPaths(t *testing.T) {
	t.Parallel()
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	var (
		share     = dir + "/share"
		trusted   = dir + "/trusted"
		elsewhere = dir + "/elsewhere"
	)
	for _, d := range []string{share + "/sub", trusted + "/ctx", elsewhere} {
		if err := os.MkdirAll(d, 0o750); err != nil {
			t.Fatal(err)
		}
	}
	files := map[string]string{
		share + "/Dockerfile":        "FROM share\n",
		share + "/base.Dockerfile":   "FROM share-base\n",
		share + "/marker":            "share\n",
		trusted + "/Dockerfile":      "FROM trusted\n",
		trusted + "/base.Dockerfile": "FROM trusted-base\n",
		trusted + "/marker":          "trusted\n",
	}
	for f, content := range files {
		if err := os.WriteFile(f, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	links := map[string]string{
		// A trusted link into an existing subdirectory of the share: "link/.."
		// is the share itself, not trusted.
		trusted + "/link": share + "/sub",
		// Dangling links whose targets the guest can create in the share.
		trusted + "/dangling-abs":     share + "/missing/Dockerfile",
		trusted + "/dangling-rel":     "../share/missing/Dockerfile",
		trusted + "/dangling-ctx-abs": share + "/missing-ctx",
		trusted + "/dangling-ctx-rel": "../share/missing-ctx",
		trusted + "/deep":             "../share/a/b/c/Dockerfile",
		// A dangling link to a missing target outside every share.
		trusted + "/out-missing": "../elsewhere/x/y/Dockerfile",
		// Trusted links whose targets contain "..", behind another link.
		trusted + "/dots-existing": "link/../Dockerfile",
		trusted + "/dots-missing":  "link/../missing/Dockerfile",
		// A link the guest can repoint, followed by "..": "wl/.." is trusted.
		share + "/wl": trusted + "/ctx",
	}
	for l, target := range links {
		if err := os.Symlink(target, l); err != nil {
			t.Fatal(err)
		}
	}
	rw := workspaceShare{workspace: "app", share: vm.Share{HostPath: share}}
	ro := workspaceShare{workspace: "app", share: vm.Share{HostPath: share, ReadOnly: true}}
	plainFile := buildInput{kind: inputDockerfile, path: trusted + "/Dockerfile"}
	plainCtx := buildInput{kind: inputContext, path: trusted + "/ctx", dir: true}
	file := func(p string) []buildInput { return []buildInput{{kind: inputDockerfile, path: p}, plainCtx} }
	ctx := func(p string) []buildInput { return []buildInput{plainFile, {kind: inputContext, path: p, dir: true}} }
	base := func(p string) []buildInput { return []buildInput{{kind: inputBaseDockerfile, path: p}} }

	tests := []struct {
		name    string
		inputs  []buildInput
		exports []workspaceShare
		wantErr string
		// read is a path the build opens and the content it must have, to
		// show the raw path names the file the test claims it does.
		read, content string
	}{
		{"Dockerfile through a link into the share and ..", file(trusted + "/link/../Dockerfile"), []workspaceShare{rw}, "lies in the read-write share", trusted + "/link/../Dockerfile", "FROM share\n"},
		{"context through a link into the share and ..", ctx(trusted + "/link/.."), []workspaceShare{rw}, "read-write share", trusted + "/link/../marker", "share\n"},
		{"base Dockerfile through a link into the share and ..", base(trusted + "/link/../base.Dockerfile"), []workspaceShare{rw}, "lies in the read-write share", trusted + "/link/../base.Dockerfile", "FROM share-base\n"},
		{"dangling absolute link Dockerfile", file(trusted + "/dangling-abs"), []workspaceShare{rw}, "read-write share", "", ""},
		{"dangling relative link Dockerfile", file(trusted + "/dangling-rel"), []workspaceShare{rw}, "read-write share", "", ""},
		{"dangling absolute link context", ctx(trusted + "/dangling-ctx-abs"), []workspaceShare{rw}, "read-write share", "", ""},
		{"dangling relative link context", ctx(trusted + "/dangling-ctx-rel"), []workspaceShare{rw}, "read-write share", "", ""},
		{"dangling base Dockerfile link", base(trusted + "/dangling-rel"), []workspaceShare{rw}, "read-write share", "", ""},
		{"link to a missing multi-component target", file(trusted + "/deep"), []workspaceShare{rw}, "read-write share", "", ""},
		{"link target with .. behind a link, existing", file(trusted + "/dots-existing"), []workspaceShare{rw}, "lies in the read-write share", trusted + "/dots-existing", "FROM share\n"},
		{"link target with .. behind a link, missing", file(trusted + "/dots-missing"), []workspaceShare{rw}, "read-write share", "", ""},
		{"Dockerfile through a writable link and ..", file(share + "/wl/../Dockerfile"), []workspaceShare{rw}, "symbolic link", share + "/wl/../Dockerfile", "FROM trusted\n"},
		{"context through a writable link and ..", ctx(share + "/wl/.."), []workspaceShare{rw}, "symbolic link", share + "/wl/../marker", "trusted\n"},
		{"missing target outside the shares", file(trusted + "/out-missing"), []workspaceShare{rw}, "", "", ""},
		{"dangling link into a read-only share", file(trusted + "/dangling-abs"), []workspaceShare{ro}, "", "", ""},
		{"link and .. into a read-only share", file(trusted + "/link/../Dockerfile"), []workspaceShare{ro}, "", trusted + "/link/../Dockerfile", "FROM share\n"},
		{"writable link and .. in a read-only share", ctx(share + "/wl/.."), []workspaceShare{ro}, "", "", ""},
		{"dangling link without shares", file(trusted + "/dangling-abs"), nil, "", "", ""},
		{"link and .. without shares", file(trusted + "/link/../Dockerfile"), nil, "", "", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if tt.read != "" {
				got, err := os.ReadFile(tt.read)
				if err != nil {
					t.Fatal(err)
				}
				if string(got) != tt.content {
					t.Fatalf("%s holds %q, want %q", tt.read, got, tt.content)
				}
			}
			assertErr := func(t *testing.T, err error) {
				t.Helper()
				if tt.wantErr == "" {
					if err != nil {
						t.Fatal(err)
					}
					return
				}
				if err == nil {
					t.Fatalf("check = nil, want %q", tt.wantErr)
				}
				assertContains(t, err.Error(), tt.wantErr)
			}
			t.Run("raw inputs", func(t *testing.T) {
				assertErr(t, checkBuildInputs("app", tt.inputs, tt.exports))
			})
			t.Run("from the configuration", func(t *testing.T) {
				cfg, ws := buildInputsConfig(tt.inputs)
				inputs, err := effectiveBuildInputs(cfg, ws)
				if err != nil {
					t.Fatal(err)
				}
				assertErr(t, checkBuildInputs("app", inputs, tt.exports))
			})
		})
	}
}

// buildInputsConfig returns the configuration and workspace whose build
// reads exactly the raw inputs: the base Dockerfile padded with whitespace as
// a user may write it, the workspace Dockerfile and context as they are.
func buildInputsConfig(inputs []buildInput) (*config.Config, *workspace.Resolved) {
	cfg, ws := &config.Config{}, &workspace.Resolved{}
	for _, in := range inputs {
		switch in.kind {
		case inputBaseDockerfile:
			cfg.Base.Dockerfile = "  " + in.path + "  "
		case inputDockerfile:
			ws.Dockerfile = in.path
		case inputContext:
			ws.BuildContext = in.path
		}
	}
	return cfg, ws
}

// writeBuildInputTree creates dirs, then files with their content.
func writeBuildInputTree(t *testing.T, dirs []string, files map[string]string) {
	t.Helper()
	for _, d := range dirs {
		if err := os.MkdirAll(d, 0o750); err != nil {
			t.Fatal(err)
		}
	}
	for f, content := range files {
		if err := os.WriteFile(f, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
}

// TestCheckBuildInputsReplaceableDirectory pins that an input may not run
// through an ordinary directory of a read-write share: share/sub/../../trusted
// opens the trusted tree until the guest replaces share/sub with the link
// "evil/a/b", then share/evil/trusted. Each case owns its fixture.
func TestCheckBuildInputsReplaceableDirectory(t *testing.T) {
	t.Parallel()
	const (
		through  = "/sub/../../trusted"
		atRoot   = "/../trusted"
		rwErrMsg = "read-write share"
	)
	tests := []struct {
		name string
		kind string
		via  string
		mode string // "rw", "ro" or "none"
		// attack replaces share/sub with a link after the check.
		attack  bool
		wantErr string
	}{
		{"Dockerfile through a replaceable directory", inputDockerfile, through, "rw", true, rwErrMsg},
		{"base Dockerfile through a replaceable directory", inputBaseDockerfile, through, "rw", true, rwErrMsg},
		{"build context through a replaceable directory", inputContext, through, "rw", true, rwErrMsg},
		{"Dockerfile through a replaceable directory in a read-only share", inputDockerfile, through, "ro", false, ""},
		{"base Dockerfile through a replaceable directory in a read-only share", inputBaseDockerfile, through, "ro", false, ""},
		{"build context through a replaceable directory in a read-only share", inputContext, through, "ro", false, ""},
		{"Dockerfile through a replaceable directory without shares", inputDockerfile, through, "none", false, ""},
		{"build context through a replaceable directory without shares", inputContext, through, "none", false, ""},
		// Stepping out of the share root itself names no directory the guest can replace.
		{"Dockerfile stepping out of the share root", inputDockerfile, atRoot, "rw", false, ""},
		{"base Dockerfile stepping out of the share root", inputBaseDockerfile, atRoot, "rw", false, ""},
		{"build context stepping out of the share root", inputContext, atRoot, "rw", false, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			dir, err := filepath.EvalSymlinks(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			share, trusted, evil := dir+"/share", dir+"/trusted", dir+"/share/evil/trusted"
			file := map[string]string{
				inputDockerfile:     "Dockerfile",
				inputBaseDockerfile: "base.Dockerfile",
				inputContext:        "marker",
			}[tt.kind]
			files := map[string]string{}
			for _, d := range []string{trusted, evil} {
				for _, f := range []string{"Dockerfile", "base.Dockerfile", "marker"} {
					files[d+"/"+f] = d + "/" + f + "\n"
				}
			}
			writeBuildInputTree(t, []string{share + "/sub", share + "/evil/a/b", trusted + "/ctx", evil}, files)

			raw := share + tt.via
			if tt.kind != inputContext {
				raw += "/" + file
			}
			in := buildInput{kind: tt.kind, path: raw, dir: tt.kind == inputContext}
			inputs := []buildInput{in}
			switch tt.kind {
			case inputDockerfile:
				inputs = append(inputs, buildInput{kind: inputContext, path: trusted + "/ctx", dir: true})
			case inputContext:
				inputs = append(inputs, buildInput{kind: inputDockerfile, path: trusted + "/Dockerfile"})
			}
			var exports []workspaceShare
			switch tt.mode {
			case "rw":
				exports = []workspaceShare{{workspace: "app", share: vm.Share{HostPath: share}}}
			case "ro":
				exports = []workspaceShare{{workspace: "app", share: vm.Share{HostPath: share, ReadOnly: true}}}
			}

			readPath := share + tt.via + "/" + file
			wantContent := trusted + "/" + file + "\n"
			got, err := os.ReadFile(readPath) //nolint:gosec // G304: path of a fixture this test owns; it must stay uncleaned
			if err != nil {
				t.Fatal(err)
			}
			if string(got) != wantContent {
				t.Fatalf("before the replacement %s holds %q, want %q", readPath, got, wantContent)
			}

			err = checkBuildInputs("app", inputs, exports)
			switch {
			case tt.wantErr == "" && err != nil:
				t.Errorf("checkBuildInputs() = %v, want nil", err)
			case tt.wantErr != "" && err == nil:
				t.Errorf("checkBuildInputs() = nil before the replacement, want %q", tt.wantErr)
			case tt.wantErr != "":
				assertContains(t, err.Error(), tt.wantErr)
			}
			if !tt.attack {
				return
			}

			if err := os.Remove(share + "/sub"); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink("evil/a/b", share+"/sub"); err != nil {
				t.Fatal(err)
			}
			got, err = os.ReadFile(readPath) //nolint:gosec // G304: path of a fixture this test owns; it must stay uncleaned
			if err != nil {
				t.Fatal(err)
			}
			if want := evil + "/" + file + "\n"; string(got) != want {
				t.Fatalf("after the replacement %s holds %q, want %q", readPath, got, want)
			}
		})
	}
}

func TestContextHardLink(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	for _, f := range []string{"a", "b/c", "b/d/e"} {
		p := filepath.Join(dir, f)
		if err := os.MkdirAll(filepath.Dir(p), 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, nil, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if got, err := contextHardLink(dir, 100, hardLinked); err != nil || got != "" {
		t.Fatalf("contextHardLink() = %q, %v; want no hard link", got, err)
	}
	if _, err := contextHardLink(dir, 3, hardLinked); err == nil {
		t.Fatal("contextHardLink() over the limit = nil error, want too many entries")
	} else {
		assertContains(t, err.Error(), "more than 3 entries")
	}
	if err := os.Link(filepath.Join(dir, "b", "d", "e"), filepath.Join(t.TempDir(), "other")); err != nil {
		t.Fatal(err)
	}
	if got, err := contextHardLink(dir, 100, func(fs.FileInfo) bool { return false }); err != nil || got != "" {
		t.Fatalf("contextHardLink() with links nothing exposes = %q, %v; want none", got, err)
	}
	if got, err := contextHardLink(dir, 100, hardLinked); err != nil || got != filepath.Join(dir, "b", "d", "e") {
		t.Fatalf("contextHardLink() = %q, %v; want %q", got, err, filepath.Join(dir, "b", "d", "e"))
	}
}

// fakeWorkspaceVM records a workspace VM with shares under root whose
// supervisor has pid.
func fakeWorkspaceVM(t *testing.T, root, name string, pid int, shares ...vm.Share) {
	t.Helper()
	stateDir := filepath.Join(root, "workspaces", name)
	if err := vm.WriteSpec(vm.Spec{StateDir: stateDir, Workspace: name, Shares: shares}); err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(vm.State{PID: pid, Ready: true})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(vm.StateFile(stateDir), data, 0o600); err != nil {
		t.Fatal(err)
	}
}

// fakeStartingWorkspace records a start of workspace name under root that
// holds its start lock until the test ends.
func fakeStartingWorkspace(t *testing.T, root, name string, shares []vm.Share, inputs []buildInput) {
	t.Helper()
	stateDir := filepath.Join(root, "workspaces", name)
	release, err := holdStartLock(stateDir, name)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(release)
	if err := writeAdmission(stateDir, shares, inputs); err != nil {
		t.Fatal(err)
	}
}

func TestTracePath(t *testing.T) {
	t.Parallel()
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	for _, d := range []string{"a/b", "c"} {
		if err := os.MkdirAll(filepath.Join(dir, d), 0o750); err != nil {
			t.Fatal(err)
		}
	}
	for _, l := range [][2]string{{filepath.Join(dir, "a", "b"), filepath.Join(dir, "c", "ab")}, {"../missing/x", filepath.Join(dir, "c", "dangling")}} {
		if err := os.Symlink(l[0], l[1]); err != nil {
			t.Fatal(err)
		}
	}
	tests := []struct {
		path        string
		wantReal    string
		wantLinks   []string
		wantDetours []string
	}{
		{filepath.Join(dir, "a", "b"), filepath.Join(dir, "a", "b"), nil, nil},
		{dir + "/c/ab/../f", filepath.Join(dir, "a", "f"), []string{filepath.Join(dir, "c", "ab")}, []string{filepath.Join(dir, "c"), filepath.Join(dir, "a", "b")}},
		{filepath.Join(dir, "c", "dangling"), filepath.Join(dir, "missing", "x"), []string{filepath.Join(dir, "c", "dangling")}, []string{filepath.Join(dir, "c")}},
		{filepath.Join(dir, "new", "deeper"), filepath.Join(dir, "new", "deeper"), nil, nil},
		// Directories that ".." leaves are not on the real path.
		{dir + "/a/b/../../c", filepath.Join(dir, "c"), nil, []string{filepath.Join(dir, "a"), filepath.Join(dir, "a", "b")}},
	}
	for _, tt := range tests {
		got, err := tracePath(tt.path)
		if err != nil {
			t.Fatalf("tracePath(%q): %v", tt.path, err)
		}
		if got.real != tt.wantReal || !slices.Equal(got.links, tt.wantLinks) || !slices.Equal(got.detours, tt.wantDetours) {
			t.Errorf("tracePath(%q) = %+v, want real %q links %q detours %q", tt.path, got, tt.wantReal, tt.wantLinks, tt.wantDetours)
		}
	}
	for _, bad := range []string{"relative", dir + "/new/../x"} {
		if _, err := tracePath(bad); err == nil {
			t.Errorf("tracePath(%q) = nil error, want it refused", bad)
		}
	}
}

func TestTracePathResolves(t *testing.T) {
	t.Parallel()
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(dir+"/sub", 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(dir+"/file", []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	for l, target := range map[string]string{
		"link":     dir + "/sub",
		"linkfile": "file",
		"chain1":   dir + "/chain2",
		"chain2":   "sub",
		"dangling": "missing/x",
		"dotmiss":  "missing/../y",
		"relup":    "sub/../file",
		"loopa":    "loopb",
		"loopb":    "loopa",
		"filedots": "file/..",
	} {
		if err := os.Symlink(target, dir+"/"+l); err != nil {
			t.Fatal(err)
		}
	}
	tests := []struct {
		name      string
		path      string
		want      string
		wantLinks []string
		// exists marks paths that EvalSymlinks must resolve to the same file.
		exists  bool
		wantErr error
		anyErr  bool
	}{
		{name: "directory", path: dir + "/sub", want: dir + "/sub", exists: true},
		{name: "dot and empty components", path: dir + "//sub/./", want: dir + "/sub", exists: true},
		{name: "parent of a directory", path: dir + "/sub/../sub", want: dir + "/sub", exists: true},
		{name: "link then parent", path: dir + "/link/..", want: dir, wantLinks: []string{dir + "/link"}, exists: true},
		{name: "link to a file", path: dir + "/linkfile", want: dir + "/file", wantLinks: []string{dir + "/linkfile"}, exists: true},
		{name: "absolute then relative link chain", path: dir + "/chain1", want: dir + "/sub", wantLinks: []string{dir + "/chain1", dir + "/chain2"}, exists: true},
		{name: "relative target with parent", path: dir + "/relup", want: dir + "/file", wantLinks: []string{dir + "/relup"}, exists: true},
		{name: "missing multi-component target", path: dir + "/missing/a/b", want: dir + "/missing/a/b"},
		{name: "missing target ignores dot and empty", path: dir + "/missing//a/./b/", want: dir + "/missing/a/b"},
		{name: "dangling link", path: dir + "/dangling", want: dir + "/missing/x", wantLinks: []string{dir + "/dangling"}},
		{name: "missing then parent", path: dir + "/missing/../file", anyErr: true},
		{name: "missing then trailing parent", path: dir + "/missing/..", anyErr: true},
		{name: "dangling link target with parent", path: dir + "/dotmiss", anyErr: true},
		{name: "file then slash", path: dir + "/file/", wantErr: syscall.ENOTDIR},
		{name: "file then dot", path: dir + "/file/.", wantErr: syscall.ENOTDIR},
		{name: "file then parent", path: dir + "/file/..", wantErr: syscall.ENOTDIR},
		{name: "file then name", path: dir + "/file/x", wantErr: syscall.ENOTDIR},
		{name: "link to a file then slash", path: dir + "/linkfile/", wantErr: syscall.ENOTDIR},
		{name: "link target of file then parent", path: dir + "/filedots", wantErr: syscall.ENOTDIR},
		{name: "link loop", path: dir + "/loopa", anyErr: true},
		{name: "root", path: "/", want: "/", exists: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, err := tracePath(tt.path)
			switch {
			case tt.wantErr != nil:
				if !errors.Is(err, tt.wantErr) {
					t.Fatalf("tracePath(%q) error = %v, want %v", tt.path, err, tt.wantErr)
				}
				return
			case tt.anyErr:
				if err == nil {
					t.Fatalf("tracePath(%q) = %q, nil, want an error", tt.path, got.real)
				}
				return
			case err != nil:
				t.Fatal(err)
			}
			if got.real != tt.want {
				t.Errorf("tracePath(%q).real = %q, want %q", tt.path, got.real, tt.want)
			}
			if !slices.Equal(got.links, tt.wantLinks) {
				t.Errorf("tracePath(%q).links = %q, want %q", tt.path, got.links, tt.wantLinks)
			}
			if !tt.exists {
				return
			}
			if want, err := filepath.EvalSymlinks(tt.path); err != nil || want != got.real {
				t.Errorf("EvalSymlinks(%q) = %q, %v; want %q", tt.path, want, err, got.real)
			}
		})
	}
}

func TestCheckBuildInputsCaseInsensitiveAlias(t *testing.T) {
	t.Parallel()
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	work := filepath.Join(dir, "work")
	if err := os.MkdirAll(work, 0o750); err != nil {
		t.Fatal(err)
	}
	alias := filepath.Join(dir, "WORK")
	if _, err := os.Stat(alias); err != nil {
		t.Skip("the file system of the temporary directory is case-sensitive")
	}
	inputs := []buildInput{{kind: inputDockerfile, path: filepath.Join(work, "Dockerfile")}}
	err = checkBuildInputs("app", inputs, []workspaceShare{{workspace: "app", share: vm.Share{HostPath: alias}}})
	if err == nil {
		t.Fatal("checkBuildInputs() = nil, want the share spelled in other case to count")
	}
	assertContains(t, err.Error(), "lies in the read-write share "+alias)
}

// TestCheckBuildInputsCaseInsensitive pins that a share and an input spelled
// with different case conflict on a case-insensitive filesystem. It skips on
// a case-sensitive one.
func TestCheckBuildInputsCaseInsensitive(t *testing.T) {
	t.Parallel()
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(dir+"/Build", 0o750); err != nil {
		t.Fatal(err)
	}
	physical, err := os.Stat(dir + "/Build")
	if err != nil {
		t.Fatal(err)
	}
	probe, err := os.Stat(dir + "/build")
	switch {
	case errors.Is(err, os.ErrNotExist):
		t.Skip("the temporary directory is on a case-sensitive filesystem")
	case err != nil:
		t.Fatal(err)
	case !os.SameFile(physical, probe):
		t.Skip("the temporary directory is on a filesystem that does not fold case")
	}

	var (
		work    = dir + "/Work"
		share   = work + "/Share"
		trusted = work + "/Trusted"
	)
	writeBuildInputTree(t,
		[]string{share + "/Sub", share + "/Same", trusted + "/ctx", work + "/Share2"},
		map[string]string{
			share + "/Dockerfile":       "FROM share\n",
			share + "/Same/Dockerfile":  "FROM same\n",
			trusted + "/Dockerfile":     "FROM trusted\n",
			work + "/Share2/Dockerfile": "FROM share2\n",
		})
	if err := os.Symlink(trusted+"/ctx", share+"/Link"); err != nil {
		t.Fatal(err)
	}

	rw := []workspaceShare{{workspace: "app", share: vm.Share{HostPath: share}}}
	ro := []workspaceShare{{workspace: "app", share: vm.Share{HostPath: share, ReadOnly: true}}}
	file := func(p string) []buildInput { return []buildInput{{kind: inputDockerfile, path: p}} }
	ctx := func(p string) []buildInput { return []buildInput{{kind: inputContext, path: p, dir: true}} }

	tests := []struct {
		name    string
		inputs  []buildInput
		exports []workspaceShare
		wantErr string
	}{
		{"existing input in a share spelled with other case", file(work + "/share/Dockerfile"), rw, "lies in the read-write share"},
		{"existing input in a subdirectory spelled with other case", file(work + "/SHARE/same/Dockerfile"), rw, "lies in the read-write share"},
		{"missing input suffix in a share spelled with other case", file(work + "/share/Missing/Dockerfile"), rw, "lies in the read-write share"},
		{"context equal to the share with other case", ctx(work + "/share"), rw, "lies in the read-write share"},
		{"context above the share with other case", ctx(work), rw, "contains the read-write share"},
		{"context above the share spelled with other case", ctx(dir + "/work"), rw, "contains the read-write share"},
		{"guest link reached through the share with other case", file(work + "/share/Link/Dockerfile"), rw, "symbolic link"},
		{"guest link spelled with other case", file(work + "/Share/link/Dockerfile"), rw, "symbolic link"},
		{"replaceable directory reached through the share with other case", file(work + "/share/Sub/../../Trusted/Dockerfile"), rw, "read-write share"},
		{"input beside the share", file(trusted + "/Dockerfile"), rw, ""},
		{"input beside the share spelled with other case", file(work + "/trusted/Dockerfile"), rw, ""},
		{"input in a sibling that shares the name prefix", file(work + "/share2/Dockerfile"), rw, ""},
		{"context equal to a sibling that shares the name prefix", ctx(work + "/SHARE2"), rw, ""},
		{"read-only share with other case", file(work + "/share/Dockerfile"), ro, ""},
		{"read-only share and replaceable directory with other case", file(work + "/share/Sub/../../Trusted/Dockerfile"), ro, ""},
		{"no shares and other case", ctx(work + "/share"), nil, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			for _, in := range tt.inputs {
				if _, err := os.Stat(in.path); err != nil && !errors.Is(err, os.ErrNotExist) {
					t.Fatal(err)
				}
			}
			err := checkBuildInputs("app", tt.inputs, tt.exports)
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

func TestStartLock(t *testing.T) {
	t.Parallel()
	stateDir := filepath.Join(t.TempDir(), "workspaces", "app")
	if held, err := startLockHeld(stateDir); err != nil || held {
		t.Fatalf("startLockHeld() before any start = %v, %v; want false", held, err)
	}
	release, err := holdStartLock(stateDir, "app")
	if err != nil {
		t.Fatal(err)
	}
	if held, err := startLockHeld(stateDir); err != nil || !held {
		t.Fatalf("startLockHeld() during a start = %v, %v; want true", held, err)
	}
	if _, err := holdStartLock(stateDir, "app"); err == nil {
		t.Fatal("holdStartLock() during a start = nil error, want the second start refused")
	} else {
		assertContains(t, err.Error(), "being started by another jailoc process")
	}
	release()
	if held, err := startLockHeld(stateDir); err != nil || held {
		t.Fatalf("startLockHeld() after the start = %v, %v; want false", held, err)
	}
}

func TestLiveWorkspaces(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	if got, err := liveWorkspaces(filepath.Join(root, "missing"), ""); err != nil || len(got) != 0 {
		t.Fatalf("liveWorkspaces() without VMs = %v, %v; want none", got, err)
	}
	ws := func(name string) string { return filepath.Join(root, "workspaces", name) }
	guest := func(name string) []workspaceShare {
		return []workspaceShare{
			{workspace: name, share: vm.Share{HostPath: vm.GuestDataDir(ws(name))}},
			{workspace: name, share: vm.Share{HostPath: vm.BootRootDir(ws(name))}},
		}
	}
	fakeWorkspaceVM(t, root, "a", os.Getpid(), vm.Share{HostPath: "/x", GuestPath: "/x"}, vm.Share{HostPath: "/y", GuestPath: "/y", ReadOnly: true})
	fakeWorkspaceVM(t, root, "stopped", 0, vm.Share{HostPath: "/z", GuestPath: "/z"})
	fakeWorkspaceVM(t, root, "self", os.Getpid(), vm.Share{HostPath: "/s", GuestPath: "/s"})
	// The supervisor is gone, but its runner still serves the shares.
	fakeWorkspaceVM(t, root, "orphan", 0, vm.Share{HostPath: "/o", GuestPath: "/o"})
	if err := os.MkdirAll(filepath.Join(ws("orphan"), "microvm"), 0o700); err != nil {
		t.Fatal(err)
	}
	runner := []byte(`{"version":1,"active":true,"pid":` + strconv.Itoa(os.Getpid()) + `}`)
	if err := os.WriteFile(filepath.Join(ws("orphan"), "microvm", "go-microvm-state.json"), runner, 0o600); err != nil {
		t.Fatal(err)
	}
	// A start that has not booted its VM yet, from another configuration.
	input := buildInput{kind: inputDockerfile, path: "/elsewhere/Dockerfile"}
	fakeStartingWorkspace(t, root, "booting", []vm.Share{{HostPath: "/b", GuestPath: "/b"}}, []buildInput{input})
	// A record of an earlier start, which no longer counts.
	if err := writeAdmission(ws("stopped"), []vm.Share{{HostPath: "/z"}}, []buildInput{input}); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "workspaces", "a.reload.lock"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := liveWorkspaces(root, "self")
	if err != nil {
		t.Fatal(err)
	}
	want := []liveWorkspace{
		{name: "a", exports: append([]workspaceShare{
			{workspace: "a", share: vm.Share{HostPath: "/x", GuestPath: "/x"}},
			{workspace: "a", share: vm.Share{HostPath: "/y", GuestPath: "/y", ReadOnly: true}},
		}, guest("a")...)},
		{name: "booting", exports: append([]workspaceShare{{workspace: "booting", share: vm.Share{HostPath: "/b", GuestPath: "/b"}}}, guest("booting")...), inputs: []buildInput{input}},
		{name: "orphan", exports: append([]workspaceShare{{workspace: "orphan", share: vm.Share{HostPath: "/o", GuestPath: "/o"}}}, guest("orphan")...)},
	}
	if len(got) != len(want) {
		t.Fatalf("liveWorkspaces() = %+v, want %+v", got, want)
	}
	for i := range want {
		if got[i].name != want[i].name || !slices.Equal(got[i].exports, want[i].exports) || !slices.Equal(got[i].inputs, want[i].inputs) {
			t.Errorf("liveWorkspaces()[%d] = %+v, want %+v", i, got[i], want[i])
		}
	}

	// A live VM without a spec could share anything.
	if err := os.MkdirAll(ws("broken"), 0o700); err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(vm.State{PID: os.Getpid()})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(vm.StateFile(ws("broken")), data, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := liveWorkspaces(root, "self"); err == nil {
		t.Fatal("liveWorkspaces() with a live VM without a spec = nil error")
	}
	if err := os.WriteFile(vm.StateFile(ws("broken")), []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := liveWorkspaces(root, "self"); err == nil {
		t.Fatal("liveWorkspaces() with an unreadable state = nil error")
	}
}

func TestCheckBuildAdmission(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	project := filepath.Join(dir, "project")
	trusted := filepath.Join(dir, "trusted")
	for _, d := range []string{project, trusted} {
		if err := os.MkdirAll(d, 0o750); err != nil {
			t.Fatal(err)
		}
	}
	dockerfile := filepath.Join(trusted, "Dockerfile")
	if err := os.WriteFile(dockerfile, []byte("FROM scratch\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	busy := t.TempDir()
	fakeWorkspaceVM(t, busy, "other", os.Getpid(), vm.Share{HostPath: trusted, GuestPath: trusted})
	starting := t.TempDir()
	fakeStartingWorkspace(t, starting, "other", []vm.Share{{HostPath: trusted, GuestPath: trusted}}, nil)
	tests := []struct {
		name    string
		ws      workspace.Resolved
		shares  []vm.Share
		root    string
		wantErr []string
	}{
		{
			name:   "inputs outside every share",
			ws:     workspace.Resolved{Name: "app", Dockerfile: dockerfile},
			shares: []vm.Share{{HostPath: project, GuestPath: project}},
			root:   t.TempDir(),
		},
		{
			name:    "own read-write share",
			ws:      workspace.Resolved{Name: "app", Dockerfile: dockerfile},
			shares:  []vm.Share{{HostPath: trusted, GuestPath: trusted}},
			root:    t.TempDir(),
			wantErr: []string{`Dockerfile ` + dockerfile + ` of workspace "app" lies in the read-write share ` + trusted + ` of workspace "app"`},
		},
		{
			name:   "own read-only share",
			ws:     workspace.Resolved{Name: "app", Dockerfile: dockerfile},
			shares: []vm.Share{{HostPath: trusted, GuestPath: trusted, ReadOnly: true}},
			root:   t.TempDir(),
		},
		{
			name:    "share of a running workspace",
			ws:      workspace.Resolved{Name: "app", Dockerfile: dockerfile},
			root:    busy,
			wantErr: []string{`of workspace "other"`, "jailoc down"},
		},
		{
			name:    "share of a starting workspace",
			ws:      workspace.Resolved{Name: "app", Dockerfile: dockerfile},
			root:    starting,
			wantErr: []string{`of workspace "other"`},
		},
		{
			name:   "image used directly",
			ws:     workspace.Resolved{Name: "app", Image: "registry.example.com/app:1"},
			shares: []vm.Share{{HostPath: trusted, GuestPath: trusted}},
			root:   busy,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			live, err := liveWorkspaces(tt.root, tt.ws.Name)
			if err != nil {
				t.Fatal(err)
			}
			exports := vmExports(tt.ws.Name, filepath.Join(tt.root, "workspaces", tt.ws.Name), tt.shares)
			err = checkBuildAdmission(&config.Config{}, &tt.ws, exports, live)
			if len(tt.wantErr) == 0 {
				if err != nil {
					t.Fatal(err)
				}
				return
			}
			if err == nil {
				t.Fatalf("checkBuildAdmission() = nil, want %q", tt.wantErr)
			}
			for _, want := range tt.wantErr {
				assertContains(t, err.Error(), want)
			}
		})
	}
}

func TestCheckShareExposure(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	trusted := filepath.Join(dir, "trusted")
	self := filepath.Join(dir, "self")
	for _, d := range []string{trusted, self} {
		if err := os.MkdirAll(d, 0o750); err != nil {
			t.Fatal(err)
		}
	}
	cfg := &config.Config{Workspaces: map[string]config.Workspace{
		"app":    {Dockerfile: filepath.Join(trusted, "Dockerfile")},
		"remote": {Dockerfile: "https://example.com/Dockerfile"},
		"image":  {Image: "registry.example.com/image:1"},
		"self":   {Dockerfile: filepath.Join(self, "Dockerfile")},
	}}
	share := func(host string, ro bool) []workspaceShare {
		return []workspaceShare{{workspace: "self", share: vm.Share{HostPath: host, GuestPath: host, ReadOnly: ro}}}
	}

	err := checkShareExposure(cfg, share(dir, false), "self", nil)
	if err == nil {
		t.Fatal("checkShareExposure() = nil, want the inputs of workspace app")
	}
	assertContains(t, err.Error(), `build context `+trusted+` of workspace "app" lies in the read-write share `+dir+` of workspace "self"`)
	if strings.Contains(err.Error(), `of workspace "self" lies`) {
		t.Errorf("checkShareExposure() = %q, want the skipped workspace left out", err)
	}

	if err := checkShareExposure(cfg, share(dir, false), "", nil); err == nil || !strings.Contains(err.Error(), `Dockerfile `+filepath.Join(self, "Dockerfile")+` of workspace "self"`) {
		t.Errorf("checkShareExposure() without skip = %v, want the inputs of workspace self", err)
	}
	if err := checkShareExposure(cfg, share(dir, true), "self", nil); err != nil {
		t.Errorf("checkShareExposure() with a read-only share = %v, want nil", err)
	}
	if err := checkShareExposure(cfg, share(self, false), "self", nil); err != nil {
		t.Errorf("checkShareExposure() with an unrelated share = %v, want nil", err)
	}

	// A workspace another configuration starts counts by its record.
	elsewhere := filepath.Join(self, "elsewhere.Dockerfile")
	live := []liveWorkspace{{name: "foreign", inputs: []buildInput{{kind: inputDockerfile, path: elsewhere}}}}
	if err := checkShareExposure(cfg, share(self, false), "self", live); err == nil || !strings.Contains(err.Error(), `Dockerfile `+elsewhere+` of workspace "foreign"`) {
		t.Errorf("checkShareExposure() with a starting workspace = %v, want its recorded inputs", err)
	}
}

func TestCheckVMAdmission(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	own := filepath.Join(dir, "own")
	other := filepath.Join(dir, "other")
	for _, d := range []string{own, other} {
		if err := os.MkdirAll(d, 0o750); err != nil {
			t.Fatal(err)
		}
	}
	cfg := &config.Config{Workspaces: map[string]config.Workspace{
		"app":   {Dockerfile: filepath.Join(own, "Dockerfile")},
		"other": {Dockerfile: filepath.Join(other, "Dockerfile")},
	}}
	ws := &workspace.Resolved{Name: "app", Dockerfile: filepath.Join(own, "Dockerfile")}
	err := checkVMAdmission(cfg, ws, []vm.Share{{HostPath: dir, GuestPath: dir}}, t.TempDir())
	if err == nil {
		t.Fatal("checkVMAdmission() = nil, want both workspaces' inputs")
	}
	assertContains(t, err.Error(), `Dockerfile `+filepath.Join(own, "Dockerfile")+` of workspace "app"`)
	assertContains(t, err.Error(), `Dockerfile `+filepath.Join(other, "Dockerfile")+` of workspace "other"`)
	assertContains(t, err.Error(), "mount them read-only")
	if err := checkVMAdmission(cfg, ws, []vm.Share{{HostPath: dir, GuestPath: dir, ReadOnly: true}}, t.TempDir()); err != nil {
		t.Errorf("checkVMAdmission() with a read-only share = %v, want nil", err)
	}

	// The guest data directory of the VM is a read-write export too.
	root := t.TempDir()
	inGuest := filepath.Join(vm.GuestDataDir(filepath.Join(root, "workspaces", "app")), "Dockerfile")
	err = checkVMAdmission(&config.Config{}, &workspace.Resolved{Name: "app", Dockerfile: inGuest}, nil, root)
	if err == nil {
		t.Fatal("checkVMAdmission() with a Dockerfile in the guest data directory = nil, want it refused")
	}
	assertContains(t, err.Error(), "lies in the read-write share "+vm.GuestDataDir(filepath.Join(root, "workspaces", "app")))

	// So is the writable root file system it boots from.
	inBoot := filepath.Join(vm.BootRootDir(filepath.Join(root, "workspaces", "app")), "Dockerfile")
	err = checkVMAdmission(&config.Config{}, &workspace.Resolved{Name: "app", Dockerfile: inBoot}, nil, root)
	if err == nil {
		t.Fatal("checkVMAdmission() with a Dockerfile in the boot root = nil, want it refused")
	}
	assertContains(t, err.Error(), "lies in the read-write share "+vm.BootRootDir(filepath.Join(root, "workspaces", "app")))

	// A VM left from an earlier start of the workspace keeps its old shares
	// until the new start stops it, after the build.
	root = t.TempDir()
	fakeWorkspaceVM(t, root, "app", os.Getpid(), vm.Share{HostPath: own, GuestPath: own})
	err = checkVMAdmission(&config.Config{}, ws, []vm.Share{{HostPath: own, GuestPath: own, ReadOnly: true}}, root)
	if err == nil {
		t.Fatal("checkVMAdmission() with an earlier VM of the workspace = nil, want its shares to count")
	}
	assertContains(t, err.Error(), "lies in the read-write share "+own)
}

func TestCheckExportPaths(t *testing.T) {
	t.Parallel()
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	work := filepath.Join(dir, "work")
	target := filepath.Join(dir, "target")
	for _, d := range []string{filepath.Join(work, "sub"), target} {
		if err := os.MkdirAll(d, 0o750); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Symlink(target, filepath.Join(work, "link")); err != nil {
		t.Fatal(err)
	}
	share := func(name, host string) workspaceShare {
		return workspaceShare{workspace: name, share: vm.Share{HostPath: host, GuestPath: "/g"}}
	}
	others := []workspaceShare{share("a", work)}
	tests := []struct {
		name    string
		exports []workspaceShare
		wantErr string
	}{
		{"nested share", []workspaceShare{share("b", filepath.Join(work, "sub"))}, ""},
		{"same share", []workspaceShare{share("b", work)}, ""},
		{"share through a link in another share", []workspaceShare{share("b", filepath.Join(work, "link"))}, `the read-write share ` + filepath.Join(work, "link") + ` of workspace "b" runs through the symbolic link "` + filepath.Join(work, "link") + `" inside the read-write share ` + work + ` of workspace "a"`},
		{"share through a detour in another share", []workspaceShare{share("b", work+"/sub/../../target")}, `runs through "` + filepath.Join(work, "sub") + `" inside the read-write share ` + work},
		{"read-only share through a link", []workspaceShare{{workspace: "b", share: vm.Share{HostPath: filepath.Join(work, "link"), ReadOnly: true}}}, ""},
		{"own share through a link", []workspaceShare{share("a", filepath.Join(work, "link"))}, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			err := checkExportPaths(tt.exports, others)
			if tt.wantErr == "" {
				if err != nil {
					t.Fatal(err)
				}
				return
			}
			if err == nil {
				t.Fatalf("checkExportPaths() = nil, want %q", tt.wantErr)
			}
			assertContains(t, err.Error(), tt.wantErr)
		})
	}

	// checkVMAdmission checks both directions against live workspaces.
	root := t.TempDir()
	fakeWorkspaceVM(t, root, "a", os.Getpid(), vm.Share{HostPath: work, GuestPath: "/w"})
	ws := &workspace.Resolved{Name: "b"}
	if err := checkVMAdmission(&config.Config{}, ws, []vm.Share{{HostPath: filepath.Join(work, "link"), GuestPath: "/l"}}, root); err == nil {
		t.Error("checkVMAdmission() with a share through a link of a running workspace = nil, want it refused")
	}
	root = t.TempDir()
	fakeWorkspaceVM(t, root, "a", os.Getpid(), vm.Share{HostPath: filepath.Join(work, "link"), GuestPath: "/l"})
	if err := checkVMAdmission(&config.Config{}, ws, []vm.Share{{HostPath: work, GuestPath: "/w"}}, root); err == nil {
		t.Error("checkVMAdmission() with a share holding a link a running workspace exports through = nil, want it refused")
	}
}

func TestPurgeLock(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	stateDir := filepath.Join(root, "workspaces", "app")
	fakeStartingWorkspace(t, root, "app", nil, nil)
	if _, err := lockPurge(stateDir, "app"); err == nil {
		t.Fatal("lockPurge() during a start = nil error, want the purge refused")
	} else {
		assertContains(t, err.Error(), "being started by another jailoc process")
	}

	stateDir = filepath.Join(root, "workspaces", "idle")
	release, err := lockPurge(stateDir, "idle")
	if err != nil {
		t.Fatal(err)
	}
	if err := checkVMGone(stateDir, "idle"); err != nil {
		t.Errorf("checkVMGone() without a VM = %v, want nil", err)
	}
	release()

	// A runner that outlived its supervisor still serves the shares.
	stateDir = filepath.Join(root, "workspaces", "orphan")
	if err := os.MkdirAll(filepath.Join(stateDir, "microvm"), 0o700); err != nil {
		t.Fatal(err)
	}
	runner := []byte(`{"version":1,"active":true,"pid":` + strconv.Itoa(os.Getpid()) + `}`)
	if err := os.WriteFile(filepath.Join(stateDir, "microvm", "go-microvm-state.json"), runner, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := checkVMGone(stateDir, "orphan"); err == nil {
		t.Fatal("checkVMGone() with a live runner = nil, want the purge refused")
	} else {
		assertContains(t, err.Error(), "still runs")
	}
}

func TestWarnBuildInputExposure(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	root := t.TempDir()
	cfg := &config.Config{Workspaces: map[string]config.Workspace{
		"app": {Dockerfile: filepath.Join(dir, "Dockerfile")},
	}}
	var out bytes.Buffer
	warnBuildInputExposure(&out, cfg, "app", []vm.Share{{HostPath: dir, GuestPath: dir}}, root)
	assertContains(t, out.String(), "WARNING")
	assertContains(t, out.String(), `of workspace "app" lies in the read-write share `+dir)
	assertContains(t, out.String(), "jailoc restart app")
	assertContains(t, out.String(), "check the exposed files")

	out.Reset()
	warnBuildInputExposure(&out, cfg, "app", []vm.Share{{HostPath: dir, GuestPath: dir, ReadOnly: true}}, root)
	warnBuildInputExposure(&out, nil, "app", []vm.Share{{HostPath: dir, GuestPath: dir}}, root)
	if out.Len() != 0 {
		t.Errorf("warnBuildInputExposure() = %q, want no warning", out.String())
	}
}
