// Package session keeps agent processes of backend "vm" workspaces running
// independently of the terminal that started them.
//
// A holder process owns the agent's pseudo-terminal and a Unix socket. A
// client (jailoc on the host, through a relay in the guest) attaches over
// the socket with a framed protocol: the agent's output is forwarded byte
// for byte, input and window sizes go back. When a client attaches, the
// holder replays the terminal modes the agent enabled (mouse reporting,
// alternate screen, bracketed paste, keyboard protocols) and makes the agent
// redraw by changing the window size, so a full-screen program reappears
// exactly. Detaching leaves the agent running; a second client takes the
// session over from the first.
package session

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
)

// Frame types. Client to holder: Hello, Data, Resize. Holder to client:
// Data, Exit, Kicked. Info carries the session ID from the guest's session
// command to the client before the holder's stream starts.
const (
	FrameInfo   byte = 'I'
	FrameHello  byte = 'H'
	FrameData   byte = 'D'
	FrameResize byte = 'R'
	FrameExit   byte = 'X'
	FrameKicked byte = 'K'
)

const maxFrame = 1 << 20

// Frame is one protocol message.
type Frame struct {
	Type    byte
	Payload []byte
}

// WriteFrame writes f as type, big-endian length and payload.
func WriteFrame(w io.Writer, f Frame) error {
	if len(f.Payload) > maxFrame {
		return fmt.Errorf("session frame of %d bytes exceeds %d", len(f.Payload), maxFrame)
	}
	buf := make([]byte, 5+len(f.Payload))
	buf[0] = f.Type
	binary.BigEndian.PutUint32(buf[1:5], uint32(len(f.Payload))) //nolint:gosec // G115: bounded by maxFrame
	copy(buf[5:], f.Payload)
	_, err := w.Write(buf)
	return err
}

// ReadFrame reads one frame written by WriteFrame.
func ReadFrame(r io.Reader) (Frame, error) {
	var hdr [5]byte
	if _, err := io.ReadFull(r, hdr[:]); err != nil {
		return Frame{}, err
	}
	n := binary.BigEndian.Uint32(hdr[1:])
	if n > maxFrame {
		return Frame{}, fmt.Errorf("session frame of %d bytes exceeds %d", n, maxFrame)
	}
	f := Frame{Type: hdr[0], Payload: make([]byte, n)}
	if _, err := io.ReadFull(r, f.Payload); err != nil {
		if errors.Is(err, io.EOF) {
			err = io.ErrUnexpectedEOF
		}
		return Frame{}, err
	}
	return f, nil
}

// SizeFrame encodes a window size for Hello and Resize frames.
func SizeFrame(t byte, rows, cols uint16) Frame {
	p := make([]byte, 4)
	binary.BigEndian.PutUint16(p[0:2], rows)
	binary.BigEndian.PutUint16(p[2:4], cols)
	return Frame{Type: t, Payload: p}
}

// ParseSize decodes the payload of a Hello or Resize frame.
func ParseSize(p []byte) (rows, cols uint16, err error) {
	if len(p) != 4 {
		return 0, 0, fmt.Errorf("window size payload of %d bytes", len(p))
	}
	return binary.BigEndian.Uint16(p[0:2]), binary.BigEndian.Uint16(p[2:4]), nil
}

// ExitFrame reports the agent's exit status.
func ExitFrame(code int) Frame {
	p := make([]byte, 4)
	binary.BigEndian.PutUint32(p, uint32(int32(code))) //nolint:gosec // G115: exit statuses fit in int32
	return Frame{Type: FrameExit, Payload: p}
}

// ParseExit decodes the payload of an Exit frame.
func ParseExit(p []byte) (int, error) {
	if len(p) != 4 {
		return 0, fmt.Errorf("exit payload of %d bytes", len(p))
	}
	return int(int32(binary.BigEndian.Uint32(p))), nil //nolint:gosec // G115: round-trips ExitFrame
}
