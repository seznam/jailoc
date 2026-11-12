package guest

import (
	"errors"
	"fmt"
	"log/slog"
	"os"
	"strconv"
	"strings"
	"sync"
	"syscall"

	"golang.org/x/sys/unix"
)

// oomRecvBuf is the receive buffer of the OOM guard's proc connector socket;
// a build that forks quickly must not overflow it.
const oomRecvBuf = 4 << 20

// startOOMGuard makes the agent the least preferred OOM victim among the
// processes of its session: when a session holder starts its program, the
// guard lowers the program's oom_score_adj, and resets it for every process
// the program forks. jailoc-guest init and its session processes are exempt
// from the OOM killer; processes init starts outside a session are reset.
// Only init can lower the values: sessions run as the agent user. Processes
// are followed through the kernel proc connector; a child keeps its
// parent's value from its fork until the guard handles the event.
func startOOMGuard(logger *slog.Logger) (stop func(), err error) {
	fd, err := unix.Socket(unix.AF_NETLINK, unix.SOCK_DGRAM|unix.SOCK_CLOEXEC, unix.NETLINK_CONNECTOR)
	if err != nil {
		return nil, fmt.Errorf("oom guard: netlink socket: %w", err)
	}
	// Port 0 lets the kernel pick one: the exec log holds the init PID.
	if err := unix.Bind(fd, &unix.SockaddrNetlink{Family: unix.AF_NETLINK, Groups: cnIdxProc}); err != nil {
		_ = unix.Close(fd)
		return nil, fmt.Errorf("oom guard: bind proc connector: %w", err)
	}
	if err := unix.SetsockoptInt(fd, unix.SOL_SOCKET, unix.SO_RCVBUFFORCE, oomRecvBuf); err != nil {
		logger.Warn("oom guard: enlarge receive buffer", "error", err)
	}
	if err := unix.Sendto(fd, procListenMessage(), 0, &unix.SockaddrNetlink{Family: unix.AF_NETLINK}); err != nil {
		_ = unix.Close(fd)
		return nil, fmt.Errorf("oom guard: subscribe to proc events: %w", err)
	}
	g := &oomGuard{logger: logger, init: os.Getpid(), agents: map[int]bool{}, readAdj: readOOMAdj, writeAdj: writeOOMAdj}
	// Exempts init and resets the processes it started before subscribing.
	g.rescan()
	var once sync.Once
	stop = func() { once.Do(func() { _ = unix.Close(fd) }) }
	go func() {
		buf := make([]byte, 8192)
		for {
			n, _, err := unix.Recvfrom(fd, buf, 0)
			if err != nil {
				switch err {
				case unix.EINTR:
					continue
				case unix.ENOBUFS:
					// Events were dropped: rebuild the state from /proc.
					g.rescan()
					continue
				}
				return
			}
			for _, ev := range parseProcEvents(buf[:n]) {
				g.handle(ev)
			}
		}
	}()
	return stop, nil
}

// oomGuard is the state of the guard's event loop.
type oomGuard struct {
	logger *slog.Logger
	init   int          // PID of jailoc-guest init
	agents map[int]bool // thread group IDs of session programs
	// readAdj and writeAdj access /proc/<pid>/oom_score_adj; tests
	// replace them.
	readAdj  func(pid int) (int, error)
	writeAdj func(pid, adj int) error
}

func (g *oomGuard) handle(ev procEvent) {
	switch ev.what {
	case procEventExec:
		p, err := readProcInfo(ev.tgid)
		if err != nil {
			return
		}
		// A parent that exited already leaves the zero value: no holder.
		parent, _ := readProcInfo(p.ppid)
		switch classify(p, parent) {
		case roleGuest:
			g.set(ev.tgid, oomAdjGuest)
		case roleAgent:
			g.agents[ev.tgid] = true
			g.set(ev.tgid, oomAdjAgent)
		case roleOther:
		}
	case procEventFork:
		switch {
		case ev.childTGID == ev.tgid: // a new thread
		case g.agents[ev.tgid], ev.tgid == g.init:
			g.set(ev.childTGID, oomAdjChild)
		default:
			// A process that forked before the guard reset it passed init's
			// exemption on. Session processes regain it on exec, which is
			// handled after this fork.
			if adj, err := g.readAdj(ev.childTGID); err == nil && adj == oomAdjGuest {
				g.set(ev.childTGID, oomAdjChild)
			}
		}
	case procEventExit:
		if ev.pid == ev.tgid {
			delete(g.agents, ev.tgid)
		}
	}
}

