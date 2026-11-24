package vm

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/seznam/jailoc/internal/guest"
	"github.com/stacklok/go-microvm/runner"
)

const (
	// ConsoleLogMax is the size cap (1 MiB) at which console.log rotates to
	// console.log.1 and console.log.1 to console.log.2.
	ConsoleLogMax = 1 << 20
	// ConsoleLogRotations is the number of rotated console log backups kept on disk.
	ConsoleLogRotations = 2
	// ConsoleMaxLine is the maximum length of a single console line (16 KiB,
	// excluding the trailing '\n'). Longer lines are dropped so binary noise
	// cannot evict boot and OOM output.
	ConsoleMaxLine = 16 << 10
)

// ConsoleLogPath returns the path of the guest console log in stateDir.
func ConsoleLogPath(stateDir string) string {
	return filepath.Join(stateDir, "microvm", "console.log")
}

// ConsoleLogPaths returns the rotated and current console log paths in
// chronological order (oldest first: console.log.2, console.log.1, console.log).
func ConsoleLogPaths(stateDir string) []string {
	base := ConsoleLogPath(stateDir)
	paths := make([]string, 0, ConsoleLogRotations+1)
	for i := ConsoleLogRotations; i >= 1; i-- {
		paths = append(paths, base+"."+strconv.Itoa(i))
	}
	return append(paths, base)
}

// ResetConsoleLogs removes console.log and its rotated backups in stateDir.
func ResetConsoleLogs(stateDir string) error {
	for _, p := range ConsoleLogPaths(stateDir) {
		if err := os.Remove(p); err != nil && !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("reset vm console log: %w", err)
		}
	}
	return nil
}

// consolePipePath returns the FIFO path the VM runner opens in place of
// console.log; the host consoleWriter drains it into console.log.
func consolePipePath(consoleLog string) string {
	return consoleLog + ".pipe"
}

// consoleRedactor holds the workspace secret values to redact from console
// output. Values added by later reloads are merged with earlier ones so
// secrets still held by running sessions stay redacted.
type consoleRedactor struct {
	mu     sync.RWMutex
	values []string
}

func newConsoleRedactor(initial []string) *consoleRedactor {
	r := &consoleRedactor{}
	r.Add(initial)
	return r
}

func (r *consoleRedactor) Add(values []string) {
	if r == nil || len(values) == 0 {
		return
	}
	r.mu.Lock()
	r.values = guest.MergeSecretValues(r.values, values)
	r.mu.Unlock()
}

func (r *consoleRedactor) Redact(line string) string {
	var vals []string
	if r != nil {
		r.mu.RLock()
		vals = r.values
		r.mu.RUnlock()
	}
	return guest.RedactString(line, vals)
}

// consoleWriter writes guest console output to path, capping each file at max
// bytes with rotations (.1 .. .rotations, mode 0600), dropping single lines
// longer than maxLine bytes, and redacting secret values and credential
// patterns before writing to disk.
type consoleWriter struct {
	mu        sync.Mutex
	path      string
	max       int64
	rotations int
	maxLine   int
	redactor  *consoleRedactor
	f         *os.File
	size      int64
	line      bytes.Buffer
	dropping  bool
}

func newConsoleWriter(path string, redactor *consoleRedactor) *consoleWriter {
	return &consoleWriter{
		path:      path,
		max:       ConsoleLogMax,
		rotations: ConsoleLogRotations,
		maxLine:   ConsoleMaxLine,
		redactor:  redactor,
	}
}

// Write buffers partial lines, drops lines exceeding maxLine, redacts complete
// lines, and appends them to the rotating console log file.
func (w *consoleWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()

	total := len(p)
	for len(p) > 0 {
		nl := bytes.IndexByte(p, '\n')
		if nl < 0 {
			w.bufferChunk(p)
			break
		}
		w.bufferChunk(p[:nl])
		p = p[nl+1:]
		if w.dropping {
			w.line.Reset()
			w.dropping = false
			continue
		}
		line := w.line.String()
		w.line.Reset()
		if err := w.writeLine(line + "\n"); err != nil {
			return total - len(p), err
		}
	}
	return total, nil
}

func (w *consoleWriter) bufferChunk(chunk []byte) {
	if w.dropping {
		return
	}
	if w.line.Len()+len(chunk) > w.maxLine {
		w.line.Reset()
		w.dropping = true
		return
	}
	_, _ = w.line.Write(chunk)
}

// Close flushes any buffered partial line (if not oversized) and closes the
// underlying file.
func (w *consoleWriter) Close() error {
	w.mu.Lock()
	defer w.mu.Unlock()

	var writeErr error
	if !w.dropping && w.line.Len() > 0 {
		line := w.line.String()
		w.line.Reset()
		writeErr = w.writeLine(line)
	} else {
		w.line.Reset()
		w.dropping = false
	}
	if w.f == nil {
		return writeErr
	}
	closeErr := w.f.Close()
	w.f = nil
	return errors.Join(writeErr, closeErr)
}

