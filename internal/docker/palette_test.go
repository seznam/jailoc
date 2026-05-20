package docker

import (
	"bytes"
	"strings"
	"testing"
)

func TestParseOSC4Query(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		input    []byte
		wantIdx  int
		wantLen  int
		wantOK   bool
	}{
		{
			name:    "color 0 BEL terminator",
			input:   []byte("\x1b]4;0;?\x07"),
			wantIdx: 0, wantLen: 8, wantOK: true,
		},
		{
			name:    "color 15 BEL terminator",
			input:   []byte("\x1b]4;15;?\x07"),
			wantIdx: 15, wantLen: 9, wantOK: true,
		},
		{
			name:    "color 255 BEL terminator",
			input:   []byte("\x1b]4;255;?\x07"),
			wantIdx: 255, wantLen: 10, wantOK: true,
		},
		{
			name:    "color 7 ST terminator",
			input:   []byte("\x1b]4;7;?\x1b\\"),
			wantIdx: 7, wantLen: 9, wantOK: true,
		},
		{
			name:  "missing terminator",
			input: []byte("\x1b]4;0;?"),
			wantOK: false,
		},
		{
			name:  "no digits",
			input: []byte("\x1b]4;;?\x07"),
			wantOK: false,
		},
		{
			name:  "too many digits",
			input: []byte("\x1b]4;1234;?\x07"),
			wantOK: false,
		},
		{
			name:  "wrong separator",
			input: []byte("\x1b]4;0:?\x07"),
			wantOK: false,
		},
		{
			name:  "too short",
			input: []byte("\x1b]4"),
			wantOK: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			gotIdx, gotLen, gotOK := parseOSC4Query(tt.input)
			if gotOK != tt.wantOK {
				t.Fatalf("ok = %v, want %v", gotOK, tt.wantOK)
			}
			if !gotOK {
				return
			}
			if gotIdx != tt.wantIdx {
				t.Errorf("colorIdx = %d, want %d", gotIdx, tt.wantIdx)
			}
			if gotLen != tt.wantLen {
				t.Errorf("seqLen = %d, want %d", gotLen, tt.wantLen)
			}
		})
	}
}

func TestFormatOSC4Response(t *testing.T) {
	t.Parallel()

	got := string(formatOSC4Response(0))
	want := "\x1b]4;0;rgb:4848/4f4f/5858\x07"
	if got != want {
		t.Errorf("formatOSC4Response(0) = %q, want %q", got, want)
	}

	got = string(formatOSC4Response(4))
	want = "\x1b]4;4;rgb:5858/a6a6/ffff\x07"
	if got != want {
		t.Errorf("formatOSC4Response(4) = %q, want %q", got, want)
	}
}

func TestPaletteResponderPassthrough(t *testing.T) {
	t.Parallel()

	var downstream, respond bytes.Buffer
	pr := newPaletteResponder(&downstream, &respond)

	data := []byte("hello world\x1b[31mred text\x1b[0m")
	n, err := pr.Write(data)
	if err != nil {
		t.Fatalf("Write error: %v", err)
	}
	if n != len(data) {
		t.Fatalf("Write n = %d, want %d", n, len(data))
	}
	if err := pr.Flush(); err != nil {
		t.Fatalf("Flush error: %v", err)
	}

	if downstream.String() != string(data) {
		t.Errorf("downstream = %q, want %q", downstream.String(), string(data))
	}
	if respond.Len() != 0 {
		t.Errorf("respond should be empty, got %q", respond.String())
	}
}

func TestPaletteResponderInterceptsQuery(t *testing.T) {
	t.Parallel()

	var downstream, respond bytes.Buffer
	pr := newPaletteResponder(&downstream, &respond)

	data := []byte("before\x1b]4;0;?\x07after")
	if _, err := pr.Write(data); err != nil {
		t.Fatalf("Write error: %v", err)
	}
	if err := pr.Flush(); err != nil {
		t.Fatalf("Flush error: %v", err)
	}

	if downstream.String() != "beforeafter" {
		t.Errorf("downstream = %q, want %q", downstream.String(), "beforeafter")
	}

	wantResp := "\x1b]4;0;rgb:4848/4f4f/5858\x07"
	if respond.String() != wantResp {
		t.Errorf("respond = %q, want %q", respond.String(), wantResp)
	}
}

