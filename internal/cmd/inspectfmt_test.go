package cmd

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"
)

var ansi = regexp.MustCompile(`\x1b\[[0-9;]*m`)

func plain(s string) string { return strings.Join(strings.Fields(ansi.ReplaceAllString(s, "")), " ") }

func TestTrafficFormatter(t *testing.T) {
	t.Parallel()
	f := newTrafficFormatter(false)
	lines := []string{
		`{"event":"dhcp","type":"ack","time":"2026-10-27T10:00:00Z"}`,
		`{"event":"dns","network":"udp","name":"example.com.","type":"TypeA","answers":["104.20.23.154"],"time":"2026-10-27T10:00:01Z"}`,
		`{"event":"flow","conn":7,"proto":"tcp","src":"192.168.127.2:4000","dst":"104.20.23.154:443","allow":true,"reason":"public","time":"2026-10-27T10:00:02Z"}`,
		`{"event":"l7","conn":7,"proto":"tls","dst":"104.20.23.154:443","sni":"example.com","alpn":["h2"],"time":"2026-10-27T10:00:02Z"}`,
		`{"event":"flow","conn":8,"proto":"tcp","src":"192.168.127.2:4001","dst":"10.0.0.1:80","allow":false,"reason":"denied-range","time":"2026-10-27T10:00:03Z"}`,
		`{"event":"l7","conn":9,"proto":"http","dst":"93.184.215.14:80","method":"GET","host":"plain.example","path":"/index","time":"2026-10-27T10:00:04Z"}`,
		`{"event":"dns","network":"udp","name":"blocked.example.","blocked":true,"time":"2026-10-27T10:00:05Z"}`,
		`{"event":"dns","network":"udp","name":"pastebin.com.","blocked":true,"rule":"no-paste","time":"2026-10-27T10:00:05Z"}`,
		`{"event":"flow","conn":10,"proto":"tcp","src":"192.168.127.2:4002","dst":"93.184.216.34:443","name":"pastebin.com","allow":false,"reason":"rule no-paste","rule":"no-paste","time":"2026-10-27T10:00:05Z"}`,
		`{"event":"future-event","time":"2026-10-27T10:00:06Z"}`,
		`{"event":"ingress","host":"/state/ssh.sock","guest_port":22,"time":"2026-10-27T10:00:07Z"}`,
		`{"event":"ingress","host":"127.0.0.1:18080","guest_port":8080,"time":"2026-10-27T10:00:08Z"}`,
		`{"event":"service","guest_port":7022,"network":"unix","target":"/tmp/agent.sock","time":"2026-10-27T10:00:09Z"}`,
		`{"event":"service","guest_port":9222,"network":"tcp","target":"127.0.0.1:9222","time":"2026-10-27T10:00:10Z"}`,
		`{"event":"service-error","guest_port":7100,"network":"unix","target":"/tmp/figma.sock","error":"connection refused","time":"2026-10-27T10:00:11Z"}`,
		`{"event":"flow-limit","conn":12,"proto":"tcp","dst":"1.1.1.1:443","time":"2026-10-27T10:00:12Z"}`,
		`{"event":"flow-limit","proto":"tcp","guest_port":7100,"time":"2026-10-27T10:00:13Z"}`,
		`not json`,
	}
	var got []string
	for _, l := range lines {
		if out := f.line([]byte(l)); out != "" {
			got = append(got, plain(out)[9:]) // drop the local-time column
		}
	}
	want := []string{
		"DNS dns example.com TypeA → 104.20.23.154",
		"ALLOW tcp example.com:443 [104.20.23.154]",
		"└ tls example.com h2",
		"DENY tcp 10.0.0.1:80 denied-range",
		"└ http GET plain.example/index",
		"BLOCK dns blocked.example → NXDOMAIN (filtered_dns)",
		"BLOCK dns pastebin.com → NXDOMAIN (rule=no-paste)",
		"DENY tcp pastebin.com:443 [93.184.216.34] rule=no-paste",
		"IN tcp 127.0.0.1:18080 → guest port 8080",
		"HOST tcp gateway port 9222 → 127.0.0.1:9222",
		"FAIL gateway port 7100 → /tmp/figma.sock: connection refused",
		"LIMIT tcp 1.1.1.1:443 too many open connections",
		"LIMIT tcp gateway port 7100 too many open connections",
	}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("got:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}

func TestExecFormatter(t *testing.T) {
	t.Parallel()
	f := newExecFormatter(false)
	got := plain(f.line([]byte(`{"time":"2026-10-27T10:00:00Z","pid":42,"ppid":7,"uid":1000,"comm":"git","cwd":"/home/agent/proj","argv":["git","commit","-m","fix it"],"exe":"/usr/bin/git"}`)))
	if want := "42 ←7 agent ~/proj git commit -m 'fix it'"; !strings.HasSuffix(got, want) {
		t.Fatalf("line = %q, want suffix %q", got, want)
	}
	if out := f.line([]byte(`{"time":"2026-10-27T10:00:00Z","pid":43,"uid":0,"exe":"/.jailoc/jailoc-guest","argv":["/.jailoc/jailoc-guest","run"]}`)); out != "" {
		t.Fatalf("guest process shown without --all: %q", out)
	}
	if out := f.line([]byte(`{"time":"2026-10-27T10:00:00Z","pid":45,"uid":1000,"exe":"/usr/bin/bash","argv":["/bin/bash","-c","'/.jailoc/jailoc-guest' 'run' '--' 'pwd'"]}`)); out != "" {
		t.Fatalf("ssh wrapper shell shown without --all: %q", out)
	}
	if out := plain(f.line([]byte(`{"time":"2026-10-27T10:00:00Z","pid":44,"uid":0,"comm":"true"}`))); !strings.HasSuffix(out, "root [true, exited]") {
		t.Fatalf("exited process line = %q", out)
	}
}

func TestExecFormatterExits(t *testing.T) {
	t.Parallel()
	const (
		gitStart  = `{"time":"2026-10-27T10:00:00Z","pid":42,"ppid":7,"uid":1000,"argv":["git","push"]}`
		guestRun  = `{"time":"2026-10-27T10:00:00Z","pid":43,"uid":0,"exe":"/.jailoc/jailoc-guest","argv":["/.jailoc/jailoc-guest","run"]}`
		shortLive = `{"time":"2026-10-27T10:00:00Z","pid":44,"uid":0,"comm":"true"}`
	)
	tests := []struct {
		name    string
		all     bool
		records []string
		want    string // plain exit line, "" when hidden
	}{
		{name: "failure", records: []string{gitStart, `{"event":"exit","pid":42,"code":128}`}, want: "42 exit 128 git push"},
		{name: "signal", records: []string{gitStart, `{"event":"exit","pid":42,"signal":"SIGKILL"}`}, want: "42 SIGKILL git push"},
		{name: "success hidden", records: []string{gitStart, `{"event":"exit","pid":42}`}},
		{name: "success with all", all: true, records: []string{gitStart, `{"event":"exit","pid":42}`}, want: "42 exit 0 git push"},
		{name: "guest process", records: []string{guestRun, `{"event":"exit","pid":43,"code":1}`}},
		{name: "guest process with all", all: true, records: []string{guestRun, `{"event":"exit","pid":43,"code":1}`}, want: "43 exit 1 /.jailoc/jailoc-guest run"},
		{name: "exited before read", records: []string{shortLive, `{"event":"exit","pid":44,"code":1}`}, want: "44 exit 1 true"},
		{name: "start not in log", records: []string{`{"event":"exit","pid":45,"code":3}`}, want: "45 exit 3"},
		{name: "unknown event", records: []string{`{"event":"fork","pid":46}`}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			f := newExecFormatter(tt.all)
			var out string
			for _, r := range tt.records {
				out = f.line([]byte(r))
			}
			if got := plain(out); tt.want == "" && got != "" || tt.want != "" && !strings.HasSuffix(got, tt.want) {
				t.Fatalf("exit line = %q, want %q", got, tt.want)
			}
		})
	}
	f := newExecFormatter(false)
	f.line([]byte(gitStart))
	f.line([]byte(`{"event":"exit","pid":42,"code":1}`))
	if out := plain(f.line([]byte(`{"event":"exit","pid":42,"code":1}`))); !strings.HasSuffix(out, "42 exit 1") {
		t.Fatalf("second exit of a reused PID kept the old command: %q", out)
	}
}

func TestTrafficFormatterEscapesGuestFields(t *testing.T) {
	t.Parallel()
	attack := "\x1b]52;c;payload\a\r\n\u009b\u202e"
	want := `\x1b]52;c;payload\a\r\n\u009b\u202e`
	f := newTrafficFormatter(true)
	cases := []struct {
		name  string
		event trafficEvent
	}{
		{"flow", trafficEvent{Event: "flow", Proto: attack, Dst: attack, Reason: attack}},
		{"tls", trafficEvent{Event: "l7", Proto: "tls", SNI: attack, ALPN: []string{attack}}},
		{"http", trafficEvent{Event: "l7", Proto: "http", Method: attack, Host: attack, Path: attack}},
		{"dns answer", trafficEvent{Event: "dns", Name: attack, Type: attack, Answers: []string{attack}}},
		{"dns error", trafficEvent{Event: "dns", Name: attack, Type: attack, Error: attack}},
		{"dns blocked", trafficEvent{Event: "dns", Name: attack, Blocked: true}},
		{"dial error", trafficEvent{Event: "dial-error", Proto: attack, Dst: attack, Error: attack}},
		{"ingress", trafficEvent{Event: "ingress", Host: attack, GuestPort: 8080}},
		{"service", trafficEvent{Event: "service", Network: attack, Target: attack, GuestPort: 9222}},
		{"service error", trafficEvent{Event: "service-error", Target: attack, Error: attack}},
		{"reload", trafficEvent{Event: "reload", PolicyError: attack, RuntimeErrors: []string{attack}}},
		{"unknown event", trafficEvent{Event: attack}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			out := newTrafficFormatter(true).render(tc.event)
			if !strings.Contains(out, want) || strings.Contains(out, attack) {
				t.Fatalf("render = %q, want visible controls only", out)
			}
		})
	}
	dns, _ := json.Marshal(trafficEvent{Event: "dns", Name: attack + ".", Type: "A", Answers: []string{"1.2.3.4"}})
	f.line(dns)
	flow, _ := json.Marshal(trafficEvent{Event: "flow", Proto: "tcp", Dst: "1.2.3.4:443", Allow: true, Reason: "public"})
	if out := f.line(flow); !strings.Contains(out, want+":443") || strings.Contains(out, attack) {
		t.Fatalf("cached DNS name = %q", out)
	}
}

