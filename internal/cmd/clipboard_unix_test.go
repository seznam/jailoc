//go:build darwin || linux

package cmd

import (
	"bytes"
	"encoding/base64"
	"errors"
	"slices"
	"strings"
	"testing"
)

func TestOSC52Bridge(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		chunks   []string
		wantOut  string
		wantCopy []string
	}{
		{
			name:     "BEL terminator",
			chunks:   []string{"before\x1b]52;c;aGVsbG8=\x07after"},
			wantOut:  "before\x1b]52;c;aGVsbG8=\x07after",
			wantCopy: []string{"hello"},
		},
		{
			name:     "ST terminator",
			chunks:   []string{"\x1b]52;c;d29ybGQ=\x1b\\"},
			wantOut:  "\x1b]52;c;d29ybGQ=\x1b\\",
			wantCopy: []string{"world"},
		},
		{
			name:     "C1 ST terminator",
			chunks:   []string{"\x1b]52;pc;YzE=\x9c"},
			wantOut:  "\x1b]52;pc;YzE=\x9c",
			wantCopy: []string{"c1"},
		},
		{
			name:     "arbitrary chunk boundaries",
			chunks:   []string{"x\x1b", "]", "5", "2", ";", "c", ";", "Y2", "h1", "bms=", "\x1b", "\\y"},
			wantOut:  "x\x1b]52;c;Y2h1bms=\x1b\\y",
			wantCopy: []string{"chunk"},
		},
		{
			name:     "empty selection",
			chunks:   []string{"\x1b]52;;ZW1wdHk=\x07"},
			wantOut:  "\x1b]52;;ZW1wdHk=\x07",
			wantCopy: []string{"empty"},
		},
		{
			name:     "selection parameters",
			chunks:   []string{"\x1b]52;c;Yw==\x07\x1b]52;p;cA==\x07\x1b]52;pc;cGM=\x07"},
			wantOut:  "\x1b]52;c;Yw==\x07\x1b]52;p;cA==\x07\x1b]52;pc;cGM=\x07",
			wantCopy: []string{"c", "pc"},
		},
		{
			name:    "clipboard query",
			chunks:  []string{"\x1b]52;c;?\x07"},
			wantOut: "\x1b]52;c;?\x07",
		},
		{
			name:    "invalid base64",
			chunks:  []string{"\x1b]52;c;not!base64\x07"},
			wantOut: "\x1b]52;c;not!base64\x07",
		},
		{
			name: "other terminal sequences",
			chunks: []string{
				"text\x1b]0;title\x07",
				"\x1b]11;?\x07\x1b[31mred\x1b[0m",
			},
			wantOut: "text\x1b]0;title\x07\x1b]11;?\x07\x1b[31mred\x1b[0m",
		},
		{
			name: "tmux DCS wrapped",
			chunks: []string{
				"\x1bPtmux;\x1b\x1b]52;c;dG11eA==\x07\x1b\\",
			},
			wantOut:  "\x1bPtmux;\x1b\x1b]52;c;dG11eA==\x07\x1b\\",
			wantCopy: []string{"tmux"},
		},
		{
			name: "tmux DCS wrapped with inner ST",
			chunks: []string{
				"\x1bPtmux;\x1b\x1b]52;c;c3Q=\x1b\x1b\\\x1b\\",
			},
			wantOut:  "\x1bPtmux;\x1b\x1b]52;c;c3Q=\x1b\x1b\\\x1b\\",
			wantCopy: []string{"st"},
		},
		{
			name:     "cancelled DCS recovers for next OSC 52",
			chunks:   []string{"\x1bPtmux;partial\x18\x1b]52;c;Y2Fu\x07"},
			wantOut:  "\x1bPtmux;partial\x18\x1b]52;c;Y2Fu\x07",
			wantCopy: []string{"can"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			var copied []string
			bridge := newOSC52Bridge(func(data []byte) {
				copied = append(copied, string(data))
			}, false)
			var output []byte
			for _, chunk := range tt.chunks {
				output = append(output, bridge.feed([]byte(chunk))...)
			}

			if string(output) != tt.wantOut {
				t.Fatalf("output = %q, want %q", output, tt.wantOut)
			}
			if strings.Join(copied, "|") != strings.Join(tt.wantCopy, "|") {
				t.Fatalf("copies = %q, want %q", copied, tt.wantCopy)
			}
		})
	}
}

func TestOSC52BridgeTMUXPassthrough(t *testing.T) {
	t.Parallel()

	raw := "\x1b]52;c;aGk=\x07"
	bridge := newOSC52Bridge(func([]byte) {}, true)
	got := string(bridge.feed([]byte(raw)))
	want := raw + "\x1bPtmux;\x1b\x1b]52;c;aGk=\x07\x1b\\"
	if got != want {
		t.Fatalf("feed() = %q, want %q", got, want)
	}

	wrapped := "\x1bPtmux;\x1b\x1b]52;c;aGk=\x07\x1b\\"
	got = string(bridge.feed([]byte(wrapped)))
	if got != wrapped {
		t.Fatalf("already wrapped feed() = %q, want unchanged %q", got, wrapped)
	}
	query := "\x1b]52;c;?\x07"
	got = string(bridge.feed([]byte(query)))
	if got != query {
		t.Fatalf("query feed() = %q, want unchanged %q", got, query)
	}
}

