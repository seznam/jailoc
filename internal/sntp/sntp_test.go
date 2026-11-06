package sntp

import (
	"net"
	"testing"
	"time"
)

func TestTimestampRoundTrip(t *testing.T) {
	t.Parallel()
	for _, want := range []time.Time{
		time.Date(2026, 11, 6, 13, 6, 33, 123456789, time.UTC),
		time.Date(2040, 1, 2, 3, 4, 5, 500000000, time.UTC), // NTP era 1
	} {
		t.Run(want.Format(time.RFC3339), func(t *testing.T) {
			t.Parallel()
			b := make([]byte, 8)
			putTime(b, want)
			if got := getTime(b); got.Sub(want).Abs() > time.Microsecond {
				t.Fatalf("getTime(putTime(%s)) = %s", want, got)
			}
		})
	}
}

func TestReply(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		req  []byte
		want bool
	}{
		{"client request", append([]byte{version<<3 | modeClient}, make([]byte, packetLen-1)...), true},
		{"version 3 client", append([]byte{3<<3 | modeClient}, make([]byte, packetLen-1)...), true},
		{"server packet", append([]byte{version<<3 | modeServer}, make([]byte, packetLen-1)...), false},
		{"short packet", []byte{version<<3 | modeClient, 0, 0}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			now := time.Now()
			r := Reply(tt.req, now, now)
			if (r != nil) != tt.want {
				t.Fatalf("Reply = % x, want reply %v", r, tt.want)
			}
			if r == nil {
				return
			}
			if r[0]&0x07 != modeServer || r[0]>>3 != tt.req[0]>>3 || r[1] != 1 {
				t.Fatalf("reply header % x", r[:4])
			}
			if string(r[24:32]) != string(tt.req[40:48]) {
				t.Fatalf("originate %x, want the request transmit timestamp %x", r[24:32], tt.req[40:48])
			}
		})
	}
}

func TestOffset(t *testing.T) {
	t.Parallel()
	for _, shift := range []time.Duration{0, 34 * time.Minute, -2 * time.Hour} {
		t.Run(shift.String(), func(t *testing.T) {
			t.Parallel()
			srv, err := net.ListenPacket("udp", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = srv.Close() })
			go func() {
				buf := make([]byte, 512)
				for {
					n, peer, err := srv.ReadFrom(buf)
					if err != nil {
						return
					}
					// A stale reply first: Offset must skip it.
					stale := Reply(buf[:n], time.Now(), time.Now())
					stale[24] ^= 0xff
					_, _ = srv.WriteTo(stale, peer)
					now := time.Now().Add(shift)
					_, _ = srv.WriteTo(Reply(buf[:n], now, now), peer)
				}
			}()
			c, err := net.Dial("udp", srv.LocalAddr().String())
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = c.Close() })
			got, err := Offset(c, 5*time.Second)
			if err != nil {
				t.Fatalf("Offset: %v", err)
			}
			if (got - shift).Abs() > 100*time.Millisecond {
				t.Fatalf("Offset = %s, want about %s", got, shift)
			}
		})
	}
}

func TestOffsetTimeout(t *testing.T) {
	t.Parallel()
	srv, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = srv.Close() })
	c, err := net.Dial("udp", srv.LocalAddr().String())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.Close() })
	if _, err := Offset(c, 100*time.Millisecond); err == nil {
		t.Fatal("Offset without a server answer succeeded")
	}
}
