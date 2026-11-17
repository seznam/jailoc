//go:build darwin || linux

package cmd

import (
	"errors"
	"slices"
	"strings"
	"syscall"
	"testing"
)

const (
	testNotifyCompleted = "\x1b]777;jailoc-notify;completed\x07"
	testNotifyInput     = "\x1b]777;jailoc-notify;input\x07"
)

// feedNotifyOSC feeds chunks through a new filter and returns the output and
// the notifications it recognized.
func feedNotifyOSC(chunks []string) (string, []notifyKind) {
	var f notifyOSCFilter
	var out strings.Builder
	var kinds []notifyKind
	for _, c := range chunks {
		out.Write(f.feed([]byte(c), func(k notifyKind) { kinds = append(kinds, k) }))
	}
	return out.String(), kinds
}

func TestNotifyOSCFilterConsumesPrivateSequences(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		chunks    []string
		wantOut   string
		wantKinds []notifyKind
	}{
		{name: "completed", chunks: []string{"a" + testNotifyCompleted + "b"}, wantOut: "ab", wantKinds: []notifyKind{notifyCompleted}},
		{name: "input", chunks: []string{testNotifyInput}, wantOut: "", wantKinds: []notifyKind{notifyInput}},
		{name: "both in one chunk", chunks: []string{testNotifyInput + "x" + testNotifyCompleted}, wantOut: "x", wantKinds: []notifyKind{notifyInput, notifyCompleted}},
		{name: "escape before sequence", chunks: []string{"\x1b" + testNotifyInput}, wantOut: "\x1b", wantKinds: []notifyKind{notifyInput}},
		{name: "byte by byte", chunks: strings.Split("<"+testNotifyCompleted+">", ""), wantOut: "<>", wantKinds: []notifyKind{notifyCompleted}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			out, kinds := feedNotifyOSC(tt.chunks)
			if out != tt.wantOut || !slices.Equal(kinds, tt.wantKinds) {
				t.Fatalf("feed() = %q, %v; want %q, %v", out, kinds, tt.wantOut, tt.wantKinds)
			}
		})
	}
}

func TestNotifyOSCFilterConsumesAtEverySplit(t *testing.T) {
	t.Parallel()

	stream := "a" + testNotifyCompleted + "b" + testNotifyInput + "c"
	for i := range len(stream) + 1 {
		for j := i; j <= len(stream); j++ {
			out, kinds := feedNotifyOSC([]string{stream[:i], stream[i:j], stream[j:]})
			if out != "abc" || !slices.Equal(kinds, []notifyKind{notifyCompleted, notifyInput}) {
				t.Fatalf("split at %d, %d: feed() = %q, %v", i, j, out, kinds)
			}
		}
	}
}

func TestNotifyOSCFilterPreservesOtherOutput(t *testing.T) {
	t.Parallel()

	for _, stream := range []string{
		"\x1b]777;notify;title;body\x07",
		"\x1b]777;jailoc-notify;other\x07",
		"\x1b]777;jailoc-notify;completed\x1b\\",
		"\x1b]777;jailoc-notify;completedx\x07",
		"\x1b]0;window title\x07\x1b[?1049h\x1b[2Jhello",
		"\x1b\x1b\x1b[A",
		"\x1b]52;c;aGVsbG8=\x07",
	} {
		t.Run(strings.ReplaceAll(stream, "\x1b", "ESC"), func(t *testing.T) {
			t.Parallel()
			// A trailing newline ends any held prefix.
			for i := range len(stream) + 1 {
				out, kinds := feedNotifyOSC([]string{stream[:i], stream[i:], "\n"})
				if out != stream+"\n" || len(kinds) != 0 {
					t.Fatalf("split at %d: feed() = %q, %v; want %q unchanged", i, out, kinds, stream+"\n")
				}
			}
		})
	}
}

