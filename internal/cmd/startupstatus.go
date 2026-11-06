//go:build darwin || linux

package cmd

import (
	"io"
	"slices"
	"strings"
)

// startupStatus keeps a status line visible while a full-screen agent
// starts. Such agents switch to the alternate screen long before they draw
// their first frame, which would leave the terminal blank. Agent output
// passes through unchanged, so terminal queries the agent sends while it
// starts still reach the terminal; the status line is redrawn on the first
// row after each chunk until the agent prints visible text, then erased.
// Agents that never switch to the alternate screen are left alone.
type startupStatus struct {
	draw, erase []byte
	pending     bool // the agent has not drawn anything visible yet
	drawn       bool // the status line is on the alternate screen
	alt         bool // the agent is on the alternate screen
	state       escState
	params      []byte // parameters of the current CSI sequence
}

type escState int

const (
	escGround    escState = iota
	escStart              // after ESC
	escInter              // ESC followed by intermediate bytes
	escCSI                // ESC [
	escString             // OSC, DCS, SOS, PM or APC string
	escStringEnd          // ESC inside a string
)

// maxCSIParams bounds the bytes kept from one CSI sequence; private mode
// lists are short, longer sequences are never alternate screen switches.
const maxCSIParams = 32

// newStartupStatus returns a startupStatus that shows msg, truncated to
// fit a terminal cols wide. The status is drawn with the cursor and
// attributes saved and restored around it, so the agent's output is not
// moved or restyled.
func newStartupStatus(msg string, cols int) *startupStatus {
	if r := []rune(msg); cols > 1 && len(r) >= cols {
		msg = string(r[:cols-2]) + "…"
	}
	return &startupStatus{
		draw:    []byte("\x1b7\x1b[0m\x1b[1;1H\x1b[2K\x1b[2m" + msg + "\x1b8"),
		erase:   []byte("\x1b7\x1b[0m\x1b[1;1H\x1b[2K\x1b8"),
		pending: true,
	}
}

// write writes p, agent output, to w and keeps the status line on the
// alternate screen until the agent draws its first visible character.
func (s *startupStatus) write(w io.Writer, p []byte) error {
	if !s.pending {
		_, err := w.Write(p)
		return err
	}
	for i, b := range p {
		visible, altChanged := s.scan(b)
		switch {
		case altChanged && !s.alt:
			// The agent left the alternate screen and the status with it.
			s.pending = false
		case visible && s.alt && s.drawn:
			s.pending = false
			_, err := w.Write(slices.Concat(p[:i], s.erase, p[i:]))
			return err
		case visible:
			// The agent draws on the main screen, or on the alternate
			// screen before the status line was drawn.
			s.pending = false
		}
		if !s.pending {
			_, err := w.Write(p)
			return err
		}
	}
	if s.alt {
		p, s.drawn = slices.Concat(p, s.draw), true
	}
	_, err := w.Write(p)
	return err
}

// scan feeds one byte of agent output through the escape sequence parser.
// visible reports a printable character outside escape sequences; altChanged
// reports that the byte switched to or from the alternate screen.
func (s *startupStatus) scan(b byte) (visible, altChanged bool) {
	switch s.state {
	case escGround:
		if b == 0x1b {
			s.state = escStart
			return false, false
		}
		return b >= 0x20 && b != 0x7f, false
	case escStart:
		switch {
		case b == '[':
			s.state, s.params = escCSI, s.params[:0]
		case b == ']' || b == 'P' || b == 'X' || b == '^' || b == '_':
			s.state = escString
		case b >= 0x20 && b <= 0x2f:
			s.state = escInter
		default:
			s.state = escGround
		}
	case escInter:
		if b < 0x20 || b > 0x2f {
			s.state = escGround
		}
	case escCSI:
		switch {
		case b == 0x1b:
			s.state = escStart
		case b >= 0x40 && b <= 0x7e:
			s.state = escGround
			return false, s.csiFinal(b)
		case len(s.params) < maxCSIParams:
			s.params = append(s.params, b)
		}
	case escString:
		switch b {
		case 0x07:
			s.state = escGround
		case 0x1b:
			s.state = escStringEnd
		}
	case escStringEnd:
		if b == '\\' {
			s.state = escGround
		} else {
			s.state = escString
		}
	}
	return false, false
}

// csiFinal handles the final byte of a CSI sequence and reports whether it
// switched to or from the alternate screen (private modes 47, 1047, 1049).
func (s *startupStatus) csiFinal(final byte) bool {
	if final != 'h' && final != 'l' {
		return false
	}
	modes, ok := strings.CutPrefix(string(s.params), "?")
	if !ok {
		return false
	}
	for mode := range strings.SplitSeq(modes, ";") {
		if mode == "47" || mode == "1047" || mode == "1049" {
			on := final == 'h'
			changed := s.alt != on
			s.alt = on
			return changed
		}
	}
	return false
}
