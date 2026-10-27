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
	"golang.org/x/sys/unix"

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
		if err := addHostsEntry("/etc/hosts", cfg.Hostname); err != nil {
			logger.Warn("add hostname to /etc/hosts", "error", err)
		}
	}
	if err := netcfg.Configure(logger); err != nil {
		return fmt.Errorf("network: %w", err)
	}
	// Containers and other guest-side routing (Docker bridge networks)
	// need forwarding; it does not reach past the broker's policy.
	if err := harden.Set("net.ipv4.ip_forward", "1"); err != nil {
		logger.Warn("enable ip forwarding", "error", err)
	}
	acct, err := ensureAccount(cfg)
	if err != nil {
		return err
	}
	if err := ownedDir(acct.home, acct, 0o755); err != nil {
		return err
	}
	if err := mountShares(logger, cfg.Shares, acct); err != nil {
		return err
	}
	if err := bindCopies(cfg.Copies, acct); err != nil {
		return err
	}
	if err := protectPaths(cfg.Pins, cfg.ReadOnly); err != nil {
		return err
	}
	// SSH_AUTH_SOCK and NODE_USE_SYSTEM_CA reach sessions through EnvFile,
	// which the host rewrites when the settings change on a reload.
	env := sessionEnv(cfg)
	for _, dir := range []string{SecretsDir, filepath.Dir(EnvFile)} {
		if err := ownedDir(dir, acct, 0o700); err != nil {
			return err
		}
	}
	live := &liveState{logger: logger, acct: acct}
	if err := live.apply(Reload{CABundle: cfg.CABundle, SSHAgent: cfg.SSHAgent}); err != nil {
		return err
	}

	// By default the agent may become root through sudo: the VM is the
	// isolation boundary, and network policy and share permissions are
	// enforced on the host. Guest-side protections (Pins, ReadOnly) only
	// hold while sessions cannot gain privileges.
	harden.KernelDefaults(logger)
	var docker *dockerDaemon
	if cfg.Docker != nil {
		if docker, err = startDocker(logger, cfg.Docker, acct); err != nil {
			return err
		}
	}
	if cfg.NoPrivilegeEscalation {
		if err := disablePrivilegeEscalation(); err != nil {
			return err
		}
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
		live.poll()
		if _, err := os.Stat(filepath.Join(ControlDir, "shutdown")); err == nil {
			logger.Info("shutdown requested")
			srv.Close()
			docker.stop(logger)
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
// mountShares mounts the virtio-fs shares at their guest paths. Missing
// mount points and parents inside the agent's home belong to the agent, so
// the agent can still create siblings such as ~/.config/<tool>.
func mountShares(logger *slog.Logger, shares []Share, acct account) error {
	sorted := slices.Clone(shares)
	slices.SortStableFunc(sorted, func(a, b Share) int { return len(a.GuestPath) - len(b.GuestPath) })
	for _, s := range sorted {
		if err := makeParents(s.GuestPath, acct); err != nil {
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

// bindCopies binds every staged copy read-only at its destination.
// Missing parent directories are created; those inside the agent's home
// belong to the agent.
func bindCopies(copies []Copy, acct account) error {
	for _, c := range copies {
		info, err := os.Stat(c.Source)
		if err != nil {
			return fmt.Errorf("copy %s: %w", c.Target, err)
		}
		if err := makeParents(filepath.Dir(c.Target), acct); err != nil {
			return fmt.Errorf("copy %s: %w", c.Target, err)
		}
		if err := mountPoint(c.Target, info.IsDir()); err != nil {
			return fmt.Errorf("copy %s: %w", c.Target, err)
		}
		if err := syscall.Mount(c.Source, c.Target, "", syscall.MS_BIND, ""); err != nil {
			return fmt.Errorf("copy %s: bind: %w", c.Target, err)
		}
		flags := uintptr(syscall.MS_BIND | syscall.MS_REMOUNT | syscall.MS_RDONLY | syscall.MS_NOSUID | syscall.MS_NODEV)
		if err := syscall.Mount("", c.Target, "", flags, ""); err != nil {
			return fmt.Errorf("copy %s: remount read-only: %w", c.Target, err)
		}
	}
	return nil
}

// disablePrivilegeEscalation sets no_new_privs and drops every capability
// the SSH server does not need from the bounding set, on all threads of the
// process: prctl is per thread, and the Go runtime starts sessions from
// arbitrary threads. With no_new_privs, sudo and other setuid programs no
// longer grant root to sessions.
func disablePrivilegeEscalation() error {
	keep := map[uintptr]bool{unix.CAP_SETUID: true, unix.CAP_SETGID: true, unix.CAP_NET_BIND_SERVICE: true}
	last := uintptr(unix.CAP_LAST_CAP)
	if data, err := os.ReadFile("/proc/sys/kernel/cap_last_cap"); err == nil {
		if n, err := strconv.ParseUint(strings.TrimSpace(string(data)), 10, 8); err == nil {
			last = uintptr(n)
		}
	}
	for c := uintptr(0); c <= last; c++ {
		if keep[c] {
			continue
		}
		if _, _, errno := syscall.AllThreadsSyscall(syscall.SYS_PRCTL, unix.PR_CAPBSET_DROP, c, 0); errno != 0 {
			return fmt.Errorf("drop capability %d from the bounding set: %w", c, errno)
		}
	}
	if _, _, errno := syscall.AllThreadsSyscall(syscall.SYS_PRCTL, unix.PR_SET_NO_NEW_PRIVS, 1, 0); errno != 0 {
		return fmt.Errorf("set no_new_privs: %w", errno)
	}
	return nil
}

// protectPaths binds every pin onto itself, which makes it a mount point
// that cannot be renamed or removed, then binds every read-only path onto
// itself read-only. Pins are bound parents first so that later binds land
// inside them.
func protectPaths(pins, readOnly []string) error {
	pins = slices.Clone(pins)
	slices.SortFunc(pins, func(a, b string) int { return len(a) - len(b) })
	for _, p := range pins {
		if err := syscall.Mount(p, p, "", syscall.MS_BIND, ""); err != nil {
			return fmt.Errorf("pin %s: %w", p, err)
		}
	}
	for _, p := range readOnly {
		if err := syscall.Mount(p, p, "", syscall.MS_BIND, ""); err != nil {
			return fmt.Errorf("protect %s: bind: %w", p, err)
		}
		flags := uintptr(syscall.MS_BIND | syscall.MS_REMOUNT | syscall.MS_RDONLY | syscall.MS_NOSUID | syscall.MS_NODEV)
		if err := syscall.Mount("", p, "", flags, ""); err != nil {
			return fmt.Errorf("protect %s: remount read-only: %w", p, err)
		}
	}
	return nil
}

// makeParents creates dir and its missing parents with mode 0755.
func makeParents(dir string, acct account) error {
	var missing []string
	for d := filepath.Clean(dir); ; d = filepath.Dir(d) {
		if _, err := os.Lstat(d); err == nil {
			break
		}
		missing = append(missing, d)
		if d == filepath.Dir(d) {
			break
		}
	}
	for i := len(missing) - 1; i >= 0; i-- {
		d := missing[i]
		if err := os.Mkdir(d, 0o755); err != nil && !os.IsExist(err) { //nolint:gosec // G301: destination parents are ordinary directories
			return fmt.Errorf("create %s: %w", d, err)
		}
		if within(d, acct.home) {
			if err := chown(d, acct); err != nil {
				return err
			}
		}
	}
	return nil
}

// mountPoint makes sure path exists as a directory or a regular file.
func mountPoint(path string, dir bool) error {
	info, err := os.Lstat(path)
	switch {
	case err == nil && info.IsDir() != dir:
		return fmt.Errorf("destination exists as a different file type")
	case err == nil:
		return nil
	case !os.IsNotExist(err):
		return err
	case dir:
		return os.Mkdir(path, 0o755) //nolint:gosec // G301: mount point, replaced by the bind mount
	default:
		f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY, 0o644) //nolint:gosec // G302,G304: mount point, replaced by the bind mount
		if err != nil {
			return err
		}
		return f.Close()
	}
}

func within(path, dir string) bool {
	return path == dir || strings.HasPrefix(path, dir+"/")
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
	// A reload delivers the complete set: secrets no longer configured go.
	if entries, err := os.ReadDir(SecretsDir); err == nil {
		for _, e := range entries {
			if _, keep := s.Files[e.Name()]; !keep && !strings.HasPrefix(e.Name(), ".tmp-") {
				_ = os.Remove(filepath.Join(SecretsDir, e.Name()))
			}
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
	env = lastWins(append(env, extraEnv...))
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
