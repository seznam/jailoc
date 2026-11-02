package guest

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/sys/unix"
)

// Proc connector constants (linux/connector.h, linux/cn_proc.h).
const (
	cnIdxProc         = 1
	cnValProc         = 1
	procCnMcastListen = 1
	execLogMax        = 16 << 20
)

// startExecLog records every exec in the guest to the exec log in the
// control share, and the exit status of each recorded process once its last
// thread exits. It uses the
// kernel proc connector, which reports execs and exits of all processes,
// including short-lived ones; exec details are read from /proc right after
// the event and may be missing when the process already exited. The log is
// informational: guest root can disable or rewrite it.
func startExecLog(logger *slog.Logger) (stop func(), err error) {
	fd, err := unix.Socket(unix.AF_NETLINK, unix.SOCK_DGRAM|unix.SOCK_CLOEXEC, unix.NETLINK_CONNECTOR)
	if err != nil {
		return nil, fmt.Errorf("exec log: netlink socket: %w", err)
	}
	if err := unix.Bind(fd, &unix.SockaddrNetlink{Family: unix.AF_NETLINK, Groups: cnIdxProc, Pid: uint32(os.Getpid())}); err != nil { //nolint:gosec // G115: pids fit in uint32
		_ = unix.Close(fd)
		return nil, fmt.Errorf("exec log: bind proc connector: %w", err)
	}
	if err := unix.Sendto(fd, procListenMessage(), 0, &unix.SockaddrNetlink{Family: unix.AF_NETLINK}); err != nil {
		_ = unix.Close(fd)
		return nil, fmt.Errorf("exec log: subscribe to proc events: %w", err)
	}
	w := &execWriter{path: filepath.Join(ControlDir, ExecLogFile)}
	var once sync.Once
	stop = func() { once.Do(func() { _ = unix.Close(fd) }) }
	go func() {
		buf := make([]byte, 8192)
		// procs tracks the recorded processes still running, so exits are
		// logged only for processes whose start is in the log.
		procs := execTracker{}
		for {
			n, _, err := unix.Recvfrom(fd, buf, 0)
			if err != nil {
				if err == unix.ENOBUFS {
					procs.prune()
				}
				if err == unix.EINTR || err == unix.ENOBUFS {
					continue
				}
				return
			}
			for _, ev := range parseProcEvents(buf[:n]) {
				var rec ExecRecord
				switch ev.what {
				case procEventExec:
					procs.exec(ev.tgid)
					rec = readExec(ev.tgid)
				case procEventFork:
					procs.fork(ev)
					continue
				case procEventExit:
					status, done := procs.exit(ev)
					if !done {
						continue
					}
					rec = exitRecord(ev.tgid, status)
				default:
					continue
				}
				if err := w.write(rec); err != nil {
					logger.Warn("exec log write", "error", err)
				}
			}
		}
	}()
	return stop, nil
}

// procListenMessage is a netlink message subscribing to proc events.
func procListenMessage() []byte {
	const total = unix.NLMSG_HDRLEN + cnMsgLen + 4
	b := make([]byte, total)
	ne := binary.NativeEndian
	ne.PutUint32(b[0:], total)
	ne.PutUint16(b[4:], unix.NLMSG_DONE)
	ne.PutUint32(b[12:], uint32(os.Getpid())) //nolint:gosec // G115: pids fit in uint32
	cn := b[unix.NLMSG_HDRLEN:]
	ne.PutUint32(cn[0:], cnIdxProc)
	ne.PutUint32(cn[4:], cnValProc)
	ne.PutUint16(cn[16:], 4)
	ne.PutUint32(cn[cnMsgLen:], procCnMcastListen)
	return b
}

// execTracker counts the live threads of each recorded process by thread
// group ID. The kernel reports an exit per thread, and the group leader can
// exit (pthread_exit) before the rest, so a process ends with its last thread.
type execTracker map[int]*trackedProc

