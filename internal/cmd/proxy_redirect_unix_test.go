//go:build darwin || linux

package cmd

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/seznam/jailoc/internal/config"
	"github.com/seznam/jailoc/internal/proxyhost"
)

// doHTTP sends a plain HTTP request for target with the Host header host to
// the HTTP redirect listener at addr, without following redirects.
func doHTTP(t *testing.T, addr, method, host, target string) *http.Response {
	t.Helper()
	tr := &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "tcp", addr)
	}}
	t.Cleanup(tr.CloseIdleConnections)
	client := &http.Client{Transport: tr, Timeout: 5 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	req, err := http.NewRequestWithContext(t.Context(), method, "http://"+host+target, nil)
	if err != nil {
		t.Fatal(err)
	}
	res, err := client.Do(req)
	if err != nil {
		t.Fatalf("%s http://%s%s: %v", method, host, target, err)
	}
	t.Cleanup(func() { _ = res.Body.Close() })
	return res
}

// writeProxyTestConfig replaces the configuration file of the test.
func writeProxyTestConfig(t *testing.T, text string) error {
	t.Helper()
	return os.WriteFile(config.ConfigPath(), []byte(text), 0o600)
}

func (r *proxyRun) stop(t *testing.T) {
	t.Helper()
	r.cancel()
	select {
	case <-r.done:
		if r.err != nil {
			t.Fatalf("proxy ended with %v on cancel, want nil", r.err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("proxy ignored cancellation")
	}
}

func TestProxyRedirectsHTTPUnderOneLifetime(t *testing.T) {
	run, _ := startProxyRunFor(t, http.NotFoundHandler(),
		"[workspaces.e2e.proxy]\napp = { port = 3000, auth = true }\nraw = { port = 3001, redirect = false }\n",
		"app.e2e.jailoc.local", relayGuest(3000, 3001))

	res := doHTTP(t, run.httpAddr, "GET", "app.e2e.jailoc.local", "/a%20b?x=1")
	if res.StatusCode != http.StatusTemporaryRedirect || res.Header.Get("Location") != "https://app.e2e.jailoc.local/a%20b?x=1" {
		t.Errorf("route: status %d, Location %q; want a redirect to HTTPS", res.StatusCode, res.Header.Get("Location"))
	}
	// The workspace host is a route of the proxy and redirects by default.
	res = doHTTP(t, run.httpAddr, "GET", "e2e.jailoc.local", "/")
	if res.StatusCode != http.StatusTemporaryRedirect || res.Header.Get("Location") != "https://e2e.jailoc.local/" {
		t.Errorf("workspace host: status %d, Location %q; want a redirect to HTTPS", res.StatusCode, res.Header.Get("Location"))
	}
	// redirect = false and unknown hosts get the same refusal, never a redirect.
	for _, host := range []string{"raw.e2e.jailoc.local", "nope.e2e.jailoc.local", "example.com"} {
		res = doHTTP(t, run.httpAddr, "GET", host, "/")
		if res.StatusCode == http.StatusTemporaryRedirect || res.Header.Get("Location") != "" {
			t.Errorf("%s: status %d, Location %q; want no redirect", host, res.StatusCode, res.Header.Get("Location"))
		}
	}
	// The redirect listener never reaches the login or an upstream.
	res = doHTTP(t, run.httpAddr, "POST", "app.e2e.jailoc.local", "/.jailoc/login")
	if res.StatusCode != http.StatusMethodNotAllowed {
		t.Errorf("POST login over HTTP = %d, want %d", res.StatusCode, http.StatusMethodNotAllowed)
	}

	// Both listeners share one lifetime: stopping the proxy closes them.
	run.stop(t)
	for _, addr := range []string{run.addr, run.httpAddr} {
		if conn, err := net.DialTimeout("tcp", addr, time.Second); err == nil {
			_ = conn.Close()
			t.Errorf("%s still accepts connections after the proxy stopped", addr)
		}
	}
}

func TestProxyBindsConfiguredStandalonePorts(t *testing.T) {
	httpPort, httpsPort := freeLoopbackPort(t), freeLoopbackPort(t)
	cfg := fmt.Sprintf("[proxy]\nstandalone_http_port = %d\nstandalone_https_port = %d\n\n[workspaces.e2e.proxy]\napp = { port = 3000, auth = true }\n", httpPort, httpsPort)
	run, _ := startProxyRunArgs(t, http.NotFoundHandler(), cfg, "app.e2e.jailoc.local", relayGuest(3000), "--foreground")
	if want := fmt.Sprintf("127.0.0.1:%d", httpsPort); run.addr != want {
		t.Errorf("HTTPS listens on %s, want %s", run.addr, want)
	}
	if want := fmt.Sprintf("127.0.0.1:%d", httpPort); run.httpAddr != want {
		t.Errorf("HTTP redirect listens on %s, want %s", run.httpAddr, want)
	}
	// The browser authority is independent of the standalone binds: URLs
	// stay on the default ports, and the output says what to forward.
	out := run.out.String()
	if !strings.Contains(out, "https://app.e2e.jailoc.local/") || strings.Contains(out, fmt.Sprintf(":%d/", httpsPort)) {
		t.Errorf("URLs must use the browser authority, not the standalone port:\n%s", out)
	}
	for _, want := range []string{fmt.Sprintf("forward 127.0.0.1:443 to 127.0.0.1:%d", httpsPort), fmt.Sprintf("forward 127.0.0.1:80 to 127.0.0.1:%d", httpPort)} {
		if !strings.Contains(out, want) {
			t.Errorf("output lacks %q:\n%s", want, out)
		}
	}
	if res := doHTTP(t, run.httpAddr, "GET", "app.e2e.jailoc.local", "/"); res.Header.Get("Location") != "https://app.e2e.jailoc.local/" {
		t.Errorf("redirect Location = %q", res.Header.Get("Location"))
	}
}

func TestProxyListenOverridesOnlyTheHTTPSBind(t *testing.T) {
	httpPort := freeLoopbackPort(t)
	cfg := fmt.Sprintf("[proxy]\nstandalone_http_port = %d\nstandalone_https_port = %d\n\n[workspaces.e2e.proxy]\napp = { port = 3000, auth = true }\n", httpPort, freeLoopbackPort(t))
	run, _ := startProxyRunArgs(t, http.NotFoundHandler(), cfg, "app.e2e.jailoc.local", relayGuest(3000), "--foreground", "--listen", "127.0.0.1:0")
	if want := fmt.Sprintf("127.0.0.1:%d", httpPort); run.httpAddr != want {
		t.Errorf("HTTP redirect listens on %s, want %s", run.httpAddr, want)
	}
	if st := runningProxy(); st == nil || !st.ListenOverride || st.Listen != run.addr || st.RedirectListen != run.httpAddr {
		t.Errorf("state = %+v, want the override and both addresses recorded", st)
	}
}

func TestProxyConfiguredBrowserPorts(t *testing.T) {
	cfg := fmt.Sprintf("[proxy]\nhttp_port = 8080\nhttps_port = 8443\nstandalone_http_port = %d\n\n[workspaces.e2e.proxy]\napp = { port = 3000, auth = true }\n", freeLoopbackPort(t))
	run, _ := startProxyRunArgs(t, http.NotFoundHandler(), cfg, "app.e2e.jailoc.local", relayGuest(3000), "--foreground", "--listen", "127.0.0.1:0")
	out := run.out.String()
	if !strings.Contains(out, "https://app.e2e.jailoc.local:8443/") || !strings.Contains(out, "only valid for https://app.e2e.jailoc.local:8443)") {
		t.Errorf("URLs and the login origin must carry the configured port:\n%s", out)
	}

	// Only the configured authority is served.
	get := func(authority string) int {
		t.Helper()
		req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, "https://"+authority+"/", nil)
		if err != nil {
			t.Fatal(err)
		}
		res, err := run.client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		_ = res.Body.Close()
		return res.StatusCode
	}
	if got := get("app.e2e.jailoc.local:8443"); got != http.StatusSeeOther {
		t.Errorf("configured authority: status %d, want the login redirect", got)
	}
	if got := get("app.e2e.jailoc.local"); got != http.StatusBadRequest {
		t.Errorf("default authority: status %d, want %d", got, http.StatusBadRequest)
	}

	// The redirect target carries the configured HTTPS port, and the HTTP
	// authority is the configured HTTP port.
	res := doHTTP(t, run.httpAddr, "GET", "app.e2e.jailoc.local:8080", "/x")
	if res.Header.Get("Location") != "https://app.e2e.jailoc.local:8443/x" {
		t.Errorf("redirect Location = %q, want the configured HTTPS port", res.Header.Get("Location"))
	}
	if res := doHTTP(t, run.httpAddr, "GET", "app.e2e.jailoc.local", "/x"); res.Header.Get("Location") != "" {
		t.Errorf("default HTTP authority redirected to %q, want a refusal", res.Header.Get("Location"))
	}

	st := runningProxy()
	if st == nil {
		t.Fatal("runningProxy() = nil while the proxy serves")
	}
	var status strings.Builder
	printSyncedProxy(&status, *st, nil, nil)
	if !strings.Contains(status.String(), "https://app.e2e.jailoc.local:8443/") || !strings.Contains(status.String(), "HTTP redirect running on "+run.httpAddr) {
		t.Errorf("status output lacks the configured authority and the redirect address:\n%s", status.String())
	}
}

func TestProxySyncRefusesChangedPortsWithoutChangingScope(t *testing.T) {
	httpPort := freeLoopbackPort(t)
	run, _ := startProxyRunArgs(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, "hello")
	}), fmt.Sprintf("[proxy]\nstandalone_http_port = %d\n\n[workspaces.e2e.proxy]\napp = { port = 3000, auth = true }\n", httpPort), "app.e2e.jailoc.local", relayGuest(3000), "--foreground", "--listen", "127.0.0.1:0")
	st := runningProxy()
	if st == nil {
		t.Fatal("runningProxy() = nil while the proxy serves")
	}
	cookie := run.login(t)
	workspaces := "[workspaces.e2e.proxy]\napp = { port = 3000, auth = true }\n"
	for _, tt := range []struct {
		name, proxyTable, changed string
	}{
		{"browser HTTPS port", "https_port = 8443\nstandalone_http_port = %[1]d\n", "https_port"},
		{"browser HTTP port", "http_port = 8080\nstandalone_http_port = %[1]d\n", "http_port"},
		{"standalone HTTP port", "standalone_http_port = %[2]d\n", "standalone_http_port"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			table := fmt.Sprintf(tt.proxyTable, httpPort, httpPort+1)
			if err := writeProxyTestConfig(t, "[proxy]\n"+table+"\n"+workspaces); err != nil {
				t.Fatal(err)
			}
			resp, err := sendProxyControl(st.ControlSocket, proxyControlRequest{Action: "sync", Config: proxyConfigPath()})
			if err != nil {
				t.Fatal(err)
			}
			assertContains(t, resp.Error, tt.changed)
			assertContains(t, resp.Error, "jailoc proxy")
			// The session of the running proxy survives the refused sync.
			if res := run.do(t, "GET", "/", nil, cookie); res.StatusCode != http.StatusOK {
				t.Errorf("GET / after a refused sync = %d, want 200", res.StatusCode)
			}
		})
	}
}

