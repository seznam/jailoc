package config

import (
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"github.com/BurntSushi/toml"
)

func assertSecretValidation(t *testing.T, err error, wantSubstrs []string, wantContext string) {
	t.Helper()

	if len(wantSubstrs) == 0 {
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		return
	}

	if err == nil {
		t.Fatal("expected validation error, got nil")
	}

	msg := err.Error()
	t.Logf("validation error: %v", err)
	for _, sub := range wantSubstrs {
		if !strings.Contains(msg, sub) {
			t.Errorf("error message %q missing expected substring %q", msg, sub)
		}
	}
	if !strings.Contains(msg, wantContext) {
		t.Errorf("error message %q missing expected context %q", msg, wantContext)
	}
}

func TestValidateEnvVarName(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		varName string
		wantErr bool
	}{
		{name: "uppercase with digit and underscore", varName: "TOKEN_1"},
		{name: "leading underscore", varName: "_TOKEN"},
		{name: "lowercase", varName: "token"},
		{name: "single letter", varName: "A"},
		{name: "leading digit", varName: "1TOKEN", wantErr: true},
		{name: "dash", varName: "TOKEN-NAME", wantErr: true},
		{name: "dot", varName: "TOKEN.NAME", wantErr: true},
		{name: "equals sign", varName: "TOKEN=1", wantErr: true},
		{name: "space", varName: "TOKEN NAME", wantErr: true},
		{name: "empty", varName: "", wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			err := validateEnvVarName(tt.varName)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("expected error for %q, got nil", tt.varName)
				}
				if !strings.Contains(err.Error(), tt.varName) {
					t.Errorf("error message %q missing the rejected name %q", err.Error(), tt.varName)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error for %q: %v", tt.varName, err)
			}
		})
	}
}

func TestExpandPathsTildeSecretFile(t *testing.T) {
	home := "/data/home_test_" + strings.ReplaceAll(t.Name(), "/", "_")
	t.Setenv("HOME", home)

	t.Run("workspace", func(t *testing.T) {
		ws := &Workspace{
			Paths: []string{"/data"},
			Secrets: Secrets{
				File: map[string]FileSecret{"token": {FromFile: "~/secrets/token"}},
				Env:  map[string]EnvSecret{"OTHER": {FromEnv: "~HOST_VAR"}},
			},
		}

		if err := expandPaths(ws); err != nil {
			t.Fatalf("expandPaths failed: %v", err)
		}

		if got := ws.Secrets.File["token"].FromFile; got != home+"/secrets/token" {
			t.Errorf("secret file = %q, want %q", got, home+"/secrets/token")
		}
		if got := ws.Secrets.Env["OTHER"].FromEnv; got != "~HOST_VAR" {
			t.Errorf("env = %q, want it untouched", got)
		}
	})

	t.Run("defaults", func(t *testing.T) {
		secrets := Secrets{
			File: map[string]FileSecret{"token": {FromFile: "~/secrets/token"}},
			Env:  map[string]EnvSecret{"OTHER": {FromEnv: "~HOST_VAR"}},
		}

		if err := expandSecretsBlockFiles(&secrets); err != nil {
			t.Fatalf("expandSecretsBlockFiles failed: %v", err)
		}

		if got := secrets.File["token"].FromFile; got != home+"/secrets/token" {
			t.Errorf("secret file = %q, want %q", got, home+"/secrets/token")
		}
		if got := secrets.Env["OTHER"].FromEnv; got != "~HOST_VAR" {
			t.Errorf("env = %q, want it untouched", got)
		}
	})
}

// TestValidateExpandsSecretFileBeforeAbsCheck locks the ordering invariant: a
// "~" path must be expanded before the filepath.IsAbs check, otherwise a valid
// config would be rejected as relative. The config layer does NOT require the
// file to exist — the secret file is deliberately never created here.
func TestValidateExpandsSecretFileBeforeAbsCheck(t *testing.T) {
	home := safeHome(t)
	secretFile := filepath.Join(home, "token")

	t.Run("defaults", func(t *testing.T) {
		cfg := &Config{
			Defaults: Defaults{Secrets: Secrets{File: map[string]FileSecret{"token": {FromFile: "~/token"}}}},
		}
		if err := Validate(cfg); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if got := cfg.Defaults.Secrets.File["token"].FromFile; got != secretFile {
			t.Errorf("secret file = %q, want %q", got, secretFile)
		}
	})

	t.Run("workspace", func(t *testing.T) {
		cfg := &Config{
			Workspaces: map[string]Workspace{
				"myws": {Secrets: Secrets{File: map[string]FileSecret{"token": {FromFile: "~/token"}}}},
			},
		}
		if err := Validate(cfg); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if got := cfg.Workspaces["myws"].Secrets.File["token"].FromFile; got != secretFile {
			t.Errorf("secret file = %q, want %q", got, secretFile)
		}
	})
}

