//go:build darwin || linux

package cmd

import (
	"bytes"
	"context"
	"encoding/base64"
	"os"
	"os/exec"
	"runtime"
	"sync"
	"time"
)

const maxOSC52Len = 2 << 20

type osc52State uint8

const (
	osc52Scanning osc52State = iota
	osc52Raw
	osc52TMUX
)

type osc52Bridge struct {
	copyFn    func([]byte)
	tmux      bool
	state     osc52State
	recent    []byte
	sequence  []byte
	overflow  bool
	escape    bool
	escapeRun int
}

func newOSC52Bridge(copyFn func([]byte), tmux bool) *osc52Bridge {
	return &osc52Bridge{
		copyFn: copyFn,
		tmux:   tmux,
		recent: []byte{},
	}
}

func (b *osc52Bridge) feed(input []byte) []byte {
	output := make([]byte, 0, len(input))
	for _, value := range input {
		output = append(output, value)
		switch b.state {
		case osc52Scanning:
			b.scan(value)
		case osc52Raw:
			if b.capture(value) {
				if decoded, ok := decodeOSC52(b.sequence); ok {
					b.copyFn(decoded)
					if b.tmux {
						output = append(output, tmuxPassthrough(b.sequence)...)
					}
				}
				b.reset()
			}
		case osc52TMUX:
			if b.capture(value) {
				if decoded, ok := decodeTMUXOSC52(b.sequence); ok {
					b.copyFn(decoded)
				}
				b.reset()
			}
		}
	}
	return output
}

func (b *osc52Bridge) scan(value byte) {
	const rawPrefix = "\x1b]52;"
	const tmuxPrefix = "\x1bPtmux;"

	if len(b.recent) == len(tmuxPrefix) {
		copy(b.recent, b.recent[1:])
		b.recent[len(b.recent)-1] = value
	} else {
		b.recent = append(b.recent, value)
	}
	if bytes.HasSuffix(b.recent, []byte(rawPrefix)) {
		b.state = osc52Raw
		b.sequence = append([]byte(nil), rawPrefix...)
		b.recent = b.recent[:0]
		return
	}
	if bytes.HasSuffix(b.recent, []byte(tmuxPrefix)) {
		b.state = osc52TMUX
		b.sequence = append([]byte(nil), tmuxPrefix...)
		b.recent = b.recent[:0]
	}
}

func (b *osc52Bridge) capture(value byte) bool {
	if value == '\x18' || value == '\x1a' {
		b.reset()
		return false
	}
	previousWasEscape := b.escape
	b.escape = value == '\x1b'
	if b.escape {
		b.escapeRun++
	} else if value != '\\' {
		b.escapeRun = 0
	}
	if !b.overflow {
		if len(b.sequence) < maxOSC52Len {
			b.sequence = append(b.sequence, value)
		} else {
			b.sequence = b.sequence[:0]
			b.overflow = true
		}
	}
	if previousWasEscape && value == '\\' {
		terminated := b.state == osc52Raw || b.escapeRun%2 == 1
		b.escapeRun = 0
		return terminated
	}
	return b.state == osc52Raw && (value == '\a' || value == '\x9c')
}

func (b *osc52Bridge) reset() {
	b.state = osc52Scanning
	b.recent = b.recent[:0]
	b.sequence = b.sequence[:0]
	b.overflow = false
	b.escape = false
	b.escapeRun = 0
}

func decodeOSC52(sequence []byte) ([]byte, bool) {
	const prefix = "\x1b]52;"
	if !bytes.HasPrefix(sequence, []byte(prefix)) {
		return nil, false
	}
	body, ok := trimControlTerminator(sequence[len(prefix):])
	if !ok {
		return nil, false
	}
	separator := bytes.IndexByte(body, ';')
	if separator < 0 {
		return nil, false
	}
	selection := body[:separator]
	if len(selection) > 0 && bytes.IndexByte(selection, 'c') < 0 {
		return nil, false
	}
	payload := body[separator+1:]
	if bytes.Equal(payload, []byte("?")) {
		return nil, false
	}
	decoded := make([]byte, base64.StdEncoding.DecodedLen(len(payload)))
	n, err := base64.StdEncoding.Decode(decoded, payload)
	if err != nil {
		return nil, false
	}
	return decoded[:n], true
}

func trimControlTerminator(sequence []byte) ([]byte, bool) {
	switch {
	case len(sequence) >= 1 && (sequence[len(sequence)-1] == '\a' || sequence[len(sequence)-1] == '\x9c'):
		return sequence[:len(sequence)-1], true
	case len(sequence) >= 2 && bytes.HasSuffix(sequence, []byte("\x1b\\")):
		return sequence[:len(sequence)-2], true
	default:
		return nil, false
	}
}

