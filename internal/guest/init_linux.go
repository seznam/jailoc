//go:build linux

package guest

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"time"

	"golang.org/x/crypto/ssh"

	"github.com/stacklok/go-microvm/guest/harden"
	"github.com/stacklok/go-microvm/guest/mount"
	"github.com/stacklok/go-microvm/guest/netcfg"
	"github.com/stacklok/go-microvm/guest/reaper"
	"github.com/stacklok/go-microvm/guest/sshd"
)

const (
	tmpSizeMiB    = 1024
	mountRetries  = 20
	shutdownPoll  = 500 * time.Millisecond
	systemCA      = "/etc/ssl/certs/ca-certificates.crt"
	fallbackShell = "/bin/sh"
)

// account is the resolved agent user.
type account struct {
	name  string
	uid   uint32
	gid   uint32
	home  string
	shell string
}

// Init prepares the guest and serves SSH sessions until the host requests a
// shutdown. It returns when the VM should power off.
func Init(logger *slog.Logger) error {
	cfg, err := loadConfig()
	if err != nil {
		return err
	}
	if err := mount.Essential(logger, tmpSizeMiB); err != nil {
		return fmt.Errorf("essential mounts: %w", err)
	}
	if cfg.Hostname != "" {
		if err := syscall.Sethostname([]byte(cfg.Hostname)); err != nil {
			logger.Warn("set hostname", "error", err)
		}
	}
	if err := netcfg.Configure(logger); err != nil {
		return fmt.Errorf("network: %w", err)
	}
	if err := mountShares(logger, cfg.Shares); err != nil {
		return err
	}
	acct, err := ensureAccount(cfg)
	if err != nil {
		return err
	}
	if err := prepareHome(logger, cfg, acct); err != nil {
		return err
	}
	env := sessionEnv(cfg)
	if cfg.CABundle != "" {
		if err := installCABundle(cfg.CABundle); err != nil {
			return err
		}
		env = append(env, "NODE_USE_SYSTEM_CA=1")
	}
	for _, dir := range []string{SecretsDir, filepath.Dir(EnvFile)} {
		if err := ownedDir(dir, acct, 0o700); err != nil {
			return err
		}
	}

	harden.KernelDefaults(logger)
	if err := harden.DropBoundingCaps(harden.CapSetUID, harden.CapSetGID, harden.CapNetBindService); err != nil {
		return fmt.Errorf("drop capabilities: %w", err)
	}
	if err := harden.SetNoNewPrivs(); err != nil {
		return fmt.Errorf("set no_new_privs: %w", err)
	}

	srv, err := newSSHServer(logger, cfg, acct, env)
	if err != nil {
		return err
	}
	ln, err := net.Listen("tcp", fmt.Sprintf(":%d", SSHPort))
	if err != nil {
		return fmt.Errorf("listen on port %d: %w", SSHPort, err)
	}
	go func() {
		if err := srv.Serve(ln); err != nil {
			logger.Error("ssh server", "error", err)
		}
	}()
	stopReaper := reaper.Start(logger)
	defer stopReaper()
	logger.Info("jailoc guest ready", "user", acct.name, "workdir", cfg.WorkDir)

	for {
		if _, err := os.Stat(filepath.Join(ControlDir, "shutdown")); err == nil {
			logger.Info("shutdown requested")
			srv.Close()
			syscall.Sync()
			return nil
		}
		time.Sleep(shutdownPoll)
	}
}

func loadConfig() (*Config, error) {
	b, err := os.ReadFile(ConfigPath)
	if err != nil {
		return nil, fmt.Errorf("read guest config: %w", err)
	}
	var cfg Config
	if err := json.Unmarshal(b, &cfg); err != nil {
		return nil, fmt.Errorf("parse guest config: %w", err)
	}
	return &cfg, nil
}

// mountShares mounts the virtio-fs shares, parents before children.
func mountShares(logger *slog.Logger, shares []Share) error {
	sorted := slices.Clone(shares)
	slices.SortStableFunc(sorted, func(a, b Share) int { return len(a.GuestPath) - len(b.GuestPath) })
	for _, s := range sorted {
		if err := os.MkdirAll(s.GuestPath, 0o755); err != nil { //nolint:gosec // G301: mount points are traversed by the agent
			return fmt.Errorf("create mount point %s: %w", s.GuestPath, err)
		}
		flags := uintptr(syscall.MS_NOSUID | syscall.MS_NODEV)
		if s.ReadOnly {
			flags |= syscall.MS_RDONLY
		}
		var err error
		for range mountRetries {
			if err = syscall.Mount(s.Tag, s.GuestPath, "virtiofs", flags, ""); err == nil {
				break
			}
			time.Sleep(250 * time.Millisecond)
		}
		if err != nil {
			return fmt.Errorf("mount share %s on %s: %w", s.Tag, s.GuestPath, err)
		}
		logger.Info("mounted share", "path", s.GuestPath, "read_only", s.ReadOnly)
	}
	return nil
}

