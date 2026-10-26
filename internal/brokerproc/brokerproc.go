// Package brokerproc starts a jailoc-netbroker process for one VM workspace
// and ties its lifetime to the VM: the VM boots only after the broker serves
// its NIC socket, and when either side ends the other is stopped.
package brokerproc

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"sync"
	"syscall"
	"time"
)

// Options configures the broker process.
type Options struct {
	// Binary is the jailoc-netbroker executable.
	Binary string
	// Args are the broker flags; -status-fd is added by Start.
	Args []string
	// ReadyTimeout bounds the wait for the broker's "ready" line.
	ReadyTimeout time.Duration
	// Stdout and Stderr receive the broker's output; nil discards it.
	Stdout, Stderr io.Writer
}

// Process is a running broker.
type Process struct {
	cmd *exec.Cmd

	ready   chan struct{}
	nicUp   chan struct{}
	nicDown chan struct{}
	done    chan struct{}

	once    map[string]*sync.Once
	waitErr error
}

// Start launches the broker and waits until it serves the NIC socket. The
// broker is killed when it does not become ready in time.
func Start(ctx context.Context, o Options) (*Process, error) {
	r, w, err := os.Pipe()
	if err != nil {
		return nil, fmt.Errorf("status pipe: %w", err)
	}
	args := append(append([]string{}, o.Args...), "-status-fd", "3")
	cmd := exec.Command(o.Binary, args...) //nolint:gosec // G204: the broker binary and flags come from jailoc, not the guest
	cmd.ExtraFiles = []*os.File{w}
	cmd.Stdout, cmd.Stderr = o.Stdout, o.Stderr
	if err := cmd.Start(); err != nil {
		_ = r.Close()
		_ = w.Close()
		return nil, fmt.Errorf("start broker: %w", err)
	}
	_ = w.Close()

	p := &Process{
		cmd:     cmd,
		ready:   make(chan struct{}),
		nicUp:   make(chan struct{}),
		nicDown: make(chan struct{}),
		done:    make(chan struct{}),
		once:    map[string]*sync.Once{"ready": {}, "nic connected": {}, "nic disconnected": {}},
	}
	go p.readStatus(r)
	go func() {
		p.waitErr = cmd.Wait()
		close(p.done)
	}()

	timer := time.NewTimer(o.ReadyTimeout)
	defer timer.Stop()
	select {
	case <-p.ready:
		return p, nil
	case <-p.done:
		return nil, fmt.Errorf("broker exited before ready: %w", exitErr(p.waitErr))
	case <-timer.C:
		_ = p.Stop(time.Second)
		return nil, fmt.Errorf("broker not ready after %s", o.ReadyTimeout)
	case <-ctx.Done():
		_ = p.Stop(time.Second)
		return nil, fmt.Errorf("wait for broker: %w", ctx.Err())
	}
}

func exitErr(err error) error {
	if err == nil {
		return errors.New("exit status 0")
	}
	return err
}

func (p *Process) readStatus(r io.ReadCloser) {
	defer func() { _ = r.Close() }()
	chans := map[string]chan struct{}{"ready": p.ready, "nic connected": p.nicUp, "nic disconnected": p.nicDown}
	sc := bufio.NewScanner(r)
	for sc.Scan() {
		if ch, ok := chans[sc.Text()]; ok {
			p.once[sc.Text()].Do(func() { close(ch) })
		}
	}
}

// PID returns the broker's process ID.
func (p *Process) PID() int { return p.cmd.Process.Pid }

// Done is closed when the broker process has exited.
func (p *Process) Done() <-chan struct{} { return p.done }

// NICDown is closed when the VMM closes its NIC connection. libkrun never
// reconnects, so this means the VM lost its network for good.
func (p *Process) NICDown() <-chan struct{} { return p.nicDown }

// WaitNIC waits until the VMM connects its NIC to the broker. A VM whose NIC
// never connects runs without network and without an error from the VMM.
func (p *Process) WaitNIC(ctx context.Context, timeout time.Duration) error {
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case <-p.nicUp:
		return nil
	case <-p.done:
		return fmt.Errorf("broker exited before the VM NIC connected: %w", exitErr(p.waitErr))
	case <-timer.C:
		return fmt.Errorf("VM NIC did not connect within %s", timeout)
	case <-ctx.Done():
		return fmt.Errorf("wait for VM NIC: %w", ctx.Err())
	}
}

// Stop sends SIGTERM, then SIGKILL after grace, and waits for the exit.
func (p *Process) Stop(grace time.Duration) error {
	select {
	case <-p.done:
		return nil
	default:
	}
	if err := p.cmd.Process.Signal(syscall.SIGTERM); err != nil && !errors.Is(err, os.ErrProcessDone) {
		return fmt.Errorf("signal broker: %w", err)
	}
	timer := time.NewTimer(grace)
	defer timer.Stop()
	select {
	case <-p.done:
		return nil
	case <-timer.C:
	}
	if err := p.cmd.Process.Kill(); err != nil && !errors.Is(err, os.ErrProcessDone) {
		return fmt.Errorf("kill broker: %w", err)
	}
	<-p.done
	return nil
}

// VM is the part of a running VM the supervisor needs.
type VM interface {
	Alive() bool
	Stop(ctx context.Context) error
}

// Outcome reports why supervision ended.
type Outcome struct {
	Reason string // shutdown, broker-exited, nic-disconnected, vm-exited
	// StopErr collects errors from stopping the other side.
	StopErr error
}

// Supervise blocks until ctx ends, the broker exits, the VM's NIC
// disconnects or the VM exits, then stops whatever is still running. A VM
// never outlives its broker, so it is never left without its policy path.
func Supervise(ctx context.Context, p *Process, vm VM, poll, grace time.Duration) Outcome {
	ticker := time.NewTicker(poll)
	defer ticker.Stop()
	stopVM := func() error {
		sctx, cancel := context.WithTimeout(context.Background(), grace)
		defer cancel()
		if err := vm.Stop(sctx); err != nil {
			return fmt.Errorf("stop vm: %w", err)
		}
		return nil
	}
	for {
		select {
		case <-ctx.Done():
			return Outcome{Reason: "shutdown", StopErr: errors.Join(stopVM(), p.Stop(grace))}
		case <-p.Done():
			return Outcome{Reason: "broker-exited", StopErr: stopVM()}
		case <-p.NICDown():
			return Outcome{Reason: "nic-disconnected", StopErr: errors.Join(stopVM(), p.Stop(grace))}
		case <-ticker.C:
			if !vm.Alive() {
				return Outcome{Reason: "vm-exited", StopErr: p.Stop(grace)}
			}
		}
	}
}
