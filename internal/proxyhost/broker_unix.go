//go:build darwin || linux

package proxyhost

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"math"
	"net"
	"net/netip"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
)

// Listener broker protocol over a connected AF_UNIX stream socket.
//
// A privileged broker owns the loopback TCP listeners and never hands them
// out. The one attached gateway receives every accepted TCP connection as a
// descriptor instead:
//
//  1. The gateway connects. The broker checks the kernel-recorded peer UID
//     before writing anything and closes the connection on a mismatch.
//  2. The broker writes one status byte: brokerStatusAttached makes this
//     connection the single active receiver, brokerStatusBusy means another
//     gateway is attached and is followed by close. The gateway checks the
//     broker's peer UID before reading it.
//  3. For each accepted TCP connection the broker writes brokerFrameConn with
//     exactly one SCM_RIGHTS descriptor and closes its own copy. The gateway
//     answers every descriptor with one brokerFrameAck byte.
//
// Ownership of incoming connections lasts exactly as long as the gateway's
// Unix connection, so no process identity is tracked. The broker never reads
// HTTP or TLS.
//
// A second Unix socket, the health endpoint next to the control socket, lets
// a client check that the broker runs and serves its UID without taking the
// gateway slot: the broker checks the peer UID, writes one brokerStatusReady
// byte and closes. It is a separate socket because the control protocol
// attaches the first connection of the owner at once, before reading
// anything, so no first byte could tell a probe from a gateway. A broker
// that predates the endpoint cannot be probed without attaching, so the probe
// refuses it; restart or reinstall the broker to get the endpoint.
const (
	brokerStatusAttached byte = 'A'
	brokerStatusBusy     byte = 'B'
	brokerStatusReady    byte = 'R'
	brokerFrameConn      byte = 'c'
	brokerFrameAck       byte = 'k'

	defaultBrokerMaxInFlight  = 64
	defaultBrokerStallTimeout = 2 * time.Second
	defaultBrokerSendTimeout  = 2 * time.Second

	brokerSocketRoot   = "/var/run/jailoc-proxy"
	brokerSocketName   = "broker.sock"
	brokerProbeTimeout = 500 * time.Millisecond
	// brokerHealthSuffix is appended to the control socket path to name the
	// health endpoint.
	brokerHealthSuffix = ".health"
	// brokerHealthTimeout bounds one health probe, dial included.
	brokerHealthTimeout = 2 * time.Second
	brokerMaxBackoff    = time.Second
)

// ErrBrokerBusy reports that the listener broker already serves another
// proxy.
var ErrBrokerBusy = errors.New("another proxy is attached to the listener broker")

// BrokerConfig configures a listener broker. Zero limits select the
// defaults: 64 descriptors in flight, 2 s stall and send timeouts.
type BrokerConfig struct {
	// OwnerUID is the only UID allowed to attach a gateway.
	OwnerUID uint32
	// Listeners are loopback TCP listeners. The broker owns them and closes
	// them when Serve returns.
	Listeners []*net.TCPListener
	// Control is the Unix socket gateways attach to, usually from
	// ListenBrokerSocket. The broker closes it when Serve returns. NewBroker
	// binds the health endpoint next to it (see ProbeBroker), so Control must
	// be bound to a filesystem path.
	Control *net.UnixListener
	// HTTPPort and HTTPSPort are the two local ports the listeners serve.
	// The broker only reports them on its health endpoint, for
	// ProbeBrokerPair; it never uses them to treat connections differently.
	// Both zero means unreported (ProbeBrokerPair then refuses the broker);
	// otherwise both must be valid and distinct.
	HTTPPort, HTTPSPort uint16
	// MaxInFlight bounds descriptors queued or sent but not yet acknowledged.
	// When it is reached the accept loops wait and new clients stay in the
	// kernel listen backlog.
	MaxInFlight int
	// StallTimeout detaches a gateway that acknowledges nothing for this long
	// while descriptors are outstanding.
	StallTimeout time.Duration
	// SendTimeout detaches a gateway when sending one descriptor blocks for
	// longer than this.
	SendTimeout time.Duration
	// Logf receives diagnostics and may be called concurrently. Nil discards
	// them.
	Logf func(format string, args ...any)
}

// Broker accepts connections on loopback listeners and passes each one to
// the single attached gateway.
type Broker struct {
	owner        uint32
	listeners    []*net.TCPListener
	control      *net.UnixListener
	health       *net.UnixListener
	httpPort     uint16
	httpsPort    uint16
	maxInFlight  int
	stallTimeout time.Duration
	sendTimeout  time.Duration
	logf         func(format string, args ...any)

	wg sync.WaitGroup

	mu     sync.Mutex
	served bool
	closed bool
	active *brokerGateway
	nextID uint64
}

