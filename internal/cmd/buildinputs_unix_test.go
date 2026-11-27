//go:build darwin || linux

package cmd

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
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
			ws:   workspace.Resolved{Dockerfile: wsDockerfile, BuildContext: "/trusted/context/"},
			want: []buildInput{
				{kind: inputBaseDockerfile, path: baseDockerfile},
				{kind: inputDockerfile, path: wsDockerfile},
				{kind: inputContext, path: context + "/", dir: true},
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
				assertErr(t, checkBuildInputs(tt.inputs, tt.exports))
			})
			t.Run("from the configuration", func(t *testing.T) {
				cfg, ws := buildInputsConfig(tt.inputs)
				inputs, err := effectiveBuildInputs(cfg, ws)
				if err != nil {
					t.Fatal(err)
				}
				assertErr(t, checkBuildInputs(inputs, tt.exports))
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

func TestResolveBuildInputPath(t *testing.T) {
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
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if cwd, err = filepath.EvalSymlinks(cwd); err != nil {
		t.Fatal(err)
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
		{name: "relative path starts at the physical working directory", path: "buildinputs_unix.go", want: cwd + "/buildinputs_unix.go", exists: true},
		{name: "relative parent", path: "../cmd/./buildinputs_unix.go", want: cwd + "/buildinputs_unix.go", exists: true},
		{name: "root", path: "/", want: "/", exists: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, links, _, err := resolveBuildInputPath(tt.path)
			switch {
			case tt.wantErr != nil:
				if !errors.Is(err, tt.wantErr) {
					t.Fatalf("resolveBuildInputPath(%q) error = %v, want %v", tt.path, err, tt.wantErr)
				}
				return
			case tt.anyErr:
				if err == nil {
					t.Fatalf("resolveBuildInputPath(%q) = %q, nil, want an error", tt.path, got)
				}
				return
			case err != nil:
				t.Fatal(err)
			}
			if got != tt.want {
				t.Errorf("resolveBuildInputPath(%q) = %q, want %q", tt.path, got, tt.want)
			}
			if !slices.Equal(links, tt.wantLinks) {
				t.Errorf("links = %q, want %q", links, tt.wantLinks)
			}
			if !tt.exists {
				return
			}
			want, err := filepath.EvalSymlinks(tt.path)
			if err == nil && !filepath.IsAbs(want) {
				want = filepath.Join(cwd, want)
			}
			if err != nil || want != got {
				t.Errorf("EvalSymlinks(%q) = %q, %v; want %q", tt.path, want, err, got)
			}
		})
	}
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

			err = checkBuildInputs(inputs, exports)
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
