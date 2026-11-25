package guest

import (
	"context"
	"encoding/binary"
	"errors"
	"io"
	"log/slog"
	"net"
	"slices"
	"sync"
	"testing"
	"time"
)

// proxyRelayHarness runs a proxyRelay on a loopback listener whose dial seam maps
// allowed guest ports to local test servers.
type proxyRelayHarness struct {
	relay  *proxyRelay
	addr   string
	target map[uint16]string
	mu     sync.Mutex
	dials  []uint16
}

func newProxyRelayHarness(t *testing.T, allowed []uint16, target map[uint16]string, opts ...func(*proxyRelay)) *proxyRelayHarness {
	t.Helper()
	h := &proxyRelayHarness{target: target}
	allow, err := proxyRelayAllowlist(allowed)
	if err != nil {
		t.Fatal(err)
	}
	h.relay = newProxyRelay(slog.New(slog.DiscardHandler), allow, func(ctx context.Context, port uint16) (net.Conn, error) {
		h.mu.Lock()
		h.dials = append(h.dials, port)
		h.mu.Unlock()
		addr, ok := h.target[port]
		if !ok {
			return nil, errors.New("no target")
		}
		var d net.Dialer
		return d.DialContext(ctx, "tcp", addr)
	})
	for _, o := range opts {
		o(h.relay)
	}
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	h.addr = l.Addr().String()
	h.relay.Serve(l)
	t.Cleanup(h.relay.Stop)
	return h
}

func (h *proxyRelayHarness) dialed() []uint16 {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]uint16(nil), h.dials...)
}

func (h *proxyRelayHarness) client(t *testing.T) net.Conn {
	t.Helper()
	c, err := net.Dial("tcp", h.addr)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.Close() })
	_ = c.SetDeadline(time.Now().Add(5 * time.Second))
	return c
}

func portBytes(p uint16) []byte {
	return binary.BigEndian.AppendUint16(nil, p)
}

func readStatus(t *testing.T, c net.Conn) byte {
	t.Helper()
	var b [1]byte
	if _, err := io.ReadFull(c, b[:]); err != nil {
		t.Fatalf("read status: %v", err)
	}
	return b[0]
}

// echoTarget serves one-shot targets: it echoes everything with a prefix once
// the client half-closes.
func echoTarget(t *testing.T) string {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = l.Close() })
	go func() {
		for {
			c, err := l.Accept()
			if err != nil {
				return
			}
			go func() {
				defer func() { _ = c.Close() }()
				req, _ := io.ReadAll(c)
				_, _ = c.Write(append([]byte("echo:"), req...))
			}()
		}
	}()
	return l.Addr().String()
}

func TestProxyRelayAllowlist(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		ports   []uint16
		wantErr bool
	}{
		{"ok", []uint16{8080, 3000}, false},
		{"duplicates collapse", []uint16{8080, 8080}, false},
		{"empty denies everything", nil, false},
		{"zero port", []uint16{0}, true},
		{"zero port among valid", []uint16{8080, 0}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			m, err := proxyRelayAllowlist(tt.ports)
			if (err != nil) != tt.wantErr {
				t.Fatalf("err = %v, wantErr %v", err, tt.wantErr)
			}
			if err == nil {
				if len(m) != len(slices.Compact(slices.Sorted(slices.Values(tt.ports)))) {
					t.Fatalf("allowlist = %v, want ports %v", m, tt.ports)
				}
				for _, p := range tt.ports {
					if !m[p] {
						t.Fatalf("port %d missing", p)
					}
				}
			}
		})
	}
}

func TestProxyRelayConnectedImmediateAppBytesAndHalfClose(t *testing.T) {
	t.Parallel()
	h := newProxyRelayHarness(t, []uint16{8080}, map[uint16]string{8080: echoTarget(t)})
	c := h.client(t)
	// The port and the first application bytes arrive in one write: the relay
	// must not consume past the 2-byte header.
	if _, err := c.Write(append(portBytes(8080), []byte("ping")...)); err != nil {
		t.Fatal(err)
	}
	if err := c.(*net.TCPConn).CloseWrite(); err != nil {
		t.Fatal(err)
	}
	if got := readStatus(t, c); got != proxyRelayConnected {
		t.Fatalf("status = %d, want connected", got)
	}
	got, err := io.ReadAll(c)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "echo:ping" {
		t.Fatalf("response = %q", got)
	}
}

