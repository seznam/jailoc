package netbroker

import (
	"context"
	"errors"
	"io"
	"net"
	"net/netip"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"gvisor.dev/gvisor/pkg/tcpip"
	"gvisor.dev/gvisor/pkg/tcpip/adapters/gonet"
	"gvisor.dev/gvisor/pkg/tcpip/network/ipv6"
	"gvisor.dev/gvisor/pkg/tcpip/stack"
)

type readyStatus chan struct{}

func (status readyStatus) Write(data []byte) (int, error) {
	if string(data) == "ready\n" {
		close(status)
	}
	return len(data), nil
}

func connectTestBroker(t *testing.T, broker *Broker) *stack.Stack {
	t.Helper()
	ready := make(readyStatus)
	broker.cfg.Status = ready
	t.Cleanup(broker.stack.Close)
	socket := filepath.Join(t.TempDir(), "nic.sock")
	listener, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- broker.Run(t.Context(), listener) }()
	select {
	case <-ready:
	case err := <-done:
		t.Fatalf("broker startup: %v", err)
	case <-time.After(10 * time.Second):
		t.Fatal("broker startup timed out")
	}
	return fakeGuest(t, t.Context(), socket)
}

func TestBrokerIPv6RejectsPrivate(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	policy, err := ParsePolicy([]byte(`{}`), "ipv6")
	if err != nil {
		t.Fatal(err)
	}
	log := &syncBuf{}
	broker, err := New(Config{Subnet: netip.MustParsePrefix("192.168.127.0/24"), Gateway: netip.MustParseAddr("192.168.127.1"), GuestIP: netip.MustParseAddr("192.168.127.2"), Log: log}, policy)
	if err != nil {
		t.Fatal(err)
	}
	guest := connectTestBroker(t, broker)
	dialctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	conn, err := gonet.DialContextTCP(dialctx, guest, tcpip.FullAddress{NIC: 1, Addr: tcpip.AddrFrom16(netip.MustParseAddr("fd42::1").As16()), Port: 443}, ipv6.ProtocolNumber)
	if conn != nil {
		_ = conn.Close()
	}
	if err == nil || !strings.Contains(err.Error(), "refused") {
		t.Fatalf("private IPv6 dial = %v, want RST; log: %s", err, log.String())
	}
	if !strings.Contains(log.String(), `"reason":"ipv6-private"`) {
		t.Fatalf("missing deny log: %s", log.String())
	}
}

func TestBrokerIPv6Unreachable(t *testing.T) {
	t.Parallel()
	for _, protocol := range []string{ProtoTCP, ProtoUDP} {
		t.Run(protocol, func(t *testing.T) {
			t.Parallel()
			policy, err := ParsePolicy([]byte(`{}`), "ipv6")
			if err != nil {
				t.Fatal(err)
			}
			log := &syncBuf{}
			broker, err := New(Config{Subnet: netip.MustParsePrefix("192.168.127.0/24"), Gateway: netip.MustParseAddr("192.168.127.1"), GuestIP: netip.MustParseAddr("192.168.127.2"), Log: log}, policy)
			if err != nil {
				t.Fatal(err)
			}
			broker.dialTCP = func(_, _ string) (net.Conn, error) { return nil, syscall.ENETUNREACH }
			broker.dialUDP = func(_ string, _, _ *net.UDPAddr) (*net.UDPConn, error) { return nil, syscall.ENETUNREACH }
			guest := connectTestBroker(t, broker)
			dialctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
			defer cancel()
			remote := tcpip.FullAddress{NIC: 1, Addr: tcpip.AddrFrom16(netip.MustParseAddr("2606:4700:4700::1111").As16()), Port: 443}
			if protocol == ProtoTCP {
				conn, err := gonet.DialContextTCP(dialctx, guest, remote, ipv6.ProtocolNumber)
				if conn != nil {
					_ = conn.Close()
				}
				if err == nil || !strings.Contains(err.Error(), "refused") {
					t.Fatalf("unreachable IPv6 TCP = %v, want RST", err)
				}
			} else {
				conn, err := gonet.DialUDP(guest, nil, &remote, ipv6.ProtocolNumber)
				if err != nil {
					t.Fatal(err)
				}
				defer func() { _ = conn.Close() }()
				_ = conn.SetDeadline(time.Now().Add(time.Second))
				if _, err := conn.Write([]byte("unreachable")); err != nil {
					t.Fatal(err)
				}
				_, err = conn.Read(make([]byte, 32))
				var timeout net.Error
				if !errors.As(err, &timeout) || !timeout.Timeout() {
					t.Fatalf("unreachable IPv6 UDP = %v, want dropped datagram", err)
				}
			}
			if !strings.Contains(log.String(), `"event":"dial-error"`) {
				t.Fatalf("missing dial failure: %s", log.String())
			}
		})
	}
}

