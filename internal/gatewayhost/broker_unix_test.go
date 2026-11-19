//go:build darwin || linux

package gatewayhost

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

const brkTimeout = 10 * time.Second

func brkUID() uint32 {
	return uint32(os.Getuid()) //nolint:gosec // UIDs are 32-bit on Linux and Darwin.
}

func brkContext(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), brkTimeout)
	t.Cleanup(cancel)
	return ctx
}

// brkDir returns a private directory with a short path: Darwin limits
// sun_path to 104 bytes.
func brkDir(t *testing.T) string {
	t.Helper()
	base := os.TempDir()
	if len(base) > 60 {
		base = "/tmp"
	}
	dir, err := os.MkdirTemp(base, "jlbr")
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

func brkClose(t *testing.T, name string, c io.Closer) {
	t.Helper()
	if err := c.Close(); err != nil && !errors.Is(err, net.ErrClosed) && !errors.Is(err, os.ErrClosed) {
		t.Errorf("close %s: %v", name, err)
	}
}

// brkLoopback listens on an ephemeral loopback port; tcp6 skips the test
// when [::1] cannot be bound.
func brkLoopback(t *testing.T, network string) *net.TCPListener {
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

type brkHarness struct {
	path   string
	addrs  []*net.TCPAddr
	broker *Broker
	cancel context.CancelFunc
	done   chan error
	once   sync.Once
	err    error
}

// brkStart serves a broker on fresh loopback listeners for networks and a
// control socket in a private directory. cfg.Listeners and cfg.Control are
// filled in; the caller sets OwnerUID and limits.
func brkStart(t *testing.T, cfg BrokerConfig, networks ...string) *brkHarness {
	t.Helper()
	if len(networks) == 0 {
		networks = []string{"tcp4"}
	}
	h := &brkHarness{path: filepath.Join(brkDir(t), "b.sock"), done: make(chan error, 1)}
	for _, network := range networks {
		ln := brkLoopback(t, network)
		cfg.Listeners = append(cfg.Listeners, ln)
		h.addrs = append(h.addrs, ln.Addr().(*net.TCPAddr))
	}
	ul, err := ListenBrokerSocket(h.path, cfg.OwnerUID)
	if err != nil {
		for _, ln := range cfg.Listeners {
			brkClose(t, "listener", ln)
		}
		t.Fatalf("ListenBrokerSocket: %v", err)
	}
	cfg.Control = ul
	if cfg.Logf == nil {
		cfg.Logf = t.Logf
	}
	b, err := NewBroker(cfg)
	if err != nil {
		for _, ln := range cfg.Listeners {
			brkClose(t, "listener", ln)
		}
		brkClose(t, "control", ul)
		t.Fatalf("NewBroker: %v", err)
	}
	h.broker = b
	ctx, cancel := context.WithCancel(context.Background())
	h.cancel = cancel
	go func() { h.done <- b.Serve(ctx) }()
	t.Cleanup(func() {
		if err := h.stop(); err != nil {
			t.Errorf("broker Serve: %v", err)
		}
	})
	return h
}

// stop cancels Serve and waits a bounded time for it to return.
func (h *brkHarness) stop() error {
	h.once.Do(func() {
		h.cancel()
		select {
		case h.err = <-h.done:
		case <-time.After(brkTimeout):
			h.err = errors.New("Serve did not return after cancellation")
		}
	})
	return h.err
}

// brkAttach attaches a gateway, retrying while a previous gateway is still
// being detached.
func brkAttach(t *testing.T, ctx context.Context, h *brkHarness, addr *net.TCPAddr) net.Listener {
	t.Helper()
	for {
		ln, err := AttachBroker(ctx, h.path, brkUID(), addr)
		if err == nil {
			return ln
		}
		if !errors.Is(err, ErrBrokerBusy) || ctx.Err() != nil {
			t.Fatalf("AttachBroker: %v", err)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// brkServe answers every accepted connection with "<tag> <remote>\n". The
// cleanup closes ln and waits for every handler.
func brkServe(t *testing.T, ln net.Listener, tag string) {
	t.Helper()
	var wg sync.WaitGroup
	wg.Go(func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			wg.Go(func() {
				defer c.Close() //nolint:errcheck // the client reads until EOF either way.
				kind := tag
				if _, ok := c.(*net.TCPConn); !ok {
					kind = fmt.Sprintf("bad-type-%T", c)
				}
				// A failed write shows up as a client error.
				_, _ = fmt.Fprintf(c, "%s %s\n", kind, c.RemoteAddr())
			})
		}
	})
	t.Cleanup(func() {
		brkClose(t, "gateway listener", ln)
		wg.Wait()
	})
}

// brkGet dials addr and returns the line the gateway wrote, after checking
// that the gateway saw this client's address.
func brkGet(addr *net.TCPAddr, timeout time.Duration) (string, error) {
	c, err := net.DialTimeout("tcp", addr.String(), timeout)
	if err != nil {
		return "", fmt.Errorf("dial %s: %w", addr, err)
	}
	defer c.Close() //nolint:errcheck // read-only client.
	if err := c.SetDeadline(time.Now().Add(timeout)); err != nil {
		return "", fmt.Errorf("set deadline: %w", err)
	}
	line, err := bufio.NewReader(c).ReadString('\n')
	if err != nil {
		return "", fmt.Errorf("read from %s: %w", addr, err)
	}
	tag, remote, ok := strings.Cut(strings.TrimSpace(line), " ")
	if !ok {
		return "", fmt.Errorf("malformed reply %q", line)
	}
	if remote != c.LocalAddr().String() {
		return "", fmt.Errorf("gateway saw remote %s, client is %s", remote, c.LocalAddr())
	}
	return tag, nil
}

func brkWantServed(t *testing.T, addr *net.TCPAddr, tag string) {
	t.Helper()
	got, err := brkGet(addr, 5*time.Second)
	if err != nil {
		t.Fatalf("request to %s: %v", addr, err)
	}
	if got != tag {
		t.Fatalf("request to %s served by %q, want %q", addr, got, tag)
	}
}

// brkWantClosed dials addr and expects the connection to be closed without
// any data, rather than to time out.
func brkWantClosed(t *testing.T, addr *net.TCPAddr) {
	t.Helper()
	c, err := net.DialTimeout("tcp", addr.String(), 5*time.Second)
	if err != nil {
		t.Fatalf("dial %s: %v", addr, err)
	}
	defer brkClose(t, "client", c)
	if err := c.SetDeadline(time.Now().Add(5 * time.Second)); err != nil {
		t.Fatalf("SetDeadline: %v", err)
	}
	var b [1]byte
	n, err := c.Read(b[:])
	if n != 0 || err == nil || errors.Is(err, os.ErrDeadlineExceeded) {
		t.Fatalf("read = %d, %v; want the broker to close the connection", n, err)
	}
}

func brkPipe(t *testing.T) (r, w *os.File) {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("Pipe: %v", err)
	}
	t.Cleanup(func() {
		brkClose(t, "pipe read end", r)
		brkClose(t, "pipe write end", w)
	})
	return r, w
}

// brkAssertPipeEOF proves that every copy of the pipe write end is closed.
func brkAssertPipeEOF(t *testing.T, r *os.File) {
	t.Helper()
	if err := r.SetReadDeadline(time.Now().Add(5 * time.Second)); err != nil {
		t.Fatalf("pipe SetReadDeadline: %v", err)
	}
	var b [1]byte
	n, err := r.Read(b[:])
	if n != 0 || !errors.Is(err, io.EOF) {
		t.Fatalf("pipe read = %d, %v; want EOF because every received copy was closed", n, err)
	}
}

func brkRawFD(t *testing.T, c syscall.Conn) int {
	t.Helper()
	rc, err := c.SyscallConn()
	if err != nil {
		t.Fatalf("SyscallConn: %v", err)
	}
	var fd int
	if err := rc.Control(func(s uintptr) { fd = int(s) }); err != nil { //nolint:gosec // file descriptors fit in int.
		t.Fatalf("Control: %v", err)
	}
	return fd
}

// brkFakeBroker accepts one gateway on a private socket and hands its end to
// serve, which runs in the test process (and so passes the UID check).
func brkFakeBroker(t *testing.T, serve func(c *net.UnixConn)) string {
	t.Helper()
	path := filepath.Join(brkDir(t), "f.sock")
	ul, err := net.ListenUnix("unix", &net.UnixAddr{Name: path, Net: "unix"})
	if err != nil {
		t.Fatalf("ListenUnix: %v", err)
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		c, err := ul.AcceptUnix()
		if err != nil {
			return
		}
		serve(c)
	}()
	t.Cleanup(func() {
		brkClose(t, "fake broker", ul)
		<-done
	})
	return path
}

func TestBrokerSocketPath(t *testing.T) {
	t.Parallel()
	if got, want := BrokerSocketPath(501), "/var/run/jailoc-gateway/501/broker.sock"; got != want {
		t.Fatalf("BrokerSocketPath(501) = %q, want %q", got, want)
	}
}

func TestNewBrokerRejectsInvalidConfig(t *testing.T) {
	t.Parallel()
	dir := brkDir(t)
	ul, err := net.ListenUnix("unix", &net.UnixAddr{Name: filepath.Join(dir, "c.sock"), Net: "unix"})
	if err != nil {
		t.Fatalf("ListenUnix: %v", err)
	}
	t.Cleanup(func() { brkClose(t, "control", ul) })
	good := brkLoopback(t, "tcp4")
	t.Cleanup(func() { brkClose(t, "listener", good) })
	closed := brkLoopback(t, "tcp4")
	brkClose(t, "closed listener", closed)

	tests := []struct {
		name    string
		cfg     BrokerConfig
		wantErr string
	}{
		{name: "no listeners", cfg: BrokerConfig{Control: ul}, wantErr: "at least one listener"},
		{name: "nil listener", cfg: BrokerConfig{Control: ul, Listeners: []*net.TCPListener{nil}}, wantErr: "nil"},
		{name: "closed listener", cfg: BrokerConfig{Control: ul, Listeners: []*net.TCPListener{closed}}, wantErr: "listener"},
		{name: "nil control", cfg: BrokerConfig{Listeners: []*net.TCPListener{good}}, wantErr: "control"},
		{name: "negative in-flight limit", cfg: BrokerConfig{Control: ul, Listeners: []*net.TCPListener{good}, MaxInFlight: -1}, wantErr: "MaxInFlight"},
		{name: "negative stall timeout", cfg: BrokerConfig{Control: ul, Listeners: []*net.TCPListener{good}, StallTimeout: -time.Second}, wantErr: "StallTimeout"},
		{name: "negative send timeout", cfg: BrokerConfig{Control: ul, Listeners: []*net.TCPListener{good}, SendTimeout: -time.Second}, wantErr: "SendTimeout"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			b, err := NewBroker(tt.cfg)
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("NewBroker() = %v, %v; want error containing %q", b, err, tt.wantErr)
			}
		})
	}
}

