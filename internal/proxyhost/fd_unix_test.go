//go:build darwin || linux

package proxyhost

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

func fdClose(t *testing.T, name string, c io.Closer) {
	t.Helper()
	if err := c.Close(); err != nil && !errors.Is(err, net.ErrClosed) && !errors.Is(err, os.ErrClosed) {
		t.Errorf("close %s: %v", name, err)
	}
}

func fdCloseRaw(t *testing.T, fd int) {
	t.Helper()
	if err := unix.Close(fd); err != nil {
		t.Errorf("close fd %d: %v", fd, err)
	}
}

func fdSocketDir(t *testing.T) string {
	t.Helper()
	base := os.TempDir()
	if len(base) > 60 {
		base = "/tmp"
	}
	dir, err := os.MkdirTemp(base, "jlgw")
	if err != nil {
		t.Fatalf("MkdirTemp: %v", err)
	}
	t.Cleanup(func() {
		if err := os.RemoveAll(dir); err != nil {
			t.Errorf("RemoveAll(%s): %v", dir, err)
		}
	})
	return dir
}

// fdUnixPair returns both ends of a real AF_UNIX stream connection made
// through a filesystem socket.
func fdUnixPair(t *testing.T) (server, client *net.UnixConn) {
	t.Helper()
	path := filepath.Join(fdSocketDir(t), "s")
	ln, err := net.ListenUnix("unix", &net.UnixAddr{Name: path, Net: "unix"})
	if err != nil {
		t.Fatalf("ListenUnix: %v", err)
	}
	t.Cleanup(func() { fdClose(t, "unix listener", ln) })
	c, err := net.DialUnix("unix", nil, &net.UnixAddr{Name: path, Net: "unix"})
	if err != nil {
		t.Fatalf("DialUnix: %v", err)
	}
	t.Cleanup(func() { fdClose(t, "unix client", c) })
	s, err := ln.AcceptUnix()
	if err != nil {
		t.Fatalf("AcceptUnix: %v", err)
	}
	t.Cleanup(func() { fdClose(t, "unix server", s) })
	return s, c
}

func fdLoopbackListener(t *testing.T, network string) *net.TCPListener {
	t.Helper()
	ip := net.IPv4(127, 0, 0, 1)
	if network == "tcp6" {
		ip = net.IPv6loopback
	}
	ln, err := net.ListenTCP(network, &net.TCPAddr{IP: ip})
	if err != nil {
		if network == "tcp6" {
			t.Skipf("IPv6 loopback unavailable: %v", err)
		}
		t.Fatalf("ListenTCP(%s): %v", network, err)
	}
	return ln
}

func fdRawFD(t *testing.T, c syscall.Conn) int {
	t.Helper()
	rc, err := c.SyscallConn()
	if err != nil {
		t.Fatalf("SyscallConn: %v", err)
	}
	fd := -1
	if err := rc.Control(func(s uintptr) { fd = int(s) }); err != nil {
		t.Fatalf("Control: %v", err)
	}
	return fd
}

// fdSocketID identifies one kernel socket object by its device and inode,
// which every descriptor copy of the socket shares and no other socket has.
type fdSocketID string

func fdSocketIdentity(t *testing.T, c syscall.Conn) fdSocketID {
	t.Helper()
	var st unix.Stat_t
	if err := unix.Fstat(fdRawFD(t, c), &st); err != nil {
		t.Fatalf("Fstat: %v", err)
	}
	if st.Mode&unix.S_IFMT != unix.S_IFSOCK {
		t.Fatalf("fd %d is not a socket (mode %#o)", fdRawFD(t, c), st.Mode)
	}
	return fdSocketID(fmt.Sprintf("%d:%d", st.Dev, st.Ino))
}

// fdSocketCopies counts this process's open descriptors that refer to the
// socket id, through /proc/self/fd. Descriptors closed by parallel tests
// during the scan are skipped; they cannot be the socket under test.
func fdSocketCopies(t *testing.T, id fdSocketID) int {
	t.Helper()
	const dir = "/proc/self/fd"
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("ReadDir(%s): %v", dir, err)
	}
	n := 0
	for _, e := range entries {
		fi, err := os.Stat(filepath.Join(dir, e.Name()))
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			t.Fatalf("Stat(%s/%s): %v", dir, e.Name(), err)
		}
		st, ok := fi.Sys().(*syscall.Stat_t)
		if !ok || fi.Mode().Type() != os.ModeSocket {
			continue
		}
		if fdSocketID(fmt.Sprintf("%d:%d", st.Dev, st.Ino)) == id {
			n++
		}
	}
	return n
}

