package docker

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestDetectSourceType(t *testing.T) {
	t.Parallel()
	cases := []struct {
		input   string
		want    sourceType
		wantErr bool
	}{
		{"/tmp/Dockerfile", sourceLocal, false},
		{"~/Dockerfile", sourceLocal, false},
		{"/absolute/path/file", sourceLocal, false},
		{"https://example.com/Dockerfile", sourceHTTP, false},
		{"http://example.com/Dockerfile", sourceHTTP, false},
		{"", 0, true},
		{"relative/path", 0, true},
		{"ftp://bad.example.com/Dockerfile", 0, true},
		{"justAName", 0, true},
	}
	for _, tc := range cases {
		t.Run(tc.input, func(t *testing.T) {
			t.Parallel()
			got, err := detectSourceType(tc.input)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("detectSourceType(%q): expected error, got nil", tc.input)
				}
				return
			}
			if err != nil {
				t.Fatalf("detectSourceType(%q): unexpected error: %v", tc.input, err)
			}
			if got != tc.want {
				t.Fatalf("detectSourceType(%q) = %v, want %v", tc.input, got, tc.want)
			}
		})
	}
}

func TestReadLocalDockerfile(t *testing.T) {
	t.Parallel()
	content := []byte("FROM ubuntu:22.04\nRUN apt-get update\n")
	f, err := os.CreateTemp("", "test-dockerfile-*")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Remove(f.Name()) }) //nolint:gosec // cleaning up temp file created in this test
	if _, err := f.Write(content); err != nil {
		t.Fatal(err)
	}
	_ = f.Close()

	got, err := readLocalDockerfile(f.Name())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if string(got) != string(content) {
		t.Fatalf("got %q, want %q", got, content)
	}
}

func TestReadLocalDockerfileNotFound(t *testing.T) {
	t.Parallel()
	_, err := readLocalDockerfile("/nonexistent/path/Dockerfile")
	if err == nil {
		t.Fatal("expected error for nonexistent file, got nil")
	}
}

func TestReadLocalDockerfileTooLarge(t *testing.T) {
	t.Parallel()
	f, err := os.CreateTemp("", "test-dockerfile-large-*")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Remove(f.Name()) }) //nolint:gosec // cleaning up temp file created in this test
	data := make([]byte, (1<<20)+1)
	for i := range data {
		data[i] = 'A'
	}
	if _, err := f.Write(data); err != nil {
		t.Fatal(err)
	}
	_ = f.Close()

	_, err = readLocalDockerfile(f.Name())
	if err == nil {
		t.Fatal("expected error for oversized file, got nil")
	}
	errStr := err.Error()
	if !strings.Contains(errStr, "too large") && !strings.Contains(errStr, "exceeds") && !strings.Contains(errStr, "1MiB") {
		t.Fatalf("expected error to mention size limit, got: %v", err)
	}
}

func TestLoadDockerfile(t *testing.T) {
	t.Parallel()

	content := []byte("FROM ubuntu:22.04\n")
	f, err := os.CreateTemp("", "test-dockerfile-*")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Remove(f.Name()) }) //nolint:gosec // cleaning up temp file created in this test
	if _, err := f.Write(content); err != nil {
		t.Fatal(err)
	}
	_ = f.Close()

	got, err := loadDockerfile(t.Context(), f.Name())
	if err != nil {
		t.Fatalf("loadDockerfile(local): unexpected error: %v", err)
	}
	if string(got) != string(content) {
		t.Fatalf("loadDockerfile(local): got %q, want %q", got, content)
	}

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("FROM alpine:3.18\n"))
	}))
	t.Cleanup(ts.Close)

	httpGot, err := loadDockerfile(t.Context(), ts.URL+"/Dockerfile")
	if err != nil {
		t.Fatalf("loadDockerfile(HTTP): unexpected error: %v", err)
	}
	if string(httpGot) != "FROM alpine:3.18\n" {
		t.Fatalf("loadDockerfile(HTTP): got %q, want %q", httpGot, "FROM alpine:3.18\n")
	}
}

