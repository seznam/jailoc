package docker

import (
	"context"
	"crypto/tls"
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

	"golang.org/x/net/http/httpproxy"
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

	originCopy := *req.URL
	origin := &originCopy
	proxyURL, err := httpproxy.FromEnvironment().ProxyFunc()(origin)
	if err != nil {
		return nil, fmt.Errorf("resolve proxy for %s: %w", rawURL, err)
	}
	var pinnedHost string
	var proxyHost string
	if proxyURL != nil {
		pinnedHost, err = resolveProxiedTarget(ctx, origin.Scheme, origin.Hostname(), origin.Host)
		if err != nil {
			return nil, fmt.Errorf("execute fetch request to %s: %w", rawURL, err)
		}
		proxyHost = proxyURL.Hostname()
		req.Host = origin.Host
		req.URL.Host = pinnedHost
	}

	dialer := &net.Dialer{
		Timeout: 10 * time.Second,
		Control: func(_, address string, _ syscall.RawConn) error {
			if proxyHost != "" {
				return checkProxyDialAddr(proxyHost, address)
			}
			return checkFetchDialAddr(origin.Hostname(), address)
		},
	}
	transport := &http.Transport{
		Proxy: func(_ *http.Request) (*url.URL, error) {
			return proxyURL, nil
		},
		DialContext:           dialer.DialContext,
		ForceAttemptHTTP2:     true,
		TLSHandshakeTimeout:   10 * time.Second,
		ResponseHeaderTimeout: 15 * time.Second,
		TLSClientConfig: &tls.Config{
			ServerName: origin.Hostname(),
			MinVersion: tls.VersionTLS12,
		},
	}
	defer transport.CloseIdleConnections()

	client := &http.Client{
		Timeout:   30 * time.Second,
		Transport: transport,
		CheckRedirect: func(next *http.Request, via []*http.Request) error {
			if err := checkDockerfileRedirect(origin, pinnedHost, next, via); err != nil {
				return err
			}
			if pinnedHost != "" {
				next.Host = origin.Host
				next.URL.Host = pinnedHost
			}
			return nil
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

var (
	cgnatPrefix    = netip.MustParsePrefix("100.64.0.0/10")
	lookupFetchIPs = func(ctx context.Context, host string) ([]netip.Addr, error) {
		return net.DefaultResolver.LookupNetIP(ctx, "ip", host)
	}
)

func checkDockerfileRedirect(origin *url.URL, pinnedHost string, next *http.Request, via []*http.Request) error {
	if len(via) >= maxDockerfileRedirects {
		return fmt.Errorf("too many redirects")
	}
	if next.URL.Scheme != "http" && next.URL.Scheme != "https" {
		return fmt.Errorf("refusing redirect to unsupported scheme %q", next.URL.Scheme)
	}
	prev := origin
	if len(via) > 0 && via[len(via)-1].URL != nil {
		prev = via[len(via)-1].URL
	}
	if (origin.Scheme == "https" || prev.Scheme == "https") && next.URL.Scheme != "https" {
		return fmt.Errorf("refusing redirect from https to http")
	}
	sameOrigin := strings.EqualFold(next.URL.Host, origin.Host) ||
		(pinnedHost != "" && strings.EqualFold(next.URL.Host, pinnedHost))
	if !sameOrigin {
		return fmt.Errorf("refusing redirect to %s", next.URL.Redacted())
	}
	return nil
}

func resolveProxiedTarget(ctx context.Context, scheme, originHost, targetHostPort string) (string, error) {
	host, port := splitHostOptionalPort(targetHostPort)
	lower := strings.ToLower(strings.TrimSuffix(host, "."))
	if lower == "localhost" || strings.HasSuffix(lower, ".localhost") {
		return "", fmt.Errorf("refusing proxied fetch to localhost target %q", host)
	}
	if ip, err := netip.ParseAddr(host); err == nil {
		unmapped := ip.Unmap()
		if originIP, err := netip.ParseAddr(originHost); err == nil && unmapped == originIP.Unmap() {
			return joinHostOptionalPort(unmapped.String(), port), nil
		}
		if !isPublicFetchAddr(unmapped) {
			return "", fmt.Errorf("refusing proxied fetch to non-public address %s", unmapped)
		}
		return joinHostOptionalPort(unmapped.String(), port), nil
	}
	if scheme != "https" {
		return "", fmt.Errorf("refusing proxied plain-http fetch for hostname %s: use https or an IP literal so destination IP can be pinned", host)
	}
	ips, err := lookupFetchIPs(ctx, host)
	if err != nil {
		return "", fmt.Errorf("resolve proxied host %s: %w", host, err)
	}
	if len(ips) == 0 {
		return "", fmt.Errorf("resolve proxied host %s: no addresses", host)
	}
	for _, ip := range ips {
		if !isPublicFetchAddr(ip.Unmap()) {
			return "", fmt.Errorf("refusing proxied fetch to non-public address %s for host %s", ip.Unmap(), host)
		}
	}
	return joinHostOptionalPort(ips[0].Unmap().String(), port), nil
}

func splitHostOptionalPort(hostPort string) (host, port string) {
	if h, p, err := net.SplitHostPort(hostPort); err == nil {
		return h, p
	}
	return strings.TrimSuffix(strings.TrimPrefix(hostPort, "["), "]"), ""
}

func joinHostOptionalPort(host, port string) string {
	if port != "" {
		return net.JoinHostPort(host, port)
	}
	if strings.Contains(host, ":") {
		return "[" + host + "]"
	}
	return host
}
func checkProxyDialAddr(proxyHost, address string) error {
	ap, err := netip.ParseAddrPort(address)
	if err != nil {
		return fmt.Errorf("invalid proxy dial address %q: %w", address, err)
	}
	if proxyIP, err := netip.ParseAddr(proxyHost); err == nil {
		if ap.Addr().Unmap() != proxyIP.Unmap() {
			return fmt.Errorf("refusing dial to %s for configured proxy %s", ap.Addr().Unmap(), proxyHost)
		}
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
