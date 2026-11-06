//go:build darwin || linux

package cmd

import (
	"bytes"
	"strings"
	"testing"
)

func TestStartupStatusWrite(t *testing.T) {
	t.Parallel()

	const (
		alt   = "\x1b[?1049h"
		draw  = "\x1b7\x1b[1;1H\x1b[22;23;24;25;27;28;29m\x1b[2K\x1b[2mS\x1b8"
		erase = "\x1b7\x1b[1;1H\x1b[2K\x1b8"
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
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			var out bytes.Buffer
			s := newStartupStatus("S", 80)
			for _, c := range tt.chunks {
				if err := s.write(&out, []byte(c)); err != nil {
					t.Fatalf("write(%q): %v", c, err)
				}
			}
			if got := out.String(); got != tt.want {
				t.Errorf("output = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestNewStartupStatusTruncates(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		cols int
		want string
	}{
		{name: "fits", cols: 80, want: "Starting OpenCode…"},
		{name: "too wide", cols: 10, want: "Starting…"},
		{name: "unknown width", cols: 0, want: "Starting OpenCode…"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			draw := string(newStartupStatus("Starting OpenCode…", tt.cols).draw)
			if !strings.Contains(draw, "\x1b[2m"+tt.want+"\x1b8") {
				t.Errorf("draw = %q, want message %q", draw, tt.want)
			}
		})
	}
}
