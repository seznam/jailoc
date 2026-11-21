//go:build darwin || linux

package gatewayhost

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

const bcTimeout = 10 * time.Second

func bcUID() uint32 {
	return uint32(os.Getuid()) //nolint:gosec // UIDs are 32-bit on Linux and Darwin.
}

// bcTree is a private directory tree standing in for a root-owned install
// location: root (0700) / bin (0755) / the executable (0755). The tests'
// "root" is the current user, so the trust check gets the user's UID as the
// expected owner and root as the top of the chain.
type bcTree struct {
	root string
	bin  string
	exe  string
}

func bcNewTree(t *testing.T) bcTree {
	t.Helper()
	base := os.TempDir()
	if len(base) > 60 {
		base = "/tmp"
	}
	root, err := os.MkdirTemp(base, "jbc")
	if err != nil {
		t.Fatalf("MkdirTemp: %v", err)
	}
	// Resolve links (macOS: /var -> /private/var): the command checks the
	// resolved executable path.
	if root, err = filepath.EvalSymlinks(root); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		// A test may have made a directory unwritable or opened it up;
		// restore access before removing.
		if err := os.Chmod(root, 0o700); err != nil { //nolint:gosec // root is a test directory.
			t.Errorf("chmod %s: %v", root, err)
		}
		if err := os.RemoveAll(root); err != nil {
			t.Errorf("RemoveAll(%s): %v", root, err)
		}
	})
	if err := os.Chmod(root, 0o700); err != nil { //nolint:gosec // root is a test directory.
		t.Fatal(err)
	}
	bin := filepath.Join(root, "bin")
	if err := os.Mkdir(bin, 0o755); err != nil { //nolint:gosec // simulates a system bin directory.
		t.Fatal(err)
	}
	if err := os.Chmod(bin, 0o755); err != nil { //nolint:gosec // simulates a system bin directory.
		t.Fatal(err)
	}
	exe := filepath.Join(bin, "jailoc-listener-broker")
	if err := os.WriteFile(exe, []byte("#!/bin/sh\n"), 0o755); err != nil { //nolint:gosec // simulates an installed executable.
		t.Fatal(err)
	}
	if err := os.Chmod(exe, 0o755); err != nil { //nolint:gosec // simulates an installed executable.
		t.Fatal(err)
	}
	return bcTree{root: root, bin: bin, exe: exe}
}

func TestBrokerCmdParseArgs(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		args    []string
		want    uint32
		wantErr string
	}{
		{name: "separate value", args: []string{"--uid", "501"}, want: 501},
		{name: "equals value", args: []string{"--uid=1000"}, want: 1000},
		{name: "largest uid", args: []string{"--uid", "2147483647"}, want: 2147483647},
		{name: "no arguments", args: nil, wantErr: "--uid"},
		{name: "flag without value", args: []string{"--uid"}, wantErr: "--uid"},
		{name: "empty value", args: []string{"--uid", ""}, wantErr: "--uid"},
		{name: "non-numeric", args: []string{"--uid", "alice"}, wantErr: "--uid"},
		{name: "negative", args: []string{"--uid", "-1"}, wantErr: "--uid"},
		{name: "signed", args: []string{"--uid", "+501"}, wantErr: "--uid"},
		{name: "zero", args: []string{"--uid", "0"}, wantErr: "root"},
		{name: "overflow", args: []string{"--uid", "2147483648"}, wantErr: "--uid"},
		{name: "positional only", args: []string{"501"}, wantErr: "--uid"},
		{name: "extra positional", args: []string{"--uid", "501", "extra"}, wantErr: "arguments"},
		{name: "extra flag", args: []string{"--uid", "501", "--port", "80"}, wantErr: "port"},
		{name: "listen flag", args: []string{"--uid", "501", "--listen", "127.0.0.1:80"}, wantErr: "listen"},
		{name: "unknown flag only", args: []string{"--socket", "/tmp/x"}, wantErr: "socket"},
		{name: "repeated uid", args: []string{"--uid", "501", "--uid", "502"}, wantErr: "once"},
		{name: "help is not a flag", args: []string{"--help"}, wantErr: "help"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, err := brokerCmdParseArgs(tt.args)
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("brokerCmdParseArgs(%q) = %d, %v; want an error containing %q", tt.args, got, err, tt.wantErr)
				}
				return
			}
			if err != nil || got != tt.want {
				t.Fatalf("brokerCmdParseArgs(%q) = %d, %v; want %d", tt.args, got, err, tt.want)
			}
		})
	}
}

