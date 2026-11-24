package guest

import (
	"slices"
	"strings"
	"testing"
)

func TestParseOAuthRedirect(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		url      string
		wantPort uint16
		wantPath string
		wantOK   bool
	}{
		{
			name:     "ipv4 loopback with path",
			url:      "https://auth.example.com/authorize?client_id=abc&redirect_uri=http%3A%2F%2F127.0.0.1%3A19876%2Fmcp%2Foauth%2Fcallback&state=xyz",
			wantPort: 19876,
			wantPath: "/mcp/oauth/callback",
			wantOK:   true,
		},
		{
			name:     "localhost empty path normalized to slash",
			url:      "https://accounts.google.com/o/oauth2/auth?redirect_uri=http%3A%2F%2Flocalhost%3A8085",
			wantPort: 8085,
			wantPath: "/",
			wantOK:   true,
		},
		{
			name:     "ipv6 loopback",
			url:      "https://auth.example.com/authorize?redirect_uri=http%3A%2F%2F%5B%3A%3A1%5D%3A43210%2Fcb",
			wantPort: 43210,
			wantPath: "/cb",
			wantOK:   true,
		},
		{
			name:   "no redirect_uri",
			url:    "https://example.com/docs",
			wantOK: false,
		},
		{
			name:   "non-loopback redirect_uri",
			url:    "https://auth.example.com/authorize?redirect_uri=http%3A%2F%2F192.168.1.5%3A8080%2Fcb",
			wantOK: false,
		},
		{
			name:   "https redirect_uri rejected",
			url:    "https://auth.example.com/authorize?redirect_uri=https%3A%2F%2F127.0.0.1%3A8443%2Fcb",
			wantOK: false,
		},
		{
			name:   "privileged port rejected",
			url:    "https://auth.example.com/authorize?redirect_uri=http%3A%2F%2F127.0.0.1%3A80%2Fcb",
			wantOK: false,
		},
		{
			name:   "missing port rejected",
			url:    "https://auth.example.com/authorize?redirect_uri=http%3A%2F%2Flocalhost%2Fcb",
			wantOK: false,
		},
		{
			name:   "non-http outer scheme rejected",
			url:    "file:///etc/passwd?redirect_uri=http%3A%2F%2F127.0.0.1%3A8080%2Fcb",
			wantOK: false,
		},
		{
			name:   "control char in redirect path rejected",
			url:    "https://auth.example.com/authorize?redirect_uri=http%3A%2F%2F127.0.0.1%3A8080%2Fcb%0d%0a",
			wantOK: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			port, path, ok := parseOAuthRedirect(tt.url)
			if ok != tt.wantOK || port != tt.wantPort || path != tt.wantPath {
				t.Fatalf("parseOAuthRedirect(%q) = (%d, %q, %v), want (%d, %q, %v)",
					tt.url, port, path, ok, tt.wantPort, tt.wantPath, tt.wantOK)
			}
		})
	}
}

func TestParseProcNetTCP(t *testing.T) {
	t.Parallel()
	// 19876 == 0x4DA4.
	const tcpData = `  sl  local_address rem_address   st tx_queue rx_queue tr tm->when retrnsmt   uid  timeout inode
   0: 0100007F:4DA4 00000000:0000 0A 00000000:00000000 00:00000000 00000000  1000        0 12345 1 0000000000000000 100 0 0 10 0
   1: 00000000:1F90 00000000:0000 0A 00000000:00000000 00:00000000 00000000  1000        0 12346 1 0000000000000000 100 0 0 10 0
   2: 027FA8C0:2328 00000000:0000 0A 00000000:00000000 00:00000000 00000000  1000        0 12347 1 0000000000000000 100 0 0 10 0
   3: 0100007F:2710 0100007F:C350 01 00000000:00000000 00:00000000 00000000  1000        0 12348 1 0000000000000000 100 0 0 10 0
`
	const tcp6Data = `  sl  local_address                         remote_address                        st tx_queue rx_queue tr tm->when retrnsmt   uid  timeout inode
   0: 00000000000000000000000001000000:4DA5 00000000000000000000000000000000:0000 0A 00000000:00000000 00:00000000 00000000  1000        0 22345 1 0000000000000000 100 0 0 10 0
   1: 00000000000000000000000000000000:4DA6 00000000000000000000000000000000:0000 0A 00000000:00000000 00:00000000 00000000  1000        0 22346 1 0000000000000000 100 0 0 10 0
   2: 0000000000000000FFFF00000100007F:4DA7 00000000000000000000000000000000:0000 0A 00000000:00000000 00:00000000 00000000  1000        0 22347 1 0000000000000000 100 0 0 10 0
`

	tests := []struct {
		name string
		data string
		ipv6 bool
		port uint16
		want bool
	}{
		{"ipv4 127.0.0.1 LISTEN", tcpData, false, 19876, true},
		{"ipv4 0.0.0.0 LISTEN", tcpData, false, 8080, true},
		{"ipv4 non-loopback 192.168.127.2 ignored", tcpData, false, 9000, false},
		{"ipv4 ESTABLISHED state 01 ignored", tcpData, false, 10000, false},
		{"ipv4 absent port", tcpData, false, 19999, false},
		{"ipv6 ::1 LISTEN", tcp6Data, true, 19877, true},
		{"ipv6 :: LISTEN", tcp6Data, true, 19878, true},
		{"ipv6 ::ffff:127.0.0.1 LISTEN", tcp6Data, true, 19879, true},
		{"ipv6 absent port", tcp6Data, true, 19880, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := parseProcNetTCP(strings.NewReader(tt.data), tt.ipv6, tt.port); got != tt.want {
				t.Fatalf("parseProcNetTCP(ipv6=%v, port=%d) = %v, want %v", tt.ipv6, tt.port, got, tt.want)
			}
		})
	}
}

func TestBrowserEnv(t *testing.T) {
	t.Parallel()
	if got := browserEnv([]string{"PATH=/usr/bin"}); !slices.Equal(got, []string{"BROWSER=" + XDGOpenPath}) {
		t.Fatalf("browserEnv(without BROWSER) = %v, want [%s]", got, "BROWSER="+XDGOpenPath)
	}
	if got := browserEnv([]string{"BROWSER=/custom/browser"}); len(got) != 0 {
		t.Fatalf("browserEnv(with BROWSER) = %v, want empty", got)
	}
}