func fdAssertSocketCopies(t *testing.T, id fdSocketID, want int, what string) {
	t.Helper()
	if got := fdSocketCopies(t, id); got != want {
		t.Fatalf("%s: %d open descriptors refer to the socket, want %d", what, got, want)
	}
}

func fdAssertOnlyPinnedListener(t *testing.T, id fdSocketID) {
	t.Helper()
	if runtime.GOOS != "linux" {
		return
	}
	fdAssertSocketCopies(t, id, 1, "a received listener descriptor leaked")
}

func fdAssertListenerGone(t *testing.T, id fdSocketID, addr string) {
	t.Helper()
	if runtime.GOOS == "linux" {
		fdAssertSocketCopies(t, id, 0, "after closing the last descriptor")
		return
	}
	c, err := net.DialTimeout("tcp", addr, 2*time.Second)
	if err == nil {
		fdClose(t, "tcp probe", c)
		t.Fatalf("dial %s succeeded; a received listener descriptor leaked", addr)
	}
	if !errors.Is(err, syscall.ECONNREFUSED) {
		t.Fatalf("dial %s error = %v, want ECONNREFUSED", addr, err)
	}
}

func fdRawSocket(t *testing.T, domain, typ int) int {
	t.Helper()
	fd, err := unix.Socket(domain, typ, 0)
	if err != nil {
		if domain == unix.AF_INET6 {
			t.Skipf("IPv6 socket unavailable: %v", err)
		}
		t.Fatalf("Socket: %v", err)
	}
	unix.CloseOnExec(fd)
	t.Cleanup(func() { fdCloseRaw(t, fd) })
	return fd
}

func fdBind(t *testing.T, fd int, sa unix.Sockaddr) {
	t.Helper()
	if err := unix.Bind(fd, sa); err != nil {
		if _, ok := sa.(*unix.SockaddrInet6); ok {
			t.Skipf("IPv6 bind unavailable: %v", err)
		}
		t.Fatalf("Bind: %v", err)
	}
}

