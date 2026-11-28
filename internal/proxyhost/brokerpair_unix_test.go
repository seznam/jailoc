//go:build darwin || linux

package proxyhost

import (
	"context"
	"errors"
	"net"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// bpListen binds a loopback listener of network on port, retrying with a new
// ephemeral port pair when the other family's port is taken.
func bpPair(t *testing.T, withIPv6 bool) []*net.TCPListener {
	t.Helper()
	for range 20 {
		v4 := brkLoopback(t, "tcp4")
		if !withIPv6 {
			return []*net.TCPListener{v4}
		}
		port := v4.Addr().(*net.TCPAddr).Port
		v6, err := net.ListenTCP("tcp6", &net.TCPAddr{IP: net.IPv6loopback, Port: port})
		if err == nil {
			return []*net.TCPListener{v4, v6}
		}
		brkClose(t, "listener", v4)
	}
	t.Skip("no ephemeral port free on both loopback families")
	return nil
}

type bpHarness struct {
	*brkHarness
	httpAddrs, httpsAddrs []*net.TCPAddr
}

// bpStart serves a broker with an HTTP and an HTTPS listener pair on
// ephemeral ports, reporting them on its health endpoint unless mutate
// clears that.
func bpStart(t *testing.T, withIPv6 bool, mutate func(*BrokerConfig)) *bpHarness {
	t.Helper()
	httpLns, httpsLns := bpPair(t, withIPv6), bpPair(t, withIPv6)
	cfg := BrokerConfig{
		OwnerUID:  brkUID(),
		Listeners: append(append([]*net.TCPListener{}, httpLns...), httpsLns...),
		HTTPPort:  uint16(httpLns[0].Addr().(*net.TCPAddr).Port),  //nolint:gosec // test ports are 16-bit.
		HTTPSPort: uint16(httpsLns[0].Addr().(*net.TCPAddr).Port), //nolint:gosec // test ports are 16-bit.
	}
	if mutate != nil {
		mutate(&cfg)
	}
	h := &bpHarness{brkHarness: brkStart(t, cfg)}
	for _, ln := range httpLns {
		h.httpAddrs = append(h.httpAddrs, ln.Addr().(*net.TCPAddr))
	}
	for _, ln := range httpsLns {
		h.httpsAddrs = append(h.httpsAddrs, ln.Addr().(*net.TCPAddr))
	}
	return h
}

func bpAttach(t *testing.T, ctx context.Context, path string, httpAddr, httpsAddr *net.TCPAddr) (httpLn, httpsLn net.Listener) {
	t.Helper()
	for {
		httpLn, httpsLn, err := AttachBrokerPair(ctx, path, brkUID(), httpAddr, httpsAddr)
		if err == nil {
			return httpLn, httpsLn
		}
		if !errors.Is(err, ErrBrokerBusy) || ctx.Err() != nil {
			t.Fatalf("AttachBrokerPair: %v", err)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func bpAcceptErr(t *testing.T, ln net.Listener) error {
	t.Helper()
	res := make(chan error, 1)
	go func() {
		c, err := ln.Accept()
		if err == nil {
			_ = c.Close() // the test only wants the error
		}
		res <- err
	}()
	select {
	case err := <-res:
		return err
	case <-time.After(brkTimeout):
		t.Fatal("Accept did not return")
		return nil
	}
}

func TestAttachBrokerPairRoutesByLocalPort(t *testing.T) {
	t.Parallel()
	h := bpStart(t, true, nil)
	ctx := brkContext(t)
	httpLn, httpsLn := bpAttach(t, ctx, h.path, h.httpAddrs[0], h.httpsAddrs[0])
	brkServe(t, httpLn, "http")
	brkServe(t, httpsLn, "https")

	// Both families of a port reach that port's listener.
	for _, a := range h.httpAddrs {
		brkWantServed(t, a, "http")
	}
	for _, a := range h.httpsAddrs {
		brkWantServed(t, a, "https")
	}
	if got := httpLn.Addr().String(); got != h.httpAddrs[0].String() {
		t.Errorf("HTTP listener Addr = %s, want %s", got, h.httpAddrs[0])
	}
	if got := httpsLn.Addr().String(); got != h.httpsAddrs[0].String() {
		t.Errorf("HTTPS listener Addr = %s, want %s", got, h.httpsAddrs[0])
	}
}

func TestAttachBrokerPairClosingOneKeepsOther(t *testing.T) {
	t.Parallel()
	h := bpStart(t, false, nil)
	ctx := brkContext(t)
	httpLn, httpsLn := bpAttach(t, ctx, h.path, h.httpAddrs[0], h.httpsAddrs[0])
	brkServe(t, httpsLn, "https")

	if err := httpLn.Close(); err != nil {
		t.Fatalf("close HTTP listener: %v", err)
	}
	if err := bpAcceptErr(t, httpLn); !errors.Is(err, net.ErrClosed) {
		t.Fatalf("HTTP Accept after Close = %v, want net.ErrClosed", err)
	}
	if err := httpLn.Close(); err != nil {
		t.Errorf("second Close = %v, want nil", err)
	}
	// The closed side drops its connections and acknowledges them, so the
	// other side keeps being served.
	brkWantClosed(t, h.httpAddrs[0])
	brkWantServed(t, h.httpsAddrs[0], "https")

	// Closing the last listener frees the gateway slot of the broker.
	if err := httpsLn.Close(); err != nil {
		t.Fatalf("close HTTPS listener: %v", err)
	}
	again, againHTTPS := bpAttach(t, ctx, h.path, h.httpAddrs[0], h.httpsAddrs[0])
	for _, ln := range []net.Listener{again, againHTTPS} {
		if err := ln.Close(); err != nil {
			t.Errorf("close reattached listener: %v", err)
		}
	}
}

func TestAttachBrokerPairBrokerShutdownEndsBoth(t *testing.T) {
	t.Parallel()
	h := bpStart(t, false, nil)
	httpLn, httpsLn := bpAttach(t, brkContext(t), h.path, h.httpAddrs[0], h.httpsAddrs[0])
	if err := h.stop(); err != nil {
		t.Fatalf("stop broker: %v", err)
	}
	for name, ln := range map[string]net.Listener{"HTTP": httpLn, "HTTPS": httpsLn} {
		if err := bpAcceptErr(t, ln); err == nil {
			t.Errorf("%s Accept after broker shutdown = nil, want an error", name)
		}
		if err := ln.Close(); err != nil {
			t.Errorf("close %s listener: %v", name, err)
		}
	}
}

func TestAttachBrokerPairPortMismatchFailsBoth(t *testing.T) {
	t.Parallel()
	h := bpStart(t, false, nil)
	// The gateway expects another HTTPS port than the broker serves, like a
	// proxy configured with ports the installed broker does not know.
	wrong := &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1), Port: h.httpsAddrs[0].Port + 1}
	if wrong.Port > 65535 || wrong.Port == h.httpAddrs[0].Port {
		wrong.Port = h.httpsAddrs[0].Port - 1
	}
	httpLn, httpsLn := bpAttach(t, brkContext(t), h.path, h.httpAddrs[0], wrong)
	defer func() {
		for _, ln := range []net.Listener{httpLn, httpsLn} {
			if err := ln.Close(); err != nil {
				t.Errorf("close listener: %v", err)
			}
		}
	}()

	go func() {
		// The broker's client is closed once the gateway fails.
		if c, err := net.DialTimeout("tcp", h.httpsAddrs[0].String(), brkTimeout); err == nil {
			_ = c.Close() // only provokes the delivery
		}
	}()
	for name, ln := range map[string]net.Listener{"HTTP": httpLn, "HTTPS": httpsLn} {
		err := bpAcceptErr(t, ln)
		if err == nil || !strings.Contains(err.Error(), "local port "+strconv.Itoa(h.httpsAddrs[0].Port)) {
			t.Errorf("%s Accept = %v, want an error naming the unexpected local port", name, err)
		}
	}
}

func TestAttachBrokerPairRejectsBadAddresses(t *testing.T) {
	t.Parallel()
	good := &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 8080}
	tests := []struct {
		name        string
		http, https *net.TCPAddr
		want        string
	}{
		{"nil http", nil, good, "HTTP listener"},
		{"nil https", good, nil, "HTTPS listener"},
		{"http without port", &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1)}, good, "no port"},
		{"https not loopback", good, &net.TCPAddr{IP: net.IPv4(192, 0, 2, 1), Port: 8443}, "not a loopback"},
		{"wildcard", good, &net.TCPAddr{IP: net.IPv4zero, Port: 8443}, "not a loopback"},
		{"same port", good, &net.TCPAddr{IP: net.IPv6loopback, Port: 8080}, "must differ"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			// The path does not exist: a validation error must come first.
			missing := filepath.Join(t.TempDir(), "none.sock")
			h, s, err := AttachBrokerPair(t.Context(), missing, brkUID(), tt.http, tt.https)
			if h != nil || s != nil || err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("AttachBrokerPair = %v, %v, %v; want an error containing %q", h, s, err, tt.want)
			}
		})
	}
}