func TestBrokerCmdCheckTrusted(t *testing.T) {
	t.Parallel()
	self := bcUID()
	tests := []struct {
		name    string
		prepare func(t *testing.T, tr bcTree)
		owner   uint32
		wantErr string
	}{
		{name: "correct chain", owner: self},
		{
			name:    "group-writable parent",
			prepare: func(t *testing.T, tr bcTree) { bcChmod(t, tr.bin, 0o775) },
			owner:   self, wantErr: "group or other writable",
		},
		{
			name:    "other-writable parent",
			prepare: func(t *testing.T, tr bcTree) { bcChmod(t, tr.bin, 0o757) },
			owner:   self, wantErr: "group or other writable",
		},
		{
			name:    "group-writable ancestor",
			prepare: func(t *testing.T, tr bcTree) { bcChmod(t, tr.root, 0o770) },
			owner:   self, wantErr: "group or other writable",
		},
		{
			name:    "other-writable ancestor",
			prepare: func(t *testing.T, tr bcTree) { bcChmod(t, tr.root, 0o707) },
			owner:   self, wantErr: "group or other writable",
		},
		{
			name:    "group-writable file",
			prepare: func(t *testing.T, tr bcTree) { bcChmod(t, tr.exe, 0o775) },
			owner:   self, wantErr: "group or other writable",
		},
		{
			name:    "other-writable file",
			prepare: func(t *testing.T, tr bcTree) { bcChmod(t, tr.exe, 0o757) },
			owner:   self, wantErr: "group or other writable",
		},
		{name: "file owned by someone else", owner: self + 1, wantErr: "owned by uid"},
		{
			name: "file is a directory",
			prepare: func(t *testing.T, tr bcTree) {
				if err := os.Remove(tr.exe); err != nil {
					t.Fatal(err)
				}
				if err := os.Mkdir(tr.exe, 0o755); err != nil { //nolint:gosec // test fixture.
					t.Fatal(err)
				}
			},
			owner: self, wantErr: "regular file",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			tr := bcNewTree(t)
			if tt.prepare != nil {
				tt.prepare(t, tr)
			}
			err := brokerCmdCheckTrusted(tr.exe, tt.owner, tr.root)
			if tt.wantErr == "" {
				if err != nil {
					t.Fatalf("brokerCmdCheckTrusted: %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("brokerCmdCheckTrusted error = %v, want one containing %q", err, tt.wantErr)
			}
		})
	}
}

func TestBrokerCmdCheckTrustedRejectsBadInput(t *testing.T) {
	t.Parallel()
	tr := bcNewTree(t)
	if err := brokerCmdCheckTrusted("relative/jailoc-listener-broker", bcUID(), tr.root); err == nil || !strings.Contains(err.Error(), "absolute") {
		t.Errorf("relative path: error = %v, want an absolute-path error", err)
	}
	if err := brokerCmdCheckTrusted(filepath.Join(tr.bin, "missing"), bcUID(), tr.root); err == nil {
		t.Error("missing file: want an error")
	}
	// The chain must end at top: a top that is not an ancestor is a bug in
	// the caller, not a trusted path.
	other := bcNewTree(t)
	if err := brokerCmdCheckTrusted(tr.exe, bcUID(), other.root); err == nil || !strings.Contains(err.Error(), "not below") {
		t.Errorf("top that is not an ancestor: error = %v, want a not-below error", err)
	}
}

func bcChmod(t *testing.T, path string, mode os.FileMode) {
	t.Helper()
	if err := os.Chmod(path, mode); err != nil {
		t.Fatal(err)
	}
}

// bcHarness wires brokerCmdDeps to unprivileged seams: the user's own UID
// stands in for root, ports are ephemeral and every path is temporary.
type bcHarness struct {
	tree   bcTree
	sock   string
	stderr *bcBuffer

	mu        sync.Mutex
	listens   []string // "network addr" per listen call
	listeners []*net.TCPListener
	failOn    int // 1-based listen call that fails, 0 for none

	deps brokerCmdDeps
}

type bcBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *bcBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *bcBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

func bcNewHarness(t *testing.T) *bcHarness {
	t.Helper()
	tr := bcNewTree(t)
	h := &bcHarness{tree: tr, sock: filepath.Join(tr.root, "b.sock"), stderr: &bcBuffer{}}
	h.deps = brokerCmdDeps{
		uids:         func() (int, int) { return 0, 0 },
		executable:   func() (string, error) { return tr.exe, nil },
		trustedOwner: bcUID(),
		trustTop:     tr.root,
		binds:        []brokerCmdBind{{"tcp4", "127.0.0.1:0"}, {"tcp6", "[::1]:0"}},
		listen:       h.listen,
		socketPath:   func(uint32) string { return h.sock },
		stderr:       h.stderr,
	}
	return h
}

func (h *bcHarness) listen(network, addr string) (*net.TCPListener, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.listens = append(h.listens, network+" "+addr)
	if h.failOn == len(h.listens) {
		return nil, errors.New("injected bind failure")
	}
	ln, err := brokerCmdListen(network, addr)
	if err != nil {
		return nil, err
	}
	h.listeners = append(h.listeners, ln)
	return ln, nil
}

func (h *bcHarness) listenCalls() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return len(h.listens)
}

