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
// control share. It uses the kernel proc connector, which reports execs of
// all processes, including short-lived ones; their details are read from
// /proc right after the event and may be missing when the process already
// exited. The log is informational: guest root can disable or rewrite it.
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
		for {
			n, _, err := unix.Recvfrom(fd, buf, 0)
			if err != nil {
				if err == unix.EINTR || err == unix.ENOBUFS {
					continue
				}
				return
			}
			for _, pid := range execPIDs(buf[:n]) {
				if err := w.write(readExec(pid)); err != nil {
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

// execPIDs returns the thread group IDs of exec events in a netlink
// datagram.
func execPIDs(b []byte) []int {
	var out []int
	ne := binary.NativeEndian
	for len(b) >= unix.NLMSG_HDRLEN {
		l := int(ne.Uint32(b[0:]))
		if l < unix.NLMSG_HDRLEN || l > len(b) {
			break
		}
		msg := b[unix.NLMSG_HDRLEN:l]
		// proc_event: what u32, cpu u32, timestamp u64, then exec {pid, tgid}.
		if len(msg) >= cnMsgLen+24 {
			ev := msg[cnMsgLen:]
			if ne.Uint32(ev[0:]) == procEventExec {
				out = append(out, int(ne.Uint32(ev[20:])))
			}
		}
		b = b[(l+unix.NLMSG_ALIGNTO-1)&^(unix.NLMSG_ALIGNTO-1):]
	}
	return out
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