func TestBrokerIPv6Flows(t *testing.T) {
	t.Parallel()
	for _, protocol := range []string{ProtoTCP, ProtoUDP} {
		t.Run(protocol, func(t *testing.T) {
			t.Parallel()
			ctx := t.Context()
			policy, err := ParsePolicy([]byte(`{}`), "ipv6")
			if err != nil {
				t.Fatal(err)
			}
			log := &syncBuf{}
			broker, err := New(Config{Subnet: netip.MustParsePrefix("192.168.127.0/24"), Gateway: netip.MustParseAddr("192.168.127.1"), GuestIP: netip.MustParseAddr("192.168.127.2"), Log: log}, policy)
			if err != nil {
				t.Fatal(err)
			}
			target := netip.MustParseAddrPort("[2606:4700:4700::1111]:443")
			dialed := make(chan string, 1)
			if protocol == ProtoTCP {
				listener, err := net.Listen("tcp6", "[::1]:0")
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { _ = listener.Close() })
				go func() {
					conn, err := listener.Accept()
					if err == nil {
						defer func() { _ = conn.Close() }()
						_, _ = io.Copy(conn, conn)
					}
				}()
				broker.dialTCP = func(network, address string) (net.Conn, error) {
					dialed <- address
					return net.DialTimeout(network, listener.Addr().String(), time.Second)
				}
			} else {
				listener, err := net.ListenUDP("udp6", &net.UDPAddr{IP: net.IPv6loopback})
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { _ = listener.Close() })
				go func() {
					buffer := make([]byte, 1024)
					count, peer, err := listener.ReadFromUDP(buffer)
					if err == nil {
						_, _ = listener.WriteToUDP(buffer[:count], peer)
					}
				}()
				broker.dialUDP = func(network string, local, remote *net.UDPAddr) (*net.UDPConn, error) {
					dialed <- remote.String()
					return net.DialUDP(network, local, listener.LocalAddr().(*net.UDPAddr))
				}
			}
			guest := connectTestBroker(t, broker)
			dialctx, cancel := context.WithTimeout(ctx, 10*time.Second)
			defer cancel()
			remote := tcpip.FullAddress{NIC: 1, Addr: tcpip.AddrFrom16(target.Addr().As16()), Port: target.Port()}
			var conn net.Conn
			if protocol == ProtoTCP {
				conn, err = gonet.DialContextTCP(dialctx, guest, remote, ipv6.ProtocolNumber)
			} else {
				conn, err = gonet.DialUDP(guest, nil, &remote, ipv6.ProtocolNumber)
			}
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = conn.Close() }()
			_ = conn.SetDeadline(time.Now().Add(10 * time.Second))
			if _, err := conn.Write([]byte("ipv6 echo")); err != nil {
				t.Fatal(err)
			}
			buffer := make([]byte, len("ipv6 echo"))
			if _, err := io.ReadFull(conn, buffer); err != nil || string(buffer) != "ipv6 echo" {
				t.Fatalf("echo = %q, %v", buffer, err)
			}
			select {
			case address := <-dialed:
				if address != target.String() {
					t.Fatalf("dialed %s, want %s", address, target)
				}
			case <-dialctx.Done():
				t.Fatal("no host dial")
			}
			if !strings.Contains(log.String(), `"allow":true`) || !strings.Contains(log.String(), `"dst":"`+target.String()+`"`) {
				t.Fatalf("missing flow log: %s", log.String())
			}
		})
	}
}