func (w *consoleWriter) open() error {
	if err := os.MkdirAll(filepath.Dir(w.path), 0o700); err != nil {
		return fmt.Errorf("create console log dir: %w", err)
	}
	f, err := os.OpenFile(w.path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600) //nolint:gosec // G304: jailoc-owned state path
	if err != nil {
		return fmt.Errorf("open console log: %w", err)
	}
	info, err := f.Stat()
	if err != nil {
		_ = f.Close()
		return fmt.Errorf("stat console log: %w", err)
	}
	w.f, w.size = f, info.Size()
	return nil
}

func (w *consoleWriter) writeLine(raw string) error {
	redacted := w.redactor.Redact(raw)
	data := []byte(redacted)
	if w.f == nil {
		if err := w.open(); err != nil {
			return err
		}
	}
	if w.size > 0 && w.size+int64(len(data)) > w.max {
		if err := w.rotate(); err != nil {
			return err
		}
	}
	n, err := w.f.Write(data)
	w.size += int64(n)
	if err != nil {
		return fmt.Errorf("write console log: %w", err)
	}
	return nil
}

func (w *consoleWriter) rotate() error {
	err := w.f.Close()
	w.f = nil
	if err != nil {
		return fmt.Errorf("close console log: %w", err)
	}
	for i := w.rotations - 1; i >= 1; i-- {
		src := w.path + "." + strconv.Itoa(i)
		dst := w.path + "." + strconv.Itoa(i+1)
		if err := os.Rename(src, dst); err != nil && !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("rotate console log: %w", err)
		}
	}
	if w.rotations >= 1 {
		if err := os.Rename(w.path, w.path+".1"); err != nil && !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("rotate console log: %w", err)
		}
	}
	return w.open()
}

// startConsolePipe creates a FIFO at consolePipePath(consoleLog), opens it
// O_RDWR|O_NONBLOCK so the runner's open for writing does not block, and
// streams what the runner writes into a consoleWriter for consoleLog. A writer
// end kept open by jailoc stops the reader from seeing EOF before the runner
// opens the pipe. The caller must call wait only after the runner has exited
// (or failed to start): it closes that writer end and waits until the reader
// has drained the pipe into console.log.
func startConsolePipe(consoleLog string, redactor *consoleRedactor) (pipePath string, wait func(), err error) {
	if consoleLog == "" {
		return "", func() {}, nil
	}
	if err := os.MkdirAll(filepath.Dir(consoleLog), 0o700); err != nil {
		return "", nil, fmt.Errorf("create console log dir: %w", err)
	}
	// Pre-create console.log (0600) so callers and tests see the file as soon
	// as the runner starts, even before the first newline arrives.
	f, err := os.OpenFile(consoleLog, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600) //nolint:gosec // G304: jailoc-owned state path
	if err != nil {
		return "", nil, fmt.Errorf("create console log: %w", err)
	}
	_ = f.Close()

	pipePath = consolePipePath(consoleLog)
	_ = os.Remove(pipePath)
	if err := syscall.Mkfifo(pipePath, 0o600); err != nil {
		return "", nil, fmt.Errorf("create console pipe: %w", err)
	}
	rfd, err := syscall.Open(pipePath, syscall.O_RDONLY|syscall.O_NONBLOCK|syscall.O_CLOEXEC, 0)
	if err != nil {
		_ = os.Remove(pipePath)
		return "", nil, fmt.Errorf("open console pipe reader: %w", err)
	}
	wfd, err := syscall.Open(pipePath, syscall.O_WRONLY|syscall.O_NONBLOCK|syscall.O_CLOEXEC, 0)
	if err != nil {
		_ = syscall.Close(rfd)
		_ = os.Remove(pipePath)
		return "", nil, fmt.Errorf("open console pipe writer: %w", err)
	}
	rf := os.NewFile(uintptr(rfd), pipePath)
	wf := os.NewFile(uintptr(wfd), pipePath+"-keeper")

	cw := newConsoleWriter(consoleLog, redactor)
	done := make(chan struct{})
	go func() {
		defer close(done)
		drainConsole(cw, rf)
		_ = rf.Close()
		if err := cw.Close(); err != nil {
			slog.Warn("vm console log write failed", "path", consoleLog, "error", err)
		}
		_ = os.Remove(pipePath)
	}()

	var waitOnce sync.Once
	wait = func() {
		waitOnce.Do(func() {
			_ = wf.Close()
			<-done
		})
	}
	return pipePath, wait, nil
}

// drainConsole copies src to dst until src ends. Once dst fails, for example
// on a full host disk, it reports the error and keeps reading src without
// writing, so the runner's console writes do not fail with EPIPE.
func drainConsole(dst io.Writer, src io.Reader) {
	buf := make([]byte, 32<<10)
	var sinkErr error
	for {
		n, err := src.Read(buf)
		if n > 0 && sinkErr == nil {
			if _, sinkErr = dst.Write(buf[:n]); sinkErr != nil {
				slog.Warn("vm console log write failed, discarding further console output", "error", sinkErr)
			}
		}
		if err != nil {
			return
		}
	}
}