// NewBroker validates cfg. Every listener must be a TCP socket listening on
// a loopback address. The broker takes ownership of the listeners and the
// control socket only once Serve runs. NewBroker itself binds the health
// endpoint, so that it exists as soon as the control socket does; Serve
// closes and removes it, so a Broker that was built must be served.
func NewBroker(cfg BrokerConfig) (*Broker, error) {
	if len(cfg.Listeners) == 0 {
		return nil, errors.New("new listener broker: at least one listener is required")
	}
	if cfg.Control == nil {
		return nil, errors.New("new listener broker: control socket is nil")
	}
	if cfg.MaxInFlight < 0 {
		return nil, fmt.Errorf("new listener broker: MaxInFlight %d is negative", cfg.MaxInFlight)
	}
	if cfg.StallTimeout < 0 {
		return nil, fmt.Errorf("new listener broker: StallTimeout %s is negative", cfg.StallTimeout)
	}
	if cfg.SendTimeout < 0 {
		return nil, fmt.Errorf("new listener broker: SendTimeout %s is negative", cfg.SendTimeout)
	}
	for i, ln := range cfg.Listeners {
		if err := validateBrokerListener(ln); err != nil {
			return nil, fmt.Errorf("new listener broker: listener %d: %w", i, err)
		}
	}
	if err := validateBrokerPortConfig(cfg); err != nil {
		return nil, fmt.Errorf("new listener broker: %w", err)
	}
	health, err := listenBrokerHealth(cfg.Control, cfg.OwnerUID)
	if err != nil {
		return nil, fmt.Errorf("new listener broker: %w", err)
	}
	b := &Broker{
		owner:        cfg.OwnerUID,
		listeners:    append([]*net.TCPListener(nil), cfg.Listeners...),
		control:      cfg.Control,
		health:       health,
		httpPort:     cfg.HTTPPort,
		httpsPort:    cfg.HTTPSPort,
		maxInFlight:  cfg.MaxInFlight,
		stallTimeout: cfg.StallTimeout,
		sendTimeout:  cfg.SendTimeout,
		logf:         cfg.Logf,
	}
	if b.maxInFlight == 0 {
		b.maxInFlight = defaultBrokerMaxInFlight
	}
	if b.stallTimeout == 0 {
		b.stallTimeout = defaultBrokerStallTimeout
	}
	if b.sendTimeout == 0 {
		b.sendTimeout = defaultBrokerSendTimeout
	}
	if b.logf == nil {
		b.logf = func(string, ...any) {}
	}
	return b, nil
}

// validateBrokerPortConfig checks the reported ports of cfg. Unreported
// ports (both zero) need no check.
func validateBrokerPortConfig(cfg BrokerConfig) error {
	if cfg.HTTPPort == 0 && cfg.HTTPSPort == 0 {
		return nil
	}
	return ValidateBrokerPorts(int(cfg.HTTPPort), int(cfg.HTTPSPort))
}

func validateBrokerListener(ln *net.TCPListener) error {
	if ln == nil {
		return errors.New("listener is nil")
	}
	rc, err := ln.SyscallConn()
	if err != nil {
		return fmt.Errorf("listener descriptor: %w", err)
	}
	var verr error
	if err := rc.Control(func(fd uintptr) {
		_, verr = validateLoopbackTCPListenerFD(int(fd)) //nolint:gosec // file descriptors fit in int.
	}); err != nil {
		return fmt.Errorf("listener descriptor: %w", err)
	}
	if verr != nil {
		return fmt.Errorf("validate listener: %w", verr)
	}
	return nil
}

// Serve accepts gateways and TCP connections until ctx is done. On return
// the listeners, the control socket, the health endpoint and any attached
// gateway are closed and every goroutine of the broker has exited. It returns
// nil after ctx ends and an error only when called more than once.
func (b *Broker) Serve(ctx context.Context) error {
	b.mu.Lock()
	if b.served {
		b.mu.Unlock()
		return errors.New("serve listener broker: Serve was already called")
	}
	b.served = true
	b.mu.Unlock()

	b.wg.Go(func() { b.acceptUnix(ctx, b.control, "gateway", b.attach) })
	b.wg.Go(func() { b.acceptUnix(ctx, b.health, "health probe", b.answerHealth) })
	for _, ln := range b.listeners {
		b.wg.Go(func() { b.acceptConns(ctx, ln) })
	}
	<-ctx.Done()
	b.shutdown()
	b.wg.Wait()
	return nil
}

// shutdown closes everything the broker owns. Close errors cannot change
// the outcome of Serve, so they are reported through Logf.
func (b *Broker) shutdown() {
	b.mu.Lock()
	b.closed = true
	g := b.active
	b.mu.Unlock()
	for _, ln := range b.listeners {
		if err := ln.Close(); err != nil && !errors.Is(err, net.ErrClosed) {
			b.logf("listener broker: close listener %s: %v", ln.Addr(), err)
		}
	}
	if err := b.control.Close(); err != nil && !errors.Is(err, net.ErrClosed) {
		b.logf("listener broker: close control socket: %v", err)
	}
	if err := b.health.Close(); err != nil && !errors.Is(err, net.ErrClosed) {
		b.logf("listener broker: close health endpoint: %v", err)
	}
	if g != nil {
		b.detach(g, errors.New("broker stopped"))
	}
}

