package netbroker

import (
	"crypto/tls"
	"net"
	"slices"
	"testing"
	"time"
)

// clientHelloBytes captures the first TLS record a client sends.
func clientHelloBytes(t *testing.T, serverName string, alpn []string) []byte {
	t.Helper()
	client, server := net.Pipe()
	t.Cleanup(func() { _ = client.Close(); _ = server.Close() })
	go func() {
		c := tls.Client(client, &tls.Config{ServerName: serverName, NextProtos: alpn, MinVersion: tls.VersionTLS12})
		_ = c.Handshake()
	}()
	_ = server.SetReadDeadline(time.Now().Add(5 * time.Second))
	hdr := make([]byte, 5)
	if _, err := readFull(server, hdr); err != nil {
		t.Fatalf("read record header: %v", err)
	}
	body := make([]byte, int(hdr[3])<<8|int(hdr[4]))
	if _, err := readFull(server, body); err != nil {
		t.Fatalf("read record body: %v", err)
	}
	return append(hdr, body...)
}

func readFull(c net.Conn, b []byte) (int, error) {
	n := 0
	for n < len(b) {
		m, err := c.Read(b[n:])
		n += m
		if err != nil {
			return n, err
		}
	}
	return n, nil
}

func TestIdentify(t *testing.T) {
	t.Parallel()
	hello := clientHelloBytes(t, "api.example.com", []string{"h2", "http/1.1"})
	tests := []struct {
		name     string
		data     []byte
		want     map[string]any
		wantMore bool
	}{
		{name: "tls client hello", data: hello, want: map[string]any{"proto": "tls", "sni": "api.example.com", "alpn": []string{"h2", "http/1.1"}}},
		{name: "partial tls record", data: hello[:40], wantMore: true},
		{name: "http request", data: []byte("POST /v1/items?token=secret HTTP/1.1\r\nHost: api.example.com\r\nContent-Length: 0\r\n\r\n"),
			want: map[string]any{"proto": "http", "method": "POST", "path": "/v1/items", "host": "api.example.com"}},
		{name: "http request line only", data: []byte("GET / HTTP/1.1\r\n"), wantMore: true},
		{name: "partial method", data: []byte("GE"), wantMore: true},
		{name: "ssh banner", data: []byte("SSH-2.0-OpenSSH_9.6\r\n")},
		{name: "binary", data: []byte{0x00, 0x01, 0x02}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, more := identify(tt.data)
			if more != tt.wantMore {
				t.Fatalf("more = %v, want %v (info %v)", more, tt.wantMore, got)
			}
			if len(got) != len(tt.want) {
				t.Fatalf("info = %v, want %v", got, tt.want)
			}
			for k, v := range tt.want {
				switch want := v.(type) {
				case []string:
					if g, _ := got[k].([]string); !slices.Equal(g, want) {
						t.Fatalf("%s = %v, want %v", k, got[k], want)
					}
				default:
					if got[k] != want {
						t.Fatalf("%s = %v, want %v", k, got[k], want)
					}
				}
			}
		})
	}
}

func TestSniffConnAcrossReads(t *testing.T) {
	t.Parallel()
	hello := clientHelloBytes(t, "split.example", nil)
	var got []map[string]any
	c := &sniffConn{report: func(info map[string]any) { got = append(got, info) }}
	c.inspect(hello[:100])
	c.inspect(hello[100:])
	c.inspect([]byte("more application data"))
	if len(got) != 1 || got[0]["sni"] != "split.example" {
		t.Fatalf("reports = %v, want one with sni split.example", got)
	}
}

