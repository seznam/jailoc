//go:build darwin || linux

package proxyhost

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"sync"
	"time"
)

const (
	// DefaultBrokerHTTPPort and DefaultBrokerHTTPSPort are the loopback ports
	// the listener broker binds when it is given no port flags.
	DefaultBrokerHTTPPort  = 80
	DefaultBrokerHTTPSPort = 443
)

// ValidateBrokerPorts checks the two TCP ports of the listener broker: both
// must be in 1..65535 and they must differ.
func ValidateBrokerPorts(httpPort, httpsPort int) error {
	for _, p := range []struct {
		name string
		port int
	}{{"HTTP", httpPort}, {"HTTPS", httpsPort}} {
		if p.port < 1 || p.port > 65535 {
			return fmt.Errorf("%s port %d is outside 1-65535", p.name, p.port)
		}
	}
	if httpPort == httpsPort {
		return fmt.Errorf("HTTP and HTTPS ports must differ, both are %d", httpPort)
	}
	return nil
}

// AttachBroker attaches to the listener broker at path as its gateway. It
// checks that the broker runs as brokerUID before reading its status, and
// returns an error wrapping ErrBrokerBusy when another gateway is attached.
// The returned listener reports addr, which must be a loopback address with
// a port, and yields every connection the broker passes, whatever local port
// it arrived on, as a validated *net.TCPConn. Closing it detaches from the
// broker. Use AttachBrokerPair for a broker that serves an HTTP and an HTTPS
// port.
func AttachBroker(ctx context.Context, path string, brokerUID uint32, addr *net.TCPAddr) (net.Listener, error) {
	laddr, err := brokerListenAddr(addr)
	if err != nil {
		return nil, fmt.Errorf("attach listener broker: %w", err)
	}
	gw, err := attachBrokerClient(ctx, path, brokerUID, []*brokerListener{{addr: laddr}})
	if err != nil {
		return nil, err
	}
	return gw.listeners[0], nil
}

// AttachBrokerPair attaches to the listener broker at path as its one
// gateway, like AttachBroker, and splits what the broker passes into two
// logical listeners by the local port each connection actually arrived on:
// httpLn yields connections to the port of httpAddr, httpsLn those to the
// port of httpsAddr. Both addresses must be loopback addresses with a port
// and the two ports must differ. The address families do not matter: a
// connection to [::1]:P and one to 127.0.0.1:P go to the same listener. The
// gateway never reads from a connection to classify it.
//
// A connection on any other local port means the broker was installed with
// different ports; the attachment then fails, both listeners return an error
// from Accept naming the ports and the connection is closed, instead of
// serving it on the wrong listener. Call ProbeBrokerPair first to learn of
// such a mismatch before attaching.
//
// The attachment ends when both listeners are closed. Closing one makes its
// Accept fail with net.ErrClosed, drops (closes) later connections for it
// and leaves the other listener serving. Connections for a listener nobody
// accepts from hold up the other one, as the broker detaches a gateway that
// acknowledges nothing for its stall timeout; keep both accept loops running.
func AttachBrokerPair(ctx context.Context, path string, brokerUID uint32, httpAddr, httpsAddr *net.TCPAddr) (httpLn, httpsLn net.Listener, err error) {
	httpAP, err := brokerListenAddr(httpAddr)
	if err != nil {
		return nil, nil, fmt.Errorf("attach listener broker: HTTP listener: %w", err)
	}
	httpsAP, err := brokerListenAddr(httpsAddr)
	if err != nil {
		return nil, nil, fmt.Errorf("attach listener broker: HTTPS listener: %w", err)
	}
	if err := ValidateBrokerPorts(int(httpAP.Port()), int(httpsAP.Port())); err != nil {
		return nil, nil, fmt.Errorf("attach listener broker: %w", err)
	}
	gw, err := attachBrokerClient(ctx, path, brokerUID, []*brokerListener{
		{addr: httpAP, port: httpAP.Port()},
		{addr: httpsAP, port: httpsAP.Port()},
	})
	if err != nil {
		return nil, nil, err
	}
	return gw.listeners[0], gw.listeners[1], nil
}

func attachBrokerClient(ctx context.Context, path string, brokerUID uint32, listeners []*brokerListener) (*brokerClient, error) {
	var d net.Dialer
	c, err := d.DialContext(ctx, "unix", path)
	if err != nil {
		return nil, fmt.Errorf("attach listener broker: %w", err)
	}
	uc, ok := c.(*net.UnixConn)
	if !ok {
		return nil, errors.Join(fmt.Errorf("attach listener broker %s: dialed %T, want a Unix connection", path, c), closeConnErr(c))
	}
	if err := attachStatus(ctx, uc, brokerUID); err != nil {
		return nil, errors.Join(fmt.Errorf("attach listener broker %s: %w", path, err), closeConnErr(uc))
	}
	gw := &brokerClient{
		uc:        uc,
		listeners: listeners,
		open:      len(listeners),
		allClosed: make(chan struct{}),
		failed:    make(chan struct{}),
		loopDone:  make(chan struct{}),
	}
	for _, l := range listeners {
		l.gw = gw
		l.conns = make(chan *net.TCPConn)
		l.closed = make(chan struct{})
	}
	go gw.receive()
	return gw, nil
}