// listenBrokerHealth creates the health endpoint next to the control socket
// with the same directory checks, mode and owner.
func listenBrokerHealth(control *net.UnixListener, owner uint32) (*net.UnixListener, error) {
	addr, ok := control.Addr().(*net.UnixAddr)
	if !ok || addr.Name == "" || addr.Name[0] == '@' {
		return nil, fmt.Errorf("control socket %v is not bound to a filesystem path, so it has no health endpoint", control.Addr())
	}
	ul, err := ListenBrokerSocket(brokerHealthPath(addr.Name), owner)
	if err != nil {
		return nil, fmt.Errorf("listen on the health endpoint: %w", err)
	}
	return ul, nil
}

// brokerHealthPath is the health endpoint of the broker whose control socket
// is at controlPath.
func brokerHealthPath(controlPath string) string {
	return controlPath + brokerHealthSuffix
}

// acceptUnix serves ln until it is closed, running handle for each
// connection. what names the kind of client in diagnostics.
func (b *Broker) acceptUnix(ctx context.Context, ln *net.UnixListener, what string, handle func(*net.UnixConn)) {
	var backoff time.Duration
	for {
		uc, err := ln.AcceptUnix()
		if err != nil {
			if errors.Is(err, net.ErrClosed) {
				return
			}
			backoff = nextBackoff(backoff)
			b.logf("listener broker: accept %s: %v; retrying in %s", what, err, backoff)
			if !sleepContext(ctx, backoff) {
				return
			}
			continue
		}
		backoff = 0
		b.wg.Go(func() { handle(uc) })
	}
}

// answerHealth tells a probe of the owner that the broker serves, without
// touching the gateway slot. It checks the peer UID before writing anything.
func (b *Broker) answerHealth(uc *net.UnixConn) {
	defer b.closeControlConn(uc)
	if err := verifyUnixPeer(uc, b.owner); err != nil {
		b.logf("listener broker: refused health probe: %v", err)
		return
	}
	if err := uc.SetWriteDeadline(time.Now().Add(b.sendTimeout)); err != nil {
		b.logf("listener broker: set health status deadline: %v", err)
		return
	}
	// The ports follow the ready byte in the same write; a probe that only
	// wants the ready byte reads one byte and closes.
	reply := []byte{brokerStatusReady, 0, 0, 0, 0}
	binary.BigEndian.PutUint16(reply[1:], b.httpPort)
	binary.BigEndian.PutUint16(reply[3:], b.httpsPort)
	if _, err := uc.Write(reply); err != nil {
		b.logf("listener broker: send health status: %v", err)
	}
}

// attach checks the peer UID before writing anything, then either makes uc
// the active gateway or refuses it as busy.
func (b *Broker) attach(uc *net.UnixConn) {
	if err := verifyUnixPeer(uc, b.owner); err != nil {
		b.logf("listener broker: refused gateway: %v", err)
		b.closeControlConn(uc)
		return
	}
	b.mu.Lock()
	if b.closed {
		b.mu.Unlock()
		b.closeControlConn(uc)
		return
	}
	if b.active != nil {
		id := b.active.id
		b.mu.Unlock()
		b.refuseBusy(uc, id)
		return
	}
	b.nextID++
	g := &brokerGateway{
		id:     b.nextID,
		uc:     uc,
		queue:  make(chan *net.TCPConn, b.maxInFlight),
		done:   make(chan struct{}),
		notify: make(chan struct{}),
	}
	b.active = g
	b.mu.Unlock()

	if err := uc.SetWriteDeadline(time.Now().Add(b.sendTimeout)); err != nil {
		b.detach(g, fmt.Errorf("set status deadline: %w", err))
		return
	}
	if _, err := uc.Write([]byte{brokerStatusAttached}); err != nil {
		b.detach(g, fmt.Errorf("send attach status: %w", err))
		return
	}
	b.logf("listener broker: gateway %d attached", g.id)
	b.wg.Go(func() { b.readAcks(g) })
	b.wg.Go(func() { b.sendConns(g) })
	b.wg.Go(func() { b.watchStall(g) })
}

func (b *Broker) refuseBusy(uc *net.UnixConn, active uint64) {
	b.logf("listener broker: refused gateway: gateway %d is attached", active)
	if err := uc.SetWriteDeadline(time.Now().Add(b.sendTimeout)); err != nil {
		b.logf("listener broker: set busy status deadline: %v", err)
	} else if _, err := uc.Write([]byte{brokerStatusBusy}); err != nil {
		b.logf("listener broker: send busy status: %v", err)
	}
	b.closeControlConn(uc)
}

func (b *Broker) closeControlConn(uc *net.UnixConn) {
	if err := uc.Close(); err != nil && !errors.Is(err, net.ErrClosed) {
		b.logf("listener broker: close gateway connection: %v", err)
	}
}

// brokerGateway is one attached gateway. inFlight counts connections queued
// or sent but not acknowledged; it bounds len(queue), so sends to queue
// never block.
type brokerGateway struct {
	id    uint64
	uc    *net.UnixConn
	queue chan *net.TCPConn
	done  chan struct{}
	once  sync.Once

	mu       sync.Mutex
	inFlight int
	unacked  int       // sent and not yet acknowledged
	progress time.Time // last acknowledgement, or when inFlight left zero
	notify   chan struct{}
}

