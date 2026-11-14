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
	"sync"
	"syscall"
	"time"

	"golang.org/x/crypto/ssh"
	"golang.org/x/sys/unix"

	"github.com/stacklok/go-microvm/guest/harden"
	"github.com/stacklok/go-microvm/guest/mount"
	"github.com/stacklok/go-microvm/guest/netcfg"
	"github.com/stacklok/go-microvm/guest/reaper"
	"github.com/stacklok/go-microvm/guest/sshd"
	"github.com/stacklok/go-microvm/net/topology"
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
	if cfg.RootShare == "" {
		return errors.New("guest config has no root share")
	}
	mapper, err := overlayRoot(logger, cfg)
	if err != nil {
		return fmt.Errorf("root filesystem: %w", err)
	}
	defer mapper.close()
	if err := remountAfterPivot(logger); err != nil {
		return err
	}
	if cfg.Hostname != "" {
		if err := syscall.Sethostname([]byte(cfg.Hostname)); err != nil {
			logger.Warn("set hostname", "error", err)
		}
		if err := addHostsEntry("/etc/hosts", "127.0.1.1", cfg.Hostname); err != nil {
			logger.Warn("add hostname to /etc/hosts", "error", err)
		}
	}
	for _, alias := range HostAliases {
		if err := addHostsEntry("/etc/hosts", topology.GatewayIP, alias); err != nil {
			logger.Warn("add host alias to /etc/hosts", "alias", alias, "error", err)
		}
	}
	if err := netcfg.Configure(logger); err != nil {
		return fmt.Errorf("network: %w", err)
	}
	if err := os.WriteFile("/sys/class/net/eth0/mtu", []byte(strconv.Itoa(MTU)), 0o644); err != nil { //nolint:gosec // G306: sysfs attribute
		logger.Warn("set MTU", "error", err)
	}
	if cfg.IPv6 {
		if err := configureIPv6(); err != nil {
			return fmt.Errorf("network IPv6: %w", err)
		}
	} else if err := harden.Set("net.ipv6.conf.eth0.disable_ipv6", "1"); err != nil {
		return fmt.Errorf("disable IPv6: %w", err)
	}
	// Containers and other guest-side routing (Docker bridge networks)
	// need forwarding; it does not reach past the broker's policy.
	if err := harden.Set("net.ipv4.ip_forward", "1"); err != nil {
		logger.Warn("enable ip forwarding", "error", err)
	}
	// As soon as the network is up: the clock starts from the whole-second RTC
	// and falls behind whenever the host sleeps.
	defer startClockSync(logger)()
	acct, err := ensureAccount(cfg)
	if err != nil {
		return err
	}
	if err := ownedDir(acct.home, acct, 0o755); err != nil {
		return err
	}
	if err := mountPrivate(); err != nil {
		return err
	}
	// Shares and internal binds nest in both directions (a share inside a
	// persistent home directory, a persistent directory inside a share), so
	// they are mounted in one order, parents before children.
	for _, m := range mountOrder(cfg.Shares, cfg.Binds) {
		var err error
		if m.share != nil {
			err = mountShare(logger, *m.share, acct, mapper)
		} else {
			err = bindInternal(*m.bind, acct)
		}
		if err != nil {
			return err
		}
	}
	live := &liveState{logger: logger, acct: acct, created: map[string]bool{}, hostPorts: map[uint16]func(){}}
	if err := live.setSnapshots(cfg.Snapshots); err != nil {
		return err
	}
	if err := protectPaths(cfg.Pins, cfg.ReadOnly); err != nil {
		return err
	}
	// SSH_AUTH_SOCK and NODE_USE_SYSTEM_CA reach sessions through EnvFile,
	// which the host rewrites when the settings change on a reload.
	env, zone := sessionEnv(cfg)
	for _, dir := range []string{SecretsDir, filepath.Dir(EnvFile)} {
		if err := ownedDir(dir, acct, 0o700); err != nil {
			return err
		}
	}
	if err := live.apply(Reload{CABundle: cfg.CABundle, SSHAgent: cfg.SSHAgent, GPGAgent: cfg.GPGAgent, HostPorts: cfg.HostPorts, Snapshots: cfg.Snapshots, TimeZone: zone}); err != nil {
		return err
	}

	if err := setupZram(logger); err != nil {
		logger.Warn("zram swap disabled", "error", err)
	}
	defer startReclaim(logger)()

	// By default the agent may become root through sudo: the VM is the
	// isolation boundary, and network policy and share permissions are
	// enforced on the host. Guest-side protections (Pins, ReadOnly) only
	// hold while sessions cannot gain privileges.
	harden.KernelDefaults(logger)
	// Record process starts before anything else runs; a kernel without the
	// proc connector only loses the log.
	if stopExecLog, err := startExecLog(logger); err != nil {
		logger.Warn("exec log disabled", "error", err)
	} else {
		defer stopExecLog()
	}
	// Started before the SSH server, so it sees every session start.
	if stopOOMGuard, err := startOOMGuard(logger); err != nil {
		logger.Warn("agent OOM protection disabled", "error", err)
	} else {
		defer stopOOMGuard()
	}
	if stopOOMLog, err := startOOMLog(logger); err != nil {
		logger.Warn("kernel OOM kills not logged", "error", err)
	} else {
		defer stopOOMLog()
	}
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
	rawLn, err := net.Listen("tcp", fmt.Sprintf(":%d", SSHPort))
	if err != nil {
		return fmt.Errorf("listen on port %d: %w", SSHPort, err)
	}
	ln := newWakeListener(rawLn)
	go func() {
		if err := srv.Serve(ln); err != nil {
			logger.Error("ssh server", "error", err)
		}
	}()
	stopReaper := reaper.Start(logger)
	defer stopReaper()
	logger.Info("jailoc guest ready", "user", acct.name, "workdir", cfg.WorkDir)

	ticker := time.NewTicker(shutdownPoll)
	defer ticker.Stop()
	for {
		live.poll()
		if _, err := os.Stat(filepath.Join(ControlDir, "shutdown")); err == nil {
			logger.Info("shutdown requested")
			srv.Close()
			docker.stop(logger)
			syscall.Sync()
			return nil
		}
		select {
		case <-ln.wake:
		case <-ticker.C:
		}
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

// mountShare mounts the virtio-fs share s at its guest path. Missing mount
// points and parents inside the agent's home belong to the agent, so the
// agent can still create siblings such as ~/.config/<tool>.
func mountShare(logger *slog.Logger, s Share, acct account, mapper *idmapper) error {
	if err := makeParents(s.GuestPath, acct); err != nil {
		return fmt.Errorf("create mount point %s: %w", s.GuestPath, err)
	}
	var err error
	for range mountRetries {
		if err = mapper.mountShare(s.Tag, s.GuestPath, s.ReadOnly); err == nil {
			break
		}
		time.Sleep(250 * time.Millisecond)
	}
	if err != nil {
		return fmt.Errorf("mount share %s on %s: %w", s.Tag, s.GuestPath, err)
	}
	logger.Info("mounted share", "path", s.GuestPath, "read_only", s.ReadOnly)
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
	// An image user with the configured name but another UID (the built-in
	// image's agent is 1000; Linux hosts use the host user's IDs) is
	// renumbered rather than duplicated, so names resolve to one account.
	if renumbered, err := renumberAccount(cfg); err != nil {
		return account{}, err
	} else if renumbered != nil {
		return *renumbered, nil
	}
	if err := appendLine("/etc/passwd", fmt.Sprintf("%s:x:%d:%d:jailoc agent:%s:%s", acct.name, acct.uid, acct.gid, acct.home, acct.shell)); err != nil {
		return account{}, err
	}
	if err := appendLine("/etc/group", fmt.Sprintf("%s:x:%d:", acct.name, acct.gid)); err != nil {
		return account{}, err
	}
	return acct, nil
}

// renumberAccount gives the /etc/passwd entry named cfg.User the configured
// UID and GID, and the /etc/group entry of the same name the GID. It returns
// nil when no entry has that name.
func renumberAccount(cfg *Config) (*account, error) {
	var acct *account
	err := rewriteEntries("/etc/passwd", func(fields []string) bool {
		if len(fields) < 7 || fields[0] != cfg.User {
			return false
		}
		fields[2] = strconv.FormatUint(uint64(cfg.UID), 10)
		fields[3] = strconv.FormatUint(uint64(cfg.GID), 10)
		acct = &account{name: fields[0], uid: cfg.UID, gid: cfg.GID, home: fields[5], shell: fields[6]}
		if acct.shell == "" {
			acct.shell = fallbackShell
		}
		return true
	})
	if err != nil || acct == nil {
		return nil, err
	}
	if err := rewriteEntries("/etc/group", func(fields []string) bool {
		if len(fields) < 3 || fields[0] != cfg.User {
			return false
		}
		fields[2] = strconv.FormatUint(uint64(cfg.GID), 10)
		return true
	}); err != nil {
		return nil, err
	}
	return acct, nil
}

// rewriteEntries rewrites the colon-separated lines of path for which edit
// returns true. A missing file is left alone.
func rewriteEntries(path string, edit func(fields []string) bool) error {
	data, err := os.ReadFile(path) //nolint:gosec // G304: fixed system paths
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("read %s: %w", path, err)
	}
	lines := strings.Split(string(data), "\n")
	changed := false
	for i, line := range lines {
		fields := strings.Split(line, ":")
		if edit(fields) {
			lines[i] = strings.Join(fields, ":")
			changed = true
		}
	}
	if !changed {
		return nil
	}
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")), 0o644); err != nil { //nolint:gosec // G306: /etc/passwd and /etc/group are world-readable
		return fmt.Errorf("write %s: %w", path, err)
	}
	return nil
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

