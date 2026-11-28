package netbroker

import (
	"bufio"
	"bytes"
	"context"
	"crypto/x509"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"
)

// UpstreamProxy contains only trusted host configuration and secret source
// references. Resolved headers and credential values never enter the policy.
type UpstreamProxy struct {
	Workspace string        `json:"workspace"`
	URL       string        `json:"url,omitempty"`
	Command   string        `json:"command,omitempty"`
	CA        string        `json:"ca,omitempty"`
	Direct    []string      `json:"direct,omitempty"`
	Auth      *UpstreamAuth `json:"auth,omitempty"`
}

// UpstreamAuth describes a Basic password source resolved by MainOptions.
type UpstreamAuth struct {
	Username    string `json:"username"`
	FromEnv     string `json:"from_env,omitempty"`
	FromFile    string `json:"from_file,omitempty"`
	FromCommand string `json:"from_command,omitempty"`
	TTL         string `json:"ttl,omitempty"`
}

type upstreamEndpoint struct{ network, address string }

type upstreamRoute struct {
	workspace, url, mode string
	endpoint             upstreamEndpoint
	headers              http.Header
	direct               []Rule
	ca                   []byte
	generation           uint64
}

func (u *UpstreamProxy) validate() error {
	if u.Workspace == "" || strings.Trim(u.Workspace, "abcdefghijklmnopqrstuvwxyz0123456789-") != "" {
		return errors.New("upstream_proxy: invalid workspace name")
	}
	if (u.URL == "") == (u.Command == "") || (u.Command != "" && strings.TrimSpace(u.Command) == "") {
		return errors.New("upstream_proxy: exactly one of url or command is required")
	}
	if u.URL != "" {
		if _, err := parseUpstreamEndpoint(u.URL); err != nil {
			return err
		}
	}
	if u.CA != "" && !filepath.IsAbs(u.CA) {
		return errors.New("upstream_proxy: ca path must be absolute")
	}
	if _, err := compileUpstreamDirect(u.Direct); err != nil {
		return err
	}
	if u.Auth == nil {
		return nil
	}
	a := u.Auth
	if u.Command != "" {
		return errors.New("upstream_proxy: auth requires a static url")
	}
	if strings.TrimSpace(a.Username) == "" || strings.ContainsAny(a.Username, ":\r\n\x00") {
		return errors.New("upstream_proxy: invalid auth username")
	}
	n := 0
	for _, s := range []string{a.FromEnv, a.FromFile, a.FromCommand} {
		if s != "" {
			n++
		}
	}
	if n != 1 {
		return errors.New("upstream_proxy: auth needs exactly one password source")
	}
	if a.TTL != "" && a.FromCommand == "" {
		return errors.New("upstream_proxy: auth ttl requires from_command")
	}
	return nil
}

func parseUpstreamEndpoint(raw string) (upstreamEndpoint, error) {
	bad := func() (upstreamEndpoint, error) {
		return upstreamEndpoint{}, errors.New("upstream_proxy: invalid url; use http://host[:port] or unix:///absolute/socket without credentials, query or fragment")
	}
	if strings.ContainsFunc(raw, func(r rune) bool { return r <= ' ' || r == 127 }) || strings.ContainsAny(raw, "?#") {
		return bad()
	}
	u, err := url.Parse(raw)
	if err != nil || u.User != nil || u.Opaque != "" {
		return bad()
	}
	switch u.Scheme {
	case "http":
		if u.Hostname() == "" || (u.Path != "" && u.Path != "/") {
			return bad()
		}
		host := u.Hostname()
		if ip, err := netip.ParseAddr(host); err != nil {
			if !validUpstreamName(host) {
				return bad()
			}
		} else if ip.Zone() != "" {
			return bad()
		}
		port := u.Port()
		if port == "" {
			if strings.HasSuffix(u.Host, ":") {
				return bad()
			}
			port = "80"
		}
		if n, err := strconv.ParseUint(port, 10, 16); err != nil || n == 0 {
			return bad()
		}
		return upstreamEndpoint{network: "tcp", address: net.JoinHostPort(host, port)}, nil
	case "unix":
		if u.Host != "" || !strings.HasPrefix(raw, "unix:///") || !filepath.IsAbs(u.Path) || len(u.Path) > 103 || strings.ContainsRune(u.Path, 0) {
			return bad()
		}
		return upstreamEndpoint{network: "unix", address: u.Path}, nil
	default:
		return bad()
	}
}