// signalLocked wakes everything waiting on the current notify channel.
func (g *brokerGateway) signalLocked() {
	close(g.notify)
	g.notify = make(chan struct{})
}

// reserve takes one in-flight slot. When none is free it returns a channel
// that is closed on the next acknowledgement.
func (g *brokerGateway) reserve(limit int) (bool, <-chan struct{}) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.inFlight >= limit {
		return false, g.notify
	}
	if g.inFlight == 0 {
		g.progress = time.Now()
		g.signalLocked()
	}
	g.inFlight++
	return true, nil
}

func (g *brokerGateway) markSent() {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.unacked++
}

func (g *brokerGateway) ack(n int) error {
	g.mu.Lock()
	defer g.mu.Unlock()
	if n > g.unacked {
		return fmt.Errorf("gateway acknowledged %d descriptors, %d were outstanding", n, g.unacked)
	}
	g.unacked -= n
	g.inFlight -= n
	g.progress = time.Now()
	g.signalLocked()
	return nil
}

// detach ends an attachment once. Clearing b.active happens under b.mu, the
// same lock deliver holds while queueing, so the drain below sees every
// connection that will ever be queued for g.
func (b *Broker) detach(g *brokerGateway, cause error) {
	g.once.Do(func() {
		close(g.done)
		b.closeControlConn(g.uc)
		b.mu.Lock()
		if b.active == g {
			b.active = nil
		}
		b.mu.Unlock()
		for {
			select {
			case c := <-g.queue:
				b.closeTCPConn(c, "undelivered connection")
				continue
			default:
			}
			break
		}
		b.logf("listener broker: gateway %d detached: %v", g.id, cause)
	})
}

// readAcks consumes acknowledgements. EOF, any other byte or any ancillary
// data ends the attachment.
func (b *Broker) readAcks(g *brokerGateway) {
	buf := make([]byte, 64)
	oob := make([]byte, unix.CmsgSpace(4*maxReceivedFDs))
	for {
		n, oobn, _, _, err := g.uc.ReadMsgUnix(buf, oob)
		// ReadMsgUnix can report -1 bytes alongside an error.
		n, oobn = max(n, 0), max(oobn, 0)
		if oobn > 0 {
			fds, perr := receivedRights(oob[:oobn])
			b.detach(g, errors.Join(errors.New("protocol violation: gateway sent ancillary data"), perr, closeFDs(fds)))
			return
		}
		for _, c := range buf[:n] {
			if c != brokerFrameAck {
				b.detach(g, fmt.Errorf("protocol violation: unexpected byte %q", c))
				return
			}
		}
		if n > 0 {
			if aerr := g.ack(n); aerr != nil {
				b.detach(g, fmt.Errorf("protocol violation: %w", aerr))
				return
			}
		}
		if err != nil {
			if errors.Is(err, io.EOF) {
				b.detach(g, errors.New("gateway closed the connection"))
			} else {
				b.detach(g, fmt.Errorf("read acknowledgements: %w", err))
			}
			return
		}
		if n == 0 {
			b.detach(g, errors.New("gateway closed the connection"))
			return
		}
	}
}

// sendConns passes queued connections to the gateway and closes the
// broker's copy of each one.
func (b *Broker) sendConns(g *brokerGateway) {
	for {
		select {
		case <-g.done:
			return
		case c := <-g.queue:
			err := b.sendConn(g, c)
			if cerr := c.Close(); cerr != nil {
				err = errors.Join(err, fmt.Errorf("close broker copy of connection: %w", cerr))
			}
			if err != nil {
				b.detach(g, err)
				return
			}
		}
	}
}

func (b *Broker) sendConn(g *brokerGateway, c *net.TCPConn) error {
	if err := g.uc.SetWriteDeadline(time.Now().Add(b.sendTimeout)); err != nil {
		return fmt.Errorf("set send deadline: %w", err)
	}
	rc, err := c.SyscallConn()
	if err != nil {
		return fmt.Errorf("connection descriptor: %w", err)
	}
	// Counted before the write: the gateway may acknowledge before
	// WriteMsgUnix returns here.
	g.markSent()
	var n, oobn, want int
	var werr error
	if err := rc.Control(func(fd uintptr) {
		oob := unix.UnixRights(int(fd)) //nolint:gosec // file descriptors fit in int.
		want = len(oob)
		n, oobn, werr = g.uc.WriteMsgUnix([]byte{brokerFrameConn}, oob, nil)
	}); err != nil {
		return fmt.Errorf("connection descriptor: %w", err)
	}
	if werr != nil {
		if errors.Is(werr, os.ErrDeadlineExceeded) {
			return fmt.Errorf("send connection: blocked for %s: %w", b.sendTimeout, werr)
		}
		return fmt.Errorf("send connection: %w", werr)
	}
	if n != 1 || oobn != want {
		return fmt.Errorf("send connection: wrote %d bytes and %d of %d ancillary bytes", n, oobn, want)
	}
	return nil
}