func TestFetchDockerfile(t *testing.T) {
	t.Parallel()

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/success":
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("FROM ubuntu:24.04\nRUN echo 'success'"))
		case "/notfound":
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte("not found"))
		case "/servererror":
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = w.Write([]byte("internal server error"))
		case "/empty":
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(""))
		case "/toolarge":
			w.WriteHeader(http.StatusOK)
			data := make([]byte, 1<<20+10)
			_, _ = w.Write(data)
		case "/exactlimit":
			w.WriteHeader(http.StatusOK)
			data := make([]byte, 1<<20)
			_, _ = w.Write(data)
		case "/timeout":
			time.Sleep(100 * time.Millisecond)
			w.WriteHeader(http.StatusOK)
		default:
			w.WriteHeader(http.StatusBadRequest)
		}
	}))
	t.Cleanup(ts.Close)

	tests := []struct {
		name    string
		path    string
		ctxFn   func() (context.Context, context.CancelFunc)
		wantErr bool
		errText string
	}{
		{
			name:    "success",
			path:    "/success",
			ctxFn:   func() (context.Context, context.CancelFunc) { return context.WithCancel(context.Background()) },
			wantErr: false,
		},
		{
			name:    "not found",
			path:    "/notfound",
			ctxFn:   func() (context.Context, context.CancelFunc) { return context.WithCancel(context.Background()) },
			wantErr: true,
			errText: "unexpected status 404",
		},
		{
			name:    "server error",
			path:    "/servererror",
			ctxFn:   func() (context.Context, context.CancelFunc) { return context.WithCancel(context.Background()) },
			wantErr: true,
			errText: "unexpected status 500",
		},
		{
			name:    "empty body",
			path:    "/empty",
			ctxFn:   func() (context.Context, context.CancelFunc) { return context.WithCancel(context.Background()) },
			wantErr: false,
		},
		{
			name:    "too large body",
			path:    "/toolarge",
			ctxFn:   func() (context.Context, context.CancelFunc) { return context.WithCancel(context.Background()) },
			wantErr: true,
			errText: "exceeds 1MiB limit",
		},
		{
			name:    "exact limit body",
			path:    "/exactlimit",
			ctxFn:   func() (context.Context, context.CancelFunc) { return context.WithCancel(context.Background()) },
			wantErr: false,
		},
		{
			name: "context cancelled",
			path: "/success",
			ctxFn: func() (context.Context, context.CancelFunc) {
				ctx, cancel := context.WithCancel(context.Background())
				cancel()
				return ctx, cancel
			},
			wantErr: true,
			errText: "execute fetch request to",
		},
		{
			name: "context timeout",
			path: "/timeout",
			ctxFn: func() (context.Context, context.CancelFunc) {
				return context.WithTimeout(context.Background(), 10*time.Millisecond)
			},
			wantErr: true,
			errText: "execute fetch request to",
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			ctx, cancel := tt.ctxFn()
			defer cancel()

			data, err := fetchDockerfile(ctx, ts.URL+tt.path)

			if tt.wantErr {
				if err == nil {
					t.Fatalf("expected error, got nil")
				}
				if tt.errText != "" && !strings.Contains(err.Error(), tt.errText) {
					t.Errorf("expected error containing %q, got %q", tt.errText, err.Error())
				}
			} else {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				if tt.path == "/success" && !strings.Contains(string(data), "FROM ubuntu:24.04") {
					t.Errorf("expected body to contain FROM ubuntu:24.04, got %q", string(data))
				}
				if tt.path == "/empty" && len(data) != 0 {
					t.Errorf("expected empty body, got %d bytes", len(data))
				}
			}
		})
	}
}

