//go:build darwin || linux

package session

import (
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"os/signal"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/creack/pty"
)

const (
	startTimeout = 10 * time.Second
	helloTimeout = 5 * time.Second
	writeTimeout = 10 * time.Second
	stopGrace    = 5 * time.Second
	clientQueue  = 512
	nudgeWait    = 500 * time.Millisecond
	nudgeSettle  = 30 * time.Millisecond
)

// Options describe a new session.
type Options struct {
	Name       string   // shown in session lists, for example the program name
	Dir        string   // working directory
	Argv       []string // program and arguments
	Env        []string // complete environment of the program
	Rows, Cols uint16   // initial window size
}

// Create starts a holder for a new session and returns the session ID once
// its socket accepts clients. holder is the command line that runs
// [Serve] for the session ID appended as last argument; it is started in
// its own process session so it outlives the caller.
func Create(stateDir string, opts Options, holder []string) (string, error) {
	if len(opts.Argv) == 0 {
		return "", errors.New("create session: no program")
	}
	if err := os.MkdirAll(stateDir, 0o700); err != nil {
		return "", fmt.Errorf("create session directory: %w", err)
	}
	id, err := newID()
	if err != nil {
		return "", err
	}
	info := &Info{ID: id, Name: opts.Name, Dir: opts.Dir, Argv: opts.Argv, Env: opts.Env, Rows: opts.Rows, Cols: opts.Cols, Created: time.Now().UTC()}
	if err := writeInfo(stateDir, info); err != nil {
		return "", err
	}
	cmd := exec.Command(holder[0], append(holder[1:], id)...) //nolint:gosec // G204: holder command line chosen by jailoc-guest
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		_ = os.Remove(infoPath(stateDir, id))
		return "", fmt.Errorf("start session holder: %w", err)
	}
	exited := make(chan error, 1)
	go func() { exited <- cmd.Wait() }()
	deadline := time.After(startTimeout)
	for {
		if conn, err := net.Dial("unix", socketPath(stateDir, id)); err == nil {
			_ = conn.Close()
			return id, nil
		}
		select {
		case err := <-exited:
			return "", fmt.Errorf("session holder exited before accepting clients: %v", err)
		case <-deadline:
			_ = cmd.Process.Kill()
			return "", errors.New("session holder did not start in time")
		case <-time.After(20 * time.Millisecond):
		}
	}
}

// Attach relays a client's framed stream between in/out and the session's
// socket until either side closes.
func Attach(stateDir, id string, in io.Reader, out io.Writer) error {
	if !validID(id) {
		return fmt.Errorf("invalid session id %q", id)
	}
	conn, err := net.Dial("unix", socketPath(stateDir, id))
	if err != nil {
		return fmt.Errorf("attach session %s: %w", id, err)
	}
	defer func() { _ = conn.Close() }()
	go func() {
		_, _ = io.Copy(conn, in)
		_ = conn.Close()
	}()
	_, err = io.Copy(out, conn)
	if errors.Is(err, net.ErrClosed) {
		err = nil
	}
	return err
}

type client struct {
	conn net.Conn
	out  chan Frame
	once sync.Once
	done chan struct{}
}

func (c *client) close() {
	c.once.Do(func() {
		close(c.done)
		_ = c.conn.Close()
	})
}

// send queues f without blocking; a client that does not keep up is
// disconnected so the program never stalls on its output.
func (c *client) send(f Frame) {
	select {
	case <-c.done:
	case c.out <- f:
	default:
		c.close()
	}
}

func (c *client) writeLoop() {
	for {
		select {
		case <-c.done:
			return
		case f := <-c.out:
			_ = c.conn.SetWriteDeadline(time.Now().Add(writeTimeout))
			if err := WriteFrame(c.conn, f); err != nil || f.Type == FrameExit || f.Type == FrameKicked {
				c.close()
				return
			}
		}
	}
}

type holder struct {
	stateDir string
	info     *Info
	ptmx     *os.File
	mu       sync.Mutex
	modes    *ModeTracker
	current  *client
	rows     uint16
	cols     uint16
	output   atomic.Uint64 // bytes read from the program
}

