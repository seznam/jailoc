package compose

import (
	"reflect"
	"strings"
	"testing"
)

func secretsParams(secrets []SecretSpec) ComposeParams {
	return ComposeParams{
		WorkspaceName:  "secrets-test",
		Port:           4950,
		Image:          "ghcr.io/seznam/jailoc:test",
		Paths:          []string{"/tmp/work"},
		CPU:            2.0,
		Memory:         "4g",
		UseDataVolume:  true,
		UseCacheVolume: true,
		EnableDocker:   true,
		Secrets:        secrets,
	}
}

func renderSecrets(t *testing.T, secrets []SecretSpec) string {
	t.Helper()

	out, err := GenerateCompose(secretsParams(secrets))
	if err != nil {
		t.Fatalf("GenerateCompose returned error: %v", err)
	}
	return string(out)
}

// servicesWithSecretsGrant walks the rendered compose file and reports which
// services carry a `secrets:` grant. The grant must never reach the privileged
// dind sidecar, so the check is structural rather than a substring match.
func servicesWithSecretsGrant(t *testing.T, rendered string) []string {
	t.Helper()

	var (
		found      []string
		current    string
		inServices bool
	)

	for _, line := range strings.Split(rendered, "\n") {
		if line == "" {
			continue
		}
		if !strings.HasPrefix(line, " ") {
			inServices = line == "services:"
			current = ""
			continue
		}
		if !inServices {
			continue
		}
		if isMappingKeyAtIndent(line, 2) {
			current = strings.TrimSuffix(strings.TrimSpace(line), ":")
			continue
		}
		if line == "    secrets:" {
			found = append(found, current)
		}
	}

	return found
}

func topLevelSecretKeys(t *testing.T, rendered string) []string {
	t.Helper()

	var (
		keys    []string
		inBlock bool
	)

	for _, line := range strings.Split(rendered, "\n") {
		if line == "" {
			continue
		}
		if !strings.HasPrefix(line, " ") {
			inBlock = line == "secrets:"
			continue
		}
		if inBlock && isMappingKeyAtIndent(line, 2) {
			keys = append(keys, strings.TrimSuffix(strings.TrimSpace(line), ":"))
		}
	}

	return keys
}

func serviceSecretGrants(t *testing.T, rendered string) []string {
	t.Helper()

	var (
		grants  []string
		inGrant bool
	)

	for _, line := range strings.Split(rendered, "\n") {
		if line == "    secrets:" {
			inGrant = true
			continue
		}
		if !inGrant {
			continue
		}
		if entry, ok := strings.CutPrefix(line, "      - "); ok {
			grants = append(grants, entry)
			continue
		}
		inGrant = false
	}

	return grants
}

func isMappingKeyAtIndent(line string, indent int) bool {
	prefix := strings.Repeat(" ", indent)
	return strings.HasPrefix(line, prefix) &&
		!strings.HasPrefix(line, prefix+" ") &&
		strings.HasSuffix(line, ":")
}

func TestGenerateComposeSecrets(t *testing.T) {
	t.Parallel()

	secrets := []SecretSpec{
		{Name: "123", Environment: "NUMERIC_TOKEN"},
		{Name: "gh", Environment: "GH_TOKEN"},
		{Name: "tls-key", File: "/etc/jailoc-secrets/tls.pem"},
	}

	rendered := renderSecrets(t, secrets)

	t.Run("top level block", func(t *testing.T) {
		t.Parallel()

		assertContains(t, rendered, "\nsecrets:\n")
		assertContains(t, rendered, "\n  \"123\":\n    environment: \"NUMERIC_TOKEN\"")
		assertContains(t, rendered, "\n  \"gh\":\n    environment: \"GH_TOKEN\"")
		assertContains(t, rendered, "\n  \"tls-key\":\n    file: \"/etc/jailoc-secrets/tls.pem\"")
	})

	t.Run("numeric name is a quoted string key", func(t *testing.T) {
		t.Parallel()

		assertContains(t, rendered, "  \"123\":")
		assertNotContains(t, rendered, "\n  123:")
		assertNotContains(t, rendered, "\n      - 123\n")
	})

	t.Run("opencode service grant", func(t *testing.T) {
		t.Parallel()

		assertContains(t, rendered, "\n    secrets:\n      - \"123\"\n      - \"gh\"\n      - \"tls-key\"\n")
	})

	t.Run("only opencode is granted", func(t *testing.T) {
		t.Parallel()

		assertContains(t, rendered, "  dind:")

		got := servicesWithSecretsGrant(t, rendered)
		want := []string{"opencode"}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("expected only the opencode service to carry a secrets grant, got %v in:\n%s", got, rendered)
		}
	})

	t.Run("grant invariant", func(t *testing.T) {
		t.Parallel()

		keys := topLevelSecretKeys(t, rendered)
		grants := serviceSecretGrants(t, rendered)

		want := []string{"\"123\"", "\"gh\"", "\"tls-key\""}
		if !reflect.DeepEqual(keys, want) {
			t.Fatalf("top-level secret keys = %v, want %v", keys, want)
		}
		if !reflect.DeepEqual(grants, want) {
			t.Fatalf("opencode secret grants = %v, want %v", grants, want)
		}
	})
}

func TestGenerateComposeSecretsFileOnly(t *testing.T) {
	t.Parallel()

	rendered := renderSecrets(t, []SecretSpec{
		{Name: "only-file", File: "/host/secrets/token"},
	})

	assertContains(t, rendered, "\n  \"only-file\":\n    file: \"/host/secrets/token\"\n")
	assertNotContains(t, rendered, "environment: \"")
}

func TestGenerateComposeNoSecrets(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		secrets []SecretSpec
	}{
		{name: "nil", secrets: nil},
		{name: "empty", secrets: []SecretSpec{}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			rendered := renderSecrets(t, tt.secrets)

			assertNotContains(t, rendered, "\nsecrets:")
			assertNotContains(t, rendered, "    secrets:")

			if got := servicesWithSecretsGrant(t, rendered); len(got) != 0 {
				t.Fatalf("expected no service secrets grant, got %v", got)
			}
			if got := topLevelSecretKeys(t, rendered); len(got) != 0 {
				t.Fatalf("expected no top-level secrets, got %v", got)
			}
		})
	}
}

func TestGenerateComposeSecretsDeterministic(t *testing.T) {
	t.Parallel()

	params := secretsParams([]SecretSpec{
		{Name: "123", Environment: "NUMERIC_TOKEN"},
		{Name: "gh", Environment: "GH_TOKEN"},
		{Name: "tls-key", File: "/etc/jailoc-secrets/tls.pem"},
	})

	first, err := GenerateCompose(params)
	if err != nil {
		t.Fatalf("GenerateCompose returned error: %v", err)
	}

	for i := range 20 {
		out, err := GenerateCompose(params)
		if err != nil {
			t.Fatalf("GenerateCompose returned error on iteration %d: %v", i, err)
		}
		if string(out) != string(first) {
			t.Fatalf("render %d differs from the first render;\nfirst:\n%s\ngot:\n%s", i, first, out)
		}
	}
}

func TestSecretSpecCarriesNoValue(t *testing.T) {
	t.Parallel()

	var got []string
	specType := reflect.TypeOf(SecretSpec{})
	for i := range specType.NumField() {
		got = append(got, specType.Field(i).Name)
	}

	want := []string{"Name", "Environment", "File"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("SecretSpec fields = %v, want %v; a secret VALUE must never be carried into compose rendering", got, want)
	}
}
