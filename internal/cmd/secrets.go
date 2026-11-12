package cmd

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"slices"
	"strings"
	"time"

	"github.com/fatih/color"
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
const (
	secretCommandTimeout       = 2 * time.Minute
	secretCommandWaitDelay     = 200 * time.Millisecond
	defaultSecretCommandNotice = 2 * time.Second
)

// errSecretNotCached is returned by secretKeyring.get for a missing entry.
var errSecretNotCached = errors.New("secret is not cached")

// secretKeyring is the host keychain. delete succeeds for a missing entry.
type secretKeyring interface {
	get(ctx context.Context, account string) ([]byte, error)
	set(ctx context.Context, account string, data []byte, ttl time.Duration) error
	delete(ctx context.Context, account string) error
}

var (
	secretCache         secretKeyring = defaultSecretKeyring
	secretCacheTime                   = time.Now
	secretCommandNotice               = defaultSecretCommandNotice
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

// secretCacheIndex maps the cached accounts of a workspace to their expiry,
// so a purge finds secrets whose configuration changed or was removed.
type secretCacheIndex map[string]time.Time

func readSecretCacheIndex(ctx context.Context, wsName string) (secretCacheIndex, error) {
	raw, err := secretCache.get(ctx, secretCacheIndexAccount(wsName))
	if errors.Is(err, errSecretNotCached) {
		return secretCacheIndex{}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read secret cache index: %w", err)
	}
	idx := secretCacheIndex{}
	if err := json.Unmarshal(raw, &idx); err != nil {
		return nil, fmt.Errorf("parse secret cache index: %w", err)
	}
	return idx, nil
}

// indexExpiryMargin keeps the workspace index alive past the kernel keyring's
// whole-second upward rounding of entry timeouts, and indexRetryTTL keeps a
// nonempty index whose entries have all expired (because deleting an entry
// failed) so a later purge retries them.
const (
	indexExpiryMargin = time.Second
	indexRetryTTL     = 24 * time.Hour
)

// writeSecretCacheIndex stores idx until its latest expiry plus
// indexExpiryMargin, so it outlives every secret it lists, or deletes it when
// it is empty.
func writeSecretCacheIndex(ctx context.Context, wsName string, idx secretCacheIndex) error {
	account := secretCacheIndexAccount(wsName)
	if len(idx) == 0 {
		return secretCache.delete(ctx, account)
	}
	var latest time.Time
	for _, expires := range idx {
		if expires.After(latest) {
			latest = expires
		}
	}
	ttl := max(latest.Sub(secretCacheTime())+indexExpiryMargin, indexRetryTTL)
	data, err := json.Marshal(idx)
	if err != nil {
		return fmt.Errorf("encode secret cache index: %w", err)
	}
	return secretCache.set(ctx, account, data, ttl)
}

// recordSecretCacheAccount lists account in the workspace index until
// expires and drops entries that expired and could be deleted. The caller
// holds the workspace's secret cache lock.
func recordSecretCacheAccount(ctx context.Context, wsName, account string, expires time.Time) error {
	idx, err := readSecretCacheIndex(ctx, wsName)
	if err != nil {
		return err
	}
	now := secretCacheTime()
	for a, exp := range idx {
		// The kernel keyring drops expired entries by itself, the macOS
		// keychain keeps them until they are deleted.
		if !now.Before(exp) && secretCache.delete(ctx, a) == nil {
			delete(idx, a)
		}
	}
	if expires.After(idx[account]) {
		idx[account] = expires
	}
	return writeSecretCacheIndex(ctx, wsName, idx)
}

func readCachedSecret(ctx context.Context, wsName string, s workspace.ResolvedSecret) ([]byte, bool) {
	raw, err := secretCache.get(ctx, secretCacheAccount(wsName, s))
	if err != nil {
		return nil, false
	}
	var env cachedSecretEnvelope
	if err := json.Unmarshal(raw, &env); err != nil {
		return nil, false
	}
	if !secretCacheTime().Before(env.ExpiresAt) || len(env.Value) == 0 {
		return nil, false
	}
	return env.Value, true
}

// writeCachedSecret caches value until s.TTL elapses. The account is listed
// in the workspace index before it is written, so a purge finds every cached
// secret. Caching is best effort: on failure the command runs again next
// time.
func writeCachedSecret(wsName string, s workspace.ResolvedSecret, value []byte) {
	ctx := context.Background()
	_ = withSecretCacheLock(ctx, wsName, func() error {
		writeCachedSecretLocked(ctx, wsName, s, value)
		return nil
	})
}

func writeCachedSecretLocked(ctx context.Context, wsName string, s workspace.ResolvedSecret, value []byte) {
	expires := secretCacheTime().Add(s.TTL)
	data, err := json.Marshal(cachedSecretEnvelope{Value: value, ExpiresAt: expires})
	if err != nil {
		return
	}
	account := secretCacheAccount(wsName, s)
	if err := recordSecretCacheAccount(ctx, wsName, account, expires); err != nil {
		return
	}
	if ttl := expires.Sub(secretCacheTime()); ttl > 0 {
		_ = secretCache.set(ctx, account, data, ttl)
	}
}

// purgeWorkspaceSecretCache deletes the cached secrets listed in the
// workspace index and those of the configured from_command secrets. Entries
// that can't be deleted stay in the index, so a later purge retries them.
func purgeWorkspaceSecretCache(ctx context.Context, ws *workspace.Resolved) error {
	if ws == nil {
		return nil
	}
	err := withSecretCacheLock(ctx, ws.Name, func() error {
		genErr := bumpSecretCacheGeneration(ws.Name)
		idx, idxErr := readSecretCacheIndex(ctx, ws.Name)
		pending := secretCacheIndex{}
		for a, exp := range idx {
			pending[a] = exp
		}
		for _, s := range ws.Secrets {
			if a := secretCacheAccount(ws.Name, s); s.FromCommand != "" && pending[a].IsZero() {
				pending[a] = secretCacheTime().Add(s.TTL)
			}
		}
		errs := []error{genErr, idxErr}
		for a := range pending {
			if err := secretCache.delete(ctx, a); err != nil {
				errs = append(errs, err)
				continue
			}
			delete(pending, a)
		}
		// An unreadable index is kept: rewriting it would lose its entries.
		if idxErr == nil {
			errs = append(errs, writeSecretCacheIndex(ctx, ws.Name, pending))
		}
		return errors.Join(errs...)
	})
	if err != nil {
		return fmt.Errorf("purge cached secrets of workspace %q: %w", ws.Name, err)
	}
	return nil
}

// readSecret returns the value of s from its host source. A from_command
// runs with the terminal attached and JAILOC_WORKSPACE set to wsName, so
// password managers can prompt and commands can scope credentials per
// workspace. When s.TTL > 0 and purgeCache is false, valid cached command
// outputs in the system keychain are reused. Env secret values lose their
// trailing line break and must fit on one line.
func readSecret(ctx context.Context, wsName string, s workspace.ResolvedSecret, purgeCache bool) ([]byte, error) {
	switch {
	case s.FromEnv != "":
		return checkSecretValue(s, []byte(os.Getenv(s.FromEnv)))
	case s.FromFile != "":
		data, err := os.ReadFile(s.FromFile)
		if err != nil {
			return nil, fmt.Errorf("read secret %q: %w", s.Name, err)
		}
		return checkSecretValue(s, data)
	default:
		if s.TTL <= 0 {
			raw, err := runSecretCommand(ctx, wsName, s)
			if err != nil {
				return nil, err
			}
			return checkSecretValue(s, raw)
		}
		var (
			value  []byte
			hit    bool
			gen    uint64
			genErr error
		)
		if err := withSecretCacheLock(ctx, wsName, func() error {
			if purgeCache {
				if err := bumpSecretCacheGeneration(wsName); err != nil {
					return fmt.Errorf("secret %q: %w", s.Name, err)
				}
				if err := secretCache.delete(ctx, secretCacheAccount(wsName, s)); err != nil {
					return fmt.Errorf("secret %q: evict cached value: %w", s.Name, err)
				}
			} else if cached, ok := readCachedSecret(ctx, wsName, s); ok {
				var err error
				value, err = checkSecretValue(s, cached)
				hit = true
				return err
			}
			gen, genErr = secretCacheGeneration(wsName)
			return nil
		}); err != nil || hit {
			return value, err
		}
		raw, err := runSecretCommand(ctx, wsName, s)
		if err != nil {
			return nil, err
		}
		value, err = checkSecretValue(s, raw)
		if err != nil {
			return nil, err
		}
		if genErr == nil {
			_ = withSecretCacheLock(ctx, wsName, func() error {
				if cur, err := secretCacheGeneration(wsName); err == nil && cur == gen {
					writeCachedSecretLocked(ctx, wsName, s, raw)
				}
				return nil
			})
		}
		return value, nil
	}
}

func runSecretCommand(ctx context.Context, wsName string, s workspace.ResolvedSecret) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, secretCommandTimeout)
	defer cancel()
	if secretCommandNotice > 0 {
		timer := time.AfterFunc(secretCommandNotice, func() {
			if ctx.Err() == nil {
				_, _ = color.New(color.FgCyan).Fprintf(os.Stderr, "Waiting for secret %s from_command (workspace %s)...\n", escapeVisible(s.Name), wsName)
			}
		})
		defer timer.Stop()
	}
	cmd := exec.CommandContext(ctx, "/bin/sh", "-c", s.FromCommand) //nolint:gosec // G204: the user's own configured command
	cmd.WaitDelay = secretCommandWaitDelay
	cmd.Env = slices.DeleteFunc(os.Environ(), func(e string) bool {
		return strings.HasPrefix(e, "JAILOC_WORKSPACE=")
	})
	cmd.Env = append(cmd.Env, "JAILOC_WORKSPACE="+wsName)
	cmd.Stdin = os.Stdin
	cmd.Stderr = os.Stderr
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("secret %q: from_command %q: %w", s.Name, s.FromCommand, err)
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("secret %q: from_command %q printed nothing", s.Name, s.FromCommand)
	}
	return out, nil
}

func checkSecretValue(s workspace.ResolvedSecret, value []byte) ([]byte, error) {
	if s.Kind == workspace.SecretKindEnv {
		value = bytes.TrimRight(value, "\r\n")
		if bytes.ContainsAny(value, "\r\n\x00") {
			return nil, fmt.Errorf("env secret %q: value spans several lines or contains NUL; use a file secret", s.Name)
		}
	}
	return value, nil
}
