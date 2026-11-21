//go:build darwin || linux

package cmd

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"errors"
	"net"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/seznam/jailoc/internal/agent"
	"github.com/seznam/jailoc/internal/config"
	"github.com/seznam/jailoc/internal/guest"
	"github.com/seznam/jailoc/internal/vm"
	"github.com/seznam/jailoc/internal/workspace"
	"golang.org/x/crypto/ssh"
)

func TestWorkspaceSpecTmp(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		storage  string
		size     int64
		ttl      time.Duration
		wantDisk int64
	}{
		{name: "ram", storage: config.TmpStorageRAM, size: 1 << 30, ttl: 8 * time.Hour},
		{name: "disk", storage: config.TmpStorageDisk, size: 2 << 30, ttl: 15 * time.Minute, wantDisk: 2 << 30},
		{name: "disk without cleanup", storage: config.TmpStorageDisk, size: 64 << 20, wantDisk: 64 << 20},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			ws := &workspace.Resolved{
				Name: "test", CPU: 2, Memory: "2G", DockerDisk: "20g",
				TmpStorage: tt.storage, TmpSizeBytes: tt.size, TmpTTL: tt.ttl,
			}
			spec, err := workspaceSpec(t.Context(), ws, vmRuntimeFiles{}, "image", nil, nil)
			if err != nil {
				t.Fatalf("workspaceSpec: %v", err)
			}
			want := guest.TmpConfig{Storage: tt.storage, SizeBytes: tt.size, TTL: tt.ttl}
			if spec.Guest.Tmp != want {
				t.Errorf("Guest.Tmp = %+v, want %+v", spec.Guest.Tmp, want)
			}
			if spec.TmpDiskSize != tt.wantDisk {
				t.Errorf("TmpDiskSize = %d, want %d", spec.TmpDiskSize, tt.wantDisk)
			}
			if spec.GuestProtocol != guest.Protocol {
				t.Errorf("GuestProtocol = %d, want %d", spec.GuestProtocol, guest.Protocol)
			}
		})
	}
}

