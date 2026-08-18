package cmd

import (
	"fmt"
	"os"
	"strings"

	"github.com/seznam/jailoc/internal/compose"
	"github.com/seznam/jailoc/internal/config"
	"github.com/seznam/jailoc/internal/workspace"
)

// validateSecretSources performs the up-time checks that config.Validate cannot
// do: it inspects the live host environment and file system so that jailoc up
// and jailoc add fail fast instead of leaving the container to die on a missing
// /run/secrets/<name>.
//
// It is shared by runUp and maybeRestartWorkspace and MUST be called before any
// generated file is written (compose file, entrypoint, allowed-hosts and the
// secret-env manifest) so an invalid or colliding configuration can never leave
// a stale manifest behind.
//
// It never reads a secret value — only its metadata.
func validateSecretSources(ws *workspace.Resolved) error {
	envKeys := envKeySet(ws.Env)
	envSecretNames := make(map[string]bool, len(ws.Secrets))

	for _, s := range ws.Secrets {
		if s.FromEnv != "" {
			if err := validateSecretEnvSource(s); err != nil {
				return err
			}
		} else {
			if err := validateSecretFileExists(s); err != nil {
				return err
			}
			if s.Kind == workspace.SecretKindFile {
				if err := validateSecretFileWorldReadable(s); err != nil {
					return err
				}
			}
		}

		if s.Kind == workspace.SecretKindEnv {
			if envKeys[s.Name] {
				return fmt.Errorf("env secret %q collides with an env entry", s.Name)
			}
			if envSecretNames[s.Name] {
				return fmt.Errorf("duplicate env secret %q", s.Name)
			}
			envSecretNames[s.Name] = true
		}
	}

	return nil
}

// validateSecretEnvSource rejects a host environment variable that is unset or
// set-but-empty. Docker Compose silently omits an empty environment-sourced
// secret, so /run/secrets/<name> would simply not exist inside the container.
func validateSecretEnvSource(s workspace.ResolvedSecret) error {
	value, ok := os.LookupEnv(s.FromEnv)
	if !ok {
		return fmt.Errorf("secret %q: host environment variable %q is not set", s.Name, s.FromEnv)
	}
	if value == "" {
		return fmt.Errorf("secret %q: host environment variable %q is set but empty; Docker Compose omits empty secrets, so /run/secrets/%s would not exist", s.Name, s.FromEnv, s.Name)
	}
	return nil
}

func validateSecretFileExists(s workspace.ResolvedSecret) error {
	fi, err := os.Stat(s.FromFile)
	if err != nil {
		if os.IsNotExist(err) {
			return fmt.Errorf("secret %q: file %q does not exist", s.Name, s.FromFile)
		}
		return fmt.Errorf("secret %q: stat file %q: %w", s.Name, s.FromFile, err)
	}
	if !fi.Mode().IsRegular() {
		return fmt.Errorf("secret %q: file %q is not a regular file", s.Name, s.FromFile)
	}
	return nil
}

func validateSecretFileWorldReadable(s workspace.ResolvedSecret) error {
	fi, err := os.Stat(s.FromFile)
	if err != nil {
		return fmt.Errorf("secret %q: stat file %q: %w", s.Name, s.FromFile, err)
	}
	mode := fi.Mode().Perm()
	if mode&0o004 == 0 {
		return fmt.Errorf("secret %q: file %q is not world-readable (mode %04o); the agent runs as UID 1000 and this is a conservative check — run 'chmod o+r %s' to make it readable inside the container", s.Name, s.FromFile, mode, s.FromFile)
	}
	return nil
}

// envKeySet returns the set of KEY names from a resolved KEY=VALUE env slice.
// Entries without a separator or with an empty key are ignored — config
// validation already rejects them, so they cannot collide meaningfully.
func envKeySet(env []string) map[string]bool {
	keys := make(map[string]bool, len(env))
	for _, entry := range env {
		key, _, found := strings.Cut(entry, "=")
		if !found || key == "" {
			continue
		}
		keys[key] = true
	}
	return keys
}

// secretReference renders a secret for jailoc config output. It emits the
// source REFERENCE (host variable name or host path) and never resolves it, so
// no secret value can reach stdout.
func secretReference(s workspace.ResolvedSecret) string {
	destWord := "file"
	if s.Kind == workspace.SecretKindEnv {
		destWord = "env"
	}
	sourceWord := "file"
	value := s.FromFile
	if s.FromEnv != "" {
		sourceWord = "env"
		value = s.FromEnv
	}
	return fmt.Sprintf("%s (%s from_%s %s)", s.Name, destWord, sourceWord, value)
}

// secretSpecs maps resolved secrets onto compose secret sources. The order of
// ws.Secrets is preserved: workspace.Resolve already sorts it by Name, and that
// sort is the single source of ordering for the compose file, so re-sorting
// here would be redundant.
func secretSpecs(ws *workspace.Resolved) []compose.SecretSpec {
	if len(ws.Secrets) == 0 {
		return nil
	}
	specs := make([]compose.SecretSpec, 0, len(ws.Secrets))
	for _, s := range ws.Secrets {
		if s.FromEnv != "" {
			specs = append(specs, compose.SecretSpec{Name: s.Name, Environment: s.FromEnv})
		} else {
			specs = append(specs, compose.SecretSpec{Name: s.Name, File: s.FromFile})
		}
	}
	return specs
}

// secretEnvPairs returns the name/variable pairs for env secrets.
func secretEnvPairs(ws *workspace.Resolved) []config.SecretEnvPair {
	if len(ws.Secrets) == 0 {
		return nil
	}
	pairs := make([]config.SecretEnvPair, 0, len(ws.Secrets))
	for _, s := range ws.Secrets {
		if s.Kind == workspace.SecretKindEnv {
			pairs = append(pairs, config.SecretEnvPair{Name: s.Name, Var: s.Name})
		}
	}
	if len(pairs) == 0 {
		return nil
	}
	return pairs
}