// watchStall detaches g when descriptors stay outstanding without any
// acknowledgement for the stall timeout.
func (b *Broker) watchStall(g *brokerGateway) {
	timer := time.NewTimer(b.stallTimeout)
	defer timer.Stop()
	for {
		g.mu.Lock()
		inFlight, since, wake := g.inFlight, g.progress, g.notify
		g.mu.Unlock()
		var fire <-chan time.Time
		if inFlight > 0 {
			left := b.stallTimeout - time.Since(since)
			if left <= 0 {
				b.detach(g, fmt.Errorf("gateway stalled: %d descriptors outstanding without acknowledgement for %s", inFlight, b.stallTimeout))
				return
			}
			timer.Reset(left)
			fire = timer.C
		}
		select {
		case <-g.done:
			return
		case <-wake:
		case <-fire:
		}
	}
}

// acceptConns accepts TCP connections on ln until it is closed.
func (b *Broker) acceptConns(ctx context.Context, ln *net.TCPListener) {
	var backoff time.Duration
	for {
		c, err := ln.AcceptTCP()
		if err != nil {
			if errors.Is(err, net.ErrClosed) {
				return
			}
			backoff = nextBackoff(backoff)
			b.logf("listener broker: accept on %s: %v; retrying in %s", ln.Addr(), err, backoff)
			if !sleepContext(ctx, backoff) {
				return
			}
			continue
		}
		backoff = 0
		b.deliver(ctx, c)
	}
}

// deliver queues c for the active gateway. While the gateway has the
// maximum number of descriptors in flight it waits, so later clients stay in
// the kernel backlog. Without a gateway, or once ctx is done while waiting,
// c is closed.
func (b *Broker) deliver(ctx context.Context, c *net.TCPConn) {
	for {
		b.mu.Lock()
		g := b.active
		if g == nil {
			b.mu.Unlock()
			b.closeTCPConn(c, "connection without a gateway")
			return
		}
		ok, wake := g.reserve(b.maxInFlight)
		if ok {
			g.queue <- c // cannot block: inFlight, which counts this entry, bounds the queue
			b.mu.Unlock()
			return
		}
		b.mu.Unlock()
		select {
		case <-wake:
		case <-g.done:
		case <-ctx.Done():
			b.closeTCPConn(c, "connection during shutdown")
			return
		}
	}
}

func (b *Broker) closeTCPConn(c *net.TCPConn, what string) {
	if err := c.Close(); err != nil && !errors.Is(err, net.ErrClosed) {
		b.logf("listener broker: close %s: %v", what, err)
	}
}

func nextBackoff(d time.Duration) time.Duration {
	if d == 0 {
		return 5 * time.Millisecond
	}
	return min(2*d, brokerMaxBackoff)
}

// sleepContext waits for d and reports false when ctx ended first.
func sleepContext(ctx context.Context, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return true
	case <-ctx.Done():
		return false
	}
}

// BrokerSocketPath is the control socket of the listener broker serving uid.
func BrokerSocketPath(uid uint32) string {
	return filepath.Join(brokerSocketRoot, strconv.FormatUint(uint64(uid), 10), brokerSocketName)
}

// ListenBrokerSocket creates the broker control socket at path with mode
// 0600, owned by ownerUID when running as root. The directories it manages
// (the socket's parent and, for the system socket path, the jailoc-gateway
// directory above it) are created top-down below a trusted base directory,
// each with mode 0755 as root and 0700 otherwise. Every managed directory must
// be a real directory owned by the effective UID and not writable by group or
// others. An existing socket that still answers is refused; a stale one is
// replaced. Anything else at path is refused and left alone.
func ListenBrokerSocket(path string, ownerUID uint32) (*net.UnixListener, error) {
	if path == "" {
		return nil, errors.New("listen broker socket: path is empty")
	}
	if ownerUID > math.MaxInt32 {
		return nil, fmt.Errorf("listen broker socket: uid %d exceeds %d", ownerUID, math.MaxInt32)
	}
	euid := os.Geteuid()
	base, managed := brokerSocketDirs(path)
	if err := ensureBrokerDirs(base, managed, euid); err != nil {
		return nil, fmt.Errorf("listen broker socket %s: %w", path, err)
	}
	if err := removeStaleSocket(path); err != nil {
		return nil, fmt.Errorf("listen broker socket %s: %w", path, err)
	}
	ul, err := net.ListenUnix("unix", &net.UnixAddr{Name: path, Net: "unix"})
	if err != nil {
		return nil, fmt.Errorf("listen broker socket: %w", err)
	}
	ul.SetUnlinkOnClose(true)
	// The socket exists with umask permissions until the chmod; the broker
	// checks every peer's UID, so a connection in that window gains nothing.
	if err := os.Chmod(path, 0o600); err != nil {
		return nil, errors.Join(fmt.Errorf("listen broker socket: %w", err), closeUnixListener(ul))
	}
	if euid == 0 {
		if err := os.Lchown(path, int(ownerUID), -1); err != nil {
			return nil, errors.Join(fmt.Errorf("listen broker socket: %w", err), closeUnixListener(ul))
		}
	}
	return ul, nil
}

