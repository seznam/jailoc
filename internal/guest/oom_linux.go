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
// the program forks. jailoc-guest session processes are exempt from the
// OOM killer. Only init can lower the values: sessions run as the agent
// user. Processes are followed through the kernel proc connector; a child
// keeps the agent's value from its fork until the guard handles the event.
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
	g := &oomGuard{logger: logger, agents: map[int]bool{}}
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
	agents map[int]bool // thread group IDs of session programs
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
		if ev.childTGID != ev.tgid && g.agents[ev.tgid] {
			g.set(ev.childTGID, oomAdjChild)
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
	agents, writes := rescanPlan(procs)
	g.agents = agents
	for pid, adj := range writes {
		g.set(pid, adj)
	}
}

// set writes the oom_score_adj of pid. A process that exited is skipped.
func (g *oomGuard) set(pid, adj int) {
	err := os.WriteFile("/proc/"+strconv.Itoa(pid)+"/oom_score_adj", []byte(strconv.Itoa(adj)), 0)
	if err != nil && !errors.Is(err, os.ErrNotExist) && !errors.Is(err, syscall.ESRCH) {
		g.logger.Warn("oom guard: set oom_score_adj", "pid", pid, "error", err)
	}
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