func (h *bcHarness) addr(i int) *net.TCPAddr {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.listeners[i].Addr().(*net.TCPAddr)
}

func bcSkipWithoutIPv6(t *testing.T) {
	t.Helper()
	ln, err := net.ListenTCP("tcp6", &net.TCPAddr{IP: net.IPv6loopback})
	if err != nil {
		t.Skipf("IPv6 loopback unavailable: %v", err)
	}
	if err := ln.Close(); err != nil {
		t.Fatal(err)
	}
}

func bcSkipAsRoot(t *testing.T) {
	t.Helper()
	if os.Getuid() == 0 {
		t.Skip("the test needs an unprivileged user")
	}
}

func TestBrokerCmdRefusesNonRoot(t *testing.T) {
	t.Parallel()
	for _, tt := range []struct {
		name       string
		ruid, euid int
	}{
		{"unprivileged", 1000, 1000},
		{"real root, unprivileged effective", 0, 1000},
		{"unprivileged real, effective root", 1000, 0},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			h := bcNewHarness(t)
			h.deps.uids = func() (int, int) { return tt.ruid, tt.euid }
			err := brokerCmdRun(t.Context(), []string{"--uid", "501"}, h.deps)
			if err == nil || !strings.Contains(err.Error(), "root") {
				t.Fatalf("brokerCmdRun error = %v, want a refusal that names root", err)
			}
			if n := h.listenCalls(); n != 0 {
				t.Errorf("refused run bound %d addresses", n)
			}
			if _, err := os.Lstat(h.sock); !errors.Is(err, os.ErrNotExist) {
				t.Errorf("refused run left a socket: %v", err)
			}
		})
	}
}

func TestBrokerCmdRefusesBadArgs(t *testing.T) {
	t.Parallel()
	for _, args := range [][]string{nil, {"--uid", "0"}, {"--uid", "x"}, {"--uid", "501", "extra"}, {"--uid", "501", "--port", "80"}} {
		h := bcNewHarness(t)
		if err := brokerCmdRun(t.Context(), args, h.deps); err == nil {
			t.Errorf("brokerCmdRun(%q) = nil, want a refusal", args)
		}
		if n := h.listenCalls(); n != 0 {
			t.Errorf("brokerCmdRun(%q) bound %d addresses", args, n)
		}
	}
}