func TestBrokerDeliversConnections(t *testing.T) {
	t.Parallel()
	for _, network := range []string{"tcp4", "tcp6"} {
		t.Run(network, func(t *testing.T) {
			t.Parallel()
			ctx := brkContext(t)
			h := brkStart(t, BrokerConfig{OwnerUID: brkUID()}, network)
			addr := h.addrs[0]
			ln, err := AttachBroker(ctx, h.path, brkUID(), addr)
			if err != nil {
				t.Fatalf("AttachBroker: %v", err)
			}
			got, ok := ln.Addr().(*net.TCPAddr)
			if !ok || !got.IP.Equal(addr.IP) || got.Port != addr.Port {
				t.Fatalf("Addr() = %v, want %v", ln.Addr(), addr)
			}
			brkServe(t, ln, "A")
			for range 3 {
				brkWantServed(t, addr, "A")
			}
		})
	}
}

func TestBrokerAcceptedConnection(t *testing.T) {
	t.Parallel()
	ctx := brkContext(t)
	h := brkStart(t, BrokerConfig{OwnerUID: brkUID()})
	addr := h.addrs[0]
	ln, err := AttachBroker(ctx, h.path, brkUID(), addr)
	if err != nil {
		t.Fatalf("AttachBroker: %v", err)
	}
	t.Cleanup(func() { brkClose(t, "gateway listener", ln) })
	client, err := net.DialTimeout("tcp", addr.String(), 5*time.Second)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	t.Cleanup(func() { brkClose(t, "client", client) })
	c, err := ln.Accept()
	if err != nil {
		t.Fatalf("Accept: %v", err)
	}
	t.Cleanup(func() { brkClose(t, "accepted", c) })
	tc, ok := c.(*net.TCPConn)
	if !ok {
		t.Fatalf("Accept returned %T, want *net.TCPConn", c)
	}
	if tc.LocalAddr().String() != addr.String() || tc.RemoteAddr().String() != client.LocalAddr().String() {
		t.Fatalf("accepted %s -> %s, want %s -> %s", tc.RemoteAddr(), tc.LocalAddr(), client.LocalAddr(), addr)
	}
	flags, err := unix.FcntlInt(uintptr(brkRawFD(t, tc)), unix.F_GETFD, 0) //nolint:gosec // file descriptors are non-negative.
	if err != nil {
		t.Fatalf("F_GETFD: %v", err)
	}
	if flags&unix.FD_CLOEXEC == 0 {
		t.Fatal("accepted connection lacks FD_CLOEXEC")
	}
	if _, err := tc.Write([]byte("x")); err != nil {
		t.Fatalf("write: %v", err)
	}
	var b [1]byte
	if err := client.SetReadDeadline(time.Now().Add(5 * time.Second)); err != nil {
		t.Fatalf("SetReadDeadline: %v", err)
	}
	if _, err := io.ReadFull(client, b[:]); err != nil || b[0] != 'x' {
		t.Fatalf("client read = %q, %v", b[:], err)
	}
}

