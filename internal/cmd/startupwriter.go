package cmd

import (
	"bufio"
	"bytes"
	"fmt"
	"io"
	"strings"
	"sync"
	"time"
)

var startupAltScreenEnable = []byte("\x1b[?1049h")

const (
	activateMinBytes = 512     // post-alt-screen bytes before activation
	maxBufferSize    = 1 << 20 // defensive cap on buffered data (1 MiB)
)

var _ io.WriteCloser = (*startupWriter)(nil)

type startupWriter struct {
	w            io.Writer
	status       io.Writer
	logReader    io.Reader
	logCancel    func()
	logDone      chan struct{}
	buf          []byte
	overlap      []byte
	ready        bool
	altSeen      bool
	postAltBytes int
	timer        *time.Timer
	mu           sync.Mutex
	timeout      time.Duration
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
		sw.activate()
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

	if len(s.buf) > maxBufferSize {
		s.activate()
		return len(p), nil
	}

	combined := append(append([]byte{}, s.overlap...), p...)
	if !s.altSeen && bytes.Contains(combined, startupAltScreenEnable) {
		s.altSeen = true
	}

	if s.altSeen {
		s.postAltBytes += len(p)
		if s.postAltBytes >= activateMinBytes {
			s.activate()
		}
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

func (s *startupWriter) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.activate()
	return nil
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

		if len(msg) > 80 {
			msg = msg[:80]
		}

		s.mu.Lock()
		ready := s.ready
		s.mu.Unlock()
		if ready {
			break
		}

		if time.Since(lastUpdate) < 100*time.Millisecond {
			continue
		}

		_, _ = fmt.Fprintf(s.status, "\x1b[2K\r%s", msg)
		lastUpdate = time.Now()
	}
}

// extractLogfmtMsg extracts the value of the msg= field from a logfmt-formatted line.
// Returns empty string if no msg= field is found or the value is empty.
// Handles both quoted (msg="text here") and unquoted (msg=connecting) values.
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