// TestLoadFromAcceptsMissingSecretFile locks the bug fix: config.Load runs
// Validate on EVERY jailoc command, so a missing secret file in any workspace
// must not break unrelated commands (config, status, logs, down). Existence is
// enforced at up-time only.
func TestLoadFromAcceptsMissingSecretFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")
	writeFile(t, path, `
[workspaces.default]
paths = ["/data/workspace"]

[workspaces.default.secrets.file.token]
from_file = "/nonexistent/path/secret"
`)

	cfg, err := LoadFrom(path)
	if err != nil {
		t.Fatalf("LoadFrom failed: %v", err)
	}

	if got := cfg.Workspaces["default"].Secrets.File["token"].FromFile; got != "/nonexistent/path/secret" {
		t.Errorf("secret file = %q, want %q", got, "/nonexistent/path/secret")
	}
}

func TestDefaultConfigContentDocumentsSecrets(t *testing.T) {
	t.Parallel()

	wantLines := []string{
		"# [defaults.secrets.env.MY_TOKEN]",
		"# [defaults.secrets.file.MY_FILE]",
		"# [workspaces.default.secrets.env.MY_TOKEN]",
		"# [workspaces.default.secrets.file.MY_FILE]",
		`#   from_env = "HOST_ENV_VAR"`,
		`#   from_file = "/path/to/secret"`,
	}

	for _, line := range wantLines {
		if !strings.Contains(defaultConfigContent, line) {
			t.Errorf("defaultConfigContent missing %q", line)
		}
	}
	if strings.Contains(defaultConfigContent, "expose_env") {
		t.Error("defaultConfigContent still documents expose_env")
	}
}

func decodeRaw(t *testing.T, input string) map[string]any {
	t.Helper()

	raw := map[string]any{}
	if _, err := toml.Decode(input, &raw); err != nil {
		t.Fatalf("decode raw TOML: %v", err)
	}
	return raw
}

func TestDetectLegacySecrets(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		input       string
		wantSubstrs []string
	}{
		{
			name: "valid new schema",
			input: `
[defaults.secrets.env.MY_TOKEN]
from_env = "GH_TOKEN"

[defaults.secrets.file.NETRC]
from_file = "/home/me/.netrc"

[workspaces.web.secrets.env.API_KEY]
from_env = "API_KEY"
`,
		},
		{
			name: "workspace literally named secrets is not flagged",
			input: `
[workspaces.secrets]
paths = []
`,
		},
		{
			name: "bare empty defaults secrets table is accepted",
			input: `
[defaults.secrets]
`,
		},
		{
			name: "top level secrets namespace",
			input: `
[secrets.env.X]
from_env = "HOST"
`,
			wantSubstrs: []string{"top-level [secrets]", "defaults.secrets.env.<NAME>", secretsDocURL},
		},
		{
			name: "legacy defaults secret with env source",
			input: `
[defaults.secrets.TOKEN]
env = "HOST_TOKEN"
`,
			wantSubstrs: []string{
				`legacy secret "defaults.secrets.TOKEN"`,
				"[defaults.secrets.env.TOKEN] from_env",
				"[defaults.secrets.file.TOKEN] from_file",
				"/run/secrets/TOKEN",
				"secrets.env",
				secretsDocURL,
			},
		},
		{
			name: "legacy workspace secret with file source",
			input: `
[workspaces.web.secrets.TOKEN]
file = "/abs/token"
`,
			wantSubstrs: []string{
				`legacy secret "workspaces.web.secrets.TOKEN"`,
				"[workspaces.web.secrets.env.TOKEN] from_env",
				"[workspaces.web.secrets.file.TOKEN] from_file",
			},
		},
		{
			name: "legacy secret literally named env",
			input: `
[defaults.secrets.env]
env = "HOST_TOKEN"
`,
			wantSubstrs: []string{`legacy secret "defaults.secrets.env"`, "schema was removed"},
		},
		{
			name: "legacy secret literally named file",
			input: `
[defaults.secrets.file]
file = "/x"
`,
			wantSubstrs: []string{`legacy secret "defaults.secrets.file"`, "schema was removed"},
		},
		{
			name:        "legacy inline table",
			input:       `defaults = { secrets = { TOKEN = { env = "HOST_TOKEN" } } }`,
			wantSubstrs: []string{`legacy secret "defaults.secrets.TOKEN"`},
		},
		{
			name: "empty env namespace table is ambiguous",
			input: `
[defaults.secrets.env]
`,
			wantSubstrs: []string{`legacy secret "defaults.secrets.env"`},
		},
		{
			name: "empty file namespace table is ambiguous",
			input: `
[defaults.secrets.file]
`,
			wantSubstrs: []string{`legacy secret "defaults.secrets.file"`},
		},
		{
			name:        "empty inline env namespace is ambiguous",
			input:       `defaults = { secrets = { env = {} } }`,
			wantSubstrs: []string{`legacy secret "defaults.secrets.env"`},
		},
		{
			name:        "empty inline file namespace is ambiguous",
			input:       `defaults = { secrets = { file = {} } }`,
			wantSubstrs: []string{`legacy secret "defaults.secrets.file"`},
		},
		{
			name: "workspace named secrets alongside a legacy defaults secret",
			input: `
[workspaces.secrets]
paths = []

[defaults.secrets.TOKEN]
env = "HOST_TOKEN"
`,
			wantSubstrs: []string{`legacy secret "defaults.secrets.TOKEN"`},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			err := detectLegacySecrets(decodeRaw(t, tt.input))

			if len(tt.wantSubstrs) == 0 {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				return
			}

			if err == nil {
				t.Fatal("expected a migration error, got nil")
			}
			t.Logf("migration error: %v", err)
			for _, sub := range tt.wantSubstrs {
				if !strings.Contains(err.Error(), sub) {
					t.Errorf("error %q missing expected substring %q", err.Error(), sub)
				}
			}
		})
	}
}

