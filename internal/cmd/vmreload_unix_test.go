//go:build darwin || linux

package cmd

import (
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/seznam/jailoc/internal/guest"
	"github.com/seznam/jailoc/internal/vm"
)

func TestBrokerReloadEvent(t *testing.T) {
	t.Parallel()
	const (
		old    = `{"event":"reload","policy_error":"old"}` + "\n"
		denied = `{"event":"flow","allow":false}` + "\n"
		reload = `{"event":"reload","policy_error":"new"}` + "\n"
	)
	tests := []struct {
		name  string
		after func(t *testing.T, path string) // what the broker writes after the offset is taken
		want  string                          // policy_error of the event found, "" when none
	}{
		{"appended", func(t *testing.T, path string) { appendFile(t, path, denied+reload) }, "new"},
		{"not yet written", func(t *testing.T, path string) { appendFile(t, path, denied) }, ""},
		{"written before rotation", func(t *testing.T, path string) {
			appendFile(t, path, reload)
			rotate(t, path, denied)
		}, "new"},
		{"written after rotation", func(t *testing.T, path string) {
			rotate(t, path, denied+reload)
		}, "new"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			path := filepath.Join(t.TempDir(), "broker.jsonl")
			appendFile(t, path, old)
			before, err := os.Open(path) //nolint:gosec // test path
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = before.Close() })
			tt.after(t, path)
			ev, ok := brokerReloadEvent(before, int64(len(old)), path)
			if got := ev.PolicyError; ok != (tt.want != "") || got != tt.want {
				t.Fatalf("brokerReloadEvent() = %q, %v; want %q", got, ok, tt.want)
			}
		})
	}
}

func appendFile(t *testing.T, path, data string) {
	t.Helper()
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600) //nolint:gosec // test path
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString(data); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
}

// rotate moves path to path.1 and starts a new log with data, as the broker does.
func rotate(t *testing.T, path, data string) {
	t.Helper()
	if err := os.Rename(path, path+".1"); err != nil {
		t.Fatal(err)
	}
	appendFile(t, path, data)
}

func TestRestartOnlyChangesDockerDisk(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	base := vm.Spec{Agent: "opencode", Docker: true, GuestProtocol: guest.Protocol, Guest: &guest.Config{}}
	tests := []struct {
		name          string
		running, want int64
		docker        bool
		changes       []string
	}{
		{"unchanged default", 0, 20 << 30, true, nil},
		{"grown", 20 << 30, 40 << 30, true, []string{"docker_disk"}},
		{"grown from a spec without disk_size", 0, 40 << 30, true, []string{"docker_disk"}},
		{"docker off", 20 << 30, 40 << 30, false, nil},
	}
	for _, tt := range tests {
		running, want := base, base
		running.DiskSize, want.DiskSize = tt.running, tt.want
		running.Docker, want.Docker = tt.docker, tt.docker
		if got := restartOnlyChanges(running, want); !slices.Equal(got, tt.changes) {
			t.Errorf("%s: got %v, want %v", tt.name, got, tt.changes)
		}
	}
}

func TestRestartOnlyChangesIPv6(t *testing.T) {
	t.Parallel()
	for _, tt := range []struct {
		name          string
		protocol      int
		running, want bool
		changes       []string
	}{
		{"both off", guest.Protocol, false, false, nil},
		{"both on", guest.Protocol, true, true, nil},
		{"enable", guest.Protocol, false, true, []string{"network.ipv6"}},
		{"disable", guest.Protocol, true, false, []string{"network.ipv6"}},
		// Legacy guests and brokers enable IPv6 whatever the spec says.
		{"legacy off", 1, false, false, []string{"network.ipv6", "the jailoc guest (jailoc was upgraded)"}},
		{"legacy on", 1, false, true, []string{"the jailoc guest (jailoc was upgraded)"}},
	} {
		running := vm.Spec{Agent: "opencode", GuestProtocol: tt.protocol, Guest: &guest.Config{IPv6: tt.running}}
		want := vm.Spec{Agent: "opencode", GuestProtocol: guest.Protocol, Guest: &guest.Config{IPv6: tt.want}}
		if got := restartOnlyChanges(running, want); !slices.Equal(got, tt.changes) {
			t.Errorf("%s: got %v, want %v", tt.name, got, tt.changes)
		}
	}
}
