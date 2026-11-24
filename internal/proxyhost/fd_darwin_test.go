//go:build darwin

package proxyhost

import (
	"net"
	"testing"

	"golang.org/x/sys/unix"
)

func TestSocketIsListening(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		fd      func(t *testing.T) int
		want    bool
		wantErr bool
	}{
		{
			name: "listening IPv4 loopback",
			fd: func(t *testing.T) int {
				ln := fdLoopbackListener(t, "tcp4")
				t.Cleanup(func() { fdClose(t, "tcp listener", ln) })
				return fdRawFD(t, ln)
			},
			want: true,
		},
		{
			name: "listening IPv6 loopback",
			fd: func(t *testing.T) int {
				ln := fdLoopbackListener(t, "tcp6")
				t.Cleanup(func() { fdClose(t, "tcp listener", ln) })
				return fdRawFD(t, ln)
			},
			want: true,
		},
		{
			name: "bound IPv4 not listening",
			fd: func(t *testing.T) int {
				fd := fdRawSocket(t, unix.AF_INET, unix.SOCK_STREAM)
				fdBind(t, fd, &unix.SockaddrInet4{Addr: [4]byte{127, 0, 0, 1}})
				return fd
			},
		},
		{
			name: "bound IPv6 not listening",
			fd: func(t *testing.T) int {
				fd := fdRawSocket(t, unix.AF_INET6, unix.SOCK_STREAM)
				fdBind(t, fd, &unix.SockaddrInet6{Addr: [16]byte{15: 1}})
				return fd
			},
		},
		{
			name: "connected client",
			fd: func(t *testing.T) int {
				ln := fdLoopbackListener(t, "tcp4")
				t.Cleanup(func() { fdClose(t, "tcp listener", ln) })
				c, err := net.Dial("tcp4", ln.Addr().String())
				if err != nil {
					t.Fatalf("Dial: %v", err)
				}
				t.Cleanup(func() { fdClose(t, "tcp client", c) })
				return fdRawFD(t, c.(*net.TCPConn))
			},
		},
		{
			name:    "bad descriptor",
			fd:      func(*testing.T) int { return -1 },
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, err := socketIsListening(tt.fd(t))
			if tt.wantErr {
				if err == nil {
					t.Fatalf("socketIsListening = %v, nil error, want error", got)
				}
				if got {
					t.Fatal("socketIsListening reported listening alongside an error")
				}
				return
			}
			if err != nil {
				t.Fatalf("socketIsListening error = %v, want nil", err)
			}
			if got != tt.want {
				t.Fatalf("socketIsListening = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestValidateLoopbackTCPListenerFDBadDescriptor(t *testing.T) {
	t.Parallel()

	if addr, err := validateLoopbackTCPListenerFD(-1); err == nil {
		t.Fatalf("validate accepted descriptor -1 as %v", addr)
	}
}
