//go:build darwin || linux

package cmd

import (
	"bytes"
	"io"
	"strings"
	"testing"

	"github.com/seznam/jailoc/internal/agent"
	"github.com/seznam/jailoc/internal/guest"
	"github.com/seznam/jailoc/internal/guest/session"
	"github.com/seznam/jailoc/internal/vm"
	"github.com/seznam/jailoc/internal/workspace"
)

func TestDetachMatcher(t *testing.T) {
	t.Parallel()

	const ctrlB, ctrlBackslash = 0x02, 0x1c
	tests := []struct {
		name       string
		keys       []byte
		chunks     []string
		wantOut    string
		wantDetach bool
	}{
		{name: "plain input", keys: []byte{ctrlB, 'd'}, chunks: []string{"hello"}, wantOut: "hello"},
		{name: "sequence in one read", keys: []byte{ctrlB, 'd'}, chunks: []string{"ab\x02dcd"}, wantOut: "ab", wantDetach: true},
		{name: "sequence split across reads", keys: []byte{ctrlB, 'd'}, chunks: []string{"ab\x02", "d"}, wantOut: "ab", wantDetach: true},
		{name: "prefix then other key", keys: []byte{ctrlB, 'd'}, chunks: []string{"\x02", "x"}, wantOut: "\x02x"},
		{name: "prefix twice sends it once", keys: []byte{ctrlB, 'd'}, chunks: []string{"\x02\x02", "d"}, wantOut: "\x02d"},
		{name: "prefix twice then sequence", keys: []byte{ctrlB, 'd'}, chunks: []string{"\x02\x02\x02d"}, wantOut: "\x02", wantDetach: true},
		{name: "held prefix at end", keys: []byte{ctrlB, 'd'}, chunks: []string{"a\x02"}, wantOut: "a"},
		{name: "single key", keys: []byte{ctrlBackslash}, chunks: []string{"a\x1cb"}, wantOut: "a", wantDetach: true},
		{name: "three keys partial mismatch", keys: []byte{ctrlB, 'd', 'q'}, chunks: []string{"\x02dx\x02dq"}, wantOut: "\x02dx", wantDetach: true},
		{name: "mismatch restarts on prefix", keys: []byte{ctrlB, 'd', 'q'}, chunks: []string{"\x02d\x02dq"}, wantOut: "\x02d", wantDetach: true},
		{name: "repeated key sequence", keys: []byte{ctrlB, ctrlB}, chunks: []string{"a\x02", "\x02"}, wantOut: "a", wantDetach: true},
		{name: "kitty ctrl-b", keys: []byte{ctrlB, 'd'}, chunks: []string{"x\x1b[98;5u", "d"}, wantOut: "x", wantDetach: true},
		{name: "kitty ctrl-b with event type", keys: []byte{ctrlB, 'd'}, chunks: []string{"\x1b[98;5:1u", "\x1b[100u"}, wantDetach: true},
		{name: "modifyOtherKeys ctrl-b", keys: []byte{ctrlB, 'd'}, chunks: []string{"\x1b[27;5;98~", "d"}, wantDetach: true},
		{name: "kitty ctrl-backslash", keys: []byte{ctrlBackslash}, chunks: []string{"\x1b[92;5u"}, wantDetach: true},
		{name: "kitty ctrl-b twice sends it once", keys: []byte{ctrlB, 'd'}, chunks: []string{"\x1b[98;5u", "\x1b[98;5u", "d"}, wantOut: "\x1b[98;5ud"},
		{name: "kitty ctrl-b then arrow", keys: []byte{ctrlB, 'd'}, chunks: []string{"\x1b[98;5u", "\x1b[A"}, wantOut: "\x1b[98;5u\x1b[A"},
		{name: "esc is not held", keys: []byte{ctrlB, 'd'}, chunks: []string{"\x1b"}, wantOut: "\x1b"},
		{name: "esc after prefix is not held", keys: []byte{ctrlB, 'd'}, chunks: []string{"\x02", "\x1b"}, wantOut: "\x02\x1b"},
		{name: "other escape sequences pass", keys: []byte{ctrlB, 'd'}, chunks: []string{"\x1b[A\x1b[99;5u"}, wantOut: "\x1b[A\x1b[99;5u"},
		{name: "kitty release events between keys", keys: []byte{ctrlB, 'd'}, chunks: []string{"\x1b[98;5u", "\x1b[98;5:3u", "\x1b[100u"}, wantDetach: true},
		{name: "kitty repeat events between keys", keys: []byte{ctrlB, 'd'}, chunks: []string{"\x1b[98;5u\x1b[98;5:2u\x1b[98;5:3u", "\x1b[100;1u"}, wantDetach: true},
		{name: "kitty modifier key presses", keys: []byte{ctrlB, 'd'}, chunks: []string{"\x1b[57442u\x1b[98;5u\x1b[57442;5:3u\x1b[98;1:3u", "\x1b[100u"}, wantOut: "\x1b[57442u", wantDetach: true},
		{name: "kitty release of other key between keys", keys: []byte{ctrlB, 'd'}, chunks: []string{"\x1b[98;5u\x1b[1;5:3A", "d"}, wantDetach: true},
		{name: "kitty ctrl-b with caps lock", keys: []byte{ctrlB, 'd'}, chunks: []string{"\x1b[98;69u", "\x1b[100;65u"}, wantDetach: true},
		{name: "kitty ctrl-b with num lock", keys: []byte{ctrlB, 'd'}, chunks: []string{"\x1b[98;133u", "d"}, wantDetach: true},
		{name: "kitty alternate keys", keys: []byte{ctrlB, 'D'}, chunks: []string{"\x1b[98:66:98;5u", "\x1b[100:68;2u"}, wantDetach: true},
		{name: "kitty shift letter without alternate", keys: []byte{ctrlB, 'D'}, chunks: []string{"\x1b[98;5u", "\x1b[100;2u"}, wantDetach: true},
		{name: "kitty ctrl-shift-b is not ctrl-b", keys: []byte{ctrlB, 'd'}, chunks: []string{"\x1b[98;6u", "d"}, wantOut: "\x1b[98;6ud"},
		{name: "kitty release forwarded outside sequence", keys: []byte{ctrlB, 'd'}, chunks: []string{"\x1b[97;1:3u"}, wantOut: "\x1b[97;1:3u"},
		{name: "kitty esc key", keys: []byte{0x1b}, chunks: []string{"\x1b[27u"}, wantDetach: true},
		{name: "alt key is not esc", keys: []byte{0x1b}, chunks: []string{"\x1bd"}, wantOut: "\x1bd"},
		{name: "partial escape sequence flushes held keys", keys: []byte{ctrlB, 'd'}, chunks: []string{"\x02", "\x1b[9"}, wantOut: "\x02\x1b[9"},
		{name: "terminal reply passes", keys: []byte{ctrlB, 'd'}, chunks: []string{"\x1b[?1u\x1b[12;40R"}, wantOut: "\x1b[?1u\x1b[12;40R"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			m := newDetachMatcher(tt.keys)
			var out []byte
			var detach bool
			for _, c := range tt.chunks {
				got, d := m.feed([]byte(c))
				out = append(out, got...)
				if d {
					detach = true
					break
				}
			}
			if !bytes.Equal(out, []byte(tt.wantOut)) || detach != tt.wantDetach {
				t.Fatalf("feed(%q) = %q, %v; want %q, %v", tt.chunks, out, detach, tt.wantOut, tt.wantDetach)
			}
		})
	}
}

