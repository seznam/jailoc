package netbroker

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestUpstreamHelperOutput(t *testing.T) {
	t.Parallel()
	for _, tt := range []struct {
		name, output string
		valid        bool
	}{
		{"valid", `{"version":1,"url":"http://localhost:3128","future":true,"headers":{"Proxy-Authorization":"Bearer secret"}}`, true},
		{"missing version", `{"url":"http://proxy"}`, false},
		{"future version", `{"version":2,"url":"http://proxy"}`, false},
		{"missing url", `{"version":1}`, false},
		{"trailing", `{"version":1,"url":"http://proxy"} {}`, false},
		{"token", `{"version":1,"url":"http://proxy","headers":{"bad name":"secret"}}`, false},
		{"reserved", `{"version":1,"url":"http://proxy","headers":{"x-jailoc-flow":"secret"}}`, false},
		{"CRLF", `{"version":1,"url":"http://proxy","headers":{"Authorization":"secret\r\nX: y"}}`, false},
		{"tab in value", `{"version":1,"url":"http://proxy","headers":{"Authorization":"secret\tvalue"}}`, false},
		{"duplicate", `{"version":1,"url":"http://proxy","headers":{"Authorization":"one","authorization":"two"}}`, false},
		{"duplicate exact", `{"version":1,"url":"http://proxy","headers":{"Authorization":"one","Authorization":"two"}}`, false},
		{"bad direct", `{"version":1,"url":"http://proxy","direct":["host:0"]}`, false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			_, err := parseUpstreamResult([]byte(tt.output))
			if (err == nil) != tt.valid {
				t.Fatalf("parse = %v, valid %v", err, tt.valid)
			}
		})
	}
	for _, name := range []string{"Host", "Content-Length", "Transfer-Encoding", "Connection", "Proxy-Connection", "Trailer", "Upgrade"} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			data, _ := json.Marshal(map[string]any{"version": 1, "url": "http://proxy", "headers": map[string]string{name: "secret"}})
			if _, err := parseUpstreamResult(data); err == nil {
				t.Fatal("framing override accepted")
			}
		})
	}
}

func TestUpstreamHelperExecution(t *testing.T) {
	t.Setenv("JAILOC_WORKSPACE", "spoofed")
	t.Setenv("JAILOC_UPSTREAM_EVENT", "spoofed")
	t.Setenv("JAILOC_UPSTREAM_VERSION", "spoofed")
	res, err := runUpstreamHelper(t.Context(), `printf '%s|%s|%s|%s' "$JAILOC_WORKSPACE" "$JAILOC_UPSTREAM_EVENT" "$JAILOC_UPSTREAM_VERSION" "$PWD"; read ignored || printf '|eof'`, "test", "reload", time.Second)
	if err != nil || string(res.stdout) != "test|reload|1|/|eof" || res.stderr != "" {
		t.Fatalf("output=%q stderr=%q error=%v", res.stdout, res.stderr, err)
	}
	res, err = runUpstreamHelper(t.Context(), `yes x | dd bs=1024 count=70 2>/dev/null`, "test", "start", time.Second)
	if err == nil || len(res.stdout) > 64<<10 || !res.stdoutOverflow || !strings.Contains(err.Error(), "64 KiB") {
		t.Fatalf("stdout len=%d overflow=%v error=%v", len(res.stdout), res.stdoutOverflow, err)
	}
	res, err = runUpstreamHelper(t.Context(), `yes x | dd bs=1024 count=20 >&2; exit 1`, "test", "reload", time.Second)
	if err == nil || len(res.stderr) > 16<<10 || len(res.stderr) == 0 || !res.stderrOverflow {
		t.Fatalf("stderr len=%d overflow=%v error=%v", len(res.stderr), res.stderrOverflow, err)
	}
	start := time.Now()
	_, err = runUpstreamHelper(t.Context(), `sleep 30 & wait`, "test", "start", 30*time.Millisecond)
	if err == nil || time.Since(start) > 2*time.Second {
		t.Fatalf("unbounded cancellation %s: %v", time.Since(start), err)
	}
}

