package cmd

import (
	"bytes"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/seznam/jailoc/internal/agent"
	"github.com/seznam/jailoc/internal/embed"
	"github.com/seznam/jailoc/internal/workspace"
)

func TestWorkspaceCacheDir(t *testing.T) {
	tests := []struct {
		name      string
		workspace string
		homeEnv   string
		wantPath  string
	}{
		{
			name:      "normal workspace with HOME set",
			workspace: "default",
			homeEnv:   "/tmp/fakehome",
			wantPath:  filepath.Join("/tmp/fakehome", ".cache", "jailoc", "default"),
		},
		{
			name:      "different workspace name",
			workspace: "api",
			homeEnv:   "/tmp/fakehome",
			wantPath:  filepath.Join("/tmp/fakehome", ".cache", "jailoc", "api"),
		},
		{
			name:      "workspace with special chars",
			workspace: "my-app-prod",
			homeEnv:   "/tmp/fakehome",
			wantPath:  filepath.Join("/tmp/fakehome", ".cache", "jailoc", "my-app-prod"),
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("HOME", tc.homeEnv)

			got := workspaceCacheDir(tc.workspace)

			if got != tc.wantPath {
				t.Fatalf("workspaceCacheDir(%q) = %q, want %q", tc.workspace, got, tc.wantPath)
			}
		})
	}
}

func TestResolveImageStrategy(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name          string
		wsImage       string
		defaultsImage string
		wsDockerfile  string
		wantStrategy  imageStrategy
		wantImage     string
	}{
		{
			name:          "workspace image only uses direct image",
			wsImage:       "foo:1",
			defaultsImage: "",
			wsDockerfile:  "",
			wantStrategy:  strategyDirectImage,
			wantImage:     "foo:1",
		},
		{
			name:          "workspace image wins over defaults and dockerfile",
			wsImage:       "foo:1",
			defaultsImage: "base:2",
			wsDockerfile:  "/df",
			wantStrategy:  strategyDirectImage,
			wantImage:     "foo:1",
		},
		{
			name:          "defaults image without dockerfile uses direct defaults",
			wsImage:       "",
			defaultsImage: "base:2",
			wsDockerfile:  "",
			wantStrategy:  strategyDefaultsDirect,
			wantImage:     "base:2",
		},
		{
			name:          "defaults image with dockerfile uses defaults overlay",
			wsImage:       "",
			defaultsImage: "base:2",
			wsDockerfile:  "/path/Dockerfile",
			wantStrategy:  strategyDefaultsOverlay,
			wantImage:     "base:2",
		},
		{
			name:          "no images and no dockerfile uses cascade",
			wsImage:       "",
			defaultsImage: "",
			wsDockerfile:  "",
			wantStrategy:  strategyCascade,
			wantImage:     "",
		},
		{
			name:          "dockerfile without images still uses cascade",
			wsImage:       "",
			defaultsImage: "",
			wsDockerfile:  "/path",
			wantStrategy:  strategyCascade,
			wantImage:     "",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			gotStrategy, gotImage := resolveImageStrategy(tc.wsImage, tc.defaultsImage, tc.wsDockerfile)

			if gotStrategy != tc.wantStrategy {
				t.Fatalf("resolveImageStrategy(%q, %q, %q) strategy = %v, want %v", tc.wsImage, tc.defaultsImage, tc.wsDockerfile, gotStrategy, tc.wantStrategy)
			}

			if gotImage != tc.wantImage {
				t.Fatalf("resolveImageStrategy(%q, %q, %q) image = %q, want %q", tc.wsImage, tc.defaultsImage, tc.wsDockerfile, gotImage, tc.wantImage)
			}
		})
	}
}