func TestLoadFromRejectsLegacySecrets(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		input       string
		wantSubstrs []string
	}{
		{
			name: "defaults legacy entry",
			input: `
[defaults.secrets.TOKEN]
env = "HOST_TOKEN"
`,
			wantSubstrs: []string{"defaults.secrets.TOKEN", "secrets.env"},
		},
		{
			name: "legacy entry named env",
			input: `
[defaults.secrets.env]
env = "HOST_TOKEN"
`,
			wantSubstrs: []string{"defaults.secrets.env", "schema was removed"},
		},
		{
			name:        "top-level secrets",
			input:       "[secrets.env.X]\nfrom_env = \"HOST\"\n",
			wantSubstrs: []string{"top-level [secrets]"},
		},
		{
			name:        "legacy inline entry",
			input:       `defaults = { secrets = { TOKEN = { env = "HOST" } } }`,
			wantSubstrs: []string{"defaults.secrets.TOKEN"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			path := filepath.Join(t.TempDir(), "config.toml")
			writeFile(t, path, tt.input)

			_, err := LoadFrom(path)
			if err == nil {
				t.Fatal("expected migration error, got nil")
			}
			for _, sub := range tt.wantSubstrs {
				if !strings.Contains(err.Error(), sub) {
					t.Errorf("error %q missing expected substring %q", err.Error(), sub)
				}
			}
		})
	}
}

func TestLoadFromValidatesEnvSecretNames(t *testing.T) {
	t.Parallel()

	for _, name := range []string{"gh-token", "HOME", "PATH", "DOCKER_HOST"} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			path := filepath.Join(t.TempDir(), "config.toml")
			writeFile(t, path, fmt.Sprintf(`
[workspaces.default.secrets.env.%s]
from_env = "HOST_TOKEN"
`, name))

			_, err := LoadFrom(path)
			if err == nil {
				t.Fatalf("expected validation error for %q, got nil", name)
			}
			if !strings.Contains(err.Error(), name) {
				t.Fatalf("error %q does not name %q", err.Error(), name)
			}
		})
	}
}