func (g *oomGuard) rescan() {
	entries, err := os.ReadDir("/proc")
	if err != nil {
		g.logger.Warn("oom guard: rescan", "error", err)
		return
	}
	procs := map[int]procInfo{}
	for _, e := range entries {
		pid, err := strconv.Atoi(e.Name())
		if err != nil {
			continue
		}
		if p, err := readProcInfo(pid); err == nil {
			procs[pid] = p
		}
	}
	agents, writes := rescanPlan(procs, g.init)
	g.agents = agents
	for pid, adj := range writes {
		g.set(pid, adj)
	}
}

// set writes the oom_score_adj of pid. A process that exited is skipped.
func (g *oomGuard) set(pid, adj int) {
	err := g.writeAdj(pid, adj)
	if err != nil && !errors.Is(err, os.ErrNotExist) && !errors.Is(err, syscall.ESRCH) {
		g.logger.Warn("oom guard: set oom_score_adj", "pid", pid, "error", err)
	}
}

func readOOMAdj(pid int) (int, error) {
	data, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/oom_score_adj")
	if err != nil {
		return 0, fmt.Errorf("read oom_score_adj of %d: %w", pid, err)
	}
	adj, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil {
		return 0, fmt.Errorf("parse oom_score_adj of %d: %w", pid, err)
	}
	return adj, nil
}

func writeOOMAdj(pid, adj int) error {
	if err := os.WriteFile("/proc/"+strconv.Itoa(pid)+"/oom_score_adj", []byte(strconv.Itoa(adj)), 0); err != nil {
		return fmt.Errorf("write oom_score_adj of %d: %w", pid, err)
	}
	return nil
}

// readProcInfo reads the parent, executable, command line and
// oom_score_adj of pid.
func readProcInfo(pid int) (procInfo, error) {
	dir := "/proc/" + strconv.Itoa(pid)
	status, err := os.ReadFile(dir + "/status") //nolint:gosec // G304: procfs
	if err != nil {
		return procInfo{}, fmt.Errorf("read status of %d: %w", pid, err)
	}
	var p procInfo
	for line := range strings.SplitSeq(string(status), "\n") {
		if v, ok := strings.CutPrefix(line, "PPid:"); ok {
			p.ppid, _ = strconv.Atoi(strings.TrimSpace(v)) // malformed: no parent
			break
		}
	}
	// Kernel threads have no executable or command line.
	p.exe, _ = os.Readlink(dir + "/exe")
	if data, err := os.ReadFile(dir + "/cmdline"); err == nil && len(data) > 0 { //nolint:gosec // G304: procfs
		p.argv = strings.Split(strings.TrimSuffix(string(data), "\x00"), "\x00")
	}
	if data, err := os.ReadFile(dir + "/oom_score_adj"); err == nil { //nolint:gosec // G304: procfs
		p.adj, _ = strconv.Atoi(strings.TrimSpace(string(data))) // malformed: 0, rewritten if needed
	}
	return p, nil
}

// startOOMLog logs the kernel's OOM kills to the console log, which
// outlives the VM; the kernel log does not.
func startOOMLog(logger *slog.Logger) (stop func(), err error) {
	f, err := os.Open("/dev/kmsg")
	if err != nil {
		return nil, fmt.Errorf("open kernel log: %w", err)
	}
	go func() {
		buf := make([]byte, 8192) // a record is at most 8 KiB with its header
		for {
			n, err := f.Read(buf)
			if errors.Is(err, syscall.EPIPE) {
				continue // records were overwritten before they were read
			}
			if err != nil {
				return
			}
			if k, ok := parseOOMKill(string(buf[:n])); ok {
				logger.Warn("out of memory: kernel killed a process",
					"pid", k.pid, "process", k.comm, "anon_rss_mib", k.anonRSSKiB/1024, "oom_score_adj", k.adj)
			}
		}
	}()
	return func() { _ = f.Close() }, nil
}