func TestWriteTUIConfig(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()

	if err := writeTUIConfig(tmpDir); err != nil {
		t.Fatalf("writeTUIConfig failed: %v", err)
	}

	readPluginSpec := func(path string) string {
		t.Helper()
		data, err := os.ReadFile(path) //nolint:gosec // test path
		if err != nil {
			t.Fatalf("os.ReadFile(%q) failed: %v", path, err)
		}
		var config map[string][]string
		if err := json.Unmarshal(data, &config); err != nil {
			t.Fatalf("json.Unmarshal(%q) failed: %v", path, err)
		}
		plugins, ok := config["plugin"]
		if !ok {
			t.Fatalf("%s missing 'plugin' key", path)
		}
		if len(plugins) != 1 {
			t.Fatalf("%s: expected 1 plugin, got %d", path, len(plugins))
		}
		return plugins[0]
	}

	wantGuest := "file:///etc/jailoc-tui-plugin"
	if got := readPluginSpec(filepath.Join(tmpDir, "tui-container.json")); got != wantGuest {
		t.Fatalf("guest tui.json plugin = %q, want %q", got, wantGuest)
	}

	pkgJSON, err := os.ReadFile(filepath.Join(tmpDir, "tui-plugin", "package.json")) //nolint:gosec // test path
	if err != nil {
		t.Fatalf("reading tui-plugin/package.json: %v", err)
	}
	if !bytes.Equal(pkgJSON, embed.TUIPluginJSON()) {
		t.Fatalf("package.json content mismatch")
	}
	var pkg map[string]any
	if err := json.Unmarshal(pkgJSON, &pkg); err != nil {
		t.Fatalf("json.Unmarshal(package.json) failed: %v", err)
	}
	if got, ok := pkg["type"].(string); !ok || got != "module" {
		t.Fatalf("package.json type = %v, want %q", pkg["type"], "module")
	}

	tuiJS, err := os.ReadFile(filepath.Join(tmpDir, "tui-plugin", "tui.js")) //nolint:gosec // test path
	if err != nil {
		t.Fatalf("reading tui-plugin/tui.js: %v", err)
	}
	if !bytes.Equal(tuiJS, embed.TUIPluginOpenCode1JS()) {
		t.Fatalf("tui.js content mismatch")
	}
}

func TestEnsureConfigGitignore(t *testing.T) {
	t.Parallel()

	t.Run("creates directory and file when missing", func(t *testing.T) {
		t.Parallel()
		dir := filepath.Join(t.TempDir(), "opencode")
		if err := ensureConfigGitignore(dir, "node_modules\n"); err != nil {
			t.Fatalf("ensureConfigGitignore() = %v", err)
		}
		data, err := os.ReadFile(filepath.Join(dir, ".gitignore")) //nolint:gosec // test path
		if err != nil {
			t.Fatalf("ReadFile: %v", err)
		}
		if string(data) != "node_modules\n" {
			t.Fatalf("content = %q, want %q", data, "node_modules\n")
		}
	})

	t.Run("no-op when file already exists", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		existing := "existing content\n"
		if err := os.WriteFile(filepath.Join(dir, ".gitignore"), []byte(existing), 0o644); err != nil { //nolint:gosec // test
			t.Fatalf("WriteFile: %v", err)
		}
		if err := ensureConfigGitignore(dir, "node_modules\n"); err != nil {
			t.Fatalf("ensureConfigGitignore() = %v", err)
		}
		data, err := os.ReadFile(filepath.Join(dir, ".gitignore")) //nolint:gosec // test path
		if err != nil {
			t.Fatalf("ReadFile: %v", err)
		}
		if string(data) != existing {
			t.Fatalf("content changed: got %q, want %q", data, existing)
		}
	})
}

