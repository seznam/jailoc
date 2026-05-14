package cmd

import (
	"bufio"
	"bytes"
	"fmt"
	"io"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

var startupAltScreenEnable = []byte("\x1b[?1049h")

const (
	activateMinVisible = 1       // visible (non-escape) characters after alt-screen
	maxBufferSize      = 1 << 20 // defensive cap on buffered data (1 MiB)
)

var _ io.Writer = (*startupWriter)(nil)

type startupWriter struct {
	w              io.Writer
	status         io.Writer
	logReader      io.Reader
	logCancel      func()
	logDone        chan struct{}
	buf            []byte
	overlap        []byte
	ready          bool
	altSeen        bool
	postAltVisible int
	inEscape       bool
	escType        byte
	timer          *time.Timer
	mu             sync.Mutex
	timeout        time.Duration
}

func newStartupWriter(w io.Writer, status io.Writer, timeout time.Duration, logReader io.Reader, logCancel func()) *startupWriter {
	sw := &startupWriter{w: w, status: status, timeout: timeout, logReader: logReader, logCancel: logCancel}
	if status == nil {
		sw.ready = true
		return sw
	}

	_, _ = status.Write([]byte("Starting OpenCode..."))
	sw.timer = time.AfterFunc(timeout, func() {
		sw.mu.Lock()
		defer sw.mu.Unlock()
		if sw.ready {
			return
		}
		_, _ = fmt.Fprintf(sw.status, "\x1b[2K\rOpenCode is taking unusually long — run 'jailoc logs' to check")
	})
	if logReader != nil {
		sw.logDone = make(chan struct{})
		go sw.readLogs()
	}

	return sw
}

func (s *startupWriter) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.ready {
		return s.w.Write(p)
	}

	s.buf = append(s.buf, p...)

	// Buffer cap exceeded — activate() flushes s.buf (which already
	// includes the current p via the append above) to s.w and marks
	// s.ready=true so subsequent Writes bypass buffering.
	if len(s.buf) > maxBufferSize {
		s.activate()
		return len(p), nil
	}

	if !s.altSeen {
		combined := append(append([]byte{}, s.overlap...), p...)
		if idx := bytes.Index(combined, startupAltScreenEnable); idx >= 0 {
			s.altSeen = true
			postAltStart := idx + len(startupAltScreenEnable) - len(s.overlap)
			if postAltStart < 0 {
				postAltStart = 0
			}
			if postAltStart < len(p) {
				s.postAltVisible += s.countVisibleIn(p[postAltStart:])
			}
		}
	} else {
		s.postAltVisible += s.countVisibleIn(p)
	}

	if s.altSeen && s.postAltVisible >= activateMinVisible {
		s.activate()
	}

	if len(p) >= len(startupAltScreenEnable) {
		s.overlap = append(s.overlap[:0], p[len(p)-len(startupAltScreenEnable):]...)
	} else {
		s.overlap = append(s.overlap, p...)
		if len(s.overlap) > len(startupAltScreenEnable) {
			s.overlap = s.overlap[len(s.overlap)-len(startupAltScreenEnable):]
		}
	}

	return len(p), nil
}

func (s *startupWriter) activate() {
	if s.ready {
		return
	}

	logCancel := s.logCancel
	s.logCancel = nil
	logDone := s.logDone

	if logCancel != nil {
		logCancel()
	}

	if logDone != nil {
		s.mu.Unlock()
		select {
		case <-logDone:
		case <-time.After(time.Second):
		}
		s.mu.Lock()
		if s.ready {
			return
		}
	}

	s.ready = true
	if s.timer != nil {
		s.timer.Stop()
	}

	if s.status != nil {
		_, _ = s.status.Write([]byte("\x1b[2K\r"))
	}

	if len(s.buf) > 0 {
		_, _ = s.w.Write(s.buf)
		s.buf = nil
	}
}

func (s *startupWriter) countVisibleIn(p []byte) int {
	n := 0
	for _, b := range p {
		switch {
		case s.inEscape && s.escType == 0:
			if b == '[' || b == ']' {
				s.escType = b
			} else {
				s.inEscape = false
			}
		case s.inEscape && s.escType == '[':
			if b >= 0x40 && b <= 0x7E {
				s.inEscape = false
			}
		case s.inEscape && s.escType == ']':
			switch b {
			case 0x07:
				s.inEscape = false
			case 0x1b:
				s.escType = 0
			}
		case b == 0x1b:
			s.inEscape = true
			s.escType = 0
		case b >= 0x20 && b <= 0x7E:
			n++
		}
	}
	return n
}

func (s *startupWriter) Close() {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.activate()
}

func (s *startupWriter) readLogs() {
	defer close(s.logDone)

	scanner := bufio.NewScanner(s.logReader)
	var lastUpdate time.Time

	for scanner.Scan() {
		msg := extractLogfmtMsg(scanner.Text())
		if msg == "" {
			continue
		}

		if utf8.RuneCountInString(msg) > 80 {
			runes := []rune(msg)
			msg = string(runes[:79]) + "…"
		}

		s.mu.Lock()
		if s.ready {
			s.mu.Unlock()
			break
		}
		if time.Since(lastUpdate) >= 100*time.Millisecond {
			_, _ = fmt.Fprintf(s.status, "\x1b[2K\r%s", msg)
			lastUpdate = time.Now()
		}
		s.mu.Unlock()
	}
}

// extractLogfmtMsg extracts the value of the msg= field from a logfmt-formatted line.
// Returns empty string if no msg= field is found or the value is empty.
// Handles both quoted (msg="text here") and unquoted (msg=connecting) values.
// Note: backslash-escaped quotes inside quoted values are not handled.
func extractLogfmtMsg(line string) string {
	const key = "msg="

	_, rest, found := strings.Cut(line, key)
	if !found {
		return ""
	}
	if len(rest) == 0 {
		return ""
	}

	if rest[0] == '"' {
		end := strings.Index(rest[1:], "\"")
		if end < 0 {
			return rest[1:]
		}
		return rest[1 : end+1]
	}

	value, _, _ := strings.Cut(rest, " ")
	return value
}
