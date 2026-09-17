package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDindCABundleDecodeAcceptsSupportedValues(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		value     string
		want      DindCABundle
		wantUnset bool
	}{
		{name: "omitted", wantUnset: true},
		{name: "automatic", value: "true", want: AutomaticDindCABundle()},
		{name: "disabled", value: "false", want: DisabledDindCABundle()},
		{name: "custom path", value: `"/tmp/company-ca.pem"`, want: CustomDindCABundle("/tmp/company-ca.pem")},
		{name: "custom path preserves whitespace", value: `" /tmp/company ca.pem "`, want: CustomDindCABundle(" /tmp/company ca.pem ")},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			path := filepath.Join(t.TempDir(), "config.toml")
			line := ""
			if tt.value != "" {
				line = "dind_ca_bundle = " + tt.value + "\n"
			}
			writeFile(t, path, "[defaults]\n"+line+"[workspaces.default]\npaths = [\"/data\"]\n")

			cfg, err := LoadFrom(path)
			if err != nil {
				t.Fatalf("LoadFrom() error = %v", err)
			}

			if tt.wantUnset {
				if cfg.Defaults.DindCABundle != nil {
					t.Fatalf("DindCABundle = %#v, want nil", cfg.Defaults.DindCABundle)
				}
				return
			}
			if cfg.Defaults.DindCABundle == nil {
				t.Fatal("DindCABundle = nil, want configured value")
			}
			if *cfg.Defaults.DindCABundle != tt.want {
				t.Fatalf("DindCABundle = %#v, want %#v", *cfg.Defaults.DindCABundle, tt.want)
			}
		})
	}
}

func TestDindCABundleDecodeRejectsUnsupportedValues(t *testing.T) {
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
			writeFile(t, path, "[defaults]\ndind_ca_bundle = "+tt.value+"\n[workspaces.default]\npaths = [\"/data\"]\n")

			_, err := LoadFrom(path)
			if err == nil {
				t.Fatal("LoadFrom() error = nil, want unsupported dind_ca_bundle error")
			}
			if !strings.Contains(err.Error(), "dind_ca_bundle") {
				t.Fatalf("LoadFrom() error = %q, want field context", err)
			}
		})
	}
}

func TestAddPathPreservesDindCABundleValues(t *testing.T) {
	tests := []struct {
		name          string
		defaultsLine  string
		workspaceLine string
	}{
		{name: "omitted"},
		{name: "automatic default", defaultsLine: "dind_ca_bundle = true\n"},
		{name: "disabled default", defaultsLine: "dind_ca_bundle = false\n"},
		{name: "custom default", defaultsLine: "dind_ca_bundle = \"/tmp/default.pem\"\n"},
		{name: "automatic workspace", workspaceLine: "dind_ca_bundle = true\n"},
		{name: "disabled workspace", workspaceLine: "dind_ca_bundle = false\n"},
		{name: "custom workspace", workspaceLine: "dind_ca_bundle = \"~/workspace.pem\"\n"},
		{name: "custom workspace with spaces", workspaceLine: "dind_ca_bundle = \"/tmp/company ca.pem \"\n"},
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

			if before.Defaults.DindCABundle == nil != (after.Defaults.DindCABundle == nil) {
				t.Fatalf("defaults omission changed: before=%#v after=%#v", before.Defaults.DindCABundle, after.Defaults.DindCABundle)
			}
			if before.Defaults.DindCABundle != nil && *before.Defaults.DindCABundle != *after.Defaults.DindCABundle {
				t.Fatalf("defaults value changed: before=%#v after=%#v", *before.Defaults.DindCABundle, *after.Defaults.DindCABundle)
			}
			beforeWorkspace := before.Workspaces["default"]
			afterWorkspace := after.Workspaces["default"]
			if beforeWorkspace.DindCABundle == nil != (afterWorkspace.DindCABundle == nil) {
				t.Fatalf("workspace omission changed: before=%#v after=%#v", beforeWorkspace.DindCABundle, afterWorkspace.DindCABundle)
			}
			if beforeWorkspace.DindCABundle != nil && *beforeWorkspace.DindCABundle != *afterWorkspace.DindCABundle {
				t.Fatalf("workspace value changed: before=%#v after=%#v", *beforeWorkspace.DindCABundle, *afterWorkspace.DindCABundle)
			}

			data, err := os.ReadFile(ConfigPath())
			if err != nil {
				t.Fatalf("ReadFile() error = %v", err)
			}
			if tt.defaultsLine == "" && tt.workspaceLine == "" && strings.Contains(string(data), "dind_ca_bundle") {
				t.Fatalf("omitted dind_ca_bundle was emitted:\n%s", data)
			}
		})
	}
}

func TestDefaultConfigDocumentsDindCABundle(t *testing.T) {
	t.Parallel()

	if count := strings.Count(defaultConfigContent, "# dind_ca_bundle = true"); count != 2 {
		t.Fatalf("default config dind_ca_bundle examples = %d, want defaults and workspace comments", count)
	}
	for _, term := range []string{"SSL_CERT_FILE", "NIX_SSL_CERT_FILE", "false", "string", "inherit defaults"} {
		if !strings.Contains(defaultConfigContent, term) {
			t.Fatalf("default config dind_ca_bundle comments missing %q", term)
		}
	}
}