func TestFetchDockerfileRedirectConfinement(t *testing.T) {
	t.Parallel()

	var internalHits atomic.Int32
	internalSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		internalHits.Add(1)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("FROM scratch\n"))
	}))
	t.Cleanup(internalSrv.Close)

	publisherSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/redirect-other-origin":
			http.Redirect(w, r, internalSrv.URL+"/internal-data", http.StatusFound)
		case "/redirect-same-origin":
			http.Redirect(w, r, "/final", http.StatusFound)
		case "/final":
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("FROM ubuntu:24.04\n"))
		case "/loop":
			http.Redirect(w, r, "/loop", http.StatusFound)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(publisherSrv.Close)

	t.Run("refuses cross-origin redirect without contacting target", func(t *testing.T) {
		t.Parallel()
		_, err := fetchDockerfile(t.Context(), publisherSrv.URL+"/redirect-other-origin")
		if err == nil || !strings.Contains(err.Error(), "refusing redirect") {
			t.Fatalf("expected refusing redirect error, got %v", err)
		}
		if got := internalHits.Load(); got != 0 {
			t.Fatalf("internal server received %d hits, want 0", got)
		}
	})

	t.Run("allows same-origin redirect", func(t *testing.T) {
		t.Parallel()
		data, err := fetchDockerfile(t.Context(), publisherSrv.URL+"/redirect-same-origin")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if string(data) != "FROM ubuntu:24.04\n" {
			t.Fatalf("got %q, want %q", data, "FROM ubuntu:24.04\n")
		}
	})

	t.Run("refuses excessive redirects", func(t *testing.T) {
		t.Parallel()
		_, err := fetchDockerfile(t.Context(), publisherSrv.URL+"/loop")
		if err == nil || !strings.Contains(err.Error(), "too many redirects") {
			t.Fatalf("expected too many redirects error, got %v", err)
		}
	})

	t.Run("refuses https to http downgrade", func(t *testing.T) {
		t.Parallel()
		origin, err := url.Parse("https://example.com/Dockerfile")
		if err != nil {
			t.Fatal(err)
		}
		nextReq, err := http.NewRequest(http.MethodGet, "http://example.com/Dockerfile", nil)
		if err != nil {
			t.Fatal(err)
		}
		err = checkDockerfileRedirect(origin, "", nextReq, []*http.Request{nextReq})
		if err == nil || !strings.Contains(err.Error(), "https to http") {
			t.Fatalf("expected https to http error, got %v", err)
		}
	})

	t.Run("refuses http to https to http downgrade across hops", func(t *testing.T) {
		t.Parallel()
		origin, err := url.Parse("http://example.com/a")
		if err != nil {
			t.Fatal(err)
		}
		hop1, err := http.NewRequest(http.MethodGet, "http://example.com/a", nil)
		if err != nil {
			t.Fatal(err)
		}
		hop2, err := http.NewRequest(http.MethodGet, "https://example.com/b", nil)
		if err != nil {
			t.Fatal(err)
		}
		nextReq, err := http.NewRequest(http.MethodGet, "http://example.com/c", nil)
		if err != nil {
			t.Fatal(err)
		}
		err = checkDockerfileRedirect(origin, "", nextReq, []*http.Request{hop1, hop2})
		if err == nil || !strings.Contains(err.Error(), "https to http") {
			t.Fatalf("expected https to http error after TLS upgrade hop, got %v", err)
		}
	})
}

