package netbroker

import (
	"fmt"
	"os"
	"sync"
)

// maxLogSize is the size at which the event log moves to path.1, keeping
// at most two generations on disk.
const maxLogSize = 16 << 20

// logFile is an append-only event log rotated at max bytes.
type logFile struct {
	mu   sync.Mutex
	path string
	max  int64
	f    *os.File
	size int64
}

func openLogFile(path string, maxSize int64) (*logFile, error) {
	l := &logFile{path: path, max: maxSize}
	if err := l.open(); err != nil {
		return nil, err
	}
	return l, nil
}

func (l *logFile) open() error {
	f, err := os.OpenFile(l.path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return fmt.Errorf("open log: %w", err)
	}
	info, err := f.Stat()
	if err != nil {
		_ = f.Close()
		return fmt.Errorf("stat log: %w", err)
	}
	l.f, l.size = f, info.Size()
	return nil
}

// Write appends p, first rotating the log when p would push it past max.
// A log left closed by a failed rotation is reopened, so a transient error
// loses only the events written while it lasts.
func (l *logFile) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.f == nil {
		if err := l.open(); err != nil {
			return 0, err
		}
	}
	if l.size > 0 && l.size+int64(len(p)) > l.max {
		if err := l.rotate(); err != nil {
			return 0, err
		}
	}
	n, err := l.f.Write(p)
	l.size += int64(n)
	if err != nil {
		return n, fmt.Errorf("write log: %w", err)
	}
	return n, nil
}

func (l *logFile) rotate() error {
	err := l.f.Close()
	l.f = nil
	if err != nil {
		return fmt.Errorf("close log: %w", err)
	}
	if err := os.Rename(l.path, l.path+".1"); err != nil {
		return fmt.Errorf("rotate log: %w", err)
	}
	return l.open()
}

func (l *logFile) Close() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.f == nil {
		return nil
	}
	err := l.f.Close()
	l.f = nil
	if err != nil {
		return fmt.Errorf("close log: %w", err)
	}
	return nil
}