// mountPrivate mounts the root-only tmpfs that holds the internal share.
func mountPrivate() error {
	if err := os.MkdirAll(PrivateDir, 0o700); err != nil {
		return fmt.Errorf("create %s: %w", PrivateDir, err)
	}
	if err := syscall.Mount("tmpfs", PrivateDir, "tmpfs", syscall.MS_NOSUID|syscall.MS_NODEV|syscall.MS_NOEXEC, "mode=0700,size=1m"); err != nil {
		return fmt.Errorf("mount %s: %w", PrivateDir, err)
	}
	return nil
}

// bindInternal binds b, a directory of an internal share, into place.
// Missing parents inside the agent's home belong to the agent.
func bindInternal(b Bind, acct account) error {
	if (!within(b.Source, HostDir) && !within(b.Source, ReadOnlyHostDir)) || within(b.Source, ControlDir) {
		return fmt.Errorf("bind %s: source %s is outside the internal share", b.Target, b.Source)
	}
	if err := makeParents(b.Target, acct); err != nil {
		return fmt.Errorf("bind %s: %w", b.Target, err)
	}
	if err := syscall.Mount(b.Source, b.Target, "", syscall.MS_BIND, ""); err != nil {
		return fmt.Errorf("bind %s: %w", b.Target, err)
	}
	flags := uintptr(syscall.MS_BIND | syscall.MS_REMOUNT | syscall.MS_NOSUID | syscall.MS_NODEV)
	if b.ReadOnly {
		flags |= syscall.MS_RDONLY
	}
	if err := syscall.Mount("", b.Target, "", flags, ""); err != nil {
		return fmt.Errorf("bind %s: remount: %w", b.Target, err)
	}
	return nil
}

