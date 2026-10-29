//go:build darwin || linux

package cmd

import (
	"bytes"
	"testing"
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
