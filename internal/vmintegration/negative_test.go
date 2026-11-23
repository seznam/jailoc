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
	"slices"
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
		case got != "EACCES":
			// Only EACCES proves Landlock denied it: ENOENT would show a missing
			// target and ECONNREFUSED a stale socket, not confinement.
			t.Errorf("confined runner: %s %s = %s, want EACCES", p.op, p.path, got)
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

// flow identifies broker flow events by protocol and destination.
type flow struct{ proto, dst string }

// deniedFlowProblems checks that every flow in want has events, that none of
// them was allowed, and that they carry the wanted reason when one is given.
func deniedFlowProblems(events []map[string]any, want map[flow]string) []string {
	var problems []string
	for f, reason := range want {
		seen := false
		for _, ev := range events {
			if ev["event"] != "flow" || ev["proto"] != f.proto || ev["dst"] != f.dst {
				continue
			}
			seen = true
			if ev["allow"] != false {
				problems = append(problems, fmt.Sprintf("%s flow to %s allowed: %v", f.proto, f.dst, ev))
			}
			if reason != "" && ev["reason"] != reason {
				problems = append(problems, fmt.Sprintf("%s flow to %s has reason %v, want %s", f.proto, f.dst, ev["reason"], reason))
			}
		}
		if !seen {
			problems = append(problems, "no "+f.proto+" flow event for "+f.dst)
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

	// TCP destination (as the broker logs it) -> wanted deny reason; "" accepts any.
	tcpWant := map[string]string{
		"9.9.9.9:443":         "default-deny", // public, no rule allows it
		"169.254.169.254:80":  "hard-deny",
		"[fd00:ec2::254]:443": "ipv6-metadata",
		"[fd00:ec2::23]:443":  "ipv6-metadata",
		"[fd42::1]:443":       "ipv6-private",
		"[fc00::1]:443":       "ipv6-private",
		// Host loopback through the IPv6 forms that embed 127.0.0.1.
		netip.AddrPortFrom(netip.MustParseAddr("64:ff9b::7f00:1"), port).String(): "",
		netip.AddrPortFrom(netip.MustParseAddr("2002:7f00:1::1"), port).String():  "",
	}
	// The broker checks the gateway and the guest network before host
	// addresses (internal/netbroker/policy.go), so a host interface that shares
	// one of them is denied with that reason instead.
	gateways := []netip.Addr{netip.MustParseAddr("192.168.127.1"), netip.MustParseAddr("fd00:127::1")}
	guestNets := []netip.Prefix{netip.MustParsePrefix("192.168.127.0/24"), netip.MustParsePrefix("fd00:127::/64")}
	for _, ip := range hostInterfaceAddrs(t) {
		reason := "host-address"
		switch {
		case slices.Contains(gateways, ip):
			reason = "gateway"
		case slices.ContainsFunc(guestNets, func(p netip.Prefix) bool { return p.Contains(ip) }):
			reason = "guest-network"
		}
		tcpWant[netip.AddrPortFrom(ip, port).String()] = reason
	}
	var tcpTargets []string
	for dst := range tcpWant {
		tcpTargets = append(tcpTargets, shellQuote(dst))
	}

	// A host address the guest also owns (Docker's 172.17.0.1 in an
	// enable_docker workspace) never leaves the VM, so the broker cannot log it.
	out := mustGuest(ctx, t, `python3 - `+strings.Join(tcpTargets, " ")+` <<'PY'
import socket
import sys
for target in sys.argv[1:]:
    host, port = target.rsplit(":", 1)
    host = host.strip("[]")
    family = socket.AF_INET6 if ":" in host else socket.AF_INET
    with socket.socket(family, socket.SOCK_DGRAM) as probe:
        probe.connect((host, int(port)))
        if probe.getsockname()[0] == host:
            print("guest-local", target)
            continue
    with socket.socket(family, socket.SOCK_STREAM) as conn:
        conn.settimeout(3)
        if conn.connect_ex((host, int(port))) == 0:
            raise SystemExit("connected to " + target)
print("tcp-denied")
PY`)
	for line := range strings.Lines(out) {
		if target, ok := strings.CutPrefix(strings.TrimSpace(line), "guest-local "); ok {
			t.Logf("%s is a guest address too; the connection stays inside the VM", target)
			delete(tcpWant, target)
		}
	}
	want := make(map[flow]string)
	for dst, reason := range tcpWant {
		want[flow{"tcp", dst}] = reason
	}
	// Direct DNS past the gateway resolver, even to the configured upstream,
	// over both UDP and TCP.
	for _, dst := range []string{"8.8.8.8:53", "1.1.1.1:53", "[2606:4700:4700::1111]:53"} {
		want[flow{"udp", dst}] = "dns-filtered"
		want[flow{"tcp", dst}] = "dns-filtered"
	}
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

// checkNoVsock checks that the workspace VM attaches no virtio-vsock device
// and that guest AF_VSOCK connections to the host (CID 2) and hypervisor
// (CID 0) fail with ENODEV across a port range.
func checkNoVsock(ctx context.Context, t *testing.T) {
	t.Helper()
	out := mustGuest(ctx, t, `python3 - <<'PY'
import errno
import glob
import socket

for dev in sorted(glob.glob("/sys/bus/virtio/devices/*/device")):
    with open(dev) as f:
        val = f.read().strip().lower()
    if val in ("0x0013", "0x13", "19"):
        raise SystemExit(f"unexpected virtio-vsock device at {dev}: {val}")

ports = list(range(1, 129)) + [1024, 1025, 2222, 5000, 5001, 5002, 7022, 7023, 8080, 9999]
for cid in (0, 2):
    for port in ports:
        with socket.socket(socket.AF_VSOCK, socket.SOCK_STREAM) as s:
            s.settimeout(0.5)
            err = s.connect_ex((cid, port))
        if err != errno.ENODEV:
            raise SystemExit(f"AF_VSOCK connect({cid}, {port}) = {err} ({errno.errorcode.get(err, err)}), want ENODEV")
print("vsock-absent")
PY`)
	if strings.TrimSpace(out) != "vsock-absent" {
		t.Fatalf("vsock check output = %q, want vsock-absent", out)
	}
}
