package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestWriteSecretEnvManifest(t *testing.T) {
	tests := []struct {
		name           string
		secretEnvNames []string
		want           string
	}{
		{
			name:           "single name",
			secretEnvNames: []string{"GH_TOKEN"},
			want:           "GH_TOKEN\n",
		},
		{
			name:           "caller order preserved",
			secretEnvNames: []string{"ANTHROPIC_API_KEY", "GH_TOKEN", "NPM_TOKEN"},
			want:           "ANTHROPIC_API_KEY\nGH_TOKEN\nNPM_TOKEN\n",
		},
		{
			name:           "empty names are skipped",
			secretEnvNames: []string{"GH_TOKEN", ""},
			want:           "GH_TOKEN\n",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			home := t.TempDir()
			t.Setenv("HOME", home)

			cfg := &Config{Workspaces: map[string]Workspace{"myws": {}}}

			if err := WriteAllowedFiles("myws", cfg, tt.secretEnvNames); err != nil {
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
		name           string
		secretEnvNames []string
	}{
		{name: "nil slice", secretEnvNames: nil},
		{name: "empty slice", secretEnvNames: []string{}},
		{name: "only empty names", secretEnvNames: []string{""}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			home := t.TempDir()
			t.Setenv("HOME", home)

			cfg := &Config{Workspaces: map[string]Workspace{"myws": {}}}

			if err := WriteAllowedFiles("myws", cfg, []string{"GH_TOKEN"}); err != nil {
				t.Fatalf("seed WriteAllowedFiles returned error: %v", err)
			}

			path := filepath.Join(ConfigDir(), "workspaces", "myws", "secret-env")
			if _, err := os.Stat(path); err != nil {
				t.Fatalf("expected seeded secret-env to exist: %v", err)
			}

			if err := WriteAllowedFiles("myws", cfg, tt.secretEnvNames); err != nil {
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
	t.Setenv("HOST_GH_TOKEN", "supersecret")

	cfg := &Config{Workspaces: map[string]Workspace{"myws": {
		Secrets: Secrets{Env: map[string]Secret{"GH_TOKEN": {FromEnv: "HOST_GH_TOKEN"}}},
	}}}

	if err := WriteAllowedFiles("myws", cfg, []string{"GH_TOKEN"}); err != nil {
		t.Fatalf("WriteAllowedFiles returned error: %v", err)
	}

	path := filepath.Join(ConfigDir(), "workspaces", "myws", "secret-env")

	data, err := os.ReadFile(path) //nolint:gosec // test reads from t.TempDir()
	if err != nil {
		t.Fatalf("read secret-env: %v", err)
	}
	// The manifest is also mounted into the privileged dind sidecar, so it must
	// carry neither the secret value nor the host variable it came from.
	if string(data) != "GH_TOKEN\n" {
		t.Fatalf("secret-env content = %q, want only the secret name", string(data))
	}
}