func TestBrokerRefusesSecondGateway(t *testing.T) {
	t.Parallel()
	ctx := brkContext(t)
	h := brkStart(t, BrokerConfig{OwnerUID: brkUID()})
	a, err := AttachBroker(ctx, h.path, brkUID(), h.addrs[0])
	if err != nil {
		t.Fatalf("AttachBroker A: %v", err)
	}
	brkServe(t, a, "A")
	b, err := AttachBroker(ctx, h.path, brkUID(), h.addrs[0])
	if !errors.Is(err, ErrBrokerBusy) {
		if b != nil {
			brkClose(t, "gateway B", b)
		}
		t.Fatalf("AttachBroker B = %v; want ErrBrokerBusy", err)
	}
	brkWantServed(t, h.addrs[0], "A")
}

func TestBrokerTakeoverAfterDetach(t *testing.T) {
	t.Parallel()
	ctx := brkContext(t)
	h := brkStart(t, BrokerConfig{OwnerUID: brkUID()})
	a, err := AttachBroker(ctx, h.path, brkUID(), h.addrs[0])
	if err != nil {
		t.Fatalf("AttachBroker A: %v", err)
	}
	brkServe(t, a, "A")
	brkWantServed(t, h.addrs[0], "A")
	if err := a.Close(); err != nil {
		t.Fatalf("close A: %v", err)
	}
	if _, err := a.Accept(); !errors.Is(err, net.ErrClosed) {
		t.Fatalf("Accept after Close = %v; want net.ErrClosed", err)
	}
	if err := a.Close(); err != nil {
		t.Fatalf("second close of A: %v", err)
	}
	b := brkAttach(t, ctx, h, h.addrs[0])
	brkServe(t, b, "B")
	brkWantServed(t, h.addrs[0], "B")
}

func TestBrokerBurstBackpressure(t *testing.T) {
	t.Parallel()
	ctx := brkContext(t)
	h := brkStart(t, BrokerConfig{OwnerUID: brkUID(), MaxInFlight: 8}, "tcp4")
	ln, err := AttachBroker(ctx, h.path, brkUID(), h.addrs[0])
	if err != nil {
		t.Fatalf("AttachBroker: %v", err)
	}
	brkServe(t, ln, "A")

	const n = 250
	errs := make(chan error, n)
	var wg sync.WaitGroup
	for range n {
		wg.Go(func() {
			tag, err := brkGet(h.addrs[0], brkTimeout)
			if err == nil && tag != "A" {
				err = fmt.Errorf("served by %q", tag)
			}
			errs <- err
		})
	}
	wg.Wait()
	close(errs)
	failed := 0
	var first error
	for err := range errs {
		if err != nil {
			failed++
			if first == nil {
				first = err
			}
		}
	}
	if failed != 0 {
		t.Fatalf("%d of %d connections failed, first: %v", failed, n, first)
	}
}

