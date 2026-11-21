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

const desktopWorkspace = "desktopvm"

// desktopVM is a workspace of its own with its own HOME, so enabling the
// desktop (which adds an apt image layer and a second boot) cannot leak into
// the settings that TestWorkspaceVM rewrites with writeConfig. The tests run
// one after the other, never in parallel.
type desktopVM struct {
	home    string
	project string
}

func (d desktopVM) stateDir() string {
	return filepath.Join(d.home, ".cache", "jailoc-vm", "workspaces", desktopWorkspace)
}

// writeConfig writes the desktop workspace configuration. enableDesktop and
// web are the settings a reload reports as restart-only changes; web holds
// the extra lines of the web table (foo = 3000 is always there).
func (d desktopVM) writeConfig(t *testing.T, enableDesktop bool, web string) {
	t.Helper()
	cfg := fmt.Sprintf(`[vm]
runner = %q
lib_dir = %q

[workspaces.%[3]s]
backend = "vm"
paths = [%[4]q]
cpu = 2.0
memory = "3g"
enable_desktop = %[5]t
protect_git = true

[workspaces.%[3]s.proxy]
foo = 3000
%[6]s
[workspaces.%[3]s.network]
ipv6 = false
`, os.Getenv("JAILOC_VM_RUNNER"), os.Getenv("JAILOC_VM_LIBDIR"), desktopWorkspace, d.project, enableDesktop, web)
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

func (d desktopVM) jailoc(ctx context.Context, t *testing.T, args ...string) string {
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

// guest runs a shell command in the desktop VM as the agent user.
func (d desktopVM) guest(ctx context.Context, t *testing.T, command string) (string, error) {
	t.Helper()
	cctx, cancel := context.WithTimeout(ctx, 3*time.Minute)
	defer cancel()
	client, err := vm.DialSSH(cctx, d.stateDir())
	if err != nil {
		t.Fatalf("dial desktop VM: %v", err)
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

func (d desktopVM) mustGuest(ctx context.Context, t *testing.T, command string) string {
	t.Helper()
	out, err := d.guest(ctx, t, command)
	if err != nil {
		t.Fatalf("guest %q: %v\n%s", command, err, out)
	}
	return strings.TrimSpace(out)
}

func (d desktopVM) dumpLogs(t *testing.T) {
	t.Helper()
	for _, name := range []string{"supervisor.log", "microvm/vm.log", "microvm/console.log", "broker.jsonl"} {
		data, err := os.ReadFile(filepath.Join(d.stateDir(), name)) //nolint:gosec // G304: test state file
		if err != nil {
			continue
		}
		t.Logf("==== %s ====\n%s", name, tail(data, 8000))
	}
}

// get fetches path from the guest loopback port through the web relay, the
// way the host web gateway does, and returns the status and body.
func (d desktopVM) get(ctx context.Context, port uint16, path string) (int, string, error) {
	client := &http.Client{
		Timeout: 30 * time.Second,
		Transport: &http.Transport{
			DisableKeepAlives: true,
			DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
				return vm.DialWeb(ctx, d.stateDir(), port)
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

// TestDesktopWeb boots a desktop workspace and checks the native desktop and
// the guest web relay from the host and from inside the guest.
func TestDesktopWeb(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
	defer cancel()
	// Workspace paths may not live under /tmp, so the home is created in the
	// real home directory, like the home of TestWorkspaceVM.
	realHome, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}
	dir, err := os.MkdirTemp(realHome, "jailoc-vm-desktop-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	d := desktopVM{home: dir, project: filepath.Join(dir, "project")}
	d.writeConfig(t, true, "")
	t.Cleanup(func() {
		if t.Failed() {
			d.dumpLogs(t)
		}
		cctx, ccancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer ccancel()
		cmd := exec.CommandContext(cctx, binaryPath, "down", "--purge", desktopWorkspace) //nolint:gosec // G204: the jailoc binary under test
		cmd.Env = append(os.Environ(), "HOME="+d.home, "SSH_AUTH_SOCK=")
		_ = cmd.Run()
	})

	start := time.Now()
	d.jailoc(ctx, t, "up", desktopWorkspace)
	t.Logf("desktop up (image build included): %s", time.Since(start).Round(time.Second))

	t.Run("session display and X server", func(t *testing.T) {
		if got := d.mustGuest(ctx, t, "id -u"); got != "1000" {
			t.Fatalf("id -u = %q, want 1000", got)
		}
		if got := d.mustGuest(ctx, t, `/.jailoc/jailoc-guest run -- sh -c 'echo "$DISPLAY"'`); got != ":1" {
			t.Fatalf("session DISPLAY = %q, want :1", got)
		}
		if got := d.mustGuest(ctx, t, `/.jailoc/jailoc-guest run -- sh -c 'xdpyinfo -display :1 >/dev/null && echo x-ok'`); got != "x-ok" {
			t.Fatalf("xdpyinfo -display :1 = %q", got)
		}
	})

	t.Run("desktop runs as the agent user without TCP X11", func(t *testing.T) {
		out := d.mustGuest(ctx, t, "ps -eo uid=,args=")
		for _, name := range []string{"Xtigervnc", "openbox", "websockify"} {
			found := false
			for line := range strings.SplitSeq(out, "\n") {
				fields := strings.Fields(line)
				if len(fields) < 2 || !strings.Contains(strings.Join(fields[1:], " "), name) {
					continue
				}
				found = true
				if fields[0] != "1000" {
					t.Errorf("%s runs as uid %s, want 1000: %s", name, fields[0], line)
				}
			}
			if !found {
				t.Errorf("no %s process in the guest:\n%s", name, out)
			}
		}
		// Loopback services stay on loopback, the relay is on the guest NIC
		// only, and the X server has no TCP listener (:1 would be 6001).
		d.mustGuest(ctx, t, `python3 - <<'PY'
import socket

listeners = {}
for path, v6 in (("/proc/net/tcp", False), ("/proc/net/tcp6", True)):
    try:
        lines = open(path).read().splitlines()[1:]
    except FileNotFoundError:
        continue
    for line in lines:
        fields = line.split()
        if fields[3] != "0A":
            continue
        addr, port = fields[1].split(":")
        raw = bytes.fromhex(addr)
        if v6:
            ip = socket.inet_ntop(socket.AF_INET6, b"".join(raw[i:i + 4][::-1] for i in range(0, 16, 4)))
        else:
            ip = socket.inet_ntoa(raw[::-1])
        listeners.setdefault(int(port, 16), set()).add(ip)

assert listeners.get(6080) == {"127.0.0.1"}, listeners
assert listeners.get(5901) == {"127.0.0.1"}, listeners
assert listeners.get(6081) == {"192.168.127.2"}, listeners
assert not [p for p in listeners if 6000 <= p <= 6009], listeners
print("listeners-ok")
PY`)
	})

	t.Run("protect_git restrictions apply to the desktop workspace", func(t *testing.T) {
		if out, err := d.guest(ctx, t, "sudo -n id -u"); err == nil {
			t.Fatalf("sudo works with protect_git: %s", out)
		}
	})

	t.Run("noVNC over the web relay", func(t *testing.T) {
		status, body, err := d.get(ctx, 6080, "/vnc.html")
		if err != nil {
			t.Fatal(err)
		}
		if status != http.StatusOK || !strings.Contains(body, "noVNC") {
			t.Fatalf("GET /vnc.html = %d %q, want 200 with the noVNC client", status, tail([]byte(body), 300))
		}
	})

	t.Run("web route to a guest loopback server", func(t *testing.T) {
		// Nothing serves port 3000 yet: the route is allowed but unavailable.
		if conn, err := vm.DialWeb(ctx, d.stateDir(), 3000); !errors.Is(err, vm.ErrWebUnavailable) {
			if conn != nil {
				_ = conn.Close()
			}
			t.Fatalf("DialWeb(3000) before the server starts = %v, want ErrWebUnavailable", err)
		}
		d.mustGuest(ctx, t, "cd /tmp && echo web-ok > index.html && setsid -f python3 -m http.server 3000 --bind 127.0.0.1 >/dev/null 2>&1")
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
		if err != nil || status != http.StatusOK || strings.TrimSpace(body) != "web-ok" {
			t.Fatalf("GET /index.html via 3000 = %d %q, %v", status, body, err)
		}
	})

	t.Run("ports outside the allowlist are denied", func(t *testing.T) {
		// 3001 is not a route; 5901 is the VNC server, 6081 the relay itself
		// and 22 an arbitrary service. None of them is on the allowlist.
		for _, port := range []uint16{3001, 5901, 6081, 22} {
			conn, err := vm.DialWeb(ctx, d.stateDir(), port)
			if err == nil {
				_ = conn.Close()
				t.Fatalf("DialWeb(%d) succeeded, want an error", port)
			}
			if !errors.Is(err, vm.ErrWebDenied) {
				t.Errorf("DialWeb(%d) = %v, want ErrWebDenied", port, err)
			}
		}
	})

	t.Run("reload reports desktop and web changes as restart-only", func(t *testing.T) {
		d.writeConfig(t, false, "bar = 3002\n")
		out := d.jailoc(ctx, t, "reload", desktopWorkspace)
		for _, want := range []string{"enable_desktop", "proxy", "jailoc restart " + desktopWorkspace} {
			if !strings.Contains(out, want) {
				t.Fatalf("reload output misses %q:\n%s", want, out)
			}
		}
		// The running guest keeps its relay allowlist and its desktop.
		if conn, err := vm.DialWeb(ctx, d.stateDir(), 3002); err == nil {
			_ = conn.Close()
			t.Fatal("DialWeb(3002) succeeded after a reload; the allowlist must change only on restart")
		} else if !errors.Is(err, vm.ErrWebDenied) {
			t.Fatalf("DialWeb(3002) = %v, want ErrWebDenied", err)
		}
		if status, body, err := d.get(ctx, 6080, "/vnc.html"); err != nil || status != http.StatusOK || !strings.Contains(body, "noVNC") {
			t.Fatalf("desktop after the reload: GET /vnc.html = %d, %v", status, err)
		}

		d.writeConfig(t, true, "")
		if out := d.jailoc(ctx, t, "reload", desktopWorkspace); strings.Contains(out, "jailoc restart") {
			t.Fatalf("reload of the unchanged settings asks for a restart:\n%s", out)
		}
	})
}
