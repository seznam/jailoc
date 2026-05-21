package logging

import (
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"testing"
)

func cleanupLogFile(t *testing.T) {
	t.Helper()
	t.Cleanup(func() {
		if logFile != nil {
			_ = logFile.Close()
			logFile = nil
		}
	})
}

func TestInitCreatesFile(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	cleanupLogFile(t)

	if err := Init(); err != nil {
		t.Fatalf("Init() error = %v, want nil", err)
	}

	info, err := os.Stat(logFilePath())
	if err != nil {
		t.Fatalf("os.Stat(%q) error = %v", logFilePath(), err)
	}

	if got, want := info.Mode().Perm(), os.FileMode(0o640); got != want {
		t.Fatalf("log file permissions = %v, want %v", got, want)
	}
}

func TestInitRotatesLargeFile(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	cleanupLogFile(t)

	logPath := logFilePath()
	if err := os.MkdirAll(filepath.Dir(logPath), 0o750); err != nil {
		t.Fatalf("os.MkdirAll(%q) error = %v", filepath.Dir(logPath), err)
	}

	if err := os.WriteFile(logPath, bytesOfSize(maxLogSize+1), 0o640); err != nil { //nolint:gosec // G306: 0o640 is intentional log file permission (owner rw, group r)
		t.Fatalf("os.WriteFile(%q) error = %v", logPath, err)
	}

	if err := Init(); err != nil {
		t.Fatalf("Init() error = %v, want nil", err)
	}

	if _, err := os.Stat(logPath + ".1"); err != nil {
		t.Fatalf("rotated backup missing: %v", err)
	}

	info, err := os.Stat(logPath)
	if err != nil {
		t.Fatalf("os.Stat(%q) error = %v", logPath, err)
	}

	if info.Size() >= maxLogSize {
		t.Fatalf("rotated log size = %d, want < %d", info.Size(), maxLogSize)
	}
}

func TestInitGracefulDegradation(t *testing.T) {
	t.Setenv("HOME", "/dev/null")
	cleanupLogFile(t)

	if err := Init(); err != nil {
		t.Fatalf("Init() error = %v, want nil", err)
	}

	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("slog.Debug panicked: %v", r)
		}
	}()

	slog.Debug("test")
}

func TestInitConcurrentWrites(t *testing.T) {
	home := t.TempDir()
	prevHome, hadHome := os.LookupEnv("HOME")
	if err := os.Setenv("HOME", home); err != nil {
		t.Fatalf("os.Setenv(HOME) error = %v", err)
	}
	t.Cleanup(func() {
		if hadHome {
			_ = os.Setenv("HOME", prevHome)
			return
		}
		_ = os.Unsetenv("HOME")
	})
	cleanupLogFile(t)

	if err := Init(); err != nil {
		t.Fatalf("Init() error = %v, want nil", err)
	}

	var wg sync.WaitGroup
	wg.Add(10)
	for i := range 10 {
		go func(i int) {
			defer wg.Done()
			slog.Debug("msg", "i", i)
		}(i)
	}
	wg.Wait()
}

func bytesOfSize(size int) []byte {
	if size <= 0 {
		return []byte{}
	}

	buf := make([]byte, size)
	for i := range buf {
		buf[i] = 'x'
	}
	return buf
}
