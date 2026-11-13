package docker

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

type sourceType int

const (
	sourceLocal sourceType = iota
	sourceHTTP
)

func detectSourceType(value string) (sourceType, error) {
	if filepath.IsAbs(value) || strings.HasPrefix(value, "~") {
		return sourceLocal, nil
	}
	if strings.HasPrefix(value, "http://") || strings.HasPrefix(value, "https://") {
		return sourceHTTP, nil
	}
	return 0, fmt.Errorf("unsupported dockerfile source %q: must be an absolute local path, a ~/path, or an HTTP(S) URL", value)
}

func readLocalDockerfile(path string) ([]byte, error) {
	limit := int64(1 << 20)

	f, err := os.Open(path) //nolint:gosec // path is validated (absolute or tilde-expanded) in config.Validate before reaching here
	if err != nil {
		return nil, fmt.Errorf("open dockerfile %q: %w", path, err)
	}
	defer func() { _ = f.Close() }()

	lr := io.LimitReader(f, limit+1)
	data, err := io.ReadAll(lr)
	if err != nil {
		return nil, fmt.Errorf("read dockerfile %q: %w", path, err)
	}

	if int64(len(data)) > limit {
		return nil, fmt.Errorf("dockerfile at %q exceeds 1MiB limit", path)
	}

	return data, nil
}

func loadDockerfile(ctx context.Context, source string) ([]byte, error) {
	st, err := detectSourceType(source)
	if err != nil {
		return nil, fmt.Errorf("load dockerfile: %w", err)
	}

	switch st {
	case sourceLocal:
		return readLocalDockerfile(source)
	case sourceHTTP:
		return fetchDockerfile(ctx, source)
	default:
		return nil, fmt.Errorf("load dockerfile: unknown source type for %q", source)
	}
}

func fetchDockerfile(ctx context.Context, rawURL string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, fmt.Errorf("create fetch request for %s: %w", rawURL, err)
	}

	origin := req.URL
	dialer := &net.Dialer{
		Timeout: 10 * time.Second,
		Control: func(_, address string, _ syscall.RawConn) error {
			return checkFetchDialAddr(origin.Hostname(), address)
		},
	}
	transport := &http.Transport{
		DialContext:           dialer.DialContext,
		ForceAttemptHTTP2:     true,
		TLSHandshakeTimeout:   10 * time.Second,
		ResponseHeaderTimeout: 15 * time.Second,
	}
	defer transport.CloseIdleConnections()

	client := &http.Client{
		Timeout:   30 * time.Second,
		Transport: transport,
		CheckRedirect: func(next *http.Request, via []*http.Request) error {
			return checkDockerfileRedirect(origin, next, via)
		},
	}

	resp, err := client.Do(req) //nolint:gosec // URL is validated in config.Validate and confined by CheckRedirect + Dialer.Control
	if err != nil {
		return nil, fmt.Errorf("execute fetch request to %s: %w", rawURL, err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("fetch dockerfile from %s: unexpected status %d", rawURL, resp.StatusCode)
	}

	limit := int64(1 << 20)
	lr := io.LimitReader(resp.Body, limit+1)

	data, err := io.ReadAll(lr)
	if err != nil {
		return nil, fmt.Errorf("read dockerfile body from %s: %w", rawURL, err)
	}

	if int64(len(data)) > limit {
		return nil, fmt.Errorf("dockerfile at %s exceeds 1MiB limit", rawURL)
	}

	return data, nil
}

const maxDockerfileRedirects = 5

var cgnatPrefix = netip.MustParsePrefix("100.64.0.0/10")

func checkDockerfileRedirect(origin *url.URL, next *http.Request, via []*http.Request) error {
	if len(via) >= maxDockerfileRedirects {
		return fmt.Errorf("too many redirects")
	}
	if next.URL.Scheme != "http" && next.URL.Scheme != "https" {
		return fmt.Errorf("refusing redirect to unsupported scheme %q", next.URL.Scheme)
	}
	if origin.Scheme == "https" && next.URL.Scheme != "https" {
		return fmt.Errorf("refusing redirect from https to http")
	}
	if !strings.EqualFold(next.URL.Host, origin.Host) {
		return fmt.Errorf("refusing redirect to %s", next.URL.Redacted())
	}
	return nil
}

func checkFetchDialAddr(originHost, address string) error {
	ap, err := netip.ParseAddrPort(address)
	if err != nil {
		return fmt.Errorf("invalid dial address %q: %w", address, err)
	}
	ip := ap.Addr().Unmap()
	if originIP, err := netip.ParseAddr(originHost); err == nil {
		if ip == originIP.Unmap() {
			return nil
		}
		return fmt.Errorf("refusing dial to %s for configured host %s", ip, originHost)
	}
	if !isPublicFetchAddr(ip) {
		return fmt.Errorf("refusing dial to non-public address %s for host %s", ip, originHost)
	}
	return nil
}

func isPublicFetchAddr(ip netip.Addr) bool {
	if !ip.IsValid() ||
		ip.IsLoopback() ||
		ip.IsPrivate() ||
		ip.IsLinkLocalUnicast() ||
		ip.IsLinkLocalMulticast() ||
		ip.IsMulticast() ||
		ip.IsUnspecified() ||
		cgnatPrefix.Contains(ip) {
		return false
	}
	return true
}