func TestProxyRelayFragmentedHandshake(t *testing.T) {
	t.Parallel()
	h := newProxyRelayHarness(t, []uint16{8080}, map[uint16]string{8080: echoTarget(t)})
	c := h.client(t)
	b := portBytes(8080)
	if _, err := c.Write(b[:1]); err != nil {
		t.Fatal(err)
	}
	time.Sleep(50 * time.Millisecond)
	if _, err := c.Write(b[1:]); err != nil {
		t.Fatal(err)
	}
	_, _ = c.Write([]byte("x"))
	_ = c.(*net.TCPConn).CloseWrite()
	if got := readStatus(t, c); got != proxyRelayConnected {
		t.Fatalf("status = %d", got)
	}
	got, _ := io.ReadAll(c)
	if string(got) != "echo:x" {
		t.Fatalf("response = %q", got)
	}
}

func TestProxyRelayTruncatedHandshake(t *testing.T) {
	t.Parallel()
	h := newProxyRelayHarness(t, []uint16{8080}, map[uint16]string{8080: echoTarget(t)})
	c := h.client(t)
	if _, err := c.Write(portBytes(8080)[:1]); err != nil {
		t.Fatal(err)
	}
	_ = c.(*net.TCPConn).CloseWrite()
	got, err := io.ReadAll(c)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Fatalf("truncated handshake got response %v", got)
	}
	if d := h.dialed(); len(d) != 0 {
		t.Fatalf("dialed %v on truncated handshake", d)
	}
}

func TestProxyRelayHandshakeTimeout(t *testing.T) {
	t.Parallel()
	h := newProxyRelayHarness(t, []uint16{8080}, nil)
	h.relay.handshakeTimeout = 100 * time.Millisecond
	c := h.client(t)
	if _, err := c.Write(portBytes(8080)[:1]); err != nil {
		t.Fatal(err)
	}
	// The relay gives up and closes without a status.
	got, err := io.ReadAll(c)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("got %v", got)
	}
}

func TestProxyRelayDeniedBeforeDial(t *testing.T) {
	t.Parallel()
	target := echoTarget(t)
	h := newProxyRelayHarness(t, []uint16{8080}, map[uint16]string{8080: target, 9090: target, 0: target})
	for _, p := range []uint16{9090, 0, 22} {
		c := h.client(t)
		if _, err := c.Write(append(portBytes(p), []byte("GET /")...)); err != nil {
			t.Fatal(err)
		}
		if got := readStatus(t, c); got != proxyRelayDenied {
			t.Fatalf("port %d status = %d, want denied", p, got)
		}
		rest, _ := io.ReadAll(c)
		if len(rest) != 0 {
			t.Fatalf("port %d got payload after denial: %q", p, rest)
		}
	}
	if d := h.dialed(); len(d) != 0 {
		t.Fatalf("denied ports were dialed: %v", d)
	}
}

func TestProxyRelayTargetFailure(t *testing.T) {
	t.Parallel()
	// Allowed but nothing listens: the dial seam fails.
	h := newProxyRelayHarness(t, []uint16{8080}, map[uint16]string{})
	c := h.client(t)
	if _, err := c.Write(portBytes(8080)); err != nil {
		t.Fatal(err)
	}
	if got := readStatus(t, c); got != proxyRelayUnavailable {
		t.Fatalf("status = %d, want unavailable", got)
	}
	rest, _ := io.ReadAll(c)
	if len(rest) != 0 {
		t.Fatalf("payload after unavailable: %q", rest)
	}
	if d := h.dialed(); len(d) != 1 || d[0] != 8080 {
		t.Fatalf("dialed = %v", d)
	}
}

func TestProxyRelayFullDuplex(t *testing.T) {
	t.Parallel()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = l.Close() })
	go func() {
		c, err := l.Accept()
		if err != nil {
			return
		}
		defer func() { _ = c.Close() }()
		_, _ = io.Copy(c, c)
	}()
	h := newProxyRelayHarness(t, []uint16{8080}, map[uint16]string{8080: l.Addr().String()})
	c := h.client(t)
	if _, err := c.Write(portBytes(8080)); err != nil {
		t.Fatal(err)
	}
	if got := readStatus(t, c); got != proxyRelayConnected {
		t.Fatalf("status = %d", got)
	}
	for _, msg := range []string{"one", "two", "three"} {
		if _, err := c.Write([]byte(msg)); err != nil {
			t.Fatal(err)
		}
		buf := make([]byte, len(msg))
		if _, err := io.ReadFull(c, buf); err != nil {
			t.Fatal(err)
		}
		if string(buf) != msg {
			t.Fatalf("echo = %q, want %q", buf, msg)
		}
	}
}