func TestProxyStatePortChanges(t *testing.T) {
	t.Parallel()
	def := config.ProxyPorts{HTTP: 80, HTTPS: 443, StandaloneHTTP: 7080, StandaloneHTTPS: 7043}
	started := proxyState{HTTPPort: 80, HTTPSPort: 443, StandaloneHTTPPort: 7080, StandaloneHTTPSPort: 7043}
	with := func(f func(*config.ProxyPorts)) config.ProxyPorts { p := def; f(&p); return p }
	for _, tt := range []struct {
		name string
		st   proxyState
		want config.ProxyPorts
		out  []string
	}{
		{"unchanged", started, def, nil},
		{"HTTPS authority", started, with(func(p *config.ProxyPorts) { p.HTTPS = 8443 }), []string{"https_port"}},
		{"HTTP authority", started, with(func(p *config.ProxyPorts) { p.HTTP = 8080 }), []string{"http_port"}},
		{"standalone HTTP", started, with(func(p *config.ProxyPorts) { p.StandaloneHTTP = 7081 }), []string{"standalone_http_port"}},
		{"standalone HTTPS", started, with(func(p *config.ProxyPorts) { p.StandaloneHTTPS = 7044 }), []string{"standalone_https_port"}},
		{"standalone HTTPS under --listen", proxyState{HTTPPort: 80, HTTPSPort: 443, StandaloneHTTPPort: 7080, StandaloneHTTPSPort: 7043, ListenOverride: true}, with(func(p *config.ProxyPorts) { p.StandaloneHTTPS = 7044 }), nil},
		{"standalone ports with the broker", proxyState{HTTPPort: 80, HTTPSPort: 443, ListenerBroker: true}, with(func(p *config.ProxyPorts) { p.StandaloneHTTP, p.StandaloneHTTPS = 1, 2 }), nil},
		{"broker authority", proxyState{HTTPPort: 80, HTTPSPort: 443, ListenerBroker: true}, with(func(p *config.ProxyPorts) { p.HTTPS = 8443 }), []string{"https_port"}},
		{"state from before ports, defaults", proxyState{}, def, nil},
		{"state from before ports, standalone ports differ", proxyState{}, with(func(p *config.ProxyPorts) { p.StandaloneHTTP = 1 }), nil},
		{"state from before ports, authority differs", proxyState{}, with(func(p *config.ProxyPorts) { p.HTTP, p.HTTPS = 8080, 8443 }), []string{"http_port", "https_port"}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := tt.st.portChanges(tt.want); strings.Join(got, ",") != strings.Join(tt.out, ",") {
				t.Errorf("portChanges() = %v, want %v", got, tt.out)
			}
		})
	}
}

