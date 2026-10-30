//go:build vmintegration

// Package vmintegration boots real workspace VMs with jailoc and checks them
// from the host and from inside the guest. It needs /dev/kvm (Linux) or the
// Hypervisor framework (macOS), a go-microvm runner built from the revision
// pinned in go.mod, and a host Docker daemon for image builds:
//
//	JAILOC_VM_RUNNER=/path/to/go-microvm-runner JAILOC_VM_LIBDIR=/path/to/libs \
//	  go test -tags vmintegration -v -timeout 45m ./internal/vmintegration/
package vmintegration

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"testing"
	"time"

	"golang.org/x/crypto/ssh/agent"

	"github.com/seznam/jailoc/internal/vm"
)

const (
	workspaceName = "vmtest"
	hostPort      = "18080"
	agentComment  = "jailoc-vmintegration"
)

var (
	binaryPath string
	home       string
	project    string
)

func TestMain(m *testing.M) {
	os.Exit(run(m))
}

func run(m *testing.M) int {
	if os.Getenv("JAILOC_VM_RUNNER") == "" {
		fmt.Fprintln(os.Stderr, "JAILOC_VM_RUNNER is not set; skipping VM integration tests")
		return 0
	}
	root, err := projectRoot()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	tmp, err := os.MkdirTemp("", "jailoc-vmintegration-bin-")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	defer func() { _ = os.RemoveAll(tmp) }()
	binaryPath = filepath.Join(tmp, "jailoc")
	for _, args := range [][]string{
		{"generate", "./internal/guestbin"},
		{"build", "-o", binaryPath, "./cmd/jailoc"},
	} {
		cmd := exec.Command("go", args...) //nolint:gosec // G204: fixed go subcommands
		cmd.Dir = root
		cmd.Env = append(os.Environ(), "CGO_ENABLED=0")
		if out, err := cmd.CombinedOutput(); err != nil {
			fmt.Fprintf(os.Stderr, "go %s: %v\n%s", strings.Join(args, " "), err, out)
			return 1
		}
	}
	// Workspace paths may not live under /tmp, so the test home is created
	// in the real home directory.
	realHome, err := os.UserHomeDir()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	home, err = os.MkdirTemp(realHome, "jailoc-vm-")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	defer func() { _ = os.RemoveAll(home) }()
	project = filepath.Join(home, "project")
	return m.Run()
}

func projectRoot() (string, error) {
	dir, err := os.Getwd()
	if err != nil {
		return "", err
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", errors.New("go.mod not found")
		}
		dir = parent
	}
}

// writeConfig writes the test configuration; extra is appended to the
// workspace table.
func writeConfig(t *testing.T, extra string) {
	t.Helper()
	cfg := fmt.Sprintf(`[vm]
runner = %q
lib_dir = %q

[workspaces.%s]
backend = "vm"
paths = [%q]
cpu = 2.0
memory = "3g"
ports = ["%s:8080"]
ssh_auth_sock = true
filtered_dns = true
dns_upstream = "1.1.1.1"
dns_blocked_zones = ["example.org"]
enable_docker = true
%s

[workspaces.%[7]s.secrets.env.VMTEST_TOKEN]
from_env = "VMTEST_HOST_TOKEN"

[workspaces.%[7]s.secrets.env.VMTEST_CMD]
from_command = "echo cmd-token"

[workspaces.%[7]s.secrets.file.vmtest-file]
from_command = "printf 'line1\\nline2'"
`, os.Getenv("JAILOC_VM_RUNNER"), os.Getenv("JAILOC_VM_LIBDIR"), workspaceName, project, hostPort, extra, workspaceName)
	dir := filepath.Join(home, ".config", "jailoc")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "config.toml"), []byte(cfg), 0o600); err != nil { //nolint:gosec // G703: test directory
		t.Fatal(err)
	}
	if err := os.MkdirAll(project, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(project, "README.txt"), []byte("hello from host\n"), 0o600); err != nil { //nolint:gosec // G703: test directory
		t.Fatal(err)
	}
}

