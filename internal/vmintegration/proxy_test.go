//go:build vmintegration

package vmintegration

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/seznam/jailoc/internal/vm"
)

const proxyWorkspace = "proxyvm"

// proxyVM is a workspace of its own with its own HOME, so it cannot leak into
// the settings that TestWorkspaceVM rewrites with writeConfig. The tests run
// one after the other, never in parallel.
type proxyVM struct {
	home    string
	project string
}

func (d proxyVM) stateDir() string {
	return filepath.Join(d.home, ".cache", "jailoc-vm", "workspaces", proxyWorkspace)
}

// writeConfig writes the proxy workspace configuration. proxy holds the extra
// lines of the proxy table (foo = 3000 is always there).
func (d proxyVM) writeConfig(t *testing.T, proxy string) {
	t.Helper()
	cfg := fmt.Sprintf(`[vm]
runner = %q
lib_dir = %q

[workspaces.%[3]s]
backend = "vm"
paths = [%[4]q]
cpu = 2.0
memory = "3g"
protect_git = true

[workspaces.%[3]s.proxy]
foo = 3000
%[5]s
[workspaces.%[3]s.network]
ipv6 = false
`, os.Getenv("JAILOC_VM_RUNNER"), os.Getenv("JAILOC_VM_LIBDIR"), proxyWorkspace, d.project, proxy)
	dir := filepath.Join(d.home, ".config", "jailoc")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "config.toml"), []byte(cfg), 0o600); err != nil { //nolint:gosec // G703: test directory
		t.Fatal(err)
	}
	if err := os.MkdirAll(d.project, 0o750); err != nil {
		t.Fatal(err)
	}
}

func (d proxyVM) jailoc(ctx context.Context, t *testing.T, args ...string) string {
	t.Helper()
	cmd := exec.CommandContext(ctx, binaryPath, args...) //nolint:gosec // G204: the jailoc binary under test
	cmd.Dir = d.project
	cmd.Env = append(os.Environ(), "HOME="+d.home, "SSH_AUTH_SOCK=")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("jailoc %s: %v\n%s", strings.Join(args, " "), err, tail(out, 6000))
	}
	return string(out)
}

// guest runs a shell command in the proxy VM as the agent user.
func (d proxyVM) guest(ctx context.Context, t *testing.T, command string) (string, error) {
	t.Helper()
	cctx, cancel := context.WithTimeout(ctx, 3*time.Minute)
	defer cancel()
	client, err := vm.DialSSH(cctx, d.stateDir())
	if err != nil {
		t.Fatalf("dial proxy VM: %v", err)
	}
	defer func() { _ = client.Close() }()
	sess, err := client.NewSession()
	if err != nil {
		t.Fatalf("ssh session: %v", err)
	}
	defer func() { _ = sess.Close() }()
	type result struct {
		out []byte
		err error
	}
	done := make(chan result, 1)
	go func() {
		out, err := sess.CombinedOutput(command)
		done <- result{out, err}
	}()
	select {
	case r := <-done:
		return string(r.out), r.err
	case <-cctx.Done():
		return "", fmt.Errorf("command %q timed out", command)
	}
}

func (d proxyVM) mustGuest(ctx context.Context, t *testing.T, command string) string {
	t.Helper()
	out, err := d.guest(ctx, t, command)
	if err != nil {
		t.Fatalf("guest %q: %v\n%s", command, err, out)
	}
	return strings.TrimSpace(out)
}

func (d proxyVM) dumpLogs(t *testing.T) {
	t.Helper()
	for _, name := range []string{"supervisor.log", "microvm/vm.log", "microvm/console.log", "broker.jsonl"} {
		data, err := os.ReadFile(filepath.Join(d.stateDir(), name)) //nolint:gosec // G304: test state file
		if err != nil {
			continue
		}
		t.Logf("==== %s ====\n%s", name, tail(data, 8000))
	}
}