// TestDetectLegacySecretsReportsOnceInSortedOrder locks P2-01: the map[string]any
// view is unordered, so the detector must sort scopes (defaults first, then
// workspace names) and secret names, and emit exactly one line per legacy secret.
func TestDetectLegacySecretsReportsOnceInSortedOrder(t *testing.T) {
	t.Parallel()

	input := `
[workspaces.zeta.secrets.ZZZ]
env = "HOST"

[workspaces.alpha.secrets.BBB]
env = "HOST"

[workspaces.alpha.secrets.AAA]
env = "HOST"

[defaults.secrets.MMM]
env = "HOST"
`

	want := []string{
		"defaults.secrets.MMM",
		"workspaces.alpha.secrets.AAA",
		"workspaces.alpha.secrets.BBB",
		"workspaces.zeta.secrets.ZZZ",
	}

	for range 50 {
		err := detectLegacySecrets(decodeRaw(t, input))
		if err == nil {
			t.Fatal("expected a migration error, got nil")
		}

		lines := strings.Split(err.Error(), "\n")
		if len(lines) != len(want) {
			t.Fatalf("got %d error lines, want %d: %v", len(lines), len(want), lines)
		}
		for i, name := range want {
			if !strings.Contains(lines[i], `legacy secret "`+name+`"`) {
				t.Fatalf("line %d = %q, want it to report %q", i, lines[i], name)
			}
		}
	}
}

func TestValidateEnvSecret(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		secretName  string
		secret      EnvSecret
		wantSubstrs []string
	}{
		{name: "uppercase name", secretName: "MY_TOKEN", secret: EnvSecret{FromEnv: "GH_TOKEN"}},
		{name: "leading underscore", secretName: "_TOKEN9", secret: EnvSecret{FromEnv: "GH_TOKEN"}},
		{
			name:       "from_env is not grammar checked",
			secretName: "MY_TOKEN",
			secret:     EnvSecret{FromEnv: "not-a-valid-shell-identifier"},
		},
		{
			name:        "name with a dash",
			secretName:  "gh-token",
			secret:      EnvSecret{FromEnv: "GH_TOKEN"},
			wantSubstrs: []string{"gh-token", "^[A-Za-z_][A-Za-z0-9_]*$"},
		},
		{
			name:        "name starting with a digit",
			secretName:  "1TOKEN",
			secret:      EnvSecret{FromEnv: "GH_TOKEN"},
			wantSubstrs: []string{"1TOKEN", "^[A-Za-z_][A-Za-z0-9_]*$"},
		},
		{
			name:        "empty name",
			secretName:  "",
			secret:      EnvSecret{FromEnv: "GH_TOKEN"},
			wantSubstrs: []string{"^[A-Za-z_][A-Za-z0-9_]*$"},
		},
		{
			name:        "HOME is reserved",
			secretName:  "HOME",
			secret:      EnvSecret{FromEnv: "GH_TOKEN"},
			wantSubstrs: []string{"HOME", "reserved"},
		},
		{
			name:        "PATH is reserved",
			secretName:  "PATH",
			secret:      EnvSecret{FromEnv: "GH_TOKEN"},
			wantSubstrs: []string{"PATH", "reserved"},
		},
		{
			name:        "DOCKER_HOST is reserved",
			secretName:  "DOCKER_HOST",
			secret:      EnvSecret{FromEnv: "GH_TOKEN"},
			wantSubstrs: []string{"DOCKER_HOST", "reserved"},
		},
		{
			name:        "SSH_AUTH_SOCK is reserved",
			secretName:  "SSH_AUTH_SOCK",
			secret:      EnvSecret{FromEnv: "GH_TOKEN"},
			wantSubstrs: []string{"SSH_AUTH_SOCK", "reserved"},
		},
		{
			name:        "empty from_env",
			secretName:  "MY_TOKEN",
			secret:      EnvSecret{},
			wantSubstrs: []string{"MY_TOKEN", "from_env", "must not be empty"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			err := validateEnvSecret(tt.secretName, tt.secret, "defaults")
			assertSecretValidation(t, err, tt.wantSubstrs, "defaults")
		})
	}
}