// startAgent serves an in-memory SSH agent with one generated key.
func startAgent(t *testing.T) string {
	t.Helper()
	_, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	keyring := agent.NewKeyring()
	if err := keyring.Add(agent.AddedKey{PrivateKey: key, Comment: agentComment}); err != nil {
		t.Fatal(err)
	}
	sock := filepath.Join(home, "agent.sock")
	l, err := net.Listen("unix", sock)
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
				_ = agent.ServeAgent(keyring, c)
			}()
		}
	}()
	return sock
}

func jailoc(ctx context.Context, t *testing.T, agentSock string, args ...string) string {
	t.Helper()
	cmd := exec.CommandContext(ctx, binaryPath, args...) //nolint:gosec // G204: the jailoc binary under test
	cmd.Dir = project
	cmd.Env = append(os.Environ(),
		"HOME="+home,
		"SSH_AUTH_SOCK="+agentSock,
		"VMTEST_HOST_TOKEN=token-from-host",
		"OPENCODE_SERVER_PASSWORD=vmintegration",
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("jailoc %s: %v\n%s", strings.Join(args, " "), err, tail(out, 6000))
	}
	return string(out)
}

func tail(b []byte, n int) []byte {
	if len(b) > n {
		return b[len(b)-n:]
	}
	return b
}

func stateDir() string {
	return filepath.Join(home, ".cache", "jailoc-vm", "workspaces", workspaceName)
}

