package cmd

import (
	"bytes"
	"context"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/fatih/color"
	"github.com/seznam/jailoc/internal/compose"
	"github.com/seznam/jailoc/internal/config"
	"github.com/seznam/jailoc/internal/workspace"
	"github.com/spf13/cobra"
)

func TestValidateSecretSources(t *testing.T) {
	worldReadable := writeSecretFile(t, "world-readable", 0o644)
	ownerOnly := writeSecretFile(t, "owner-only", 0o600)
	dir := t.TempDir()

	tests := []struct {
		name    string
		env     map[string]string
		ws      *workspace.Resolved
		wantErr string
	}{
		{
			name: "no secrets",
			ws:   &workspace.Resolved{Name: "ws"},
		},
		{
			name: "nil secrets slice with env entries",
			ws:   &workspace.Resolved{Name: "ws", Env: []string{"FOO=bar"}},
		},
		{
			name: "env destination from environment source",
			env:  map[string]string{"JAILOC_TEST_TOKEN": "value"},
			ws: &workspace.Resolved{Name: "ws", Secrets: []workspace.ResolvedSecret{
				{Name: "GH_TOKEN", Kind: workspace.SecretKindEnv, FromEnv: "JAILOC_TEST_TOKEN"},
			}},
		},
		{
			name: "env destination from file source need not be world readable",
			ws: &workspace.Resolved{Name: "ws", Secrets: []workspace.ResolvedSecret{
				{Name: "FILE_KEY", Kind: workspace.SecretKindEnv, FromFile: ownerOnly},
			}},
		},
		{
			name: "file destination from environment source",
			env:  map[string]string{"JAILOC_TEST_TOKEN": "value"},
			ws: &workspace.Resolved{Name: "ws", Secrets: []workspace.ResolvedSecret{
				{Name: "envkey", Kind: workspace.SecretKindFile, FromEnv: "JAILOC_TEST_TOKEN"},
			}},
		},
		{
			name: "env source unset",
			ws: &workspace.Resolved{Name: "ws", Secrets: []workspace.ResolvedSecret{
				{Name: "GH_TOKEN", Kind: workspace.SecretKindEnv, FromEnv: "JAILOC_TEST_ABSENT"},
			}},
			wantErr: `secret "GH_TOKEN": host environment variable "JAILOC_TEST_ABSENT" is not set`,
		},
		{
			name: "env source set but empty",
			env:  map[string]string{"JAILOC_TEST_EMPTY": ""},
			ws: &workspace.Resolved{Name: "ws", Secrets: []workspace.ResolvedSecret{
				{Name: "GH_TOKEN", Kind: workspace.SecretKindEnv, FromEnv: "JAILOC_TEST_EMPTY"},
			}},
			wantErr: `secret "GH_TOKEN": host environment variable "JAILOC_TEST_EMPTY" is set but empty; Docker Compose omits empty secrets, so /run/secrets/GH_TOKEN would not exist`,
		},
		{
			name: "file destination from file source world readable",
			ws: &workspace.Resolved{Name: "ws", Secrets: []workspace.ResolvedSecret{
				{Name: "key", Kind: workspace.SecretKindFile, FromFile: worldReadable},
			}},
		},
		{
			name: "file source missing",
			ws: &workspace.Resolved{Name: "ws", Secrets: []workspace.ResolvedSecret{
				{Name: "key", Kind: workspace.SecretKindFile, FromFile: filepath.Join(dir, "absent")},
			}},
			wantErr: fmt.Sprintf("secret %q: file %q does not exist", "key", filepath.Join(dir, "absent")),
		},
		{
			name: "file source is a directory",
			ws: &workspace.Resolved{Name: "ws", Secrets: []workspace.ResolvedSecret{
				{Name: "key", Kind: workspace.SecretKindFile, FromFile: dir},
			}},
			wantErr: fmt.Sprintf("secret %q: file %q is not a regular file", "key", dir),
		},
		{
			name: "file source not world readable",
			ws: &workspace.Resolved{Name: "ws", Secrets: []workspace.ResolvedSecret{
				{Name: "key", Kind: workspace.SecretKindFile, FromFile: ownerOnly},
			}},
			wantErr: fmt.Sprintf("secret %q: file %q is not world-readable (mode 0600); the agent runs as UID 1000 and this is a conservative check — run 'chmod o+r %s' to make it readable inside the container", "key", ownerOnly, ownerOnly),
		},
		{
			name: "env secret name collides with env entry",
			env:  map[string]string{"JAILOC_TEST_TOKEN": "value"},
			ws: &workspace.Resolved{
				Name: "ws",
				Env:  []string{"FOO=bar", "GH_TOKEN=already-here"},
				Secrets: []workspace.ResolvedSecret{
					{Name: "GH_TOKEN", Kind: workspace.SecretKindEnv, FromEnv: "JAILOC_TEST_TOKEN"},
				},
			},
			wantErr: `env secret "GH_TOKEN" collides with an env entry`,
		},
		{
			name: "file-sourced env secret name collides with env entry",
			ws: &workspace.Resolved{
				Name: "ws",
				Env:  []string{"FILE_KEY=already-here"},
				Secrets: []workspace.ResolvedSecret{
					{Name: "FILE_KEY", Kind: workspace.SecretKindEnv, FromFile: worldReadable},
				},
			},
			wantErr: `env secret "FILE_KEY" collides with an env entry`,
		},
		{
			// workspace.Resolve keys secrets by name, so a repeated name in
			// Resolved.Secrets is unrepresentable and needs no runtime check here.
			// TestResolveSecretsWorkspaceOverrideAcrossKinds covers the dedup.
			name: "env secret not colliding with a differently named env entry",
			env:  map[string]string{"JAILOC_TEST_TOKEN": "value"},
			ws: &workspace.Resolved{Name: "ws", Env: []string{"OTHER=1"}, Secrets: []workspace.ResolvedSecret{
				{Name: "TOKEN", Kind: workspace.SecretKindEnv, FromEnv: "JAILOC_TEST_TOKEN"},
			}},
		},
		{
			name: "distinct env and file secrets pass",
			env:  map[string]string{"JAILOC_TEST_TOKEN": "value"},
			ws: &workspace.Resolved{
				Name: "ws",
				Env:  []string{"FOO=bar"},
				Secrets: []workspace.ResolvedSecret{
					{Name: "ALPHA", Kind: workspace.SecretKindEnv, FromEnv: "JAILOC_TEST_TOKEN"},
					{Name: "beta", Kind: workspace.SecretKindFile, FromFile: worldReadable},
				},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			for k, v := range tt.env {
				t.Setenv(k, v)
			}
			err := validateSecretSources(tt.ws)
			if tt.wantErr == "" {
				if err != nil {
					t.Fatalf("validateSecretSources() = %v, want nil", err)
				}
				return
			}
			if err == nil {
				t.Fatalf("validateSecretSources() = nil, want error %q", tt.wantErr)
			}
			if err.Error() != tt.wantErr {
				t.Fatalf("validateSecretSources() error =\n  %q\nwant\n  %q", err.Error(), tt.wantErr)
			}
		})
	}
}