func TestBrokerDetachesStalledGateway(t *testing.T) {
	t.Parallel()
	ctx := brkContext(t)
	const limit = 4
	h := brkStart(t, BrokerConfig{OwnerUID: brkUID(), MaxInFlight: limit, StallTimeout: 200 * time.Millisecond})

	// A raw gateway that attaches, receives descriptors and never acknowledges.
	stalled, err := net.DialUnix("unix", nil, &net.UnixAddr{Name: h.path, Net: "unix"})
	if err != nil {
		t.Fatalf("dial broker: %v", err)
	}
	t.Cleanup(func() { brkClose(t, "stalled gateway", stalled) })
	if err := stalled.SetReadDeadline(time.Now().Add(brkTimeout)); err != nil {
		t.Fatalf("SetReadDeadline: %v", err)
	}
	var status [1]byte
	if _, err := io.ReadFull(stalled, status[:]); err != nil || status[0] != 'A' {
		t.Fatalf("status = %q, %v; want A", status[:], err)
	}

	// More clients than the in-flight limit, so the accept loop also waits.
	for range limit + 2 {
		c, err := net.DialTimeout("tcp", h.addrs[0].String(), 5*time.Second)
		if err != nil {
			t.Fatalf("dial: %v", err)
		}
		t.Cleanup(func() { brkClose(t, "stalled client", c) })
	}

	// Read frames until the broker detaches the stalled gateway (EOF).
	received := 0
	for {
		var b [1]byte
		oob := make([]byte, unix.CmsgSpace(4*maxReceivedFDs))
		n, oobn, _, _, err := stalled.ReadMsgUnix(b[:], oob)
		fds, perr := receivedRights(oob[:max(oobn, 0)])
		if cerr := closeFDs(fds); cerr != nil {
			t.Errorf("close fds: %v", cerr)
		}
		if perr != nil {
			t.Fatalf("ancillary data: %v", perr)
		}
		if n <= 0 {
			if err != nil && !errors.Is(err, io.EOF) {
				t.Fatalf("stalled gateway read: %v; want EOF from the broker detaching it", err)
			}
			break
		}
		if b[0] != 'c' || len(fds) != 1 {
			t.Fatalf("frame %q with %d fds", b[:], len(fds))
		}
		received++
	}
	if received > limit {
		t.Fatalf("stalled gateway received %d descriptors, limit %d", received, limit)
	}

	next := brkAttach(t, ctx, h, h.addrs[0])
	brkServe(t, next, "B")
	brkWantServed(t, h.addrs[0], "B")
}

func TestBrokerSendTimeoutDetachesGateway(t *testing.T) {
	t.Parallel()
	ctx := brkContext(t)
	// A huge in-flight limit and stall timeout leave only the send timeout:
	// a gateway that never reads fills the socket buffer and blocks sends.
	h := brkStart(t, BrokerConfig{OwnerUID: brkUID(), MaxInFlight: 100000, StallTimeout: time.Hour, SendTimeout: 100 * time.Millisecond})
	stalled, err := net.DialUnix("unix", nil, &net.UnixAddr{Name: h.path, Net: "unix"})
	if err != nil {
		t.Fatalf("dial broker: %v", err)
	}
	t.Cleanup(func() { brkClose(t, "stalled gateway", stalled) })
	var status [1]byte
	if err := stalled.SetReadDeadline(time.Now().Add(brkTimeout)); err != nil {
		t.Fatalf("SetReadDeadline: %v", err)
	}
	if _, err := io.ReadFull(stalled, status[:]); err != nil || status[0] != 'A' {
		t.Fatalf("status = %q, %v; want A", status[:], err)
	}
	// Keep opening connections until the broker frees the slot.
	for {
		if ctx.Err() != nil {
			t.Fatal("broker never detached a gateway whose sends block")
		}
		ln, err := AttachBroker(ctx, h.path, brkUID(), h.addrs[0])
		if err == nil {
			brkServe(t, ln, "B")
			break
		}
		if !errors.Is(err, ErrBrokerBusy) {
			t.Fatalf("AttachBroker: %v", err)
		}
		for range 32 {
			c, err := net.DialTimeout("tcp", h.addrs[0].String(), 5*time.Second)
			if err != nil {
				t.Fatalf("dial: %v", err)
			}
			brkClose(t, "client", c)
		}
	}
	brkWantServed(t, h.addrs[0], "B")
}

func TestBrokerClosesConnectionsWithoutGateway(t *testing.T) {
	t.Parallel()
	h := brkStart(t, BrokerConfig{OwnerUID: brkUID()}, "tcp4", "tcp6")
	for _, addr := range h.addrs {
		brkWantClosed(t, addr)
	}
}

func TestBrokerRefusesWrongOwner(t *testing.T) {
	t.Parallel()
	ctx := brkContext(t)
	h := brkStart(t, BrokerConfig{OwnerUID: brkUID() + 1})

	raw, err := net.DialUnix("unix", nil, &net.UnixAddr{Name: h.path, Net: "unix"})
	if err != nil {
		t.Fatalf("dial broker: %v", err)
	}
	t.Cleanup(func() { brkClose(t, "raw gateway", raw) })
	// A client connection while the refused peer is connected must not reach it.
	brkWantClosed(t, h.addrs[0])
	if err := raw.SetReadDeadline(time.Now().Add(5 * time.Second)); err != nil {
		t.Fatalf("SetReadDeadline: %v", err)
	}
	var b [1]byte
	oob := make([]byte, unix.CmsgSpace(4*maxReceivedFDs))
	n, oobn, _, _, err := raw.ReadMsgUnix(b[:], oob)
	if n > 0 || oobn > 0 || (err != nil && !errors.Is(err, io.EOF)) {
		t.Fatalf("refused peer read = %d bytes, %d ancillary bytes, %v; want EOF and nothing else", n, oobn, err)
	}

	ln, err := AttachBroker(ctx, h.path, brkUID(), h.addrs[0])
	if err == nil {
		brkClose(t, "gateway", ln)
		t.Fatal("AttachBroker succeeded for a UID the broker does not serve")
	}
	if errors.Is(err, ErrBrokerBusy) {
		t.Fatalf("AttachBroker = %v; want a refusal, not busy", err)
	}
}

