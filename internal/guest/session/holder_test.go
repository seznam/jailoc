//go:build darwin || linux

package session

import (
	"bytes"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

const holderEnv = "JAILOC_SESSION_TEST_HOLDER"

func TestMain(m *testing.M) {
	if dir := os.Getenv(holderEnv); dir != "" {
		if err := Serve(dir, os.Args[len(os.Args)-1]); err != nil {
			os.Exit(2)
		}
		os.Exit(0)
	}
	os.Exit(m.Run())
}

// script ignores read failures: dash aborts a read interrupted by the
// trapped SIGWINCH.
const script = `printf '\033[?1049h\033[?1000h'
trap 'echo winch' WINCH
while :; do
  read -r l || continue
  [ "$l" = quit ] && exit 7
  echo "got:$l"
done`

type testClient struct {
	t    *testing.T
	conn net.Conn
	buf  bytes.Buffer
}

func dial(t *testing.T, dir, id string, rows, cols uint16) *testClient {
	t.Helper()
	conn, err := net.Dial("unix", socketPath(dir, id))
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	if err := WriteFrame(conn, SizeFrame(FrameHello, rows, cols)); err != nil {
		t.Fatalf("hello: %v", err)
	}
	return &testClient{t: t, conn: conn}
}

// waitFor reads frames until the accumulated output contains want or a
// frame of type until arrives; it returns that frame's type.
func (c *testClient) waitFor(want string, until byte) Frame {
	c.t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		if want != "" && strings.Contains(c.buf.String(), want) {
			return Frame{Type: FrameData}
		}
		_ = c.conn.SetReadDeadline(deadline)
		f, err := ReadFrame(c.conn)
		if err != nil {
			c.t.Fatalf("waiting for %q (output %q): %v", want, c.buf.String(), err)
		}
		if f.Type == FrameData {
			c.buf.Write(f.Payload)
			continue
		}
		if f.Type == until {
			return f
		}
	}
}

func (c *testClient) send(s string) {
	c.t.Helper()
	if err := WriteFrame(c.conn, Frame{Type: FrameData, Payload: []byte(s)}); err != nil {
		c.t.Fatalf("send: %v", err)
	}
}

func TestSessionLifecycle(t *testing.T) {
	dir, err := os.MkdirTemp("/tmp", "jls-") // short path: unix socket paths are limited
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	t.Setenv(holderEnv, dir)
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}

	id, err := Create(dir, Options{
		Name: "sh", Dir: t.TempDir(), Argv: []string{"/bin/sh", "-c", script},
		Env: []string{"PATH=/usr/bin:/bin", "TERM=xterm-256color"}, Rows: 24, Cols: 80,
	}, []string{exe})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	a := dial(t, dir, id, 24, 80)
	a.waitFor("\x1b[?1000h", 0)
	a.waitFor("winch", 0) // same size: the nudge makes the program redraw
	a.send("hello\n")
	a.waitFor("got:hello", 0)

	infos, err := List(dir)
	if err != nil || len(infos) != 1 || infos[0].ID != id || !infos[0].Attached {
		t.Fatalf("List = %+v, %v", infos, err)
	}
	if found, err := Find(dir, infos[0].Dir); err != nil || found == nil || found.ID != id {
		t.Fatalf("Find = %+v, %v", found, err)
	}

	// A second client takes over and gets the modes replayed.
	b := dial(t, dir, id, 30, 100)
	if f := a.waitFor("", FrameKicked); f.Type != FrameKicked {
		t.Fatalf("first client got %c, want kicked", f.Type)
	}
	b.waitFor("\x1b[?1049h\x1b[?1000h", 0)
	b.waitFor("winch", 0)
	b.send("quit\n")
	f := b.waitFor("", FrameExit)
	if code, err := ParseExit(f.Payload); err != nil || code != 7 {
		t.Fatalf("exit = %d, %v; want 7", code, err)
	}

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(filepath.Join(dir, id+".json")); os.IsNotExist(err) {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if infos, err := List(dir); err != nil || len(infos) != 0 {
		t.Fatalf("List after exit = %+v, %v", infos, err)
	}
}

func TestStopSession(t *testing.T) {
	dir, err := os.MkdirTemp("/tmp", "jls-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	t.Setenv(holderEnv, dir)
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	id, err := Create(dir, Options{Name: "sleep", Dir: "/", Argv: []string{"/bin/sleep", "60"}, Env: []string{"PATH=/bin"}, Rows: 24, Cols: 80}, []string{exe})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	c := dial(t, dir, id, 24, 80)
	if err := Stop(dir, id); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	f := c.waitFor("", FrameExit)
	if code, _ := ParseExit(f.Payload); code == 0 {
		t.Fatalf("stopped session exited with 0")
	}
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if infos, _ := List(dir); len(infos) == 0 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if err := Stop(dir, id); err == nil {
		t.Fatal("stopping an ended session succeeded")
	}
}

func TestOpenSharesOneSession(t *testing.T) {
	dir, err := os.MkdirTemp("/tmp", "jls-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	t.Setenv(holderEnv, dir)
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	opts := Options{Name: "sleep", Dir: "/", Argv: []string{"/bin/sleep", "60"}, Env: []string{"PATH=/bin"}, Rows: 24, Cols: 80}
	ids := make([]string, 8)
	errs := make([]error, len(ids))
	created := make([]bool, len(ids))
	var wg sync.WaitGroup
	for i := range ids {
		wg.Go(func() { ids[i], created[i], errs[i] = Open(dir, opts, []string{exe}) })
	}
	wg.Wait()
	t.Cleanup(func() {
		infos, _ := List(dir)
		for _, info := range infos {
			_ = Stop(dir, info.ID)
		}
	})
	for i := range ids {
		if errs[i] != nil {
			t.Fatalf("Open: %v", errs[i])
		}
		if ids[i] != ids[0] {
			t.Fatalf("concurrent Open started sessions %v", ids)
		}
	}
	n := 0
	for _, c := range created {
		if c {
			n++
		}
	}
	if n != 1 {
		t.Fatalf("concurrent Open reported %d created sessions, want 1", n)
	}
}
