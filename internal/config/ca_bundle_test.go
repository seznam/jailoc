package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCABundleDecodeAcceptsSupportedValues(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		value     string
		want      CABundle
		wantUnset bool
	}{
		{name: "omitted", wantUnset: true},
		{name: "automatic", value: "true", want: AutomaticCABundle()},
		{name: "disabled", value: "false", want: DisabledCABundle()},
		{name: "custom path", value: `"/tmp/company-ca.pem"`, want: CustomCABundle("/tmp/company-ca.pem")},
		{name: "custom path preserves whitespace", value: `" /tmp/company ca.pem "`, want: CustomCABundle(" /tmp/company ca.pem ")},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			path := filepath.Join(t.TempDir(), "config.toml")
			line := ""
			if tt.value != "" {
				line = "ca_bundle = " + tt.value + "\n"
			}
			writeFile(t, path, "[defaults]\n"+line+"[workspaces.default]\npaths = [\"/data\"]\n")

			cfg, err := LoadFrom(path)
			if err != nil {
				t.Fatalf("LoadFrom() error = %v", err)
			}

			if tt.wantUnset {
				if cfg.Defaults.CABundle != nil {
					t.Fatalf("CABundle = %#v, want nil", cfg.Defaults.CABundle)
				}
				return
			}
			if cfg.Defaults.CABundle == nil {
				t.Fatal("CABundle = nil, want configured value")
			}
			if *cfg.Defaults.CABundle != tt.want {
				t.Fatalf("CABundle = %#v, want %#v", *cfg.Defaults.CABundle, tt.want)
			}
		})
	}
}

func TestCABundleDecodeRejectsUnsupportedValues(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		value string
	}{
		{name: "empty string", value: `""`},
		{name: "blank string", value: `"   "`},
		{name: "integer", value: "1"},
		{name: "float", value: "1.5"},
		{name: "array", value: "[]"},
		{name: "table", value: `{ path = "/tmp/ca.pem" }`},
		{name: "date", value: "1979-05-27"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			path := filepath.Join(t.TempDir(), "config.toml")
			writeFile(t, path, "[defaults]\nca_bundle = "+tt.value+"\n[workspaces.default]\npaths = [\"/data\"]\n")

			_, err := LoadFrom(path)
			if err == nil {
				t.Fatal("LoadFrom() error = nil, want unsupported ca_bundle error")
			}
			if !strings.Contains(err.Error(), "ca_bundle") {
				t.Fatalf("LoadFrom() error = %q, want field context", err)
			}
		})
	}
}

func TestAddPathPreservesCABundleValues(t *testing.T) {
	tests := []struct {
		name          string
		defaultsLine  string
		workspaceLine string
	}{
		{name: "omitted"},
		{name: "automatic default", defaultsLine: "ca_bundle = true\n"},
		{name: "disabled default", defaultsLine: "ca_bundle = false\n"},
		{name: "custom default", defaultsLine: "ca_bundle = \"/tmp/default.pem\"\n"},
		{name: "automatic workspace", workspaceLine: "ca_bundle = true\n"},
		{name: "disabled workspace", workspaceLine: "ca_bundle = false\n"},
		{name: "custom workspace", workspaceLine: "ca_bundle = \"~/workspace.pem\"\n"},
		{name: "custom workspace with spaces", workspaceLine: "ca_bundle = \"/tmp/company ca.pem \"\n"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			home := t.TempDir()
			t.Setenv("HOME", home)
			writeFile(t, ConfigPath(), "[defaults]\n"+tt.defaultsLine+"[workspaces.default]\npaths = [\"/data\"]\n"+tt.workspaceLine)

			before, err := LoadFrom(ConfigPath())
			if err != nil {
				t.Fatalf("initial LoadFrom() error = %v", err)
			}
			if err := AddPath("default", "/more"); err != nil {
				t.Fatalf("AddPath() error = %v", err)
			}
			after, err := LoadFrom(ConfigPath())
			if err != nil {
				t.Fatalf("final LoadFrom() error = %v", err)
			}

			if before.Defaults.CABundle == nil != (after.Defaults.CABundle == nil) {
				t.Fatalf("defaults omission changed: before=%#v after=%#v", before.Defaults.CABundle, after.Defaults.CABundle)
			}
			if before.Defaults.CABundle != nil && *before.Defaults.CABundle != *after.Defaults.CABundle {
				t.Fatalf("defaults value changed: before=%#v after=%#v", *before.Defaults.CABundle, *after.Defaults.CABundle)
			}
			beforeWorkspace := before.Workspaces["default"]
			afterWorkspace := after.Workspaces["default"]
			if beforeWorkspace.CABundle == nil != (afterWorkspace.CABundle == nil) {
				t.Fatalf("workspace omission changed: before=%#v after=%#v", beforeWorkspace.CABundle, afterWorkspace.CABundle)
			}
			if beforeWorkspace.CABundle != nil && *beforeWorkspace.CABundle != *afterWorkspace.CABundle {
				t.Fatalf("workspace value changed: before=%#v after=%#v", *beforeWorkspace.CABundle, *afterWorkspace.CABundle)
			}

			data, err := os.ReadFile(ConfigPath())
			if err != nil {
				t.Fatalf("ReadFile() error = %v", err)
			}
			if tt.defaultsLine == "" && tt.workspaceLine == "" && strings.Contains(string(data), "ca_bundle") {
				t.Fatalf("omitted ca_bundle was emitted:\n%s", data)
			}
		})
	}
}

func TestDefaultConfigDocumentsCABundle(t *testing.T) {
	t.Parallel()

	if count := strings.Count(defaultConfigContent, "# ca_bundle = true"); count != 2 {
		t.Fatalf("default config ca_bundle examples = %d, want defaults and workspace comments", count)
	}
	for _, term := range []string{"SSL_CERT_FILE", "NIX_SSL_CERT_FILE", "false", "string", "inherit defaults"} {
		if !strings.Contains(defaultConfigContent, term) {
			t.Fatalf("default config ca_bundle comments missing %q", term)
		}
	}
}

func TestCABundleDoesNotAcceptDindAlias(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "config.toml")
	legacyKey := "dind_" + "ca_bundle"
	writeFile(t, path, "[defaults]\n"+legacyKey+" = false\n[workspaces.default]\npaths = [\"/data\"]\n")

	cfg, err := LoadFrom(path)
	if err != nil {
		t.Fatalf("LoadFrom() error = %v", err)
	}
	if cfg.Defaults.CABundle != nil {
		t.Fatalf("CABundle = %#v, want nil for unsupported alias", cfg.Defaults.CABundle)
	}
}
