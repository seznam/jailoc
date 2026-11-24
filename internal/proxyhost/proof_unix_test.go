//go:build darwin || linux

package proxyhost

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"errors"
	"fmt"
	"io"
	"log"
	"math/big"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

// Check is one row of the host proof report.
type Check struct {
	Name   string
	Passed bool
	Detail string
}

// ProofResult is the ordered list of checks RunProof executed.
type ProofResult struct {
	Checks []Check
}

const (
	proofTimeout = 15 * time.Second
	stopWait     = 2 * time.Second
	proofHost    = "proof.jl.local"
	wrongHost    = "wrong.jl.local"
	proofBody    = "jailoc proxy host-proof\n"
	unrelated    = "# unrelated edit\n192.0.2.7 unrelated.invalid\n"
)

// RunProof runs an unprivileged, bounded, same-user proof: a loopback listener
// broker, two sequential gateways in the same process, TLS with an in-memory CA, and
// an exact-ownership hosts-file plan on a temporary fixture. It does not touch
// the system hosts file, trust stores, credential files or any privileged port,
// and it makes no claim about DNS, OS or browser trust, other users or root.
func RunProof(ctx context.Context) (res ProofResult, err error) {
	p := &proof{uid: uint32(os.Geteuid())} //nolint:gosec // effective uid is never negative
	if os.Geteuid() == 0 {
		err = errors.New("refusing to run with effective uid 0; run the proof unprivileged")
		p.res.Checks = append(p.res.Checks, Check{Name: "unprivileged", Detail: err.Error()})
		return p.res, fmt.Errorf("run host proof: %w", err)
	}
	ctx, cancel := context.WithTimeout(ctx, proofTimeout) // keeps a shorter caller deadline
	defer cancel()

	// Darwin's Unix socket paths are limited to 104 bytes; respect a short
	// TMPDIR (also useful when /tmp is full), but avoid long /var/folders paths.
	base := os.TempDir()
	if len(base) > 60 {
		base = "/tmp"
	}
	if p.root, err = os.MkdirTemp(base, "jlgp-"); err != nil {
		return p.res, fmt.Errorf("create proof root: %w", err)
	}
	defer func() {
		cerr := p.closeAll()
		if rerr := os.RemoveAll(p.root); rerr != nil {
			cerr = errors.Join(cerr, fmt.Errorf("remove proof root: %w", rerr))
		}
		if _, serr := os.Lstat(p.root); cerr == nil && !errors.Is(serr, os.ErrNotExist) {
			cerr = fmt.Errorf("proof root still present: %v", serr)
		}
		row := Check{Name: "cleanup", Passed: cerr == nil, Detail: "gateway servers and attachments closed; broker stopped; listeners, control socket and temporary root removed"}
		if cerr != nil {
			row.Detail = cerr.Error()
			err = errors.Join(err, fmt.Errorf("cleanup: %w", cerr))
		}
		p.res.Checks = append(p.res.Checks, row)
		res = p.res
	}()
	if err = p.step(ctx, "unprivileged", "non-root effective uid; private temp root", func() error { return os.Chmod(p.root, 0o700) }); err != nil { //nolint:gosec // p.root is a directory.
		return p.res, fmt.Errorf("run host proof: %w", err)
	}
	err = p.run(ctx)
	if err != nil {
		return p.res, fmt.Errorf("run host proof: %w", err)
	}
	return p.res, nil
}

type closer struct {
	name string
	fn   func() error
}

type proof struct {
	res     ProofResult
	root    string
	uid     uint32
	closers []closer
}

func (p *proof) own(name string, fn func() error) { p.closers = append(p.closers, closer{name, fn}) }

// closeAll closes owned resources in reverse order; already-closed is fine.
func (p *proof) closeAll() error {
	var errs []error
	for i := len(p.closers) - 1; i >= 0; i-- {
		c := p.closers[i]
		if err := c.fn(); err != nil && !errors.Is(err, net.ErrClosed) && !errors.Is(err, os.ErrClosed) {
			errs = append(errs, fmt.Errorf("close %s: %w", c.name, err))
		}
	}
	p.closers = nil
	return errors.Join(errs...)
}

