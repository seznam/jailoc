package cmd

// Temporary screenshot driver. Copy into internal/cmd/, run, then delete.
// It feeds sample events through the real `jailoc traffic` / `jailoc strace`
// formatters, replicates the `jailoc sessions` table and hand-writes a
// `jailoc shell` session, writing ANSI
// transcripts for render.mjs. Lines starting with "$ " are prompts.
// Agent command lines are shortened to the binary name (plus --continue):
// the real ones also carry unattended and plugin flags.

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"text/tabwriter"
	"time"

	"github.com/fatih/color"
	"github.com/seznam/jailoc/internal/guest"
)

// shotAt returns the clock time Europe/Prague (CET) on the sample day.
func shotAt(clock string) time.Time {
	t, err := time.Parse(time.DateTime, "2026-11-02 "+clock)
	if err != nil {
		panic(err)
	}
	return t.Add(-time.Hour).UTC() // CET = UTC+1; render with TZ=Europe/Prague
}

func TestZZShots(t *testing.T) {
	dir := os.Getenv("SHOTS_DIR")
	if dir == "" {
		t.Skip("SHOTS_DIR not set")
	}
	color.NoColor = false
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}

	// The story: OpenCode in workspace "shop" (~/shop) fixes a cart bug.
	traffic := []trafficEvent{
		{Time: shotAt("14:31:08"), Event: "dns", Name: "api.anthropic.com.", Type: "TypeA", Answers: []string{"160.79.104.10"}},
		{Time: shotAt("14:31:08"), Event: "flow", Proto: "tcp", Dst: "160.79.104.10:443", Allow: true, Reason: "public"},
		{Time: shotAt("14:31:08"), Event: "l7", Proto: "tls", SNI: "api.anthropic.com", ALPN: []string{"h2"}},
		{Time: shotAt("14:31:12"), Event: "dns", Name: "registry.npmjs.org.", Type: "TypeA", Answers: []string{"104.16.27.34"}},
		{Time: shotAt("14:31:12"), Event: "flow", Proto: "tcp", Dst: "104.16.27.34:443", Allow: true, Reason: "public"},
		{Time: shotAt("14:31:12"), Event: "l7", Proto: "tls", SNI: "registry.npmjs.org", ALPN: []string{"h2", "http/1.1"}},
		{Time: shotAt("14:31:16"), Event: "dns", Name: "github.com.", Type: "TypeA", Answers: []string{"140.82.121.4"}},
		{Time: shotAt("14:31:16"), Event: "flow", Proto: "tcp", Dst: "140.82.121.4:22", Allow: true, Reason: "public"},
		{Time: shotAt("14:31:21"), Event: "dns", Name: "gitlab.corp.example.", Type: "TypeA", Answers: []string{"10.20.4.17"}},
		{Time: shotAt("14:31:21"), Event: "flow", Proto: "tcp", Dst: "10.20.4.17:443", Allow: true, Reason: "exception 10.20.4.17/32"},
		{Time: shotAt("14:31:21"), Event: "l7", Proto: "tls", SNI: "gitlab.corp.example", ALPN: []string{"h2", "http/1.1"}},
		{Time: shotAt("14:31:27"), Event: "dns", Name: "deb.debian.org.", Type: "TypeA", Answers: []string{"151.101.2.132"}},
		{Time: shotAt("14:31:27"), Event: "flow", Proto: "tcp", Dst: "151.101.2.132:80", Allow: true, Reason: "public"},
		{Time: shotAt("14:31:27"), Event: "l7", Proto: "http", Method: "GET", Host: "deb.debian.org", Path: "/debian/dists/bookworm/InRelease"},
		{Time: shotAt("14:31:31"), Event: "dns", Name: "postgres.prod.corp.example.", Type: "TypeA", Blocked: true},
		{Time: shotAt("14:31:32"), Event: "flow", Proto: "tcp", Dst: "10.20.9.3:5432", Allow: false, Reason: "denied-range"},
		{Time: shotAt("14:31:34"), Event: "flow", Proto: "tcp", Dst: "169.254.169.254:80", Allow: false, Reason: "hard-deny"},
		{Time: shotAt("14:31:42"), Event: "ingress", Host: "127.0.0.1:5173", GuestPort: 5173},
	}
	tf := newTrafficFormatter(false)
	tb := strings.Builder{}
	tb.WriteString("$ jailoc traffic -f\n")
	for _, e := range traffic {
		raw, err := json.Marshal(e)
		if err != nil {
			t.Fatal(err)
		}
		if s := tf.line(raw); s != "" {
			tb.WriteString(s + "\n")
		}
	}
	writeShot(t, dir, "traffic.ansi", tb.String())

	const agentUID, root = 1000, 0
	start := func(clock string, pid, ppid, uid int, cwd string, argv ...string) guest.ExecRecord {
		return guest.ExecRecord{Time: shotAt(clock), PID: pid, PPID: ppid, UID: uid, Cwd: cwd, Argv: argv}
	}
	exit := func(clock string, pid, code int) guest.ExecRecord {
		return guest.ExecRecord{Time: shotAt(clock), Event: "exit", PID: pid, UID: -1, Code: code}
	}
	const shop, web = "/Users/jane/shop", "/Users/jane/shop/web"
	execs := []guest.ExecRecord{
		start("14:31:07", 812, 788, agentUID, shop, "opencode"),
		start("14:31:11", 851, 812, agentUID, web, "npm", "ci"),
		start("14:31:15", 866, 812, agentUID, shop, "git", "fetch", "origin"),
		start("14:31:15", 867, 866, agentUID, shop, "ssh", "git@github.com", "git-upload-pack", "acme/shop.git"),
		exit("14:31:17", 867, 0),
		exit("14:31:17", 866, 0),
		exit("14:31:19", 851, 0),
		start("14:31:20", 880, 812, agentUID, shop, "go", "test", "./internal/cart/..."),
		start("14:31:24", 902, 880, agentUID, "/tmp/go-build/b001", "/tmp/go-build/b001/cart.test", "-test.v=false"),
		exit("14:31:25", 902, 0),
		exit("14:31:25", 880, 0),
		start("14:31:26", 915, 812, root, shop, "sudo", "apt-get", "install", "-y", "postgresql-client"),
		start("14:31:26", 917, 915, root, shop, "apt-get", "install", "-y", "postgresql-client"),
		exit("14:31:30", 917, 0),
		exit("14:31:30", 915, 0),
		start("14:31:31", 940, 812, agentUID, shop, "psql", "-h", "postgres.prod.corp.example", "-c", "select 1"),
		exit("14:31:31", 940, 2),
		start("14:31:32", 943, 812, agentUID, shop, "psql", "-h", "10.20.9.3", "-c", "select 1"),
		exit("14:31:32", 943, 2),
		start("14:31:34", 946, 812, agentUID, shop, "curl", "-s", "http://169.254.169.254/latest/meta-data/"),
		exit("14:31:34", 946, 7),
		start("14:31:40", 951, 812, agentUID, web, "npm", "run", "dev", "--", "--port", "5173"),
		start("14:31:41", 958, 951, agentUID, web, "node", "node_modules/.bin/vite", "--port", "5173"),
		start("14:31:48", 970, 812, agentUID, shop, "git", "commit", "-am", "fix(cart): round totals once"),
		exit("14:31:48", 970, 0),
	}
	ef := newExecFormatter(false)
	eb := strings.Builder{}
	eb.WriteString("$ jailoc strace -f\n")
	for _, e := range execs {
		raw, err := json.Marshal(e)
		if err != nil {
			t.Fatal(err)
		}
		if s := ef.line(raw); s != "" {
			eb.WriteString(s + "\n")
		}
	}
	writeShot(t, dir, "strace.ansi", eb.String())

	// Mirrors listSessionsVM in vmsession_unix.go; keep in sync.
	type session struct {
		id, state, dir, cmd string
		created             time.Time
	}
	sb := strings.Builder{}
	sb.WriteString("$ jailoc sessions\n")
	tw := tabwriter.NewWriter(&sb, 0, 0, 2, ' ', 0)
	_, _ = fmt.Fprintln(tw, "ID\tSTATE\tSTARTED\tDIRECTORY\tCOMMAND")
	for _, s := range []session{
		{"c5803a", "detached", shop + "/docs", "omo", shotAt("09:12:40")},
		{"3f2a9c", "attached", shop, "opencode", shotAt("12:31:07")},
		{"b71d04", "detached", web, "copilot", shotAt("13:56:07")},
		{"9d41e7", "attached", shop, "claude", shotAt("14:02:51")},
		{"e0c58f", "detached", shop, "opencode --continue", shotAt("14:27:07")},
	} {
		_, _ = fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\n", s.id, s.state, s.created.Local().Format(time.DateTime), s.dir, s.cmd)
	}
	if err := tw.Flush(); err != nil {
		t.Fatal(err)
	}
	writeShot(t, dir, "sessions.ansi", sb.String())

	// Interactive shell: the prompt is Ubuntu's default coloured bash PS1,
	// the hostname is the workspace name and the directory the shared host path.
	ps1 := "\x1b[01;32magent@shop\x1b[00m:\x1b[01;34m" + shop + "\x1b[00m$ "
	shell := strings.Join([]string{
		"$ jailoc shell",
		ps1 + "id",
		"uid=1000(agent) gid=1000(agent) groups=1000(agent)",
		ps1 + "ls /run/secrets",
		"npmrc",
		ps1 + `curl -s -o /dev/null -w '%{http_code}\n' https://registry.npmjs.org/`,
		"200",
		ps1 + `getent hosts postgres.prod.corp.example || echo "exit $?"`,
		"exit 2",
		ps1 + "curl http://169.254.169.254/",
		"curl: (7) Failed to connect to 169.254.169.254 port 80 after 2 ms: Couldn't connect to server",
		ps1, // no trailing newline: the cursor stays in the shell
	}, "\n")
	writeShot(t, dir, "shell.ansi", shell)
}

func writeShot(t *testing.T, dir, name, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}