func TestCLIConfigContent(t *testing.T) {
	oc2, _ := agent.Lookup("opencode2")
	tests := []struct {
		name      string
		files     map[string]string
		mount     bool
		imported  bool
		snapshots []string
		want      string
		wantErr   string
	}{
		{name: "config dir not mounted", want: `{"plugins":["file:///etc/jailoc-tui-plugin"]}`},
		{name: "no plugin config", mount: true, want: `{"plugins":["file:///etc/jailoc-tui-plugin"]}`},
		{
			name:  "cli.json plugins kept",
			files: map[string]string{"cli.json": `{"theme":"x","plugins":["npm-plugin",["opts-plugin",{"a":1}],"./local","/abs"]}`},
			mount: true,
			want:  `{"plugins":["npm-plugin",["opts-plugin",{"a":1}],"/home/agent/.config/opencode/local","/abs","file:///etc/jailoc-tui-plugin"]}`,
		},
		{
			name:     "default snapshot plugins kept",
			files:    map[string]string{"cli.json": `{"plugins":["npm-plugin","./local"]}`},
			imported: true,
			want:     `{"plugins":["npm-plugin","/home/agent/.config/opencode/local","file:///etc/jailoc-tui-plugin"]}`,
		},
		{
			name:      "default snapshot removed",
			files:     map[string]string{"cli.json": `{"plugins":["npm-plugin"]}`},
			imported:  true,
			snapshots: []string{":/home/agent/.config/opencode:ro"},
			want:      `{"plugins":["file:///etc/jailoc-tui-plugin"]}`,
		},
		{
			name:      "snapshot override wins over default snapshot",
			files:     map[string]string{"cli.json": `{"plugins":["default-plugin"]}`, "override/cli.json": `{"plugins":["override-plugin"]}`},
			imported:  true,
			snapshots: []string{"$DIR/override:/home/agent/.config/opencode:ro"},
			want:      `{"plugins":["override-plugin","file:///etc/jailoc-tui-plugin"]}`,
		},
		{
			name:      "file snapshot override wins over default snapshot",
			files:     map[string]string{"cli.json": `{"plugins":["default-plugin"]}`, "override-cli.json": `{"plugins":["file-override-plugin"]}`},
			imported:  true,
			snapshots: []string{"$DIR/override-cli.json:/home/agent/.config/opencode/cli.json:ro"},
			want:      `{"plugins":["file-override-plugin","file:///etc/jailoc-tui-plugin"]}`,
		},
		{
			name:  "cli.json wins over tui.json",
			files: map[string]string{"cli.json": `{"plugins":["v2"]}`, "tui.json": `{"plugin":["v1"]}`},
			mount: true,
			want:  `{"plugins":["v2","file:///etc/jailoc-tui-plugin"]}`,
		},
		{
			name:  "tui.json plugins migrated",
			files: map[string]string{"tui.json": `{"plugin":["v1"]}`},
			mount: true,
			want:  `{"plugins":["v1","file:///etc/jailoc-tui-plugin"]}`,
		},
		{
			name:  "jailoc plugin not duplicated",
			files: map[string]string{"cli.json": `{"plugins":["file:///etc/jailoc-tui-plugin"]}`},
			mount: true,
			want:  `{"plugins":["file:///etc/jailoc-tui-plugin"]}`,
		},
		{name: "invalid cli.json", files: map[string]string{"cli.json": `{"plugins":`}, mount: true, wantErr: "parse "},
		{name: "invalid plugins", files: map[string]string{"cli.json": `{"plugins":"x"}`}, mount: true, wantErr: "plugins"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			home := t.TempDir()
			t.Setenv("HOME", home)
			dir := filepath.Join(home, ".config", "opencode")
			if err := os.MkdirAll(dir, 0o700); err != nil {
				t.Fatal(err)
			}
			for name, content := range tt.files {
				p := filepath.Join(dir, filepath.FromSlash(name))
				if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			ws := &workspace.Resolved{Agent: oc2, ImportAgentConfig: tt.imported}
			if tt.mount {
				ws.Mounts = []string{dir + ":/home/agent/.config/opencode:ro"}
			}
			for _, s := range tt.snapshots {
				ws.Snapshots = append(ws.Snapshots, filepath.FromSlash(os.Expand(s, func(k string) string {
					if k == "DIR" {
						return dir
					}
					return ""
				})))
			}
			got, err := cliConfigContent(ws)
			if tt.wantErr != "" {
				if err == nil {
					t.Fatalf("cliConfigContent() = %q, want error containing %q", got, tt.wantErr)
				}
				assertContains(t, err.Error(), tt.wantErr)
				return
			}
			if err != nil {
				t.Fatalf("cliConfigContent() error = %v", err)
			}
			if got != tt.want {
				t.Fatalf("cliConfigContent() = %s, want %s", got, tt.want)
			}
		})
	}
}

