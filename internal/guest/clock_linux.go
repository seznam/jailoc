//go:build linux

package guest

import (
	"fmt"
	"log/slog"
	"net"
	"strconv"
	"time"

	"github.com/stacklok/go-microvm/net/topology"
	"golang.org/x/sys/unix"

	"github.com/seznam/jailoc/internal/sntp"
)

const (
	// clockSyncInterval is how often the guest compares its clock with the
	// host. The guest clock stops while the host sleeps, so after a wake-up
	// it is behind for at most this long.
	clockSyncInterval = 30 * time.Second
	// clockSyncTimeout bounds one query to the broker.
	clockSyncTimeout = 2 * time.Second
	// clockStepThreshold is the smallest offset the guest corrects. The guest
	// counter runs at the host rate, so only host sleep and the whole-second
	// RTC read at boot exceed it.
	clockStepThreshold = 500 * time.Millisecond
)

// startClockSync keeps the guest clock on host time: it queries the broker's
// SNTP service on the gateway right away and then every clockSyncInterval,
// and steps the clock when it is off by more than clockStepThreshold. Init
// keeps CAP_SYS_TIME even when sessions lose it.
func startClockSync(logger *slog.Logger) (stop func()) {
	done := make(chan struct{})
	go func() {
		t := time.NewTicker(clockSyncInterval)
		defer t.Stop()
		failing := false
		for {
			stepped, err := syncClock()
			switch {
			case err != nil && !failing:
				logger.Warn("sync clock with host", "error", err)
			case err == nil && failing:
				logger.Info("clock sync with host recovered")
			case stepped != 0:
				logger.Info("stepped clock to host time", "offset", stepped)
			}
			failing = err != nil
			select {
			case <-done:
				return
			case <-t.C:
			}
		}
	}()
	return func() { close(done) }
}

// syncClock queries the host time once and steps the guest clock when it is
// off by more than clockStepThreshold. It returns the applied offset, or 0
// when the clock was left alone.
func syncClock() (time.Duration, error) {
	c, err := net.Dial("udp", net.JoinHostPort(topology.GatewayIP, strconv.Itoa(sntp.Port)))
	if err != nil {
		return 0, fmt.Errorf("dial host time service: %w", err)
	}
	defer func() { _ = c.Close() }()
	offset, err := sntp.Offset(c, clockSyncTimeout)
	if err != nil {
		return 0, fmt.Errorf("query host time: %w", err)
	}
	if offset.Abs() < clockStepThreshold {
		return 0, nil
	}
	ts := unix.NsecToTimespec(time.Now().Add(offset).UnixNano())
	if err := unix.ClockSettime(unix.CLOCK_REALTIME, &ts); err != nil {
		return 0, fmt.Errorf("set clock: %w", err)
	}
	return offset, nil
}