func TestNotifyOSCFilterHoldsPrefixUntilDecided(t *testing.T) {
	t.Parallel()

	var f notifyOSCFilter
	if got := string(f.feed([]byte("x\x1b]777;jail"), nil)); got != "x" {
		t.Fatalf("feed(prefix) = %q, want %q", got, "x")
	}
	if got := string(f.feed([]byte("ly"), nil)); got != "\x1b]777;jailly" {
		t.Fatalf("feed(mismatch) = %q, want the held prefix back", got)
	}
}

func TestNotifyOSCFilterFinishReturnsHeldPrefix(t *testing.T) {
	t.Parallel()

	var f notifyOSCFilter
	var kinds []notifyKind
	emit := func(k notifyKind) { kinds = append(kinds, k) }
	if got := string(f.feed([]byte("x\x1b]777;jail"), emit)); got != "x" {
		t.Fatalf("feed(prefix) = %q, want %q", got, "x")
	}
	if got := string(f.finish()); got != "\x1b]777;jail" {
		t.Fatalf("finish() = %q, want the held prefix", got)
	}
	if got := f.finish(); len(got) != 0 {
		t.Fatalf("second finish() = %q, want nothing", got)
	}
	// The filter is reusable after finishing; the flushed prefix is not
	// completed by what follows.
	if got := string(f.feed([]byte("oc-notify;completed\x07ab"+testNotifyInput), emit)); got != "oc-notify;completed\x07ab" {
		t.Fatalf("feed() after finish = %q, want the text unchanged", got)
	}
	if !slices.Equal(kinds, []notifyKind{notifyInput}) {
		t.Fatalf("notifications after finish = %v, want only the whole sequence", kinds)
	}
}

func TestNotifyOSCFinishWithNothingHeld(t *testing.T) {
	t.Parallel()

	var f notifyOSCFilter
	if got := f.finish(); len(got) != 0 {
		t.Fatalf("finish() of a new filter = %q, want nothing", got)
	}
	f.feed([]byte("plain"), nil)
	if got := f.finish(); len(got) != 0 {
		t.Fatalf("finish() after plain output = %q, want nothing", got)
	}
}

func TestFocusOutputTracksGuestFocusReporting(t *testing.T) {
	t.Parallel()

	const reassert = "\x1b[?1004h"
	tests := []struct {
		name      string
		chunks    []string
		wantOut   string
		wantGuest bool
	}{
		{name: "enable", chunks: []string{"\x1b[?1004h"}, wantOut: "\x1b[?1004h", wantGuest: true},
		{name: "disable reasserts", chunks: []string{"\x1b[?1004h", "a\x1b[?1004lb"}, wantOut: "\x1b[?1004ha\x1b[?1004l" + reassert + "b"},
		{name: "disable in a mode list", chunks: []string{"\x1b[?1004h\x1b[?2004;1004l"}, wantOut: "\x1b[?1004h\x1b[?2004;1004l" + reassert},
		{name: "fragmented disable", chunks: []string{"\x1b[?1004h\x1b[?10", "04", "l."}, wantOut: "\x1b[?1004h\x1b[?1004l" + reassert + "."},
		{name: "full reset", chunks: []string{"\x1b[?1004h\x1bcz"}, wantOut: "\x1b[?1004h\x1bc" + reassert + "z"},
		{name: "soft reset", chunks: []string{"\x1b[?1004h\x1b[!p"}, wantOut: "\x1b[?1004h\x1b[!p" + reassert, wantGuest: true},
		{name: "save and restore", chunks: []string{"\x1b[?1004h\x1b[?1004s\x1b[?1004l\x1b[?1004r"}, wantOut: "\x1b[?1004h\x1b[?1004s\x1b[?1004l" + reassert + "\x1b[?1004r" + reassert, wantGuest: true},
		{name: "other modes", chunks: []string{"\x1b[?1004h\x1b[?2004l\x1b[1004l\x1b[?10045l"}, wantOut: "\x1b[?1004h\x1b[?2004l\x1b[1004l\x1b[?10045l", wantGuest: true},
		{name: "inside a string", chunks: []string{"\x1b[?1004h\x1b]0;[?1004l\x07"}, wantOut: "\x1b[?1004h\x1b]0;[?1004l\x07", wantGuest: true},
		{name: "after a string", chunks: []string{"\x1b[?1004h\x1b]0;t\x1b\\\x1b[?1004l"}, wantOut: "\x1b[?1004h\x1b]0;t\x1b\\\x1b[?1004l" + reassert},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			focus := &sessionFocus{}
			o := focusOutput{focus: focus}
			var out strings.Builder
			for _, c := range tt.chunks {
				out.Write(o.feed([]byte(c)))
			}
			if out.String() != tt.wantOut || focus.guestReports() != tt.wantGuest {
				t.Fatalf("feed() = %q, guest %v; want %q, guest %v", out.String(), focus.guestReports(), tt.wantOut, tt.wantGuest)
			}
		})
	}
}