func TestAttachBrokerChecksBrokerUID(t *testing.T) {
	t.Parallel()
	ctx := brkContext(t)
	release := make(chan struct{})
	// The fake broker never writes a status: reading it would block until
	// the deadline instead of failing on the UID.
	path := brkFakeBroker(t, func(c *net.UnixConn) {
		defer c.Close() //nolint:errcheck // test fake.
		<-release
	})
	// Registered after brkFakeBroker so it runs first: the fake broker's
	// cleanup waits for serve, which waits for release.
	t.Cleanup(func() { close(release) })
	addr := &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 443}
	ln, err := AttachBroker(ctx, path, brkUID()+1, addr)
	if err == nil {
		brkClose(t, "gateway", ln)
		t.Fatal("AttachBroker accepted a broker running as an unexpected UID")
	}
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, os.ErrDeadlineExceeded) || !strings.Contains(err.Error(), "peer uid") {
		t.Fatalf("AttachBroker = %v; want a peer UID refusal before reading the status", err)
	}
}

func TestAttachBrokerRejectsAddr(t *testing.T) {
	t.Parallel()
	missing := filepath.Join(brkDir(t), "missing.sock")
	tests := []struct {
		name string
		addr *net.TCPAddr
	}{
		{name: "nil", addr: nil},
		{name: "unspecified", addr: &net.TCPAddr{IP: net.IPv4zero, Port: 443}},
		{name: "public IPv4", addr: &net.TCPAddr{IP: net.IPv4(192, 0, 2, 1), Port: 443}},
		{name: "public IPv6", addr: &net.TCPAddr{IP: net.ParseIP("2001:db8::1"), Port: 443}},
		{name: "no port", addr: &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1)}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			ln, err := AttachBroker(brkContext(t), missing, brkUID(), tt.addr)
			if err == nil {
				brkClose(t, "gateway", ln)
				t.Fatal("AttachBroker accepted the address")
			}
			if !strings.Contains(err.Error(), "address") || strings.Contains(err.Error(), "missing.sock") {
				t.Fatalf("AttachBroker = %v; want an address error before dialing", err)
			}
		})
	}
}

func TestAttachBrokerRejectsMalformedFrames(t *testing.T) {
	t.Parallel()
	type sentinels struct {
		pipeW    *os.File
		listener *net.TCPListener
		unixConn *net.UnixConn
	}
	tests := []struct {
		name    string
		status  []byte // status frame; its descriptors come from statusFDs
		frame   []byte // frame sent after an 'A' status
		fds     func(t *testing.T, s *sentinels) []int
		inState bool // fds ride on the status frame instead of the frame
		pipe    bool // fds include the pipe write end, which must be closed
		wantErr string
	}{
		{name: "unknown status", status: []byte{'Z'}, wantErr: "status"},
		{name: "status with descriptor", status: []byte{'A'}, inState: true, pipe: true,
			fds: func(t *testing.T, s *sentinels) []int { return []int{brkRawFD(t, s.pipeW)} }, wantErr: "descriptor"},
		{name: "unknown frame byte", status: []byte{'A'}, frame: []byte{'x'}, pipe: true,
			fds: func(t *testing.T, s *sentinels) []int { return []int{brkRawFD(t, s.pipeW)} }, wantErr: "frame"},
		{name: "connection frame without descriptor", status: []byte{'A'}, frame: []byte{'c'}, wantErr: "descriptor"},
		{name: "connection frame with two descriptors", status: []byte{'A'}, frame: []byte{'c'}, pipe: true,
			fds: func(t *testing.T, s *sentinels) []int { return []int{brkRawFD(t, s.pipeW), brkRawFD(t, s.pipeW)} }, wantErr: "descriptor"},
		{name: "truncated ancillary data", status: []byte{'A'}, frame: []byte{'c'}, pipe: true,
			fds: func(t *testing.T, s *sentinels) []int {
				fds := make([]int, 64)
				for i := range fds {
					fds[i] = brkRawFD(t, s.pipeW)
				}
				return fds
			}, wantErr: "truncated"},
		{name: "pipe descriptor", status: []byte{'A'}, frame: []byte{'c'}, pipe: true,
			fds: func(t *testing.T, s *sentinels) []int { return []int{brkRawFD(t, s.pipeW)} }, wantErr: "socket type"},
		{name: "listening socket", status: []byte{'A'}, frame: []byte{'c'},
			fds: func(t *testing.T, s *sentinels) []int { return []int{brkRawFD(t, s.listener)} }, wantErr: "listening"},
		{name: "unix stream socket", status: []byte{'A'}, frame: []byte{'c'},
			fds: func(t *testing.T, s *sentinels) []int { return []int{brkRawFD(t, s.unixConn)} }, wantErr: "descriptor"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			ctx := brkContext(t)
			pipeR, pipeW := brkPipe(t)
			listener := brkLoopback(t, "tcp4")
			t.Cleanup(func() { brkClose(t, "listener", listener) })
			s := &sentinels{pipeW: pipeW, listener: listener, unixConn: brkFakeConn(t)}
			var fds []int
			if tt.fds != nil {
				fds = tt.fds(t, s)
			}
			sent := make(chan error, 1)
			gatewayDone := make(chan struct{})
			path := brkFakeBroker(t, func(c *net.UnixConn) {
				defer c.Close() //nolint:errcheck // test fake.
				sent <- brkSendFrames(c, tt.status, tt.frame, fds, tt.inState)
				// Keep the connection open until the gateway has failed, so
				// the descriptors are not discarded unread by the kernel.
				<-gatewayDone
			})
			addr := &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 443}
			ln, err := AttachBroker(ctx, path, brkUID(), addr)
			if err == nil {
				_, err = ln.Accept()
				if err2 := func() error { _, e := ln.Accept(); return e }(); err2 == nil {
					t.Error("second Accept after a malformed frame succeeded")
				}
				brkClose(t, "gateway listener", ln)
			}
			close(gatewayDone)
			if serr := <-sent; serr != nil {
				t.Fatalf("fake broker send: %v", serr)
			}
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("gateway error = %v; want error containing %q", err, tt.wantErr)
			}
			if tt.pipe {
				brkClose(t, "pipe write end", pipeW)
				brkAssertPipeEOF(t, pipeR)
			}
		})
	}
}

