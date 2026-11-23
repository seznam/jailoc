//go:build vmintegration

package vmintegration

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/seznam/jailoc/internal/vm"
)

// runnerProbeEnv carries the script that a vmintegration build of jailoc runs
// inside the Landlock domain of the VM runner (internal/vm/vmm_sandbox_probe_linux.go).
const runnerProbeEnv = "JAILOC_VMINTEGRATION_RUNNER_PROBE"

// runnerProbeCode tries one access per argument (name|op|path) and prints the
// name with "ok" or the errno name. The paths may reference $RUNNER_PID and
// $SUPERVISOR_PID.
const runnerProbeCode = `import errno, os, socket, sys
def run(op, path):
    try:
        if op == "read":
            with open(path, "rb") as f:
                f.read(1)
        elif op == "append":
            os.close(os.open(path, os.O_WRONLY | os.O_APPEND))
        elif op == "create":
            os.close(os.open(path, os.O_WRONLY | os.O_CREAT | os.O_EXCL, 0o600))
            os.unlink(path)
        elif op == "connect":
            with socket.socket(socket.AF_UNIX) as s:
                s.settimeout(5)
                s.connect(path)
        else:
            return "unknown-op"
        return "ok"
    except OSError as e:
        return errno.errorcode.get(e.errno, str(e.errno))
for arg in sys.argv[1:]:
    name, op, path = arg.split("|", 2)
    print(name, run(op, os.path.expandvars(path)), flush=True)
`

type runnerProbe struct {
	name, op, path string
	allow          bool
}

// runnerProbes lists the accesses the confined runner must and must not get:
// its shares, its own /proc entry, and host files, sockets and processes
// outside its rules.
func runnerProbes(agentSock, snapshot string) []runnerProbe {
	state := stateDir()
	return []runnerProbe{
		{"share-read", "read", filepath.Join(project, "README.txt"), true},
		{"share-create", "create", filepath.Join(project, ".runner-probe"), true},
		{"own-proc-read", "read", "/proc/$RUNNER_PID/environ", true},
		{"config-read", "read", filepath.Join(home, ".config", "jailoc", "config.toml"), false},
		{"share-dotdot-read", "read", project + "/../.config/jailoc/config.toml", false},
		{"ssh-key-read", "read", filepath.Join(state, "id_ed25519"), false},
		{"home-create", "create", filepath.Join(home, "runner-probe-escape"), false},
		{"share-dotdot-create", "create", project + "/../runner-probe-dotdot", false},
		{"snapshot-write", "append", snapshot, false},
		{"supervisor-log-write", "append", filepath.Join(state, "supervisor.log"), false},
		{"spec-write", "append", vm.SpecFile(state), false},
		{"supervisor-proc-read", "read", "/proc/$SUPERVISOR_PID/environ", false},
		{"ssh-agent-connect", "connect", agentSock, false},
	}
}