func TestWriteAgentFilesOpenCode2(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	oc2, _ := agent.Lookup("opencode2")
	if err := writeAgentFiles(&workspace.Resolved{Agent: oc2}, dir); err != nil {
		t.Fatalf("writeAgentFiles() error = %v", err)
	}
	js, err := os.ReadFile(filepath.Join(dir, "tui-plugin", "tui.js")) //nolint:gosec // test path
	if err != nil {
		t.Fatalf("reading tui-plugin/tui.js: %v", err)
	}
	if !bytes.Equal(js, embed.TUIPluginOpenCode2JS()) {
		t.Fatal("tui.js is not the OpenCode 2 plugin")
	}
	if _, err := os.Stat(filepath.Join(dir, "tui-container.json")); !os.IsNotExist(err) {
		t.Fatalf("tui-container.json written for OpenCode 2: %v", err)
	}
}

func TestWriteAgentFilesIndicators(t *testing.T) {
	t.Parallel()

	tests := []struct {
		agent string
		files map[string][]byte
	}{
		{"omo", map[string][]byte{"omo.js": embed.OmOExtensionJS()}},
		{"copilot", nil},
		{"agy", nil},
		{"claude", map[string][]byte{
			"statusline.mjs":       embed.StatusLineJS(),
			"claude-settings.json": []byte(`{"statusLine":{"command":"node /etc/jailoc-tui-plugin/statusline.mjs claude","type":"command"}}`),
		}},
	}
	for _, tt := range tests {
		t.Run(tt.agent, func(t *testing.T) {
			t.Parallel()
			dir := t.TempDir()
			h, _ := agent.Lookup(tt.agent)
			if err := writeAgentFiles(&workspace.Resolved{Agent: h}, dir); err != nil {
				t.Fatalf("writeAgentFiles() error = %v", err)
			}
			entries, err := os.ReadDir(filepath.Join(dir, "tui-plugin"))
			if tt.files == nil && errors.Is(err, fs.ErrNotExist) {
				return
			}
			if err != nil || len(entries) != len(tt.files) {
				t.Fatalf("tui-plugin entries = %v, %v", entries, err)
			}
			for name, want := range tt.files {
				if got, err := os.ReadFile(filepath.Join(dir, "tui-plugin", name)); err != nil || !bytes.Equal(got, want) { //nolint:gosec // test path
					t.Fatalf("%s = %s, %v; want %s", name, got, err, want)
				}
			}
		})
	}
}

func TestWorkspaceStatusLineCommand(t *testing.T) {
	t.Parallel()

	tests := []struct {
		env  []string
		want string
	}{
		{[]string{"JAILOC_WORKSPACE=api"}, "\U0001F512 jailoc / api\n"},
		{[]string{}, "\U0001F512 jailoc / unknown\n"},
	}
	for _, tt := range tests {
		t.Run(tt.want, func(t *testing.T) {
			t.Parallel()
			cmd := exec.Command("/bin/sh", "-c", workspaceStatusLineCommand)
			cmd.Env = tt.env
			out, err := cmd.Output()
			if err != nil || string(out) != tt.want {
				t.Fatalf("status line = %q, %v; want %q", out, err, tt.want)
			}
		})
	}
}
