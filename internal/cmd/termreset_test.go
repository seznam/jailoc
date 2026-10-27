package cmd

import (
	"bytes"
	"strings"
	"testing"
)

func TestResetTerminalModes(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	resetTerminalModes(&buf)
	out := buf.String()
	for name, seq := range map[string]string{
		"mouse tracking":   "\x1b[?1000l",
		"button motion":    "\x1b[?1002l",
		"any motion":       "\x1b[?1003l",
		"SGR mouse":        "\x1b[?1006l",
		"bracketed paste":  "\x1b[?2004l",
		"focus events":     "\x1b[?1004l",
		"kitty keyboard":   "\x1b[<99u",
		"alternate screen": "\x1b[?1049l",
		"cursor visible":   "\x1b[?25h",
	} {
		if !strings.Contains(out, seq) {
			t.Errorf("reset is missing %s (%q)", name, seq)
		}
	}
	if !strings.HasSuffix(out, "\x1b[0m") {
		t.Errorf("reset does not end with an SGR reset: %q", out)
	}
}