func TestFocusInputTracksFocus(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		guest    bool
		chunks   []string
		wantOut  string
		wantAway bool
	}{
		{name: "focus out dropped", chunks: []string{"\x1b[O"}, wantOut: "", wantAway: true},
		{name: "focus in dropped", chunks: []string{"\x1b[O", "\x1b[I"}, wantOut: ""},
		{name: "forwarded to a reporting guest", guest: true, chunks: []string{"a\x1b[Ob"}, wantOut: "a\x1b[Ob"},
		{name: "forwarded focus out leaves away", guest: true, chunks: []string{"\x1b[O"}, wantOut: "\x1b[O", wantAway: true},
		{name: "typing returns", chunks: []string{"\x1b[O", "a"}, wantOut: "a"},
		{name: "arrow key returns", chunks: []string{"\x1b[O", "\x1b[A"}, wantOut: "\x1b[A"},
		{name: "SS3 key returns", chunks: []string{"\x1b[O", "\x1bOP"}, wantOut: "\x1bOP"},
		{name: "alt key returns", chunks: []string{"\x1b[O", "\x1bx"}, wantOut: "\x1bx"},
		{name: "SGR mouse stays away", chunks: []string{"\x1b[O", "\x1b[<35;10;5M"}, wantOut: "\x1b[<35;10;5M", wantAway: true},
		{name: "X10 mouse stays away", chunks: []string{"\x1b[O", "\x1b[M #!"}, wantOut: "\x1b[M #!", wantAway: true},
		{name: "OSC reply stays away", chunks: []string{"\x1b[O", "\x1b]11;rgb:0000/0000/0000\x1b\\"}, wantOut: "\x1b]11;rgb:0000/0000/0000\x1b\\", wantAway: true},
		{name: "BEL reply stays away", chunks: []string{"\x1b[O", "\x1b]10;rgb:ffff/ffff/ffff\x07"}, wantOut: "\x1b]10;rgb:ffff/ffff/ffff\x07", wantAway: true},
		{name: "DA reply stays away", chunks: []string{"\x1b[O", "\x1b[?62;22c\x1b[>1;10;0c"}, wantOut: "\x1b[?62;22c\x1b[>1;10;0c", wantAway: true},
		{name: "kitty flags reply stays away", chunks: []string{"\x1b[O", "\x1b[?1u"}, wantOut: "\x1b[?1u", wantAway: true},
		{name: "paste returns and is not parsed", chunks: []string{"\x1b[O", "\x1b[200~a\x1b[Ob\x1b[201~"}, wantOut: "\x1b[200~a\x1b[Ob\x1b[201~"},
		{name: "focus after paste", chunks: []string{"\x1b[200~a\x1b[201~\x1b[O"}, wantOut: "\x1b[200~a\x1b[201~", wantAway: true},
		{name: "fragmented paste end", chunks: []string{"\x1b[200~a\x1b[2", "01~\x1b[O"}, wantOut: "\x1b[200~a\x1b[201~", wantAway: true},
		{name: "report split after escape is recognized", chunks: []string{"\x1b[I", "\x1b", "[O"}, wantOut: "\x1b[O", wantAway: true},
		{name: "focus in split after escape is recognized", chunks: []string{"\x1b[O", "\x1b", "[I"}, wantOut: "\x1b[I"},
		{name: "report split after CSI is recognized", chunks: []string{"\x1b[", "O"}, wantOut: "\x1b[O", wantAway: true},
		{name: "split prefix is unknown until it completes", chunks: []string{"\x1b[O", "\x1b["}, wantOut: "\x1b["},
		{name: "split mouse report stays unknown", chunks: []string{"\x1b[O", "\x1b", "[<35;10;5M"}, wantOut: "\x1b[<35;10;5M"},
		{name: "split reply stays unknown", chunks: []string{"\x1b[O", "\x1b", "]11;rgb:0000/0000/0000\x07"}, wantOut: "\x1b]11;rgb:0000/0000/0000\x07"},
		{name: "focus out after split", chunks: []string{"\x1b", "[O", "\x1b[O"}, wantOut: "\x1b[O", wantAway: true},
		{name: "trailing escape is unknown", chunks: []string{"\x1b[O", "\x1b"}, wantOut: "\x1b"},
		{name: "escape key returns", chunks: []string{"\x1b[O", "\x1b", "\x1b[O", "\x1b\x1b"}, wantOut: "\x1b\x1b\x1b"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			focus := &sessionFocus{}
			focus.setGuestReports(tt.guest)
			in := focusInput{focus: focus}
			var out strings.Builder
			for _, c := range tt.chunks {
				out.Write(in.feed([]byte(c)))
			}
			if away, _ := focus.away(); out.String() != tt.wantOut || away != tt.wantAway {
				t.Fatalf("feed() = %q, away %v; want %q, away %v", out.String(), away, tt.wantOut, tt.wantAway)
			}
		})
	}
}

