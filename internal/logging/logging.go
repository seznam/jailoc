package logging

import (
	"io"
	"log/slog"
	"os"
	"path/filepath"
)

const maxLogSize = 5 * 1024 * 1024 // 5MB

// Init sets up file-based slog logging at ~/.cache/jailoc/jailoc.log.
// On any failure it falls back to a discard handler and returns nil.
// Never errors, never panics, never writes to stderr/stdout.
func Init() error {
	logPath := logFilePath()

	if err := os.MkdirAll(filepath.Dir(logPath), 0o750); err != nil {
		setDiscard()
		return nil
	}

	// Rotate if over size cap
	if info, err := os.Stat(logPath); err == nil && info.Size() > maxLogSize {
		_ = os.Rename(logPath, logPath+".1")
	}

	f, err := os.OpenFile(logPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o640) //nolint:gosec // G304: path derived from os.UserHomeDir(), not user input
	if err != nil {
		setDiscard()
		return nil
	}

	h := slog.NewTextHandler(f, &slog.HandlerOptions{Level: slog.LevelDebug})
	slog.SetDefault(slog.New(h))
	return nil
}

func logFilePath() string {
	home, err := os.UserHomeDir()
	if err != nil {
		home = os.TempDir()
	}
	return filepath.Join(home, ".cache", "jailoc", "jailoc.log")
}

func setDiscard() {
	slog.SetDefault(slog.New(slog.NewTextHandler(io.Discard, nil)))
}