func TestExecFormatterEscapesGuestFields(t *testing.T) {
	t.Parallel()
	attack := "\x1b]52;c;payload\a\r\n\u009b\u202e"
	want := `\x1b]52;c;payload\a\r\n\u009b\u202e`
	for _, record := range []struct {
		name string
		argv []string
		comm string
	}{
		{name: "argv", argv: []string{"run", attack}},
		{name: "comm", comm: attack},
	} {
		t.Run(record.name, func(t *testing.T) {
			t.Parallel()
			raw, _ := json.Marshal(map[string]any{"pid": 42, "uid": 1000, "cwd": "/home/agent/" + attack, "argv": record.argv, "comm": record.comm})
			out := newExecFormatter(false).line(raw)
			if !strings.Contains(out, want) || strings.Contains(out, attack) {
				t.Fatalf("exec line = %q, want visible controls only", out)
			}
		})
	}
}

func TestTrafficFormatterEscapesNonprintingUnicode(t *testing.T) {
	t.Parallel()
	input := "café 日本語 👋 \x1b]52;c;data\a\r\n\t\x00\u009b\u202e" + string([]byte{0xff})
	want := `café 日本語 👋 \x1b]52;c;data\a\r\n\t\x00\u009b\u202e\xff`
	if got := newTrafficFormatter(false).render(trafficEvent{Event: "ingress", Host: input, GuestPort: 8080}); !strings.Contains(got, want) {
		t.Fatalf("render = %q, want visible field %q", got, want)
	}
}

