package session

import (
	"bytes"
	"testing"
)

func TestFrameRoundTrip(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	frames := []Frame{
		{Type: FrameData, Payload: []byte("hello\x1b[0m")},
		SizeFrame(FrameHello, 40, 120),
		ExitFrame(-3),
		{Type: FrameKicked},
	}
	for _, f := range frames {
		if err := WriteFrame(&buf, f); err != nil {
			t.Fatal(err)
		}
	}
	for i, want := range frames {
		got, err := ReadFrame(&buf)
		if err != nil {
			t.Fatalf("frame %d: %v", i, err)
		}
		if got.Type != want.Type || !bytes.Equal(got.Payload, want.Payload) {
			t.Fatalf("frame %d = %+v, want %+v", i, got, want)
		}
	}
	rows, cols, err := ParseSize(frames[1].Payload)
	if err != nil || rows != 40 || cols != 120 {
		t.Fatalf("ParseSize = %d %d %v", rows, cols, err)
	}
	if code, err := ParseExit(frames[2].Payload); err != nil || code != -3 {
		t.Fatalf("ParseExit = %d %v", code, err)
	}
	if _, err := ReadFrame(bytes.NewReader([]byte{FrameData, 0, 0, 0, 5, 'a'})); err == nil {
		t.Fatal("truncated frame accepted")
	}
}

func TestModeTracker(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		chunks []string
		want   string
	}{
		{name: "default terminal", want: ""},
		{name: "full-screen TUI", chunks: []string{"\x1b[?1049h\x1b[?25l\x1b[?1000h\x1b[?1006h\x1b[?2004h\x1b[>1u"}, want: "\x1b[?1049h\x1b[?25l\x1b[?1000h\x1b[?1006h\x1b[?2004h\x1b[>1u"},
		{name: "split sequence", chunks: []string{"text\x1b", "[?10", "02h more"}, want: "\x1b[?1002h"},
		{name: "combined params", chunks: []string{"\x1b[?1000;1006h"}, want: "\x1b[?1000h\x1b[?1006h"},
		{name: "disabled again", chunks: []string{"\x1b[?1000h\x1b[?1049h", "\x1b[?1000l\x1b[?1049l"}, want: ""},
		{name: "untracked modes ignored", chunks: []string{"\x1b[?7l\x1b[4h\x1b[?2026h"}, want: ""},
		{name: "kitty push pop", chunks: []string{"\x1b[>1u\x1b[>3u\x1b[<u"}, want: "\x1b[>1u"},
		{name: "kitty set modes", chunks: []string{"\x1b[=1;1u\x1b[=4;2u\x1b[=1;3u"}, want: "\x1b[>4u"},
		{name: "modifyOtherKeys and keypad", chunks: []string{"\x1b[>4;2m\x1b="}, want: "\x1b=\x1b[>4;2m"},
		{name: "full reset", chunks: []string{"\x1b[?1000h\x1b[>1u\x1bc"}, want: ""},
		{name: "colors are not modes", chunks: []string{"\x1b[38;5;196mred\x1b[0m"}, want: ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			tr := NewModeTracker()
			for _, c := range tt.chunks {
				tr.Feed([]byte(c))
			}
			if got := string(tr.Replay()); got != tt.want {
				t.Fatalf("Replay() = %q, want %q", got, tt.want)
			}
		})
	}
}
