package guest

import (
	"encoding/json"
	"errors"
	"fmt"
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
)

const (
	desktopLogDir      = "/var/log"
	desktopX11Dir      = "/tmp/.X11-unix"
	desktopNoVNCDir    = "/usr/share/novnc"
	desktopReadyWait   = 15 * time.Second
	desktopTermWait    = 5 * time.Second
	desktopPollEvery   = 25 * time.Millisecond
	desktopDialTimeout = 200 * time.Millisecond
)

// desktopSpec is one program of the desktop. ready, when set, reports nil
// once the program serves what the next one needs; without it the program
// only has to stay alive.
type desktopSpec struct {
	name  string
	argv  []string
	ready func() error
}

// desktopRun is how the desktop programs are started.
type desktopRun struct {
	env    []string
	dir    string
	logDir string
	// cred switches the programs to the agent user; nil keeps the caller's.
	cred      *syscall.Credential
	termWait  time.Duration
	readyWait time.Duration
	// started is called after each program starts (tests).
	started func(name string, pid int)
}

type desktopProc struct {
	name string
	pid  int
}

// desktop is the running desktop programs, in start order. Each leads its own
// session and process group. Their exit status is collected by the reaper,
// like dockerd's, so they are tracked by PID only.
type desktop struct {
	procs    []desktopProc
	termWait time.Duration
}

// desktopSpecs are the desktop programs in start order: the X display
// (Xtigervnc is the display itself, there is no Xvfb), the window manager on
// that display, and the websockify that serves noVNC for it. All of them
// listen on loopback only; the web relay is what the host reaches.
func desktopSpecs() []desktopSpec {
	return []desktopSpec{
		{
			name: "Xtigervnc",
			argv: []string{
				"Xtigervnc", DesktopDisplay,
				"-localhost",
				"-SecurityTypes", "None",
				"-rfbport", strconv.Itoa(VNCPort),
				"-nolisten", "tcp",
				// -localhost permits both loopback families and IPv6 is on
				// by default, so VNC would also listen on ::1. It is IPv4
				// only: websockify targets 127.0.0.1.
				"-UseIPv6=0",
				"-geometry", "1280x800",
				"-depth", "24",
			},
			ready: func() error {
				return dialReady("unix", filepath.Join(desktopX11Dir, "X"+strings.TrimPrefix(DesktopDisplay, ":")))
			},
		},
		// Openbox draws no background or panel, so the desktop would open as
		// an empty black screen. It starts an xterm once it runs; an image
		// without xterm still gets a working, empty desktop.
		{
			name:  "openbox",
			argv:  []string{"dbus-run-session", "--", "openbox", "--startup", "xterm"},
			ready: func() error { return procCommRunning("openbox") },
		},
		{
			name: "websockify",
			argv: []string{
				"websockify", "--web", desktopNoVNCDir,
				net.JoinHostPort("127.0.0.1", strconv.Itoa(DesktopPort)),
				net.JoinHostPort("127.0.0.1", strconv.Itoa(VNCPort)),
			},
			ready: func() error { return dialReady("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(DesktopPort))) },
		},
	}
}

func dialReady(network, addr string) error {
	c, err := net.DialTimeout(network, addr, desktopDialTimeout)
	if err != nil {
		return fmt.Errorf("dial %s %s: %w", network, addr, err)
	}
	return c.Close()
}

func procCommRunning(want string) error {
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return fmt.Errorf("read /proc: %w", err)
	}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		pid, err := strconv.Atoi(e.Name())
		if err != nil || pid <= 0 {
			continue
		}
		b, err := os.ReadFile(filepath.Join("/proc", e.Name(), "comm")) //nolint:gosec // G304: numeric /proc/<pid>/comm path
		if err != nil || strings.TrimSpace(string(b)) != want {
			continue
		}
		if leaderRunning(pid) {
			return nil
		}
	}
	return fmt.Errorf("process %s is not running", want)
}

// desktopEnv is the environment of the desktop programs: base (the session
// environment), the entries of envFile (EnvFile, where the host's current
// variables land) and the identity of the agent user, with DISPLAY forced to
// the desktop's display. It is a snapshot of boot time: secrets the host
// delivers after boot do not reach programs started now. Applications
// launched from the desktop therefore do not see them either; sessions
// started later carry the up-to-date environment.
func desktopEnv(base []string, envFile string, acct account) []string {
	env := append([]string{}, base...)
	env = append(env, readEnvFile(envFile)...)
	env = append(env,
		"HOME="+acct.home,
		"USER="+acct.name,
		"LOGNAME="+acct.name,
		"SHELL="+acct.shell,
		"DISPLAY="+DesktopDisplay,
	)
	return programLastWins(withDesktopDisplay(env))
}

