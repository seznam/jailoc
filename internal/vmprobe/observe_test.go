package vmprobe

import (
	"bytes"
	"context"
	"net"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

func listen(t *testing.T, handle func(net.Conn)) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go handle(c)
		}
	}()
	return ln.Addr().String()
}

func TestProbeTCPClassification(t *testing.T) {
	t.Parallel()
	closedLn, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	closedAddr := closedLn.Addr().String()
	_ = closedLn.Close()

	var held sync.WaitGroup
	held.Add(1)
	t.Cleanup(held.Done)

	tests := []struct {
		name string
		addr string
		want string
	}{
		{"refused", closedAddr, ObsBlocked},
		{"banner", listen(t, func(c net.Conn) {
			defer func() { _ = c.Close() }()
			_, _ = c.Write([]byte("hello\n"))
		}), ObsReached},
		{"accept then close", listen(t, func(c net.Conn) { _ = c.Close() }), ObsProxyClosed},
		{"accept and hold", listen(t, func(c net.Conn) {
			held.Wait()
			_ = c.Close()
		}), ObsAmbiguous},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, detail := ProbeTCP(context.Background(), tt.addr, Payload("n", "K#1", "c"), 300*time.Millisecond)
			if got != tt.want {
				t.Fatalf("ProbeTCP = %s (%s), want %s", got, detail, tt.want)
			}
		})
	}
}

func TestPayloadRoundTrip(t *testing.T) {
	t.Parallel()
	n, k, c, ok := ParsePayload(strings.TrimSpace(string(Payload("abc", "N-HOST-TCP#3", "guest-root"))))
	if !ok || n != "abc" || k != "N-HOST-TCP#3" || c != "guest-root" {
		t.Fatalf("ParsePayload = %q %q %q %v", n, k, c, ok)
	}
	if _, _, _, ok := ParsePayload("GET / HTTP/1.1"); ok {
		t.Fatal("ParsePayload accepted a foreign line")
	}
}

func TestEvaluate(t *testing.T) {
	t.Parallel()
	tests := []struct {
		expect, obs string
		want        Status
	}{
		{ExpectAllow, ObsReached, StatusPass},
		{ExpectAllow, ObsProxyClosed, StatusFail},
		{ExpectAllow, ObsBlocked, StatusFail},
		{ExpectDeny, ObsBlocked, StatusPass},
		{ExpectDeny, ObsProxyClosed, StatusPass},
		{ExpectDeny, ObsReached, StatusFail},
		{ExpectDeny, ObsAmbiguous, StatusFail},
	}
	for _, tt := range tests {
		if got := Evaluate(tt.expect, tt.obs); got != tt.want {
			t.Errorf("Evaluate(%s, %s) = %s, want %s", tt.expect, tt.obs, got, tt.want)
		}
	}
}

func TestSentinelRecordsAndAnswers(t *testing.T) {
	t.Parallel()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	_ = ln.Close()

	var log bytes.Buffer
	var mu sync.Mutex
	s := &Sentinel{Name: "test", Ports: []int{port}, Log: lockedWriter{&mu, &log}}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- s.Run(ctx) }()
	t.Cleanup(func() {
		cancel()
		if err := <-done; err != nil {
			t.Errorf("sentinel: %v", err)
		}
	})

	addr := net.JoinHostPort("127.0.0.1", strconv.Itoa(port))
	var obs string
	for range 50 {
		if obs, _ = ProbeTCP(context.Background(), addr, Payload("n1", "N-HOST-TCP#0", "guest-root"), time.Second); obs == ObsReached {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if obs != ObsReached {
		t.Fatalf("tcp probe = %s, want reached", obs)
	}
	if obs, d := ProbeUDP(context.Background(), addr, Payload("n1", "N-HOST-UDP#1", "guest-root"), time.Second); obs != ObsReached {
		t.Fatalf("udp probe = %s (%s), want reached", obs, d)
	}

	mu.Lock()
	hits, err := ReadHits(bytes.NewReader(log.Bytes()))
	mu.Unlock()
	if err != nil {
		t.Fatalf("ReadHits: %v", err)
	}
	var keys []string
	for _, h := range hits {
		if _, k, _, ok := ParsePayload(h.Payload); ok {
			keys = append(keys, h.Proto+":"+k)
		}
	}
	joined := strings.Join(keys, ",")
	for _, want := range []string{"tcp:N-HOST-TCP#0", "udp:N-HOST-UDP#1"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("hits %s missing %s", joined, want)
		}
	}
}

type lockedWriter struct {
	mu *sync.Mutex
	w  *bytes.Buffer
}

func (l lockedWriter) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.w.Write(p)
}

func TestQUICPacketShape(t *testing.T) {
	t.Parallel()
	p := QUICVersionNegotiationPacket()
	if len(p) != 1200 || p[0]&0xc0 != 0xc0 || p[5] != 8 || p[14] != 8 {
		t.Fatalf("unexpected packet header % x", p[:16])
	}
}