func TestProxyRelayBoundedConnections(t *testing.T) {
	t.Parallel()
	h := newProxyRelayHarness(t, []uint16{8080}, map[uint16]string{8080: echoTarget(t)}, func(r *proxyRelay) { r.maxConns = 2 })
	var held []net.Conn
	for range 2 {
		c := h.client(t)
		if _, err := c.Write(portBytes(8080)); err != nil {
			t.Fatal(err)
		}
		if got := readStatus(t, c); got != proxyRelayConnected {
			t.Fatalf("status = %d", got)
		}
		held = append(held, c)
	}
	over := h.client(t)
	_, _ = over.Write(portBytes(8080))
	rest, err := io.ReadAll(over)
	if err != nil && !errors.Is(err, io.EOF) {
		// A reset also proves the connection was refused.
		t.Logf("over-limit read: %v", err)
	}
	if len(rest) != 0 {
		t.Fatalf("over-limit connection got %v", rest)
	}
	_ = held[0].Close()
	deadline := time.Now().Add(5 * time.Second)
	for {
		c := h.client(t)
		_, _ = c.Write(append(portBytes(8080), 'x'))
		_ = c.(*net.TCPConn).CloseWrite()
		var b [1]byte
		if _, err := io.ReadFull(c, b[:]); err == nil && b[0] == proxyRelayConnected {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("slot never freed after close")
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func TestProxyRelayStopClosesBothSidesNoLeak(t *testing.T) {
	t.Parallel()
	tl, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = tl.Close() })
	targetClosed := make(chan struct{})
	go func() {
		c, err := tl.Accept()
		if err != nil {
			return
		}
		defer func() { _ = c.Close() }()
		_, _ = io.Copy(io.Discard, c) // returns when the relay closes the target conn
		close(targetClosed)
	}()
	h := newProxyRelayHarness(t, []uint16{8080}, map[uint16]string{8080: tl.Addr().String()})
	c := h.client(t)
	if _, err := c.Write(portBytes(8080)); err != nil {
		t.Fatal(err)
	}
	if got := readStatus(t, c); got != proxyRelayConnected {
		t.Fatalf("status = %d", got)
	}
	// A second client stuck in the handshake is tracked too.
	idle := h.client(t)

	stopped := make(chan struct{})
	go func() { h.relay.Stop(); close(stopped) }()
	select {
	case <-stopped:
	case <-time.After(5 * time.Second):
		t.Fatal("Stop did not return")
	}
	select {
	case <-targetClosed:
	case <-time.After(5 * time.Second):
		t.Fatal("target connection not closed")
	}
	for name, conn := range map[string]net.Conn{"active": c, "idle": idle} {
		_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
		if _, err := conn.Read(make([]byte, 1)); err == nil {
			t.Fatalf("%s client read data after Stop", name)
		} else if ne, ok := err.(net.Error); ok && ne.Timeout() {
			t.Fatalf("%s client connection still open after Stop", name)
		}
	}
	if _, err := net.DialTimeout("tcp", h.addr, 500*time.Millisecond); err == nil {
		t.Fatal("listener still accepting after Stop")
	}
	h.relay.Stop() // idempotent
	if n := h.relay.activeConns(); n != 0 {
		t.Fatalf("activeConns = %d after Stop", n)
	}
}

func TestStartProxyRelayOn(t *testing.T) {
	t.Parallel()
	if _, err := startProxyRelayOn(slog.New(slog.DiscardHandler), []uint16{0}, "127.0.0.1:0"); err == nil {
		t.Fatal("port 0 accepted")
	}
	busy, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = busy.Close() })
	if _, err := startProxyRelayOn(slog.New(slog.DiscardHandler), []uint16{8080}, busy.Addr().String()); err == nil {
		t.Fatal("listen on a busy address succeeded")
	}
	for _, ports := range [][]uint16{{8080}, nil} {
		r, err := startProxyRelayOn(slog.New(slog.DiscardHandler), ports, "127.0.0.1:0")
		if err != nil {
			t.Fatalf("ports %v: %v", ports, err)
		}
		r.Stop()
	}
}

// duplexTarget echoes every connection until it closes.
func duplexTarget(t *testing.T) string {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = l.Close() })
	go func() {
		for {
			c, err := l.Accept()
			if err != nil {
				return
			}
			go func() {
				defer func() { _ = c.Close() }()
				_, _ = io.Copy(c, c)
			}()
		}
	}()
	return l.Addr().String()
}

