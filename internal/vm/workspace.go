package vm

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	microvm "github.com/stacklok/go-microvm"
	"github.com/stacklok/go-microvm/image"
	"golang.org/x/crypto/ssh"

	"github.com/seznam/jailoc/internal/guest"
)

const (
	clientKeyFile  = "id_ed25519"
	hostPubKeyFile = "ssh_host_key.pub"
	internalTag    = "jailocint"
	internalROTag  = "jailocro"
	rootTag        = "jailocroot"
)

type fileContent struct {
	data []byte
	mode os.FileMode
}

// EnsureClientKey creates the SSH key jailoc uses for a workspace VM and
// returns its authorized_keys line.
func EnsureClientKey(stateDir string) (string, error) {
	path := filepath.Join(stateDir, clientKeyFile)
	if b, err := os.ReadFile(path); err == nil { //nolint:gosec // G304: jailoc-owned state path
		signer, err := ssh.ParsePrivateKey(b)
		if err != nil {
			return "", fmt.Errorf("parse %s: %w", path, err)
		}
		return string(ssh.MarshalAuthorizedKey(signer.PublicKey())), nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", fmt.Errorf("read %s: %w", path, err)
	}
	if err := os.MkdirAll(stateDir, 0o700); err != nil {
		return "", fmt.Errorf("create vm state dir: %w", err)
	}
	pemBytes, pub, err := newKey()
	if err != nil {
		return "", err
	}
	if err := os.WriteFile(path, pemBytes, 0o600); err != nil {
		return "", fmt.Errorf("write %s: %w", path, err)
	}
	return string(ssh.MarshalAuthorizedKey(pub)), nil
}

func newKey() ([]byte, ssh.PublicKey, error) {
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, nil, fmt.Errorf("generate ssh key: %w", err)
	}
	block, err := ssh.MarshalPrivateKey(priv, "")
	if err != nil {
		return nil, nil, fmt.Errorf("encode ssh key: %w", err)
	}
	signer, err := ssh.NewSignerFromKey(priv)
	if err != nil {
		return nil, nil, fmt.Errorf("ssh signer: %w", err)
	}
	return pem.EncodeToMemory(block), signer.PublicKey(), nil
}

// prepareGuest returns the files injected into the workspace root file
// system: jailoc-guest, its configuration and a fresh SSH host key whose
// public half is kept in the state directory for host key pinning.
func prepareGuest(spec Spec, layers []string) (map[string]fileContent, error) {
	if spec.Guest == nil || spec.GuestBinary == "" {
		return nil, errors.New("workspace vm spec has no guest configuration")
	}
	bin, err := os.ReadFile(spec.GuestBinary)
	if err != nil {
		return nil, fmt.Errorf("read jailoc-guest: %w", err)
	}
	authorized, err := EnsureClientKey(spec.StateDir)
	if err != nil {
		return nil, err
	}
	hostPEM, hostPub, err := newKey()
	if err != nil {
		return nil, err
	}
	if err := os.WriteFile(filepath.Join(spec.StateDir, hostPubKeyFile), ssh.MarshalAuthorizedKey(hostPub), 0o600); err != nil {
		return nil, fmt.Errorf("write host public key: %w", err)
	}
	cfg := *spec.Guest
	cfg.AuthorizedKey = authorized
	cfg.Shares = nil
	for i, s := range spec.Shares {
		cfg.Shares = append(cfg.Shares, guest.Share{Tag: shareTag(i), GuestPath: s.guestPath(), ReadOnly: s.ReadOnly})
	}
	cfg.Shares = append(cfg.Shares,
		guest.Share{Tag: internalTag, GuestPath: guest.HostDir},
		guest.Share{Tag: internalROTag, GuestPath: guest.ReadOnlyHostDir, ReadOnly: true})
	cfg.RootShare = rootTag
	cfg.RootLayers = layers
	cfgJSON, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("encode guest config: %w", err)
	}
	return map[string]fileContent{
		guest.BinaryPath:  {data: bin, mode: 0o755},
		guest.ConfigPath:  {data: cfgJSON, mode: 0o644},
		guest.HostKeyPath: {data: hostPEM, mode: 0o600},
	}, nil
}

func shareTag(i int) string { return fmt.Sprintf("jailoc%d", i) }

// simplifiedOwnership makes the agent (uid 1000) and root both see shared
// files as their own, whatever the host owner is. libkrun implements it only
// on macOS (its Linux passthrough backend ignores the semantics), and
// go-microvm rejects it on other hosts, where guests see host ownership.
const simplifiedOwnership = runtime.GOOS == "darwin"

// virtioIRQs is the number of interrupt lines libkrun gives virtio-mmio
// devices: lines 5-23 of the I/O APIC on x86_64 (with the runtime's
// IRQ_MAX patch) and SPIs 32-159 on arm64. Every share is a device.
var virtioIRQs = map[string]int{"amd64": 19, "arm64": 128}

// fixedDevices counts the virtio devices of a workspace VM besides its
// shares and Docker disk: balloon, rng, console, network, root filesystem,
// root image share, internal share and internal read-only share.
const fixedDevices = 8

// MaxShares returns how many shares a workspace VM on this host boots with.
// Beyond it libkrun runs out of interrupt lines for the devices.
func MaxShares(docker bool) int {
	return maxShares(runtime.GOARCH, docker)
}

