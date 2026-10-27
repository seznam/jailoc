package cmd

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
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
		`{"event":"future-event","time":"2026-10-27T10:00:06Z"}`,
		`{"event":"ingress","host":"/state/ssh.sock","guest_port":22,"time":"2026-10-27T10:00:07Z"}`,
		`{"event":"ingress","host":"127.0.0.1:18080","guest_port":8080,"time":"2026-10-27T10:00:08Z"}`,
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
		"IN tcp 127.0.0.1:18080 → guest port 8080",
	}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("got:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}

func TestExecFormatter(t *testing.T) {
	t.Parallel()
	f := execFormatter{}
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
			if err := tailLog(context.Background(), &out, []string{old, cur, filepath.Join(dir, "missing")}, tt.view, render); err != nil {
				t.Fatalf("tailLog: %v", err)
			}
			if out.String() != tt.want {
				t.Fatalf("out = %q, want %q", out.String(), tt.want)
			}
		})
	}
}
