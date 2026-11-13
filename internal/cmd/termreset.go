package cmd

import "io"

const (
	terminalModesCommon = "\x1b[?1000l\x1b[?1002l\x1b[?1003l\x1b[?1005l\x1b[?1006l\x1b[?1015l" +
		"\x1b[?1004l\x1b[?2004l\x1b[?1l\x1b>" +
		"\x1b[<99u\x1b[>4;0m"
	terminalModesTail = "\x1b[?25h\x1b[0m"
	terminalModeReset = terminalModesCommon + "\x1b[?1049l" + terminalModesTail
)

func resetTerminalModes(w io.Writer) {
	_, _ = io.WriteString(w, terminalModeReset)
}

// resetSessionTerminalModes turns off the terminal modes an agent may leave on
// when its session detaches or exits. Leaving the alternate screen (\x1b[?1049l)
// also restores the cursor position saved when entering it (DECRC), so it is
// sent only when the agent is still on the alternate screen; otherwise DECRC
// would jump the cursor back over error output the agent wrote on the main
// screen after leaving the alternate screen itself.
func resetSessionTerminalModes(w io.Writer, onAltScreen bool) {
	if onAltScreen {
		_, _ = io.WriteString(w, terminalModeReset)
		return
	}
	_, _ = io.WriteString(w, terminalModesCommon+terminalModesTail)
}