func TestValidateLoopbackTCPListenerFD(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		fd      func(t *testing.T) int
		wantErr string
	}{
		{
			name: "listening IPv4 loopback",
			fd: func(t *testing.T) int {
				ln := fdLoopbackListener(t, "tcp4")
				t.Cleanup(func() { fdClose(t, "tcp listener", ln) })
				return fdRawFD(t, ln)
			},
		},
		{
			name: "listening IPv6 loopback",
			fd: func(t *testing.T) int {
				ln := fdLoopbackListener(t, "tcp6")
				t.Cleanup(func() { fdClose(t, "tcp listener", ln) })
				return fdRawFD(t, ln)
			},
		},
		{
			name: "listening on non-127.0.0.1 IPv4 loopback",
			fd: func(t *testing.T) int {
				ln, err := net.ListenTCP("tcp4", &net.TCPAddr{IP: net.IPv4(127, 0, 0, 2)})
				if err != nil {
					t.Skipf("127.0.0.2 unavailable: %v", err)
				}
				t.Cleanup(func() { fdClose(t, "tcp listener", ln) })
				return fdRawFD(t, ln)
			},
		},
		{
			name: "pipe",
			fd: func(t *testing.T) int {
				r, w, err := os.Pipe()
				if err != nil {
					t.Fatalf("Pipe: %v", err)
				}
				t.Cleanup(func() {
					fdClose(t, "pipe r", r)
					fdClose(t, "pipe w", w)
				})
				return fdRawFD(t, w)
			},
			wantErr: "socket type",
		},
		{
			name: "connected UDP",
			fd: func(t *testing.T) int {
				srv, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
				if err != nil {
					t.Fatalf("ListenUDP: %v", err)
				}
				t.Cleanup(func() { fdClose(t, "udp server", srv) })
				c, err := net.DialUDP("udp4", nil, srv.LocalAddr().(*net.UDPAddr))
				if err != nil {
					t.Fatalf("DialUDP: %v", err)
				}
				t.Cleanup(func() { fdClose(t, "udp client", c) })
				return fdRawFD(t, c)
			},
			wantErr: "not a stream",
		},
		{
			name: "unix stream listener",
			fd: func(t *testing.T) int {
				ln, err := net.ListenUnix("unix", &net.UnixAddr{Name: filepath.Join(fdSocketDir(t), "l"), Net: "unix"})
				if err != nil {
					t.Fatalf("ListenUnix: %v", err)
				}
				t.Cleanup(func() { fdClose(t, "unix listener", ln) })
				return fdRawFD(t, ln)
			},
			wantErr: "not TCP",
		},
		{
			name: "unbound TCP",
			fd: func(t *testing.T) int {
				return fdRawSocket(t, unix.AF_INET, unix.SOCK_STREAM)
			},
			wantErr: "loopback",
		},
		{
			name: "IPv4 wildcard",
			fd: func(t *testing.T) int {
				fd := fdRawSocket(t, unix.AF_INET, unix.SOCK_STREAM)
				fdBind(t, fd, &unix.SockaddrInet4{})
				return fd
			},
			wantErr: "loopback",
		},
		{
			name: "IPv6 wildcard",
			fd: func(t *testing.T) int {
				fd := fdRawSocket(t, unix.AF_INET6, unix.SOCK_STREAM)
				fdBind(t, fd, &unix.SockaddrInet6{})
				return fd
			},
			wantErr: "loopback",
		},
		{
			name: "IPv4-mapped IPv6 loopback",
			fd: func(t *testing.T) int {
				fd := fdRawSocket(t, unix.AF_INET6, unix.SOCK_STREAM)
				fdBind(t, fd, &unix.SockaddrInet6{Addr: [16]byte{10: 0xff, 11: 0xff, 12: 127, 15: 1}})
				return fd
			},
			wantErr: "loopback",
		},
		{
			name: "bound loopback not listening",
			fd: func(t *testing.T) int {
				fd := fdRawSocket(t, unix.AF_INET, unix.SOCK_STREAM)
				fdBind(t, fd, &unix.SockaddrInet4{Addr: [4]byte{127, 0, 0, 1}})
				return fd
			},
			wantErr: "not listening",
		},
		{
			name: "connected loopback TCP",
			fd: func(t *testing.T) int {
				ln := fdLoopbackListener(t, "tcp4")
				t.Cleanup(func() { fdClose(t, "tcp listener", ln) })
				c, err := net.Dial("tcp4", ln.Addr().String())
				if err != nil {
					t.Fatalf("Dial: %v", err)
				}
				t.Cleanup(func() { fdClose(t, "tcp client", c) })
				return fdRawFD(t, c.(*net.TCPConn))
			},
			wantErr: "not listening",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			addr, err := validateLoopbackTCPListenerFD(tt.fd(t))
			if tt.wantErr == "" {
				if err != nil {
					t.Fatalf("validate error = %v, want nil", err)
				}
				if addr == nil || addr.Port == 0 || !addr.IP.IsLoopback() {
					t.Fatalf("validate addr = %v, want loopback with port", addr)
				}
				return
			}
			if err == nil {
				t.Fatalf("validate accepted %s as %v", tt.name, addr)
			}
			if !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("validate error = %v, want it to mention %q", err, tt.wantErr)
			}
		})
	}
}

func TestRightsFromMessages(t *testing.T) {
	t.Parallel()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("Pipe: %v", err)
	}
	t.Cleanup(func() {
		fdClose(t, "pipe r", r)
		fdClose(t, "pipe w", w)
	})
	dupRights := func(t *testing.T) unix.SocketControlMessage {
		t.Helper()
		fd, err := unix.Dup(fdRawFD(t, w))
		if err != nil {
			t.Fatalf("Dup: %v", err)
		}
		msgs, err := unix.ParseSocketControlMessage(unix.UnixRights(fd))
		if err != nil || len(msgs) != 1 {
			fdCloseRaw(t, fd)
			t.Fatalf("ParseSocketControlMessage: %v, %d messages", err, len(msgs))
		}
		return msgs[0]
	}
	t.Run("unknown message still yields rights for closing", func(t *testing.T) {
		t.Parallel()
		unknown := unix.SocketControlMessage{Data: []byte{1, 2, 3, 4}}
		unknown.Header.Level = unix.SOL_SOCKET
		unknown.Header.Type = 0x7f7f
		fds, err := rightsFromMessages([]unix.SocketControlMessage{dupRights(t), unknown})
		for _, fd := range fds {
			fdCloseRaw(t, fd)
		}
		if len(fds) != 1 {
			t.Fatalf("rights = %v, want the one descriptor", fds)
		}
		if err == nil || !strings.Contains(err.Error(), "ancillary") {
			t.Fatalf("error = %v, want unexpected ancillary message", err)
		}
	})
	t.Run("partial rights entry", func(t *testing.T) {
		t.Parallel()
		m := dupRights(t)
		m.Data = append(m.Data[:4:4], 0xaa, 0xbb)
		fds, err := rightsFromMessages([]unix.SocketControlMessage{m})
		for _, fd := range fds {
			fdCloseRaw(t, fd)
		}
		if len(fds) != 1 || fds[0] != int(int32(binary.NativeEndian.Uint32(m.Data[:4]))) { //nolint:gosec // SCM_RIGHTS carries 32-bit file descriptors.
			t.Fatalf("rights = %v, want the whole descriptor", fds)
		}
		if err == nil {
			t.Fatal("error = nil for a partial rights entry")
		}
	})
}

