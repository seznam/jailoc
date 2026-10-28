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
	var offset int64
	for i, path := range files {
		n, err := readLines(path, 0, keep)
		if err != nil {
			return err
		}
		if i == len(files)-1 {
			offset = n
		}
	}
	for _, l := range shown {
		if _, err := w.Write(l); err != nil {
			return err
		}
	}
	if !v.follow || len(files) == 0 {
		return nil
	}
	path := files[len(files)-1]
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
		if f, err := path.open(); err == nil {
			if info, err := f.Stat(); err == nil && info.Size() < offset {
				offset = 0 // rotated or recreated
			}
			_ = f.Close()
		}
		n, err := readLines(path, offset, emit)
		if err != nil {
			return err
		}
		offset = n
	}
}

// readLines calls fn for every complete line of path after offset and
// returns the offset after the last complete line.
func readLines(path logFile, offset int64, fn func([]byte)) (int64, error) {
	f, err := path.open()
	if errors.Is(err, fs.ErrNotExist) {
		return offset, nil
	}
	if err != nil {
		return offset, fmt.Errorf("open %s: %w", path, err)
	}
	defer func() { _ = f.Close() }()
	if _, err := f.Seek(offset, io.SeekStart); err != nil {
		return offset, fmt.Errorf("seek %s: %w", path, err)
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
			return offset, fmt.Errorf("read %s: %w", path, err)
		}
	}
}