func TestSecretSpecsAndEnvNames(t *testing.T) {
	t.Parallel()

	t.Run("nil secrets yields nil", func(t *testing.T) {
		t.Parallel()
		ws := &workspace.Resolved{Name: "ws"}
		if got := secretSpecs(ws); got != nil {
			t.Fatalf("secretSpecs() = %v, want nil", got)
		}
		if got := secretEnvNames(ws); got != nil {
			t.Fatalf("secretEnvNames() = %v, want nil", got)
		}
	})

	t.Run("preserves resolved order and maps fields", func(t *testing.T) {
		t.Parallel()
		ws := &workspace.Resolved{Name: "ws", Secrets: []workspace.ResolvedSecret{
			{Name: "ALPHA", Kind: workspace.SecretKindEnv, FromEnv: "ALPHA_VAR"},
			{Name: "BETA", Kind: workspace.SecretKindEnv, FromFile: "/tmp/beta"},
			{Name: "gamma", Kind: workspace.SecretKindFile, FromEnv: "GAMMA_VAR"},
			{Name: "delta", Kind: workspace.SecretKindFile, FromFile: "/tmp/delta"},
		}}

		specs := secretSpecs(ws)
		if len(specs) != 4 {
			t.Fatalf("secretSpecs() len = %d, want 4", len(specs))
		}
		if specs[0].Name != "ALPHA" || specs[0].Environment != "ALPHA_VAR" || specs[0].File != "" {
			t.Fatalf("secretSpecs()[0] = %+v", specs[0])
		}
		if specs[1].Name != "BETA" || specs[1].File != "/tmp/beta" || specs[1].Environment != "" {
			t.Fatalf("secretSpecs()[1] = %+v", specs[1])
		}
		if specs[2].Name != "gamma" || specs[2].Environment != "GAMMA_VAR" || specs[2].File != "" {
			t.Fatalf("secretSpecs()[2] = %+v", specs[2])
		}
		if specs[3].Name != "delta" || specs[3].File != "/tmp/delta" || specs[3].Environment != "" {
			t.Fatalf("secretSpecs()[3] = %+v", specs[3])
		}

		names := secretEnvNames(ws)
		want := []string{"ALPHA", "BETA"}
		if !reflect.DeepEqual(names, want) {
			t.Fatalf("secretEnvNames() = %+v, want %+v", names, want)
		}
	})

	t.Run("file secrets yield nil names", func(t *testing.T) {
		t.Parallel()
		ws := &workspace.Resolved{Name: "ws", Secrets: []workspace.ResolvedSecret{
			{Name: "beta", Kind: workspace.SecretKindFile, FromFile: "/tmp/beta"},
		}}
		if got := secretEnvNames(ws); got != nil {
			t.Fatalf("secretEnvNames() = %v, want nil", got)
		}
	})
}