// ensureAccount returns the passwd entry with the configured UID, adding
// one when the image has none.
func ensureAccount(cfg *Config) (account, error) {
	f, err := os.Open("/etc/passwd")
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return account{}, fmt.Errorf("read /etc/passwd: %w", err)
	}
	if err == nil {
		defer func() { _ = f.Close() }()
		sc := bufio.NewScanner(f)
		for sc.Scan() {
			fields := strings.Split(sc.Text(), ":")
			if len(fields) < 7 || fields[2] != strconv.FormatUint(uint64(cfg.UID), 10) {
				continue
			}
			gid, err := strconv.ParseUint(fields[3], 10, 32)
			if err != nil {
				gid = uint64(cfg.GID)
			}
			acct := account{name: fields[0], uid: cfg.UID, gid: uint32(gid), home: fields[5], shell: fields[6]}
			if acct.shell == "" {
				acct.shell = fallbackShell
			}
			return acct, nil
		}
	}
	acct := account{name: cfg.User, uid: cfg.UID, gid: cfg.GID, home: cfg.Home, shell: fallbackShell}
	if _, err := os.Stat("/bin/bash"); err == nil {
		acct.shell = "/bin/bash"
	}
	if err := appendLine("/etc/passwd", fmt.Sprintf("%s:x:%d:%d:jailoc agent:%s:%s", acct.name, acct.uid, acct.gid, acct.home, acct.shell)); err != nil {
		return account{}, err
	}
	if err := appendLine("/etc/group", fmt.Sprintf("%s:x:%d:", acct.name, acct.gid)); err != nil {
		return account{}, err
	}
	return acct, nil
}

func appendLine(path, line string) error {
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644) //nolint:gosec // G302: /etc/passwd and /etc/group are world-readable
	if err != nil {
		return fmt.Errorf("open %s: %w", path, err)
	}
	if _, err := fmt.Fprintln(f, line); err != nil {
		_ = f.Close()
		return fmt.Errorf("write %s: %w", path, err)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("close %s: %w", path, err)
	}
	return nil
}

// prepareHome creates the home directory and installs the Git config.
func prepareHome(logger *slog.Logger, cfg *Config, acct account) error {
	if err := ownedDir(acct.home, acct, 0o755); err != nil {
		return err
	}
	if cfg.GitConfig == "" {
		return nil
	}
	b, err := os.ReadFile(cfg.GitConfig)
	if err != nil {
		logger.Warn("git config not available", "path", cfg.GitConfig, "error", err)
		return nil
	}
	dst := filepath.Join(acct.home, ".gitconfig")
	if err := os.WriteFile(dst, b, 0o644); err != nil { //nolint:gosec // G306: Git config is readable by design
		return fmt.Errorf("write %s: %w", dst, err)
	}
	return chown(dst, acct)
}

func ownedDir(path string, acct account, mode os.FileMode) error {
	if err := os.MkdirAll(path, mode); err != nil {
		return fmt.Errorf("create %s: %w", path, err)
	}
	if err := os.Chmod(path, mode); err != nil {
		return fmt.Errorf("chmod %s: %w", path, err)
	}
	return chown(path, acct)
}

func chown(path string, acct account) error {
	if err := os.Lchown(path, int(acct.uid), int(acct.gid)); err != nil {
		return fmt.Errorf("chown %s: %w", path, err)
	}
	return nil
}

// installCABundle appends the forwarded bundle to the system trust store.
func installCABundle(path string) error {
	b, err := os.ReadFile(path) //nolint:gosec // G304: path from the host-written guest config
	if err != nil {
		return fmt.Errorf("read CA bundle %s: %w", path, err)
	}
	if len(b) == 0 {
		return fmt.Errorf("CA bundle %s is empty", path)
	}
	f, err := os.OpenFile(systemCA, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644) //nolint:gosec // G302: the system CA bundle is world-readable
	if err != nil {
		return fmt.Errorf("open %s: %w", systemCA, err)
	}
	if _, err := f.Write(append([]byte("\n"), b...)); err != nil {
		_ = f.Close()
		return fmt.Errorf("append CA bundle: %w", err)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("close %s: %w", systemCA, err)
	}
	return nil
}

