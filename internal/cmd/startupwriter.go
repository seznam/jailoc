package cmd

import (
	"bytes"
	"io"
	"sync"
	"time"
)

var startupAltScreenEnable = []byte("\x1b[?1049h")

var (
	_ io.WriteCloser = (*startupWriter)(nil)
	_                = newStartupWriter
)

type startupWriter struct {
	w       io.Writer
	status  io.Writer
	buf     []byte
	overlap []byte
	ready   bool
	altSeen bool
	timer   *time.Timer
	mu      sync.Mutex
	timeout time.Duration
}

func newStartupWriter(w io.Writer, status io.Writer, timeout time.Duration) *startupWriter {
	sw := &startupWriter{w: w, status: status, timeout: timeout}
	if status == nil {
		sw.ready = true
		return sw
	}

	_, _ = status.Write([]byte("Starting OpenCode...\r\n"))
	sw.timer = time.AfterFunc(timeout, func() {
		sw.mu.Lock()
		defer sw.mu.Unlock()
		sw.activate()
	})

	return sw
}

func (s *startupWriter) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.ready {
		return s.w.Write(p)
	}

	s.buf = append(s.buf, p...)

	combined := append(append([]byte{}, s.overlap...), p...)
	if !s.altSeen && bytes.Contains(combined, startupAltScreenEnable) {
		s.altSeen = true
	}

	if s.altSeen && len(p) > 0 {
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