// guest runs a shell command in the workspace VM as the agent user.
func guest(ctx context.Context, t *testing.T, command string) (string, error) {
	t.Helper()
	cctx, cancel := context.WithTimeout(ctx, 3*time.Minute)
	defer cancel()
	client, err := vm.DialSSH(cctx, stateDir())
	if err != nil {
		t.Fatalf("dial workspace VM: %v", err)
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

func mustGuest(ctx context.Context, t *testing.T, command string) string {
	t.Helper()
	out, err := guest(ctx, t, command)
	if err != nil {
		t.Fatalf("guest %q: %v\n%s", command, err, out)
	}
	return strings.TrimSpace(out)
}

func dumpLogs(t *testing.T) {
	t.Helper()
	for _, name := range []string{"supervisor.log", "microvm/vm.log", "microvm/console.log", "broker.jsonl"} {
		data, err := os.ReadFile(filepath.Join(stateDir(), name)) //nolint:gosec // G304: test state file
		if err != nil {
			continue
		}
		t.Logf("==== %s ====\n%s", name, tail(data, 8000))
	}
}

func TestWorkspaceVM(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Minute)
	defer cancel()
	snapshot := filepath.Join(home, "snapshot.txt")
	if err := os.WriteFile(snapshot, []byte("first\n"), 0o600); err != nil { //nolint:gosec // G703: test directory
		t.Fatal(err)
	}
	writeConfig(t, fmt.Sprintf(`snapshots = ["%s:/home/agent/.vmtest-snapshot"]`, snapshot))
	agentSock := startAgent(t)
	t.Cleanup(func() {
		if t.Failed() {
			dumpLogs(t)
		}
		cctx, ccancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer ccancel()
		cmd := exec.CommandContext(cctx, binaryPath, "down", "--purge", workspaceName)
		cmd.Env = append(os.Environ(), "HOME="+home)
		_ = cmd.Run()
	})

	start := time.Now()
	jailoc(ctx, t, agentSock, "up", workspaceName)
	t.Logf("first up: %s", time.Since(start).Round(time.Second))

	t.Run("agent user and sudo", func(t *testing.T) {
		if got := mustGuest(ctx, t, "id -u"); got != "1000" {
			t.Fatalf("id -u = %q, want 1000", got)
		}
		if got := mustGuest(ctx, t, "sudo -n id -u"); got != "0" {
			t.Fatalf("sudo id -u = %q", got)
		}
		if got := mustGuest(ctx, t, "stat -c %u:%g /etc/passwd ~ "+project); got != "0:0\n1000:1000\n1000:1000" {
			t.Fatalf("guest ownership = %q, want root files, agent home and project", got)
		}
	})

	t.Run("runner confined on Linux", func(t *testing.T) {
		if runtime.GOOS != "linux" {
			t.Skip("Landlock confinement of the runner is Linux-only")
		}
		want, err := filepath.EvalSymlinks(os.Getenv("JAILOC_VM_RUNNER"))
		if err != nil {
			t.Fatal(err)
		}
		exes, _ := filepath.Glob("/proc/[0-9]*/exe")
		found := false
		for _, exe := range exes {
			if target, err := os.Readlink(exe); err != nil || target != want {
				continue
			}
			status, err := os.ReadFile(filepath.Join(filepath.Dir(exe), "status")) //nolint:gosec // G304: procfs
			if err != nil {
				continue
			}
			found = true
			// jailoc sets no_new_privs right before it applies the Landlock domain.
			if !strings.Contains(string(status), "NoNewPrivs:\t1") {
				t.Fatalf("runner %s was started without the Landlock confinement", filepath.Dir(exe))
			}
		}
		if !found {
			t.Fatal("no running go-microvm-runner process found")
		}
	})

	t.Run("project share", func(t *testing.T) {
		if got := mustGuest(ctx, t, "cat "+filepath.Join(project, "README.txt")); got != "hello from host" {
			t.Fatalf("README = %q", got)
		}
		mustGuest(ctx, t, "echo from-guest > "+filepath.Join(project, "guest.txt"))
		data, err := os.ReadFile(filepath.Join(project, "guest.txt")) //nolint:gosec // G304: test file
		if err != nil || strings.TrimSpace(string(data)) != "from-guest" {
			t.Fatalf("guest file on host = %q, %v", data, err)
		}
		// Files the agent creates belong to the host user.
		if info, err := os.Stat(filepath.Join(project, "guest.txt")); err != nil {
			t.Fatal(err)
		} else if st, ok := info.Sys().(*syscall.Stat_t); ok && int(st.Uid) != os.Getuid() {
			t.Fatalf("guest file owner on host = %d, want %d", st.Uid, os.Getuid())
		}
	})

	t.Run("secrets", func(t *testing.T) {
		got := mustGuest(ctx, t, `/.jailoc/jailoc-guest run -- sh -c 'echo "$VMTEST_TOKEN $VMTEST_CMD"; ls /run/secrets; cat /run/secrets/vmtest-file'`)
		if got != "token-from-host cmd-token\nvmtest-file\nline1\nline2" {
			t.Fatalf("secrets = %q", got)
		}
	})

	t.Run("network policy", func(t *testing.T) {
		if got := mustGuest(ctx, t, "curl -sS -m 20 -o /dev/null -w '%{http_code}' https://example.com"); got != "200" {
			t.Fatalf("public https = %q", got)
		}
		for _, target := range []string{"http://10.0.0.1/", "http://192.168.127.1:22/", "http://169.254.169.254/"} {
			if out, err := guest(ctx, t, "curl -sS -m 5 -o /dev/null "+target); err == nil {
				t.Fatalf("%s reachable from the guest: %s", target, out)
			}
		}
	})

	t.Run("traffic, strace and exec", func(t *testing.T) {
		traffic := jailoc(ctx, t, agentSock, "traffic", "-n", "-1", "--json")
		for _, want := range []string{`"event":"l7"`, `"sni":"example.com"`, `"reason":"denied-range"`} {
			if !strings.Contains(traffic, want) {
				t.Fatalf("traffic log misses %s:\n%s", want, tail([]byte(traffic), 4000))
			}
		}
		if procs := jailoc(ctx, t, agentSock, "strace", "-n", "-1"); !strings.Contains(procs, "curl -sS -m 20") {
			t.Fatalf("strace misses the curl command:\n%s", tail([]byte(procs), 4000))
		}
		if got := strings.TrimSpace(jailoc(ctx, t, agentSock, "exec", "--", "sh", "-c", "id -un; pwd")); got != "agent\n"+project {
			t.Fatalf("exec output = %q", got)
		}
		cmd := exec.CommandContext(ctx, binaryPath, "exec", "--", "sh", "-c", "exit 5") //nolint:gosec // G204: the jailoc binary under test
		cmd.Dir = project
		cmd.Env = append(os.Environ(), "HOME="+home)
		var exitErr *exec.ExitError
		if err := cmd.Run(); !errors.As(err, &exitErr) || exitErr.ExitCode() != 5 {
			t.Fatalf("exec exit status = %v, want 5", err)
		}
	})

	t.Run("filtered DNS", func(t *testing.T) {
		if out, err := guest(ctx, t, "getent hosts example.org"); err == nil {
			t.Fatalf("blocked zone resolved: %s", out)
		}
		mustGuest(ctx, t, "getent hosts example.com")
	})

	t.Run("IPv6 configuration and policy", func(t *testing.T) {
		// The image has no iproute2; read the kernel tables directly.
		mustGuest(ctx, t, "grep -E '^fd000127000000000000000000000002 [0-9a-f]+ 40 .*eth0$' /proc/net/if_inet6")
		mustGuest(ctx, t, "grep -E '^0{32} 00 0{32} 00 fe800000000000000000000000000001 .*eth0$' /proc/net/ipv6_route")
		mustGuest(ctx, t, `python3 - <<'PY'
import errno
import socket
for target in ["fd42::1", "fd00:127::1", "fd00:ec2::254", "2001:db8::1"]:
    with socket.socket(socket.AF_INET6, socket.SOCK_STREAM) as conn:
        conn.settimeout(3)
        result = conn.connect_ex((target, 443))
        assert result == errno.ECONNREFUSED, (target, result)
print("ipv6-policy-ok")
PY`)
		mustGuest(ctx, t, `python3 - <<'PY'
import socket
assert socket.getaddrinfo("example.com", 443, socket.AF_INET6)
try:
    socket.getaddrinfo("example.org", 443, socket.AF_INET6)
except socket.gaierror:
    pass
else:
    raise AssertionError("blocked AAAA resolved")
print("ipv6-dns-ok")
PY`)
	})

	t.Run("memory: zram swap and reclaim", func(t *testing.T) {
		// The reclaim loop writes the root cgroup's memory.reclaim.
		mustGuest(ctx, t, "echo 1M swappiness=0 | sudo -n tee /sys/fs/cgroup/memory.reclaim")
		if got := mustGuest(ctx, t, "cat /proc/swaps"); !strings.Contains(got, "/dev/zram0") {
			t.Fatalf("/proc/swaps = %q", got)
		}
		if got := mustGuest(ctx, t, "cat /proc/sys/vm/swappiness"); got != "100" {
			t.Fatalf("swappiness = %q", got)
		}
	})

	t.Run("SSH agent", func(t *testing.T) {
		if got := mustGuest(ctx, t, "/.jailoc/jailoc-guest run -- ssh-add -l"); !strings.Contains(got, agentComment) {
			t.Fatalf("ssh-add -l = %q", got)
		}
	})

	t.Run("Docker", func(t *testing.T) {
		if got := mustGuest(ctx, t, "docker run --rm alpine:3.20 echo docker-ok"); !strings.HasSuffix(got, "docker-ok") {
			t.Fatalf("docker run = %q", got)
		}
	})

	t.Run("published port", func(t *testing.T) {
		mustGuest(ctx, t, "cd /tmp && echo port-ok > index.html && setsid -f python3 -m http.server 8080 --bind 0.0.0.0 >/dev/null 2>&1")
		var body string
		for range 30 {
			resp, err := http.Get("http://127.0.0.1:" + hostPort + "/index.html") //nolint:gosec,noctx // G107: fixed test URL
			if err == nil {
				b, _ := io.ReadAll(resp.Body)
				_ = resp.Body.Close()
				body = strings.TrimSpace(string(b))
				if body == "port-ok" {
					return
				}
			}
			time.Sleep(time.Second)
		}
		t.Fatalf("published port answered %q", body)
	})

	t.Run("reload re-copies snapshots and applies env", func(t *testing.T) {
		if got := mustGuest(ctx, t, "cat ~/.vmtest-snapshot"); got != "first" {
			t.Fatalf("snapshot = %q", got)
		}
		if out, err := guest(ctx, t, "echo x > ~/.vmtest-snapshot"); err == nil {
			t.Fatalf("snapshot writable: %s", out)
		}
		if err := os.WriteFile(snapshot, []byte("second\n"), 0o600); err != nil { //nolint:gosec // G703: test directory
			t.Fatal(err)
		}
		extra := filepath.Join(home, "extra.txt")
		if err := os.WriteFile(extra, []byte("extra\n"), 0o600); err != nil { //nolint:gosec // G703: test directory
			t.Fatal(err)
		}
		writeConfig(t, fmt.Sprintf(`snapshots = ["%s:/home/agent/.vmtest-snapshot", "%s:/home/agent/.config/vmtest/extra.txt"]
env = ["VMTEST_RELOADED=yes"]`, snapshot, extra))
		if out := jailoc(ctx, t, agentSock, "reload", workspaceName); strings.Contains(out, "jailoc restart") {
			t.Fatalf("reload asks for a restart:\n%s", out)
		}
		if got := mustGuest(ctx, t, "cat ~/.vmtest-snapshot ~/.config/vmtest/extra.txt"); got != "second\nextra" {
			t.Fatalf("snapshots after reload = %q", got)
		}
		if got := mustGuest(ctx, t, `/.jailoc/jailoc-guest run -- sh -c 'echo "$VMTEST_RELOADED"'`); got != "yes" {
			t.Fatalf("env after reload = %q", got)
		}

		writeConfig(t, fmt.Sprintf(`snapshots = ["%s:/home/agent/.vmtest-snapshot"]`, snapshot))
		jailoc(ctx, t, agentSock, "reload", workspaceName)
		if out, err := guest(ctx, t, "test -e ~/.config/vmtest/extra.txt"); err == nil {
			t.Fatalf("removed snapshot still present: %s", out)
		}
		if got := mustGuest(ctx, t, `/.jailoc/jailoc-guest run -- sh -c 'echo "${VMTEST_RELOADED:-unset}"'`); got != "unset" {
			t.Fatalf("removed env after reload = %q", got)
		}
	})

	t.Run("restart keeps home and Docker data", func(t *testing.T) {
		mustGuest(ctx, t, "echo persisted > ~/.cache/marker")
		jailoc(ctx, t, agentSock, "down", workspaceName)
		if out := jailoc(ctx, t, agentSock, "status", workspaceName); !strings.Contains(out, "stopped") {
			t.Fatalf("status after down:\n%s", out)
		}
		start := time.Now()
		jailoc(ctx, t, agentSock, "up", workspaceName)
		t.Logf("cached up: %s", time.Since(start).Round(time.Second))
		if got := mustGuest(ctx, t, "cat ~/.cache/marker"); got != "persisted" {
			t.Fatalf("home marker = %q", got)
		}
		// dockerd starts in the background after the VM is up.
		if got := mustGuest(ctx, t, "for i in $(seq 1 60); do docker info >/dev/null 2>&1 && break; sleep 0.5; done; docker image ls --format '{{.Repository}}:{{.Tag}}'"); !strings.Contains(got, "alpine:3.20") {
			t.Fatalf("docker images after restart = %q", got)
		}
	})
}