// step records one check; it fails fast on an already-done context and wraps
// the context error into any failure that happened while it was done.
func (p *proof) step(ctx context.Context, name, detail string, fn func() error) error {
	err := ctx.Err()
	if err == nil {
		err = fn()
		if err != nil && ctx.Err() != nil && !errors.Is(err, ctx.Err()) {
			err = errors.Join(err, fmt.Errorf("context ended: %w", ctx.Err()))
		}
	}
	if err != nil {
		p.res.Checks = append(p.res.Checks, Check{Name: name, Detail: err.Error()})
		return fmt.Errorf("%s: %w", name, err)
	}
	p.res.Checks = append(p.res.Checks, Check{Name: name, Passed: true, Detail: detail})
	return nil
}

func (p *proof) run(ctx context.Context) error {
	var (
		ca, otherCA *authority
		leaf        tls.Certificate
		ul          *net.UnixListener
		addr        *net.TCPAddr
		srv         *proofServer
	)
	sock := filepath.Join(p.root, "s")
	steps := []struct {
		name, detail string
		fn           func() error
	}{
		{"memory-ca", "ECDSA P-256 CA and proof.jl.local serverAuth leaf generated in memory only", func() (e error) {
			now := time.Now()
			if ca, e = newAuthority("jailoc proof CA", now); e != nil {
				return fmt.Errorf("new CA: %w", e)
			}
			if otherCA, e = newAuthority("jailoc unrelated proof CA", now); e != nil {
				return fmt.Errorf("new unrelated CA: %w", e)
			}
			leaf, e = ca.issueLeaf(proofHost, now)
			if e != nil {
				return fmt.Errorf("issue leaf: %w", e)
			}
			return nil
		}},
		{"private-socket", "temp root 0700, AF_UNIX socket 0600, both owned by the effective uid", func() (e error) {
			if ul, e = ListenBrokerSocket(sock, p.uid); e != nil {
				return fmt.Errorf("listen broker control socket: %w", e)
			}
			p.own("broker control socket", ul.Close)
			if e = checkPrivate(p.root, sock, 0o700, 0o600, int(p.uid)); e != nil {
				return fmt.Errorf("check private modes: %w", e)
			}
			return nil
		}},
		{"broker-listener", "broker owns an ephemeral 127.0.0.1 TCP listener and forwards accepted connections, not the listener", func() error {
			tcp, e := net.ListenTCP("tcp4", &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1)})
			if e != nil {
				return fmt.Errorf("listen tcp: %w", e)
			}
			p.own("tcp listener", tcp.Close)
			addr = tcp.Addr().(*net.TCPAddr)
			if addr.Port < 1024 {
				return fmt.Errorf("ephemeral listener selected privileged port %d", addr.Port)
			}
			broker, e := NewBroker(BrokerConfig{OwnerUID: p.uid, Listeners: []*net.TCPListener{tcp}, Control: ul})
			if e != nil {
				return fmt.Errorf("new broker: %w", e)
			}
			// Cleanup stops gateways before the broker, even if execution is
			// canceled. This independent lifetime is bounded by closeAll's wait.
			brokerCtx, cancel := context.WithCancel(context.Background())
			done := make(chan error, 1)
			go func() { done <- broker.Serve(brokerCtx) }()
			p.own("broker", func() error {
				cancel()
				select {
				case e := <-done:
					if e != nil {
						return fmt.Errorf("serve broker: %w", e)
					}
					return nil
				case <-time.After(stopWait):
					return errors.New("broker did not stop in time")
				}
			})
			return nil
		}},
		{"gateway-a-tls", "gateway A attaches with own-uid broker verification and serves HTTPS over a brokered connection; explicit CA pool, exact SNI proof.jl.local, no DNS; gateways run in the same process", func() (e error) {
			if srv, e = p.serve(ctx, sock, addr, leaf); e != nil {
				return fmt.Errorf("attach gateway A: %w", e)
			}
			if e = httpsGet(ctx, addr.Port, ca.pool()); e != nil {
				return fmt.Errorf("gateway A HTTPS: %w", e)
			}
			return nil
		}},
		{"wrong-hostname-rejected", "handshake with SNI wrong.jl.local was rejected by certificate verification (explicit pool only; no DNS, OS or browser trust claimed)", func() error {
			return expectReject(ctx, addr.Port, wrongHost, ca.pool(), func(err error) bool {
				var he x509.HostnameError
				return errors.As(err, &he)
			})
		}},
		{"untrusted-ca-rejected", "handshake against an unrelated in-memory CA pool was rejected (explicit pool only; no DNS, OS or browser trust claimed)", func() error {
			return expectReject(ctx, addr.Port, proofHost, otherCA.pool(), func(err error) bool {
				var ue x509.UnknownAuthorityError
				return errors.As(err, &ue)
			})
		}},
		{"second-gateway-refused", "second attachment refused with ErrBrokerBusy while gateway A is attached; gateways run in the same process", func() error {
			ln, e := AttachBroker(ctx, sock, p.uid, addr)
			if ln != nil {
				p.own("unexpected gateway attachment", ln.Close)
				return errors.New("second gateway unexpectedly attached while A was attached")
			}
			if !errors.Is(e, ErrBrokerBusy) {
				return fmt.Errorf("second gateway refusal did not wrap ErrBrokerBusy: %w", e)
			}
			return nil
		}},
		{"gateway-b-replacement-tls", "gateway A detaches; gateway B attaches and serves verified HTTPS on the same broker-owned port; gateways run in the same process", func() (e error) {
			if e = srv.stop(); e != nil {
				return fmt.Errorf("detach gateway A: %w", e)
			}
			for {
				srv, e = p.serve(ctx, sock, addr, leaf)
				if !errors.Is(e, ErrBrokerBusy) {
					break
				}
				// Detachment is asynchronous at the broker. Wait only for that
				// transition, bounded by the proof's execution context.
				if !sleepContext(ctx, 10*time.Millisecond) {
					return fmt.Errorf("wait for gateway A detachment: %w", ctx.Err())
				}
			}
			if e != nil {
				return fmt.Errorf("attach gateway B: %w", e)
			}
			if got := srv.ln.Addr().(*net.TCPAddr).Port; got != addr.Port {
				return fmt.Errorf("gateway B port %d differs from broker port %d", got, addr.Port)
			}
			if e = httpsGet(ctx, addr.Port, ca.pool()); e != nil {
				return fmt.Errorf("gateway B HTTPS: %w", e)
			}
			return nil
		}},
		{"hosts-plan", "fixture add, unrelated edit, removal and modified-block denial behaved as specified", func() error {
			return hostsProof(filepath.Join(p.root, "hosts"))
		}},
	}
	for _, s := range steps {
		if err := p.step(ctx, s.name, s.detail, s.fn); err != nil {
			return fmt.Errorf("proof checks: %w", err)
		}
	}
	return nil
}