func TestTrafficFormatterKeepsApplicationColor(t *testing.T) {
	t.Parallel()
	f := newTrafficFormatter(false)
	e := trafficEvent{Event: "l7", Proto: "http", Method: "GET", Host: "café.example", Path: "/\x1b]52;c;x\a"}
	out := f.render(e)
	want := colInfo.Sprint(`GET café.example/\x1b]52;c;x\a`)
	if !strings.Contains(out, want) {
		t.Fatalf("render = %q, want colored field %q", out, want)
	}
}

func TestTailLog(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	old, cur := filepath.Join(dir, "log.1"), filepath.Join(dir, "log")
	if err := os.WriteFile(old, []byte("a\nskip\nb\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(cur, []byte("c\nd\npartial"), 0o600); err != nil {
		t.Fatal(err)
	}
	render := func(raw []byte) string {
		if string(raw) == "skip" {
			return ""
		}
		return "<" + string(raw) + ">"
	}
	tests := []struct {
		name string
		view logView
		want string
	}{
		{name: "last lines", view: logView{lines: 3}, want: "<b>\n<c>\n<d>\n"},
		{name: "all", view: logView{lines: -1}, want: "<a>\n<b>\n<c>\n<d>\n"},
		{name: "none", view: logView{}, want: ""},
		{name: "json keeps raw lines", view: logView{lines: 2, json: true}, want: "c\nd\n"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			var out bytes.Buffer
			if err := tailLog(context.Background(), &out, []logFile{{path: old}, {path: cur}, {root: dir, path: "missing"}}, tt.view, render); err != nil {
				t.Fatalf("tailLog: %v", err)
			}
			if out.String() != tt.want {
				t.Fatalf("out = %q, want %q", out.String(), tt.want)
			}
		})
	}
}