func TestValidateFileSecret(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		secretName  string
		secret      FileSecret
		wantSubstrs []string
	}{
		{name: "absolute path", secretName: "NETRC", secret: FileSecret{FromFile: "/home/me/.netrc"}},
		{name: "name with dash and underscore", secretName: "my-secret_1", secret: FileSecret{FromFile: "/abs/x"}},
		{
			name:       "nonexistent file is accepted at the config layer",
			secretName: "NETRC",
			secret:     FileSecret{FromFile: "/nonexistent/path/secret"},
		},
		{
			name:        "name with a dot",
			secretName:  "my.secret",
			secret:      FileSecret{FromFile: "/abs/x"},
			wantSubstrs: []string{"my.secret", "invalid name", "[a-zA-Z0-9_-]+"},
		},
		{
			name:        "name with a slash",
			secretName:  "dir/token",
			secret:      FileSecret{FromFile: "/abs/x"},
			wantSubstrs: []string{"dir/token", "invalid name"},
		},
		{
			name:        "empty name",
			secretName:  "",
			secret:      FileSecret{FromFile: "/abs/x"},
			wantSubstrs: []string{"invalid name"},
		},
		{
			name:        "empty from_file",
			secretName:  "NETRC",
			secret:      FileSecret{},
			wantSubstrs: []string{"NETRC", "from_file", "must not be empty"},
		},
		{
			name:        "relative from_file",
			secretName:  "NETRC",
			secret:      FileSecret{FromFile: "relative/secret"},
			wantSubstrs: []string{"NETRC", "relative/secret", "must be absolute"},
		},
		{
			name:        "from_file containing a dollar sign",
			secretName:  "NETRC",
			secret:      FileSecret{FromFile: "/secrets/$USER/token"},
			wantSubstrs: []string{"NETRC", "/secrets/$USER/token", "$", "interpolates"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			err := validateFileSecret(tt.secretName, tt.secret, "workspace \"myws\"")
			assertSecretValidation(t, err, tt.wantSubstrs, "myws")
		})
	}
}

func TestValidateSecretsBlock(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		secrets     Secrets
		wantSubstrs []string
	}{
		{name: "empty block"},
		{
			name: "env and file with distinct names",
			secrets: Secrets{
				Env:  map[string]EnvSecret{"MY_TOKEN": {FromEnv: "GH_TOKEN"}},
				File: map[string]FileSecret{"NETRC": {FromFile: "/home/me/.netrc"}},
			},
		},
		{
			name: "same name in env and file",
			secrets: Secrets{
				Env:  map[string]EnvSecret{"TOKEN": {FromEnv: "GH_TOKEN"}},
				File: map[string]FileSecret{"TOKEN": {FromFile: "/abs/token"}},
			},
			wantSubstrs: []string{"TOKEN", "secrets.env", "secrets.file", "only one"},
		},
		{
			name:        "invalid env entry propagates",
			secrets:     Secrets{Env: map[string]EnvSecret{"gh-token": {FromEnv: "GH_TOKEN"}}},
			wantSubstrs: []string{"gh-token", "^[A-Za-z_][A-Za-z0-9_]*$"},
		},
		{
			name:        "invalid file entry propagates",
			secrets:     Secrets{File: map[string]FileSecret{"NETRC": {FromFile: "relative/x"}}},
			wantSubstrs: []string{"NETRC", "must be absolute"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			err := validateSecretsBlock(tt.secrets, "defaults")
			assertSecretValidation(t, err, tt.wantSubstrs, "defaults")
		})
	}
}

func TestValidateSecretsBlockIteratesSortedNames(t *testing.T) {
	t.Parallel()

	secrets := Secrets{Env: map[string]EnvSecret{"aaa": {}, "mmm": {}, "zzz": {}}}

	for range 50 {
		err := validateSecretsBlock(secrets, "defaults")
		if err == nil {
			t.Fatal("expected validation error, got nil")
		}
		if !strings.Contains(err.Error(), `env secret "aaa"`) {
			t.Fatalf("expected the alphabetically first secret to be reported, got: %v", err)
		}
	}
}

func TestExpandSecretsBlockFiles(t *testing.T) {
	home := "/data/home_test_" + strings.ReplaceAll(t.Name(), "/", "_")
	t.Setenv("HOME", home)

	secrets := Secrets{
		Env:  map[string]EnvSecret{"TOKEN": {FromEnv: "~HOST_VAR"}},
		File: map[string]FileSecret{"NETRC": {FromFile: "~/secrets/netrc"}, "ABS": {FromFile: "/abs/x"}},
	}

	if err := expandSecretsBlockFiles(&secrets); err != nil {
		t.Fatalf("expandSecretsBlockFiles failed: %v", err)
	}

	if got := secrets.File["NETRC"].FromFile; got != home+"/secrets/netrc" {
		t.Errorf("from_file = %q, want %q", got, home+"/secrets/netrc")
	}
	if got := secrets.File["ABS"].FromFile; got != "/abs/x" {
		t.Errorf("from_file = %q, want it untouched", got)
	}
	if got := secrets.Env["TOKEN"].FromEnv; got != "~HOST_VAR" {
		t.Errorf("from_env = %q, want it untouched", got)
	}
}