func TestUpstreamReloadRetainsRoute(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	result := filepath.Join(dir, "result.json")
	stderr := filepath.Join(dir, "stderr")
	command := `if [ "$JAILOC_UPSTREAM_EVENT" = stop ]; then exit 0; fi; cat ` + result + `; cat ` + stderr + ` >&2`
	if err := os.WriteFile(result, []byte(`{"version":1,"url":"http://127.0.0.1:3128","headers":{"Proxy-Authorization":"Bearer secret-session-token"}}`), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(stderr, nil, 0600); err != nil {
		t.Fatal(err)
	}
	pJSON, _ := json.Marshal(map[string]any{"upstream_proxy": UpstreamProxy{Workspace: "test", Command: command}})
	p, err := ParsePolicy(pJSON, "test")
	if err != nil {
		t.Fatal(err)
	}
	b, err := New(Config{Subnet: netip.MustParsePrefix("192.168.127.0/24"), Gateway: netip.MustParseAddr("192.168.127.1"), GuestIP: netip.MustParseAddr("192.168.127.2"), UpstreamState: filepath.Join(dir, "status.json"), UpstreamCA: filepath.Join(dir, "ca.pem")}, p)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(b.stack.Close)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	if err := b.startUpstream(ctx); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(b.stopUpstream)
	old := b.upstream.Load()
	if err := os.WriteFile(result, []byte(`{"version":99,"url":"http://proxy"}`), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(stderr, []byte("sign in again: secret-session-token"), 0600); err != nil {
		t.Fatal(err)
	}
	event := b.reloadUpstream(ctx, p)
	if event["upstream_error"] == nil || b.upstream.Load() != old {
		t.Fatalf("failed reload replaced route: %v", event)
	}
	encoded, _ := json.Marshal(event)
	if strings.Contains(string(encoded), "secret-session-token") || !strings.Contains(string(encoded), "sign in again") {
		t.Fatalf("unsafe stderr: %s", encoded)
	}
	s, err := ReadUpstreamStatus(b.cfg.UpstreamState)
	if err != nil {
		t.Fatal(err)
	}
	if s.Generation != 1 || s.State != "degraded" || strings.Contains(s.LastStderr, "secret-session-token") {
		t.Fatalf("status %+v", s)
	}
	for _, path := range []string{b.cfg.UpstreamState, b.cfg.UpstreamCA} {
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm() != 0600 {
			t.Fatal(info.Mode())
		}
	}
}

func TestUpstreamCAChangeAndStop(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	ca1, ca2 := newTestCA(t, "first"), newTestCA(t, "second")
	result := filepath.Join(dir, "result.json")
	stopped := filepath.Join(dir, "stopped")
	command := `if [ "$JAILOC_UPSTREAM_EVENT" = stop ]; then printf stopped > ` + stopped + `; exit 0; fi; cat ` + result
	writeResult := func(ca string) {
		t.Helper()
		data, _ := json.Marshal(map[string]any{"version": 1, "url": "http://127.0.0.1:3128", "ca": ca})
		if err := os.WriteFile(result, data, 0600); err != nil {
			t.Fatal(err)
		}
	}
	writeResult(ca1.certPath)
	u, _ := json.Marshal(UpstreamProxy{Workspace: "test", Command: command, CA: ca1.certPath})
	p, err := ParsePolicy([]byte(`{"upstream_proxy":`+string(u)+`}`), "CA")
	if err != nil {
		t.Fatal(err)
	}
	status := &syncBuf{}
	b, err := New(Config{Subnet: netip.MustParsePrefix("192.168.127.0/24"), Gateway: netip.MustParseAddr("192.168.127.1"), GuestIP: netip.MustParseAddr("192.168.127.2"), UpstreamState: filepath.Join(dir, "status.json"), UpstreamCA: filepath.Join(dir, "public.pem"), Status: status}, p)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(b.stack.Close)
	if err := b.startUpstream(t.Context()); err != nil {
		t.Fatal(err)
	}
	old := b.upstream.Load()
	writeResult(ca2.certPath)
	event := b.reloadUpstream(t.Context(), p)
	if event["upstream_error"] != nil {
		t.Fatal(event)
	}
	if status.String() != "upstream ca changed\n" {
		t.Fatalf("CA lifecycle %q", status.String())
	}
	ca, err := os.ReadFile(b.cfg.UpstreamCA)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Equal(ca, old.ca) || bytes.Count(ca, []byte("BEGIN CERTIFICATE")) != 2 {
		t.Fatal("public merged CA not rotated")
	}
	if bytes.Count(old.ca, []byte("BEGIN CERTIFICATE")) != 1 {
		t.Fatal("mutated route snapshot")
	}
	b.stopUpstream()
	if data, err := os.ReadFile(stopped); err != nil || string(data) != "stopped" { //nolint:gosec // G304: test temp file
		t.Fatalf("stop not run %q %v", data, err)
	}
}

func TestUpstreamStartFailure(t *testing.T) {
	t.Parallel()
	for _, tt := range []struct {
		name     string
		upstream UpstreamProxy
		want     string
	}{
		{"helper", UpstreamProxy{Workspace: "test", Command: "printf 'sign in using helper login' >&2; exit 1"}, "sign in using helper login"},
		{"missing auth resolver", UpstreamProxy{Workspace: "test", URL: "http://127.0.0.1:3128", Auth: &UpstreamAuth{Username: "user", FromEnv: "PASS"}}, "resolver"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			u, _ := json.Marshal(tt.upstream)
			p, err := ParsePolicy([]byte(`{"upstream_proxy":`+string(u)+`}`), "start")
			if err != nil {
				t.Fatal(err)
			}
			status := &syncBuf{}
			b, err := New(Config{Subnet: netip.MustParsePrefix("192.168.127.0/24"), Gateway: netip.MustParseAddr("192.168.127.1"), GuestIP: netip.MustParseAddr("192.168.127.2"), Status: status}, p)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(b.stack.Close)
			l, err := net.Listen("tcp", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = l.Close() }()
			err = b.Run(t.Context(), l)
			if err == nil || !strings.Contains(err.Error(), tt.want) || strings.Contains(status.String(), "ready") {
				t.Fatalf("startup %v status %q", err, status.String())
			}
		})
	}
}

func TestUpstreamRecovery(t *testing.T) {
	t.Parallel()
	dir, err := os.MkdirTemp("", "up-recovery")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	p := newTestConnectProxy(t, "unix", filepath.Join(dir, "proxy.sock"), "HTTP/1.1 200 OK\r\n\r\nrecovered", "")
	counter := filepath.Join(dir, "calls")
	command := `if [ "$JAILOC_UPSTREAM_EVENT" = stop ]; then exit 0; fi; n=0; if [ -f ` + counter + ` ]; then n=$(cat ` + counter + `); fi; n=$((n+1)); printf '%s' "$n" > ` + counter + `; if [ "$n" -eq 1 ]; then printf '{"version":1,"url":"unix:///tmp/nonexistent-upstream.sock"}'; elif [ "$n" -eq 2 ]; then printf 'retry login' >&2; exit 1; else printf '{"version":1,"url":"` + p.url + `"}'; fi`
	u, _ := json.Marshal(UpstreamProxy{Workspace: "test", Command: command})
	policy, err := ParsePolicy([]byte(`{"upstream_proxy":`+string(u)+`}`), "recovery")
	if err != nil {
		t.Fatal(err)
	}
	b, err := New(Config{Subnet: netip.MustParsePrefix("192.168.127.0/24"), Gateway: netip.MustParseAddr("192.168.127.1"), GuestIP: netip.MustParseAddr("192.168.127.2"), UpstreamState: filepath.Join(dir, "status.json")}, policy)
	if err != nil {
		t.Fatal(err)
	}
	b.upstreamState.backoffMin = 10 * time.Millisecond
	g := connectTestBroker(t, b)
	c, err := upstreamGuestDial(t, g, l7IP, 443)
	if err == nil {
		_ = c.SetReadDeadline(time.Now().Add(time.Second))
		_, err = c.Read(make([]byte, 1))
		_ = c.Close()
	}
	if err == nil {
		t.Fatal("dead helper endpoint succeeded")
	}
	deadline := time.Now().Add(3 * time.Second)
	for b.upstream.Load().generation < 2 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if b.upstream.Load().generation != 2 {
		t.Fatal("helper recovery never published")
	}
	c, err = upstreamGuestDial(t, g, l7IP, 443)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = c.Close() }()
	_ = c.SetReadDeadline(time.Now().Add(time.Second))
	data := make([]byte, 9)
	if _, err := io.ReadFull(c, data); err != nil || string(data) != "recovered" {
		t.Fatalf("recovery tunnel %q %v", data, err)
	}
	if data, err := os.ReadFile(counter); err != nil || string(data) != "3" { //nolint:gosec // G304: test temp file
		t.Fatalf("single-flight retries %q %v", data, err)
	}
}

