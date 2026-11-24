package vm

import (
	"bufio"
	"context"
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"testing/iotest"
	"time"
)

func TestConsoleWriterRotatesAndPermissions(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "microvm", "console.log")
	w := newConsoleWriter(path, nil)
	w.max = 20

	// Each line is 6 bytes ("lineX\n"). With max=20, 3 lines fit (18 bytes);
	// the 4th triggers a rotation.
	for _, s := range []string{
		"line1\n", "line2\n", "line3\n", // -> becomes .2 after second rotation
		"line4\n", "line5\n", "line6\n", // -> becomes .1 after second rotation
		"line7\n", "line8\n", // -> stays in console.log
	} {
		if _, err := w.Write([]byte(s)); err != nil {
			t.Fatalf("Write(%q): %v", s, err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	wantFiles := map[string]string{
		path + ".2": "line1\nline2\nline3\n",
		path + ".1": "line4\nline5\nline6\n",
		path:        "line7\nline8\n",
	}
	for p, want := range wantFiles {
		info, err := os.Stat(p)
		if err != nil {
			t.Fatalf("Stat(%s): %v", p, err)
		}
		if perm := info.Mode().Perm(); perm != 0o600 {
			t.Errorf("%s mode = %v, want 0600", p, perm)
		}
		got, err := os.ReadFile(p) //nolint:gosec // G304: test temp dir
		if err != nil {
			t.Fatalf("ReadFile(%s): %v", p, err)
		}
		if string(got) != want {
			t.Errorf("%s = %q, want %q", p, got, want)
		}
	}
	if _, err := os.Stat(path + ".3"); !os.IsNotExist(err) {
		t.Errorf("unexpected rotation .3: %v", err)
	}
}

func TestConsoleWriterDropsOversizedLines(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "console.log")
	w := newConsoleWriter(path, nil)
	w.maxLine = 32

	// Chunked write of an oversized line, surrounded by normal lines, plus a
	// boundary line of exactly maxLine bytes.
	exact := strings.Repeat("a", 32)
	huge := strings.Repeat("B", 20)
	chunks := []string{
		"boot start\n",
		huge, huge, "tail\n", // 44 bytes > 32: dropped
		exact + "\n", // 32 bytes == maxLine: kept
		"oom killer ran\n",
		huge, huge, // trailing oversized partial line on Close: dropped
	}
	for _, c := range chunks {
		if _, err := w.Write([]byte(c)); err != nil {
			t.Fatalf("Write: %v", err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	got, err := os.ReadFile(path) //nolint:gosec // G304: test temp dir
	if err != nil {
		t.Fatal(err)
	}
	want := "boot start\n" + exact + "\noom killer ran\n"
	if string(got) != want {
		t.Fatalf("console.log = %q, want %q", got, want)
	}
}

func TestConsoleWriterRedactsSecretsAndPatterns(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "console.log")
	red := newConsoleRedactor([]string{"workspace-secret-val"})
	w := newConsoleWriter(path, red)

	// Split a secret across two Write calls to ensure line buffering keeps
	// redaction intact across chunk boundaries.
	_, _ = w.Write([]byte("echo workspace-sec"))
	_, _ = w.Write([]byte("ret-val and " + "ghp_" + "1234567890abcdefghij1234567890abcdef\n"))
	red.Add([]string{"reloaded-secret-token"})
	_, _ = w.Write([]byte("old=workspace-secret-val new=reloaded-secret-token\n"))
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}

	got, err := os.ReadFile(path) //nolint:gosec // G304: test temp dir
	if err != nil {
		t.Fatal(err)
	}
	want := "echo [redacted] and [redacted]\nold=[redacted] new=[redacted]\n"
	if string(got) != want {
		t.Fatalf("console.log = %q, want %q", got, want)
	}
}

func TestStartConsolePipeAndSecretServer(t *testing.T) {
	t.Parallel()
	stateDir, err := os.MkdirTemp("/tmp", "jlc-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(stateDir) })

	red := newConsoleRedactor(nil)
	stopSock, err := startConsoleSecretServer(stateDir, red)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(stopSock)

	if err := SendConsoleSecrets(context.Background(), stateDir, []string{"delivered-secret-value"}); err != nil {
		t.Fatalf("SendConsoleSecrets: %v", err)
	}

	consoleLog := ConsoleLogPath(stateDir)
	pipePath, wait, err := startConsolePipe(consoleLog, red)
	if err != nil {
		t.Fatalf("startConsolePipe: %v", err)
	}
	defer wait()

	// libkrun opens the pipe some time after the runner process starts; the
	// reader must still be there when it does.
	time.Sleep(100 * time.Millisecond)
	runnerOut, err := os.OpenFile(pipePath, os.O_WRONLY|syscall.O_NONBLOCK, 0) //nolint:gosec // G304: test pipe
	if err != nil {
		t.Fatalf("open pipe as runner: %v", err)
	}

	if _, err := runnerOut.WriteString("booting with delivered-secret-value\n"); err != nil {
		t.Fatalf("write to pipe: %v", err)
	}
	if err := runnerOut.Close(); err != nil {
		t.Fatalf("close runner pipe: %v", err)
	}
	wait()

	got, err := os.ReadFile(consoleLog) //nolint:gosec // G304: test file
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "booting with [redacted]\n" {
		t.Fatalf("console.log = %q, want %q", got, "booting with [redacted]\n")
	}
	if _, err := os.Stat(pipePath); !os.IsNotExist(err) {
		t.Errorf("pipe %s still exists after wait: %v", pipePath, err)
	}
}

func TestSendConsoleSecretsRequiresAck(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		reply   string
		wantErr bool
	}{
		{name: "ok", reply: "ok\n"},
		{name: "error reply", reply: "decode secrets: unexpected EOF\n", wantErr: true},
		{name: "closed without reply", reply: "", wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			stateDir, err := os.MkdirTemp("/tmp", "jlc-")
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = os.RemoveAll(stateDir) })
			ln, err := net.Listen("unix", consoleSocket(stateDir))
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = ln.Close() })
			go func() {
				c, err := ln.Accept()
				if err != nil {
					return
				}
				defer func() { _ = c.Close() }()
				_, _ = bufio.NewReader(c).ReadString('\n')
				_, _ = io.WriteString(c, tt.reply)
			}()

			err = SendConsoleSecrets(context.Background(), stateDir, []string{"delivered-secret-value"})
			if (err != nil) != tt.wantErr {
				t.Fatalf("SendConsoleSecrets() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestSendConsoleSecretsWithoutSocket(t *testing.T) {
	t.Parallel()
	if err := SendConsoleSecrets(context.Background(), t.TempDir(), []string{"delivered-secret-value"}); err != nil {
		t.Fatalf("SendConsoleSecrets() without socket = %v, want nil", err)
	}
}

type failingWriter struct{ writes int }

func (w *failingWriter) Write([]byte) (int, error) {
	w.writes++
	return 0, errors.New("no space left on device")
}

func TestDrainConsoleKeepsReadingAfterWriteFailure(t *testing.T) {
	t.Parallel()
	src := strings.NewReader("first\nsecond\nthird\n")
	dst := &failingWriter{}
	drainConsole(dst, iotest.OneByteReader(src))
	if src.Len() != 0 {
		t.Errorf("drainConsole left %d bytes unread", src.Len())
	}
	if dst.writes != 1 {
		t.Errorf("drainConsole wrote %d times after the sink failed, want 1", dst.writes)
	}
}

type fakeProcess struct {
	stopErr error
	alive   bool
}

func (p fakeProcess) Stop(context.Context) error { return p.stopErr }
func (p fakeProcess) IsAlive() bool              { return p.alive }
func (p fakeProcess) PID() int                   { return 1 }

func TestConsoleProcessHandleStopDoesNotHang(t *testing.T) {
	t.Parallel()
	block := make(chan struct{})
	t.Cleanup(func() { close(block) })
	tests := []struct {
		name    string
		proc    fakeProcess
		wantErr bool
	}{
		{name: "failed stop with live runner", proc: fakeProcess{stopErr: errors.New("timed out"), alive: true}, wantErr: true},
		{name: "drain bounded by context", proc: fakeProcess{}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			h := consoleProcessHandle{ProcessHandle: tt.proc, wait: func() { <-block }}
			ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
			defer cancel()
			returned := make(chan error, 1)
			go func() { returned <- h.Stop(ctx) }()
			select {
			case err := <-returned:
				if (err != nil) != tt.wantErr {
					t.Fatalf("Stop() = %v, wantErr %v", err, tt.wantErr)
				}
			case <-time.After(5 * time.Second):
				t.Fatal("Stop() hung waiting for the console pipe")
			}
		})
	}
}
