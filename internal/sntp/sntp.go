// Package sntp is the minimal SNTP (RFC 4330) the network broker serves on
// the gateway and jailoc-guest queries to keep the guest clock on host time.
package sntp

import (
	"encoding/binary"
	"errors"
	"fmt"
	"net"
	"time"
)

const (
	// Port is the NTP port the broker serves on the gateway.
	Port = 123
	// packetLen is the size of an SNTP packet without extensions.
	packetLen = 48
	// ntpEpochOffset is the number of seconds from 1900 (the NTP era) to 1970.
	ntpEpochOffset = 2208988800

	modeClient = 3
	modeServer = 4
	version    = 4
)

// refID marks the broker as the time source: the host clock.
var refID = [4]byte{'H', 'O', 'S', 'T'}

// Reply answers the client request req with the host clock: received is when
// req arrived and now is when the reply leaves. It returns nil when req is
// not a client request.
func Reply(req []byte, received, now time.Time) []byte {
	if len(req) < packetLen || req[0]&0x07 != modeClient {
		return nil
	}
	vn := req[0] >> 3 & 0x07
	if vn == 0 {
		vn = version
	}
	r := make([]byte, packetLen)
	r[0] = vn<<3 | modeServer // LI 0: no leap second pending
	r[1] = 1                  // stratum: primary, the host clock
	r[2] = req[2]             // poll
	r[3] = 0xec               // precision: 2^-20 s
	copy(r[12:16], refID[:])
	putTime(r[16:24], now)      // reference timestamp
	copy(r[24:32], req[40:48])  // originate: the client's transmit timestamp
	putTime(r[32:40], received) // receive timestamp
	putTime(r[40:48], now)      // transmit timestamp
	return r
}

// Offset queries the server on c once and returns how far the local clock
// is behind it (negative when ahead). c must be a connected UDP socket.
func Offset(c net.Conn, timeout time.Duration) (time.Duration, error) {
	req := make([]byte, packetLen)
	req[0] = version<<3 | modeClient
	t1 := time.Now()
	putTime(req[40:48], t1)
	if err := c.SetDeadline(t1.Add(timeout)); err != nil {
		return 0, fmt.Errorf("set deadline: %w", err)
	}
	if _, err := c.Write(req); err != nil {
		return 0, fmt.Errorf("send request: %w", err)
	}
	buf := make([]byte, 512)
	for {
		n, err := c.Read(buf)
		if err != nil {
			return 0, fmt.Errorf("read reply: %w", err)
		}
		t4 := time.Now()
		r := buf[:n]
		// A late reply to an earlier request carries another originate
		// timestamp; skip it and wait for the answer to this one.
		if n < packetLen || r[0]&0x07 != modeServer || string(r[24:32]) != string(req[40:48]) {
			continue
		}
		if r[1] == 0 || r[1] > 15 {
			return 0, errors.New("server is unsynchronized")
		}
		t2, t3 := getTime(r[32:40]), getTime(r[40:48])
		// RFC 4330: offset = ((T2 - T1) + (T3 - T4)) / 2. Strip the monotonic
		// readings so that the subtractions use wall clock time.
		return (t2.Sub(t1.Round(0)) + t3.Sub(t4.Round(0))) / 2, nil
	}
}

func putTime(b []byte, t time.Time) {
	secs := uint64(t.Unix() + ntpEpochOffset)                  //nolint:gosec // G115: times after 1900
	frac := uint64(t.Nanosecond()) << 32 / uint64(time.Second) //nolint:gosec // G115: nanoseconds are non-negative
	binary.BigEndian.PutUint32(b[0:4], uint32(secs))           //nolint:gosec // G115: NTP era 0 wraps in 2036 by design
	binary.BigEndian.PutUint32(b[4:8], uint32(frac))           //nolint:gosec // G115: below 2^32
}

func getTime(b []byte) time.Time {
	secs := int64(binary.BigEndian.Uint32(b[0:4]))
	frac := int64(binary.BigEndian.Uint32(b[4:8]))
	// Timestamps before 1968 belong to era 1 (after February 2036).
	if secs < 1<<31 {
		secs += 1 << 32
	}
	return time.Unix(secs-ntpEpochOffset, frac*int64(time.Second)>>32)
}