func TestSessionFocusEpochChangesWithState(t *testing.T) {
	t.Parallel()

	var f sessionFocus
	if away, _ := f.away(); away {
		t.Fatal("new focus is away; want unknown, which is not away")
	}
	f.set(focusOut)
	_, e1 := f.away()
	f.set(focusOut)
	if _, e := f.away(); e != e1 {
		t.Fatalf("epoch changed without a state change: %d -> %d", e1, e)
	}
	f.set(focusIn)
	f.set(focusOut)
	if away, e := f.away(); !away || e == e1 {
		t.Fatalf("away() = %v, epoch %d; want away with an epoch other than %d", away, e, e1)
	}
}

func TestNotifyTerminalWriteDoesNotBlock(t *testing.T) {
	t.Parallel()

	var fds [2]int
	if err := syscall.Pipe(fds[:]); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = syscall.Close(fds[0])
		_ = syscall.Close(fds[1])
	})
	if err := syscall.SetNonblock(fds[1], true); err != nil {
		t.Fatal(err)
	}
	// Fill the pipe like a terminal that stopped reading.
	chunk := make([]byte, 4096)
	for {
		if _, err := syscall.Write(fds[1], chunk); err != nil {
			if !errors.Is(err, syscall.EAGAIN) {
				t.Fatalf("fill pipe: %v", err)
			}
			break
		}
	}
	tty := &notifyTerminal{fd: fds[1]}
	if n, err := tty.write([]byte(testNotifyInput)); n != 0 || !errors.Is(err, errNotifyDropped) {
		t.Fatalf("write() to a full terminal = %d, %v; want 0 and a drop", n, err)
	}
	if _, err := syscall.Read(fds[0], make([]byte, 64<<10)); err != nil {
		t.Fatal(err)
	}
	if n, err := tty.write([]byte(testNotifyInput)); n != len(testNotifyInput) || err != nil {
		t.Fatalf("write() with room = %d, %v; want %d, nil", n, err, len(testNotifyInput))
	}
}

func TestNotifyTerminalWriteError(t *testing.T) {
	t.Parallel()

	tty := &notifyTerminal{fd: -1}
	if _, err := tty.write([]byte("x")); err == nil || errors.Is(err, errNotifyDropped) {
		t.Fatalf("write() to a bad descriptor = %v, want a failure that is not a drop", err)
	}
}
