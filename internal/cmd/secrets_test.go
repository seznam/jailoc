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
	"sort"
	"strings"
	"testing"

	"github.com/fatih/color"
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
			name: "env source set and non-empty",
			env:  map[string]string{"JAILOC_TEST_TOKEN": "value"},
			ws: &workspace.Resolved{Name: "ws", Secrets: []workspace.ResolvedSecret{
				{Name: "gh", Env: "JAILOC_TEST_TOKEN"},
			}},
		},
		{
			name: "env source unset",
			ws: &workspace.Resolved{Name: "ws", Secrets: []workspace.ResolvedSecret{
				{Name: "gh", Env: "JAILOC_TEST_ABSENT"},
			}},
			wantErr: `secret "gh": host environment variable "JAILOC_TEST_ABSENT" is not set`,
		},
		{
			name: "env source set but empty",
			env:  map[string]string{"JAILOC_TEST_EMPTY": ""},
			ws: &workspace.Resolved{Name: "ws", Secrets: []workspace.ResolvedSecret{
				{Name: "gh", Env: "JAILOC_TEST_EMPTY"},
			}},
			wantErr: `secret "gh": host environment variable "JAILOC_TEST_EMPTY" is set but empty; Docker Compose omits empty secrets, so /run/secrets/gh would not exist`,
		},
		{
			name: "file source world readable",
			ws: &workspace.Resolved{Name: "ws", Secrets: []workspace.ResolvedSecret{
				{Name: "key", File: worldReadable},
			}},
		},
		{
			name: "file source missing",
			ws: &workspace.Resolved{Name: "ws", Secrets: []workspace.ResolvedSecret{
				{Name: "key", File: filepath.Join(dir, "absent")},
			}},
			wantErr: fmt.Sprintf("secret %q: file %q does not exist", "key", filepath.Join(dir, "absent")),
		},
		{
			name: "file source is a directory",
			ws: &workspace.Resolved{Name: "ws", Secrets: []workspace.ResolvedSecret{
				{Name: "key", File: dir},
			}},
			wantErr: fmt.Sprintf("secret %q: file %q is not a regular file", "key", dir),
		},
		{
			name: "file source not world readable without expose_env",
			ws: &workspace.Resolved{Name: "ws", Secrets: []workspace.ResolvedSecret{
				{Name: "key", File: ownerOnly},
			}},
			wantErr: fmt.Sprintf("secret %q: file %q is not world-readable (mode 0600); the agent runs as UID 1000 and this is a conservative check — run 'chmod o+r %s' or set expose_env", "key", ownerOnly, ownerOnly),
		},
		{
			name: "file source not world readable with expose_env is allowed",
			ws: &workspace.Resolved{Name: "ws", Secrets: []workspace.ResolvedSecret{
				{Name: "key", File: ownerOnly, ExposeEnv: "KEY"},
			}},
		},
		{
			name: "expose_env collides with env entry",
			env:  map[string]string{"JAILOC_TEST_TOKEN": "value"},
			ws: &workspace.Resolved{
				Name: "ws",
				Env:  []string{"FOO=bar", "GH_TOKEN=already-here"},
				Secrets: []workspace.ResolvedSecret{
					{Name: "gh", Env: "JAILOC_TEST_TOKEN", ExposeEnv: "GH_TOKEN"},
				},
			},
			wantErr: `secret "gh": expose_env "GH_TOKEN" collides with an env entry`,
		},
		{
			name: "two secrets share expose_env",
			env:  map[string]string{"JAILOC_TEST_TOKEN": "value"},
			ws: &workspace.Resolved{Name: "ws", Secrets: []workspace.ResolvedSecret{
				{Name: "alpha", Env: "JAILOC_TEST_TOKEN", ExposeEnv: "GH_TOKEN"},
				{Name: "beta", Env: "JAILOC_TEST_TOKEN", ExposeEnv: "GH_TOKEN"},
			}},
			wantErr: `secrets "alpha" and "beta" both expose_env "GH_TOKEN"`,
		},
		{
			name: "distinct expose_env values pass",
			env:  map[string]string{"JAILOC_TEST_TOKEN": "value"},
			ws: &workspace.Resolved{
				Name: "ws",
				Env:  []string{"FOO=bar"},
				Secrets: []workspace.ResolvedSecret{
					{Name: "alpha", Env: "JAILOC_TEST_TOKEN", ExposeEnv: "ALPHA"},
					{Name: "beta", File: worldReadable, ExposeEnv: "BETA"},
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

func TestSecretSpecsAndEnvPairs(t *testing.T) {
	t.Parallel()

	t.Run("nil secrets yields nil", func(t *testing.T) {
		t.Parallel()
		ws := &workspace.Resolved{Name: "ws"}
		if got := secretSpecs(ws); got != nil {
			t.Fatalf("secretSpecs() = %v, want nil", got)
		}
		if got := secretEnvPairs(ws); got != nil {
			t.Fatalf("secretEnvPairs() = %v, want nil", got)
		}
	})

	t.Run("preserves resolved order and maps fields", func(t *testing.T) {
		t.Parallel()
		ws := &workspace.Resolved{Name: "ws", Secrets: []workspace.ResolvedSecret{
			{Name: "alpha", Env: "ALPHA_VAR", ExposeEnv: "ALPHA"},
			{Name: "beta", File: "/tmp/beta"},
			{Name: "gamma", Env: "GAMMA_VAR", ExposeEnv: "GAMMA"},
		}}

		specs := secretSpecs(ws)
		if len(specs) != 3 {
			t.Fatalf("secretSpecs() len = %d, want 3", len(specs))
		}
		if specs[0].Name != "alpha" || specs[0].Environment != "ALPHA_VAR" || specs[0].File != "" {
			t.Fatalf("secretSpecs()[0] = %+v", specs[0])
		}
		if specs[1].Name != "beta" || specs[1].File != "/tmp/beta" || specs[1].Environment != "" {
			t.Fatalf("secretSpecs()[1] = %+v", specs[1])
		}

		pairs := secretEnvPairs(ws)
		want := []config.SecretEnvPair{{Name: "alpha", Var: "ALPHA"}, {Name: "gamma", Var: "GAMMA"}}
		if len(pairs) != len(want) {
			t.Fatalf("secretEnvPairs() = %+v, want %+v", pairs, want)
		}
		for i := range want {
			if pairs[i] != want[i] {
				t.Fatalf("secretEnvPairs()[%d] = %+v, want %+v", i, pairs[i], want[i])
			}
		}
	})

	t.Run("secrets without expose_env yield nil pairs", func(t *testing.T) {
		t.Parallel()
		ws := &workspace.Resolved{Name: "ws", Secrets: []workspace.ResolvedSecret{
			{Name: "beta", File: "/tmp/beta"},
		}}
		if got := secretEnvPairs(ws); got != nil {
			t.Fatalf("secretEnvPairs() = %v, want nil", got)
		}
	})
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

[workspaces.alpha.secrets.gh]
env = "GITHUB_TOKEN"
expose_env = "GH_TOKEN"

[workspaces.alpha.secrets.key]
file = %q

[workspaces.bare]
paths = ["/data/bare"]
`, secretFile)
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

	assertContains(t, out, "  Secrets:\n    - gh (env GITHUB_TOKEN -> GH_TOKEN)\n    - key (file "+secretFile+")\n")
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
