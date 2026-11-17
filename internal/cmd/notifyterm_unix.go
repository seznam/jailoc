//go:build darwin || linux

package cmd

import (
	"bytes"
	"errors"
	"fmt"
	"strings"

	"golang.org/x/sys/unix"
)

// errNotifyDropped marks a notification left out on purpose: the terminal
// was busy, not reading or focused again. Such drops are expected and not
// failures of the transport.
var errNotifyDropped = errors.New("notification dropped")

// notifyTerminal writes notifications to the user's terminal through a file
// description of its own that does not block, so a write never waits for a
// terminal that stopped reading and needs no goroutine to give up on: it
// takes what fits, or nothing. Agent output keeps its blocking standard
// output, whose file description other processes may share.
type notifyTerminal struct {
	fd int
}

// openNotifyTerminal opens the controlling terminal for notifications after
// checking that standard output, fd stdout, is that terminal.
func openNotifyTerminal(stdout int) (*notifyTerminal, error) {
	if _, err := unix.IoctlGetInt(stdout, unix.TIOCGPGRP); err != nil {
		return nil, fmt.Errorf("standard output is not the controlling terminal: %w", err)
	}
	fd, err := unix.Open("/dev/tty", unix.O_WRONLY|unix.O_NOCTTY|unix.O_NONBLOCK|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, fmt.Errorf("open /dev/tty: %w", err)
	}
	return &notifyTerminal{fd: fd}, nil
}

// write writes p without waiting. A terminal with no room fails it with an
// error wrapping errNotifyDropped; one with room for less takes part of p.
func (t *notifyTerminal) write(p []byte) (int, error) {
	for {
		n, err := unix.Write(t.fd, p)
		switch {
		case errors.Is(err, unix.EINTR):
			continue
		case errors.Is(err, unix.EAGAIN):
			return 0, fmt.Errorf("%w: the terminal is not reading", errNotifyDropped)
		case err != nil:
			return 0, fmt.Errorf("write terminal notification: %w", err)
		}
		return n, nil
	}
}

func (t *notifyTerminal) close() error {
	if err := unix.Close(t.fd); err != nil {
		return fmt.Errorf("close /dev/tty: %w", err)
	}
	return nil
}

// notifyKind is what the jailoc TUI plugin reports about the agent.
type notifyKind uint8

const (
	notifyCompleted notifyKind = iota + 1 // the agent finished its work
	notifyInput                           // the agent waits for the user
)

// notifySequences are the private OSC sequences the jailoc TUI plugin writes
// to report the agent's state. Only these exact bytes are recognized.
var notifySequences = []struct {
	seq  []byte
	kind notifyKind
}{
	{[]byte("\x1b]777;jailoc-notify;completed\x07"), notifyCompleted},
	{[]byte("\x1b]777;jailoc-notify;input\x07"), notifyInput},
}

// notifyOSCFilter removes the notifySequences from agent output. A possible
// start of one is held back, also across chunks, until it either completes or
// differs; at most the length of the longest sequence is held.
type notifyOSCFilter struct {
	held []byte
}

// feed returns p without the notifySequences and calls emit for each one.
func (f *notifyOSCFilter) feed(p []byte, emit func(notifyKind)) []byte {
	if len(f.held) == 0 && bytes.IndexByte(p, 0x1b) < 0 {
		return p
	}
	out := make([]byte, 0, len(f.held)+len(p))
	for _, b := range p {
		if len(f.held) == 0 && b != 0x1b {
			out = append(out, b)
			continue
		}
		f.held = append(f.held, b)
		kind, prefix := matchNotifySequence(f.held)
		switch {
		case kind != 0:
			f.held = f.held[:0]
			if emit != nil {
				emit(kind)
			}
		case prefix:
		default:
			// Not a notification: pass on what was held and look at b anew.
			out = append(out, f.held[:len(f.held)-1]...)
			f.held = f.held[:0]
			if b == 0x1b {
				f.held = append(f.held, b)
			} else {
				out = append(out, b)
			}
		}
	}
	return out
}