func TestTmpRestartChanges(t *testing.T) {
	ws := func(storage string, size int64, ttl time.Duration) *workspace.Resolved {
		return &workspace.Resolved{
			Name: "ws", Agent: agent.Harness{Name: "opencode"}, CPU: 2, Memory: "4g",
			TmpStorage: storage, TmpSizeBytes: size, TmpTTL: ttl,
		}
	}
	running := func(g guest.TmpConfig) vm.Spec {
		return vm.Spec{Agent: "opencode", CPUs: 2, MemoryMiB: 4096, GuestProtocol: guest.Protocol, Guest: &guest.Config{Tmp: g}}
	}
	ramGiB := guest.TmpConfig{Storage: guest.TmpStorageRAM, SizeBytes: 1 << 30, TTL: 8 * time.Hour}
	tests := []struct {
		name    string
		ws      *workspace.Resolved
		running vm.Spec
		want    []string
	}{
		{"same", ws("ram", 1<<30, 8*time.Hour), running(ramGiB), nil},
		{"ttl only", ws("ram", 1<<30, 15*time.Minute), running(ramGiB), nil},
		{"ttl disabled", ws("ram", 1<<30, 0), running(ramGiB), nil},
		{"storage", ws("disk", 1<<30, 8*time.Hour), running(ramGiB), []string{"tmp_storage"}},
		{"size", ws("ram", 2<<30, 8*time.Hour), running(ramGiB), []string{"tmp_size"}},
		{"storage and size", ws("disk", 2<<30, 8*time.Hour), running(ramGiB), []string{"tmp_storage", "tmp_size"}},
		{"legacy spec matches the defaults", ws("ram", 1<<30, 8*time.Hour), running(guest.TmpConfig{}), nil},
		{"legacy spec against a bigger tmp", ws("ram", 2<<30, 8*time.Hour), running(guest.TmpConfig{}), []string{"tmp_size"}},
		{"legacy spec against disk", ws("disk", 1<<30, 0), running(guest.TmpConfig{}), []string{"tmp_storage"}},
		{"spec without guest config", ws("ram", 1<<30, 8*time.Hour), vm.Spec{Agent: "opencode", CPUs: 2, MemoryMiB: 4096, GuestProtocol: guest.Protocol}, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("HOME", t.TempDir())
			got := slices.DeleteFunc(statusRestartChanges(tt.ws, tt.running), func(s string) bool { return !strings.HasPrefix(s, "tmp_") })
			if !slices.Equal(got, tt.want) {
				t.Errorf("statusRestartChanges tmp = %v, want %v", got, tt.want)
			}
			// restartOnlyChanges compares two specs; build the wanted one from the workspace.
			want := vm.Spec{Agent: "opencode", CPUs: 2, MemoryMiB: 4096, GuestProtocol: guest.Protocol, Guest: &guest.Config{Tmp: guest.TmpConfig{Storage: tt.ws.TmpStorage, SizeBytes: tt.ws.TmpSizeBytes, TTL: tt.ws.TmpTTL}}}
			base := tt.running
			if base.Guest == nil {
				base.Guest = &guest.Config{}
			}
			got = slices.DeleteFunc(restartOnlyChanges(base, want), func(s string) bool { return !strings.HasPrefix(s, "tmp_") })
			if !slices.Equal(got, tt.want) {
				t.Errorf("restartOnlyChanges tmp = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestCheckTmpReload(t *testing.T) {
	t.Parallel()
	ws := func(ttl time.Duration) *workspace.Resolved {
		return &workspace.Resolved{Name: "ws", TmpStorage: "ram", TmpSizeBytes: 1 << 30, TmpTTL: ttl}
	}
	current := vm.Spec{GuestProtocol: tmpGuestProtocol, Guest: &guest.Config{Tmp: guest.TmpConfig{Storage: "ram", SizeBytes: 1 << 30, TTL: time.Hour}}}
	old := vm.Spec{GuestProtocol: tmpGuestProtocol - 1, Guest: &guest.Config{}}
	tests := []struct {
		name    string
		ws      *workspace.Resolved
		running vm.Spec
		wantErr bool
	}{
		{"current guest changes ttl", ws(15 * time.Minute), current, false},
		{"current guest keeps ttl", ws(time.Hour), current, false},
		{"old guest keeps its no-cleanup ttl", ws(0), old, false},
		{"old guest cannot take a ttl", ws(8 * time.Hour), old, true},
		{"old guest without guest config", ws(8 * time.Hour), vm.Spec{GuestProtocol: tmpGuestProtocol - 1}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			err := checkTmpReload(tt.ws, tt.running)
			if (err != nil) != tt.wantErr {
				t.Fatalf("checkTmpReload = %v, wantErr %t", err, tt.wantErr)
			}
			if err != nil && !strings.Contains(err.Error(), "jailoc restart ws") {
				t.Errorf("error %q does not point to `jailoc restart ws`", err)
			}
		})
	}
}

func TestReloadedTmp(t *testing.T) {
	t.Parallel()
	ttl := 15 * time.Minute
	runningTmp := guest.TmpConfig{Storage: "disk", SizeBytes: 2 << 30, TTL: time.Hour}
	want := guest.TmpConfig{Storage: "ram", SizeBytes: 1 << 30, TTL: ttl}
	if got := reloadedTmp(vm.Spec{GuestProtocol: tmpGuestProtocol, Guest: &guest.Config{Tmp: runningTmp}}, want); got != (guest.TmpConfig{Storage: "disk", SizeBytes: 2 << 30, TTL: ttl}) {
		t.Errorf("current guest: only the ttl is copied, got %+v", got)
	}
	if got := reloadedTmp(vm.Spec{GuestProtocol: tmpGuestProtocol - 1, Guest: &guest.Config{}}, want); got != (guest.TmpConfig{}) {
		t.Errorf("old guest: the spec stays unchanged, got %+v", got)
	}
}

func TestTmpLabel(t *testing.T) {
	t.Parallel()
	ramSpec := vm.Spec{GuestProtocol: tmpGuestProtocol, Guest: &guest.Config{Tmp: guest.TmpConfig{Storage: "ram", SizeBytes: 1 << 30, TTL: 8 * time.Hour}}}
	diskSpec := vm.Spec{GuestProtocol: tmpGuestProtocol, Guest: &guest.Config{Tmp: guest.TmpConfig{Storage: "disk", SizeBytes: 2 << 30}}}
	legacy := vm.Spec{GuestProtocol: 3, Guest: &guest.Config{}}
	usage := func(capacity, used uint64) *guest.TmpUsage {
		return &guest.TmpUsage{CapacityBytes: capacity, UsedBytes: used, AvailableBytes: capacity - used}
	}
	tests := []struct {
		name  string
		spec  vm.Spec
		usage *guest.TmpUsage
		err   error
		want  string
	}{
		{"ram", ramSpec, usage(1<<30, 12<<20), nil, "ram, 1 GiB, cleanup after 8h; 12 MiB of 1 GiB used (1%), 1012 MiB free"},
		{"disk without cleanup", diskSpec, usage(2<<30, 0), nil, "disk, 2 GiB, no cleanup; 0 B of 2 GiB used (0%), 2 GiB free"},
		{"just below the warning", ramSpec, usage(100, 89), nil, "ram, 1 GiB, cleanup after 8h; 89 B of 100 B used (89%), 11 B free"},
		{"warning at 90 percent", ramSpec, usage(100, 90), nil, "ram, 1 GiB, cleanup after 8h; 90 B of 100 B used (90%), 10 B free - WARNING: almost full"},
		{"reserved blocks reduce free below unused", ramSpec, &guest.TmpUsage{CapacityBytes: 100, UsedBytes: 40, AvailableBytes: 30}, nil, "ram, 1 GiB, cleanup after 8h; 40 B of 100 B used (40%), 30 B free"},
		{"full", ramSpec, usage(100, 100), nil, "ram, 1 GiB, cleanup after 8h; 100 B of 100 B used (100%), 0 B free - WARNING: almost full"},
		{"usage error is not zero", ramSpec, nil, errors.New("timed out"), "ram, 1 GiB, cleanup after 8h; usage unknown (timed out)"},
		{"no usage is not zero", diskSpec, nil, nil, "disk, 2 GiB, no cleanup; usage unknown"},
		{"empty capacity is unknown", ramSpec, &guest.TmpUsage{}, nil, "ram, 1 GiB, cleanup after 8h; usage unknown (the guest reported no capacity)"},
		{"used above capacity is unknown", ramSpec, &guest.TmpUsage{CapacityBytes: 10, UsedBytes: 11}, nil, "ram, 1 GiB, cleanup after 8h; usage unknown (the guest reported more used than capacity)"},
		{"available above capacity is unknown", ramSpec, &guest.TmpUsage{CapacityBytes: 10, UsedBytes: 1, AvailableBytes: 11}, nil, "ram, 1 GiB, cleanup after 8h; usage unknown (the guest reported more available than capacity)"},
		{"legacy spec", legacy, nil, errors.New("old guest"), "ram, 1 GiB, no cleanup; usage unknown (old guest)"},
		{
			"escapes control chars in usage error",
			ramSpec,
			nil,
			errors.New("bad\x1b[31m\nfake line"),
			`ram, 1 GiB, cleanup after 8h; usage unknown (bad\x1b[31m\nfake line)`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := tmpLabel(tt.spec, tt.usage, tt.err); got != tt.want {
				t.Errorf("tmpLabel = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestParseTmpUsage(t *testing.T) {
	t.Parallel()
	got, err := parseTmpUsage([]byte(`{"capacity_bytes":3,"used_bytes":2,"available_bytes":1}` + "\n"))
	if err != nil || got != (guest.TmpUsage{CapacityBytes: 3, UsedBytes: 2, AvailableBytes: 1}) {
		t.Fatalf("parseTmpUsage = %+v, %v", got, err)
	}
	tests := []struct {
		name string
		in   string
	}{
		{"empty", ``},
		{"not json", `not json`},
		{"array", `[]`},
		{"null", `null`},
		{"negative capacity", `{"capacity_bytes":-1,"used_bytes":0,"available_bytes":0}`},
		{"zero capacity", `{"capacity_bytes":0,"used_bytes":0,"available_bytes":0}`},
		{"present but null capacity", `{"capacity_bytes":null,"used_bytes":1,"available_bytes":1}`},
		{"missing capacity", `{"used_bytes":1,"available_bytes":1}`},
		{"missing used", `{"capacity_bytes":3,"available_bytes":1}`},
		{"missing available", `{"capacity_bytes":3,"used_bytes":2}`},
		{"whole object missing", `{}`},
		{"used above capacity", `{"capacity_bytes":3,"used_bytes":4,"available_bytes":0}`},
		{"available above capacity", `{"capacity_bytes":3,"used_bytes":1,"available_bytes":4}`},
		{"trailing object", `{"capacity_bytes":3,"used_bytes":2,"available_bytes":1}{}`},
		{"trailing array", `{"capacity_bytes":3,"used_bytes":2,"available_bytes":1}[]`},
		{"trailing word", `{"capacity_bytes":3,"used_bytes":2,"available_bytes":1} trailing`},
		{"trailing comma garbage", `{"capacity_bytes":3,"used_bytes":2,"available_bytes":1},`},
		{"duplicate object", `{"capacity_bytes":3,"used_bytes":2,"available_bytes":1}{"capacity_bytes":3}`},
		{"trailing close bracket", `{"capacity_bytes":3,"used_bytes":2,"available_bytes":1}]`},
		{"trailing close brace", `{"capacity_bytes":3,"used_bytes":2,"available_bytes":1}}`},
		{"trailing scalar", `{"capacity_bytes":3,"used_bytes":2,"available_bytes":1} 5`},
		{"truncated trailing object", `{"capacity_bytes":3,"used_bytes":2,"available_bytes":1} {`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got, err := parseTmpUsage([]byte(tt.in)); err == nil {
				t.Errorf("parseTmpUsage(%q) = %+v, want error", tt.in, got)
			}
		})
	}
	t.Run("accepts surrounding whitespace", func(t *testing.T) {
		t.Parallel()
		if _, err := parseTmpUsage([]byte(" \n{\"capacity_bytes\":3,\"used_bytes\":2,\"available_bytes\":1}\n\n")); err != nil {
			t.Errorf("parseTmpUsage with whitespace: %v", err)
		}
	})
	t.Run("ignores unknown fields", func(t *testing.T) {
		t.Parallel()
		if _, err := parseTmpUsage([]byte(`{"capacity_bytes":3,"used_bytes":2,"available_bytes":1,"inodes":7}`)); err != nil {
			t.Errorf("parseTmpUsage with an unknown field: %v", err)
		}
	})
}

// tmpTestServer is an SSH server answering exec requests: "ok" prints a
// usage line, "big" floods stdout, "hang" never answers.
func tmpTestServer(t *testing.T) *ssh.Client {
	t.Helper()
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	signer, err := ssh.NewSignerFromKey(priv)
	if err != nil {
		t.Fatalf("signer: %v", err)
	}
	cfg := &ssh.ServerConfig{NoClientAuth: true}
	cfg.AddHostKey(signer)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	go func() {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		sc, chans, reqs, err := ssh.NewServerConn(conn, cfg)
		if err != nil {
			return
		}
		defer func() { _ = sc.Close() }()
		go ssh.DiscardRequests(reqs)
		for nc := range chans {
			ch, chReqs, err := nc.Accept()
			if err != nil {
				continue
			}
			go serveTmpExec(ch, chReqs)
		}
	}()
	client, err := ssh.Dial("tcp", ln.Addr().String(), &ssh.ClientConfig{
		User:            "agent",
		HostKeyCallback: ssh.FixedHostKey(signer.PublicKey()),
		Timeout:         5 * time.Second,
	})
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	t.Cleanup(func() { _ = client.Close() })
	return client
}

func serveTmpExec(ch ssh.Channel, reqs <-chan *ssh.Request) {
	defer func() { _ = ch.Close() }()
	for req := range reqs {
		if req.Type != "exec" {
			_ = req.Reply(false, nil)
			continue
		}
		_ = req.Reply(true, nil)
		cmd := string(req.Payload[4:])
		switch {
		case strings.Contains(cmd, "hang"):
			continue // the request loop ends when the client closes the channel
		case strings.Contains(cmd, "big"):
			chunk := make([]byte, 64<<10)
			for range 64 {
				if _, err := ch.Write(chunk); err != nil {
					return
				}
			}
		default:
			_, _ = ch.Write([]byte(`{"capacity_bytes":3,"used_bytes":2,"available_bytes":1}`))
		}
		_, _ = ch.SendRequest("exit-status", false, []byte{0, 0, 0, 0})
		return
	}
}

func TestSSHOutputBounded(t *testing.T) {
	t.Parallel()
	t.Run("returns the output", func(t *testing.T) {
		t.Parallel()
		out, err := sshOutputBounded(t.Context(), tmpTestServer(t), []string{"ok"}, 4096)
		if err != nil || !strings.Contains(string(out), `"capacity_bytes":3`) {
			t.Fatalf("sshOutputBounded = %q, %v", out, err)
		}
	})
	t.Run("rejects output over the limit", func(t *testing.T) {
		t.Parallel()
		out, err := sshOutputBounded(t.Context(), tmpTestServer(t), []string{"big"}, 1024)
		if err == nil || !strings.Contains(err.Error(), "more than 1024 bytes") {
			t.Fatalf("sshOutputBounded error = %v, want limit error", err)
		}
		if len(out) != 0 {
			t.Errorf("returned %d bytes of an oversized output", len(out))
		}
	})
	t.Run("deadline closes the connected session", func(t *testing.T) {
		t.Parallel()
		client := tmpTestServer(t)
		ctx, cancel := context.WithTimeout(t.Context(), 200*time.Millisecond)
		defer cancel()
		start := time.Now()
		_, err := sshOutputBounded(ctx, client, []string{"hang"}, 4096)
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("sshOutputBounded error = %v, want deadline exceeded", err)
		}
		if elapsed := time.Since(start); elapsed > 3*time.Second {
			t.Errorf("returned after %v, deadline was 200ms", elapsed)
		}
		closed := make(chan struct{})
		go func() { _ = client.Wait(); close(closed) }()
		select {
		case <-closed:
		case <-time.After(3 * time.Second):
			t.Fatal("the SSH connection stayed open after the deadline")
		}
	})
}

func TestReloadGuestSendsTmpTTL(t *testing.T) {
	t.Parallel()
	hour, off := time.Hour, time.Duration(0)
	tests := []struct {
		name string
		ttl  *time.Duration
		want string
	}{
		{"not sent", nil, ""},
		{"sent", &hour, `"tmp_ttl_ns":3600000000000`},
		{"disable sent", &off, `"tmp_ttl_ns":0`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			stateDir := t.TempDir()
			if err := os.MkdirAll(filepath.Join(vm.GuestDataDir(stateDir), vm.ControlName), 0o700); err != nil {
				t.Fatal(err)
			}
			// Play the guest: answer the request with its own Seq.
			answered := make(chan []byte, 1)
			go func() {
				for {
					b, err := vm.ReadControl(stateDir, guest.ReloadFile)
					if err != nil {
						time.Sleep(5 * time.Millisecond)
						continue
					}
					var req guest.Reload
					if json.Unmarshal(b, &req) != nil {
						continue
					}
					done, _ := json.Marshal(guest.ReloadDone{Seq: req.Seq})
					_ = vm.WriteControl(stateDir, guest.ReloadDoneFile, done)
					answered <- b
					return
				}
			}()
			if err := reloadGuest(stateDir, guest.Config{}, tt.ttl); err != nil {
				t.Fatalf("reloadGuest: %v", err)
			}
			req := string(<-answered)
			if tt.want == "" && strings.Contains(req, "tmp_ttl_ns") {
				t.Errorf("request %s carries tmp_ttl_ns", req)
			}
			if tt.want != "" && !strings.Contains(req, tt.want) {
				t.Errorf("request %s lacks %s", req, tt.want)
			}
		})
	}
}