func TestUpstreamPublicErrorsRedactHeaders(t *testing.T) {
	t.Parallel()
	p := newTestConnectProxy(t, "tcp", "127.0.0.1:0", "HTTP/1.1 403 secret-session-token\r\n\r\n", "")
	ep, err := parseUpstreamEndpoint(p.url)
	if err != nil {
		t.Fatal(err)
	}
	b := &Broker{upstreamState: &upstreamManager{}, log: json.NewEncoder(io.Discard)}
	b.upstreamState.b = b
	r := &upstreamRoute{endpoint: ep, workspace: "test", headers: http.Header{"Proxy-Authorization": []string{"Bearer secret-session-token"}}}
	_, err = b.dialEgress(t.Context(), r, 1, netip.MustParseAddrPort(l7IP+":443"), "example.com")
	if err == nil || strings.Contains(err.Error(), "secret-session-token") {
		t.Fatalf("proxy reflected secret: %v", err)
	}
	// Verify a reflected token crossing the 64-byte status truncation boundary is redacted BEFORE truncation.
	boundaryStatus := "HTTP/1.1 403 " + strings.Repeat("a", 45) + "Bearer secret-session-token\r\n\r\n"
	pBoundary := newTestConnectProxy(t, "tcp", "127.0.0.1:0", boundaryStatus, "")
	epBoundary, err := parseUpstreamEndpoint(pBoundary.url)
	if err != nil {
		t.Fatal(err)
	}
	rBoundary := &upstreamRoute{endpoint: epBoundary, workspace: "test", headers: http.Header{"Proxy-Authorization": []string{"Bearer secret-session-token"}}}
	_, err = b.dialEgress(t.Context(), rBoundary, 2, netip.MustParseAddrPort(l7IP+":443"), "example.com")
	if err == nil || strings.Contains(err.Error(), "Bearer") || strings.Contains(err.Error(), "secret") {
		t.Fatalf("proxy reflected secret prefix across 64-byte boundary: %v", err)
	}
}