func TestProxyListenerBrokerServesHTTPRedirect(t *testing.T) {
	target := startTestListenerBroker(t)
	useProxyBrokerHost(t, target)
	run, _ := startProxyRunArgs(t, http.NotFoundHandler(),
		"[workspaces.e2e.proxy]\napp = { port = 3000, auth = true }\n", "app.e2e.jailoc.local", relayGuest(3000), "--foreground", "--listener-broker")
	if run.httpAddr != target.httpAddr.String() {
		t.Errorf("redirect listener reports %s, want the broker address %s", run.httpAddr, target.httpAddr)
	}
	// Reached through the broker, not through a listener of the proxy.
	res := doHTTP(t, target.httpAddr.String(), "GET", "app.e2e.jailoc.local", "/p")
	if res.StatusCode != http.StatusTemporaryRedirect || res.Header.Get("Location") != "https://app.e2e.jailoc.local/p" {
		t.Errorf("status %d, Location %q; want a redirect to HTTPS", res.StatusCode, res.Header.Get("Location"))
	}
	run.stop(t)
}

func TestServiceOwnedByUIDWithPorts(t *testing.T) {
	t.Parallel()
	const (
		linuxHelper  = "/usr/local/libexec/jailoc-listener-broker"
		darwinHelper = "/Library/PrivilegedHelperTools/jailoc-listener-broker"
	)
	render := func(t *testing.T, goos string, uid uint32, ports ...int) []byte {
		t.Helper()
		var (
			data []byte
			err  error
		)
		if goos == "darwin" {
			data, err = proxyhost.RenderLaunchDaemonPlist(darwinHelper, uid, ports...)
		} else {
			data, err = proxyhost.RenderSystemdUnit(linuxHelper, uid, ports...)
		}
		if err != nil {
			t.Fatal(err)
		}
		return data
	}
	for _, goos := range []string{"darwin", "linux"} {
		helper := linuxHelper
		if goos == "darwin" {
			helper = darwinHelper
		}
		for _, tc := range []struct {
			name      string
			onDisk    func(t *testing.T) []byte
			wantOwned bool
		}{
			{"unit from before the ports, only --uid", func(t *testing.T) []byte { return render(t, goos, 501) }, true},
			{"unit with the expected ports", func(t *testing.T) []byte { return render(t, goos, 501, 8080, 8443) }, true},
			{"unit with other ports", func(t *testing.T) []byte { return render(t, goos, 501, 80, 443) }, true},
			{"other user with ports", func(t *testing.T) []byte { return render(t, goos, 502, 8080, 8443) }, false},
			{"uid that starts with ours, with ports", func(t *testing.T) []byte { return render(t, goos, 5010, 8080, 8443) }, false},
			{"uid that ends with ours, with ports", func(t *testing.T) []byte { return render(t, goos, 1501, 8080, 8443) }, false},
			{"extra argument after the ports", func(t *testing.T) []byte {
				if goos == "darwin" {
					return []byte(strings.Replace(string(render(t, goos, 501, 80, 443)), "</array>", "<string>--other</string></array>", 1))
				}
				return []byte(strings.Replace(string(render(t, goos, 501, 80, 443)), "--https-port 443", "--https-port 443 --other", 1))
			}, false},
		} {
			t.Run(goos+"/"+tc.name, func(t *testing.T) {
				t.Parallel()
				d := proxyHostDeps{goos: goos, readFile: func(string) ([]byte, error) { return tc.onDisk(t), nil }}
				present, owned, _, err := serviceOwnedByUID(d, proxyhost.ServiceUnitPath(goos), helper, 501, 8080, 8443)
				if err != nil {
					t.Fatal(err)
				}
				if !present || owned != tc.wantOwned {
					t.Errorf("serviceOwnedByUID() = (%v, %v), want (true, %v)", present, owned, tc.wantOwned)
				}
			})
		}
	}
}

func TestProxyInstallRendersConfiguredPorts(t *testing.T) {
	cfg := "[proxy]\nhttp_port = 8080\nhttps_port = 8443\n\n" + hostTestConfig
	for _, goos := range []string{"darwin", "linux"} {
		t.Run(goos, func(t *testing.T) {
			h := useHostHarness(t, goos, cfg, hostTestOriginal)
			out, err := h.run("install", "-y")
			if err != nil {
				t.Fatalf("install: %v\n%s", err, out)
			}
			unit := string(h.priv[0].files["JAILOC_UNIT_SRC"])
			if goos == "darwin" {
				for _, want := range []string{"<string>--http-port</string>", "<string>8080</string>", "<string>--https-port</string>", "<string>8443</string>"} {
					if !strings.Contains(unit, want) {
						t.Errorf("plist lacks %q:\n%s", want, unit)
					}
				}
			} else if !strings.Contains(unit, " --uid 501 --http-port 8080 --https-port 8443\n") {
				t.Errorf("unit lacks the configured ports:\n%s", unit)
			}
		})
	}
}