// brokerClient is the gateway side of one attachment: it receives the
// descriptors and routes each connection to one of its listeners.
type brokerClient struct {
	uc        *net.UnixConn
	listeners []*brokerListener

	mu        sync.Mutex
	open      int           // listeners not yet closed
	allClosed chan struct{} // closed with the last listener
	closeErr  error

	failed   chan struct{} // closed after err is set
	err      error
	loopDone chan struct{}
}

func (g *brokerClient) receive() {
	defer close(g.loopDone)
	g.err = g.receiveLoop()
	close(g.failed)
	// Give the slot back to the broker at once. An error means every
	// listener's Close has already closed the connection.
	_ = g.uc.Close()
}

// receiveLoop hands each received connection to Accept of its listener and
// acknowledges it once Accept has taken it, so a gateway that stops
// accepting stops acknowledging and the broker applies backpressure.
func (g *brokerClient) receiveLoop() error {
	for {
		frame, fds, err := readBrokerFrame(g.uc)
		if err != nil {
			select {
			case <-g.allClosed:
				return fmt.Errorf("broker listener: %w", net.ErrClosed)
			default:
			}
			return fmt.Errorf("receive broker connection: %w", err)
		}
		if frame != brokerFrameConn {
			return errors.Join(fmt.Errorf("receive broker connection: unexpected broker frame %q", frame), closeFDs(fds))
		}
		if len(fds) != 1 {
			return errors.Join(fmt.Errorf("receive broker connection: connection frame carried %d descriptors, want exactly one descriptor", len(fds)), closeFDs(fds))
		}
		c, err := brokerConnFromFD(fds[0])
		if err != nil {
			return fmt.Errorf("receive broker connection: %w", err)
		}
		l, err := g.route(c)
		if err != nil {
			return errors.Join(fmt.Errorf("receive broker connection: %w", err), closeConnErr(c))
		}
		select {
		case l.conns <- c:
		case <-l.closed:
			cerr := closeConnErr(c)
			select {
			case <-g.allClosed:
				return errors.Join(fmt.Errorf("broker listener: %w", net.ErrClosed), cerr)
			default:
			}
			// Only this listener is closed: drop the connection and keep
			// serving the other one.
			if cerr != nil {
				return fmt.Errorf("drop connection for a closed listener: %w", cerr)
			}
		}
		if err := g.uc.SetWriteDeadline(time.Now().Add(fdExchangeTimeout)); err != nil {
			return fmt.Errorf("acknowledge broker connection: %w", err)
		}
		if _, err := g.uc.Write([]byte{brokerFrameAck}); err != nil {
			return fmt.Errorf("acknowledge broker connection: %w", err)
		}
	}
}

// route picks the listener for c by the local port the connection actually
// arrived on. A listener without a port takes everything.
func (g *brokerClient) route(c *net.TCPConn) (*brokerListener, error) {
	local, ok := c.LocalAddr().(*net.TCPAddr)
	if !ok {
		return nil, fmt.Errorf("connection local address %v is not a TCP address", c.LocalAddr())
	}
	for _, l := range g.listeners {
		if l.port == 0 || int(l.port) == local.Port {
			return l, nil
		}
	}
	var want []string
	for _, l := range g.listeners {
		want = append(want, fmt.Sprint(l.port))
	}
	return nil, fmt.Errorf("connection arrived on local port %d, want one of %v: the broker was installed with different ports than this proxy uses, reinstall jailoc-listener-broker", local.Port, want)
}

// release records that one listener closed. The last one detaches from the
// broker and waits for the receive loop to end.
func (g *brokerClient) release() error {
	g.mu.Lock()
	g.open--
	last := g.open == 0
	if last {
		close(g.allClosed)
		if err := g.uc.Close(); err != nil && !errors.Is(err, net.ErrClosed) {
			g.closeErr = fmt.Errorf("close broker listener: %w", err)
		}
	}
	g.mu.Unlock()
	if !last {
		return nil
	}
	<-g.loopDone
	return g.closeErr
}

// brokerListener is one logical listener of an attachment: a net.Listener
// fed by the broker.
type brokerListener struct {
	gw   *brokerClient
	addr netip.AddrPort
	port uint16 // local port served; 0 serves every connection

	conns     chan *net.TCPConn
	closed    chan struct{}
	closeOnce sync.Once
}

// Accept returns the next connection received from the broker for this
// listener.
func (l *brokerListener) Accept() (net.Conn, error) {
	select {
	case <-l.closed:
		return nil, fmt.Errorf("accept broker connection: %w", net.ErrClosed)
	default:
	}
	select {
	case c := <-l.conns:
		return c, nil
	case <-l.closed:
		return nil, fmt.Errorf("accept broker connection: %w", net.ErrClosed)
	case <-l.gw.failed:
		return nil, fmt.Errorf("accept broker connection: %w", l.gw.err)
	}
}

// Close closes this listener. Closing the last listener of an attachment
// detaches from the broker and waits for the receive loop to end. Calling it
// again returns nil.
func (l *brokerListener) Close() error {
	var err error
	l.closeOnce.Do(func() {
		close(l.closed)
		err = l.gw.release()
	})
	return err
}

// Addr returns the loopback address this listener was attached with.
func (l *brokerListener) Addr() net.Addr {
	return net.TCPAddrFromAddrPort(l.addr)
}