// brkFakeConn returns one end of a connected AF_UNIX stream socket.
func brkFakeConn(t *testing.T) *net.UnixConn {
	t.Helper()
	fds, err := unix.Socketpair(unix.AF_UNIX, unix.SOCK_STREAM, 0)
	if err != nil {
		t.Fatalf("Socketpair: %v", err)
	}
	var conns []*net.UnixConn
	for i, fd := range fds {
		f := os.NewFile(uintptr(fd), fmt.Sprintf("pair-%d", i)) //nolint:gosec // file descriptors are non-negative.
		c, err := net.FileConn(f)
		brkClose(t, "pair file", f)
		if err != nil {
			t.Fatalf("FileConn: %v", err)
		}
		uc := c.(*net.UnixConn)
		t.Cleanup(func() { brkClose(t, "pair conn", uc) })
		conns = append(conns, uc)
	}
	return conns[0]
}

func brkSendFrames(c *net.UnixConn, status, frame []byte, fds []int, inStatus bool) error {
	var oob []byte
	if len(fds) > 0 {
		oob = unix.UnixRights(fds...)
	}
	statusOOB, frameOOB := []byte(nil), oob
	if inStatus {
		statusOOB, frameOOB = oob, nil
	}
	if _, _, err := c.WriteMsgUnix(status, statusOOB, nil); err != nil {
		return fmt.Errorf("write status: %w", err)
	}
	if frame == nil {
		return nil
	}
	if _, _, err := c.WriteMsgUnix(frame, frameOOB, nil); err != nil {
		return fmt.Errorf("write frame: %w", err)
	}
	return nil
}

func TestAttachBrokerFailsWhenBrokerGoes(t *testing.T) {
	t.Parallel()
	ctx := brkContext(t)
	path := brkFakeBroker(t, func(c *net.UnixConn) {
		// Accept the gateway, then go away.
		_, _ = c.Write([]byte{'A'}) // a failed write surfaces as a gateway error
		_ = c.Close()               // the broker disappears
	})
	ln, err := AttachBroker(ctx, path, brkUID(), &net.TCPAddr{IP: net.IPv6loopback, Port: 443})
	if err != nil {
		t.Fatalf("AttachBroker: %v", err)
	}
	t.Cleanup(func() { brkClose(t, "gateway listener", ln) })
	if _, err := ln.Accept(); err == nil || errors.Is(err, net.ErrClosed) {
		t.Fatalf("Accept after broker exit = %v; want a broker error", err)
	}
}

func TestBrokerServeStopsOnCancel(t *testing.T) {
	t.Parallel()
	ctx := brkContext(t)
	h := brkStart(t, BrokerConfig{OwnerUID: brkUID(), MaxInFlight: 1})
	ln, err := AttachBroker(ctx, h.path, brkUID(), h.addrs[0])
	if err != nil {
		t.Fatalf("AttachBroker: %v", err)
	}
	t.Cleanup(func() { brkClose(t, "gateway listener", ln) })
	// The gateway never calls Accept, so the broker's accept loop waits on
	// the in-flight limit when Serve is canceled.
	for range 3 {
		c, err := net.DialTimeout("tcp", h.addrs[0].String(), 5*time.Second)
		if err != nil {
			t.Fatalf("dial: %v", err)
		}
		t.Cleanup(func() { brkClose(t, "client", c) })
	}
	time.Sleep(50 * time.Millisecond) // let the broker accept and block

	if err := h.stop(); err != nil {
		t.Fatalf("Serve: %v", err)
	}
	if err := h.broker.Serve(ctx); err == nil {
		t.Fatal("second Serve succeeded")
	}
	// Connections received before the broker stopped may still be handed
	// out; after them Accept must fail with a broker error.
	var acceptErr error
	for range 5 {
		c, err := ln.Accept()
		if err != nil {
			acceptErr = err
			break
		}
		brkClose(t, "delivered connection", c)
	}
	if acceptErr == nil || errors.Is(acceptErr, net.ErrClosed) {
		t.Fatalf("gateway Accept after broker stop = %v; want a broker error", acceptErr)
	}
	if c, err := net.DialTimeout("tcp", h.addrs[0].String(), time.Second); err == nil {
		brkClose(t, "client", c)
		t.Fatalf("TCP listener %s still accepts after Serve returned", h.addrs[0])
	}
	if _, err := os.Lstat(h.path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("control socket after Serve: %v; want it removed", err)
	}
}

