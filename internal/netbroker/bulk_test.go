package netbroker

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"io"
	"net"
	"net/netip"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"gvisor.dev/gvisor/pkg/tcpip"
	"gvisor.dev/gvisor/pkg/tcpip/adapters/gonet"
	"gvisor.dev/gvisor/pkg/tcpip/network/ipv4"
)

// TestBrokerBulkTransfer pushes data through the broker in both directions
// on several connections at once, at the MTU jailoc uses, and checks that
// every byte arrives and nothing stalls.
func TestBrokerBulkTransfer(t *testing.T) {
	const (
		mtu   = 65520
		conns = 4
	)
	size := 8 << 20
	switch {
	case raceEnabled:
		size = 128 << 10
	case testing.Short():
		size = 1 << 20
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	t.Cleanup(cancel)

	pol, err := ParsePolicy([]byte(`{}`), "test")
	if err != nil {
		t.Fatalf("policy: %v", err)
	}
	ingress := freePort(t)
	b, err := New(Config{
		Subnet:   netip.MustParsePrefix("192.168.127.0/24"),
		Gateway:  netip.MustParseAddr("192.168.127.1"),
		GuestIP:  netip.MustParseAddr("192.168.127.2"),
		MTU:      mtu,
		Forwards: []Forward{{Host: netip.AddrPortFrom(netip.MustParseAddr("127.0.0.1"), ingress), GuestPort: 9000}},
		Log:      io.Discard,
	}, pol)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	sock := filepath.Join(t.TempDir(), "nic.sock")
	l, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	go func() { _ = b.Run(ctx, l) }()
	g := fakeGuestMTU(t, ctx, sock, mtu)

	// Guest echo server: every byte received is sent back.
	gl, err := gonet.ListenTCP(g, tcpip.FullAddress{NIC: 1, Port: 9000}, ipv4.ProtocolNumber)
	if err != nil {
		t.Fatalf("guest listen: %v", err)
	}
	t.Cleanup(func() { _ = gl.Close() })
	go func() {
		for {
			c, err := gl.Accept()
			if err != nil {
				return
			}
			go func() {
				defer func() { _ = c.Close() }()
				_, _ = io.Copy(c, c)
			}()
		}
	}()

	var wg sync.WaitGroup
	errs := make(chan error, conns)
	start := time.Now()
	for range conns {
		wg.Go(func() {
			payload := make([]byte, size)
			_, _ = rand.Read(payload)
			want := sha256.Sum256(payload)
			c, err := dialRetry(ctx, netip.AddrPortFrom(netip.MustParseAddr("127.0.0.1"), ingress).String())
			if err != nil {
				errs <- err
				return
			}
			defer func() { _ = c.Close() }()
			if dl, ok := ctx.Deadline(); ok {
				_ = c.SetDeadline(dl)
			}
			go func() {
				_, _ = io.Copy(c, bytes.NewReader(payload))
				_ = c.(*net.TCPConn).CloseWrite()
			}()
			h := sha256.New()
			n, err := io.Copy(h, c)
			if err != nil {
				errs <- err
				return
			}
			if n != int64(size) || !bytes.Equal(h.Sum(nil), want[:]) {
				errs <- io.ErrUnexpectedEOF
			}
		})
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatalf("transfer: %v", err)
	}
	t.Logf("%d connections echoed %d KiB each in %s", conns, size>>10, time.Since(start).Round(time.Millisecond))
}

// dialRetry dials addr until the broker's forward listener accepts.
func dialRetry(ctx context.Context, addr string) (net.Conn, error) {
	for {
		c, err := net.DialTimeout("tcp", addr, 5*time.Second)
		if err == nil {
			return c, nil
		}
		select {
		case <-ctx.Done():
			return nil, err
		case <-time.After(20 * time.Millisecond):
		}
	}
}