type trackedProc struct {
	threads int
	status  uint32 // wait status of the last thread that exited
}

// exec starts tracking tgid; a process has a single thread right after exec.
func (t execTracker) exec(tgid int) {
	t[tgid] = &trackedProc{threads: 1}
}

// fork counts a new thread of a tracked process.
func (t execTracker) fork(ev procEvent) {
	if p := t[ev.childTGID]; p != nil && ev.childPID != ev.childTGID {
		p.threads++
	}
}

// exit records a thread exit and reports the process's wait status once its
// last thread is gone.
func (t execTracker) exit(ev procEvent) (status uint32, done bool) {
	p := t[ev.tgid]
	if p == nil {
		return 0, false
	}
	p.status = ev.exitCode
	if p.threads--; p.threads > 0 {
		return 0, false
	}
	delete(t, ev.tgid)
	return p.status, true
}

// prune forgets processes that exited while proc events were dropped.
func (t execTracker) prune() {
	for tgid := range t {
		if _, err := os.Stat("/proc/" + strconv.Itoa(tgid)); err != nil {
			delete(t, tgid)
		}
	}
}

// exitRecord is the exit of pid with the given wait status.
func exitRecord(pid int, status uint32) ExecRecord {
	e := ExecRecord{Time: time.Now().UTC(), Event: "exit", PID: pid, UID: -1}
	if sig := unix.Signal(status & 0x7f); sig != 0 {
		e.Signal = unix.SignalName(sig)
		if e.Signal == "" {
			e.Signal = "signal " + strconv.Itoa(int(sig))
		}
		return e
	}
	e.Code = int(status>>8) & 0xff
	return e
}

func readExec(pid int) ExecRecord {
	e := ExecRecord{Time: time.Now().UTC(), PID: pid, UID: -1}
	dir := "/proc/" + strconv.Itoa(pid)
	if data, err := os.ReadFile(dir + "/cmdline"); err == nil && len(data) > 0 { //nolint:gosec // G304: procfs
		e.Argv = strings.Split(strings.TrimSuffix(string(data), "\x00"), "\x00")
	}
	if data, err := os.ReadFile(dir + "/comm"); err == nil { //nolint:gosec // G304: procfs
		e.Comm = strings.TrimSpace(string(data))
	}
	e.Cwd, _ = os.Readlink(dir + "/cwd")
	e.Exe, _ = os.Readlink(dir + "/exe")
	if data, err := os.ReadFile(dir + "/status"); err == nil { //nolint:gosec // G304: procfs
		for _, line := range strings.Split(string(data), "\n") {
			key, val, ok := strings.Cut(line, ":")
			if !ok {
				continue
			}
			fields := strings.Fields(val)
			if len(fields) == 0 {
				continue
			}
			switch key {
			case "PPid":
				e.PPID, _ = strconv.Atoi(fields[0])
			case "Uid":
				e.UID, _ = strconv.Atoi(fields[0])
			}
		}
	}
	return e
}

// execWriter appends records and rotates the file at execLogMax bytes.
type execWriter struct {
	path string
	f    *os.File
	size int64
}

func (w *execWriter) write(e ExecRecord) error {
	if w.f == nil {
		f, err := os.OpenFile(w.path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644) //nolint:gosec // G302: read by the host user
		if err != nil {
			return err
		}
		info, err := f.Stat()
		if err != nil {
			_ = f.Close()
			return err
		}
		w.f, w.size = f, info.Size()
	}
	var line bytes.Buffer
	if err := json.NewEncoder(&line).Encode(e); err != nil {
		return err
	}
	n, err := w.f.Write(line.Bytes())
	w.size += int64(n)
	if err != nil {
		return err
	}
	if w.size >= execLogMax {
		_ = w.f.Close()
		w.f = nil
		return os.Rename(w.path, w.path+".1")
	}
	return nil
}