// openSplice connects a client to port and waits until the relay spliced it.
func openSplice(t *testing.T, h *proxyRelayHarness, port uint16) net.Conn {
	t.Helper()
	c := h.client(t)
	if _, err := c.Write(portBytes(port)); err != nil {
		t.Fatal(err)
	}
	if got := readStatus(t, c); got != proxyRelayConnected {
		t.Fatalf("port %d status = %d, want connected", port, got)
	}
	return c
}

// echoRoundTrip reports whether msg comes back over c.
func echoRoundTrip(c net.Conn, msg string) bool {
	_ = c.SetDeadline(time.Now().Add(5 * time.Second))
	if _, err := c.Write([]byte(msg)); err != nil {
		return false
	}
	buf := make([]byte, len(msg))
	if _, err := io.ReadFull(c, buf); err != nil {
		return false
	}
	return string(buf) == msg
}

func TestProxyRelayEmptyAllowlistDeniesEverything(t *testing.T) {
	t.Parallel()
	h := newProxyRelayHarness(t, nil, map[uint16]string{8080: echoTarget(t)})
	c := h.client(t)
	if _, err := c.Write(portBytes(8080)); err != nil {
		t.Fatal(err)
	}
	if got := readStatus(t, c); got != proxyRelayDenied {
		t.Fatalf("status = %d, want denied", got)
	}
	if d := h.dialed(); len(d) != 0 {
		t.Fatalf("dialed %v with an empty allowlist", d)
	}
}

func TestProxyRelaySetPortsAdds(t *testing.T) {
	t.Parallel()
	target := duplexTarget(t)
	h := newProxyRelayHarness(t, nil, map[uint16]string{8080: target})
	c := h.client(t)
	if _, err := c.Write(portBytes(8080)); err != nil {
		t.Fatal(err)
	}
	if got := readStatus(t, c); got != proxyRelayDenied {
		t.Fatalf("before SetPorts status = %d, want denied", got)
	}
	if err := h.relay.SetPorts([]uint16{8080}); err != nil {
		t.Fatal(err)
	}
	if !echoRoundTrip(openSplice(t, h, 8080), "hello") {
		t.Fatal("no echo after the port was added")
	}
}

func TestProxyRelaySetPortsRemoves(t *testing.T) {
	t.Parallel()
	target := duplexTarget(t)
	h := newProxyRelayHarness(t, []uint16{8080, 9090}, map[uint16]string{8080: target, 9090: target})
	removed := openSplice(t, h, 8080)
	kept := openSplice(t, h, 9090)
	if !echoRoundTrip(removed, "a") || !echoRoundTrip(kept, "b") {
		t.Fatal("splices do not echo before SetPorts")
	}
	if err := h.relay.SetPorts([]uint16{9090}); err != nil {
		t.Fatal(err)
	}
	// The established splice to the removed port is closed on both sides.
	_ = removed.SetReadDeadline(time.Now().Add(5 * time.Second))
	if _, err := removed.Read(make([]byte, 1)); err == nil {
		t.Fatal("removed splice still delivers data")
	} else if ne, ok := err.(net.Error); ok && ne.Timeout() {
		t.Fatal("removed splice still open")
	}
	if !echoRoundTrip(kept, "still") {
		t.Fatal("splice to the kept port broke")
	}
	// New connections to the removed port are denied; the kept port still works.
	c := h.client(t)
	if _, err := c.Write(portBytes(8080)); err != nil {
		t.Fatal(err)
	}
	if got := readStatus(t, c); got != proxyRelayDenied {
		t.Fatalf("removed port status = %d, want denied", got)
	}
	if !echoRoundTrip(openSplice(t, h, 9090), "new") {
		t.Fatal("new splice to the kept port failed")
	}
	waitActive(t, h.relay, 2)
}

