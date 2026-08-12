package cmd

import (
	"fmt"
	"os"
	"sort"
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
	exposedBy := make(map[string]string, len(ws.Secrets))

	for _, s := range ws.Secrets {
		if s.Env != "" {
			if err := validateSecretEnvSource(s); err != nil {
				return err
			}
		}
		if s.File != "" {
			if err := validateSecretFileSource(s); err != nil {
				return err
			}
		}
		if s.ExposeEnv == "" {
			continue
		}
		if envKeys[s.ExposeEnv] {
			return fmt.Errorf("secret %q: expose_env %q collides with an env entry", s.Name, s.ExposeEnv)
		}
		if other, ok := exposedBy[s.ExposeEnv]; ok {
			return fmt.Errorf("secrets %q and %q both expose_env %q", other, s.Name, s.ExposeEnv)
		}
		exposedBy[s.ExposeEnv] = s.Name
	}

	return nil
}

// validateSecretEnvSource rejects a host environment variable that is unset or
// set-but-empty. Docker Compose silently omits an empty environment-sourced
// secret, so /run/secrets/<name> would simply not exist inside the container.
func validateSecretEnvSource(s workspace.ResolvedSecret) error {
	value, ok := os.LookupEnv(s.Env)
	if !ok {
		return fmt.Errorf("secret %q: host environment variable %q is not set", s.Name, s.Env)
	}
	if value == "" {
		return fmt.Errorf("secret %q: host environment variable %q is set but empty; Docker Compose omits empty secrets, so /run/secrets/%s would not exist", s.Name, s.Env, s.Name)
	}
	return nil
}

// validateSecretFileSource checks that a file-sourced secret exists, is a
// regular file, and is plausibly readable by the agent.
//
// The world-readable check is deliberately CONSERVATIVE: a file-sourced secret
// is a read-only bind mount that keeps its host ownership and mode, and the
// effective UID mapping inside the container is unknowable at up-time (it
// differs between macOS virtiofs and native Linux). When expose_env is set the
// check is skipped — the root entrypoint reads the file before setpriv drops to
// UID 1000, so host permissions for the agent user do not matter.
func validateSecretFileSource(s workspace.ResolvedSecret) error {
	fi, err := os.Stat(s.File)
	if err != nil {
		if os.IsNotExist(err) {
			return fmt.Errorf("secret %q: file %q does not exist", s.Name, s.File)
		}
		return fmt.Errorf("secret %q: stat file %q: %w", s.Name, s.File, err)
	}
	if !fi.Mode().IsRegular() {
		return fmt.Errorf("secret %q: file %q is not a regular file", s.Name, s.File)
	}
	mode := fi.Mode().Perm()
	if mode&0o004 == 0 && s.ExposeEnv == "" {
		return fmt.Errorf("secret %q: file %q is not world-readable (mode %04o); the agent runs as UID 1000 and this is a conservative check — run 'chmod o+r %s' or set expose_env", s.Name, s.File, mode, s.File)
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

// sortedSecretNames returns the secret names of a config-layer secrets map in
// deterministic order — Go map iteration order is random.
func sortedSecretNames(secrets map[string]config.Secret) []string {
	names := make([]string, 0, len(secrets))
	for name := range secrets {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// secretReference renders a secret for jailoc config output. It emits the
// source REFERENCE (host variable name or host path) and never resolves it, so
// no secret value can reach stdout.
func secretReference(name string, s config.Secret) string {
	var source string
	switch {
	case s.Env != "":
		source = fmt.Sprintf("env %s", s.Env)
	case s.File != "":
		source = fmt.Sprintf("file %s", s.File)
	default:
		source = "no source"
	}
	if s.ExposeEnv != "" {
		return fmt.Sprintf("%s (%s -> %s)", name, source, s.ExposeEnv)
	}
	return fmt.Sprintf("%s (%s)", name, source)
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
		specs = append(specs, compose.SecretSpec{
			Name:        s.Name,
			Environment: s.Env,
			File:        s.File,
		})
	}
	return specs
}

// secretEnvPairs returns the name/variable pairs for secrets that opt into
// environment exposure. WriteAllowedFiles writes them verbatim in this order,
// which is already sorted by Name via workspace.Resolve.
func secretEnvPairs(ws *workspace.Resolved) []config.SecretEnvPair {
	if len(ws.Secrets) == 0 {
		return nil
	}
	pairs := make([]config.SecretEnvPair, 0, len(ws.Secrets))
	for _, s := range ws.Secrets {
		if s.ExposeEnv == "" {
			continue
		}
		pairs = append(pairs, config.SecretEnvPair{Name: s.Name, Var: s.ExposeEnv})
	}
	if len(pairs) == 0 {
		return nil
	}
	return pairs
}