func TestAttachBrokerPairBusy(t *testing.T) {
	t.Parallel()
	h := bpStart(t, false, nil)
	ctx := brkContext(t)
	httpLn, httpsLn := bpAttach(t, ctx, h.path, h.httpAddrs[0], h.httpsAddrs[0])
	t.Cleanup(func() {
		brkClose(t, "HTTP listener", httpLn)
		brkClose(t, "HTTPS listener", httpsLn)
	})
	if _, _, err := AttachBrokerPair(ctx, h.path, brkUID(), h.httpAddrs[0], h.httpsAddrs[0]); !errors.Is(err, ErrBrokerBusy) {
		t.Fatalf("second AttachBrokerPair = %v, want ErrBrokerBusy", err)
	}
}

func TestProbeBrokerPair(t *testing.T) {
	t.Parallel()
	h := bpStart(t, false, nil)
	ctx := brkContext(t)
	if err := ProbeBrokerPair(ctx, h.path, brkUID(), h.httpAddrs[0], h.httpsAddrs[0]); err != nil {
		t.Fatalf("ProbeBrokerPair = %v", err)
	}
	// Probing never takes the gateway slot, attached or not.
	httpLn, httpsLn := bpAttach(t, ctx, h.path, h.httpAddrs[0], h.httpsAddrs[0])
	t.Cleanup(func() {
		brkClose(t, "HTTP listener", httpLn)
		brkClose(t, "HTTPS listener", httpsLn)
	})
	if err := ProbeBrokerPair(ctx, h.path, brkUID(), h.httpAddrs[0], h.httpsAddrs[0]); err != nil {
		t.Fatalf("ProbeBrokerPair with an attached gateway = %v", err)
	}
	if err := ProbeBroker(ctx, h.path, brkUID(), h.httpsAddrs[0]); err != nil {
		t.Fatalf("ProbeBroker against a broker that reports ports = %v", err)
	}

	swapped := ProbeBrokerPair(ctx, h.path, brkUID(), h.httpsAddrs[0], h.httpAddrs[0])
	if swapped == nil || !strings.Contains(swapped.Error(), "reinstall") {
		t.Errorf("ProbeBrokerPair with swapped ports = %v, want a reinstall hint", swapped)
	}
	other := &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 1}
	if err := ProbeBrokerPair(ctx, h.path, brkUID(), h.httpAddrs[0], other); err == nil || !strings.Contains(err.Error(), "reinstall") {
		t.Errorf("ProbeBrokerPair with another HTTPS port = %v, want a reinstall hint", err)
	}
	if err := ProbeBrokerPair(ctx, h.path, brkUID(), h.httpAddrs[0], h.httpAddrs[0]); err == nil || !strings.Contains(err.Error(), "must differ") {
		t.Errorf("ProbeBrokerPair with equal ports = %v, want a validation error", err)
	}
	if err := ProbeBrokerPair(ctx, h.path, brkUID()+1, h.httpAddrs[0], h.httpsAddrs[0]); err == nil {
		t.Error("ProbeBrokerPair as another UID = nil, want a refusal")
	}
}