// serve attaches a gateway and serves HTTPS over brokered connections.
func (p *proof) serve(ctx context.Context, sock string, addr *net.TCPAddr, leaf tls.Certificate) (*proofServer, error) {
	ln, err := AttachBroker(ctx, sock, p.uid, addr)
	if err != nil {
		return nil, fmt.Errorf("attach gateway: %w", err)
	}
	s := newProofServer(ln, leaf)
	p.own("https server", s.stop)
	return s, nil
}

type proofServer struct {
	srv     *http.Server
	ln      net.Listener
	done    chan error
	stopped bool
	err     error
}

func newProofServer(ln net.Listener, leaf tls.Certificate) *proofServer {
	s := &proofServer{ln: ln, done: make(chan error, 1)}
	s.srv = &http.Server{
		Handler: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			// A failed write means the client went away; the proof client reports the
			// failed response, and there is nothing else to do at this boundary.
			_, _ = io.WriteString(w, proofBody)
		}),
		TLSConfig:         &tls.Config{Certificates: []tls.Certificate{leaf}, MinVersion: tls.VersionTLS12},
		ReadHeaderTimeout: 5 * time.Second,
		ErrorLog:          log.New(io.Discard, "", 0),
	}
	go func() { s.done <- s.srv.ServeTLS(ln, "", "") }()
	return s
}

// stop closes the server and listener and waits (bounded) for Serve to return.
func (s *proofServer) stop() error {
	if s.stopped {
		if s.err != nil {
			return fmt.Errorf("stop HTTPS server: %w", s.err)
		}
		return nil
	}
	s.stopped = true
	var errs []error
	if err := s.srv.Close(); err != nil {
		errs = append(errs, fmt.Errorf("close server: %w", err))
	}
	select {
	case serr := <-s.done:
		if !errors.Is(serr, http.ErrServerClosed) {
			errs = append(errs, fmt.Errorf("serve: %w", serr))
		}
	case <-time.After(stopWait):
		errs = append(errs, errors.New("https server did not stop in time"))
	}
	if err := s.ln.Close(); err != nil && !errors.Is(err, net.ErrClosed) {
		errs = append(errs, fmt.Errorf("close listener: %w", err))
	}
	s.err = errors.Join(errs...)
	if s.err != nil {
		return fmt.Errorf("stop HTTPS server: %w", s.err)
	}
	return nil
}

