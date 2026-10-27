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
