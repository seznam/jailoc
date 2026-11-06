package agent

import (
	"os"
	"path/filepath"
	"slices"
	"syscall"
	"testing"
)

func TestOpenCode1Installs(t *testing.T) {
	t.Parallel()

	// installed puts pkg into the OpenCode package cache of home.
	installed := func(t *testing.T, home, pkg, name string) {
		t.Helper()
		if err := os.MkdirAll(filepath.Join(home, ".cache", "opencode", "packages", pkg, "node_modules", name), 0o750); err != nil {
			t.Fatal(err)
		}
	}
	write := func(t *testing.T, path, content string) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	tests := []struct {
		name  string
		setup func(t *testing.T, home, project string) []string
		want  []string
	}{
		{
			name:  "no configuration",
			setup: func(*testing.T, string, string) []string { return nil },
		},
		{
			name: "global plugin not installed",
			setup: func(t *testing.T, home, _ string) []string {
				write(t, filepath.Join(home, ".config", "opencode", "opencode.json"), `{"plugin": ["oh-my-openagent@5.1.48"]}`)
				return nil
			},
			want: []string{"oh-my-openagent@5.1.48"},
		},
		{
			name: "global plugin installed",
			setup: func(t *testing.T, home, _ string) []string {
				write(t, filepath.Join(home, ".config", "opencode", "opencode.json"), `{"plugin": ["oh-my-openagent@5.1.48"]}`)
				installed(t, home, "oh-my-openagent@5.1.48", "oh-my-openagent")
				return nil
			},
		},
		{
			name: "other version installed",
			setup: func(t *testing.T, home, _ string) []string {
				write(t, filepath.Join(home, ".config", "opencode", "opencode.json"), `{"plugin": ["oh-my-openagent@5.1.49"]}`)
				installed(t, home, "oh-my-openagent@5.1.48", "oh-my-openagent")
				return nil
			},
			want: []string{"oh-my-openagent@5.1.49"},
		},
		{
			name: "bare and scoped names",
			setup: func(t *testing.T, home, _ string) []string {
				write(t, filepath.Join(home, ".config", "opencode", "opencode.jsonc"), `{
					// plugins
					"plugin": [
						"plain", /* bare: latest */
						["@scope/tool@^1.2", {"url": "http://x//y"}],
						"@scope/bare",
					],
				}`)
				installed(t, home, "@scope/tool@^1.2", "@scope/tool")
				return nil
			},
			want: []string{"plain@latest", "@scope/bare@latest"},
		},
		{
			name: "paths urls git and aliases ignored",
			setup: func(t *testing.T, home, _ string) []string {
				write(t, filepath.Join(home, ".config", "opencode", "opencode.json"), `{"plugin": [
					"./local.ts", "/abs/plugin.js", "file:///p.js", "~/p.js",
					"https://example.com/p.tgz", "git+https://example.com/p.git", "github:o/r",
					"o/r", "alias@npm:real@1", "@scope", "x@", "bad name", "esc\u001b[2J"
				]}`)
				return nil
			},
		},
		{
			name: "later source wins by name",
			setup: func(t *testing.T, home, project string) []string {
				write(t, filepath.Join(home, ".config", "opencode", "config.json"), `{"plugin": ["a@1", "b@1"]}`)
				write(t, filepath.Join(project, "opencode.json"), `{"plugin": ["a@2"]}`)
				return nil
			},
			want: []string{"b@1", "a@2"},
		},
		{
			name: "project files up to the git root",
			setup: func(t *testing.T, home, project string) []string {
				write(t, filepath.Join(filepath.Dir(project), "opencode.json"), `{"plugin": ["outside"]}`)
				write(t, filepath.Join(project, ".git", "HEAD"), "ref: refs/heads/main\n")
				write(t, filepath.Join(project, "opencode.jsonc"), `{"plugin": ["root"]}`)
				write(t, filepath.Join(project, "sub", ".opencode", "opencode.json"), `{"plugin": ["dotdir"]}`)
				return nil
			},
			want: []string{"root@latest", "dotdir@latest"},
		},
		{
			name: "project configuration disabled",
			setup: func(t *testing.T, home, project string) []string {
				write(t, filepath.Join(project, "opencode.json"), `{"plugin": ["root"]}`)
				write(t, filepath.Join(project, "sub", ".opencode", "opencode.json"), `{"plugin": ["dotdir"]}`)
				return []string{"OPENCODE_DISABLE_PROJECT_CONFIG=true"}
			},
		},
		{
			name: "home opencode directory",
			setup: func(t *testing.T, home, _ string) []string {
				write(t, filepath.Join(home, ".opencode", "opencode.json"), `{"plugin": ["home"]}`)
				return nil
			},
			want: []string{"home@latest"},
		},
		{
			name: "environment sources",
			setup: func(t *testing.T, home, _ string) []string {
				write(t, filepath.Join(home, "custom.json"), `{"plugin": ["custom"]}`)
				write(t, filepath.Join(home, "cfgdir", "opencode.json"), `{"plugin": ["cfgdir"]}`)
				return []string{
					"OPENCODE_CONFIG=" + filepath.Join(home, "custom.json"),
					"OPENCODE_CONFIG_DIR=" + filepath.Join(home, "cfgdir"),
					`OPENCODE_CONFIG_CONTENT={"plugin": ["content@3"]}`,
				}
			},
			want: []string{"custom@latest", "cfgdir@latest", "content@3"},
		},
		{
			name: "relative environment paths from the working directory",
			setup: func(t *testing.T, _, project string) []string {
				write(t, filepath.Join(project, "sub", "custom.json"), `{"plugin": ["custom"]}`)
				write(t, filepath.Join(project, "cfgdir", "opencode.json"), `{"plugin": ["cfgdir"]}`)
				return []string{"OPENCODE_CONFIG=custom.json", "OPENCODE_CONFIG_DIR=../cfgdir"}
			},
			want: []string{"custom@latest", "cfgdir@latest"},
		},
		{
			name: "xdg directories",
			setup: func(t *testing.T, home, _ string) []string {
				write(t, filepath.Join(home, "xc", "opencode", "opencode.json"), `{"plugin": ["p@1", "q@1"]}`)
				if err := os.MkdirAll(filepath.Join(home, "xd", "opencode", "packages", "p@1", "node_modules", "p"), 0o750); err != nil {
					t.Fatal(err)
				}
				return []string{"XDG_CONFIG_HOME=" + filepath.Join(home, "xc"), "XDG_CACHE_HOME=" + filepath.Join(home, "xd")}
			},
			want: []string{"q@1"},
		},
		{
			name: "later global file replaces the plugin list",
			setup: func(t *testing.T, home, _ string) []string {
				write(t, filepath.Join(home, ".config", "opencode", "config.json"), `{"plugin": ["old@1"]}`)
				write(t, filepath.Join(home, ".config", "opencode", "opencode.json"), `{"plugin": []}`)
				write(t, filepath.Join(home, ".config", "opencode", "opencode.jsonc"), `{"model": "x/y"}`)
				write(t, filepath.Join(home, ".opencode", "opencode.json"), `{"plugin": ["home@1"]}`)
				return nil
			},
			want: []string{"home@1"},
		},
		{
			name: "special files skipped",
			setup: func(t *testing.T, home, project string) []string {
				write(t, filepath.Join(project, "opencode.json"), `{"plugin": ["root@1"]}`)
				global := filepath.Join(home, ".config", "opencode")
				if err := os.MkdirAll(global, 0o750); err != nil {
					t.Fatal(err)
				}
				// Reading a FIFO without a writer would block forever.
				if err := syscall.Mkfifo(filepath.Join(global, "opencode.json"), 0o600); err != nil {
					t.Fatal(err)
				}
				if err := os.MkdirAll(filepath.Join(global, "opencode.jsonc"), 0o750); err != nil {
					t.Fatal(err)
				}
				return nil
			},
			want: []string{"root@1"},
		},
		{
			name: "invalid files skipped",
			setup: func(t *testing.T, home, project string) []string {
				write(t, filepath.Join(home, ".config", "opencode", "opencode.json"), `{"plugin": ["a"`)
				write(t, filepath.Join(project, "opencode.json"), `{"plugin": "b"}`)
				write(t, filepath.Join(home, ".opencode", "opencode.json"), `{"plugin": ["c", 1, [], [2], ["d"]]}`)
				return nil
			},
			want: []string{"c@latest", "d@latest"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			root := t.TempDir()
			home := filepath.Join(root, "home")
			project := filepath.Join(root, "work", "project")
			if err := os.MkdirAll(filepath.Join(project, "sub"), 0o750); err != nil {
				t.Fatal(err)
			}
			env := append([]string{"HOME=" + home, "PATH=/usr/bin"}, tt.setup(t, home, project)...)
			got := openCode1Installs(env, filepath.Join(project, "sub"))
			if !slices.Equal(got, tt.want) {
				t.Errorf("openCode1Installs() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestInstallsOnlyOpenCode1(t *testing.T) {
	t.Parallel()

	for _, h := range All() {
		if got := h.Installs != nil; got != (h.Name == "opencode1") {
			t.Errorf("%s: Installs set = %v", h.Name, got)
		}
	}
}