func shellQuote(s string) string { return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'" }

// runnerProbeScript is the shell script the probe runs with /bin/sh. It needs
// python3 on the host; the runner's rules let it execute anything under /usr.
func runnerProbeScript(probes []runnerProbe) string {
	args := make([]string, len(probes))
	for i, p := range probes {
		args[i] = shellQuote(p.name + "|" + p.op + "|" + p.path)
	}
	return "echo resolve_unix \"$RESOLVE_UNIX\"\nexec python3 -I -c " + shellQuote(runnerProbeCode) + " " + strings.Join(args, " ")
}

// checkRunnerProbe reads the output of the probe that ran in the runner's
// Landlock domain when the VM started.
func checkRunnerProbe(t *testing.T, probes []runnerProbe) {
	t.Helper()
	out, err := os.ReadFile(filepath.Join(stateDir(), "microvm", "runner-probe.log")) //nolint:gosec // G304: test state dir
	if err != nil {
		t.Fatalf("runner probe did not run: %v", err)
	}
	results := map[string]string{}
	resolveUnix := false
	for line := range strings.Lines(strings.TrimSpace(string(out))) {
		f := strings.Fields(line)
		switch {
		case len(f) == 2 && f[0] == "resolve_unix":
			resolveUnix = f[1] == "1"
		case len(f) == 2:
			results[f[0]] = f[1]
		default:
			t.Fatalf("unexpected runner probe output %q:\n%s", line, out)
		}
	}
	for _, p := range probes {
		got, ok := results[p.name]
		switch {
		case !ok:
			t.Errorf("runner probe %s has no result:\n%s", p.name, out)
		case p.allow:
			if got != "ok" {
				t.Errorf("confined runner: %s %s = %s, want ok", p.op, p.path, got)
			}
		case p.op == "connect" && !resolveUnix:
			t.Logf("kernel cannot restrict Unix socket connections: %s %s = %s", p.op, p.path, got)
		case got == "ok" || got == "ENOENT":
			// ENOENT would only show the target is missing, not that Landlock denied it.
			t.Errorf("confined runner: %s %s = %s, want a Landlock denial", p.op, p.path, got)
		}
		if p.op == "create" && !p.allow {
			if _, err := os.Stat(p.path); !errors.Is(err, os.ErrNotExist) {
				t.Errorf("%s exists on the host after the runner probe: %v", p.path, err)
			}
		}
	}
}

// hostInterfaceAddrs returns the host's own addresses the guest can route to:
// everything but loopback (the guest keeps those for itself) and link-local.
func hostInterfaceAddrs(t *testing.T) []netip.Addr {
	t.Helper()
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		t.Fatal(err)
	}
	var out []netip.Addr
	for _, a := range addrs {
		pfx, err := netip.ParsePrefix(a.String())
		if err != nil {
			continue
		}
		ip := pfx.Addr().Unmap()
		if ip.IsLoopback() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() {
			continue
		}
		out = append(out, ip)
	}
	return out
}

// trafficEvents returns the broker events `jailoc traffic --json` shows.
func trafficEvents(ctx context.Context, t *testing.T, agentSock string) (string, []map[string]any) {
	t.Helper()
	out := jailoc(ctx, t, agentSock, "traffic", "-n", "-1", "--json")
	var events []map[string]any
	for line := range strings.Lines(out) {
		var ev map[string]any
		if json.Unmarshal([]byte(line), &ev) == nil {
			events = append(events, ev)
		}
	}
	return out, events
}

// deniedFlowProblems checks that every destination in want has flow events,
// that none of them was allowed, and that they carry the wanted reason when
// one is given.
func deniedFlowProblems(events []map[string]any, want map[string]string) []string {
	var problems []string
	for dst, reason := range want {
		seen := false
		for _, ev := range events {
			if ev["event"] != "flow" || ev["dst"] != dst {
				continue
			}
			seen = true
			if ev["allow"] != false {
				problems = append(problems, fmt.Sprintf("flow to %s allowed: %v", dst, ev))
			}
			if reason != "" && ev["reason"] != reason {
				problems = append(problems, fmt.Sprintf("flow to %s has reason %v, want %s", dst, ev["reason"], reason))
			}
		}
		if !seen {
			problems = append(problems, "no flow event for "+dst)
		}
	}
	return problems
}

// droppedAnswer reports whether the broker logged a DNS answer for name with
// addr dropped from it.
func droppedAnswer(events []map[string]any, name, addr string) bool {
	for _, ev := range events {
		qname, _ := ev["name"].(string)
		if ev["event"] != "dns" || strings.TrimSuffix(qname, ".") != name {
			continue
		}
		dropped, _ := ev["dropped"].([]any)
		for _, d := range dropped {
			if d == addr {
				return true
			}
		}
	}
	return false
}