// waitActive waits until the relay tracks exactly n accepted connections.
func waitActive(t *testing.T, r *proxyRelay, n int) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for r.activeConns() != n {
		if time.Now().After(deadline) {
			t.Fatalf("activeConns = %d, want %d", r.activeConns(), n)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestProxyRelaySetPortsToEmptyClosesAll(t *testing.T) {
	t.Parallel()
	target := duplexTarget(t)
	h := newProxyRelayHarness(t, []uint16{8080}, map[uint16]string{8080: target})
	c := openSplice(t, h, 8080)
	if err := h.relay.SetPorts(nil); err != nil {
		t.Fatal(err)
	}
	_ = c.SetReadDeadline(time.Now().Add(5 * time.Second))
	if _, err := c.Read(make([]byte, 1)); err == nil {
		t.Fatal("splice still delivers data")
	} else if ne, ok := err.(net.Error); ok && ne.Timeout() {
		t.Fatal("splice still open")
	}
}

func TestProxyRelayRetainPorts(t *testing.T) {
	t.Parallel()
	target := duplexTarget(t)
	h := newProxyRelayHarness(t, []uint16{8080, 9090}, map[uint16]string{8080: target, 9090: target, 7070: target})
	removed := openSplice(t, h, 8080)
	kept := openSplice(t, h, 9090)
	// 7070 is in the list but not allowed, so RetainPorts must not add it.
	if err := h.relay.RetainPorts([]uint16{9090, 7070}); err != nil {
		t.Fatal(err)
	}
	_ = removed.SetReadDeadline(time.Now().Add(5 * time.Second))
	if _, err := removed.Read(make([]byte, 1)); err == nil {
		t.Fatal("removed splice still delivers data")
	} else if ne, ok := err.(net.Error); ok && ne.Timeout() {
		t.Fatal("removed splice still open")
	}
	if !echoRoundTrip(kept, "still") {
		t.Fatal("splice to the kept port broke")
	}
	for _, p := range []uint16{8080, 7070} {
		if h.relay.portAllowed(p) {
			t.Errorf("port %d allowed after RetainPorts, want denied", p)
		}
	}
	if err := h.relay.RetainPorts([]uint16{0}); err == nil {
		t.Fatal("port 0 accepted")
	}
	if !h.relay.portAllowed(9090) {
		t.Fatal("rejected RetainPorts changed the allowlist")
	}
}

func TestProxyRelaySetPortsRejectsInvalid(t *testing.T) {
	t.Parallel()
	h := newProxyRelayHarness(t, []uint16{8080}, map[uint16]string{8080: duplexTarget(t)})
	c := openSplice(t, h, 8080)
	if err := h.relay.SetPorts([]uint16{9090, 0}); err == nil {
		t.Fatal("port 0 accepted")
	}
	if !echoRoundTrip(c, "ok") {
		t.Fatal("established splice broke after a rejected SetPorts")
	}
	if !echoRoundTrip(openSplice(t, h, 8080), "ok") {
		t.Fatal("old allowlist lost after a rejected SetPorts")
	}
	d := h.client(t)
	if _, err := d.Write(portBytes(9090)); err != nil {
		t.Fatal(err)
	}
	if got := readStatus(t, d); got != proxyRelayDenied {
		t.Fatalf("rejected list took effect: status = %d", got)
	}
}

// A handshake that passed the allowlist check before SetPorts and dials after
// it must not survive.
func TestProxyRelaySetPortsDuringDial(t *testing.T) {
	t.Parallel()
	target := duplexTarget(t)
	dialing := make(chan struct{})
	proceed := make(chan struct{})
	h := newProxyRelayHarness(t, []uint16{8080}, map[uint16]string{8080: target}, func(r *proxyRelay) {
		inner := r.dial
		r.dial = func(ctx context.Context, port uint16) (net.Conn, error) {
			close(dialing)
			<-proceed
			return inner(ctx, port)
		}
	})
	c := h.client(t)
	if _, err := c.Write(portBytes(8080)); err != nil {
		t.Fatal(err)
	}
	<-dialing
	if err := h.relay.SetPorts(nil); err != nil {
		t.Fatal(err)
	}
	close(proceed)
	if got := readStatus(t, c); got != proxyRelayDenied {
		t.Fatalf("status = %d, want denied", got)
	}
	rest, _ := io.ReadAll(c)
	if len(rest) != 0 {
		t.Fatalf("payload after denial: %q", rest)
	}
}

func TestProxyRelaySetPortsConcurrent(t *testing.T) {
	t.Parallel()
	target := duplexTarget(t)
	h := newProxyRelayHarness(t, []uint16{8080}, map[uint16]string{8080: target})
	stop := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; ; i++ {
			select {
			case <-stop:
				return
			default:
			}
			ports := []uint16{8080}
			if i%2 == 1 {
				ports = nil
			}
			if err := h.relay.SetPorts(ports); err != nil {
				t.Error(err)
				return
			}
		}
	}()
	for range 20 {
		c, err := net.Dial("tcp", h.addr)
		if err != nil {
			t.Fatal(err)
		}
		_ = c.SetDeadline(time.Now().Add(5 * time.Second))
		_, _ = c.Write(portBytes(8080))
		var b [1]byte
		_, _ = io.ReadFull(c, b[:])
		_ = c.Close()
	}
	close(stop)
	wg.Wait()
	if err := h.relay.SetPorts([]uint16{8080}); err != nil {
		t.Fatal(err)
	}
	waitActive(t, h.relay, 0)
}
