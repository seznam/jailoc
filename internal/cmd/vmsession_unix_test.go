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
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			m := &detachMatcher{keys: tt.keys}
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