// checkNegativeEgress drives the destinations an agent must not reach from
// the guest and checks both the refusal inside the guest and the broker's
// reason in `jailoc traffic`.
func checkNegativeEgress(ctx context.Context, t *testing.T, agentSock string) {
	t.Helper()
	ln, err := net.Listen("tcp", ":0") //nolint:gosec // G102: the test listens on every host address on purpose
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	accepted := make(chan string, 64)
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			accepted <- conn.RemoteAddr().String()
			_ = conn.Close()
		}
	}()
	port := uint16(ln.Addr().(*net.TCPAddr).Port) //nolint:gosec // G115: a TCP port fits uint16

	// Destination (as the broker logs it) -> wanted deny reason; "" accepts any.
	want := map[string]string{
		"9.9.9.9:443":               "default-deny", // public, no rule allows it
		"169.254.169.254:80":        "hard-deny",
		"[fd00:ec2::254]:443":       "ipv6-metadata",
		"[fd00:ec2::23]:443":        "ipv6-metadata",
		"[fd42::1]:443":             "ipv6-private",
		"[fc00::1]:443":             "ipv6-private",
		"8.8.8.8:53":                "dns-filtered", // direct DNS past the gateway resolver
		"1.1.1.1:53":                "dns-filtered", // even to the configured upstream
		"[2606:4700:4700::1111]:53": "dns-filtered",
		// Host loopback through the IPv6 forms that embed 127.0.0.1.
		netip.AddrPortFrom(netip.MustParseAddr("64:ff9b::7f00:1"), port).String(): "",
		netip.AddrPortFrom(netip.MustParseAddr("2002:7f00:1::1"), port).String():  "",
	}
	for _, ip := range hostInterfaceAddrs(t) {
		want[netip.AddrPortFrom(ip, port).String()] = "host-address"
	}
	var tcpTargets []string
	for dst := range want {
		if !strings.HasSuffix(dst, ":53") {
			tcpTargets = append(tcpTargets, shellQuote(dst))
		}
	}

	mustGuest(ctx, t, `python3 - `+strings.Join(tcpTargets, " ")+` <<'PY'
import socket
import sys
for target in sys.argv[1:]:
    host, port = target.rsplit(":", 1)
    host = host.strip("[]")
    family = socket.AF_INET6 if ":" in host else socket.AF_INET
    with socket.socket(family, socket.SOCK_STREAM) as conn:
        conn.settimeout(3)
        if conn.connect_ex((host, int(port))) == 0:
            raise SystemExit("connected to " + target)
print("tcp-denied")
PY`)
	mustGuest(ctx, t, `python3 - <<'PY'
import socket
import struct
query = struct.pack(">6H", 0x1234, 0x0100, 1, 0, 0, 0) + b"\x07example\x03com\x00" + struct.pack(">2H", 1, 1)
for server in ["8.8.8.8", "1.1.1.1", "2606:4700:4700::1111"]:
    family = socket.AF_INET6 if ":" in server else socket.AF_INET
    with socket.socket(family, socket.SOCK_DGRAM) as udp:
        udp.settimeout(3)
        try:
            udp.connect((server, 53))
            udp.send(query)
            udp.recv(512)
        except OSError:
            pass
        else:
            raise SystemExit("direct DNS over UDP answered by " + server)
    with socket.socket(family, socket.SOCK_STREAM) as tcp:
        tcp.settimeout(3)
        if tcp.connect_ex((server, 53)) == 0:
            raise SystemExit("direct DNS over TCP connected to " + server)
print("direct-dns-denied")
PY`)
	// DNS rebinding: *.nip.io is allowed, but the broker drops the private
	// address from the answer before the guest sees it.
	const rebind, rebindAddr = "10-0-0-1.nip.io", "10.0.0.1"
	mustGuest(ctx, t, `python3 - <<'PY'
import socket
try:
    infos = socket.getaddrinfo("`+rebind+`", 80)
except socket.gaierror:
    infos = []
assert not any(info[4][0] == "`+rebindAddr+`" for info in infos), infos
print("rebinding-dropped")
PY`)

	select {
	case from := <-accepted:
		t.Fatalf("host listener on every address accepted a connection from %s", from)
	default:
	}

	// The broker writes events asynchronously; give it a moment.
	deadline := time.Now().Add(15 * time.Second)
	for {
		out, events := trafficEvents(ctx, t, agentSock)
		problems := deniedFlowProblems(events, want)
		if !droppedAnswer(events, rebind, rebindAddr) {
			problems = append(problems, "no DNS event for "+rebind+" with "+rebindAddr+" dropped")
		}
		if len(problems) == 0 {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("traffic log:\n%s\n\n%s", strings.Join(problems, "\n"), tail([]byte(out), 6000))
		}
		time.Sleep(2 * time.Second)
	}
}