func TestBrokerServeCanceledBeforeStart(t *testing.T) {
	t.Parallel()
	dir := brkDir(t)
	ul, err := ListenBrokerSocket(filepath.Join(dir, "b.sock"), brkUID())
	if err != nil {
		t.Fatalf("ListenBrokerSocket: %v", err)
	}
	t.Cleanup(func() { brkClose(t, "control", ul) })
	ln := brkLoopback(t, "tcp4")
	t.Cleanup(func() { brkClose(t, "listener", ln) })
	b, err := NewBroker(BrokerConfig{OwnerUID: brkUID(), Listeners: []*net.TCPListener{ln}, Control: ul})
	if err != nil {
		t.Fatalf("NewBroker: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := b.Serve(ctx); err != nil {
		t.Fatalf("Serve with canceled context = %v; want nil", err)
	}
	if c, err := net.DialTimeout("tcp", ln.Addr().String(), time.Second); err == nil {
		brkClose(t, "client", c)
		t.Fatal("TCP listener still open after Serve returned")
	}
}

func TestListenBrokerSocket(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		setup   func(t *testing.T, dir string) string // returns the socket path
		check   func(t *testing.T, path string)
		wantErr string
	}{
		{
			name:  "creates missing parent",
			setup: func(t *testing.T, dir string) string { return filepath.Join(dir, "new", "b.sock") },
			check: func(t *testing.T, path string) {
				fi, err := os.Lstat(filepath.Dir(path))
				if err != nil {
					t.Fatalf("Lstat parent: %v", err)
				}
				want := os.FileMode(0o700)
				if os.Geteuid() == 0 {
					want = 0o755
				}
				if !fi.IsDir() || fi.Mode().Perm() != want {
					t.Fatalf("parent mode %v, want a directory with %v", fi.Mode(), want)
				}
			},
		},
		{
			name:  "existing private parent",
			setup: func(t *testing.T, dir string) string { return filepath.Join(dir, "b.sock") },
		},
		{
			name: "group-writable parent",
			setup: func(t *testing.T, dir string) string {
				brkChmod(t, dir, 0o770)
				return filepath.Join(dir, "b.sock")
			},
			wantErr: "writable",
		},
		{
			name: "other-writable parent",
			setup: func(t *testing.T, dir string) string {
				brkChmod(t, dir, 0o703)
				return filepath.Join(dir, "b.sock")
			},
			wantErr: "writable",
		},
		{
			name: "symlinked parent",
			setup: func(t *testing.T, dir string) string {
				real := filepath.Join(dir, "real")
				if err := os.Mkdir(real, 0o700); err != nil {
					t.Fatalf("Mkdir: %v", err)
				}
				link := filepath.Join(dir, "link")
				if err := os.Symlink(real, link); err != nil {
					t.Fatalf("Symlink: %v", err)
				}
				return filepath.Join(link, "b.sock")
			},
			wantErr: "symlink",
		},
		{
			name: "parent owned by another user",
			setup: func(t *testing.T, dir string) string {
				if os.Geteuid() == 0 {
					t.Skip("running as root: / is owned by the current user")
				}
				return "/jailoc-broker-test.sock"
			},
			wantErr: "owned",
		},
		{
			name: "live socket",
			setup: func(t *testing.T, dir string) string {
				path := filepath.Join(dir, "b.sock")
				live, err := net.ListenUnix("unix", &net.UnixAddr{Name: path, Net: "unix"})
				if err != nil {
					t.Fatalf("ListenUnix: %v", err)
				}
				t.Cleanup(func() { brkClose(t, "live socket", live) })
				return path
			},
			check: func(t *testing.T, path string) {
				c, err := net.Dial("unix", path)
				if err != nil {
					t.Fatalf("live socket no longer answers: %v", err)
				}
				brkClose(t, "probe", c)
			},
			wantErr: "answers",
		},
		{
			name: "stale socket",
			setup: func(t *testing.T, dir string) string {
				path := filepath.Join(dir, "b.sock")
				stale, err := net.ListenUnix("unix", &net.UnixAddr{Name: path, Net: "unix"})
				if err != nil {
					t.Fatalf("ListenUnix: %v", err)
				}
				stale.SetUnlinkOnClose(false)
				brkClose(t, "stale socket", stale)
				return path
			},
		},
		{
			name: "regular file",
			setup: func(t *testing.T, dir string) string {
				path := filepath.Join(dir, "b.sock")
				if err := os.WriteFile(path, []byte("keep"), 0o600); err != nil {
					t.Fatalf("WriteFile: %v", err)
				}
				return path
			},
			check: func(t *testing.T, path string) {
				got, err := os.ReadFile(path)
				if err != nil || string(got) != "keep" {
					t.Fatalf("regular file after refusal = %q, %v; want it untouched", got, err)
				}
			},
			wantErr: "not a socket",
		},
		{
			name: "directory",
			setup: func(t *testing.T, dir string) string {
				path := filepath.Join(dir, "b.sock")
				if err := os.Mkdir(path, 0o700); err != nil {
					t.Fatalf("Mkdir: %v", err)
				}
				return path
			},
			wantErr: "not a socket",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			dir := brkDir(t)
			path := tt.setup(t, dir)
			ul, err := ListenBrokerSocket(path, brkUID())
			if tt.wantErr != "" {
				if err == nil {
					brkClose(t, "control", ul)
					t.Fatalf("ListenBrokerSocket succeeded; want error containing %q", tt.wantErr)
				}
				if !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("ListenBrokerSocket = %v; want error containing %q", err, tt.wantErr)
				}
				if tt.check != nil {
					tt.check(t, path)
				}
				return
			}
			if err != nil {
				t.Fatalf("ListenBrokerSocket: %v", err)
			}
			fi, err := os.Lstat(path)
			if err != nil {
				t.Fatalf("Lstat socket: %v", err)
			}
			if fi.Mode().Type() != os.ModeSocket || fi.Mode().Perm() != 0o600 {
				t.Fatalf("socket mode %v, want a 0600 socket", fi.Mode())
			}
			c, err := net.Dial("unix", path)
			if err != nil {
				t.Fatalf("dial new socket: %v", err)
			}
			brkClose(t, "probe", c)
			if tt.check != nil {
				tt.check(t, path)
			}
			brkClose(t, "control", ul)
			if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("socket after Close: %v; want it removed", err)
			}
		})
	}
}