func closeUnixListener(ul *net.UnixListener) error {
	if err := ul.Close(); err != nil {
		return fmt.Errorf("close broker socket: %w", err)
	}
	return nil
}

// brokerSocketDirs returns the trusted base directory and the directories
// below it that ListenBrokerSocket creates and checks for path, top-down.
// The system socket path manages both /var/run/jailoc-gateway and its per-UID
// directory; any other path manages only the socket's parent.
func brokerSocketDirs(path string) (base string, managed []string) {
	parent := filepath.Dir(filepath.Clean(path))
	if parent == brokerSocketRoot || filepath.Dir(parent) == brokerSocketRoot {
		managed = []string{brokerSocketRoot}
		if parent != brokerSocketRoot {
			managed = append(managed, parent)
		}
		return filepath.Dir(brokerSocketRoot), managed
	}
	return filepath.Dir(parent), []string{parent}
}

// ensureBrokerDirs checks base, then creates or checks each managed directory
// in order, so no directory is used before the ones above it are verified.
func ensureBrokerDirs(base string, managed []string, euid int) error {
	if err := checkBrokerBase(base, euid); err != nil {
		return fmt.Errorf("check socket directories: %w", err)
	}
	for _, dir := range managed {
		if err := ensureBrokerDir(dir, euid); err != nil {
			return fmt.Errorf("check socket directories: %w", err)
		}
	}
	return nil
}

// checkBrokerBase accepts a base directory owned by root or the effective UID
// whose writers are trusted (see brokerBaseWritersTrusted). It follows
// symlinks: /var/run is one on macOS.
func checkBrokerBase(base string, euid int) error {
	fi, err := os.Stat(base)
	if err != nil {
		return fmt.Errorf("inspect socket base directory: %w", err)
	}
	if !fi.IsDir() {
		return fmt.Errorf("socket base directory %s is not a directory", base)
	}
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok {
		return fmt.Errorf("socket base directory %s: no ownership information", base)
	}
	if st.Uid != 0 && int64(st.Uid) != int64(euid) {
		return fmt.Errorf("socket base directory %s is owned by uid %d, want root or %d", base, st.Uid, euid)
	}
	if !brokerBaseWritersTrusted(fi.Mode(), st.Uid, uint32(st.Gid)) {
		return fmt.Errorf("socket base directory %s has mode %v and group %d: writable by untrusted users", base, fi.Mode().Perm(), st.Gid)
	}
	return nil
}

// brokerBaseWritersTrusted reports whether only trusted users can remove or
// replace entries in a base directory with this mode and ownership. A sticky
// directory such as /tmp is safe: others cannot remove entries they do not
// own. Otherwise others must not write, and the group may write only when
// root owns the directory and the group is root (0) or daemon (1). macOS
// /var/run is root:daemon 0775; daemon members already run as system
// services.
func brokerBaseWritersTrusted(mode os.FileMode, uid, gid uint32) bool {
	if mode&fs.ModeSticky != 0 {
		return true
	}
	perm := mode.Perm()
	if perm&0o002 != 0 {
		return false
	}
	if perm&0o020 != 0 {
		return uid == 0 && gid <= 1
	}
	return true
}

// ensureBrokerDir creates dir if it is missing and checks it. It never
// follows a symlink at dir. As root it sets mode 0755 so the socket's owner
// can reach the socket; directories created as another user get 0700.
func ensureBrokerDir(dir string, euid int) error {
	mode := os.FileMode(0o700)
	if euid == 0 {
		mode = 0o755
	}
	if err := os.Mkdir(dir, mode); err != nil && !errors.Is(err, fs.ErrExist) {
		return fmt.Errorf("create socket directory: %w", err)
	}
	fi, err := os.Lstat(dir)
	if err != nil {
		return fmt.Errorf("inspect socket directory: %w", err)
	}
	if fi.Mode()&fs.ModeSymlink != 0 {
		return fmt.Errorf("socket directory %s is a symlink", dir)
	}
	if !fi.IsDir() {
		return fmt.Errorf("socket directory %s is not a directory", dir)
	}
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok {
		return fmt.Errorf("socket directory %s: no ownership information", dir)
	}
	if int64(st.Uid) != int64(euid) {
		return fmt.Errorf("socket directory %s is owned by uid %d, want %d", dir, st.Uid, euid)
	}
	if fi.Mode().Perm()&0o022 != 0 {
		return fmt.Errorf("socket directory %s has mode %v: group or other writable", dir, fi.Mode().Perm())
	}
	// Mkdir applied the umask, and an existing directory may be stricter than
	// needed. The directory is ours and not writable by others, so setting
	// the mode is safe; it lets the socket owner reach the socket.
	if fi.Mode().Perm() != mode && (euid == 0 || fi.Mode().Perm()&0o700 != 0o700) {
		if err := os.Chmod(dir, mode); err != nil {
			return fmt.Errorf("set socket directory mode: %w", err)
		}
	}
	return nil
}