func TestOSC52BridgeOversizedPayload(t *testing.T) {
	t.Parallel()

	payload := bytes.Repeat([]byte("A"), maxOSC52Len+1)
	sequence := append([]byte("\x1b]52;c;"), payload...)
	sequence = append(sequence, '\a')

	var copied bool
	bridge := newOSC52Bridge(func([]byte) { copied = true }, false)
	var output []byte
	for len(sequence) > 0 {
		n := min(4093, len(sequence))
		output = append(output, bridge.feed(sequence[:n])...)
		sequence = sequence[n:]
		if len(bridge.sequence) > maxOSC52Len {
			t.Fatalf("buffer grew to %d bytes, cap is %d", len(bridge.sequence), maxOSC52Len)
		}
	}

	if copied {
		t.Fatal("oversized payload was copied")
	}
	if len(output) != len(payload)+len("\x1b]52;c;\x07") {
		t.Fatalf("output length = %d, want %d", len(output), len(payload)+len("\x1b]52;c;\x07"))
	}
}

func TestOSC52BridgeCopiesDecodedBytes(t *testing.T) {
	t.Parallel()

	want := []byte{0, 1, 2, 0xfe, 0xff}
	sequence := "\x1b]52;c;" + base64.StdEncoding.EncodeToString(want) + "\x07"
	var got []byte
	bridge := newOSC52Bridge(func(data []byte) {
		got = append([]byte(nil), data...)
	}, false)
	bridge.feed([]byte(sequence))

	if !bytes.Equal(got, want) {
		t.Fatalf("copied = %v, want %v", got, want)
	}
}

func TestHostClipboardQueueDrainsOnClose(t *testing.T) {
	t.Parallel()

	started := make(chan struct{})
	release := make(chan struct{})
	var copied []string
	q := newHostClipboardQueue(func(data []byte) {
		if len(copied) == 0 {
			close(started)
			<-release
		}
		copied = append(copied, string(data))
	})
	q.enqueue([]byte("first"))
	<-started
	q.enqueue([]byte("stale"))
	q.enqueue([]byte("latest"))
	close(release)
	q.close()

	if got, want := strings.Join(copied, "|"), "first|latest"; got != want {
		t.Fatalf("copied = %q, want %q", got, want)
	}
}

func TestHostClipboardCommands(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		goos  string
		env   map[string]string
		tools map[string]string
		want  []clipboardCmd
	}{
		{
			name: "darwin uses pbcopy",
			goos: "darwin",
			want: []clipboardCmd{{name: "pbcopy"}},
		},
		{
			name:  "linux wayland sets clipboard and primary",
			goos:  "linux",
			env:   map[string]string{"WAYLAND_DISPLAY": "wayland-0"},
			tools: map[string]string{"wl-copy": "/usr/bin/wl-copy"},
			want: []clipboardCmd{
				{name: "/usr/bin/wl-copy"},
				{name: "/usr/bin/wl-copy", args: []string{"--primary"}},
			},
		},
		{
			name:  "linux x11 xclip sets clipboard and primary",
			goos:  "linux",
			env:   map[string]string{"DISPLAY": ":0"},
			tools: map[string]string{"xclip": "/usr/bin/xclip"},
			want: []clipboardCmd{
				{name: "/usr/bin/xclip", args: []string{"-selection", "clipboard"}},
				{name: "/usr/bin/xclip", args: []string{"-selection", "primary"}},
			},
		},
		{
			name:  "linux x11 xsel sets clipboard and primary",
			goos:  "linux",
			env:   map[string]string{"DISPLAY": ":0"},
			tools: map[string]string{"xsel": "/usr/bin/xsel"},
			want: []clipboardCmd{
				{name: "/usr/bin/xsel", args: []string{"--clipboard", "--input"}},
				{name: "/usr/bin/xsel", args: []string{"--primary", "--input"}},
			},
		},
		{
			name:  "linux wayland and xwayland updates both when tools present",
			goos:  "linux",
			env:   map[string]string{"WAYLAND_DISPLAY": "wayland-0", "DISPLAY": ":0"},
			tools: map[string]string{"wl-copy": "/usr/bin/wl-copy", "xclip": "/usr/bin/xclip"},
			want: []clipboardCmd{
				{name: "/usr/bin/wl-copy"},
				{name: "/usr/bin/wl-copy", args: []string{"--primary"}},
				{name: "/usr/bin/xclip", args: []string{"-selection", "clipboard"}},
				{name: "/usr/bin/xclip", args: []string{"-selection", "primary"}},
			},
		},
		{
			name:  "linux falls back to python3 gtk when cli tools absent",
			goos:  "linux",
			env:   map[string]string{"DISPLAY": ":0"},
			tools: map[string]string{"python3": "/usr/bin/python3"},
			want: []clipboardCmd{
				{name: "/usr/bin/python3", args: []string{"-c", gtkClipboardScript}},
			},
		},
		{
			name:  "linux headless returns none",
			goos:  "linux",
			tools: map[string]string{"xclip": "/usr/bin/xclip", "python3": "/usr/bin/python3"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := hostClipboardCommands(
				tt.goos,
				func(k string) string { return tt.env[k] },
				func(bin string) (string, error) {
					if p, ok := tt.tools[bin]; ok {
						return p, nil
					}
					return "", errors.New("not found")
				},
			)
			if !slices.EqualFunc(got, tt.want, func(a, b clipboardCmd) bool {
				return a.name == b.name && slices.Equal(a.args, b.args)
			}) {
				t.Fatalf("hostClipboardCommands() = %+v, want %+v", got, tt.want)
			}
		})
	}
}