func TestFetchDockerfileHonorsHTTPProxy(t *testing.T) {
	var proxyHits atomic.Int32
	var gotConnectHost string
	proxySrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		proxyHits.Add(1)
		if r.Method == http.MethodConnect {
			gotConnectHost = r.Host
			w.WriteHeader(http.StatusBadGateway)
			return
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("FROM ubuntu:24.04\n"))
	}))
	t.Cleanup(proxySrv.Close)

	origLookup := lookupFetchIPs
	t.Cleanup(func() { lookupFetchIPs = origLookup })
	lookupFetchIPs = func(_ context.Context, host string) ([]netip.Addr, error) {
		switch host {
		case "builds.example.com":
			return []netip.Addr{netip.MustParseAddr("93.184.216.34")}, nil
		case "rebind.example.com":
			return []netip.Addr{netip.MustParseAddr("169.254.169.254")}, nil
		default:
			return nil, errors.New("dns failure")
		}
	}

	t.Setenv("HTTP_PROXY", proxySrv.URL)
	t.Setenv("HTTPS_PROXY", proxySrv.URL)
	t.Setenv("NO_PROXY", "")

	data, err := fetchDockerfile(t.Context(), "http://93.184.216.34/Dockerfile")
	if err != nil {
		t.Fatalf("fetchDockerfile via HTTP_PROXY failed: %v", err)
	}
	if string(data) != "FROM ubuntu:24.04\n" {
		t.Fatalf("got %q, want %q", data, "FROM ubuntu:24.04\n")
	}
	if got := proxyHits.Load(); got != 1 {
		t.Fatalf("proxy hits = %d, want 1", got)
	}

	// HTTPS hostname fetch pins the CONNECT target sent to the proxy to the validated IP.
	_, _ = fetchDockerfile(t.Context(), "https://builds.example.com:8443/Dockerfile")
	if gotConnectHost != "93.184.216.34:8443" {
		t.Fatalf("proxy CONNECT host = %q, want 93.184.216.34:8443", gotConnectHost)
	}
	if got := proxyHits.Load(); got != 2 {
		t.Fatalf("proxy hits after HTTPS CONNECT = %d, want 2", got)
	}

	if _, err := fetchDockerfile(t.Context(), "http://builds.example.com:8080/Dockerfile"); err == nil {
		t.Fatal("expected error for proxied plain-http hostname target, got nil")
	}
	if _, err := fetchDockerfile(t.Context(), "http://internal.localhost/Dockerfile"); err == nil {
		t.Fatal("expected error for proxied .localhost target, got nil")
	}
	if _, err := fetchDockerfile(t.Context(), "https://rebind.example.com/Dockerfile"); err == nil {
		t.Fatal("expected error for proxied hostname resolving to non-public IP, got nil")
	}
	if _, err := fetchDockerfile(t.Context(), "https://unresolvable.example.com/Dockerfile"); err == nil {
		t.Fatal("expected error when proxied hostname DNS lookup fails, got nil")
	}
	if got := proxyHits.Load(); got != 2 {
		t.Fatalf("proxy hits after rejected targets = %d, want 2", got)
	}
}

func TestCheckFetchDialAddr(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name       string
		originHost string
		address    string
		wantErr    bool
	}{
		{"public IPv4 for hostname", "example.com", "93.184.216.34:443", false},
		{"public IPv6 for hostname", "example.com", "[2606:2800:220:1:248:1893:25c8:1946]:443", false},
		{"loopback IPv4 for hostname", "example.com", "127.0.0.1:8080", true},
		{"loopback IPv6 for hostname", "example.com", "[::1]:8080", true},
		{"private RFC1918 for hostname", "example.com", "10.0.0.1:80", true},
		{"link-local metadata for hostname", "example.com", "169.254.169.254:80", true},
		{"cgnat for hostname", "example.com", "100.64.0.1:80", true},
		{"unspecified for hostname", "example.com", "0.0.0.0:80", true},
		{"matching literal loopback allowed", "127.0.0.1", "127.0.0.1:8080", false},
		{"mismatched literal loopback refused", "127.0.0.1", "127.0.0.2:8080", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			err := checkFetchDialAddr(tt.originHost, tt.address)
			if (err != nil) != tt.wantErr {
				t.Fatalf("checkFetchDialAddr(%q, %q) error = %v, wantErr %v", tt.originHost, tt.address, err, tt.wantErr)
			}
		})
	}
}
