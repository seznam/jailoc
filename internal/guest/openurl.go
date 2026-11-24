package guest

import (
	"bufio"
	"encoding/binary"
	"encoding/hex"
	"io"
	"net/netip"
	"net/url"
	"strconv"
	"strings"
)

const (
	minOAuthCallbackPort = 1024
	maxOAuthCallbackPath = 2048
)

// parseOAuthRedirect extracts a loopback OAuth redirect_uri target (port and
// normalized path) from an authorization URL passed to xdg-open / $BROWSER.
// Only http://127.0.0.1:<port>/..., http://localhost:<port>/..., and
// http://[::1]:<port>/... with an explicit unprivileged port (1024-65535) are
// accepted.
func parseOAuthRedirect(rawURL string) (uint16, string, bool) {
	u, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil {
		return 0, "", false
	}
	switch strings.ToLower(u.Scheme) {
	case "http", "https":
	default:
		return 0, "", false
	}
	if u.Host == "" {
		return 0, "", false
	}
	rawRedirect := u.Query().Get("redirect_uri")
	if rawRedirect == "" {
		return 0, "", false
	}
	ru, err := url.Parse(rawRedirect)
	if err != nil {
		return 0, "", false
	}
	if strings.ToLower(ru.Scheme) != "http" {
		return 0, "", false
	}
	switch strings.ToLower(ru.Hostname()) {
	case "127.0.0.1", "localhost", "::1":
	default:
		return 0, "", false
	}
	portStr := ru.Port()
	if portStr == "" {
		return 0, "", false
	}
	p, err := strconv.ParseUint(portStr, 10, 16)
	if err != nil || p < minOAuthCallbackPort {
		return 0, "", false
	}
	path := ru.Path
	if path == "" {
		path = "/"
	}
	if !validOAuthCallbackPath(path) {
		return 0, "", false
	}
	return uint16(p), path, true
}

func validOAuthCallbackPath(path string) bool {
	if len(path) == 0 || len(path) > maxOAuthCallbackPath || path[0] != '/' {
		return false
	}
	for i := range len(path) {
		c := path[i]
		if c < 0x20 || c == 0x7f || c == '?' || c == '#' {
			return false
		}
	}
	return true
}

// parseProcNetTCP reports whether r (in Linux /proc/net/tcp or /proc/net/tcp6
// format) contains a socket in TCP_LISTEN state (0A) bound to port on loopback
// or the wildcard address.
func parseProcNetTCP(r io.Reader, ipv6 bool, port uint16) bool {
	sc := bufio.NewScanner(r)
	for sc.Scan() {
		fields := strings.Fields(sc.Text())
		if len(fields) < 4 || fields[0] == "sl" {
			continue
		}
		if fields[3] != "0A" {
			continue
		}
		addrHex, portHex, ok := strings.Cut(fields[1], ":")
		if !ok {
			continue
		}
		p, err := strconv.ParseUint(portHex, 16, 16)
		if err != nil || uint16(p) != port {
			continue
		}
		ip, ok := parseProcNetIP(addrHex, ipv6)
		if !ok {
			continue
		}
		if ip.IsLoopback() || ip.IsUnspecified() {
			return true
		}
	}
	return false
}

// parseProcNetIP decodes the hex local_address column of /proc/net/tcp or
// /proc/net/tcp6. The Linux kernel prints each 32-bit word in host byte order.
func parseProcNetIP(s string, ipv6 bool) (netip.Addr, bool) {
	wantLen := 8
	if ipv6 {
		wantLen = 32
	}
	if len(s) != wantLen {
		return netip.Addr{}, false
	}
	raw, err := hex.DecodeString(s)
	if err != nil {
		return netip.Addr{}, false
	}
	// Each 4-byte chunk in /proc/net/tcp{,6} is formatted as %08X of a uint32
	// in host byte order. Converting via BigEndian -> NativeEndian restores the
	// network-order bytes on both little- and big-endian hosts.
	var out [16]byte
	for off := 0; off < len(raw); off += 4 {
		w := binary.BigEndian.Uint32(raw[off : off+4])
		binary.NativeEndian.PutUint32(out[off:off+4], w)
	}
	if !ipv6 {
		return netip.AddrFrom4([4]byte(out[:4])), true
	}
	return netip.AddrFrom16(out).Unmap(), true
}

// browserEnv returns BROWSER=XDGOpenPath when env does not already set
// BROWSER, so image or workspace configuration wins.
func browserEnv(env []string) []string {
	for _, kv := range env {
		name, _, _ := strings.Cut(kv, "=")
		if name == "BROWSER" {
			return nil
		}
	}
	return []string{"BROWSER=" + XDGOpenPath}
}
