package cmd

import "io"

// terminalModeReset switches off terminal features a full-screen TUI may
// have enabled and not turned off because it was killed (for example when
// its VM stops): mouse reporting in all encodings, focus events, bracketed
// paste, application cursor keys and keypad, kitty keyboard flags and
// xterm modifyOtherKeys. It then leaves the alternate screen, shows the
// cursor and resets colours. Terminals ignore sequences they do not support,
// and each one is a no-op when the feature is already off.
const terminalModeReset = "\x1b[?1000l\x1b[?1002l\x1b[?1003l\x1b[?1005l\x1b[?1006l\x1b[?1015l" +
	"\x1b[?1004l\x1b[?2004l\x1b[?1l\x1b>" +
	"\x1b[<99u\x1b[>4;0m" +
	"\x1b[?1049l\x1b[?25h\x1b[0m"

// resetTerminalModes writes terminalModeReset to w.
func resetTerminalModes(w io.Writer) {
	_, _ = io.WriteString(w, terminalModeReset)
}