func tlsDialer(serverName string, roots *x509.CertPool) *tls.Dialer {
	return &tls.Dialer{
		NetDialer: &net.Dialer{Timeout: 3 * time.Second},
		Config:    &tls.Config{RootCAs: roots, ServerName: serverName, MinVersion: tls.VersionTLS12},
	}
}

// httpsGet requests https://proof.jl.local/ but dials the bound loopback
// port directly, with an explicit trust pool and exact SNI.
func httpsGet(ctx context.Context, port int, roots *x509.CertPool) error {
	addr := net.JoinHostPort("127.0.0.1", fmt.Sprint(port))
	d := tlsDialer(proofHost, roots)
	tr := &http.Transport{
		DialTLSContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			conn, err := d.DialContext(ctx, "tcp", addr)
			if err != nil {
				return nil, fmt.Errorf("dial proof HTTPS: %w", err)
			}
			return conn, nil
		},
		DisableKeepAlives: true,
	}
	defer tr.CloseIdleConnections()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://"+proofHost+"/", nil)
	if err != nil {
		return fmt.Errorf("build request: %w", err)
	}
	resp, err := (&http.Client{Transport: tr}).Do(req)
	if err != nil {
		return fmt.Errorf("https get: %w", err)
	}
	defer resp.Body.Close() //nolint:errcheck // read-only body
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1024))
	switch {
	case err != nil:
		return fmt.Errorf("read body: %w", err)
	case resp.TLS == nil || !resp.TLS.HandshakeComplete:
		return errors.New("response was not served over a completed TLS handshake")
	case resp.StatusCode != http.StatusOK || string(body) != proofBody:
		return fmt.Errorf("unexpected response status %d or body", resp.StatusCode)
	}
	return nil
}

// expectReject dials the port and requires the handshake to fail with a
// certificate verification error accepted by isReject.
func expectReject(ctx context.Context, port int, serverName string, roots *x509.CertPool, isReject func(error) bool) error {
	conn, err := tlsDialer(serverName, roots).DialContext(ctx, "tcp", net.JoinHostPort("127.0.0.1", fmt.Sprint(port)))
	if err == nil {
		rej := errors.New("handshake unexpectedly succeeded")
		if cerr := conn.Close(); cerr != nil {
			rej = errors.Join(rej, fmt.Errorf("close connection: %w", cerr))
		}
		return fmt.Errorf("reject proof handshake: %w", rej)
	}
	if !isReject(err) {
		return fmt.Errorf("handshake failed for an unexpected reason: %w", err)
	}
	return nil
}

type authority struct {
	cert *x509.Certificate
	key  *ecdsa.PrivateKey
}

func (a *authority) pool() *x509.CertPool {
	pool := x509.NewCertPool()
	pool.AddCert(a.cert)
	return pool
}

func serial() (*big.Int, error) {
	n, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 62))
	if err != nil {
		return nil, fmt.Errorf("serial: %w", err)
	}
	return n, nil
}

func newAuthority(cn string, now time.Time) (*authority, error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("generate CA key: %w", err)
	}
	sn, err := serial()
	if err != nil {
		return nil, fmt.Errorf("CA certificate serial: %w", err)
	}
	tmpl := &x509.Certificate{
		SerialNumber:          sn,
		Subject:               pkix.Name{CommonName: cn},
		NotBefore:             now.Add(-time.Minute),
		NotAfter:              now.Add(15 * time.Minute),
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature,
		BasicConstraintsValid: true,
		IsCA:                  true,
		MaxPathLen:            0,
		MaxPathLenZero:        true,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		return nil, fmt.Errorf("create CA certificate: %w", err)
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		return nil, fmt.Errorf("parse CA certificate: %w", err)
	}
	return &authority{cert: cert, key: key}, nil
}