func TestProbeBrokerPairRefusesBrokerWithoutPorts(t *testing.T) {
	t.Parallel()
	ctx := brkContext(t)

	t.Run("broker built without ports", func(t *testing.T) {
		t.Parallel()
		h := bpStart(t, false, func(c *BrokerConfig) { c.HTTPPort, c.HTTPSPort = 0, 0 })
		err := ProbeBrokerPair(ctx, h.path, brkUID(), h.httpAddrs[0], h.httpsAddrs[0])
		if err == nil || !strings.Contains(err.Error(), "does not report its ports") {
			t.Fatalf("ProbeBrokerPair = %v, want a refusal about missing ports", err)
		}
		if err := ProbeBroker(ctx, h.path, brkUID(), h.httpsAddrs[0]); err != nil {
			t.Errorf("ProbeBroker = %v, want nil: ports are optional for it", err)
		}
	})

	t.Run("broker that predates the port report", func(t *testing.T) {
		t.Parallel()
		// The old health endpoint writes the ready byte and nothing else.
		control := filepath.Join(brkDir(t), "old.sock")
		ul, err := net.ListenUnix("unix", &net.UnixAddr{Name: brokerHealthPath(control), Net: "unix"})
		if err != nil {
			t.Fatal(err)
		}
		done := make(chan struct{})
		go func() {
			defer close(done)
			c, err := ul.AcceptUnix()
			if err != nil {
				return
			}
			_, _ = c.Write([]byte{brokerStatusReady}) // a failed write shows up as a probe error
			_ = c.Close()                             // nothing to report on a closed probe
		}()
		t.Cleanup(func() {
			brkClose(t, "old health endpoint", ul)
			<-done
		})
		a := &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 80}
		b := &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 443}
		if err := ProbeBrokerPair(ctx, control, brkUID(), a, b); err == nil || !strings.Contains(err.Error(), "does not report its ports") {
			t.Fatalf("ProbeBrokerPair against an old broker = %v, want a refusal about missing ports", err)
		}
	})
}

