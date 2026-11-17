//go:build darwin || linux

package cmd

import (
	"strings"
	"sync"
)

// focusState is what jailoc knows about the focus of the user's terminal.
type focusState uint8

const (
	focusUnknown focusState = iota // no report yet, or one jailoc could not read
	focusIn
	focusOut
)

// sessionFocus is the focus state of an attached session, shared by the
// goroutines relaying input and output and the notifier. Notifications are
// sent only while the terminal reported that it lost focus; epoch changes
// with every state change, so a notification queued before the user came
// back is dropped.
type sessionFocus struct {
	mu    sync.Mutex
	state focusState
	epoch uint64
	guest bool // the agent enabled focus reporting (DEC mode 1004)
}

func (f *sessionFocus) set(s focusState) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.state != s {
		f.state = s
		f.epoch++
	}
}

// away reports whether the terminal is known to be unfocused, and the epoch.
func (f *sessionFocus) away() (bool, uint64) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.state == focusOut, f.epoch
}

// awayAt reports whether the terminal is still unfocused in epoch, so
// nothing changed since a notification was queued.
func (f *sessionFocus) awayAt(epoch uint64) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.state == focusOut && f.epoch == epoch
}

func (f *sessionFocus) guestReports() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.guest
}

func (f *sessionFocus) setGuestReports(on bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.guest = on
}

// inState is the state of the focusInput parser.
type inState uint8

const (
	inGround    inState = iota
	inEsc               // after ESC
	inCSI               // ESC [
	inSS3               // ESC O: the next byte is the key
	inString            // OSC, DCS, SOS, PM or APC string: a terminal reply
	inStringEsc         // ESC inside a string
	inPaste             // inside a bracketed paste
	inMouse             // the three bytes of an X10 mouse report
)

// pasteEnd ends a bracketed paste.
const pasteEnd = "\x1b[201~"

// focusInput reads the focus reports (CSI I, CSI O) in terminal input and
// removes them unless the agent enabled them. Any other key the user types
// or pastes means the terminal is focused; mouse reports and replies to
// terminal queries do not. Input is forwarded as it arrives, so a report
// split across reads cannot be removed: its prefix was already forwarded.
// The rest is forwarded too, and the report still sets the focus. Until it
// completes, the focus is unknown, which sends no notifications; it stays
// unknown when the bytes turn out to be anything that does not tell.
type focusInput struct {
	focus  *sessionFocus
	state  inState
	params []byte
	escAt  int // where in the current output the escape sequence starts; -1 in earlier input
	pasted int // bytes of pasteEnd matched
	mouse  int // X10 mouse report bytes left
}

// feed returns terminal input p for the agent.
func (in *focusInput) feed(p []byte) []byte {
	out := make([]byte, 0, len(p))
	in.escAt = -1
	for _, b := range p {
		out = in.step(out, b)
	}
	if in.state == inEsc || (in.state == inCSI && len(in.params) == 0) {
		in.focus.set(focusUnknown)
	}
	return out
}

func (in *focusInput) startEscape(out []byte) []byte {
	in.state, in.escAt = inEsc, len(out)
	return append(out, 0x1b)
}

// step handles one input byte and returns out with what to forward of it.
func (in *focusInput) step(out []byte, b byte) []byte {
	switch in.state {
	case inGround:
		if b == 0x1b {
			return in.startEscape(out)
		}
		in.focus.set(focusIn)
	case inEsc:
		switch b {
		case '[':
			in.state, in.params = inCSI, in.params[:0]
		case 'O':
			in.state = inSS3
		case ']', 'P', '_', '^', 'X':
			in.state = inString
		case 0x1b: // the Esc key, then another sequence
			in.focus.set(focusIn)
			return in.startEscape(out)
		default: // Alt with a key, or Esc and a key
			in.focus.set(focusIn)
			in.state = inGround
		}
	case inSS3:
		in.focus.set(focusIn)
		in.state = inGround
	case inCSI:
		return in.csiByte(out, b)
	case inString:
		switch b {
		case 0x07:
			in.state = inGround
		case 0x1b:
			in.state, in.escAt = inStringEsc, len(out)
		}
	case inStringEsc:
		if b != '\\' {
			in.state = inEsc
			return in.step(out, b)
		}
		in.state = inGround
	case inPaste:
		switch b {
		case pasteEnd[in.pasted]:
			if in.pasted++; in.pasted == len(pasteEnd) {
				in.state, in.pasted = inGround, 0
			}
		case 0x1b:
			in.pasted = 1
		default:
			in.pasted = 0
		}
	case inMouse:
		if in.mouse--; in.mouse == 0 {
			in.state = inGround
		}
	}
	return append(out, b)
}

// csiByte handles a byte of a CSI sequence.
func (in *focusInput) csiByte(out []byte, b byte) []byte {
	switch {
	case b == 0x1b:
		in.focus.set(focusIn)
		return in.startEscape(out)
	case b >= 0x20 && b <= 0x3f:
		if len(in.params) < maxCSIParams {
			in.params = append(in.params, b)
		}
		return append(out, b)
	case b < 0x40 || b > 0x7e: // a control key ends the sequence
		in.focus.set(focusIn)
		in.state = inGround
		return in.step(out, b)
	}
	in.state = inGround
	params := string(in.params)
	switch {
	case params == "" && (b == 'I' || b == 'O'):
		return in.report(out, b)
	case params == "" && b == 'M':
		in.state, in.mouse = inMouse, 3
	case params == "200" && b == '~':
		in.focus.set(focusIn)
		in.state = inPaste
	case params != "" && strings.ContainsRune("<?>=", rune(params[0])):
		// An SGR mouse report or a reply to a terminal query.
	default:
		in.focus.set(focusIn)
	}
	return append(out, b)
}

// report handles a focus report ending in b.
func (in *focusInput) report(out []byte, b byte) []byte {
	if b == 'I' {
		in.focus.set(focusIn)
	} else {
		in.focus.set(focusOut)
	}
	if in.escAt < 0 || in.focus.guestReports() {
		// Forwarded whole, or its start went with earlier input.
		return append(out, b)
	}
	return out[:in.escAt]
}
