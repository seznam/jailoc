package docker

import (
	"bytes"
	"fmt"
	"io"
	"sync"
)

// githubDarkPalette defines the ANSI 16-color palette for the GitHub Dark
// Default terminal theme. OpenCode's "opencode" theme derives its background
// scale from the terminal palette queried via OSC 4. When running inside a
// Docker container the round-trip latency causes the query to time out,
// producing neutral gray backgrounds. By responding to OSC 4 queries
// immediately with this palette, the expected blue-tinted backgrounds appear.
var githubDarkPalette = [16][3]byte{
	{0x48, 0x4f, 0x58}, // 0:  Black
	{0xff, 0x7b, 0x72}, // 1:  Red
	{0x3f, 0xb9, 0x50}, // 2:  Green
	{0xd2, 0x99, 0x22}, // 3:  Yellow
	{0x58, 0xa6, 0xff}, // 4:  Blue
	{0xbc, 0x8c, 0xff}, // 5:  Magenta
	{0x39, 0xc5, 0xcf}, // 6:  Cyan
	{0xb1, 0xba, 0xc4}, // 7:  White
	{0x6e, 0x76, 0x81}, // 8:  Bright Black
	{0xff, 0xa1, 0x98}, // 9:  Bright Red
	{0x56, 0xd3, 0x64}, // 10: Bright Green
	{0xe3, 0xb3, 0x41}, // 11: Bright Yellow
	{0x79, 0xc0, 0xff}, // 12: Bright Blue
	{0xd2, 0xa8, 0xff}, // 13: Bright Magenta
	{0x56, 0xd4, 0xdd}, // 14: Bright Cyan
	{0xf0, 0xf6, 0xfc}, // 15: Bright White
}

// osc4Prefix is the byte sequence that starts an OSC 4 palette query.
var osc4Prefix = []byte("\x1b]4;")

// maxOSC4QueryLen is the maximum length of a complete OSC 4 query:
// \x1b]4;NNN;?\x1b\ = 4 + 3 + 2 + 2 = 11 bytes.
const maxOSC4QueryLen = 11

// paletteResponder intercepts OSC 4 palette queries in the container's stdout
// stream and writes immediate responses to the container's stdin. Queries for
// color indices 0-15 are suppressed from the downstream output to prevent
// double-responses from the host terminal. All other data passes through
// unchanged.
type paletteResponder struct {
	downstream io.Writer
	respond    io.Writer
	buf        []byte
}

func newPaletteResponder(downstream, respond io.Writer) *paletteResponder {
	return &paletteResponder{
		downstream: downstream,
		respond:    respond,
	}
}

func (r *paletteResponder) Write(p []byte) (int, error) {
	data := append(r.buf, p...) //nolint:gocritic // intentional merge of buf and p
	r.buf = r.buf[:0]

	for len(data) > 0 {
		idx := bytes.Index(data, osc4Prefix)
		if idx < 0 {
			keep := partialSuffix(data, osc4Prefix)
			flush := data[:len(data)-keep]
			if len(flush) > 0 {
				if err := writeAll(r.downstream, flush); err != nil {
					return len(p), err
				}
			}
			r.buf = append(r.buf[:0], data[len(data)-keep:]...)
			break
		}

		if idx > 0 {
			if err := writeAll(r.downstream, data[:idx]); err != nil {
				return len(p), err
			}
		}

		rest := data[idx:]
		colorIdx, seqLen, ok := parseOSC4Query(rest)
		switch {
		case ok && colorIdx < 16:
			_, _ = r.respond.Write(formatOSC4Response(colorIdx))
			data = rest[seqLen:]
		case ok:
			if err := writeAll(r.downstream, rest[:seqLen]); err != nil {
				return len(p), err
			}
			data = rest[seqLen:]
		case len(rest) < maxOSC4QueryLen:
			r.buf = append(r.buf[:0], rest...)
			return len(p), nil
		default:
			if err := writeAll(r.downstream, osc4Prefix); err != nil {
				return len(p), err
			}
			data = rest[len(osc4Prefix):]
		}
	}

	return len(p), nil
}

// Flush writes any buffered partial-match bytes to the downstream writer.
func (r *paletteResponder) Flush() error {
	if len(r.buf) > 0 {
		err := writeAll(r.downstream, r.buf)
		r.buf = r.buf[:0]
		return err
	}
	return nil
}

// parseOSC4Query parses an OSC 4 query starting at the beginning of data.
// Returns (colorIndex, sequenceLength, ok). The expected format is:
//
//	\x1b]4;<digits>;?<terminator>
//
// where terminator is \x07 (BEL) or \x1b\\ (ST).
func parseOSC4Query(data []byte) (int, int, bool) {
	if len(data) < len(osc4Prefix) {
		return 0, 0, false
	}

	pos := len(osc4Prefix)

	digitStart := pos
	for pos < len(data) && data[pos] >= '0' && data[pos] <= '9' {
		pos++
	}
	if pos == digitStart || pos-digitStart > 3 {
		return 0, 0, false
	}

	colorIdx := 0
	for _, b := range data[digitStart:pos] {
		colorIdx = colorIdx*10 + int(b-'0')
	}

	if pos+2 > len(data) {
		return 0, 0, false
	}
	if data[pos] != ';' || data[pos+1] != '?' {
		return 0, 0, false
	}
	pos += 2

	if pos >= len(data) {
		return 0, 0, false
	}
	if data[pos] == '\x07' {
		return colorIdx, pos + 1, true
	}
	if data[pos] == '\x1b' {
		if pos+1 >= len(data) {
			return 0, 0, false
		}
		if data[pos+1] == '\\' {
			return colorIdx, pos + 2, true
		}
	}

	return 0, 0, false
}

// formatOSC4Response builds an OSC 4 response for the given color index using
// the GitHub Dark palette. The format is: \x1b]4;<N>;rgb:<RRRR>/<GGGG>/<BBBB>\x07
// with 8-bit values expanded to 16-bit by duplication (0xAB → 0xABAB).
func formatOSC4Response(colorIdx int) []byte {
	c := githubDarkPalette[colorIdx]
	return fmt.Appendf(nil, "\x1b]4;%d;rgb:%02x%02x/%02x%02x/%02x%02x\x07",
		colorIdx, c[0], c[0], c[1], c[1], c[2], c[2])
}

// partialSuffix returns how many bytes at the end of data could be the start
// of target. Used to avoid splitting a match across Write boundaries.
func partialSuffix(data, target []byte) int {
	for i := 1; i < len(target) && i <= len(data); i++ {
		if bytes.Equal(data[len(data)-i:], target[:i]) {
			return i
		}
	}
	return 0
}

// writeAll writes all of p to w, retrying short writes.
func writeAll(w io.Writer, p []byte) error {
	for len(p) > 0 {
		n, err := w.Write(p)
		p = p[n:]
		if err != nil {
			return err
		}
	}
	return nil
}

type syncWriter struct {
	mu sync.Mutex
	w  io.Writer
}

func (sw *syncWriter) Write(p []byte) (int, error) {
	sw.mu.Lock()
	defer sw.mu.Unlock()
	return sw.w.Write(p)
}