func TestPaletteResponderMultipleQueries(t *testing.T) {
	t.Parallel()

	var downstream, respond bytes.Buffer
	pr := newPaletteResponder(&downstream, &respond)

	data := []byte("\x1b]4;0;?\x07\x1b]4;4;?\x07\x1b]4;15;?\x07")
	if _, err := pr.Write(data); err != nil {
		t.Fatalf("Write error: %v", err)
	}
	if err := pr.Flush(); err != nil {
		t.Fatalf("Flush error: %v", err)
	}

	if downstream.Len() != 0 {
		t.Errorf("downstream should be empty, got %q", downstream.String())
	}

	responses := respond.String()
	if !strings.Contains(responses, "\x1b]4;0;rgb:4848/4f4f/5858\x07") {
		t.Error("missing response for color 0")
	}
	if !strings.Contains(responses, "\x1b]4;4;rgb:5858/a6a6/ffff\x07") {
		t.Error("missing response for color 4")
	}
	if !strings.Contains(responses, "\x1b]4;15;rgb:f0f0/f6f6/fcfc\x07") {
		t.Error("missing response for color 15")
	}
}

func TestPaletteResponderPartialSequence(t *testing.T) {
	t.Parallel()

	var downstream, respond bytes.Buffer
	pr := newPaletteResponder(&downstream, &respond)

	if _, err := pr.Write([]byte("data\x1b]4;")); err != nil {
		t.Fatalf("Write 1 error: %v", err)
	}
	if _, err := pr.Write([]byte("3;?\x07more")); err != nil {
		t.Fatalf("Write 2 error: %v", err)
	}
	if err := pr.Flush(); err != nil {
		t.Fatalf("Flush error: %v", err)
	}

	if downstream.String() != "datamore" {
		t.Errorf("downstream = %q, want %q", downstream.String(), "datamore")
	}

	wantResp := "\x1b]4;3;rgb:d2d2/9999/2222\x07"
	if respond.String() != wantResp {
		t.Errorf("respond = %q, want %q", respond.String(), wantResp)
	}
}

func TestPaletteResponderHighColorPassthrough(t *testing.T) {
	t.Parallel()

	var downstream, respond bytes.Buffer
	pr := newPaletteResponder(&downstream, &respond)

	query := "\x1b]4;200;?\x07"
	if _, err := pr.Write([]byte(query)); err != nil {
		t.Fatalf("Write error: %v", err)
	}
	if err := pr.Flush(); err != nil {
		t.Fatalf("Flush error: %v", err)
	}

	if downstream.String() != query {
		t.Errorf("downstream = %q, want %q (high color should pass through)", downstream.String(), query)
	}
	if respond.Len() != 0 {
		t.Errorf("respond should be empty for high color, got %q", respond.String())
	}
}

func TestPaletteResponderSTTerminator(t *testing.T) {
	t.Parallel()

	var downstream, respond bytes.Buffer
	pr := newPaletteResponder(&downstream, &respond)

	data := []byte("x\x1b]4;7;?\x1b\\y")
	if _, err := pr.Write(data); err != nil {
		t.Fatalf("Write error: %v", err)
	}
	if err := pr.Flush(); err != nil {
		t.Fatalf("Flush error: %v", err)
	}

	if downstream.String() != "xy" {
		t.Errorf("downstream = %q, want %q", downstream.String(), "xy")
	}

	wantResp := "\x1b]4;7;rgb:b1b1/baba/c4c4\x07"
	if respond.String() != wantResp {
		t.Errorf("respond = %q, want %q", respond.String(), wantResp)
	}
}

func TestPartialSuffix(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		data   []byte
		target []byte
		want   int
	}{
		{"no match", []byte("hello"), []byte("\x1b]4;"), 0},
		{"1-byte match", []byte("data\x1b"), []byte("\x1b]4;"), 1},
		{"2-byte match", []byte("data\x1b]"), []byte("\x1b]4;"), 2},
		{"3-byte match", []byte("data\x1b]4"), []byte("\x1b]4;"), 3},
		{"full prefix not suffix", []byte("data\x1b]4;"), []byte("\x1b]4;"), 0},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := partialSuffix(tt.data, tt.target)
			if got != tt.want {
				t.Errorf("partialSuffix = %d, want %d", got, tt.want)
			}
		})
	}
}
