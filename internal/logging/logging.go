package logging

import (
	"io"
	"log/slog"
	"os"
	"path/filepath"
)

const maxLogSize = 5 * 1024 * 1024 // 5MB

// logFile tracks the current log file so repeated Init() calls do not leak FDs.
var logFile *os.File

// base is the handler Init installed, which Tee keeps writing to.
var base slog.Handler

// Init sets up file-based slog logging at ~/.cache/jailoc/jailoc.log.
// On any failure it falls back to a discard handler and returns nil.
// Never errors, never panics, never writes to stderr/stdout.
func Init() error {
	if logFile != nil {
		_ = logFile.Close()
		logFile = nil
	}

	logPath := logFilePath()

	if err := os.MkdirAll(filepath.Dir(logPath), 0o750); err != nil {
		setDiscard()
		return nil
	}

	// Rotate if over size cap. If rename fails, truncate to enforce the limit.
	if info, err := os.Stat(logPath); err == nil && info.Size() > maxLogSize {
		if err := os.Rename(logPath, logPath+".1"); err != nil {
			_ = os.Truncate(logPath, 0)
		}
	}

	f, err := os.OpenFile(logPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o640) //nolint:gosec // G304: path derived from os.UserHomeDir(), not user input
	if err != nil {
		setDiscard()
		return nil
	}

	logFile = f
	base = slog.NewTextHandler(f, &slog.HandlerOptions{Level: slog.LevelDebug})
	slog.SetDefault(slog.New(base))
	return nil
}

// Tee makes the default logger also write the records at Info level and
// above to w, until the returned function restores the logger before it.
func Tee(w io.Writer) (restore func()) {
	prev := slog.Default()
	h := slog.Handler(slog.NewTextHandler(w, &slog.HandlerOptions{Level: slog.LevelInfo}))
	// Without Init the default logger forwards to the log package, which
	// would forward back to the new default logger.
	if base != nil {
		h = slog.NewMultiHandler(base, h)
	}
	slog.SetDefault(slog.New(h))
	return func() { slog.SetDefault(prev) }
}

func logFilePath() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return filepath.Join(os.TempDir(), "jailoc", "jailoc.log")
	}
	return filepath.Join(home, ".cache", "jailoc", "jailoc.log")
}

func setDiscard() {
	base = slog.NewTextHandler(io.Discard, nil)
	slog.SetDefault(slog.New(base))
}
