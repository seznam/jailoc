//go:build darwin || linux

package cmd

import (
	"fmt"
	"io"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/seznam/jailoc/internal/guest/session"
)

// startupStatus keeps a status line visible while a full-screen agent
// starts. Such agents switch to the alternate screen long before they draw
// their first frame, which would leave the terminal blank. Agent output
// passes through unchanged, so terminal queries the agent sends while it
// starts still reach the terminal; a spinner, the message and the time
// elapsed are drawn in the middle of the screen after each chunk and on
// every tick, until the agent prints visible text or saves its cursor, then
// erased. Spaces do not count as visible: full-screen agents often paint an
// empty frame of spaces long before the first real one. The status line is
// drawn between a cursor save and restore, so it stops once the agent saves
// its own cursor on the alternate screen, which drawing would overwrite.
// Agents that never switch to the alternate screen are left alone.
type startupStatus struct {
	w    io.Writer
	size func() (cols, rows int)
	now  func() time.Time

	mu      sync.Mutex
	msg     string
	started time.Time
	pending bool // the agent has not drawn anything visible yet
	drew    bool // the agent has drawn a visible character
	row     int  // row the status line is drawn on, 0 if not drawn
	alt     bool // the agent is on the alternate screen
	state   escState
	osc     bool   // the current string is an OSC string, which BEL ends
	params  []byte // parameters of the current CSI sequence
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

// scanEvent is what one byte of agent output means for the status line.
type scanEvent int

const (
	scanNone       scanEvent = iota
	scanVisible              // a printable character outside escape sequences
	scanAltChanged           // a switch to or from the alternate screen
	scanSaveDEC              // ESC 7 (DECSC)
	scanSaveSCO              // CSI s (SCOSC)
)

// maxCSIParams bounds the bytes kept from one CSI sequence; private mode
// lists are short, longer sequences are never alternate screen switches.
const maxCSIParams = 32

// spinnerFrames are drawn one per spinnerInterval.
var spinnerFrames = []rune("⠋⠙⠹⠸⠼⠴⠦⠧⠇⠏")

const spinnerInterval = 100 * time.Millisecond

// resetAttrs turns off the text attributes that change how the status line
// looks but keeps the agent's colors, so the status row takes the background
// the agent paints the screen with.
const resetAttrs = "\x1b[22;23;24;25;27;28;29m"

// newStartupStatus returns a startupStatus that writes agent output to w
// and passes it through until start is called. size reports the terminal
// size the status line is centered in.
func newStartupStatus(w io.Writer, size func() (cols, rows int)) *startupStatus {
	return &startupStatus{w: w, size: size, now: time.Now}
}

// start shows msg until the agent draws on the alternate screen.
func (s *startupStatus) start(msg string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.msg, s.started, s.pending = msg, s.now(), true
}

// stop stops drawing the status line, leaving the screen to the agent.
func (s *startupStatus) stop() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.pending = false
}

// altScreen reports whether the agent is currently on the alternate screen.
func (s *startupStatus) altScreen() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.alt
}

// visible reports whether the agent has drawn any visible character.
func (s *startupStatus) visible() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.drew
}

// animate redraws the status line every spinnerInterval until it is no
// longer needed.
func (s *startupStatus) animate() {
	t := time.NewTicker(spinnerInterval)
	defer t.Stop()
	for range t.C {
		if !s.tick() {
			return
		}
	}
}

// tick redraws the status line and reports whether it is still shown.
func (s *startupStatus) tick() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.pending {
		return false
	}
	// Between chunks the agent may be inside an escape sequence; drawing
	// there would corrupt it.
	if s.alt && s.state == escGround {
		if _, err := s.w.Write(s.draw()); err != nil {
			s.pending = false
			return false
		}
	}
	return true
}

// write writes p, agent output, and keeps the status line on the alternate
// screen until the agent draws its first visible character. It continues
// scanning after the status line stops so altScreen and visible stay accurate
// for the lifetime of the session.
func (s *startupStatus) write(p []byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.pending {
		for _, b := range p {
			if s.scan(b) == scanVisible {
				s.drew = true
			}
		}
		_, err := s.w.Write(p)
		return err
	}
	for i, b := range p {
		ev := s.scan(b)
		if ev == scanVisible {
			s.drew = true
		}
		switch {
		case ev == scanAltChanged && !s.alt:
			// The agent left the alternate screen and the status with it.
			s.pending = false
		case ev == scanVisible && s.alt && s.row > 0:
			s.pending = false
			for _, rem := range p[i+1:] {
				if s.scan(rem) == scanVisible {
					s.drew = true
				}
			}
			_, err := s.w.Write(slices.Concat(p[:i], s.erase(), p[i:]))
			return err
		case ev == scanVisible:
			// The agent draws on the main screen, or on the alternate
			// screen before the status line was drawn.
			s.pending = false
		case (ev == scanSaveDEC || ev == scanSaveSCO) && s.alt:
			// Drawing saves and restores the cursor, which would overwrite
			// the agent's save. Erase right after it, restoring what the
			// agent saved.
			s.pending = false
			if s.row > 0 {
				for _, rem := range p[i+1:] {
					if s.scan(rem) == scanVisible {
						s.drew = true
					}
				}
				_, err := s.w.Write(slices.Concat(p[:i+1], s.eraseAfterSave(ev), p[i+1:]))
				return err
			}
		}
		if !s.pending {
			for _, rem := range p[i+1:] {
				if s.scan(rem) == scanVisible {
					s.drew = true
				}
			}
			_, err := s.w.Write(p)
			return err
		}
	}
	// Drawing inside an escape sequence the chunk ends in would corrupt it;
	// the next chunk or tick draws the status instead.
	if s.alt && s.state == escGround {
		p = slices.Concat(p, s.draw())
	}
	_, err := s.w.Write(p)
	return err
}