func TestBrokerCmdRefusesUntrustedExecutable(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		prepare func(t *testing.T, h *bcHarness)
		wantErr []string
	}{
		{
			name:    "group-writable directory",
			prepare: func(t *testing.T, h *bcHarness) { bcChmod(t, h.tree.bin, 0o775) },
			wantErr: []string{"group or other writable", "/usr/local/libexec", "/Library/PrivilegedHelperTools", "ownership and mode"},
		},
		{
			name:    "other-writable ancestor",
			prepare: func(t *testing.T, h *bcHarness) { bcChmod(t, h.tree.root, 0o707) },
			wantErr: []string{"group or other writable"},
		},
		{
			name:    "owned by another user",
			prepare: func(t *testing.T, h *bcHarness) { h.deps.trustedOwner = bcUID() + 1 },
			wantErr: []string{"owned by uid"},
		},
		{
			name: "symlink into an untrusted directory",
			prepare: func(t *testing.T, h *bcHarness) {
				// The link lives in a good directory and points into a
				// writable one: the target decides.
				bad := filepath.Join(h.tree.root, "bad")
				if err := os.Mkdir(bad, 0o755); err != nil { //nolint:gosec // test fixture.
					t.Fatal(err)
				}
				bcChmod(t, bad, 0o777)
				target := filepath.Join(bad, "real")
				if err := os.WriteFile(target, []byte("x"), 0o755); err != nil { //nolint:gosec // test fixture.
					t.Fatal(err)
				}
				link := filepath.Join(h.tree.bin, "link")
				if err := os.Symlink(target, link); err != nil {
					t.Fatal(err)
				}
				h.deps.executable = func() (string, error) { return link, nil }
			},
			wantErr: []string{"group or other writable"},
		},
		{
			name: "executable path unknown",
			prepare: func(t *testing.T, h *bcHarness) {
				h.deps.executable = func() (string, error) { return "", errors.New("no path") }
			},
			wantErr: []string{"executable"},
		},
		{
			name: "executable path dangling",
			prepare: func(t *testing.T, h *bcHarness) {
				h.deps.executable = func() (string, error) { return filepath.Join(h.tree.bin, "gone"), nil }
			},
			wantErr: []string{"executable"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			h := bcNewHarness(t)
			tt.prepare(t, h)
			err := brokerCmdRun(t.Context(), []string{"--uid", "501"}, h.deps)
			if err == nil {
				t.Fatal("brokerCmdRun = nil, want a refusal")
			}
			for _, want := range tt.wantErr {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("error %q lacks %q", err, want)
				}
			}
			if n := h.listenCalls(); n != 0 {
				t.Errorf("refused run bound %d addresses", n)
			}
		})
	}
}