// withDesktopDisplay returns env with DISPLAY set to the desktop's display,
// replacing any other value.
func withDesktopDisplay(env []string) []string {
	env = slices.DeleteFunc(env, func(kv string) bool { return strings.HasPrefix(kv, "DISPLAY=") })
	return append(env, "DISPLAY="+DesktopDisplay)
}

// desktopConfigured reports whether the guest configuration at path enables
// the desktop. The agent user can read it (the host writes it world-readable),
// so sessions use it to tell a desktop workspace. A configuration that is
// missing or does not parse has no desktop: the guest itself refuses to boot
// from a broken one.
func desktopConfigured(path string) bool {
	b, err := os.ReadFile(path) //nolint:gosec // G304: ConfigPath or a test path
	if err != nil {
		return false
	}
	var cfg struct {
		Desktop *Desktop `json:"desktop"`
	}
	return json.Unmarshal(b, &cfg) == nil && cfg.Desktop != nil
}

// startDesktop checks that the image has the desktop programs and starts them
// as the agent user. It runs after privilege hardening, so the desktop starts
// with the same restrictions as a session. A failure is returned, never
// swallowed: a workspace that asked for a desktop must not claim to have one.
func startDesktop(logger *slog.Logger, cfg *Config, acct account, base []string) (*desktop, error) {
	specs := desktopSpecs()
	var missing []string
	for _, s := range specs {
		if _, err := exec.LookPath(s.argv[0]); err != nil {
			missing = append(missing, s.argv[0])
		}
	}
	if len(missing) > 0 {
		return nil, fmt.Errorf("the workspace image lacks %s; build it from a desktop image", strings.Join(missing, ", "))
	}
	if fi, err := os.Stat(desktopNoVNCDir); err != nil || !fi.IsDir() {
		return nil, fmt.Errorf("the workspace image lacks %s; build it from a desktop image", desktopNoVNCDir)
	}
	// The X server's socket directory, as Xorg's wrappers create it.
	if err := os.MkdirAll(desktopX11Dir, 0o1777); err != nil { //nolint:gosec // G301: sticky world-writable like every X11 socket directory
		return nil, fmt.Errorf("create %s: %w", desktopX11Dir, err)
	}
	if err := os.Chmod(desktopX11Dir, os.ModeSticky|0o777); err != nil { //nolint:gosec // G302: see above
		return nil, fmt.Errorf("chmod %s: %w", desktopX11Dir, err)
	}
	dir := cfg.WorkDir
	if fi, err := os.Stat(dir); err != nil || !fi.IsDir() {
		dir = acct.home
	}
	return runDesktop(logger, specs, desktopRun{
		env:       desktopEnv(base, EnvFile, acct),
		dir:       dir,
		logDir:    desktopLogDir,
		cred:      &syscall.Credential{Uid: acct.uid, Gid: acct.gid},
		termWait:  desktopTermWait,
		readyWait: desktopReadyWait,
	})
}

// runDesktop starts specs in order, waiting for each to be ready before
// starting the next, and stops everything it started if one fails.
func runDesktop(logger *slog.Logger, specs []desktopSpec, run desktopRun) (*desktop, error) {
	d := &desktop{termWait: run.termWait}
	for _, s := range specs {
		pid, err := run.start(s)
		if err != nil {
			d.stop(logger)
			return nil, fmt.Errorf("%s: %w", s.name, err)
		}
		d.procs = append(d.procs, desktopProc{name: s.name, pid: pid})
		if run.started != nil {
			run.started(s.name, pid)
		}
		logger.Info("desktop program started", "program", s.name, "pid", pid)
		if err := run.waitReady(s, pid); err != nil {
			d.stop(logger)
			return nil, fmt.Errorf("%s: %w", s.name, err)
		}
	}
	for i, p := range d.procs {
		if !leaderRunning(p.pid) {
			d.stop(logger)
			return nil, fmt.Errorf("%s: exited during startup (see %s)", p.name, run.logPath(specs[i]))
		}
		if specs[i].ready != nil {
			if err := specs[i].ready(); err != nil {
				d.stop(logger)
				return nil, fmt.Errorf("%s: %w", p.name, err)
			}
		}
	}
	return d, nil
}