func TestUpstreamHelperPipeHoldingChild(t *testing.T) {
	t.Parallel()
	marker := filepath.Join(t.TempDir(), "leaked")
	_, err := runUpstreamHelper(t.Context(), `(sleep 0.5; printf leaked > `+marker+`) & exit 0`, "test", "start", time.Second)
	if err == nil {
		t.Fatal("pipe-holding child not reported")
	}
	time.Sleep(600 * time.Millisecond)
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatal("helper process group survived bounded WaitDelay")
	}
}

func TestUpstreamAuthStderrRedaction(t *testing.T) {
	t.Parallel()
	got := redactUpstream("password", http.Header{"Proxy-Authorization": []string{"Basic dXNlcjpwYXNzd29yZA=="}})
	if strings.Contains(got, "password") {
		t.Fatal("decoded Basic password leaked")
	}
}
func TestUpstreamMalformedHelperStdOutWithholdsStdErr(t *testing.T) {
	t.Parallel()
	p, err := ParsePolicy([]byte(`{"upstream_proxy":{"workspace":"test","command":"printf '{\"version\":1,\"headers\":{\"Proxy-Authorization\":\"Bearer '; printf 'secret-in-stderr' >&2"}}`), "malformed")
	if err != nil {
		t.Fatal(err)
	}
	b, err := New(Config{Subnet: netip.MustParsePrefix("192.168.127.0/24"), Gateway: netip.MustParseAddr("192.168.127.1"), GuestIP: netip.MustParseAddr("192.168.127.2")}, p)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(b.stack.Close)
	err = b.startUpstream(t.Context())
	if err == nil || strings.Contains(err.Error(), "secret-in-stderr") {
		t.Fatalf("malformed helper stdout leaked stderr: %v", err)
	}
	// Trailing non-JSON output after a valid JSON object must also withhold stderr.
	uTrailing, _ := json.Marshal(UpstreamProxy{Workspace: "test", Command: `printf '{"version":1,"url":"http://127.0.0.1:3128"} trailing-secret'; printf 'stderr-secret-token' >&2`})
	pTrailing, err := ParsePolicy([]byte(`{"upstream_proxy":`+string(uTrailing)+`}`), "trailing")
	if err != nil {
		t.Fatal(err)
	}
	bTrailing, err := New(Config{Subnet: netip.MustParsePrefix("192.168.127.0/24"), Gateway: netip.MustParseAddr("192.168.127.1"), GuestIP: netip.MustParseAddr("192.168.127.2")}, pTrailing)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(bTrailing.stack.Close)
	err = bTrailing.startUpstream(t.Context())
	if err == nil || strings.Contains(err.Error(), "stderr-secret-token") {
		t.Fatalf("trailing helper stdout leaked stderr: %v", err)
	}
	// Oversized stdout (even with a valid JSON prefix) must withhold stderr.
	dir := t.TempDir()
	uOutOverflow, _ := json.Marshal(UpstreamProxy{
		Workspace: "test",
		Command: `printf '{"version":1,"url":"http://127.0.0.1:3128"
}'; yes ' ' | dd bs=1024 count=70 2>/dev/null; printf 'overflow-stdout-secret-marker' >&2`,
	})
	pOutOverflow, err := ParsePolicy([]byte(`{"upstream_proxy":`+string(uOutOverflow)+`}`), "stdout-overflow")
	if err != nil {
		t.Fatal(err)
	}
	logBuf := &syncBuf{}
	bOutOverflow, err := New(Config{
		Subnet:        netip.MustParsePrefix("192.168.127.0/24"),
		Gateway:       netip.MustParseAddr("192.168.127.1"),
		GuestIP:       netip.MustParseAddr("192.168.127.2"),
		UpstreamState: filepath.Join(dir, "out-status.json"),
		Log:           logBuf,
	}, pOutOverflow)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(bOutOverflow.stack.Close)
	err = bOutOverflow.startUpstream(t.Context())
	if err == nil || strings.Contains(err.Error(), "overflow-stdout-secret") {
		t.Fatalf("overflowed helper stdout leaked stderr: %v", err)
	}
	if data, rErr := os.ReadFile(bOutOverflow.cfg.UpstreamState); rErr != nil || strings.Contains(string(data), "overflow-stdout-secret") {
		t.Fatalf("overflowed helper stdout leaked stderr into status: %q %v", data, rErr)
	}
	// Oversized stderr (including stop event) must withhold stderr so a credential split at 16 KiB cannot leak its prefix.
	uErrOverflow, _ := json.Marshal(UpstreamProxy{
		Workspace: "test",
		Command: `if [ "$JAILOC_UPSTREAM_EVENT" = stop ]; then printf '%016370dpartial-stop-secret-token' 0 >&2; exit 1; fi; ` +
			`if [ -f ` + filepath.Join(dir, "fail-err") + ` ]; then printf '%016370dpartial-reload-secret-token' 0 >&2; exit 1; fi; ` +
			`printf '{"version":1,"url":"http://127.0.0.1:3128","headers":{"Proxy-Authorization":"Bearer partial-reload-secret-token"}}'`,
	})
	pErrOverflow, err := ParsePolicy([]byte(`{"upstream_proxy":`+string(uErrOverflow)+`}`), "stderr-overflow")
	if err != nil {
		t.Fatal(err)
	}
	bErrOverflow, err := New(Config{
		Subnet:        netip.MustParsePrefix("192.168.127.0/24"),
		Gateway:       netip.MustParseAddr("192.168.127.1"),
		GuestIP:       netip.MustParseAddr("192.168.127.2"),
		UpstreamState: filepath.Join(dir, "err-status.json"),
		Log:           logBuf,
	}, pErrOverflow)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(bErrOverflow.stack.Close)
	if err := bErrOverflow.startUpstream(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "fail-err"), []byte("1"), 0600); err != nil {
		t.Fatal(err)
	}
	ev := bErrOverflow.reloadUpstream(t.Context(), pErrOverflow)
	bErrOverflow.stopUpstream()
	evJSON, _ := json.Marshal(ev)
	statusData, _ := os.ReadFile(bErrOverflow.cfg.UpstreamState)
	combined := string(evJSON) + "\n" + string(statusData) + "\n" + logBuf.String()
	if ev["upstream_stderr"] != "[stderr omitted: helper stderr exceeded 16 KiB]" {
		t.Fatalf("reload event missing exact stderr omission notice: %v", ev)
	}
	if !strings.Contains(logBuf.String(), `"upstream_stderr":"[stderr omitted: helper stderr exceeded 16 KiB]"`) {
		t.Fatalf("stop error log missing exact stderr omission notice: %s", logBuf.String())
	}
	if strings.Contains(combined, "partial-reload") || strings.Contains(combined, "partial-stop-") {
		t.Fatalf("overflowed helper stderr leaked partial marker: %s", combined)
	}
}