func validUpstreamName(name string) bool {
	name = strings.TrimSuffix(name, ".")
	if name == "" || len(name) > 253 {
		return false
	}
	for _, label := range strings.Split(name, ".") {
		if label == "" || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return false
		}
		for _, c := range label {
			if (c < 'a' || c > 'z') && (c < 'A' || c > 'Z') && (c < '0' || c > '9') && c != '-' {
				return false
			}
		}
	}
	return true
}

func compileUpstreamDirect(entries []string) ([]Rule, error) {
	rules := make([]Rule, 0, len(entries))
	for _, entry := range entries {
		target := strings.TrimSpace(entry)
		port := ""
		if strings.HasPrefix(target, "[") {
			inner, tail, ok := strings.Cut(target[1:], "]")
			if !ok {
				return nil, errors.New("upstream_proxy: invalid direct IPv6 brackets")
			}
			ip, ie := netip.ParseAddr(inner)
			pfx, pe := netip.ParsePrefix(inner)
			if (ie != nil || !ip.Is6()) && (pe != nil || !pfx.Addr().Is6()) {
				return nil, errors.New("upstream_proxy: direct brackets require IPv6")
			}
			target = inner
			if tail != "" {
				var ok bool
				port, ok = strings.CutPrefix(tail, ":")
				if !ok || port == "" {
					return nil, errors.New("upstream_proxy: invalid direct port")
				}
			}
		} else if strings.Count(target, ":") == 1 {
			target, port, _ = strings.Cut(target, ":")
			if port == "" {
				return nil, errors.New("upstream_proxy: empty direct port")
			}
		}
		r := Rule{Proto: ProtoAny}
		if port != "" {
			n, err := strconv.ParseUint(port, 10, 16)
			if err != nil || n == 0 || strings.Trim(port, "0123456789") != "" {
				return nil, errors.New("upstream_proxy: invalid direct port")
			}
			r.Ports = []string{port}
		}
		_, ipErr := netip.ParseAddr(target)
		_, prefixErr := netip.ParsePrefix(target)
		if ipErr == nil || prefixErr == nil || target == "*" {
			r.CIDRs = []string{target}
		} else {
			if !validUpstreamName(strings.TrimPrefix(target, "*.")) {
				return nil, errors.New("upstream_proxy: invalid direct hostname")
			}
			r.Names = []string{target}
		}
		if err := r.compile("upstream direct"); err != nil {
			return nil, fmt.Errorf("upstream direct: %w", err)
		}
		rules = append(rules, r)
	}
	return rules, nil
}

func (r *upstreamRoute) isDirect(proto string, dst netip.AddrPort, names []string) bool {
	for i := range r.direct {
		d := &r.direct[i]
		if d.wildcard && d.matchesProtoPort(proto, dst.Port()) {
			return true
		}
		if d.matchesAddr(proto, dst, true) {
			return true
		}
		for _, name := range names {
			if d.matchesName(proto, dst.Port(), name) {
				return true
			}
		}
	}
	return false
}

func upstreamNames(name string, env Env) []string {
	if name != "" {
		if validUpstreamName(name) && slices.Contains(env.Names, name) && !env.Untrusted[name] {
			return []string{name}
		}
		return nil
	}
	var trusted []string
	for _, n := range env.Names {
		if !env.Untrusted[n] {
			trusted = append(trusted, n)
		}
	}
	if len(trusted) == 1 {
		return trusted
	}
	return nil
}

func (r *upstreamRoute) needsNameInspection(dst netip.AddrPort, names []string) bool {
	if r == nil {
		return false
	}
	if len(names) != 1 {
		return true
	}
	for i := range r.direct {
		if !r.direct[i].nameSet.empty() && r.direct[i].matchesProtoPort(ProtoTCP, dst.Port()) {
			return true
		}
	}
	return false
}
func (r *upstreamRoute) roots(base *x509.CertPool) *x509.CertPool {
	if len(r.ca) == 0 {
		return base
	}
	pool := base.Clone()
	pool.AppendCertsFromPEM(r.ca)
	return pool
}

var (
	errUpstreamUnreachable = errors.New("upstream proxy unreachable")
	errUpstreamRefused     = errors.New("upstream proxy answered")
)

const maxConnectHead = 16 << 10