func TestNewBrokerRejectsBadPorts(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name        string
		http, https uint16
	}{
		{"only http", 80, 0},
		{"only https", 0, 443},
		{"equal", 8080, 8080},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			ln := brkLoopback(t, "tcp4")
			defer brkClose(t, "listener", ln)
			ul, err := ListenBrokerSocket(filepath.Join(brkDir(t), "b.sock"), brkUID())
			if err != nil {
				t.Fatal(err)
			}
			defer brkClose(t, "control", ul)
			_, err = NewBroker(BrokerConfig{OwnerUID: brkUID(), Listeners: []*net.TCPListener{ln}, Control: ul, HTTPPort: tt.http, HTTPSPort: tt.https})
			if err == nil {
				t.Fatal("NewBroker error = nil, want a port error")
			}
		})
	}
}

func TestValidateBrokerPorts(t *testing.T) {
	t.Parallel()
	tests := []struct {
		http, https int
		ok          bool
	}{
		{80, 443, true},
		{1, 65535, true},
		{8080, 8443, true},
		{0, 443, false},
		{80, 0, false},
		{-1, 443, false},
		{80, 65536, false},
		{443, 443, false},
	}
	for _, tt := range tests {
		err := ValidateBrokerPorts(tt.http, tt.https)
		if (err == nil) != tt.ok {
			t.Errorf("ValidateBrokerPorts(%d, %d) = %v, want ok=%v", tt.http, tt.https, err, tt.ok)
		}
	}
}

func TestBrokerCmdParseArgsPorts(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name        string
		args        []string
		http, https uint16
		wantErr     string
	}{
		{name: "defaults", args: []string{"--uid", "501"}, http: 80, https: 443},
		{name: "both", args: []string{"--uid", "501", "--http-port", "8080", "--https-port=8443"}, http: 8080, https: 8443},
		{name: "ports first", args: []string{"--https-port", "9443", "--uid", "501"}, http: 80, https: 9443},
		{name: "only http", args: []string{"--uid", "501", "--http-port", "8080"}, http: 8080, https: 443},
		{name: "swap defaults", args: []string{"--uid", "501", "--http-port", "443", "--https-port", "80"}, http: 443, https: 80},
		{name: "http collides with default https", args: []string{"--uid", "501", "--http-port", "443"}, wantErr: "must differ"},
		{name: "https collides with default http", args: []string{"--uid", "501", "--https-port", "80"}, wantErr: "must differ"},
		{name: "equal", args: []string{"--uid", "501", "--http-port", "8080", "--https-port", "8080"}, wantErr: "must differ"},
		{name: "zero", args: []string{"--uid", "501", "--http-port", "0"}, wantErr: "1-65535"},
		{name: "too large", args: []string{"--uid", "501", "--https-port", "65536"}, wantErr: "1-65535"},
		{name: "negative", args: []string{"--uid", "501", "--https-port", "-1"}, wantErr: "1-65535"},
		{name: "signed", args: []string{"--uid", "501", "--https-port", "+443"}, wantErr: "1-65535"},
		{name: "not a number", args: []string{"--uid", "501", "--http-port", "http"}, wantErr: "1-65535"},
		{name: "empty", args: []string{"--uid", "501", "--http-port", ""}, wantErr: "1-65535"},
		{name: "repeated", args: []string{"--uid", "501", "--http-port", "81", "--http-port", "82"}, wantErr: "once"},
		{name: "ports without uid", args: []string{"--http-port", "8080"}, wantErr: "--uid"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, err := brokerCmdParseArgs(tt.args)
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("brokerCmdParseArgs(%q) = %+v, %v; want an error containing %q", tt.args, got, err, tt.wantErr)
				}
				return
			}
			if err != nil || got.httpPort != tt.http || got.httpsPort != tt.https {
				t.Fatalf("brokerCmdParseArgs(%q) = %+v, %v; want HTTP %d, HTTPS %d", tt.args, got, err, tt.http, tt.https)
			}
		})
	}
}