// Serve runs the holder of session id until its program exits.
func Serve(stateDir, id string) error {
	info, err := readInfo(stateDir, id)
	if err != nil {
		return err
	}
	// Catch rather than ignore: ignored signals stay ignored in the program.
	hup := make(chan os.Signal, 1)
	signal.Notify(hup, syscall.SIGHUP, syscall.SIGPIPE)
	go func() {
		for range hup {
		}
	}()
	// Record the holder PID and catch stop signals before the socket
	// accepts clients: Create returns as soon as it does, and Stop needs
	// the PID. A stop that arrives before the program starts is handled
	// once it runs.
	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGTERM, syscall.SIGINT)
	info.PID = os.Getpid()
	if err := writeInfo(stateDir, info); err != nil {
		return err
	}
	_ = os.Remove(socketPath(stateDir, id))
	ln, err := net.Listen("unix", socketPath(stateDir, id))
	if err != nil {
		return fmt.Errorf("listen on session socket: %w", err)
	}
	defer func() {
		_ = ln.Close()
		_ = os.Remove(socketPath(stateDir, id))
		_ = os.Remove(infoPath(stateDir, id))
	}()
	if err := os.Chmod(socketPath(stateDir, id), 0o600); err != nil {
		return fmt.Errorf("restrict session socket: %w", err)
	}

	cmd := exec.Command(info.Argv[0], info.Argv[1:]...) //nolint:gosec // G204: program chosen by the jailoc host
	cmd.Dir = info.Dir
	cmd.Env = info.Env
	rows, cols := info.Rows, info.Cols
	if rows == 0 || cols == 0 {
		rows, cols = 24, 80
	}
	ptmx, err := pty.StartWithSize(cmd, &pty.Winsize{Rows: rows, Cols: cols})
	if err != nil {
		return fmt.Errorf("start %s: %w", info.Argv[0], err)
	}
	h := &holder{stateDir: stateDir, info: info, ptmx: ptmx, modes: NewModeTracker(), rows: rows, cols: cols}

	go func() {
		<-stop
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGHUP)
		time.Sleep(stopGrace)
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	}()

	go h.accept(ln)
	outputDone := make(chan struct{})
	go func() {
		defer close(outputDone)
		h.pump()
	}()
	waitErr := cmd.Wait()
	<-outputDone
	_ = ptmx.Close()

	code := 0
	var exitErr *exec.ExitError
	if errors.As(waitErr, &exitErr) {
		code = exitErr.ExitCode()
	}
	h.mu.Lock()
	c := h.current
	h.mu.Unlock()
	if c != nil {
		c.send(ExitFrame(code))
		select {
		case <-c.done:
		case <-time.After(writeTimeout):
		}
	}
	return nil
}

// pump forwards program output to the attached client and tracks terminal
// modes. It returns when the program closes its terminal.
func (h *holder) pump() {
	buf := make([]byte, 32<<10)
	for {
		n, err := h.ptmx.Read(buf)
		if n > 0 {
			h.output.Add(uint64(n)) //nolint:gosec // G115: n is non-negative
			data := append([]byte(nil), buf[:n]...)
			h.mu.Lock()
			h.modes.Feed(data)
			c := h.current
			h.mu.Unlock()
			if c != nil {
				c.send(Frame{Type: FrameData, Payload: data})
			}
		}
		if err != nil {
			return
		}
	}
}

func (h *holder) accept(ln net.Listener) {
	for {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		go h.attach(conn)
	}
}

func (h *holder) attach(conn net.Conn) {
	_ = conn.SetReadDeadline(time.Now().Add(helloTimeout))
	hello, err := ReadFrame(conn)
	if err != nil || hello.Type != FrameHello {
		_ = conn.Close()
		return
	}
	rows, cols, err := ParseSize(hello.Payload)
	if err != nil {
		_ = conn.Close()
		return
	}
	_ = conn.SetReadDeadline(time.Time{})
	c := &client{conn: conn, out: make(chan Frame, clientQueue), done: make(chan struct{})}

	h.mu.Lock()
	if old := h.current; old != nil {
		old.send(Frame{Type: FrameKicked})
	}
	h.current = c
	if replay := h.modes.Replay(); len(replay) > 0 {
		c.out <- Frame{Type: FrameData, Payload: replay}
	}
	h.info.Attached, h.info.LastAttach = true, time.Now().UTC()
	_ = writeInfo(h.stateDir, h.info)
	h.mu.Unlock()

	go c.writeLoop()
	h.redraw(rows, cols)
	h.readLoop(c)

	h.mu.Lock()
	if h.current == c {
		h.current = nil
		h.info.Attached = false
		_ = writeInfo(h.stateDir, h.info)
	}
	h.mu.Unlock()
}

// redraw applies the client's window size. When it matches the current
// size the program would not notice, so the size changes first to one row
// less: a full-screen program repaints for that size and again, completely,
// when the size changes back. Waiting for the first repaint keeps programs
// that debounce resizes from merging both changes into none.
func (h *holder) redraw(rows, cols uint16) {
	h.mu.Lock()
	same := rows == h.rows && cols == h.cols
	h.mu.Unlock()
	if same && rows > 1 {
		before := h.output.Load()
		h.resize(rows-1, cols)
		deadline := time.Now().Add(nudgeWait)
		for h.output.Load() == before && time.Now().Before(deadline) {
			time.Sleep(10 * time.Millisecond)
		}
		time.Sleep(nudgeSettle)
	}
	h.resize(rows, cols)
}

func (h *holder) resize(rows, cols uint16) {
	if rows == 0 || cols == 0 {
		return
	}
	h.mu.Lock()
	h.rows, h.cols = rows, cols
	h.mu.Unlock()
	_ = pty.Setsize(h.ptmx, &pty.Winsize{Rows: rows, Cols: cols})
}

func (h *holder) readLoop(c *client) {
	defer c.close()
	for {
		f, err := ReadFrame(c.conn)
		if err != nil {
			return
		}
		h.mu.Lock()
		current := h.current == c
		h.mu.Unlock()
		if !current {
			return
		}
		switch f.Type {
		case FrameData:
			if _, err := h.ptmx.Write(f.Payload); err != nil {
				return
			}
		case FrameResize:
			if rows, cols, err := ParseSize(f.Payload); err == nil {
				h.resize(rows, cols)
			}
		}
	}
}