func connectAuthority(dst netip.AddrPort, name string) string {
	if !validUpstreamName(name) {
		return dst.String()
	}
	return net.JoinHostPort(normalizeName(name), strconv.Itoa(int(dst.Port())))
}

// dialEgress is called only after the local policy has allowed the destination.
// A trusted proxy re-resolves CONNECT names and enforces final-IP restrictions.
func (b *Broker) dialEgress(ctx context.Context, r *upstreamRoute, conn uint64, dst netip.AddrPort, name string) (net.Conn, error) {
	if r == nil {
		return b.dialTCP("tcp", dst.String())
	}
	if r.mode == "blocked" {
		b.upstreamFailed(r)
		return nil, fmt.Errorf("%w: no successful upstream helper result", errUpstreamUnreachable)
	}
	d := net.Dialer{Timeout: dialTimeout}
	c, err := d.DialContext(ctx, r.endpoint.network, r.endpoint.address)
	if err != nil {
		b.upstreamFailed(r)
		return nil, fmt.Errorf("%w; check the proxy endpoint", errUpstreamUnreachable)
	}
	ok := false
	defer func() {
		if !ok {
			_ = c.Close()
		}
	}()
	deadline := time.Now().Add(dialTimeout)
	if end, ok := ctx.Deadline(); ok && end.Before(deadline) {
		deadline = end
	}
	_ = c.SetDeadline(deadline)
	stop := context.AfterFunc(ctx, func() { _ = c.SetDeadline(time.Time{}); _ = c.Close() })
	defer stop()
	authority := connectAuthority(dst, name)
	var head strings.Builder
	fmt.Fprintf(&head, "CONNECT %s HTTP/1.1\r\nHost: %s\r\nX-Jailoc-Workspace: %s\r\nX-Jailoc-Flow: %d\r\n", authority, authority, r.workspace, conn)
	for _, key := range sortedHeaderNames(r.headers) {
		fmt.Fprintf(&head, "%s: %s\r\n", key, r.headers.Get(key))
	}
	head.WriteString("\r\n")
	if _, err := io.WriteString(c, head.String()); err != nil {
		b.upstreamFailed(r)
		return nil, fmt.Errorf("%w: CONNECT write failed", errUpstreamUnreachable)
	}
	br := bufio.NewReaderSize(c, maxConnectHead)
	status, code, err := readConnectHead(br)
	if err != nil {
		b.upstreamFailed(r)
		return nil, fmt.Errorf("upstream CONNECT: %w", err)
	}
	status = replyText(redactUpstream(status, r.headers))
	b.record(map[string]any{"event": "upstream-connect", "conn": conn, "dst": dst.String(), "upstream_generation": r.generation, "header_names": sortedHeaderNames(r.headers), "upstream_status": status})
	if code < 200 || code >= 300 {
		return nil, fmt.Errorf("%w %s", errUpstreamRefused, status)
	}
	if err := c.SetDeadline(time.Time{}); err != nil {
		return nil, fmt.Errorf("clear CONNECT deadline: %w", err)
	}
	b.upstreamConnected(r)
	ok = true
	return &bufConn{Conn: c, r: br}, nil
}

func readConnectHead(br *bufio.Reader) (string, int, error) {
	var head bytes.Buffer
	for head.Len() < maxConnectHead {
		line, err := br.ReadSlice('\n')
		if head.Len()+len(line) > maxConnectHead || errors.Is(err, bufio.ErrBufferFull) {
			return "", 0, errors.New("response head too large (16 KiB)")
		}
		head.Write(line)
		if err != nil {
			return "", 0, errors.New("incomplete response head")
		}
		if bytes.Equal(line, []byte("\r\n")) {
			res, err := http.ReadResponse(bufio.NewReader(bytes.NewReader(head.Bytes())), &http.Request{Method: http.MethodHead})
			if err != nil {
				return "", 0, errors.New("invalid response head")
			}
			return res.Proto + " " + res.Status, res.StatusCode, nil
		}
	}
	return "", 0, errors.New("response head too large (16 KiB)")
}

func (b *Broker) recordUpstreamError(conn uint64, dst netip.AddrPort, err error) {
	ev := map[string]any{"event": "upstream-error", "conn": conn, "dst": dst.String(), "error": err.Error()}
	if errors.Is(err, errUpstreamUnreachable) {
		ev["hint"] = "upstream proxy unreachable; no direct fallback; check the proxy endpoint (command helpers retry with backoff)"
	}
	b.record(ev)
}