func TestBrokerCmdBinds(t *testing.T) {
	t.Parallel()
	got := brokerCmdBinds(8080, 8443)
	want := []brokerCmdBind{
		{"tcp4", "127.0.0.1:8443"}, {"tcp6", "[::1]:8443"},
		{"tcp4", "127.0.0.1:8080"}, {"tcp6", "[::1]:8080"},
	}
	if len(got) != len(want) {
		t.Fatalf("brokerCmdBinds = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("brokerCmdBinds[%d] = %v, want %v", i, got[i], want[i])
		}
	}
}

func TestRenderServiceUnitsPorts(t *testing.T) {
	t.Parallel()
	const helper = "/usr/local/libexec/jailoc-listener-broker"

	t.Run("no ports keeps the legacy unit", func(t *testing.T) {
		t.Parallel()
		unit, err := RenderSystemdUnit(helper, 1000)
		if err != nil || !strings.Contains(string(unit), "ExecStart="+helper+" --uid 1000\n") {
			t.Fatalf("RenderSystemdUnit = %q, %v", unit, err)
		}
		plist, err := RenderLaunchDaemonPlist(helper, 1000)
		if err != nil || strings.Contains(string(plist), "port") {
			t.Fatalf("RenderLaunchDaemonPlist = %q, %v; want no port arguments", plist, err)
		}
	})

	t.Run("explicit ports are always written", func(t *testing.T) {
		t.Parallel()
		// Even the defaults, so an old unit differs from the expected one.
		for _, ports := range [][]int{{80, 443}, {8080, 8443}} {
			unit, err := RenderSystemdUnit(helper, 1000, ports...)
			want := "ExecStart=" + helper + " --uid 1000 --http-port " + strconv.Itoa(ports[0]) + " --https-port " + strconv.Itoa(ports[1]) + "\n"
			if err != nil || !strings.Contains(string(unit), want) {
				t.Fatalf("RenderSystemdUnit(%v) = %q, %v; want %q", ports, unit, err, want)
			}
			plist, err := RenderLaunchDaemonPlist(helper, 1000, ports...)
			want = "\t\t<string>1000</string>\n\t\t<string>--http-port</string>\n\t\t<string>" + strconv.Itoa(ports[0]) +
				"</string>\n\t\t<string>--https-port</string>\n\t\t<string>" + strconv.Itoa(ports[1]) + "</string>\n\t</array>\n"
			if err != nil || !strings.Contains(string(plist), want) {
				t.Fatalf("RenderLaunchDaemonPlist(%v) = %q, %v; want %q", ports, plist, err, want)
			}
		}
	})

	t.Run("invalid ports", func(t *testing.T) {
		t.Parallel()
		for _, ports := range [][]int{{8080}, {1, 2, 3}, {0, 443}, {80, 65536}, {443, 443}, {-80, 443}} {
			if unit, err := RenderSystemdUnit(helper, 1000, ports...); err == nil {
				t.Errorf("RenderSystemdUnit(%v) = %q, want an error", ports, unit)
			}
			if plist, err := RenderLaunchDaemonPlist(helper, 1000, ports...); err == nil {
				t.Errorf("RenderLaunchDaemonPlist(%v) = %q, want an error", ports, plist)
			}
		}
	})
}