func (a *authority) issueLeaf(host string, now time.Time) (tls.Certificate, error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return tls.Certificate{}, fmt.Errorf("generate leaf key: %w", err)
	}
	sn, err := serial()
	if err != nil {
		return tls.Certificate{}, fmt.Errorf("leaf certificate serial: %w", err)
	}
	tmpl := &x509.Certificate{
		SerialNumber:          sn,
		Subject:               pkix.Name{CommonName: host},
		DNSNames:              []string{host},
		NotBefore:             now.Add(-time.Minute),
		NotAfter:              now.Add(10 * time.Minute),
		KeyUsage:              x509.KeyUsageDigitalSignature,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, a.cert, &key.PublicKey, a.key)
	if err != nil {
		return tls.Certificate{}, fmt.Errorf("create leaf certificate: %w", err)
	}
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key}, nil
}

// checkPrivate requires the directory and socket to have the exact modes and
// to belong to uid.
func checkPrivate(dir, sock string, dirMode, sockMode os.FileMode, uid int) error {
	for _, c := range []struct {
		path string
		mode os.FileMode
	}{{dir, dirMode}, {sock, sockMode}} {
		fi, err := os.Lstat(c.path)
		if err != nil {
			return fmt.Errorf("stat %s: %w", filepath.Base(c.path), err)
		}
		if fi.Mode().Perm() != c.mode {
			return fmt.Errorf("%s mode is %#o, want %#o", filepath.Base(c.path), fi.Mode().Perm(), c.mode)
		}
		if st, ok := fi.Sys().(*syscall.Stat_t); !ok || int(st.Uid) != uid {
			return fmt.Errorf("%s is not owned by the effective uid", filepath.Base(c.path))
		}
	}
	return nil
}

// writeFileAtomic writes data to a 0600 temp file in the same directory,
// fsyncs it, renames it over path and fsyncs the directory.
func writeFileAtomic(path string, data []byte) (err error) {
	tmp := path + ".tmp"
	f, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600) //nolint:gosec // fixture path is inside our private root
	if err != nil {
		return fmt.Errorf("create temp file: %w", err)
	}
	defer func() {
		if err != nil {
			if rerr := os.Remove(tmp); rerr != nil {
				err = errors.Join(err, fmt.Errorf("remove temp file: %w", rerr))
			}
		}
	}()
	if _, err = f.Write(data); err == nil {
		err = f.Sync()
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return fmt.Errorf("write temp file: %w", err)
	}
	if err = os.Rename(tmp, path); err != nil {
		return fmt.Errorf("rename temp file: %w", err)
	}
	dir, err := os.Open(filepath.Dir(path))
	if err != nil {
		return fmt.Errorf("open directory: %w", err)
	}
	var errs []error
	if serr := dir.Sync(); serr != nil {
		errs = append(errs, fmt.Errorf("sync directory: %w", serr))
	}
	if cerr := dir.Close(); cerr != nil {
		errs = append(errs, fmt.Errorf("close directory: %w", cerr))
	}
	return errors.Join(errs...)
}

// tamperBlock edits the first host record after offset from, simulating a user
// editing an owned block.
func tamperBlock(content []byte, from int) ([]byte, error) {
	off := from
	for _, line := range bytes.SplitAfter(content[from:], []byte("\n")) {
		if t := bytes.TrimSpace(line); len(t) > 0 && t[0] != '#' {
			end := off + len(bytes.TrimRight(line, "\r\n"))
			out := append(append([]byte{}, content[:end]...), " tampered.invalid"...)
			return append(out, content[end:]...), nil
		}
		off += len(line)
	}
	return nil, errors.New("no host record in the owned block to modify")
}