// draw returns the bytes that draw the status line centered on the screen,
// with the cursor and attributes saved and restored around it, so the
// agent's output is not moved or restyled. A line drawn on another row
// before the terminal was resized is erased.
func (s *startupStatus) draw() []byte {
	cols, rows := s.size()
	elapsed := s.now().Sub(s.started)
	frame := spinnerFrames[int(elapsed/spinnerInterval)%len(spinnerFrames)]
	line := startupLine(frame, s.msg, int(elapsed/time.Second), cols)
	row := max(1, (rows+1)/2)
	col := max(1, (cols-len([]rune(line)))/2+1)

	var b strings.Builder
	b.WriteString("\x1b7" + resetAttrs)
	if s.row > 0 && s.row != row {
		fmt.Fprintf(&b, "\x1b[%d;1H\x1b[2K", s.row)
	}
	fmt.Fprintf(&b, "\x1b[%d;1H\x1b[2K\x1b[%d;%dH\x1b[2m%s\x1b8", row, row, col, line)
	s.row = row
	return []byte(b.String())
}

// erase returns the bytes that erase the status line.
func (s *startupStatus) erase() []byte {
	return fmt.Appendf(nil, "\x1b7%s\x1b[%d;1H\x1b[2K\x1b8", resetAttrs, s.row)
}

// eraseAfterSave returns the bytes that erase the status line right after
// the agent saved its cursor with save, ending in the matching restore so
// the cursor goes back where the agent saved it. CSI s may save only the
// position, so its variant leaves the attributes alone.
func (s *startupStatus) eraseAfterSave(save scanEvent) []byte {
	if save == scanSaveSCO {
		return fmt.Appendf(nil, "\x1b[%d;1H\x1b[2K\x1b[u", s.row)
	}
	return fmt.Appendf(nil, "%s\x1b[%d;1H\x1b[2K\x1b8", resetAttrs, s.row)
}

// startupMessage returns the status shown while an agent titled title
// starts: what it installs first, or starting.
func startupMessage(starting, title string, st session.Started) string {
	// The names come from the guest: keep them from controlling the terminal.
	installs := make([]string, len(st.Install))
	for i, pkg := range st.Install {
		installs[i] = escapeVisible(pkg)
	}
	switch len(installs) {
	case 0:
		return starting
	case 1:
		return fmt.Sprintf("Installing %s for %s…", installs[0], title)
	default:
		return fmt.Sprintf("Installing %d plugins for %s: %s…", len(installs), title, strings.Join(installs, ", "))
	}
}

// startupLine returns the spinner frame, msg and seconds elapsed as one line,
// with msg truncated so the line fits a terminal cols wide. When even the
// spinner and seconds do not fit, it returns the spinner alone, or nothing.
func startupLine(frame rune, msg string, seconds, cols int) string {
	prefix, suffix := string(frame)+" ", fmt.Sprintf(" %ds", seconds)
	if r, avail := []rune(msg), cols-1-len([]rune(prefix))-len(suffix); cols > 0 && len(r) > avail {
		switch {
		case avail < 0 && cols > 1:
			return string(frame)
		case avail < 0:
			return ""
		case avail > 1:
			msg = string(r[:avail-1]) + "…"
		default:
			msg = ""
		}
	}
	return prefix + msg + suffix
}

// scan feeds one byte of agent output through the escape sequence parser
// and reports what it means for the status line.
func (s *startupStatus) scan(b byte) scanEvent {
	switch s.state {
	case escGround:
		if b == 0x1b {
			s.state = escStart
			return scanNone
		}
		if b > 0x20 && b != 0x7f {
			return scanVisible
		}
	case escStart:
		switch {
		case b == '[':
			s.state, s.params = escCSI, s.params[:0]
		case b == ']' || b == 'P' || b == 'X' || b == '^' || b == '_':
			s.state, s.osc = escString, b == ']'
		case b >= 0x20 && b <= 0x2f:
			s.state = escInter
		case b == '7':
			s.state = escGround
			return scanSaveDEC
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
			return s.csiFinal(b)
		case len(s.params) < maxCSIParams:
			s.params = append(s.params, b)
		}
	case escString:
		switch {
		case b == 0x07 && s.osc:
			// BEL ends only OSC; DCS, SOS, PM and APC end with ST.
			s.state = escGround
		case b == 0x1b:
			s.state = escStringEnd
		}
	case escStringEnd:
		if b == '\\' {
			s.state = escGround
		} else {
			s.state = escString
		}
	}
	return scanNone
}

// csiFinal handles the final byte of a CSI sequence: a cursor save, or a
// switch to or from the alternate screen (private modes 47, 1047, 1049).
func (s *startupStatus) csiFinal(final byte) scanEvent {
	if final == 's' && len(s.params) == 0 {
		return scanSaveSCO
	}
	if final != 'h' && final != 'l' {
		return scanNone
	}
	modes, ok := strings.CutPrefix(string(s.params), "?")
	if !ok {
		return scanNone
	}
	for mode := range strings.SplitSeq(modes, ";") {
		if mode == "47" || mode == "1047" || mode == "1049" {
			on := final == 'h'
			changed := s.alt != on
			s.alt = on
			if changed {
				return scanAltChanged
			}
			return scanNone
		}
	}
	return scanNone
}
