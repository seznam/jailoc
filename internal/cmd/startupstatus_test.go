//go:build darwin || linux

package cmd

import (
	"bytes"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/seznam/jailoc/internal/guest/session"
)

// testStatus returns a started startupStatus showing "S" in an 80x24
// terminal, with a clock the test controls.
func testStatus(out *bytes.Buffer, now *time.Time, size *[2]int) *startupStatus {
	s := newStartupStatus(out, func() (int, int) { return size[0], size[1] })
	s.now = func() time.Time { return *now }
	s.start("S")
	return s
}

func TestStartupStatusWrite(t *testing.T) {
	t.Parallel()

	const (
		alt   = "\x1b[?1049h"
		draw  = "\x1b7" + resetAttrs + "\x1b[12;1H\x1b[2K\x1b[12;38H\x1b[2m⠋ S 0s\x1b8"
		erase = "\x1b7" + resetAttrs + "\x1b[12;1H\x1b[2K\x1b8"
	)
	tests := []struct {
		name   string
		chunks []string
		want   string
	}{
		{name: "inline agent", chunks: []string{"\x1b[?25l", "hello", "\x1b[?1049h"}, want: "\x1b[?25lhello\x1b[?1049h"},
		{name: "first frame after clear", chunks: []string{alt, "\x1b[2J\x1b[H", "hi"}, want: alt + draw + "\x1b[2J\x1b[H" + draw + erase + "hi"},
		{name: "frame in the switching chunk", chunks: []string{alt + "\x1b[2Jhi", "x"}, want: alt + "\x1b[2Jhix"},
		{name: "erase before first visible byte", chunks: []string{alt, "\x1b[H\x1b[1mhi"}, want: alt + draw + "\x1b[H\x1b[1m" + erase + "hi"},
		{name: "switch split across chunks", chunks: []string{"\x1b[?10", "49h", "x"}, want: "\x1b[?10" + "49h" + draw + erase + "x"},
		{name: "combined private modes", chunks: []string{"\x1b[?2004;1049h", "x"}, want: "\x1b[?2004;1049h" + draw + erase + "x"},
		{name: "mode 1047", chunks: []string{"\x1b[?1047h", "x"}, want: "\x1b[?1047h" + draw + erase + "x"},
		{name: "strings are not visible", chunks: []string{alt, "\x1b_Gi=1;a=q\x1b\\\x1b]0;title\x07\x1bP+q544e\x1b\\", "x"}, want: alt + draw + "\x1b_Gi=1;a=q\x1b\\\x1b]0;title\x07\x1bP+q544e\x1b\\" + draw + erase + "x"},
		{name: "string split across chunks", chunks: []string{alt, "\x1b]0;ti", "tle\x1b", "\\", "x"}, want: alt + draw + "\x1b]0;ti" + "tle\x1b" + "\\" + draw + erase + "x"},
		{name: "csi split across chunks", chunks: []string{alt, "\x1b[38;2;", "1;2;3m", "x"}, want: alt + draw + "\x1b[38;2;" + "1;2;3m" + draw + erase + "x"},
		{name: "escape at the end of a chunk", chunks: []string{alt, "\x1b", "[Hx"}, want: alt + draw + "\x1b[H" + erase + "x"},
		{name: "spaces are not visible", chunks: []string{alt, "\x1b[48;2;1;2;3m\x1b[1;1H    ", "x"}, want: alt + draw + "\x1b[48;2;1;2;3m\x1b[1;1H    " + draw + erase + "x"},
		{name: "charset designation", chunks: []string{alt, "\x1b(B", "x"}, want: alt + draw + "\x1b(B" + draw + erase + "x"},
		{name: "controls are not visible", chunks: []string{alt, "\r\n\t", "x"}, want: alt + draw + "\r\n\t" + draw + erase + "x"},
		{name: "non-private mode", chunks: []string{"\x1b[1049h", "x"}, want: "\x1b[1049hx"},
		{name: "utf-8 is visible", chunks: []string{alt, "─"}, want: alt + draw + erase + "─"},
		{name: "leaving the alternate screen", chunks: []string{alt, "\x1b[?1049l", "x", alt}, want: alt + draw + "\x1b[?1049lx" + alt},
		{name: "reset before switching", chunks: []string{"\x1b[?1049l", alt, "x"}, want: "\x1b[?1049l" + alt + draw + erase + "x"},
		{name: "passthrough after first frame", chunks: []string{alt, "x", "\x1b[2J", "y"}, want: alt + draw + erase + "x\x1b[2Jy"},
		{name: "BEL inside a DCS string", chunks: []string{alt, "\x1bP+q\x07x\x1b\\", "y"}, want: alt + draw + "\x1bP+q\x07x\x1b\\" + draw + erase + "y"},
		{name: "OSC ended by BEL", chunks: []string{alt, "\x1b]11;?\x07", "x"}, want: alt + draw + "\x1b]11;?\x07" + draw + erase + "x"},
		{name: "DEC cursor save", chunks: []string{alt, "\x1b7\x1b[5;5H", "x"}, want: alt + draw + "\x1b7" + resetAttrs + "\x1b[12;1H\x1b[2K\x1b8" + "\x1b[5;5Hx"},
		{name: "SCO cursor save", chunks: []string{alt, "\x1b[s\x1b[5;5H", "x"}, want: alt + draw + "\x1b[s" + "\x1b[12;1H\x1b[2K\x1b[u" + "\x1b[5;5Hx"},
		{name: "cursor save split across chunks", chunks: []string{alt, "\x1b", "7x"}, want: alt + draw + "\x1b" + "7" + resetAttrs + "\x1b[12;1H\x1b[2K\x1b8" + "x"},
		{name: "cursor save before drawing", chunks: []string{alt + "\x1b7", "x"}, want: alt + "\x1b7x"},
		{name: "cursor save on the main screen", chunks: []string{"\x1b[s" + alt, "x"}, want: "\x1b[s" + alt + draw + erase + "x"},
		{name: "CSI s with parameters", chunks: []string{alt, "\x1b[1;80s", "x"}, want: alt + draw + "\x1b[1;80s" + draw + erase + "x"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			var out bytes.Buffer
			now, size := time.Unix(0, 0), [2]int{80, 24}
			s := testStatus(&out, &now, &size)
			for _, c := range tt.chunks {
				if err := s.write([]byte(c)); err != nil {
					t.Fatalf("write(%q): %v", c, err)
				}
			}
			if got := out.String(); got != tt.want {
				t.Errorf("output = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestStartupStatusPassthroughBeforeStart(t *testing.T) {
	t.Parallel()

	var out bytes.Buffer
	s := newStartupStatus(&out, func() (int, int) { return 80, 24 })
	for _, c := range []string{"\x1b[?1049h", "\x1b[2J"} {
		if err := s.write([]byte(c)); err != nil {
			t.Fatalf("write(%q): %v", c, err)
		}
	}
	if s.tick() {
		t.Error("tick() = true before start, want false")
	}
	if got, want := out.String(), "\x1b[?1049h\x1b[2J"; got != want {
		t.Errorf("output = %q, want %q", got, want)
	}
}

func TestStartupStatusTick(t *testing.T) {
	t.Parallel()

	var out bytes.Buffer
	now, size := time.Unix(0, 0), [2]int{80, 24}
	s := testStatus(&out, &now, &size)

	steps := []struct {
		name  string
		do    func()
		want  string
		shown bool
	}{
		{name: "main screen", do: func() {}, want: "", shown: true},
		{name: "inside an escape sequence", do: func() { _ = s.write([]byte("\x1b[?1049h\x1b[")); out.Reset() }, want: "", shown: true},
		{name: "spinner and elapsed time advance", do: func() { _ = s.write([]byte("H")); out.Reset(); now = now.Add(1200 * time.Millisecond) }, want: "\x1b7" + resetAttrs + "\x1b[12;1H\x1b[2K\x1b[12;38H\x1b[2m⠹ S 1s\x1b8", shown: true},
		{name: "resize erases the old row", do: func() { size = [2]int{40, 10} }, want: "\x1b7" + resetAttrs + "\x1b[12;1H\x1b[2K\x1b[5;1H\x1b[2K\x1b[5;18H\x1b[2m⠹ S 1s\x1b8", shown: true},
		{name: "after the agent draws", do: func() { _ = s.write([]byte("x")); out.Reset() }, want: "", shown: false},
	}
	for _, st := range steps {
		st.do()
		if shown := s.tick(); shown != st.shown {
			t.Errorf("%s: tick() = %v, want %v", st.name, shown, st.shown)
		}
		if got := out.String(); got != st.want {
			t.Errorf("%s: output = %q, want %q", st.name, got, st.want)
		}
		out.Reset()
	}
}

func TestStartupStatusStop(t *testing.T) {
	t.Parallel()

	var out bytes.Buffer
	now, size := time.Unix(0, 0), [2]int{80, 24}
	s := testStatus(&out, &now, &size)
	if err := s.write([]byte("\x1b[?1049h")); err != nil {
		t.Fatal(err)
	}
	out.Reset()
	s.stop()
	if s.tick() {
		t.Error("tick() = true after stop, want false")
	}
	if out.Len() != 0 {
		t.Errorf("output after stop = %q, want none", out.String())
	}
}

func TestStartupLine(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		cols int
		want string
	}{
		{name: "fits", cols: 80, want: "⠋ Starting OpenCode… 12s"},
		{name: "too wide", cols: 16, want: "⠋ Starting… 12s"},
		{name: "no room for the message", cols: 8, want: "⠋  12s"},
		{name: "no room for the seconds", cols: 6, want: "⠋"},
		{name: "no room at all", cols: 1, want: ""},
		{name: "unknown width", cols: 0, want: "⠋ Starting OpenCode… 12s"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			if got := startupLine('⠋', "Starting OpenCode…", 12, tt.cols); got != tt.want {
				t.Errorf("startupLine() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestStartupMessage(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		install []string
		want    string
	}{
		{"nothing to install", nil, "Starting OpenCode 1 in workspace w…"},
		{"one plugin", []string{"oh-my-openagent@5.1.48"}, "Installing oh-my-openagent@5.1.48 for OpenCode 1…"},
		{"several plugins", []string{"a@1", "b@latest"}, "Installing 2 plugins for OpenCode 1: a@1, b@latest…"},
		{"control characters", []string{"a\x1b[2J"}, `Installing a\x1b[2J for OpenCode 1…`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := startupMessage("Starting OpenCode 1 in workspace w…", "OpenCode 1", session.Started{Install: tt.install})
			if got != tt.want {
				t.Errorf("startupMessage() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestStartupStatusStateTrackingAfterPending(t *testing.T) {
	t.Parallel()

	var out bytes.Buffer
	now, size := time.Unix(0, 0), [2]int{80, 24}
	s := testStatus(&out, &now, &size)
	if s.altScreen() || s.visible() {
		t.Fatalf("initial state: alt=%v, visible=%v", s.altScreen(), s.visible())
	}
	// Agent enters alternate screen and draws visible text -> pending stops,
	// but altScreen and visible stay tracked.
	if err := s.write([]byte("\x1b[?1049hhello")); err != nil {
		t.Fatal(err)
	}
	if !s.altScreen() || !s.visible() {
		t.Fatalf("after drawing on alt screen: alt=%v, visible=%v; want true, true", s.altScreen(), s.visible())
	}
	// Agent leaves alternate screen on crash -> altScreen becomes false even
	// though pending was already false.
	if err := s.write([]byte("\x1b[?1049lUnexpected server error.\n")); err != nil {
		t.Fatal(err)
	}
	if s.altScreen() {
		t.Fatal("altScreen() = true after \\x1b[?1049l, want false")
	}
	// Full terminal reset ESC c also clears alternate-screen state.
	if err := s.write([]byte("\x1b[?1049h\x1bc")); err != nil {
		t.Fatal(err)
	}
	if s.altScreen() {
		t.Fatal("altScreen() = true after ESC c, want false")
	}
}

func TestResetSessionTerminalModes(t *testing.T) {
	t.Parallel()

	var onAlt, onMain bytes.Buffer
	resetSessionTerminalModes(&onAlt, true)
	resetSessionTerminalModes(&onMain, false)
	if !bytes.Contains(onAlt.Bytes(), []byte("\x1b[?1049l")) {
		t.Errorf("resetSessionTerminalModes(alt=true) = %q, want \\x1b[?1049l", onAlt.String())
	}
	if bytes.Contains(onMain.Bytes(), []byte("\x1b[?1049l")) {
		t.Errorf("resetSessionTerminalModes(alt=false) = %q, must not contain \\x1b[?1049l", onMain.String())
	}
}

// termSink takes the bytes of a notification writer into out, the buffer
// agent output goes to, so their order shows; take bounds the bytes one
// write takes, a negative take takes all.
type termSink struct {
	out  *bytes.Buffer
	take int
	err  error
}

func (k *termSink) write(p []byte) (int, error) {
	if k.err != nil {
		return 0, k.err
	}
	n := len(p)
	if k.take >= 0 {
		n = min(n, k.take)
	}
	k.out.Write(p[:n])
	return n, nil
}

func TestStartupStatusTryWriteBetween(t *testing.T) {
	t.Parallel()

	errWrite := errors.New("write failed")
	full := fmt.Errorf("%w: the terminal is not reading", errNotifyDropped)
	tests := []struct {
		name        string
		before      []string
		after       []string
		take        int
		writeErr    error
		refocused   bool
		want        string
		wantErr     error
		wantDropped bool
	}{
		{name: "between chunks", before: []string{"a\x1b[1m"}, after: []string{"b"}, take: -1, want: "a\x1b[1m<N>b"},
		{name: "inside a CSI sequence is dropped, not kept", before: []string{"\x1b[3"}, after: []string{"1", "m", "x"}, take: -1, want: "\x1b[31mx", wantDropped: true},
		{name: "inside a string is dropped, not kept", before: []string{"\x1b]0;ti"}, after: []string{"tle\x1b", "\\"}, take: -1, want: "\x1b]0;title\x1b\\", wantDropped: true},
		{name: "focused again", before: []string{"a"}, after: []string{"b"}, take: -1, refocused: true, want: "ab", wantDropped: true},
		{name: "terminal not reading", before: []string{"a"}, after: []string{"b"}, writeErr: full, want: "ab", wantDropped: true},
		{name: "part taken is aborted", before: []string{"a"}, after: []string{"b"}, take: 2, want: "a<N" + notifyAbort + "b", wantDropped: true},
		{name: "write error", before: []string{"a"}, after: []string{"b"}, writeErr: errWrite, want: "ab", wantErr: errWrite},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			var out bytes.Buffer
			s := newStartupStatus(&out, func() (int, int) { return 80, 24 })
			for _, c := range tt.before {
				if err := s.write([]byte(c)); err != nil {
					t.Fatalf("write(%q): %v", c, err)
				}
			}
			sink := &termSink{out: &out, take: tt.take, err: tt.writeErr}
			err := s.tryWriteBetween([]byte("<N>"), sink.write, func() bool { return !tt.refocused })
			switch {
			case tt.wantDropped && !errors.Is(err, errNotifyDropped):
				t.Fatalf("tryWriteBetween() = %v, want it dropped", err)
			case tt.wantErr != nil && (!errors.Is(err, tt.wantErr) || errors.Is(err, errNotifyDropped)):
				t.Fatalf("tryWriteBetween() = %v, want %v", err, tt.wantErr)
			case !tt.wantDropped && tt.wantErr == nil && err != nil:
				t.Fatalf("tryWriteBetween() = %v, want nil", err)
			}
			for _, c := range tt.after {
				if err := s.write([]byte(c)); err != nil {
					t.Fatalf("write(%q): %v", c, err)
				}
			}
			if got := out.String(); got != tt.want {
				t.Errorf("output = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestStartupStatusTryWriteBetweenAfterCut(t *testing.T) {
	t.Parallel()

	var out bytes.Buffer
	s := newStartupStatus(&out, func() (int, int) { return 80, 24 })
	cut := &termSink{out: &out, take: 1}
	if err := s.tryWriteBetween([]byte("<N>"), cut.write, func() bool { return true }); !errors.Is(err, errNotifyDropped) {
		t.Fatalf("cut tryWriteBetween() = %v, want it dropped", err)
	}
	all := &termSink{out: &out, take: -1}
	if err := s.tryWriteBetween([]byte("<M>"), all.write, func() bool { return true }); !errors.Is(err, errNotifyDropped) {
		t.Fatalf("tryWriteBetween() after a cut = %v, want it dropped", err)
	}
	s.settle()
	s.settle()
	if got, want := out.String(), "<"+notifyAbort; got != want {
		t.Fatalf("output = %q, want %q", got, want)
	}
}

// blockingWriter blocks every write until release is closed, like a
// terminal that stopped reading.
type blockingWriter struct {
	entered chan struct{}
	release chan struct{}
}

func (w blockingWriter) Write(p []byte) (int, error) {
	w.entered <- struct{}{}
	<-w.release
	return len(p), nil
}

func TestStartupStatusTryWriteBetweenDoesNotWaitForBlockedOutput(t *testing.T) {
	t.Parallel()

	w := blockingWriter{entered: make(chan struct{}), release: make(chan struct{})}
	s := newStartupStatus(w, func() (int, int) { return 80, 24 })
	wrote := make(chan error)
	go func() { wrote <- s.write([]byte("agent output")) }()
	<-w.entered

	called := false
	sink := func(p []byte) (int, error) { called = true; return len(p), nil }
	start := time.Now()
	err := s.tryWriteBetween([]byte("<N>"), sink, func() bool { return true })
	took := time.Since(start)
	close(w.release)
	if werr := <-wrote; werr != nil {
		t.Fatalf("write: %v", werr)
	}
	if !errors.Is(err, errNotifyDropped) || called {
		t.Fatalf("tryWriteBetween() = %v, wrote %v; want it dropped without writing", err, called)
	}
	if took > time.Second {
		t.Fatalf("tryWriteBetween() took %v while agent output was blocked, want no wait", took)
	}
}

func TestStartupStatusWriteBetween(t *testing.T) {
	t.Parallel()

	var out bytes.Buffer
	s := newStartupStatus(&out, func() (int, int) { return 80, 24 })
	if err := s.writeBetween([]byte("<E>")); err != nil {
		t.Fatalf("writeBetween() = %v", err)
	}
	if err := s.write([]byte("\x1b[3")); err != nil {
		t.Fatal(err)
	}
	if err := s.writeBetween([]byte("<F>")); err == nil {
		t.Fatal("writeBetween() inside an escape sequence = nil, want an error")
	}
	if got, want := out.String(), "<E>\x1b[3"; got != want {
		t.Fatalf("output = %q, want %q", got, want)
	}
}

// failOnWriter fails the write of exactly the bytes in fail and counts the
// writes it was given.
type failOnWriter struct {
	out    bytes.Buffer
	fail   string
	writes int
}

func (w *failOnWriter) Write(p []byte) (int, error) {
	w.writes++
	if string(p) == w.fail {
		return 0, errors.New("write failed")
	}
	return w.out.Write(p)
}

func TestStartupStatusTickAbortsCutNotificationOnce(t *testing.T) {
	t.Parallel()

	const (
		alt  = "\x1b[?1049h"
		draw = "\x1b7" + resetAttrs + "\x1b[12;1H\x1b[2K\x1b[12;38H\x1b[2m⠋ S 0s\x1b8"
	)
	var out bytes.Buffer
	now, size := time.Unix(0, 0), [2]int{80, 24}
	s := testStatus(&out, &now, &size)
	if err := s.write([]byte(alt)); err != nil {
		t.Fatal(err)
	}
	out.Reset()
	part := &termSink{out: &out, take: 1}
	if err := s.tryWriteBetween([]byte("<N>"), part.write, func() bool { return true }); !errors.Is(err, errNotifyDropped) {
		t.Fatalf("tryWriteBetween() = %v, want it dropped", err)
	}
	for i := range 2 {
		if !s.tick() {
			t.Fatalf("tick() #%d = false, want the status still shown", i+1)
		}
	}
	if got, want := out.String(), "<"+notifyAbort+draw+draw; got != want {
		t.Fatalf("output = %q, want the abort once before the first frame: %q", got, want)
	}
}

func TestStartupStatusTickEndsAnimationWhenAbortFails(t *testing.T) {
	t.Parallel()

	w := &failOnWriter{fail: notifyAbort}
	now, size := time.Unix(0, 0), [2]int{80, 24}
	s := newStartupStatus(w, func() (int, int) { return size[0], size[1] })
	s.now = func() time.Time { return now }
	s.start("S")
	if err := s.write([]byte("\x1b[?1049h")); err != nil {
		t.Fatal(err)
	}
	part := &termSink{out: &w.out, take: 1}
	if err := s.tryWriteBetween([]byte("<N>"), part.write, func() bool { return true }); !errors.Is(err, errNotifyDropped) {
		t.Fatalf("tryWriteBetween() = %v, want it dropped", err)
	}
	before := w.out.String()
	if s.tick() {
		t.Fatal("tick() = true after the abort failed, want the animation ended")
	}
	writes := w.writes
	if s.tick() || w.writes != writes || w.out.String() != before {
		t.Fatalf("tick() after the failure wrote %d more times, output %q; want none", w.writes-writes, w.out.String())
	}
}

func TestStartupStatusSettleCancelsUnfinishedSequence(t *testing.T) {
	t.Parallel()

	for _, tt := range []struct {
		name string
		in   string
	}{
		{name: "lone escape", in: "a\x1b"},
		{name: "string", in: "a\x1b]777;jail"},
		{name: "CSI", in: "a\x1b[3"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			var out bytes.Buffer
			s := newStartupStatus(&out, func() (int, int) { return 80, 24 })
			if err := s.write([]byte(tt.in)); err != nil {
				t.Fatal(err)
			}
			s.settle()
			s.settle()
			if got, want := out.String(), tt.in+notifyAbort; got != want {
				t.Fatalf("output = %q, want the abort once: %q", got, want)
			}
			if err := s.writeBetween([]byte("<R>")); err != nil {
				t.Fatalf("writeBetween() after settle = %v, want the escape state cleared", err)
			}
		})
	}
}

func TestStartupStatusSettleAbortFailureIsNotRetried(t *testing.T) {
	t.Parallel()

	w := &failOnWriter{fail: notifyAbort}
	s := newStartupStatus(w, func() (int, int) { return 80, 24 })
	if err := s.write([]byte("\x1b]777;jail")); err != nil {
		t.Fatal(err)
	}
	s.settle()
	writes := w.writes
	s.settle()
	if w.writes != writes {
		t.Fatalf("settle() retried the failed abort: %d writes, want %d", w.writes, writes)
	}
}
