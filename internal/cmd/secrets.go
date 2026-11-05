package cmd

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"slices"
	"strings"
	"time"

	"github.com/seznam/jailoc/internal/config"
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
		if s.TTL > 0 {
			return fmt.Sprintf("%s (%s from_command %q, ttl %s)", s.Name, destWord, s.FromCommand, s.TTL)
		}
		return fmt.Sprintf("%s (%s from_command %q)", s.Name, destWord, s.FromCommand)
	default:
		return fmt.Sprintf("%s (%s from_file %s)", s.Name, destWord, s.FromFile)
	}
}

// secretCommandTimeout bounds a from_command run, which may wait for the
// user to unlock a password manager.
const secretCommandTimeout = 2 * time.Minute

type secretKeyring interface {
	get(account string) ([]byte, error)
	set(account string, data []byte, ttl time.Duration) error
	delete(account string) error
}

var (
	secretCache     secretKeyring = defaultSecretKeyring
	secretCacheTime               = time.Now
)

type cachedSecretEnvelope struct {
	Value     []byte    `json:"value"`
	ExpiresAt time.Time `json:"expires_at"`
}

func secretCacheAccount(wsName string, s workspace.ResolvedSecret) string {
	kind := "file"
	if s.Kind == workspace.SecretKindEnv {
		kind = "env"
	}
	h := sha256.New()
	_, _ = h.Write([]byte(config.ConfigPath()))
	_, _ = h.Write([]byte{0})
	_, _ = h.Write([]byte(wsName))
	_, _ = h.Write([]byte{0})
	_, _ = h.Write([]byte(kind))
	_, _ = h.Write([]byte{0})
	_, _ = h.Write([]byte(s.Name))
	_, _ = h.Write([]byte{0})
	_, _ = h.Write([]byte(s.FromCommand))
	return "jailoc:secret:" + hex.EncodeToString(h.Sum(nil))
}

func secretCacheIndexAccount(wsName string) string {
	h := sha256.New()
	_, _ = h.Write([]byte(config.ConfigPath()))
	_, _ = h.Write([]byte{0})
	_, _ = h.Write([]byte(wsName))
	return "jailoc:index:" + hex.EncodeToString(h.Sum(nil))
}

func recordSecretCacheAccount(wsName, account string, ttl time.Duration) {
	idxAccount := secretCacheIndexAccount(wsName)
	var accounts []string
	if raw, err := secretCache.get(idxAccount); err == nil {
		_ = json.Unmarshal(raw, &accounts)
	}
	if !slices.Contains(accounts, account) {
		accounts = append(accounts, account)
	}
	data, err := json.Marshal(accounts)
	if err != nil {
		return
	}
	// Keep the workspace index alive at least as long as the newly cached secret.
	_ = secretCache.set(idxAccount, data, max(ttl, 24*time.Hour))
}

func readCachedSecret(wsName string, s workspace.ResolvedSecret) ([]byte, bool) {
	raw, err := secretCache.get(secretCacheAccount(wsName, s))
	if err != nil {
		return nil, false
	}
	var env cachedSecretEnvelope
	if err := json.Unmarshal(raw, &env); err != nil {
		return nil, false
	}
	if !secretCacheTime().Before(env.ExpiresAt) || len(env.Value) == 0 {
		_ = secretCache.delete(secretCacheAccount(wsName, s))
		return nil, false
	}
	return env.Value, true
}

func writeCachedSecret(wsName string, s workspace.ResolvedSecret, value []byte) {
	env := cachedSecretEnvelope{
		Value:     value,
		ExpiresAt: secretCacheTime().Add(s.TTL),
	}
	data, err := json.Marshal(env)
	if err != nil {
		return
	}
	account := secretCacheAccount(wsName, s)
	if err := secretCache.set(account, data, s.TTL); err == nil {
		recordSecretCacheAccount(wsName, account, s.TTL)
	}
}

func purgeWorkspaceSecretCache(ws *workspace.Resolved) {
	if ws == nil {
		return
	}
	idxAccount := secretCacheIndexAccount(ws.Name)
	if raw, err := secretCache.get(idxAccount); err == nil {
		var accounts []string
		if json.Unmarshal(raw, &accounts) == nil {
			for _, account := range accounts {
				_ = secretCache.delete(account)
			}
		}
	}
	_ = secretCache.delete(idxAccount)
	for _, s := range ws.Secrets {
		if s.FromCommand != "" {
			_ = secretCache.delete(secretCacheAccount(ws.Name, s))
		}
	}
}

// readSecret returns the value of s from its host source. A from_command
// runs with the terminal attached and JAILOC_WORKSPACE set to wsName, so
// password managers can prompt and commands can scope credentials per
// workspace. When s.TTL > 0 and purgeCache is false, valid cached command
// outputs in the system keychain are reused. Env secret values lose their
// trailing line break and must fit on one line.
func readSecret(ctx context.Context, wsName string, s workspace.ResolvedSecret, purgeCache bool) ([]byte, error) {
	var (
		value     []byte
		rawToSave []byte
	)
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
		if s.TTL > 0 {
			if purgeCache {
				_ = secretCache.delete(secretCacheAccount(wsName, s))
			} else if cached, ok := readCachedSecret(wsName, s); ok {
				value = cached
			}
		}
		if value == nil {
			ctx, cancel := context.WithTimeout(ctx, secretCommandTimeout)
			defer cancel()
			cmd := exec.CommandContext(ctx, "/bin/sh", "-c", s.FromCommand) //nolint:gosec // G204: the user's own configured command
			cmd.Env = append(os.Environ(), "JAILOC_WORKSPACE="+wsName)
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
			if s.TTL > 0 {
				rawToSave = out
			}
		}
	}
	if s.Kind == workspace.SecretKindEnv {
		value = bytes.TrimRight(value, "\r\n")
		if bytes.ContainsAny(value, "\r\n\x00") {
			return nil, fmt.Errorf("env secret %q: value spans several lines or contains NUL; use a file secret", s.Name)
		}
	}
	if rawToSave != nil {
		writeCachedSecret(wsName, s, rawToSave)
	}
	return value, nil
}