func TestVerifyUnixPeer(t *testing.T) {
	t.Parallel()
	server, client := fdUnixPair(t)
	uid := uint32(os.Geteuid()) //nolint:gosec // effective UIDs are 32-bit.
	for _, conn := range []*net.UnixConn{server, client} {
		if err := verifyUnixPeer(conn, uid); err != nil {
			t.Fatalf("same UID: %v", err)
		}
		if err := verifyUnixPeer(conn, uid+1); err == nil || !strings.Contains(err.Error(), "uid") {
			t.Fatalf("other UID: %v, want UID mismatch", err)
		}
	}
}

func TestBindDeadline(t *testing.T) {
	t.Parallel()
	for _, deadline := range []bool{false, true} {
		name := "cancellation"
		if deadline {
			name = "deadline"
		}
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			server, client := fdUnixPair(t)
			ctx, cancel := context.WithCancel(t.Context())
			want := context.Canceled
			if deadline {
				cancel()
				ctx, cancel = context.WithTimeout(t.Context(), 50*time.Millisecond)
				want = context.DeadlineExceeded
			}
			defer cancel()
			release, err := bindDeadline(ctx, client)
			if err != nil {
				t.Fatalf("bindDeadline: %v", err)
			}
			if !deadline {
				cancel()
			}
			start := time.Now()
			var b [1]byte
			_, err = client.Read(b[:])
			if !errors.Is(exchangeErr(ctx, err), want) {
				t.Fatalf("read = %v, want %v", err, want)
			}
			if time.Since(start) > 2*time.Second {
				t.Fatal("context did not promptly unblock read")
			}
			if err := release(); err != nil {
				t.Fatalf("release: %v", err)
			}
			fdAssertConnUsable(t, server, client)
			fdAssertConnUsable(t, client, server)
		})
	}
	t.Run("already canceled", func(t *testing.T) {
		t.Parallel()
		_, client := fdUnixPair(t)
		ctx, cancel := context.WithCancel(t.Context())
		cancel()
		if release, err := bindDeadline(ctx, client); release != nil || !errors.Is(err, context.Canceled) {
			t.Fatalf("bindDeadline release present = %v, error = %v, want nil release and context.Canceled", release != nil, err)
		}
	})
	t.Run("release before cancellation", func(t *testing.T) {
		t.Parallel()
		server, client := fdUnixPair(t)
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		release, err := bindDeadline(ctx, client)
		if err != nil {
			t.Fatalf("bindDeadline: %v", err)
		}
		if err := release(); err != nil {
			t.Fatalf("release: %v", err)
		}
		cancel()
		fdAssertConnUsable(t, server, client)
		fdAssertConnUsable(t, client, server)
	})
}

// fdAssertConnUsable proves the deadline used during the exchange was cleared.
func fdAssertConnUsable(t *testing.T, writer, reader *net.UnixConn) {
	t.Helper()
	if _, err := writer.Write([]byte{9}); err != nil {
		t.Fatalf("write after exchange: %v", err)
	}
	var b [1]byte
	if _, err := io.ReadFull(reader, b[:]); err != nil {
		t.Fatalf("read after exchange: %v; exchange deadline was not cleared", err)
	}
}

func TestIODeadline(t *testing.T) {
	t.Parallel()
	now := time.Unix(1_000_000, 0)
	near, cancelNear := context.WithDeadline(t.Context(), now.Add(time.Second))
	defer cancelNear()
	far, cancelFar := context.WithDeadline(t.Context(), now.Add(time.Hour))
	defer cancelFar()
	tests := []struct {
		name string
		ctx  context.Context
		want time.Time
	}{
		{name: "no context deadline", ctx: t.Context(), want: now.Add(fdExchangeTimeout)},
		{name: "earlier context deadline", ctx: near, want: now.Add(time.Second)},
		{name: "later context deadline", ctx: far, want: now.Add(fdExchangeTimeout)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := ioDeadline(tt.ctx, now); !got.Equal(tt.want) {
				t.Fatalf("ioDeadline = %s, want %s", got, tt.want)
			}
		})
	}
	if fdExchangeTimeout > 5*time.Second {
		t.Fatalf("fdExchangeTimeout = %s, want at most 5s", fdExchangeTimeout)
	}
}