func TestSecretSpecsRenderAllSourceDestinationCombinations(t *testing.T) {
	t.Parallel()

	ws := &workspace.Resolved{Name: "ws", Secrets: []workspace.ResolvedSecret{
		{Name: "FILE_KEY", Kind: workspace.SecretKindEnv, FromFile: "/tmp/x"},
		{Name: "GH_TOKEN", Kind: workspace.SecretKindEnv, FromEnv: "GITHUB_TOKEN"},
		{Name: "envkey", Kind: workspace.SecretKindFile, FromEnv: "HOST_A"},
		{Name: "key", Kind: workspace.SecretKindFile, FromFile: "/tmp/x"},
	}}

	rendered, err := compose.GenerateCompose(compose.ComposeParams{WorkspaceName: "ws", Secrets: secretSpecs(ws)})
	if err != nil {
		t.Fatalf("GenerateCompose() = %v", err)
	}
	yaml := string(rendered)
	assertContains(t, yaml, `environment: "GITHUB_TOKEN"`)
	assertContains(t, yaml, `environment: "HOST_A"`)
	if got := strings.Count(yaml, `file: "/tmp/x"`); got != 2 {
		t.Fatalf("rendered compose file source count = %d, want 2:\n%s", got, yaml)
	}

	wantNames := []string{"FILE_KEY", "GH_TOKEN"}
	if gotNames := secretEnvNames(ws); !reflect.DeepEqual(gotNames, wantNames) {
		t.Fatalf("secretEnvNames() = %+v, want %+v", gotNames, wantNames)
	}
}