func TestIdentifyForPolicyRequestLine(t *testing.T) {
	t.Parallel()
	const hdr = "Host: example.com\r\n\r\n"
	tests := []struct {
		name     string
		in       string
		wantInfo bool
		wantBad  bool
		wantMore bool
	}{
		{name: "valid request", in: "POST /x HTTP/1.1\r\n" + hdr, wantInfo: true},
		{name: "extension method with a tab", in: "PROPFIND\t/forbidden HTTP/1.1\r\n" + hdr, wantInfo: true, wantBad: true},
		{name: "extension method with a tab, line only", in: "PROPFIND\t/forbidden HTTP/1.1\r\n", wantInfo: true, wantBad: true},
		{name: "extension method with a tab, no line end yet", in: "PROPFIND\t/forbidden HTTP/1.1", wantMore: true},
		{name: "extension method and a tab so far", in: "PROPFIND\t", wantMore: true},
		{name: "known method with a tab", in: "POST\t/x HTTP/1.1\r\n" + hdr, wantInfo: true, wantBad: true},
		{name: "leading form feed", in: "\fPOST /x HTTP/1.1\r\n" + hdr, wantInfo: true, wantBad: true},
		{name: "leading empty lines", in: "\r\n\r\nPOST /x HTTP/1.1\r\n" + hdr, wantInfo: true, wantBad: true},
		{name: "leading empty lines so far", in: "\r\n\r\n", wantMore: true},
		{name: "extra word in the line", in: "GET /a b HTTP/1.1\r\n" + hdr, wantInfo: true, wantBad: true},
		{name: "line-based protocol", in: "SET k v\r\n"},
		{name: "line-based protocol so far", in: "SET k v", wantMore: true},
		{name: "ssh banner", in: "SSH-2.0-OpenSSH_9.6\r\n"},
		{name: "binary", in: "\x00\x01\x02\x03\r\n"},
		{name: "partial method", in: "GE", wantMore: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			info, more := identifyForPolicy([]byte(tt.in))
			if (info != nil) != tt.wantInfo || more != tt.wantMore {
				t.Fatalf("identifyForPolicy(%q) = %v, more=%v; want info=%v more=%v", tt.in, info, more, tt.wantInfo, tt.wantMore)
			}
			if _, bad := sniffedPeerNameStrict(info); bad != tt.wantBad {
				t.Fatalf("identifyForPolicy(%q) bad authority = %v, want %v (info %v)", tt.in, bad, tt.wantBad, info)
			}
		})
	}
}

func TestEnforceConnRequestLine(t *testing.T) {
	t.Parallel()
	const tail = "Host: allowed.example.com\r\n\r\n"
	tests := []struct {
		name   string
		chunks []string
		// want is the verdict on each chunk: true while the stream may go on.
		want []bool
	}{
		{"extension method with a tab", []string{"PROPFIND\t/forbidden HTTP/1.1\r\n" + tail}, []bool{false}},
		{"extension method with a tab, fragmented", []string{"PROPFIND\t/forb", "idden HTTP/1.1\r\n" + tail}, []bool{true, false}},
		{"leading empty lines", []string{"\r\n\r\nPOST /x HTTP/1.1\r\n" + tail}, []bool{false}},
		{"leading empty lines, fragmented", []string{"\r\n\r\n", "POST /x HTTP/1.1\r\n" + tail}, []bool{true, false}},
		{"leading form feed", []string{"\fPOST /x HTTP/1.1\r\n" + tail}, []bool{false}},
		{"valid request", []string{"POST /x HTTP/1.1\r\n" + tail}, []bool{true}},
		{"line-based protocol, fragmented", []string{"SE", "T k v\r", "\n", "anything\r\n\r\n"}, []bool{true, true, true, true}},
		{"ssh banner", []string{"SSH-2.0-OpenSSH_9.6\r\n", "POST\t/x HTTP/1.1\r\n" + tail}, []bool{true, true}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			ec := &enforceConn{check: allowOnly("allowed.example.com"), abort: func() {}}
			for i, chunk := range tt.chunks {
				if got := ec.inspect([]byte(chunk)); got != tt.want[i] {
					t.Fatalf("inspect(chunk %d %q) = %v, want %v", i, chunk, got, tt.want[i])
				}
			}
		})
	}
}

func TestSniffBeforeDialRequestLine(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		chunks   []string
		wantBad  bool
		wantInfo bool
	}{
		{"extension method with a tab", []string{"PROPFIND\t/forbidden HTTP/1.1\r\nHost: example.com\r\n\r\n"}, true, true},
		{"extension method with a tab, fragmented", []string{"PROPFIND\t/forb", "idden HTTP/1.1\r\nHost: example.com\r\n\r\n"}, true, true},
		{"leading empty lines, fragmented", []string{"\r\n", "\r\nPOST /x HTTP/1.1\r\nHost: example.com\r\n\r\n"}, true, true},
		{"valid request", []string{"POST /x HTTP/1.1\r\nHost: example.com\r\n\r\n"}, false, true},
		{"line-based protocol, fragmented", []string{"SE", "T k v\r", "\n"}, false, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			client, server := net.Pipe()
			t.Cleanup(func() { _ = client.Close(); _ = server.Close() })
			go func() {
				for _, chunk := range tt.chunks {
					if _, err := client.Write([]byte(chunk)); err != nil {
						return
					}
					time.Sleep(10 * time.Millisecond)
				}
			}()
			_, info, _ := sniffBeforeDial(server, 2*time.Second)
			if (info != nil) != tt.wantInfo {
				t.Fatalf("info = %v, want info %v", info, tt.wantInfo)
			}
			if _, bad := sniffedPeerNameStrict(info); bad != tt.wantBad {
				t.Fatalf("bad authority = %v, want %v (info %v)", bad, tt.wantBad, info)
			}
		})
	}
}