func TestUpstreamReloadAtomicSnapshot(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	stopStarted := filepath.Join(dir, "stop-started")
	stopRelease := filepath.Join(dir, "stop-release")
	command := `if [ "$JAILOC_UPSTREAM_EVENT" = stop ]; then printf 1 > ` + stopStarted + `; while [ ! -f ` + stopRelease + ` ]; do sleep 0.01; done; exit 0; fi; printf '{"version":1,"url":"http://127.0.0.1:3128"}'`
	u, _ := json.Marshal(UpstreamProxy{Workspace: "test", Command: command})
	p1, err := ParsePolicy([]byte(`{"allow":[{"cidrs":["93.184.216.34/32"]}],"upstream_proxy":`+string(u)+`}`), "p1")
	if err != nil {
		t.Fatal(err)
	}
	p2, err := ParsePolicy([]byte(`{"allow":[]}`), "p2")
	if err != nil {
		t.Fatal(err)
	}
	b, err := New(Config{Subnet: netip.MustParsePrefix("192.168.127.0/24"), Gateway: netip.MustParseAddr("192.168.127.1"), GuestIP: netip.MustParseAddr("192.168.127.2"), UpstreamState: filepath.Join(dir, "status.json"), UpstreamCA: filepath.Join(dir, "ca.pem")}, p1)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(b.stack.Close)
	if err := b.startUpstream(t.Context()); err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		b.reloadUpstream(t.Context(), p2)
	}()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(stopStarted); err == nil {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	// While stopHelper is still running during disable-and-revoke reload,
	// currentSnapshot must never pair old allow rules (p1) with a nil upstream route.
	pol, route := b.currentSnapshot()
	if pol == p1 && route == nil {
		t.Fatal("snapshot paired old allow policy with nil upstream route")
	}
	if pol != p2 || route != nil {
		t.Fatalf("expected atomic transition to (p2, nil) before stopHelper completes, got pol=%v route=%v", pol == p2, route)
	}
	if err := os.WriteFile(stopRelease, []byte("ok"), 0600); err != nil {
		t.Fatal(err)
	}
	<-done

	// Now test enable-and-allow transition: while helper start is running,
	// currentSnapshot must keep (p2, nil) and then atomically flip to (p3, route).
	startStarted := filepath.Join(dir, "start-started")
	startRelease := filepath.Join(dir, "start-release")
	cmdEnable := `if [ "$JAILOC_UPSTREAM_EVENT" = stop ]; then exit 0; fi; printf 1 > ` + startStarted + `; while [ ! -f ` + startRelease + ` ]; do sleep 0.01; done; printf '{"version":1,"url":"http://127.0.0.1:3128"
}'`
	uEnable, _ := json.Marshal(UpstreamProxy{Workspace: "test", Command: cmdEnable})
	p3, err := ParsePolicy([]byte(`{"allow":[{"cidrs":["93.184.216.34/32"]}],"upstream_proxy":`+string(uEnable)+`}`), "p3")
	if err != nil {
		t.Fatal(err)
	}
	doneEnable := make(chan struct{})
	go func() {
		defer close(doneEnable)
		b.reloadUpstream(t.Context(), p3)
	}()
	deadline = time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(startStarted); err == nil {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	pol, route = b.currentSnapshot()
	if pol != p2 || route != nil {
		t.Fatalf("expected snapshot to stay (p2, nil) while enable helper is running, got pol=%v route=%v", pol == p2, route)
	}
	if err := os.WriteFile(startRelease, []byte("ok"), 0600); err != nil {
		t.Fatal(err)
	}
	<-doneEnable
	pol, route = b.currentSnapshot()
	if pol != p3 || route == nil || route.url != "http://127.0.0.1:3128" {
		t.Fatalf("expected atomic transition to (p3, route), got pol=%v route=%+v", pol == p3, route)
	}

	// Verify recovery serialization with invalid-policy SetPolicy(DenyAll):
	// recovery can update the route without undoing a completed deny-all reload.
	recStarted := filepath.Join(dir, "rec-started")
	recRelease := filepath.Join(dir, "rec-release")
	recCount := filepath.Join(dir, "rec-count")
	cmdRecover := `if [ "$JAILOC_UPSTREAM_EVENT" = stop ]; then exit 0; fi; n=0; if [ -f ` + recCount + ` ]; then n=$(cat ` + recCount + `); fi; n=$((n+1)); printf '%s' "$n" > ` + recCount + `; if [ "$n" -eq 1 ]; then printf '{"version":1,"url":"unix:///tmp/nonexistent-rec.sock"
}'; else printf 1 > ` + recStarted + `; while [ ! -f ` + recRelease + ` ]; do sleep 0.01; done; printf '{"version":1,"url":"http://127.0.0.1:3128"}'; fi`
	uRec, _ := json.Marshal(UpstreamProxy{Workspace: "test", Command: cmdRecover})
	pAllow, err := ParsePolicy([]byte(`{"allow":[{"cidrs":["93.184.216.34/32"]}],"upstream_proxy":`+string(uRec)+`}`), "pAllow")
	if err != nil {
		t.Fatal(err)
	}
	bRec, err := New(Config{
		Subnet:        netip.MustParsePrefix("192.168.127.0/24"),
		Gateway:       netip.MustParseAddr("192.168.127.1"),
		GuestIP:       netip.MustParseAddr("192.168.127.2"),
		UpstreamState: filepath.Join(dir, "rec-status.json"),
		UpstreamCA:    filepath.Join(dir, "rec-ca.pem"),
	}, pAllow)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(bRec.stack.Close)
	bRec.upstreamState.backoffMin = 10 * time.Millisecond
	if err := bRec.startUpstream(t.Context()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(bRec.stopUpstream)
	bRec.upstreamFailed(bRec.upstream.Load())
	deadline = time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(recStarted); err == nil {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	denyDone := make(chan struct{})
	denyPol := DenyAll("invalid reload")
	go func() {
		defer close(denyDone)
		bRec.SetPolicy(denyPol)
	}()
	if err := os.WriteFile(recRelease, []byte("ok"), 0600); err != nil {
		t.Fatal(err)
	}
	<-denyDone
	deadline = time.Now().Add(2 * time.Second)
	for bRec.upstream.Load().generation < 2 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	pol, route = bRec.currentSnapshot()
	if pol != denyPol || !pol.denyAll {
		t.Fatalf("recovery overwrote DenyAll policy: got denyAll=%v source=%q", pol.denyAll, pol.source)
	}
	if route == nil || route.generation != 2 || route.url != "http://127.0.0.1:3128" {
		t.Fatalf("recovery did not publish updated route alongside DenyAll: %+v", route)
	}
}

func TestUpstreamPolicyReloadErrorPreservesHelperStateAndRetries(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	recCount := filepath.Join(dir, "count")
	cmd := `if [ "$JAILOC_UPSTREAM_EVENT" = stop ]; then exit 0; fi; n=0; if [ -f ` + recCount + ` ]; then n=$(cat ` + recCount + `); fi; n=$((n+1)); printf '%s' "$n" > ` + recCount + `; if [ "$n" -eq 1 ]; then printf '{"version":1,"url":"unix:///tmp/nonexistent-policy-err.sock"}'; elif [ "$n" -eq 2 ]; then printf 'helper-retry-err' >&2; exit 1; else printf '{"version":1,"url":"http://127.0.0.1:3128"}'; fi`
	u, _ := json.Marshal(UpstreamProxy{Workspace: "test", Command: cmd})
	pAllow, err := ParsePolicy([]byte(`{"allow":[{"cidrs":["93.184.216.34/32"]}],"upstream_proxy":`+string(u)+`}`), "pAllow")
	if err != nil {
		t.Fatal(err)
	}
	b, err := New(Config{
		Subnet:        netip.MustParsePrefix("192.168.127.0/24"),
		Gateway:       netip.MustParseAddr("192.168.127.1"),
		GuestIP:       netip.MustParseAddr("192.168.127.2"),
		UpstreamState: filepath.Join(dir, "status.json"),
		UpstreamCA:    filepath.Join(dir, "ca.pem"),
	}, pAllow)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(b.stack.Close)
	b.upstreamState.backoffMin = 30 * time.Millisecond
	if err := b.startUpstream(t.Context()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(b.stopUpstream)
	b.upstreamFailed(b.upstream.Load())

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		b.upstreamState.mu.Lock()
		lastStderr := b.upstreamState.status.LastStderr
		b.upstreamState.mu.Unlock()
		if lastStderr == "helper-retry-err" {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}

	denyPol := DenyAll("invalid reload: syntax error")
	ev := b.reloadUpstream(t.Context(), denyPol)
	if ev["upstream_generation"] != uint64(1) || !strings.Contains(fmt.Sprint(ev["upstream_error"]), "exit status 1") || ev["upstream_stderr"] != "helper-retry-err" {
		t.Fatalf("invalid-policy reload event missing preserved upstream diagnostics: %v", ev)
	}
	// Recovery continues under the retained helper config and reaches generation 2 while keeping DenyAll.
	deadline = time.Now().Add(2 * time.Second)
	for b.upstream.Load().generation < 2 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	pol, route := b.currentSnapshot()
	if pol != denyPol || !pol.denyAll {
		t.Fatalf("expected DenyAll policy after invalid reload, got %+v", pol)
	}
	if route == nil || route.generation != 2 || route.url != "http://127.0.0.1:3128" {
		t.Fatalf("expected recovery to publish generation 2 route, got %+v", route)
	}
	if d := Decide(pol, Env{}, ProtoTCP, netip.MustParseAddrPort("93.184.216.34:443")); d.Allow {
		t.Fatalf("expected new flow to be denied under invalid reload, got %+v", d)
	}
}