// TestConfigShowsSecretReferences is not parallel: it uses t.Setenv to point
// config.Load at a scratch HOME and to export the secret value that must not
// leak into stdout.
func TestConfigShowsSecretReferences(t *testing.T) {
	home := t.TempDir()
	secretFile := writeSecretFile(t, "filevalue", 0o644)

	t.Setenv("HOME", home)
	t.Setenv("GITHUB_TOKEN", "supersecret")

	configDir := filepath.Join(home, ".config", "jailoc")
	if err := os.MkdirAll(configDir, 0o750); err != nil {
		t.Fatalf("create config dir: %v", err)
	}
	content := fmt.Sprintf(`[workspaces.alpha]
paths = ["/data/alpha"]

[workspaces.alpha.secrets.env.GH_TOKEN]
from_env = "GITHUB_TOKEN"

[workspaces.alpha.secrets.env.FILE_KEY]
from_file = %q

[workspaces.alpha.secrets.file.envkey]
from_env = "GITHUB_TOKEN"

[workspaces.alpha.secrets.file.key]
from_file = %q

[workspaces.bare]
paths = ["/data/bare"]
`, secretFile, secretFile)
	if err := os.WriteFile(filepath.Join(configDir, "config.toml"), []byte(content), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}

	out := captureStdout(t, func() {
		cmd := &cobra.Command{}
		cmd.SetContext(context.Background())
		if err := runConfig(cmd, nil); err != nil {
			t.Fatalf("runConfig() = %v", err)
		}
	})

	assertContains(t, out, "  Secrets:\n    - FILE_KEY (env from_file "+secretFile+")\n    - GH_TOKEN (env from_env GITHUB_TOKEN)\n    - envkey (file from_env GITHUB_TOKEN)\n    - key (file from_file "+secretFile+")\n")
	assertContains(t, out, "Workspace: bare")

	if strings.Contains(out, "supersecret") {
		t.Fatalf("jailoc config output leaked the secret value:\n%s", out)
	}
	if strings.Contains(out, "filevalue") {
		t.Fatalf("jailoc config output leaked the secret file contents:\n%s", out)
	}

	_, bareSection, ok := strings.Cut(out, "Workspace: bare")
	if !ok {
		t.Fatalf("workspace bare section missing from output:\n%s", out)
	}
	assertContains(t, bareSection, "  Secrets:\n    (none)\n")
}

func TestConfigSecretsOutputOrder(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("HOST_A", "secret-value-a")
	t.Setenv("HOST_Z", "secret-value-z")

	configDir := filepath.Join(home, ".config", "jailoc")
	if err := os.MkdirAll(configDir, 0o750); err != nil {
		t.Fatalf("create config dir: %v", err)
	}
	content := `[workspaces.alpha]
paths = ["/data/alpha"]

[workspaces.alpha.secrets.env.Z_TOKEN]
from_env = "HOST_Z"

[workspaces.alpha.secrets.env.A_TOKEN]
from_env = "HOST_A"

[workspaces.alpha.secrets.env.F_TOKEN]
from_file = "/a/envfile"

[workspaces.alpha.secrets.file.z-file]
from_file = "/z/file"

[workspaces.alpha.secrets.file.a-file]
from_file = "/a/file"

[workspaces.alpha.secrets.file.e-file]
from_env = "HOST_A"

[workspaces.empty]
paths = []
`
	if err := os.WriteFile(filepath.Join(configDir, "config.toml"), []byte(content), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}

	out := captureStdout(t, func() {
		cmd := &cobra.Command{}
		cmd.SetContext(context.Background())
		if err := runConfig(cmd, nil); err != nil {
			t.Fatalf("runConfig() = %v", err)
		}
	})

	want := "  Secrets:\n" +
		"    - A_TOKEN (env from_env HOST_A)\n" +
		"    - F_TOKEN (env from_file /a/envfile)\n" +
		"    - Z_TOKEN (env from_env HOST_Z)\n" +
		"    - a-file (file from_file /a/file)\n" +
		"    - e-file (file from_env HOST_A)\n" +
		"    - z-file (file from_file /z/file)\n"
	assertContains(t, out, want)
	_, emptySection, ok := strings.Cut(out, "Workspace: empty")
	if !ok {
		t.Fatalf("workspace empty section missing from output:\n%s", out)
	}
	assertContains(t, emptySection, "  Secrets:\n    (none)\n")
	for _, secretValue := range []string{"secret-value-a", "secret-value-z"} {
		if strings.Contains(out, secretValue) {
			t.Fatalf("jailoc config output leaked secret value %q:\n%s", secretValue, out)
		}
	}
}

