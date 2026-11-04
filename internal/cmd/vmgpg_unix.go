//go:build darwin || linux

package cmd

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"strings"

	"github.com/fatih/color"

	"github.com/seznam/jailoc/internal/workspace"
)

// vmGPGAgent returns the host gpg-agent extra socket to relay into the
// workspace VM, or "" when gpg_agent is off or no agent is available. The
// extra socket is the one gpg-agent offers to remote machines: it refuses key
// management and secret key export.
func vmGPGAgent(ctx context.Context, ws *workspace.Resolved) string {
	if !ws.GPGAgent {
		return ""
	}
	sock, err := gpgExtraSocket(ctx)
	if err == nil && !isSocket(sock) {
		// gpg-agent starts on demand, so the socket may not exist yet.
		if out, lerr := exec.CommandContext(ctx, "gpgconf", "--launch", "gpg-agent").CombinedOutput(); lerr != nil {
			err = fmt.Errorf("gpgconf --launch gpg-agent: %w: %s", lerr, bytes.TrimSpace(out))
		} else if !isSocket(sock) {
			err = fmt.Errorf("%s is not a socket", sock)
		}
	}
	if err != nil {
		_, _ = color.New(color.FgYellow).Fprintf(os.Stderr, "WARNING: gpg_agent is enabled but the host gpg-agent is not available (%v); the workspace starts without one\n", err)
		return ""
	}
	return sock
}

// gpgExtraSocket asks gpgconf for the path of the gpg-agent extra socket.
func gpgExtraSocket(ctx context.Context) (string, error) {
	out, err := exec.CommandContext(ctx, "gpgconf", "--list-dirs", "agent-extra-socket").Output()
	if err != nil {
		return "", fmt.Errorf("gpgconf --list-dirs agent-extra-socket: %w", err)
	}
	// gpgconf percent-escapes special characters such as ':' in paths.
	sock, err := url.PathUnescape(strings.TrimSpace(string(out)))
	if err != nil || !strings.HasPrefix(sock, "/") {
		return "", fmt.Errorf("gpgconf returned an invalid socket path %q", strings.TrimSpace(string(out)))
	}
	return sock, nil
}

func isSocket(path string) bool {
	info, err := os.Stat(path) //nolint:gosec // G703: path from the user's own gpgconf
	return err == nil && info.Mode()&os.ModeSocket != 0
}

// gpgPublicKeys exports the public keys gpg in the VM needs to sign with the
// forwarded agent. Failures are warnings: the agent works without them, only
// gpg cannot find the signing key.
func gpgPublicKeys(ctx context.Context) []byte {
	keys, err := exportGPGPublicKeys(ctx)
	if err != nil {
		_, _ = color.New(color.FgYellow).Fprintf(os.Stderr, "WARNING: gpg_agent: %v; no public keys are imported into the VM\n", err)
		return nil
	}
	return keys
}

// exportGPGPublicKeys exports the public key named by user.signingKey in the
// global Git configuration, or the public keys of all host secret keys when
// it is not set or names no GPG key (for example an SSH key).
func exportGPGPublicKeys(ctx context.Context) ([]byte, error) {
	if key := gitSigningKey(ctx); key != "" {
		out, err := gpgOutput(ctx, "--batch", "--export", "--", key)
		if err != nil {
			return nil, err
		}
		if len(out) > 0 {
			return out, nil
		}
	}
	list, err := gpgOutput(ctx, "--batch", "--with-colons", "--list-secret-keys")
	if err != nil {
		return nil, err
	}
	fprs := secretKeyFingerprints(list)
	if len(fprs) == 0 {
		return nil, errors.New("the host keyring has no secret keys")
	}
	return gpgOutput(ctx, append([]string{"--batch", "--export", "--"}, fprs...)...)
}

// gitSigningKey reads user.signingKey from the global Git configuration. It
// runs in the home directory, never in a workspace repository the agent
// controls.
func gitSigningKey(ctx context.Context) string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	cmd := exec.CommandContext(ctx, "git", "config", "--global", "--includes", "--get", "user.signingKey")
	cmd.Dir = home
	out, err := cmd.Output()
	if err != nil {
		// Exit status 1: the key is not set.
		return ""
	}
	return strings.TrimSpace(string(out))
}

func gpgOutput(ctx context.Context, args ...string) ([]byte, error) {
	var stderr bytes.Buffer
	cmd := exec.CommandContext(ctx, "gpg", args...) //nolint:gosec // G204: fixed gpg subcommands; key IDs follow "--"
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("gpg %s: %w: %s", strings.Join(args, " "), err, bytes.TrimSpace(stderr.Bytes()))
	}
	return out, nil
}

// secretKeyFingerprints returns the primary key fingerprints in the output of
// gpg --with-colons --list-secret-keys: the fpr record following each sec
// record.
func secretKeyFingerprints(list []byte) []string {
	var out []string
	primary := false
	for line := range strings.Lines(string(list)) {
		fields := strings.Split(strings.TrimRight(line, "\r\n"), ":")
		switch fields[0] {
		case "sec":
			primary = true
		case "fpr":
			if primary && len(fields) > 9 && fields[9] != "" {
				out = append(out, fields[9])
			}
			primary = false
		default:
			primary = false
		}
	}
	return out
}
