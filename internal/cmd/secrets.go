package cmd

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/seznam/jailoc/internal/workspace"
)

func validateSecretSources(ws *workspace.Resolved) error {
	envKeys := envKeySet(ws.Env)

	for _, s := range ws.Secrets {
		switch {
		case s.FromEnv != "":
			if err := validateSecretEnvSource(s); err != nil {
				return err
			}
		case s.FromFile != "":
			if err := validateSecretFileSource(s); err != nil {
				return err
			}
		}

		// Names are unique by construction: workspace.Resolve keys secrets by
		// name, so only a collision with a plain env entry is possible here.
		if s.Kind == workspace.SecretKindEnv && envKeys[s.Name] {
			return fmt.Errorf("env secret %q collides with an env entry", s.Name)
		}
	}

	return nil
}

func validateSecretEnvSource(s workspace.ResolvedSecret) error {
	value, ok := os.LookupEnv(s.FromEnv)
	if !ok {
		return fmt.Errorf("secret %q: host environment variable %q is not set", s.Name, s.FromEnv)
	}
	if value == "" {
		return fmt.Errorf("secret %q: host environment variable %q is set but empty", s.Name, s.FromEnv)
	}
	return nil
}

func validateSecretFileSource(s workspace.ResolvedSecret) error {
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
// source REFERENCE (host variable name, host path or command) and never
// resolves it, so no secret value can reach stdout.
func secretReference(s workspace.ResolvedSecret) string {
	destWord := "file"
	if s.Kind == workspace.SecretKindEnv {
		destWord = "env"
	}
	switch {
	case s.FromEnv != "":
		return fmt.Sprintf("%s (%s from_env %s)", s.Name, destWord, s.FromEnv)
	case s.FromCommand != "":
		return fmt.Sprintf("%s (%s from_command %q)", s.Name, destWord, s.FromCommand)
	default:
		return fmt.Sprintf("%s (%s from_file %s)", s.Name, destWord, s.FromFile)
	}
}

// secretCommandTimeout bounds a from_command run, which may wait for the
// user to unlock a password manager.
const secretCommandTimeout = 2 * time.Minute

// readSecret returns the value of s from its host source. A from_command
// runs with the terminal attached, so password managers can prompt. Env
// secret values lose their trailing line break and must fit on one line.
func readSecret(ctx context.Context, s workspace.ResolvedSecret) ([]byte, error) {
	var value []byte
	switch {
	case s.FromEnv != "":
		value = []byte(os.Getenv(s.FromEnv))
	case s.FromFile != "":
		data, err := os.ReadFile(s.FromFile)
		if err != nil {
			return nil, fmt.Errorf("read secret %q: %w", s.Name, err)
		}
		value = data
	default:
		ctx, cancel := context.WithTimeout(ctx, secretCommandTimeout)
		defer cancel()
		cmd := exec.CommandContext(ctx, "/bin/sh", "-c", s.FromCommand) //nolint:gosec // G204: the user's own configured command
		cmd.Stdin = os.Stdin
		cmd.Stderr = os.Stderr
		out, err := cmd.Output()
		if err != nil {
			return nil, fmt.Errorf("secret %q: from_command %q: %w", s.Name, s.FromCommand, err)
		}
		if len(out) == 0 {
			return nil, fmt.Errorf("secret %q: from_command %q printed nothing", s.Name, s.FromCommand)
		}
		value = out
	}
	if s.Kind == workspace.SecretKindEnv {
		value = bytes.TrimRight(value, "\r\n")
		if bytes.ContainsAny(value, "\r\n\x00") {
			return nil, fmt.Errorf("env secret %q: value spans several lines or contains NUL; use a file secret", s.Name)
		}
	}
	return value, nil
}