// bindSnapshot binds a staged snapshot read-only at its destination. It
// reports whether it created the mount point and returns a descriptor of the
// new mount, which unmountSnapshot takes to detach exactly this mount even if
// the destination path changes later. Missing parent directories are
// created; those inside the agent's home belong to the agent.
func bindSnapshot(c Snapshot, acct account) (bool, int, error) {
	info, err := os.Stat(c.Source)
	if err != nil {
		return false, -1, fmt.Errorf("snapshot %s: %w", c.Target, err)
	}
	if err := makeParents(filepath.Dir(c.Target), acct); err != nil {
		return false, -1, fmt.Errorf("snapshot %s: %w", c.Target, err)
	}
	_, err = os.Lstat(c.Target)
	created := os.IsNotExist(err)
	if err := mountPoint(c.Target, info.IsDir()); err != nil {
		return false, -1, fmt.Errorf("snapshot %s: %w", c.Target, err)
	}
	fd, err := bindReadOnly(c.Source, c.Target, info.IsDir())
	if err != nil {
		return created, -1, fmt.Errorf("snapshot %s: %w", c.Target, err)
	}
	return created, fd, nil
}

// bindReadOnly clones source as a detached read-only mount and attaches it at
// target. The final component of target must be a directory or regular file
// of the expected kind, not a symbolic link. It returns a descriptor of the
// attached mount's root.
func bindReadOnly(source, target string, dir bool) (int, error) {
	dst, err := unix.Open(target, unix.O_PATH|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return -1, fmt.Errorf("open destination: %w", err)
	}
	defer func() { _ = unix.Close(dst) }()
	var st unix.Stat_t
	if err := unix.Fstat(dst, &st); err != nil {
		return -1, fmt.Errorf("stat destination: %w", err)
	}
	want := uint32(unix.S_IFREG)
	if dir {
		want = unix.S_IFDIR
	}
	if st.Mode&unix.S_IFMT != want {
		return -1, fmt.Errorf("destination changed to a different file type")
	}
	fd, err := unix.OpenTree(unix.AT_FDCWD, source, unix.OPEN_TREE_CLONE|unix.OPEN_TREE_CLOEXEC)
	if err != nil {
		return -1, fmt.Errorf("clone %s: %w", source, err)
	}
	attr := unix.MountAttr{Attr_set: unix.MOUNT_ATTR_RDONLY | unix.MOUNT_ATTR_NOSUID | unix.MOUNT_ATTR_NODEV}
	if err := unix.MountSetattr(fd, "", unix.AT_EMPTY_PATH, &attr); err != nil {
		_ = unix.Close(fd)
		return -1, fmt.Errorf("make read-only: %w", err)
	}
	if err := unix.MoveMount(fd, "", dst, "", unix.MOVE_MOUNT_F_EMPTY_PATH|unix.MOVE_MOUNT_T_EMPTY_PATH); err != nil {
		_ = unix.Close(fd)
		return -1, fmt.Errorf("attach: %w", err)
	}
	return fd, nil
}