// consoleSocket returns the Unix socket path where the supervisor accepts
// secret values to redact from the console log.
func consoleSocket(stateDir string) string { return filepath.Join(stateDir, "console.sock") }

const (
	// consoleSecretsOK is the supervisor's reply once it applied the values.
	consoleSecretsOK = "ok"
	// consoleSecretsMax caps the JSON list of values the supervisor reads.
	consoleSecretsMax = 16 << 20
)

// SendConsoleSecrets sends secret values to the running VM supervisor at
// stateDir so they are redacted from console.log before reaching disk. If the
// supervisor has no console socket (for example a builder VM or an older
// supervisor), it returns nil.
func SendConsoleSecrets(ctx context.Context, stateDir string, values []string) error {
	if len(values) == 0 {
		return nil
	}
	sock := consoleSocket(stateDir)
	if _, err := os.Stat(sock); errors.Is(err, os.ErrNotExist) {
		return nil
	}
	data, err := json.Marshal(values)
	if err != nil {
		return fmt.Errorf("encode console secrets: %w", err)
	}
	var d net.Dialer
	conn, err := d.DialContext(ctx, "unix", sock)
	if err != nil {
		// Only a missing socket means a supervisor without console redaction;
		// a socket nobody listens on means the redactor is unavailable.
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return fmt.Errorf("connect to console redactor: %w", err)
	}
	defer func() { _ = conn.Close() }()
	if deadline, ok := ctx.Deadline(); ok {
		_ = conn.SetDeadline(deadline)
	} else {
		_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
	}
	if _, err := conn.Write(append(data, '\n')); err != nil {
		return fmt.Errorf("send console secrets: %w", err)
	}
	if cw, ok := conn.(interface{ CloseWrite() error }); ok {
		_ = cw.CloseWrite()
	}
	// The supervisor answers once it has applied the values, so any later
	// guest output is redacted against them. Anything but "ok" means the
	// values are not registered.
	reply, err := io.ReadAll(io.LimitReader(conn, 4<<10))
	if err != nil {
		return fmt.Errorf("read console redactor reply: %w", err)
	}
	if r := strings.TrimSpace(string(reply)); r != consoleSecretsOK {
		return fmt.Errorf("console redactor rejected secrets: %q", r)
	}
	return nil
}

// startConsoleSecretServer listens on consoleSocket(stateDir) and adds
// received secret value lists into redactor.
func startConsoleSecretServer(stateDir string, redactor *consoleRedactor) (func(), error) {
	sock := consoleSocket(stateDir)
	_ = os.Remove(sock)
	ln, err := net.Listen("unix", sock)
	if err != nil {
		return nil, fmt.Errorf("listen console socket: %w", err)
	}
	_ = os.Chmod(sock, 0o600)
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func(conn net.Conn) {
				defer func() { _ = conn.Close() }()
				_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
				var vals []string
				if err := json.NewDecoder(io.LimitReader(conn, consoleSecretsMax)).Decode(&vals); err != nil {
					_, _ = fmt.Fprintf(conn, "decode secrets: %v\n", err)
					return
				}
				redactor.Add(vals)
				_, _ = io.WriteString(conn, consoleSecretsOK+"\n")
			}(c)
		}
	}()
	return func() {
		_ = ln.Close()
		_ = os.Remove(sock)
	}, nil
}

// consoleDrainWait bounds how long IsAlive waits for the console output of a
// runner that has exited to reach console.log.
const consoleDrainWait = 5 * time.Second

// consoleProcessHandle wraps a runner.ProcessHandle and waits for the
// console pipe reader goroutine to finish draining into console.log once the
// runner has exited, whether it was stopped or exited on its own.
type consoleProcessHandle struct {
	runner.ProcessHandle
	wait func()
}

// IsAlive reports whether the runner is still running. Once it has exited,
// IsAlive first lets its last console output (such as a crash diagnostic)
// reach console.log, so a supervisor that returns when the VM exits does not
// lose it.
func (p consoleProcessHandle) IsAlive() bool {
	if p.ProcessHandle.IsAlive() {
		return true
	}
	ctx, cancel := context.WithTimeout(context.Background(), consoleDrainWait)
	defer cancel()
	p.drain(ctx)
	return false
}

// Stop stops the runner and waits, at most until ctx ends, for its console
// output to reach console.log. A runner that is still alive after a failed
// Stop keeps the pipe open, so Stop then returns without waiting.
func (p consoleProcessHandle) Stop(ctx context.Context) error {
	err := p.ProcessHandle.Stop(ctx)
	if err != nil && p.ProcessHandle.IsAlive() {
		return err
	}
	p.drain(ctx)
	return err
}

// drain closes jailoc's end of the console pipe and waits, at most until ctx
// ends, for the reader to write what the exited runner left in it.
func (p consoleProcessHandle) drain(ctx context.Context) {
	if p.wait == nil {
		return
	}
	drained := make(chan struct{})
	go func() {
		p.wait()
		close(drained)
	}()
	select {
	case <-drained:
	case <-ctx.Done():
	}
}
