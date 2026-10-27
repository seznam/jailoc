package session

import (
	"fmt"
	"slices"
	"strconv"
	"strings"
)

// trackedModes are the DEC private modes replayed to a newly attached
// terminal: cursor keys, cursor visibility, alternate screen, mouse
// reporting in all encodings, focus events and bracketed paste.
var trackedModes = map[int]bool{
	1: true, 25: true, 47: true, 1000: true, 1002: true, 1003: true, 1004: true,
	1005: true, 1006: true, 1015: true, 1016: true, 1047: true, 1049: true, 2004: true,
}

// altScreenModes switch to the alternate screen; at most one is replayed,
// before every other mode.
var altScreenModes = []int{1049, 1047, 47}

const maxSequence = 64

// ModeTracker follows the terminal modes a program enables by scanning its
// output, so they can be restored on a terminal that attaches later.
type ModeTracker struct {
	state     int // 0 ground, 1 after ESC, 2 in CSI
	seq       []byte
	set       map[int]bool // DEC private modes; 25 is tracked as "set" = visible
	keypad    bool         // application keypad (ESC =)
	kitty     []int        // kitty keyboard protocol flag stack
	otherKeys int          // xterm modifyOtherKeys level
}

// NewModeTracker returns a tracker for a terminal in its default state.
func NewModeTracker() *ModeTracker {
	return &ModeTracker{set: map[int]bool{25: true}}
}

// Feed scans program output. Escape sequences may be split across calls.
func (t *ModeTracker) Feed(p []byte) {
	for _, b := range p {
		switch t.state {
		case 0:
			if b == 0x1b {
				t.state = 1
			}
		case 1:
			switch b {
			case '[':
				t.state, t.seq = 2, t.seq[:0]
			case '=':
				t.keypad, t.state = true, 0
			case '>':
				t.keypad, t.state = false, 0
			case 'c':
				t.reset()
				t.state = 0
			case 0x1b:
			default:
				t.state = 0
			}
		case 2:
			switch {
			case b >= 0x40 && b <= 0x7e:
				t.csi(string(t.seq), b)
				t.state = 0
			case b >= 0x20 && b <= 0x3f && len(t.seq) < maxSequence:
				t.seq = append(t.seq, b)
			case b == 0x1b:
				t.state = 1
			default:
				t.state = 0
			}
		}
	}
}

func (t *ModeTracker) reset() {
	*t = ModeTracker{set: map[int]bool{25: true}, seq: t.seq[:0]}
}

func (t *ModeTracker) csi(params string, final byte) {
	switch final {
	case 'h', 'l':
		if rest, ok := strings.CutPrefix(params, "?"); ok {
			for _, s := range strings.Split(rest, ";") {
				if n, err := strconv.Atoi(s); err == nil && trackedModes[n] {
					t.set[n] = final == 'h'
				}
			}
		}
	case 'u':
		t.kittyKeys(params)
	case 'm':
		if rest, ok := strings.CutPrefix(params, ">4"); ok {
			level := 0
			if v, ok := strings.CutPrefix(rest, ";"); ok {
				level, _ = strconv.Atoi(v)
			}
			t.otherKeys = level
		}
	}
}

// kittyKeys follows the kitty keyboard protocol: CSI > flags u pushes,
// CSI < n u pops, CSI = flags ; mode u changes the current flags.
func (t *ModeTracker) kittyKeys(params string) {
	switch {
	case strings.HasPrefix(params, ">"):
		flags, _ := strconv.Atoi(params[1:])
		t.kitty = append(t.kitty, flags)
	case strings.HasPrefix(params, "<"):
		n := 1
		if v, err := strconv.Atoi(params[1:]); err == nil && v > 0 {
			n = v
		}
		t.kitty = t.kitty[:max(0, len(t.kitty)-n)]
	case strings.HasPrefix(params, "="):
		fields := strings.Split(params[1:], ";")
		flags, _ := strconv.Atoi(fields[0])
		mode := 1
		if len(fields) > 1 {
			mode, _ = strconv.Atoi(fields[1])
		}
		if len(t.kitty) == 0 {
			t.kitty = append(t.kitty, 0)
		}
		top := &t.kitty[len(t.kitty)-1]
		switch mode {
		case 2:
			*top |= flags
		case 3:
			*top &^= flags
		default:
			*top = flags
		}
	}
}

// Replay returns escape sequences that put a terminal in its default state
// into the modes tracked so far.
func (t *ModeTracker) Replay() []byte {
	var b strings.Builder
	for _, m := range altScreenModes {
		if t.set[m] {
			fmt.Fprintf(&b, "\x1b[?%dh", m)
			break
		}
	}
	modes := make([]int, 0, len(t.set))
	for m := range t.set {
		modes = append(modes, m)
	}
	slices.Sort(modes)
	for _, m := range modes {
		switch {
		case slices.Contains(altScreenModes, m):
		case m == 25:
			if !t.set[m] {
				b.WriteString("\x1b[?25l")
			}
		case t.set[m]:
			fmt.Fprintf(&b, "\x1b[?%dh", m)
		}
	}
	if t.keypad {
		b.WriteString("\x1b=")
	}
	for _, flags := range t.kitty {
		fmt.Fprintf(&b, "\x1b[>%du", flags)
	}
	if t.otherKeys > 0 {
		fmt.Fprintf(&b, "\x1b[>4;%dm", t.otherKeys)
	}
	return []byte(b.String())
}