// removeStaleSocket removes a socket at path that nothing answers on.
func removeStaleSocket(path string) error {
	fi, err := os.Lstat(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("inspect existing socket: %w", err)
	}
	if fi.Mode().Type() != fs.ModeSocket {
		return fmt.Errorf("%s exists and is not a socket", path)
	}
	c, err := net.DialTimeout("unix", path, brokerProbeTimeout)
	if err == nil {
		if cerr := c.Close(); cerr != nil {
			return errors.Join(fmt.Errorf("another process answers on %s", path), fmt.Errorf("close probe: %w", cerr))
		}
		return fmt.Errorf("another process answers on %s", path)
	}
	if !errors.Is(err, syscall.ECONNREFUSED) {
		return fmt.Errorf("probe existing socket: %w", err)
	}
	if err := os.Remove(path); err != nil {
		return fmt.Errorf("remove stale socket: %w", err)
	}
	return nil
}

func closeConnErr(c net.Conn) error {
	if err := c.Close(); err != nil {
		return fmt.Errorf("close broker connection: %w", err)
	}
	return nil
}

// brokerListenAddr accepts 127.0.0.0/8 or exactly ::1 with a port.
func brokerListenAddr(addr *net.TCPAddr) (netip.AddrPort, error) {
	if addr == nil {
		return netip.AddrPort{}, errors.New("listener address is nil")
	}
	ap := addr.AddrPort()
	ip := ap.Addr().Unmap().WithZone("")
	if !ip.IsValid() || !ip.IsLoopback() {
		return netip.AddrPort{}, fmt.Errorf("listener address %s is not a loopback address", addr)
	}
	if ap.Port() == 0 {
		return netip.AddrPort{}, fmt.Errorf("listener address %s has no port", addr)
	}
	return netip.AddrPortFrom(ip, ap.Port()), nil
}

// attachStatus verifies the broker's UID and then reads its status.
func attachStatus(ctx context.Context, uc *net.UnixConn, brokerUID uint32) error {
	status, err := readBrokerStatus(ctx, uc, brokerUID)
	if err != nil {
		return err
	}
	switch status {
	case brokerStatusAttached:
		return nil
	case brokerStatusBusy:
		return ErrBrokerBusy
	default:
		return fmt.Errorf("unexpected broker status %q", status)
	}
}

// readBrokerStatus verifies the broker's UID and reads its one status byte,
// which must not carry descriptors.
func readBrokerStatus(ctx context.Context, uc *net.UnixConn, brokerUID uint32) (byte, error) {
	if err := verifyUnixPeer(uc, brokerUID); err != nil {
		return 0, fmt.Errorf("verify broker: %w", err)
	}
	release, err := bindDeadline(ctx, uc)
	if err != nil {
		return 0, fmt.Errorf("read status: %w", err)
	}
	status, fds, err := readBrokerFrame(uc)
	if cerr := release(); cerr != nil {
		err = errors.Join(err, closeFDs(fds), cerr)
		fds = nil
	}
	if err != nil {
		return 0, fmt.Errorf("read status: %w", exchangeErr(ctx, err))
	}
	if len(fds) != 0 {
		return 0, errors.Join(fmt.Errorf("status carried %d descriptors, want none", len(fds)), closeFDs(fds))
	}
	return status, nil
}

// brokerPorts are the ports a broker reports on its health endpoint. A
// broker that predates the report, or was built without ports, leaves
// reported false.
type brokerPorts struct {
	http, https uint16
	reported    bool
}

// checkBrokerHealth asks the health endpoint of the broker whose control
// socket is at path whether it serves, without connecting to the control
// socket and so without taking the gateway slot. It checks that the broker
// runs as brokerUID before reading, and bounds the whole exchange by
// brokerHealthTimeout and ctx. With wantPorts it also reads the ports the
// broker reports after the ready byte.
func checkBrokerHealth(ctx context.Context, path string, brokerUID uint32, wantPorts bool) (brokerPorts, error) {
	ctx, cancel := context.WithTimeout(ctx, brokerHealthTimeout)
	defer cancel()
	health := brokerHealthPath(path)
	var d net.Dialer
	c, err := d.DialContext(ctx, "unix", health)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			// Lstat, never a connection: an older broker would attach it.
			if fi, serr := os.Lstat(path); serr == nil && fi.Mode().Type() == fs.ModeSocket {
				return brokerPorts{}, fmt.Errorf("broker at %s has no health endpoint %s: it is starting or predates the endpoint, restart or reinstall jailoc-listener-broker: %w", path, health, err)
			}
		}
		return brokerPorts{}, fmt.Errorf("dial health endpoint: %w", err)
	}
	uc, ok := c.(*net.UnixConn)
	if !ok {
		return brokerPorts{}, errors.Join(fmt.Errorf("health endpoint %s: dialed %T, want a Unix connection", health, c), closeConnErr(c))
	}
	status, err := readBrokerStatus(ctx, uc, brokerUID)
	var ports brokerPorts
	if err == nil && status == brokerStatusReady && wantPorts {
		ports, err = readBrokerPorts(ctx, uc)
	}
	if cerr := closeConnErr(uc); cerr != nil {
		err = errors.Join(err, cerr)
	}
	if err != nil {
		return brokerPorts{}, fmt.Errorf("health endpoint %s: %w", health, err)
	}
	if status != brokerStatusReady {
		return brokerPorts{}, fmt.Errorf("health endpoint %s: unexpected status %q", health, status)
	}
	return ports, nil
}