func maxShares(arch string, docker bool) int {
	n := virtioIRQs[arch] - fixedDevices
	if docker {
		n--
	}
	return n
}

// workspaceOptions boots the workspace VM from an empty root holding files.
// The directory imageDir of the workspace image, with its EROFS layer
// images, is shared read-only, and the guest assembles its root as an
// overlay of them; env is the image's environment.
func workspaceOptions(spec Spec, files map[string]fileContent, imageDir string, env []string) []microvm.Option {
	mounts := make([]microvm.VirtioFSMount, 0, len(spec.Shares)+2)
	for i, s := range spec.Shares {
		mounts = append(mounts, microvm.VirtioFSMount{Tag: shareTag(i), HostPath: s.HostPath, ReadOnly: s.ReadOnly, SimplifiedOwnership: simplifiedOwnership})
	}
	mounts = append(mounts,
		microvm.VirtioFSMount{Tag: internalTag, HostPath: GuestDataDir(spec.StateDir), SimplifiedOwnership: simplifiedOwnership},
		microvm.VirtioFSMount{Tag: internalROTag, HostPath: GuestReadOnlyDir(spec.StateDir), ReadOnly: true, SimplifiedOwnership: simplifiedOwnership})
	opts := commonOptions(spec)
	if spec.Docker {
		// The only disk, so the guest sees it as /dev/vda (guest.DockerDisk).
		opts = append(opts, microvm.WithDisks(microvm.Disk{ID: "docker", HostPath: diskPath(spec.StateDir)}))
	}
	return append(opts,
		microvm.WithRootFSShareDir(rootTag, imageDir),
		microvm.WithEnv(imageEnv(env)...),
		microvm.WithVirtioFS(mounts...),
		microvm.WithInitOverride(guest.BinaryPath),
		microvm.WithRootFSHook(func(rootfs string, _ *image.OCIConfig) error {
			if err := os.MkdirAll(filepath.Join(rootfs, guest.Dir), 0o755); err != nil { //nolint:gosec // G301: the guest binary must be executable by the agent
				return fmt.Errorf("create guest dir: %w", err)
			}
			for path, f := range files {
				if err := os.WriteFile(filepath.Join(rootfs, path), f.data, f.mode); err != nil {
					return fmt.Errorf("write %s: %w", path, err)
				}
			}
			return nil
		}),
	)
}

// imageEnv returns the entries of an image environment go-microvm accepts
// for the init environment, which reaches the guest's sessions.
func imageEnv(env []string) []string {
	out := make([]string, 0, len(env))
	for _, kv := range env {
		if key, _, ok := strings.Cut(kv, "="); ok && key != "" && !strings.ContainsRune(kv, 0) {
			out = append(out, kv)
		}
	}
	return out
}

// DialSSH opens an SSH connection to a workspace VM through the broker's
// unix socket, pinning the host key the supervisor generated.
func DialSSH(ctx context.Context, stateDir string) (*ssh.Client, error) {
	keyPEM, err := os.ReadFile(filepath.Join(stateDir, clientKeyFile)) //nolint:gosec // G304: jailoc-owned state path
	if err != nil {
		return nil, fmt.Errorf("read ssh key: %w", err)
	}
	signer, err := ssh.ParsePrivateKey(keyPEM)
	if err != nil {
		return nil, fmt.Errorf("parse ssh key: %w", err)
	}
	hostLine, err := os.ReadFile(filepath.Join(stateDir, hostPubKeyFile)) //nolint:gosec // G304: jailoc-owned state path
	if err != nil {
		return nil, fmt.Errorf("read host key: %w", err)
	}
	hostKey, _, _, _, err := ssh.ParseAuthorizedKey(hostLine)
	if err != nil {
		return nil, fmt.Errorf("parse host key: %w", err)
	}
	var d net.Dialer
	conn, err := d.DialContext(ctx, "unix", SSHSocket(stateDir))
	if err != nil {
		return nil, fmt.Errorf("connect to workspace vm: %w", err)
	}
	if deadline, ok := ctx.Deadline(); ok {
		_ = conn.SetDeadline(deadline)
	}
	c, chans, reqs, err := ssh.NewClientConn(conn, "jailoc", &ssh.ClientConfig{
		User:            "agent",
		Auth:            []ssh.AuthMethod{ssh.PublicKeys(signer)},
		HostKeyCallback: ssh.FixedHostKey(hostKey),
		Timeout:         10 * time.Second,
	})
	if err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("ssh handshake: %w", err)
	}
	_ = conn.SetDeadline(time.Time{})
	return ssh.NewClient(c, chans, reqs), nil
}

// sshRetry is how long waitSSH waits between attempts. The guest answers within
// a few hundred milliseconds of boot, so a short interval saves most of a retry.
const sshRetry = 50 * time.Millisecond

// waitSSH waits until the guest SSH server completes a handshake.
func waitSSH(ctx context.Context, stateDir string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for {
		dctx, cancel := context.WithTimeout(ctx, 5*time.Second)
		c, err := DialSSH(dctx, stateDir)
		cancel()
		if err == nil {
			_ = c.Close()
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("guest ssh did not answer within %s: %w", timeout, err)
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("wait for guest ssh: %w", ctx.Err())
		case <-time.After(sshRetry):
		}
	}
}