// get fetches path from the guest loopback port through the proxy relay, the
// way the host HTTPS proxy does, and returns the status and body.
func (d proxyVM) get(ctx context.Context, port uint16, path string) (int, string, error) {
	client := &http.Client{
		Timeout: 30 * time.Second,
		Transport: &http.Transport{
			DisableKeepAlives: true,
			DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
				return vm.DialProxy(ctx, d.stateDir(), port)
			},
		},
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://guest.invalid"+path, nil)
	if err != nil {
		return 0, "", fmt.Errorf("build request: %w", err)
	}
	resp, err := client.Do(req)
	if err != nil {
		return 0, "", fmt.Errorf("GET %s via port %d: %w", path, port, err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return resp.StatusCode, "", fmt.Errorf("read body: %w", err)
	}
	return resp.StatusCode, string(body), nil
}

// TestProxyRelay boots a workspace with proxy routes and checks the guest proxy
// relay from the host and from inside the guest.
func TestProxyRelay(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
	defer cancel()
	// Workspace paths may not live under /tmp, so the home is created in the
	// real home directory, like the home of TestWorkspaceVM.
	realHome, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}
	dir, err := os.MkdirTemp(realHome, "jailoc-vm-proxy-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	d := proxyVM{home: dir, project: filepath.Join(dir, "project")}
	d.writeConfig(t, "")
	t.Cleanup(func() {
		if t.Failed() {
			d.dumpLogs(t)
		}
		cctx, ccancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer ccancel()
		cmd := exec.CommandContext(cctx, binaryPath, "down", "--purge", proxyWorkspace) //nolint:gosec // G204: the jailoc binary under test
		cmd.Env = append(os.Environ(), "HOME="+d.home, "SSH_AUTH_SOCK=")
		_ = cmd.Run()
	})

	start := time.Now()
	d.jailoc(ctx, t, "up", proxyWorkspace)
	t.Logf("proxy vm up (image build included): %s", time.Since(start).Round(time.Second))

	t.Run("proxy route to a guest loopback server", func(t *testing.T) {
		// Nothing serves port 3000 yet: the route is allowed but unavailable.
		if conn, err := vm.DialProxy(ctx, d.stateDir(), 3000); !errors.Is(err, vm.ErrProxyUnavailable) {
			if conn != nil {
				_ = conn.Close()
			}
			t.Fatalf("DialProxy(3000) before the server starts = %v, want ErrProxyUnavailable", err)
		}
		d.mustGuest(ctx, t, "cd /tmp && echo proxy-ok > index.html && setsid -f python3 -m http.server 3000 --bind 127.0.0.1 >/dev/null 2>&1")
		var (
			status int
			body   string
			err    error
		)
		for range 30 {
			status, body, err = d.get(ctx, 3000, "/index.html")
			if err == nil && status == http.StatusOK {
				break
			}
			time.Sleep(time.Second)
		}
		if err != nil || status != http.StatusOK || strings.TrimSpace(body) != "proxy-ok" {
			t.Fatalf("GET /index.html via 3000 = %d %q, %v", status, body, err)
		}
	})

	t.Run("ports outside the allowlist are denied", func(t *testing.T) {
		for _, port := range []uint16{3001, 5901, 6081, 22} {
			conn, err := vm.DialProxy(ctx, d.stateDir(), port)
			if err == nil {
				_ = conn.Close()
				t.Fatalf("DialProxy(%d) succeeded, want an error", port)
			}
			if !errors.Is(err, vm.ErrProxyDenied) {
				t.Errorf("DialProxy(%d) = %v, want ErrProxyDenied", port, err)
			}
		}
	})

	t.Run("reload reports proxy changes as restart-only", func(t *testing.T) {
		d.writeConfig(t, "bar = 3002\n")
		out := d.jailoc(ctx, t, "reload", proxyWorkspace)
		for _, want := range []string{"proxy", "jailoc restart " + proxyWorkspace} {
			if !strings.Contains(out, want) {
				t.Fatalf("reload output misses %q:\n%s", want, out)
			}
		}
		// The running guest keeps its relay allowlist.
		if conn, err := vm.DialProxy(ctx, d.stateDir(), 3002); err == nil {
			_ = conn.Close()
			t.Fatal("DialProxy(3002) succeeded after a reload; the allowlist must change only on restart")
		} else if !errors.Is(err, vm.ErrProxyDenied) {
			t.Fatalf("DialProxy(3002) = %v, want ErrProxyDenied", err)
		}

		d.writeConfig(t, "")
		if out := d.jailoc(ctx, t, "reload", proxyWorkspace); strings.Contains(out, "jailoc restart") {
			t.Fatalf("reload of the unchanged settings asks for a restart:\n%s", out)
		}
	})
}