func TestCheckGuestCurrent(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	write := func(name string, spec vm.Spec) {
		t.Helper()
		spec.StateDir = vmStateDir(name)
		if err := vm.WriteSpec(spec); err != nil {
			t.Fatalf("write spec: %v", err)
		}
	}
	write("same", vm.Spec{GuestBinary: "/cache/jailoc-guest-old", GuestProtocol: guest.Protocol})
	write("older", vm.Spec{GuestBinary: "/cache/jailoc-guest-old"})
	write("nobinary", vm.Spec{})

	tests := []struct {
		name    string
		wantErr bool
	}{
		{name: "same", wantErr: false},
		{name: "older", wantErr: true},
		{name: "nobinary", wantErr: false},
		{name: "missing", wantErr: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := checkGuestCurrent(tt.name)
			if (err != nil) != tt.wantErr {
				t.Fatalf("checkGuestCurrent(%q) error = %v, wantErr %v", tt.name, err, tt.wantErr)
			}
		})
	}
}

type nopWriteCloser struct{ io.Writer }

func (nopWriteCloser) Close() error { return nil }

func TestRelaySessionDiagnoseAndFormatError(t *testing.T) {
	t.Parallel()

	opencode1, _ := agent.Lookup("opencode1")
	claude, _ := agent.Lookup("claude")

	tests := []struct {
		name         string
		agent        agent.Harness
		frames       []session.Frame
		wantDiagnose bool
		wantErrSub   string
	}{
		{
			name:  "opencode crash after leaving alt screen with server error",
			agent: opencode1,
			frames: []session.Frame{
				{Type: session.FrameInfo, Payload: []byte("s1")},
				{Type: session.FrameStarted},
				{Type: session.FrameData, Payload: []byte("\x1b[?1049h\x1b[?1049l\x1b[91m\x1b[1mError: \x1b[0mUnexpected server error. Check server logs for details.\n")},
				session.ExitFrame(1),
			},
			wantDiagnose: true,
			wantErrSub:   "run `jailoc -w szn exec -- opencode --print-logs` for details",
		},
		{
			name:  "opencode crash before drawing any visible text",
			agent: opencode1,
			frames: []session.Frame{
				{Type: session.FrameInfo, Payload: []byte("s2")},
				{Type: session.FrameStarted},
				{Type: session.FrameData, Payload: []byte("\x1b[?1049h\x1b[2J")},
				session.ExitFrame(1),
			},
			wantDiagnose: true,
			wantErrSub:   "run `jailoc -w szn exec -- opencode --print-logs` for details",
		},
		{
			name:  "normal exit after drawing TUI without server error",
			agent: opencode1,
			frames: []session.Frame{
				{Type: session.FrameInfo, Payload: []byte("s3")},
				{Type: session.FrameStarted},
				{Type: session.FrameData, Payload: []byte("\x1b[?1049hWelcome to OpenCode\x1b[?1049l")},
				session.ExitFrame(1),
			},
			wantDiagnose: false,
			wantErrSub:   "opencode exited with status 1",
		},
		{
			name:  "agent without Diagnose command",
			agent: claude,
			frames: []session.Frame{
				{Type: session.FrameInfo, Payload: []byte("s4")},
				{Type: session.FrameStarted},
				session.ExitFrame(1),
			},
			wantDiagnose: true,
			wantErrSub:   "claude exited with status 1",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			var stream bytes.Buffer
			for _, f := range tt.frames {
				if err := session.WriteFrame(&stream, f); err != nil {
					t.Fatal(err)
				}
			}
			var in bytes.Buffer
			_, outcome, code, diagnose := relaySession(-1, false, nil, "Starting…", tt.agent.Title, nopWriteCloser{&in}, &stream, 24, 80)
			if outcome != outcomeExited || code != 1 || diagnose != tt.wantDiagnose {
				t.Fatalf("relaySession() = outcome %v, code %d, diagnose %v; want %v, 1, %v", outcome, code, diagnose, outcomeExited, tt.wantDiagnose)
			}
			ws := &workspace.Resolved{Name: "szn", Agent: tt.agent}
			err := formatAgentExitError(ws, code, diagnose)
			if err == nil || !strings.Contains(err.Error(), tt.wantErrSub) {
				t.Fatalf("formatAgentExitError() = %v, want substring %q", err, tt.wantErrSub)
			}
			if !tt.wantDiagnose && strings.Contains(err.Error(), "exec --") {
				t.Fatalf("unexpected diagnostic hint in %v", err)
			}
		})
	}
}