// readBrokerPorts reads the four port bytes that follow the ready byte of a
// health reply. A broker that predates them closes after the ready byte,
// which is not an error but an unreported result.
func readBrokerPorts(ctx context.Context, uc *net.UnixConn) (brokerPorts, error) {
	release, err := bindDeadline(ctx, uc)
	if err != nil {
		return brokerPorts{}, fmt.Errorf("read ports: %w", err)
	}
	var buf [4]byte
	_, err = io.ReadFull(uc, buf[:])
	if cerr := release(); cerr != nil {
		return brokerPorts{}, errors.Join(err, cerr)
	}
	if err != nil {
		if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
			return brokerPorts{}, nil
		}
		return brokerPorts{}, fmt.Errorf("read ports: %w", exchangeErr(ctx, err))
	}
	p := brokerPorts{http: binary.BigEndian.Uint16(buf[:2]), https: binary.BigEndian.Uint16(buf[2:])}
	p.reported = p.http != 0 && p.https != 0
	return p, nil
}

// readBrokerFrame reads one frame byte and its descriptors. A single-byte
// buffer keeps every recvmsg to one frame, so descriptors cannot be
// attributed to the wrong frame. On error every received descriptor is
// already closed.
func readBrokerFrame(uc *net.UnixConn) (byte, []int, error) {
	var b [1]byte
	oob := make([]byte, unix.CmsgSpace(4*maxReceivedFDs)+unix.CmsgSpace(64))
	n, oobn, flags, _, rerr := uc.ReadMsgUnix(b[:], oob)
	fds, perr := receivedRights(oob[:max(oobn, 0)])
	reject := func(err error) (byte, []int, error) {
		return 0, nil, errors.Join(err, closeFDs(fds))
	}
	switch {
	case n <= 0 && (rerr == nil || errors.Is(rerr, io.EOF)):
		return reject(errors.New("broker closed the connection"))
	case rerr != nil:
		return reject(fmt.Errorf("read frame: %w", rerr))
	case n != 1:
		return reject(fmt.Errorf("read %d frame bytes, want one", n))
	case flags&unix.MSG_CTRUNC != 0:
		return reject(errors.New("frame ancillary data truncated"))
	case perr != nil:
		return reject(perr)
	}
	return b[0], fds, nil
}

// brokerConnFromFD validates fd and converts it into a connection. It
// always consumes fd: net.FileConn works on its own duplicate.
func brokerConnFromFD(fd int) (*net.TCPConn, error) {
	if err := validateBrokerConnFD(fd); err != nil {
		return nil, errors.Join(fmt.Errorf("validate received descriptor: %w", err), closeFDs([]int{fd}))
	}
	f := os.NewFile(uintptr(fd), "broker-connection")
	if f == nil {
		return nil, fmt.Errorf("invalid received descriptor %d", fd)
	}
	c, err := net.FileConn(f)
	if err != nil {
		err = fmt.Errorf("convert received descriptor: %w", err)
	}
	if cerr := f.Close(); cerr != nil {
		err = errors.Join(err, fmt.Errorf("close received descriptor: %w", cerr))
	}
	if err != nil {
		if c != nil {
			err = errors.Join(err, closeConnErr(c))
		}
		return nil, err
	}
	tc, ok := c.(*net.TCPConn)
	if !ok {
		return nil, errors.Join(fmt.Errorf("received %T, want a TCP connection", c), closeConnErr(c))
	}
	return tc, nil
}

// validateBrokerConnFD checks that fd is a TCP stream socket that is not
// listening and whose local address is loopback (127.0.0.0/8 or exactly
// ::1).
func validateBrokerConnFD(fd int) error {
	sotype, err := unix.GetsockoptInt(fd, unix.SOL_SOCKET, unix.SO_TYPE)
	if err != nil {
		return fmt.Errorf("read socket type: %w", err)
	}
	if sotype != unix.SOCK_STREAM {
		return fmt.Errorf("socket type %d is not a stream socket", sotype)
	}
	if err := requireTCPProtocol(fd); err != nil {
		return fmt.Errorf("check socket protocol: %w", err)
	}
	sa, err := unix.Getsockname(fd)
	if err != nil {
		return fmt.Errorf("read socket address: %w", err)
	}
	ap, err := loopbackSocketAddr(sa, "connection")
	if err != nil {
		return fmt.Errorf("validate connection address: %w", err)
	}
	listening, err := socketIsListening(fd)
	if err != nil {
		return fmt.Errorf("read listening state: %w", err)
	}
	if listening {
		return fmt.Errorf("socket on %s is listening, want a connection", ap)
	}
	return nil
}