func decodeTMUXOSC52(sequence []byte) ([]byte, bool) {
	const prefix = "\x1bPtmux;"
	if !bytes.HasPrefix(sequence, []byte(prefix)) || !bytes.HasSuffix(sequence, []byte("\x1b\\")) {
		return nil, false
	}
	inner := bytes.ReplaceAll(sequence[len(prefix):len(sequence)-2], []byte("\x1b\x1b"), []byte("\x1b"))
	return decodeOSC52(inner)
}

func tmuxPassthrough(sequence []byte) []byte {
	output := make([]byte, 0, len(sequence)+16)
	output = append(output, "\x1bPtmux;"...)
	for _, value := range sequence {
		if value == '\x1b' {
			output = append(output, '\x1b')
		}
		output = append(output, value)
	}
	return append(output, '\x1b', '\\')
}

type hostClipboardQueue struct {
	mu      sync.Mutex
	closed  bool
	ch      chan []byte
	done    chan struct{}
	started bool
	copyFn  func([]byte)
}

func newHostClipboardQueue(copyFn func([]byte)) *hostClipboardQueue {
	return &hostClipboardQueue{
		ch:     make(chan []byte, 1),
		done:   make(chan struct{}),
		copyFn: copyFn,
	}
}

func (q *hostClipboardQueue) enqueue(data []byte) {
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.closed {
		return
	}
	if !q.started {
		q.started = true
		go func() {
			defer close(q.done)
			for value := range q.ch {
				q.copyFn(value)
			}
		}()
	}
	value := append([]byte(nil), data...)
	select {
	case q.ch <- value:
		return
	default:
	}
	select {
	case <-q.ch:
	default:
	}
	select {
	case q.ch <- value:
	default:
	}
}

func (q *hostClipboardQueue) close() {
	q.mu.Lock()
	if q.closed {
		q.mu.Unlock()
		return
	}
	q.closed = true
	started := q.started
	close(q.ch)
	q.mu.Unlock()
	if !started {
		return
	}
	select {
	case <-q.done:
	case <-time.After(2 * time.Second):
	}
}

type clipboardCmd struct {
	name string
	args []string
}

const gtkClipboardScript = "import sys, gi\n" +
	"gi.require_version('Gtk', '3.0')\n" +
	"gi.require_version('Gdk', '3.0')\n" +
	"from gi.repository import Gtk, Gdk\n" +
	"if not Gtk.init_check()[0]:\n" +
	"    sys.exit(1)\n" +
	"text = sys.stdin.buffer.read().decode('utf-8', 'replace')\n" +
	"for name in ('CLIPBOARD', 'PRIMARY'):\n" +
	"    cb = Gtk.Clipboard.get(Gdk.Atom.intern(name, False))\n" +
	"    cb.set_text(text, -1)\n" +
	"    cb.store()\n" +
	"while Gtk.events_pending():\n" +
	"    Gtk.main_iteration()\n"

func copyToHostClipboard(data []byte) {
	for _, c := range hostClipboardCommands(runtime.GOOS, os.Getenv, exec.LookPath) {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		cmd := exec.CommandContext(ctx, c.name, c.args...) //nolint:gosec // G204: fixed clipboard tool resolved from PATH
		cmd.Stdin = bytes.NewReader(data)
		cmd.Stdout = nil
		cmd.Stderr = nil
		if runtime.GOOS == "darwin" && os.Getenv("LANG") == "" {
			cmd.Env = append(os.Environ(), "LANG=en_US.UTF-8")
		}
		_ = cmd.Run()
		cancel()
	}
}

func hostClipboardCommands(
	goos string,
	getenv func(string) string,
	lookPath func(string) (string, error),
) []clipboardCmd {
	if goos == "darwin" {
		return []clipboardCmd{{name: "pbcopy"}}
	}
	var cmds []clipboardCmd
	if getenv("WAYLAND_DISPLAY") != "" {
		if path, err := lookPath("wl-copy"); err == nil {
			cmds = append(cmds,
				clipboardCmd{name: path},
				clipboardCmd{name: path, args: []string{"--primary"}},
			)
		}
	}
	if getenv("DISPLAY") != "" {
		if path, err := lookPath("xclip"); err == nil {
			cmds = append(cmds,
				clipboardCmd{name: path, args: []string{"-selection", "clipboard"}},
				clipboardCmd{name: path, args: []string{"-selection", "primary"}},
			)
		} else if path, err := lookPath("xsel"); err == nil {
			cmds = append(cmds,
				clipboardCmd{name: path, args: []string{"--clipboard", "--input"}},
				clipboardCmd{name: path, args: []string{"--primary", "--input"}},
			)
		}
	}
	if len(cmds) == 0 && (getenv("WAYLAND_DISPLAY") != "" || getenv("DISPLAY") != "") {
		if path, err := lookPath("python3"); err == nil {
			cmds = append(cmds, clipboardCmd{name: path, args: []string{"-c", gtkClipboardScript}})
		}
	}
	return cmds
}