func TestBrokerSocketDirs(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name        string
		path        string
		wantBase    string
		wantManaged []string
	}{
		{
			name:        "system socket",
			path:        BrokerSocketPath(501),
			wantBase:    "/var/run",
			wantManaged: []string{"/var/run/jailoc-gateway", "/var/run/jailoc-gateway/501"},
		},
		{
			name:        "other socket",
			path:        "/srv/a/b/broker.sock",
			wantBase:    "/srv/a",
			wantManaged: []string{"/srv/a/b"},
		},
		{
			name:        "socket below the system root without a uid directory",
			path:        "/var/run/jailoc-gateway/broker.sock",
			wantBase:    "/var/run",
			wantManaged: []string{"/var/run/jailoc-gateway"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			base, managed := brokerSocketDirs(tt.path)
			if base != tt.wantBase || strings.Join(managed, ",") != strings.Join(tt.wantManaged, ",") {
				t.Fatalf("brokerSocketDirs(%q) = %q, %q; want %q, %q", tt.path, base, managed, tt.wantBase, tt.wantManaged)
			}
		})
	}
}

// TestEnsureBrokerDirsAppliesModeToEachLevel is not parallel: it changes the
// process umask, which parallel tests would otherwise observe.
func TestEnsureBrokerDirsAppliesModeToEachLevel(t *testing.T) {
	base := brkDir(t)
	managed := []string{filepath.Join(base, "gw"), filepath.Join(base, "gw", "501")}
	// 0277 leaves the owner without write permission, so a level created
	// with the umask applied could not hold the next one.
	old := syscall.Umask(0o277)
	defer syscall.Umask(old)
	if err := ensureBrokerDirs(base, managed, os.Geteuid()); err != nil {
		t.Fatalf("ensureBrokerDirs: %v", err)
	}
	want := os.FileMode(0o700)
	if os.Geteuid() == 0 {
		want = 0o755
	}
	for _, dir := range managed {
		fi, err := os.Lstat(dir)
		if err != nil {
			t.Fatalf("Lstat %s: %v", dir, err)
		}
		if !fi.IsDir() || fi.Mode().Perm() != want {
			t.Fatalf("%s mode %v, want directory with %v", dir, fi.Mode(), want)
		}
	}
}

func TestEnsureBrokerDirsRefusesUnsafePaths(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		setup   func(t *testing.T, base string) []string
		wantErr string
	}{
		{
			name: "shared writable base",
			setup: func(t *testing.T, base string) []string {
				brkChmod(t, base, 0o777)
				return []string{filepath.Join(base, "gw"), filepath.Join(base, "gw", "501")}
			},
			wantErr: "writable",
		},
		{
			name: "symlinked upper level",
			setup: func(t *testing.T, base string) []string {
				real := filepath.Join(base, "real")
				if err := os.Mkdir(real, 0o700); err != nil {
					t.Fatalf("Mkdir: %v", err)
				}
				link := filepath.Join(base, "gw")
				if err := os.Symlink(real, link); err != nil {
					t.Fatalf("Symlink: %v", err)
				}
				return []string{link, filepath.Join(link, "501")}
			},
			wantErr: "symlink",
		},
		{
			name: "writable upper level",
			setup: func(t *testing.T, base string) []string {
				gw := filepath.Join(base, "gw")
				if err := os.Mkdir(gw, 0o700); err != nil {
					t.Fatalf("Mkdir: %v", err)
				}
				brkChmod(t, gw, 0o770)
				return []string{gw, filepath.Join(gw, "501")}
			},
			wantErr: "writable",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			base := brkDir(t)
			managed := tt.setup(t, base)
			err := ensureBrokerDirs(base, managed, os.Geteuid())
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("ensureBrokerDirs = %v; want error containing %q", err, tt.wantErr)
			}
			last := managed[len(managed)-1]
			if _, err := os.Lstat(last); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("%s after refusal: %v; want it not created", last, err)
			}
		})
	}
}

func TestEnsureBrokerDirsAcceptsStickyBase(t *testing.T) {
	t.Parallel()
	base := brkDir(t)
	brkChmod(t, base, 0o777|os.ModeSticky)
	managed := []string{filepath.Join(base, "gw")}
	if err := ensureBrokerDirs(base, managed, os.Geteuid()); err != nil {
		t.Fatalf("ensureBrokerDirs with sticky base: %v", err)
	}
}

func TestBrokerBaseWriters(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		mode os.FileMode
		uid  uint32
		gid  uint32
		want bool
	}{
		{name: "root 0755", mode: 0o755, uid: 0, gid: 0, want: true},
		{name: "macOS /var/run root:daemon 0775", mode: 0o775, uid: 0, gid: 1, want: true},
		{name: "root:wheel 0775", mode: 0o775, uid: 0, gid: 0, want: true},
		{name: "root with user group 0775", mode: 0o775, uid: 0, gid: 20, want: false},
		{name: "user-owned group writable", mode: 0o775, uid: 501, gid: 1, want: false},
		{name: "world writable", mode: 0o777, uid: 0, gid: 0, want: false},
		{name: "world writable sticky", mode: 0o777 | os.ModeSticky, uid: 0, gid: 0, want: true},
		{name: "group writable sticky", mode: 0o775 | os.ModeSticky, uid: 501, gid: 20, want: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := brokerBaseWritersTrusted(tt.mode, tt.uid, tt.gid); got != tt.want {
				t.Fatalf("brokerBaseWritersTrusted(%v, %d, %d) = %v, want %v", tt.mode, tt.uid, tt.gid, got, tt.want)
			}
		})
	}
}

func brkChmod(t *testing.T, path string, mode os.FileMode) {
	t.Helper()
	if err := os.Chmod(path, mode); err != nil {
		t.Fatalf("Chmod: %v", err)
	}
	t.Cleanup(func() {
		if err := os.Chmod(path, 0o700); err != nil {
			t.Errorf("restore mode of %s: %v", path, err)
		}
	})
}