// unmountSnapshot lazily detaches the mount fd refers to and closes fd. A
// mount that is already gone is not an error.
func unmountSnapshot(fd int) error {
	defer func() { _ = unix.Close(fd) }()
	err := unix.Unmount("/proc/self/fd/"+strconv.Itoa(fd), unix.MNT_DETACH)
	if err != nil && !errors.Is(err, unix.EINVAL) {
		return err
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

// mountPoint makes sure path exists as a directory or a regular file. An
// existing symbolic link or special file is rejected.
func mountPoint(path string, dir bool) error {
	info, err := os.Lstat(path)
	switch {
	case err == nil && info.IsDir() != dir, err == nil && !dir && !info.Mode().IsRegular():
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
// environment that libkrun passes to init, plus the configured entries. It
// also returns the zone the guest manages through /etc/localtime: the
// configured one, or else the image's TZ when it names a zone the image has.
// The image's TZ is dropped whenever a zone is managed, at boot, so sessions
// follow /etc/localtime and a reload that changes the zone reaches them; only
// a TZ the guest cannot manage, such as a POSIX rule, is kept. A TZ in the
// configured entries still wins.
func sessionEnv(cfg *Config) ([]string, *TimeZone) {
	zone := cfg.TimeZone
	var env []string
	for _, kv := range os.Environ() {
		name, value, _ := strings.Cut(kv, "=")
		switch {
		case strings.HasPrefix(name, "KRUN_"), name == "HOME", name == "USER", name == "SHELL", name == "TERM", name == "HOSTNAME":
			continue
		case name == "TZ" && zone == nil:
			if tz := strings.TrimPrefix(value, ":"); ValidTimeZoneName(tz) && imageHasZone("/", tz) {
				zone = &TimeZone{Name: tz}
				continue
			}
		case name == "TZ":
			continue
		}
		env = append(env, kv)
	}
	return append(env, cfg.Env...), zone
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

// wakeListener wraps the SSH listener so a host connection (including the
// control nudge from WriteControl) wakes the control loop immediately, and
// Close closes active connections so srv.Close does not block on open sessions.
type wakeListener struct {
	net.Listener
	wake   chan struct{}
	mu     sync.Mutex
	closed bool
	conns  map[net.Conn]struct{}
}

func newWakeListener(ln net.Listener) *wakeListener {
	return &wakeListener{
		Listener: ln,
		wake:     make(chan struct{}, 1),
		conns:    map[net.Conn]struct{}{},
	}
}

func (l *wakeListener) Accept() (net.Conn, error) {
	c, err := l.Listener.Accept()
	if err != nil {
		return nil, err
	}
	l.mu.Lock()
	if l.closed {
		l.mu.Unlock()
		_ = c.Close()
		return nil, net.ErrClosed
	}
	l.conns[c] = struct{}{}
	l.mu.Unlock()
	select {
	case l.wake <- struct{}{}:
	default:
	}
	return &trackedConn{Conn: c, release: func() {
		l.mu.Lock()
		delete(l.conns, c)
		l.mu.Unlock()
	}}, nil
}

func (l *wakeListener) Close() error {
	err := l.Listener.Close()
	l.mu.Lock()
	l.closed = true
	for c := range l.conns {
		_ = c.Close()
	}
	l.mu.Unlock()
	return err
}

type trackedConn struct {
	net.Conn
	once    sync.Once
	release func()
}

func (c *trackedConn) Close() error {
	c.once.Do(c.release)
	return c.Conn.Close()
}

// WriteSecrets stores the secrets payload read from r: file secrets in
// SecretsDir and environment entries in EnvFile, and imports the forwarded
// GPG public keys. It runs as the agent user. A failed key import is reported
// on w and does not fail the delivery.
func WriteSecrets(r io.Reader, w io.Writer) error {
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
	if err := writeAtomic(EnvFile, []byte(b.String()), 0o600); err != nil {
		return err
	}
	if len(s.GPGPublicKeys) > 0 {
		importGPGKeys(s.GPGPublicKeys, w)
	}
	return nil
}

// importGPGKeys imports the public keys of the host's signing keys, so gpg in
// the VM can find the keys whose secret parts the forwarded agent holds.
func importGPGKeys(keys []byte, w io.Writer) {
	gpg, err := exec.LookPath("gpg")
	if err != nil {
		_, _ = fmt.Fprintln(w, "gpg_agent: gpg is not installed in the workspace image; the public keys are not imported")
		return
	}
	cmd := exec.Command(gpg, "--batch", "--quiet", "--no-autostart", "--import") //nolint:gosec // G204: gpg from PATH with fixed arguments
	cmd.Stdin = strings.NewReader(string(keys))
	if out, err := cmd.CombinedOutput(); err != nil {
		_, _ = fmt.Fprintf(w, "gpg_agent: import the public keys: %v: %s\n", err, strings.TrimSpace(string(out)))
	}
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

// programEnv returns the environment of a program run with extraEnv: the
// process environment, the entries of EnvFile and then extraEnv.
func programEnv(extraEnv []string) []string {
	env := os.Environ()
	if b, err := os.ReadFile(EnvFile); err == nil {
		for line := range strings.SplitSeq(string(b), "\n") {
			if line != "" {
				env = append(env, line)
			}
		}
	}
	return lastWins(append(env, extraEnv...))
}

// Exec replaces the process with args run in dir, adding the entries of
// EnvFile and then extraEnv to the environment.
func Exec(dir string, extraEnv, args []string) error {
	if len(args) == 0 {
		return errors.New("run: no command")
	}
	env := programEnv(extraEnv)
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
