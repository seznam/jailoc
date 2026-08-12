package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestWriteSecretEnvManifest(t *testing.T) {
	tests := []struct {
		name      string
		secretEnv []SecretEnvPair
		want      string
	}{
		{
			name:      "single pair",
			secretEnv: []SecretEnvPair{{Name: "gh", Var: "GH_TOKEN"}},
			want:      "gh GH_TOKEN\n",
		},
		{
			name: "caller order preserved",
			secretEnv: []SecretEnvPair{
				{Name: "anthropic", Var: "ANTHROPIC_API_KEY"},
				{Name: "gh", Var: "GH_TOKEN"},
				{Name: "npm", Var: "NPM_TOKEN"},
			},
			want: "anthropic ANTHROPIC_API_KEY\ngh GH_TOKEN\nnpm NPM_TOKEN\n",
		},
		{
			name: "pairs without a target var are skipped",
			secretEnv: []SecretEnvPair{
				{Name: "gh", Var: "GH_TOKEN"},
				{Name: "internal-only", Var: ""},
			},
			want: "gh GH_TOKEN\n",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			home := t.TempDir()
			t.Setenv("HOME", home)

			cfg := &Config{Workspaces: map[string]Workspace{"myws": {}}}

			if err := WriteAllowedFiles("myws", cfg, tt.secretEnv); err != nil {
				t.Fatalf("WriteAllowedFiles returned error: %v", err)
			}

			path := filepath.Join(ConfigDir(), "workspaces", "myws", "secret-env")

			data, err := os.ReadFile(path) //nolint:gosec // test reads from t.TempDir()
			if err != nil {
				t.Fatalf("read secret-env: %v", err)
			}
			if string(data) != tt.want {
				t.Fatalf("secret-env content = %q, want %q", string(data), tt.want)
			}

			info, err := os.Stat(path)
			if err != nil {
				t.Fatalf("stat secret-env: %v", err)
			}
			if perm := info.Mode().Perm(); perm != 0o600 {
				t.Fatalf("secret-env mode = %#o, want %#o", perm, 0o600)
			}
		})
	}
}

func TestWriteSecretEnvRemovesStaleManifest(t *testing.T) {
	tests := []struct {
		name      string
		secretEnv []SecretEnvPair
	}{
		{name: "nil slice", secretEnv: nil},
		{name: "empty slice", secretEnv: []SecretEnvPair{}},
		{name: "only pairs without a target var", secretEnv: []SecretEnvPair{{Name: "gh", Var: ""}}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			home := t.TempDir()
			t.Setenv("HOME", home)

			cfg := &Config{Workspaces: map[string]Workspace{"myws": {}}}

			if err := WriteAllowedFiles("myws", cfg, []SecretEnvPair{{Name: "gh", Var: "GH_TOKEN"}}); err != nil {
				t.Fatalf("seed WriteAllowedFiles returned error: %v", err)
			}

			path := filepath.Join(ConfigDir(), "workspaces", "myws", "secret-env")
			if _, err := os.Stat(path); err != nil {
				t.Fatalf("expected seeded secret-env to exist: %v", err)
			}

			if err := WriteAllowedFiles("myws", cfg, tt.secretEnv); err != nil {
				t.Fatalf("WriteAllowedFiles returned error: %v", err)
			}

			if _, err := os.Stat(path); !os.IsNotExist(err) {
				t.Fatalf("expected stale secret-env to be removed, stat error = %v", err)
			}
		})
	}
}

func TestWriteSecretEnvManifestCarriesNoValues(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	cfg := &Config{Workspaces: map[string]Workspace{
		"myws": {Secrets: map[string]Secret{
			"gh": {File: "/tmp/token", ExposeEnv: "GH_TOKEN"},
		}},
	}}

	if err := WriteAllowedFiles("myws", cfg, []SecretEnvPair{{Name: "gh", Var: "GH_TOKEN"}}); err != nil {
		t.Fatalf("WriteAllowedFiles returned error: %v", err)
	}

	path := filepath.Join(ConfigDir(), "workspaces", "myws", "secret-env")

	data, err := os.ReadFile(path) //nolint:gosec // test reads from t.TempDir()
	if err != nil {
		t.Fatalf("read secret-env: %v", err)
	}
	if string(data) != "gh GH_TOKEN\n" {
		t.Fatalf("secret-env content = %q, want only the name/var pair", string(data))
	}
}
