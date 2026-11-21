//go:build linux

package gatewayhost

import (
	"strings"
	"testing"

	"golang.org/x/sys/unix"
)

// TestBrokerFrameRejectsUnknownAncillary makes the kernel attach an
// SCM_CREDENTIALS message next to a valid listener descriptor (SO_PASSCRED on
// the receiving socket) and checks that the response is refused and the
// descriptor is not leaked.
func TestBrokerFrameRejectsUnknownAncillary(t *testing.T) {
	t.Parallel()

	ln := fdLoopbackListener(t, "tcp4")
	t.Cleanup(func() { fdClose(t, "tcp listener", ln) })
	id := fdSocketIdentity(t, ln)
	server, client := fdUnixPair(t)
	if err := unix.SetsockoptInt(fdRawFD(t, client), unix.SOL_SOCKET, unix.SO_PASSCRED, 1); err != nil {
		t.Fatalf("SO_PASSCRED: %v", err)
	}
	if _, _, err := server.WriteMsgUnix([]byte{brokerFrameConn}, unix.UnixRights(fdRawFD(t, ln)), nil); err != nil {
		t.Fatalf("send frame: %v", err)
	}
	_, got, err := readBrokerFrame(client)
	fdClose(t, "unix client", client)
	if len(got) != 0 {
		for _, fd := range got {
			fdCloseRaw(t, fd)
		}
		t.Fatal("readBrokerFrame accepted a response with SCM_CREDENTIALS")
	}
	if err == nil || !strings.Contains(err.Error(), "ancillary") {
		t.Fatalf("readBrokerFrame error = %v, want unexpected ancillary message", err)
	}
	// The test's own descriptor is the only one left; the SCM_RIGHTS copy the
	// kernel installed next to the credentials was closed by the receiver.
	fdAssertOnlyPinnedListener(t, id)
	addr := ln.Addr().String()
	fdClose(t, "tcp listener", ln)
	fdAssertListenerGone(t, id, addr)
}

// TestFDSocketCopiesCountsDescriptors keeps the leak check from being
// vacuous: a duplicated descriptor of the same socket must be counted, and a
// different socket must not be.
func TestFDSocketCopiesCountsDescriptors(t *testing.T) {
	t.Parallel()

	ln := fdLoopbackListener(t, "tcp4")
	t.Cleanup(func() { fdClose(t, "tcp listener", ln) })
	other := fdLoopbackListener(t, "tcp4")
	t.Cleanup(func() { fdClose(t, "other tcp listener", other) })
	id := fdSocketIdentity(t, ln)
	fdAssertSocketCopies(t, id, 1, "one listener")

	dup, err := unix.Dup(fdRawFD(t, ln))
	if err != nil {
		t.Fatalf("Dup: %v", err)
	}
	fdAssertSocketCopies(t, id, 2, "after Dup")
	fdCloseRaw(t, dup)
	fdAssertSocketCopies(t, id, 1, "after closing the Dup")
	if fdSocketIdentity(t, other) == id {
		t.Fatal("two sockets share one identity")
	}
}