// bcAttach attaches as the gateway, retrying until the broker's control
// socket is up.
func bcAttach(t *testing.T, ctx context.Context, path string, addr *net.TCPAddr) net.Listener {
	t.Helper()
	deadline := time.Now().Add(bcTimeout)
	for {
		ln, err := AttachBroker(ctx, path, bcUID(), addr)
		if err == nil {
			t.Cleanup(func() {
				if err := ln.Close(); err != nil {
					t.Errorf("close gateway listener: %v", err)
				}
			})
			return ln
		}
		if time.Now().After(deadline) {
			t.Fatalf("AttachBroker: %v", err)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// bcWaitListeners waits until the harness bound n addresses.
func bcWaitListeners(t *testing.T, h *bcHarness, n int) {
	t.Helper()
	deadline := time.Now().Add(bcTimeout)
	for h.listenCalls() < n {
		if time.Now().After(deadline) {
			t.Fatalf("broker bound %d addresses, want %d", h.listenCalls(), n)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestBrokerCmdRunServesAndStopsCleanly(t *testing.T) {
	t.Parallel()
	bcSkipAsRoot(t)
	bcSkipWithoutIPv6(t)
	h := bcNewHarness(t)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- brokerCmdRun(ctx, []string{"--uid", itoa(bcUID())}, h.deps) }()
	bcWaitListeners(t, h, 2)

	gwCtx, gwCancel := context.WithTimeout(t.Context(), bcTimeout)
	defer gwCancel()
	gw := bcAttach(t, gwCtx, h.sock, h.addr(0))

	for i, addr := range []*net.TCPAddr{h.addr(0), h.addr(1)} {
		c, err := net.DialTimeout("tcp", addr.String(), bcTimeout)
		if err != nil {
			t.Fatalf("dial %s: %v", addr, err)
		}
		defer func() { _ = c.Close() }() // test connection; close errors do not matter
		if _, err := io.WriteString(c, "ping"); err != nil {
			t.Fatalf("write %d: %v", i, err)
		}
		accepted := make(chan net.Conn, 1)
		go func() {
			ac, err := gw.Accept()
			if err != nil {
				t.Errorf("Accept: %v", err)
				close(accepted)
				return
			}
			accepted <- ac
		}()
		select {
		case ac, ok := <-accepted:
			if !ok {
				return
			}
			buf := make([]byte, 4)
			if err := ac.SetReadDeadline(time.Now().Add(bcTimeout)); err != nil {
				t.Fatal(err)
			}
			if _, err := io.ReadFull(ac, buf); err != nil || string(buf) != "ping" {
				t.Fatalf("connection %d through broker: read %q, %v", i, buf, err)
			}
			_ = ac.Close() // test connection
		case <-time.After(bcTimeout):
			t.Fatalf("gateway received no connection for %s", addr)
		}
	}

	if st, err := os.Lstat(h.sock); err != nil || st.Mode().Perm() != 0o600 {
		t.Errorf("control socket: %v, %v; want mode 0600", st, err)
	}
	listening := h.stderr.String()
	if !strings.Contains(listening, "listening") {
		t.Errorf("stderr lacks a listening line: %q", listening)
	}

	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("brokerCmdRun after cancel = %v, want nil", err)
		}
	case <-time.After(bcTimeout):
		t.Fatal("brokerCmdRun did not stop after cancel")
	}
	if _, err := os.Lstat(h.sock); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("control socket still exists after stop: %v", err)
	}
	for _, addr := range []*net.TCPAddr{h.addr(0), h.addr(1)} {
		if c, err := net.DialTimeout("tcp", addr.String(), time.Second); err == nil {
			_ = c.Close() // the dial must not succeed; nothing to report on close
			t.Errorf("%s still accepts connections after stop", addr)
		}
	}
}

func itoa(u uint32) string { return strconv.FormatUint(uint64(u), 10) }

func TestBrokerCmdSecondBindFailureClosesFirst(t *testing.T) {
	t.Parallel()
	bcSkipAsRoot(t)
	h := bcNewHarness(t)
	h.failOn = 2
	err := brokerCmdRun(t.Context(), []string{"--uid", itoa(bcUID())}, h.deps)
	if err == nil || !strings.Contains(err.Error(), "[::1]:0") || !strings.Contains(err.Error(), "injected bind failure") {
		t.Fatalf("brokerCmdRun error = %v, want the failing address and cause", err)
	}
	if n := h.listenCalls(); n != 2 {
		t.Fatalf("listen calls = %d, want 2", n)
	}
	first := h.addr(0)
	if c, err := net.DialTimeout("tcp", first.String(), time.Second); err == nil {
		_ = c.Close() // the dial must not succeed; nothing to report on close
		t.Errorf("the first listener %s is still open after the second bind failed", first)
	}
	if _, err := os.Lstat(h.sock); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("a control socket exists after a bind failure: %v", err)
	}
}

func TestBrokerCmdFirstBindFailureReportsAddress(t *testing.T) {
	t.Parallel()
	bcSkipAsRoot(t)
	h := bcNewHarness(t)
	h.failOn = 1
	err := brokerCmdRun(t.Context(), []string{"--uid", itoa(bcUID())}, h.deps)
	if err == nil || !strings.Contains(err.Error(), "127.0.0.1:0") {
		t.Fatalf("brokerCmdRun error = %v, want the failing address", err)
	}
	if n := h.listenCalls(); n != 1 {
		t.Errorf("listen calls = %d, want 1 (no attempt after a failure)", n)
	}
}

func TestBrokerCmdControlSocketFailureClosesListeners(t *testing.T) {
	t.Parallel()
	bcSkipAsRoot(t)
	bcSkipWithoutIPv6(t)
	h := bcNewHarness(t)
	// A regular file where the socket should go makes ListenBrokerSocket
	// refuse.
	if err := os.WriteFile(h.sock, []byte("not a socket"), 0o600); err != nil {
		t.Fatal(err)
	}
	err := brokerCmdRun(t.Context(), []string{"--uid", itoa(bcUID())}, h.deps)
	if err == nil || !strings.Contains(err.Error(), "control socket") {
		t.Fatalf("brokerCmdRun error = %v, want a control socket error", err)
	}
	for _, addr := range []*net.TCPAddr{h.addr(0), h.addr(1)} {
		if c, err := net.DialTimeout("tcp", addr.String(), time.Second); err == nil {
			_ = c.Close() // the dial must not succeed; nothing to report on close
			t.Errorf("%s is still open after the control socket failed", addr)
		}
	}
	if data, err := os.ReadFile(h.sock); err != nil || string(data) != "not a socket" { //nolint:gosec // test fixture path.
		t.Errorf("the foreign file at the socket path was touched: %q, %v", data, err)
	}
}