// matchNotifySequence reports which sequence p is, or whether it starts one.
func matchNotifySequence(p []byte) (kind notifyKind, prefix bool) {
	for _, s := range notifySequences {
		if bytes.HasPrefix(s.seq, p) {
			if len(p) == len(s.seq) {
				return s.kind, false
			}
			prefix = true
		}
	}
	return 0, prefix
}

// enableFocusReports turns on focus reporting (DEC mode 1004) in the user's
// terminal.
const enableFocusReports = "\x1b[?1004h"

// focusOutput follows whether the agent wants focus reports by scanning its
// output. jailoc needs the reports itself while it sends notifications, so
// after a sequence that turns them off, it turns them on again; the reports
// reach the agent only while it wants them. Escape sequences may be split
// across chunks.
type focusOutput struct {
	focus    *sessionFocus
	state    escState
	params   []byte
	overflow bool // the current CSI sequence is longer than maxCSIParams
	saved    bool // the agent's mode 1004 saved by CSI ? 1004 s
}

// feed returns agent output p with enableFocusReports after each sequence
// that turns focus reporting off.
func (o *focusOutput) feed(p []byte) []byte {
	var out []byte
	last := 0
	for i, b := range p {
		if !o.step(b) {
			continue
		}
		if out == nil {
			out = make([]byte, 0, len(p)+len(enableFocusReports))
		}
		out = append(append(out, p[last:i+1]...), enableFocusReports...)
		last = i + 1
	}
	if out == nil {
		return p
	}
	return append(out, p[last:]...)
}

// step scans one byte and reports whether it ended a sequence that may have
// turned focus reporting off.
func (o *focusOutput) step(b byte) bool {
	switch o.state {
	case escGround:
		if b == 0x1b {
			o.state = escStart
		}
	case escStart:
		o.state = escGround
		switch {
		case b == '[':
			o.state, o.params, o.overflow = escCSI, o.params[:0], false
		case b == ']' || b == 'P' || b == 'X' || b == '^' || b == '_':
			o.state = escString
		case b == 0x1b:
			o.state = escStart
		case b >= 0x20 && b <= 0x2f:
			o.state = escInter
		case b == 'c': // RIS resets every mode
			o.focus.setGuestReports(false)
			o.saved = false
			return true
		}
	case escInter:
		switch {
		case b == 0x1b:
			o.state = escStart
		case b < 0x20 || b > 0x2f:
			o.state = escGround
		}
	case escCSI:
		switch {
		case b == 0x1b:
			o.state = escStart
		case b >= 0x40 && b <= 0x7e:
			o.state = escGround
			return o.csi(b)
		case b >= 0x20 && b <= 0x3f && len(o.params) < maxCSIParams:
			o.params = append(o.params, b)
		case b >= 0x20 && b <= 0x3f:
			o.overflow = true
		}
	case escString:
		switch b {
		case 0x07:
			o.state = escGround
		case 0x1b:
			o.state = escStringEnd
		}
	case escStringEnd:
		if b == '\\' {
			o.state = escGround
			return false
		}
		o.state = escStart
		return o.step(b)
	}
	return false
}

// csi handles the final byte of a CSI sequence: set, reset, save or restore
// of private mode 1004, or a soft reset.
func (o *focusOutput) csi(final byte) bool {
	if o.overflow {
		return false
	}
	params := string(o.params)
	if final == 'p' && params == "!" { // DECSTR
		return true
	}
	modes, ok := strings.CutPrefix(params, "?")
	if !ok || !strings.ContainsRune("hlsr", rune(final)) {
		return false
	}
	found := false
	for m := range strings.SplitSeq(modes, ";") {
		found = found || m == "1004"
	}
	if !found {
		return false
	}
	switch final {
	case 'h':
		o.focus.setGuestReports(true)
	case 'l':
		o.focus.setGuestReports(false)
		return true
	case 's':
		o.saved = o.focus.guestReports()
	case 'r':
		o.focus.setGuestReports(o.saved)
		return true
	}
	return false
}