// hostsProof exercises PlanHosts against a temporary fixture file only.
func hostsProof(path string) error {
	original := []byte("# jailoc proof fixture\n127.0.0.1 localhost # no trailing newline")
	if err := writeFileAtomic(path, original); err != nil {
		return fmt.Errorf("write fixture: %w", err)
	}
	add, err := PlanHosts(original, Ledger{}, []Name{{Workspace: "proof", Route: "app"}})
	if err != nil {
		return fmt.Errorf("plan add: %w", err)
	}
	owned := add.Ledger.Owned
	if len(owned) == 0 || owned[0] != '\n' || !bytes.Equal(add.Content, append(bytes.Clone(original), owned...)) {
		return errors.New("add plan did not keep the original bytes and append only a newline separator plus the owned block")
	}
	if err := writeFileAtomic(path, add.Content); err != nil {
		return fmt.Errorf("write added fixture: %w", err)
	}
	withUnrelated := append(append([]byte{}, add.Content...), unrelated...)
	if err := writeFileAtomic(path, withUnrelated); err != nil {
		return fmt.Errorf("write unrelated edit: %w", err)
	}
	remove, err := PlanHosts(withUnrelated, add.Ledger, nil)
	if err != nil {
		return fmt.Errorf("plan remove: %w", err)
	}
	if want := append([]byte(string(original)+"\n"), unrelated...); !bytes.Equal(remove.Content, want) {
		return errors.New("remove plan did not keep exactly the original bytes, a newline separator and the unrelated edit")
	}
	if err := writeFileAtomic(path, remove.Content); err != nil {
		return fmt.Errorf("write removed fixture: %w", err)
	}
	modified, err := tamperBlock(add.Content, len(original))
	if err != nil {
		return fmt.Errorf("tamper owned block: %w", err)
	}
	if err := writeFileAtomic(path, modified); err != nil {
		return fmt.Errorf("write modified fixture: %w", err)
	}
	if _, err := PlanHosts(modified, add.Ledger, nil); err == nil {
		return errors.New("modified owned block was not denied")
	}
	got, err := os.ReadFile(path) //nolint:gosec // fixture path is inside our private root
	if err != nil {
		return fmt.Errorf("read fixture: %w", err)
	}
	if !bytes.Equal(got, modified) {
		return errors.New("fixture changed after a denied plan")
	}
	return nil
}

// shortTempDir returns a short directory so AF_UNIX paths stay under the
// sun_path limit on macOS.
func shortTempDir(t *testing.T) string {
	t.Helper()
	base := os.TempDir()
	if len(base) > 60 {
		base = "/tmp"
	}
	dir, err := os.MkdirTemp(base, "jlt-")
	if err != nil {
		t.Fatalf("MkdirTemp: %v", err)
	}
	t.Cleanup(func() {
		if err := os.RemoveAll(dir); err != nil {
			t.Errorf("remove temporary directory: %v", err)
		}
	})
	return dir
}

func assertEmptyDir(t *testing.T, dir string) {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	if len(entries) != 0 {
		t.Errorf("temp root %s not cleaned up, left %d entries", dir, len(entries))
	}
}

