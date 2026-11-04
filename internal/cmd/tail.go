package cmd

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"syscall"
	"time"
)

const tailPoll = 250 * time.Millisecond

// logView selects what tailLog prints.
type logView struct {
	lines  int  // last N shown events; negative prints all
	follow bool // keep printing new events
	json   bool // print shown events as their raw JSON lines
}

// logFile is a log to tail: path, or name inside the directory root when root
// is set. Logs the guest can write are read through root, so a link the
// guest plants cannot make jailoc print another host file.
type logFile struct {
	root string
	path string
}

func (l logFile) open() (*os.File, error) {
	if l.root == "" {
		return os.Open(l.path) //nolint:gosec // G304: jailoc state file
	}
	r, err := os.OpenRoot(l.root)
	if err != nil {
		return nil, err
	}
	defer func() { _ = r.Close() }()
	return r.Open(l.path)
}

func (l logFile) String() string {
	if l.root == "" {
		return l.path
	}
	return filepath.Join(l.root, l.path)
}

// tailLog prints the last v.lines events from files (oldest first) that
// render to a non-empty line, then follows the last file when v.follow is
// set. render sees every event in order, so it can keep state such as DNS
// names. Missing files count as empty.
func tailLog(ctx context.Context, w io.Writer, files []logFile, v logView, render func([]byte) string) error {
	var shown [][]byte
	keep := func(raw []byte) {
		text := render(raw)
		if text == "" || v.lines == 0 {
			return
		}
		out := raw
		if !v.json {
			out = []byte(text)
		}
		shown = append(shown, append(append([]byte(nil), out...), '\n'))
		if v.lines > 0 && len(shown) > v.lines {
			shown = shown[1:]
		}
	}
	var last *follower
	defer func() {
		if last != nil {
			last.close()
		}
	}()
	for i, path := range files {
		fl := &follower{path: path}
		err := fl.poll(keep)
		if i == len(files)-1 && v.follow {
			last = fl
		} else {
			fl.close()
		}
		if err != nil {
			return err
		}
	}
	for _, l := range shown {
		if _, err := w.Write(l); err != nil {
			return err
		}
	}
	if last == nil {
		return nil
	}
	emit := func(raw []byte) {
		text := render(raw)
		if text == "" {
			return
		}
		if v.json {
			_, _ = w.Write(append(append([]byte(nil), raw...), '\n'))
			return
		}
		_, _ = fmt.Fprintln(w, text)
	}
	t := time.NewTicker(tailPoll)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-t.C:
		}
		if err := last.poll(emit); err != nil {
			return err
		}
	}
}

// follower reads the complete lines appended to a log. It keeps the file it
// read open, so lines written just before the log is rotated are still read
// from the old file before the new one is started.
type follower struct {
	path   logFile
	f      *os.File
	offset int64
}

func (fl *follower) close() {
	if fl.f != nil {
		_ = fl.f.Close() // read-only
		fl.f = nil
	}
}

// poll calls fn for every complete line appended since the last poll. When
// path names another file than the one open, because the log was rotated or
// replaced, poll finishes the open file and reads the new one from the start.
func (fl *follower) poll(fn func([]byte)) error {
	if fl.f != nil {
		n, err := readLines(fl.f, fl.offset, fn)
		if err != nil {
			return fmt.Errorf("read %s: %w", fl.path, err)
		}
		fl.offset = n
	}
	f, err := fl.path.open()
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("open %s: %w", fl.path, err)
	}
	if fl.f != nil {
		same, err := sameFile(fl.f, f)
		if err != nil || same {
			_ = f.Close() // read-only
			if err != nil {
				return fmt.Errorf("stat %s: %w", fl.path, err)
			}
			return nil
		}
	}
	fl.close()
	fl.f, fl.offset = f, 0
	n, err := readLines(f, 0, fn)
	if err != nil {
		return fmt.Errorf("read %s: %w", fl.path, err)
	}
	fl.offset = n
	return nil
}

func sameFile(a, b *os.File) (bool, error) {
	ai, err := a.Stat()
	if err != nil {
		return false, err
	}
	bi, err := b.Stat()
	if err != nil {
		return false, err
	}
	return os.SameFile(ai, bi), nil
}

// readLines calls fn for every complete line of f after offset and returns
// the offset after the last complete line. It reads from the start when f
// is shorter than offset, because the log was truncated.
func readLines(f *os.File, offset int64, fn func([]byte)) (int64, error) {
	info, err := f.Stat()
	if err != nil {
		return offset, err
	}
	if info.Size() < offset {
		offset = 0
	}
	if _, err := f.Seek(offset, io.SeekStart); err != nil {
		return offset, err
	}
	r := bufio.NewReaderSize(f, 64<<10)
	for {
		line, err := r.ReadBytes('\n')
		if len(line) > 0 && line[len(line)-1] == '\n' {
			offset += int64(len(line))
			if l := bytes.TrimSpace(line); len(l) > 0 {
				fn(l)
			}
		}
		if err != nil {
			if errors.Is(err, io.EOF) {
				return offset, nil
			}
			return offset, err
		}
	}
}

// readRegular reads name inside root when it is a regular file of at most
// maxSize bytes. The guest can write below root, so the file is opened
// without blocking (a FIFO would block a plain open) and read with a bound.
func readRegular(root *os.Root, name string, maxSize int64) ([]byte, error) {
	f, err := root.OpenFile(name, os.O_RDONLY|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }() // read-only
	info, err := f.Stat()
	if err != nil {
		return nil, fmt.Errorf("stat %s: %w", name, err)
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("%s is not a regular file", name)
	}
	if info.Size() > maxSize {
		return nil, fmt.Errorf("%s is larger than %d bytes", name, maxSize)
	}
	data, err := io.ReadAll(io.LimitReader(f, maxSize+1))
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", name, err)
	}
	if int64(len(data)) > maxSize {
		return nil, fmt.Errorf("%s is larger than %d bytes", name, maxSize)
	}
	return data, nil
}
