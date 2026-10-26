package brokerproc

import (
	"context"
	"os"
	"os/signal"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"
)

const helperEnv = "JAILOC_BROKERPROC_HELPER"

// TestMain turns the test binary into a fake broker when helperEnv is set.
func TestMain(m *testing.M) {
	if mode := os.Getenv(helperEnv); mode != "" {
		os.Exit(fakeBroker(mode))
	}
	os.Exit(m.Run())
}

func fakeBroker(mode string) int {
	status := os.NewFile(3, "status")
	say := func(s string) { _, _ = status.WriteString(s + "\n") }
	term := make(chan os.Signal, 1)
	signal.Notify(term, syscall.SIGTERM)
	switch mode {
	case "exit-early":
		return 3
	case "never-ready":
		time.Sleep(time.Minute)
		return 0
	case "ignore-term":
		signal.Ignore(syscall.SIGTERM)
		say("ready")
		time.Sleep(time.Minute)
		return 0
	}
	say("ready")
	if strings.Contains(mode, "nic") {
		say("nic connected")
	}
	switch mode {
	case "ready-then-exit":
		time.Sleep(200 * time.Millisecond)
		return 1
	case "nic-down":
		time.Sleep(200 * time.Millisecond)
		say("nic disconnected")
	}
	<-term
	return 0
}

func start(t *testing.T, mode string, timeout time.Duration) (*Process, error) {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv(helperEnv, mode)
	p, err := Start(context.Background(), Options{Binary: exe, ReadyTimeout: timeout})
	if p != nil {
		t.Cleanup(func() { _ = p.Stop(time.Second) })
	}
	return p, err
}

type fakeVM struct {
	alive   atomic.Bool
	stopped atomic.Bool
}

func (v *fakeVM) Alive() bool { return v.alive.Load() }
func (v *fakeVM) Stop(context.Context) error {
	v.stopped.Store(true)
	v.alive.Store(false)
	return nil
}

func TestStart(t *testing.T) {
	tests := []struct {
		mode    string
		wantErr string
	}{
		{mode: "ready"},
		{mode: "exit-early", wantErr: "exited before ready"},
		{mode: "never-ready", wantErr: "not ready after"},
	}
	for _, tt := range tests {
		t.Run(tt.mode, func(t *testing.T) {
			_, err := start(t, tt.mode, 2*time.Second)
			switch {
			case tt.wantErr == "" && err != nil:
				t.Fatalf("Start: %v", err)
			case tt.wantErr != "" && (err == nil || !strings.Contains(err.Error(), tt.wantErr)):
				t.Fatalf("Start error = %v, want %q", err, tt.wantErr)
			}
		})
	}
}

func TestWaitNIC(t *testing.T) {
	tests := []struct {
		mode    string
		wantErr string
	}{
		{mode: "ready-nic"},
		{mode: "ready", wantErr: "did not connect"},
		{mode: "ready-then-exit", wantErr: "exited before the VM NIC connected"},
	}
	for _, tt := range tests {
		t.Run(tt.mode, func(t *testing.T) {
			p, err := start(t, tt.mode, 2*time.Second)
			if err != nil {
				t.Fatalf("Start: %v", err)
			}
			err = p.WaitNIC(context.Background(), time.Second)
			switch {
			case tt.wantErr == "" && err != nil:
				t.Fatalf("WaitNIC: %v", err)
			case tt.wantErr != "" && (err == nil || !strings.Contains(err.Error(), tt.wantErr)):
				t.Fatalf("WaitNIC error = %v, want %q", err, tt.wantErr)
			}
		})
	}
}

func TestStopKillsAfterGrace(t *testing.T) {
	p, err := start(t, "ignore-term", 2*time.Second)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	if err := p.Stop(200 * time.Millisecond); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	select {
	case <-p.Done():
	default:
		t.Fatal("broker still running after Stop")
	}
}

func TestSupervise(t *testing.T) {
	tests := []struct {
		name       string
		mode       string
		vmExits    bool
		cancel     bool
		wantReason string
		wantVMStop bool
		wantBroker bool // broker must have exited
	}{
		{name: "broker exit stops vm", mode: "ready-then-exit", wantReason: "broker-exited", wantVMStop: true, wantBroker: true},
		{name: "nic disconnect stops both", mode: "nic-down", wantReason: "nic-disconnected", wantVMStop: true, wantBroker: true},
		{name: "vm exit stops broker", mode: "ready-nic", vmExits: true, wantReason: "vm-exited", wantBroker: true},
		{name: "shutdown stops both", mode: "ready-nic", cancel: true, wantReason: "shutdown", wantVMStop: true, wantBroker: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p, err := start(t, tt.mode, 2*time.Second)
			if err != nil {
				t.Fatalf("Start: %v", err)
			}
			vm := &fakeVM{}
			vm.alive.Store(true)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if tt.vmExits {
				time.AfterFunc(100*time.Millisecond, func() { vm.alive.Store(false) })
			}
			if tt.cancel {
				time.AfterFunc(100*time.Millisecond, cancel)
			}
			out := Supervise(ctx, p, vm, 20*time.Millisecond, time.Second)
			if out.Reason != tt.wantReason || out.StopErr != nil {
				t.Fatalf("Supervise = %+v, want reason %q", out, tt.wantReason)
			}
			if vm.stopped.Load() != tt.wantVMStop {
				t.Fatalf("vm stopped = %v, want %v", vm.stopped.Load(), tt.wantVMStop)
			}
			select {
			case <-p.Done():
			default:
				if tt.wantBroker {
					t.Fatal("broker still running")
				}
			}
		})
	}
}