// lockedBuffer is a bytes.Buffer safe for one writer and one reader.
type lockedBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *lockedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

func TestTailLogFollowsReplacedFile(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "log")
	if err := os.WriteFile(path, []byte("a\nb\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	out := &lockedBuffer{}
	done := make(chan error, 1)
	go func() {
		done <- tailLog(ctx, out, []logFile{{path: path}}, logView{lines: -1, follow: true}, func(raw []byte) string { return string(raw) })
	}()
	waitFor := func(want string) {
		t.Helper()
		deadline := time.Now().Add(5 * time.Second)
		for out.String() != want {
			if time.Now().After(deadline) {
				t.Fatalf("out = %q, want %q", out.String(), want)
			}
			time.Sleep(10 * time.Millisecond)
		}
	}
	waitFor("a\nb\n")
	// Replace the log with a new file of the same size, as rotation does.
	next := filepath.Join(dir, "log.new")
	if err := os.WriteFile(next, []byte("c\nd\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(next, path); err != nil {
		t.Fatal(err)
	}
	waitFor("a\nb\nc\nd\n")
	cancel()
	if err := <-done; err != nil {
		t.Fatalf("tailLog: %v", err)
	}
}

func TestTailLogReadsLinesWrittenBeforeRotation(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "log")
	if err := os.WriteFile(path, []byte("a\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	out := &lockedBuffer{}
	done := make(chan error, 1)
	go func() {
		done <- tailLog(ctx, out, []logFile{{path: path}}, logView{lines: -1, follow: true}, func(raw []byte) string { return string(raw) })
	}()
	waitFor := func(want string) {
		t.Helper()
		deadline := time.Now().Add(5 * time.Second)
		for out.String() != want {
			if time.Now().After(deadline) {
				t.Fatalf("out = %q, want %q", out.String(), want)
			}
			time.Sleep(10 * time.Millisecond)
		}
	}
	waitFor("a\n")
	// Append to the log and rotate it before the next poll.
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0) //nolint:gosec // G304: test file
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString("b\n"); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(path, path+".1"); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("c\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	waitFor("a\nb\nc\n")
	cancel()
	if err := <-done; err != nil {
		t.Fatalf("tailLog: %v", err)
	}
}

func TestEscapeLines(t *testing.T) {
	t.Parallel()
	got := escapeLines("first \x1b]52;c;aGk=\x07\nsecond\r")
	if want := `first \x1b]52;c;aGk=\a` + "\n" + `second\r`; got != want {
		t.Fatalf("escapeLines = %q, want %q", got, want)
	}
}