// TestConfigShowsDefaultsSecrets is not parallel: it uses t.Setenv to point
// config.Load at a scratch HOME.
//
// A secret declared only under [defaults.secrets] applies to every workspace,
// so jailoc config must surface it. The per-workspace sections print the raw
// workspace scope and therefore never mention it.
func TestConfigShowsDefaultsSecrets(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("HOST_GLOBAL", "global-secret-value")

	configDir := filepath.Join(home, ".config", "jailoc")
	if err := os.MkdirAll(configDir, 0o750); err != nil {
		t.Fatalf("create config dir: %v", err)
	}
	content := `[defaults.secrets.env.GLOBAL_TOKEN]
from_env = "HOST_GLOBAL"

[defaults.secrets.file.global-cert]
from_file = "/etc/ssl/global.pem"

[workspaces.alpha]
paths = ["/data/alpha"]
`
	if err := os.WriteFile(filepath.Join(configDir, "config.toml"), []byte(content), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}

	out := captureStdout(t, func() {
		cmd := &cobra.Command{}
		cmd.SetContext(context.Background())
		if err := runConfig(cmd, nil); err != nil {
			t.Fatalf("runConfig() = %v", err)
		}
	})

	assertContains(t, out, "Defaults Secrets:\n"+
		"  - GLOBAL_TOKEN (env from_env HOST_GLOBAL)\n"+
		"  - global-cert (file from_file /etc/ssl/global.pem)\n")

	if strings.Contains(out, "global-secret-value") {
		t.Fatalf("jailoc config output leaked the default secret value:\n%s", out)
	}
}