func (run desktopRun) logPath(s desktopSpec) string {
	return filepath.Join(run.logDir, "jailoc-desktop-"+s.name+".log")
}

func (run desktopRun) start(s desktopSpec) (int, error) {
	logPath := run.logPath(s)
	path, err := exec.LookPath(s.argv[0])
	if err != nil {
		return 0, fmt.Errorf("look up program: %w", err)
	}
	if err := os.MkdirAll(run.logDir, 0o755); err != nil { //nolint:gosec // G301: standard log directory
		return 0, fmt.Errorf("create log dir: %w", err)
	}
	logFile, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o640) //nolint:gosec // G304: fixed directory and program name
	if err != nil {
		return 0, fmt.Errorf("open log: %w", err)
	}
	defer func() { _ = logFile.Close() }()
	cmd := exec.Command(path, s.argv[1:]...) //nolint:gosec // G204: fixed programs and arguments
	cmd.Env = run.env
	cmd.Dir = run.dir
	cmd.Stdin = nil
	cmd.Stdout = logFile
	cmd.Stderr = logFile
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true, Credential: run.cred}
	if err := cmd.Start(); err != nil {
		return 0, fmt.Errorf("start: %w", err)
	}
	return cmd.Process.Pid, nil
}

// waitReady polls until s is ready, failing when its process exits or the
// wait runs out.
func (run desktopRun) waitReady(s desktopSpec, pid int) error {
	logPath := run.logPath(s)
	deadline := time.Now().Add(run.readyWait)
	var last error
	for {
		if !leaderRunning(pid) {
			return fmt.Errorf("exited during startup (see %s)", logPath)
		}
		if s.ready == nil {
			if time.Now().After(deadline) {
				return nil
			}
			time.Sleep(min(desktopPollEvery, time.Until(deadline)))
			if !leaderRunning(pid) {
				return fmt.Errorf("exited during startup (see %s)", logPath)
			}
			return nil
		}
		if last = s.ready(); last == nil {
			if !leaderRunning(pid) {
				return fmt.Errorf("exited during startup (see %s)", logPath)
			}
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("not ready after %s: %w", run.readyWait, last)
		}
		time.Sleep(desktopPollEvery)
	}
}

// stop sends TERM to the process group of every program, last started
// first, waits a bounded time for the leaders to exit and then kills every
// group, which also ends children that ignore TERM. It is safe to call more
// than once.
func (d *desktop) stop(logger *slog.Logger) {
	if d == nil || len(d.procs) == 0 {
		return
	}
	procs := d.procs
	d.procs = nil
	for i := len(procs) - 1; i >= 0; i-- {
		signalGroup(logger, procs[i], syscall.SIGTERM)
	}
	deadline := time.Now().Add(d.termWait)
	for _, p := range procs {
		for leaderRunning(p.pid) && time.Now().Before(deadline) {
			time.Sleep(10 * time.Millisecond)
		}
		if leaderRunning(p.pid) {
			logger.Warn("desktop program ignored TERM, killing", "program", p.name, "pid", p.pid)
		}
	}
	for i := len(procs) - 1; i >= 0; i-- {
		signalGroup(logger, procs[i], syscall.SIGKILL)
	}
}

// signalGroup signals the process group led by p. A group that is already
// gone is expected; any other failure is logged.
func signalGroup(logger *slog.Logger, p desktopProc, sig syscall.Signal) {
	if err := syscall.Kill(-p.pid, sig); err != nil && !errors.Is(err, syscall.ESRCH) {
		logger.Warn("signal desktop program", "program", p.name, "pid", p.pid, "signal", sig.String(), "error", err)
	}
}

// leaderRunning reports whether pid is a live process. A zombie is not live:
// until the reaper collects it, its PID still answers signals.
func leaderRunning(pid int) bool {
	state, err := processState(pid)
	return err == nil && state != 'Z'
}

// processState returns the state letter of pid in /proc/<pid>/stat.
func processState(pid int) (byte, error) {
	b, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat")
	if err != nil {
		return 0, fmt.Errorf("read state of process %d: %w", pid, err)
	}
	i := strings.LastIndexByte(string(b), ')')
	if i < 0 || i+2 >= len(b) {
		return 0, fmt.Errorf("state of process %d: malformed /proc stat", pid)
	}
	return b[i+2], nil
}