// sessionEnv is the base environment of every session: the image
// environment that libkrun passes to init, plus the configured entries.
func sessionEnv(cfg *Config) []string {
	var env []string
	for _, kv := range os.Environ() {
		name, _, _ := strings.Cut(kv, "=")
		switch {
		case strings.HasPrefix(name, "KRUN_"), name == "HOME", name == "USER", name == "SHELL", name == "TERM", name == "HOSTNAME":
			continue
		}
		env = append(env, kv)
	}
	return append(env, cfg.Env...)
}

func newSSHServer(logger *slog.Logger, cfg *Config, acct account, env []string) (*sshd.Server, error) {
	key, _, _, _, err := ssh.ParseAuthorizedKey([]byte(cfg.AuthorizedKey))
	if err != nil {
		return nil, fmt.Errorf("parse authorized key: %w", err)
	}
	hostPEM, err := os.ReadFile(HostKeyPath)
	if err != nil {
		return nil, fmt.Errorf("read host key: %w", err)
	}
	_ = os.Remove(HostKeyPath)
	hostKey, err := ssh.ParsePrivateKey(hostPEM)
	if err != nil {
		return nil, fmt.Errorf("parse host key: %w", err)
	}
	srv, err := sshd.New(sshd.Config{
		Port:           SSHPort,
		AuthorizedKeys: []ssh.PublicKey{key},
		Env:            env,
		DefaultUID:     acct.uid,
		DefaultGID:     acct.gid,
		DefaultUser:    acct.name,
		DefaultHome:    acct.home,
		DefaultShell:   acct.shell,
		DefaultWorkDir: cfg.WorkDir,
		HostKey:        hostKey,
		Logger:         logger,
	})
	if err != nil {
		return nil, fmt.Errorf("create ssh server: %w", err)
	}
	return srv, nil
}

// WriteSecrets stores the secrets payload read from r: file secrets in
// SecretsDir and environment entries in EnvFile. It runs as the agent user.
func WriteSecrets(r io.Reader) error {
	var s Secrets
	if err := json.NewDecoder(io.LimitReader(r, 16<<20)).Decode(&s); err != nil {
		return fmt.Errorf("decode secrets: %w", err)
	}
	for name, value := range s.Files {
		if name == "" || strings.ContainsAny(name, "/\x00") || name == "." || name == ".." {
			return fmt.Errorf("invalid secret name %q", name)
		}
		if err := writeAtomic(filepath.Join(SecretsDir, name), value, 0o400); err != nil {
			return err
		}
	}
	var b strings.Builder
	for _, kv := range s.Env {
		if strings.ContainsAny(kv, "\n\x00") || !strings.Contains(kv, "=") {
			return fmt.Errorf("invalid environment entry")
		}
		b.WriteString(kv)
		b.WriteByte('\n')
	}
	return writeAtomic(EnvFile, []byte(b.String()), 0o600)
}

func writeAtomic(path string, data []byte, mode os.FileMode) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), ".tmp-")
	if err != nil {
		return fmt.Errorf("create %s: %w", path, err)
	}
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmp.Name())
		return fmt.Errorf("write %s: %w", path, err)
	}
	if err := tmp.Chmod(mode); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmp.Name())
		return fmt.Errorf("chmod %s: %w", path, err)
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmp.Name())
		return fmt.Errorf("close %s: %w", path, err)
	}
	if err := os.Rename(tmp.Name(), path); err != nil {
		_ = os.Remove(tmp.Name())
		return fmt.Errorf("install %s: %w", path, err)
	}
	return nil
}

// Exec replaces the process with args run in dir, adding the entries of
// EnvFile and then extraEnv to the environment.
func Exec(dir string, extraEnv, args []string) error {
	if len(args) == 0 {
		return errors.New("run: no command")
	}
	env := os.Environ()
	if b, err := os.ReadFile(EnvFile); err == nil {
		for line := range strings.SplitSeq(string(b), "\n") {
			if line != "" {
				env = append(env, line)
			}
		}
	}
	env = append(env, extraEnv...)
	if dir != "" {
		if err := os.Chdir(dir); err != nil {
			return fmt.Errorf("run: %w", err)
		}
	}
	path, err := exec.LookPath(args[0])
	if err != nil {
		return fmt.Errorf("run %s: %w", args[0], err)
	}
	return syscall.Exec(path, args, env) //nolint:gosec // G204: command chosen by the jailoc host
}