// TestConfigDefaultsSecretsEmpty is not parallel: it uses t.Setenv to point
// config.Load at a scratch HOME.
func TestConfigDefaultsSecretsEmpty(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	configDir := filepath.Join(home, ".config", "jailoc")
	if err := os.MkdirAll(configDir, 0o750); err != nil {
		t.Fatalf("create config dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(configDir, "config.toml"), []byte("[workspaces.alpha]\npaths = [\"/data/alpha\"]\n"), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}

	out := captureStdout(t, func() {
		cmd := &cobra.Command{}
		cmd.SetContext(context.Background())
		if err := runConfig(cmd, nil); err != nil {
			t.Fatalf("runConfig() = %v", err)
		}
	})

	assertContains(t, out, "Defaults Secrets:\n  (none)\n")
}

// TestPrintSecretsOrderMatchesResolvedOrder locks jailoc config to the single
// ordering definition in workspace.FlattenSecrets: one sort by name across both
// destination sub-tables. Grouping by destination first would place ZZZ_TOKEN
// before aaa-cert, which is what this input distinguishes.
func TestPrintSecretsOrderMatchesResolvedOrder(t *testing.T) {
	t.Parallel()

	secrets := config.Secrets{
		Env:  map[string]config.Secret{"zzz_token": {FromEnv: "HOST_Z"}},
		File: map[string]config.Secret{"aaa-cert": {FromFile: "/a/cert"}},
	}

	var names []string
	for _, s := range workspace.FlattenSecrets(secrets) {
		names = append(names, s.Name)
	}
	want := []string{"aaa-cert", "zzz_token"}
	if !reflect.DeepEqual(names, want) {
		t.Fatalf("FlattenSecrets order = %v, want %v", names, want)
	}

	out := captureStdout(t, func() { printSecrets(secrets, "  ") })
	wantOut := "  - aaa-cert (file from_file /a/cert)\n  - zzz_token (env from_env HOST_Z)\n"
	if out != wantOut {
		t.Fatalf("printSecrets() = %q, want %q", out, wantOut)
	}
}

// TestComposeParamsParity fails when up.go and add.go stop assigning the same
// set of compose.ComposeParams fields. Image is excluded: add.go deliberately
// hardcodes jailoc-base:embedded while up.go resolves the image.
func TestComposeParamsParity(t *testing.T) {
	t.Parallel()

	upFields := composeParamsFields(t, "up.go")
	addFields := composeParamsFields(t, "add.go")

	delete(upFields, "Image")
	delete(addFields, "Image")

	if missing := setDifference(upFields, addFields); len(missing) > 0 {
		t.Errorf("add.go is missing ComposeParams fields set in up.go: %v", missing)
	}
	if extra := setDifference(addFields, upFields); len(extra) > 0 {
		t.Errorf("add.go sets ComposeParams fields absent from up.go: %v", extra)
	}
}

// composeParamsFields returns the field names assigned in every
// compose.ComposeParams composite literal in the given file of this package.
func composeParamsFields(t *testing.T, filename string) map[string]bool {
	t.Helper()

	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, filename, nil, 0)
	if err != nil {
		t.Fatalf("parse %s: %v", filename, err)
	}

	fields := make(map[string]bool)
	found := false
	ast.Inspect(file, func(n ast.Node) bool {
		lit, ok := n.(*ast.CompositeLit)
		if !ok {
			return true
		}
		sel, ok := lit.Type.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		pkg, ok := sel.X.(*ast.Ident)
		if !ok || pkg.Name != "compose" || sel.Sel.Name != "ComposeParams" {
			return true
		}
		found = true
		for _, elt := range lit.Elts {
			kv, ok := elt.(*ast.KeyValueExpr)
			if !ok {
				t.Fatalf("%s: compose.ComposeParams uses a positional literal; the parity check requires keyed fields", filename)
			}
			key, ok := kv.Key.(*ast.Ident)
			if !ok {
				t.Fatalf("%s: compose.ComposeParams has a non-identifier field key", filename)
			}
			fields[key.Name] = true
		}
		return true
	})

	if !found {
		t.Fatalf("%s: no compose.ComposeParams composite literal found", filename)
	}
	return fields
}

func setDifference(a, b map[string]bool) []string {
	var diff []string
	for k := range a {
		if !b[k] {
			diff = append(diff, k)
		}
	}
	sort.Strings(diff)
	return diff
}

func writeSecretFile(t *testing.T, content string, mode os.FileMode) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "secret")
	if err := os.WriteFile(path, []byte(content), mode); err != nil {
		t.Fatalf("write secret file: %v", err)
	}
	if err := os.Chmod(path, mode); err != nil {
		t.Fatalf("chmod secret file: %v", err)
	}
	return path
}

func captureStdout(t *testing.T, fn func()) string {
	t.Helper()

	origNoColor := color.NoColor
	color.NoColor = true
	origStdout := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("create pipe: %v", err)
	}
	os.Stdout = w
	t.Cleanup(func() {
		os.Stdout = origStdout
		color.NoColor = origNoColor
	})

	done := make(chan string, 1)
	go func() {
		var buf bytes.Buffer
		_, _ = io.Copy(&buf, r)
		done <- buf.String()
	}()

	fn()

	if err := w.Close(); err != nil {
		t.Fatalf("close pipe writer: %v", err)
	}
	out := <-done
	if err := r.Close(); err != nil {
		t.Fatalf("close pipe reader: %v", err)
	}
	return out
}
