package webgateway

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/httputil"
	"strconv"
	"strings"
	"time"
)

func (g *Gateway) proxy(route Route, s *session) *httputil.ReverseProxy {
	transport := &http.Transport{
		// Fresh connections prevent Transport's reused-connection retry path,
		// including POSTs labeled replayable by an upstream idempotency key.
		DisableKeepAlives: true, DisableCompression: true, ForceAttemptHTTP2: false,
		ResponseHeaderTimeout: 30 * time.Second, MaxResponseHeaderBytes: 32 << 10,
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
			stop := context.AfterFunc(s.ctx, cancel)
			defer stop()
			defer cancel()
			conn, err := route.Dial(ctx)
			if err != nil {
				return nil, fmt.Errorf("dial web route: %w", err)
			}
			if conn == nil {
				return nil, fmt.Errorf("web route dial returned no connection")
			}
			return s.track(conn)
		},
	}
	return &httputil.ReverseProxy{
		Transport: transport, ErrorLog: log.New(io.Discard, "", 0),
		Rewrite: func(p *httputil.ProxyRequest) {
			p.Out.URL.Scheme, p.Out.URL.Host = "http", p.In.Host
			p.Out.Host = p.In.Host
			for h := range p.Out.Header {
				if strings.EqualFold(h, "Forwarded") || strings.HasPrefix(strings.ToLower(h), "x-forwarded-") {
					p.Out.Header.Del(h)
				}
			}
			p.SetXForwarded()
			ip, _, err := net.SplitHostPort(p.In.RemoteAddr)
			if err != nil {
				ip = "unknown"
			}
			if strings.ContainsRune(ip, ':') {
				ip = "[" + ip + "]"
			}
			p.Out.Header.Set("Forwarded", "for="+strconv.Quote(ip)+";host="+strconv.Quote(p.In.Host)+";proto=https")
			stripGatewayCookie(p.Out.Header)
		},
		ModifyResponse: func(res *http.Response) error {
			filterResponseHeaders(res.Header)
			return nil
		},
		ErrorHandler: func(w http.ResponseWriter, _ *http.Request, _ error) {
			http.Error(w, "upstream unavailable", http.StatusBadGateway)
		},
	}
}

func stripGatewayCookie(header http.Header) {
	kept := make([]string, 0)
	for _, line := range header.Values("Cookie") {
		for _, pair := range strings.Split(line, ";") {
			name, _, _ := strings.Cut(strings.TrimSpace(pair), "=")
			if !strings.EqualFold(strings.TrimSpace(name), sessionCookie) {
				kept = append(kept, strings.TrimSpace(pair))
			}
		}
	}
	header.Del("Cookie")
	if len(kept) != 0 {
		header.Set("Cookie", strings.Join(kept, "; "))
	}
}

func filterResponseHeaders(header http.Header) {
	cookies := header.Values("Set-Cookie")
	header.Del("Set-Cookie")
	for _, cookie := range cookies {
		name, _, _ := strings.Cut(cookie, "=")
		if !strings.EqualFold(strings.TrimSpace(name), sessionCookie) {
			header.Add("Set-Cookie", cookie)
		}
	}
	for h := range header {
		if strings.HasPrefix(strings.ToLower(h), "access-control-") || strings.EqualFold(h, "Alt-Svc") {
			header.Del(h)
		}
	}
}

type sessionWriter struct {
	http.ResponseWriter
	owner *session
}

func (w *sessionWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }

func (w *sessionWriter) WriteHeader(status int) {
	// ReverseProxy forwards informational responses before ModifyResponse.
	filterResponseHeaders(w.Header())
	w.ResponseWriter.WriteHeader(status)
}

func (w *sessionWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	conn, rw, err := http.NewResponseController(w.ResponseWriter).Hijack()
	if err != nil {
		return nil, nil, fmt.Errorf("hijack web connection: %w", err)
	}
	tracked, err := w.owner.track(conn)
	if err != nil {
		return nil, nil, fmt.Errorf("track web connection: %w", err)
	}
	return tracked, rw, nil
}