func TestProbeTLS(t *testing.T) {
	t.Parallel()
	now := time.Now()
	ca, err := newAuthority("test CA", now)
	if err != nil {
		t.Fatal(err)
	}
	other, err := newAuthority("other CA", now)
	if err != nil {
		t.Fatal(err)
	}
	leaf, err := ca.issueLeaf(proofHost, now)
	if err != nil {
		t.Fatal(err)
	}
	ln, err := net.ListenTCP("tcp", &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	srv := newProofServer(ln, leaf)
	t.Cleanup(func() {
		if err := srv.stop(); err != nil {
			t.Errorf("stop: %v", err)
		}
	})
	port := ln.Addr().(*net.TCPAddr).Port

	isHostname := func(err error) bool { var e x509.HostnameError; return errors.As(err, &e) }
	isUnknown := func(err error) bool { var e x509.UnknownAuthorityError; return errors.As(err, &e) }
	tests := []struct {
		name    string
		sni     string
		roots   *x509.CertPool
		reject  func(error) bool
		wantErr bool
	}{
		{name: "exact name and trusted CA", sni: proofHost, roots: ca.pool()},
		{name: "wrong hostname", sni: wrongHost, roots: ca.pool(), reject: isHostname},
		{name: "untrusted CA", sni: proofHost, roots: other.pool(), reject: isUnknown},
		{name: "reject expected but handshake succeeds", sni: proofHost, roots: ca.pool(), reject: isHostname, wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			var err error
			if tt.reject == nil {
				err = httpsGet(t.Context(), port, tt.roots)
			} else {
				err = expectReject(t.Context(), port, tt.sni, tt.roots, tt.reject)
			}
			if (err != nil) != tt.wantErr {
				t.Fatalf("err = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestLeafProperties(t *testing.T) {
	t.Parallel()
	now := time.Now()
	ca, err := newAuthority("test CA", now)
	if err != nil {
		t.Fatal(err)
	}
	leaf, err := ca.issueLeaf(proofHost, now)
	if err != nil {
		t.Fatal(err)
	}
	cert, err := x509.ParseCertificate(leaf.Certificate[0])
	if err != nil {
		t.Fatal(err)
	}
	if len(cert.DNSNames) != 1 || cert.DNSNames[0] != proofHost {
		t.Errorf("DNSNames = %v, want exactly %s", cert.DNSNames, proofHost)
	}
	if len(cert.ExtKeyUsage) != 1 || cert.ExtKeyUsage[0] != x509.ExtKeyUsageServerAuth {
		t.Errorf("ExtKeyUsage = %v, want serverAuth only", cert.ExtKeyUsage)
	}
	if cert.NotAfter.Sub(now) > time.Hour {
		t.Errorf("leaf lifetime too long: %v", cert.NotAfter.Sub(now))
	}
	if _, err := cert.Verify(x509.VerifyOptions{Roots: ca.pool(), DNSName: proofHost, KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}); err != nil {
		t.Errorf("verify against signing CA: %v", err)
	}
}

func TestWriteFileAtomic(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		initial []byte
		data    []byte
	}{
		{name: "create", data: []byte("a")},
		{name: "replace", initial: []byte("old content"), data: []byte("new")},
		{name: "empty", initial: []byte("old"), data: nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			dir := t.TempDir()
			path := filepath.Join(dir, "hosts")
			if tt.initial != nil {
				if err := os.WriteFile(path, tt.initial, 0o600); err != nil {
					t.Fatal(err)
				}
			}
			if err := writeFileAtomic(path, tt.data); err != nil {
				t.Fatalf("writeFileAtomic: %v", err)
			}
			got, err := os.ReadFile(path) //nolint:gosec // test fixture path.
			if err != nil || !bytes.Equal(got, tt.data) {
				t.Errorf("content = %q (%v), want %q", got, err, tt.data)
			}
			fi, err := os.Stat(path)
			if err != nil || fi.Mode().Perm() != 0o600 {
				t.Errorf("mode = %v (%v), want 0600", fi.Mode().Perm(), err)
			}
			if entries, _ := os.ReadDir(dir); len(entries) != 1 {
				t.Errorf("directory has %d entries, want only the target", len(entries))
			}
		})
	}
	t.Run("stale temp blocks write and is not clobbered", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		path := filepath.Join(dir, "hosts")
		if err := os.WriteFile(path+".tmp", []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := writeFileAtomic(path, []byte("y")); err == nil {
			t.Fatal("want error for pre-existing temp file")
		}
		if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("target must not exist, stat err = %v", err)
		}
	})
}

func TestCheckPrivate(t *testing.T) {
	t.Parallel()
	uid := os.Geteuid()
	tests := []struct {
		name              string
		dirMode, fileMode os.FileMode
		uid               int
		wantErr           bool
	}{
		{name: "private", dirMode: 0o700, fileMode: 0o600, uid: uid},
		{name: "dir too open", dirMode: 0o755, fileMode: 0o600, uid: uid, wantErr: true},
		{name: "file too open", dirMode: 0o700, fileMode: 0o644, uid: uid, wantErr: true},
		{name: "other owner", dirMode: 0o700, fileMode: 0o600, uid: uid + 1, wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			dir := t.TempDir()
			file := filepath.Join(dir, "f")
			if err := os.WriteFile(file, nil, 0o600); err != nil {
				t.Fatal(err)
			}
			if err := os.Chmod(file, tt.fileMode); err != nil {
				t.Fatal(err)
			}
			if err := os.Chmod(dir, tt.dirMode); err != nil {
				t.Fatal(err)
			}
			err := checkPrivate(dir, file, 0o700, 0o600, tt.uid)
			if (err != nil) != tt.wantErr {
				t.Fatalf("err = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestTamperBlock(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		content string
		from    int
		want    string
		wantErr bool
	}{
		{name: "first record", content: "keep\n# BEGIN\n127.0.0.1 a\n# END\n", from: 4, want: "keep\n# BEGIN\n127.0.0.1 a tampered.invalid\n# END\n"},
		{name: "leading separator skipped", content: "keep\n# BEGIN\n127.0.0.1 a\n", from: 4, want: "keep\n# BEGIN\n127.0.0.1 a tampered.invalid\n"},
		{name: "comments only", content: "keep\n# BEGIN\n# END\n", from: 4, wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, err := tamperBlock([]byte(tt.content), tt.from)
			if (err != nil) != tt.wantErr {
				t.Fatalf("err = %v, wantErr %v", err, tt.wantErr)
			}
			if string(got) != tt.want {
				t.Errorf("got %q, want %q", got, tt.want)
			}
		})
	}
}

func TestHostsProof(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "hosts")
	if err := hostsProof(path); err != nil {
		t.Fatalf("hostsProof: %v", err)
	}
}

// RunProof tests change TMPDIR, so they cannot run in parallel.
func TestRunProof(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("proof refuses to run as root; see TestRunProofRefusesRoot")
	}
	tmp := shortTempDir(t)
	t.Setenv("TMPDIR", tmp)

	res, err := RunProof(t.Context())
	if err != nil {
		t.Fatalf("RunProof: %v\nchecks: %+v", err, res.Checks)
	}
	want := []string{
		"unprivileged", "memory-ca", "private-socket", "broker-listener",
		"gateway-a-tls", "wrong-hostname-rejected", "untrusted-ca-rejected",
		"second-gateway-refused", "gateway-b-replacement-tls", "hosts-plan", "cleanup",
	}
	if len(res.Checks) != len(want) {
		t.Fatalf("got %d checks, want %d: %+v", len(res.Checks), len(want), res.Checks)
	}
	for i, c := range res.Checks {
		if c.Name != want[i] || !c.Passed {
			t.Errorf("check %d = %+v, want passing %q", i, c, want[i])
		}
		if strings.HasPrefix(c.Name, "gateway-") && !strings.Contains(c.Detail, "same process") {
			t.Errorf("check %q must disclose that gateways run in the same process: %q", c.Name, c.Detail)
		}
		for _, obsolete := range []string{"handoff", "retainer", "retained listener"} {
			if strings.Contains(c.Detail, obsolete) {
				t.Errorf("check %q still describes the removed %q design", c.Name, obsolete)
			}
		}
		for _, bad := range []string{"PRIVATE KEY", "BEGIN CERTIFICATE", "token"} {
			if strings.Contains(c.Detail, bad) {
				t.Errorf("check %q detail leaks %q", c.Name, bad)
			}
		}
	}
	assertEmptyDir(t, tmp)
}

func TestRunProofContext(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("proof refuses to run as root")
	}
	tests := []struct {
		name string
		ctx  func(*testing.T) context.Context
		want error
	}{
		{name: "canceled before start", want: context.Canceled, ctx: func(t *testing.T) context.Context {
			ctx, cancel := context.WithCancel(t.Context())
			cancel()
			return ctx
		}},
		{name: "deadline already passed", want: context.DeadlineExceeded, ctx: func(t *testing.T) context.Context {
			ctx, cancel := context.WithDeadline(t.Context(), time.Now().Add(-time.Second))
			t.Cleanup(cancel)
			return ctx
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tmp := shortTempDir(t)
			t.Setenv("TMPDIR", tmp)
			res, err := RunProof(tt.ctx(t))
			if !errors.Is(err, tt.want) {
				t.Fatalf("err = %v, want %v", err, tt.want)
			}
			var failed, cleanedUp bool
			for _, c := range res.Checks {
				failed = failed || !c.Passed && c.Name != "cleanup"
				cleanedUp = cleanedUp || c.Name == "cleanup" && c.Passed
			}
			if !failed || !cleanedUp {
				t.Errorf("want a failed row and a passing cleanup row, got %+v", res.Checks)
			}
			assertEmptyDir(t, tmp)
		})
	}
}

func TestRunProofCanceledMidway(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("proof refuses to run as root")
	}
	tmp := shortTempDir(t)
	t.Setenv("TMPDIR", tmp)
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() { _, err := RunProof(ctx); done <- err }()
	time.Sleep(20 * time.Millisecond)
	cancel()
	select {
	case err := <-done:
		if err != nil && !errors.Is(err, context.Canceled) {
			t.Errorf("err = %v, want nil or context.Canceled", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("RunProof did not return after cancellation")
	}
	assertEmptyDir(t, tmp)
}

func TestRunProofRefusesRoot(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("requires euid 0")
	}
	res, err := RunProof(t.Context())
	if err == nil || len(res.Checks) != 1 || res.Checks[0].Name != "unprivileged" || res.Checks[0].Passed || !strings.Contains(err.Error(), "uid 0") {
		t.Fatalf("want one failed check and an error, got %+v, %v", res.Checks, err)
	}
}
