package config

import (
	"fmt"
	"net/netip"
	"net/url"
	"path/filepath"
	"slices"
	"strings"
)

// maxUpstreamUnixPath is the longest unix socket path the upstream proxy URL
// accepts: sockaddr_un.sun_path holds 104 bytes on macOS including the
// terminating NUL.
const maxUpstreamUnixPath = 103

// UpstreamProxy is a [<scope>.network.upstream_proxy] table: the HTTP proxy
// the host-side broker sends allowed guest egress through. A workspace table
// replaces the whole [defaults.network.upstream_proxy] table; enabled = false
// opts the scope out. Exactly one of URL and Command selects the proxy unless
// the table is disabled.
type UpstreamProxy struct {
	// Enabled, when false, turns the upstream proxy off for the scope.
	// Unset means true.
	Enabled *bool `toml:"enabled,omitempty"`
	// URL is a static proxy: "http://host[:port]" (port defaults to 80) or
	// "unix:///absolute/path.sock".
	URL string `toml:"url,omitempty"`
	// Command is a host command run with /bin/sh -c whose standard output
	// selects the proxy URL when the workspace starts.
	Command string `toml:"command,omitempty"`
	// CA is the absolute host path of a PEM bundle the proxy's certificates
	// are verified against.
	CA string `toml:"ca,omitempty"`
	// Direct lists targets the broker reaches without the proxy, in the
	// network rule allow grammar with an optional ":port".
	Direct []string `toml:"direct,omitempty"`
	// Auth are the proxy credentials, valid with a static URL only.
	Auth *UpstreamAuth `toml:"auth,omitempty"`
}

// UpstreamAuth is the [<scope>.network.upstream_proxy.auth] table: proxy
// basic credentials. The password is never stored in the configuration;
// exactly one From* field says where the host reads it from, with the
// semantics of a secret ([<scope>.secrets]).
type UpstreamAuth struct {
	Username    string `toml:"username,omitempty"`
	FromEnv     string `toml:"from_env,omitempty"`
	FromFile    string `toml:"from_file,omitempty"`
	FromCommand string `toml:"from_command,omitempty"`
	TTL         string `toml:"ttl,omitempty"`
}

// IsEnabled reports whether p is set and not opted out with enabled = false.
func (p *UpstreamProxy) IsEnabled() bool {
	return p != nil && (p.Enabled == nil || *p.Enabled)
}

// Clone returns a deep copy of p, so a caller never aliases the loaded
// configuration. The clone of nil is nil.
func (p *UpstreamProxy) Clone() *UpstreamProxy {
	if p == nil {
		return nil
	}
	c := *p
	if p.Enabled != nil {
		c.Enabled = new(bool)
		*c.Enabled = *p.Enabled
	}
	c.Direct = slices.Clone(p.Direct)
	if p.Auth != nil {
		a := *p.Auth
		c.Auth = &a
	}
	return &c
}

// secret returns the password source of a as a Secret, which the secret
// validator checks.
func (a UpstreamAuth) secret() Secret {
	return Secret{FromEnv: a.FromEnv, FromFile: a.FromFile, FromCommand: a.FromCommand, TTL: a.TTL}
}

// ParseUpstreamDirect parses an entry of upstream_proxy.direct into the
// allow rule it stands for: the target without its port, protocol "any" and
// the port, if given, as the only entry of Ports. The target uses the
// network rule allow grammar (host name, "*.domain" wildcard, "*", IP address
// or CIDR) plus an optional ":port"; an IPv6 target takes its port in
// brackets, "[fd00::1]:443".
func ParseUpstreamDirect(target string) (NetworkRule, error) {
	t := strings.TrimSpace(target)
	host, port, err := splitUpstreamDirect(t)
	if err != nil {
		return NetworkRule{}, fmt.Errorf("invalid direct target %q: %w", target, err)
	}
	if _, _, err := ParseRuleTarget(host); err != nil {
		return NetworkRule{}, fmt.Errorf("invalid direct target %q: %w", target, err)
	}
	rule := NetworkRule{Allow: []string{host}, Proto: RuleProtoAny}
	if port != "" {
		rule.Ports = []RulePort{RulePort(port)}
	}
	return rule, nil
}

// splitUpstreamDirect splits t into the rule target and the optional port.
func splitUpstreamDirect(t string) (host, port string, err error) {
	if t == "" {
		return "", "", fmt.Errorf("empty target")
	}
	if rest, ok := strings.CutPrefix(t, "["); ok {
		inner, tail, found := strings.Cut(rest, "]")
		if !found {
			return "", "", fmt.Errorf("missing closing \"]\"")
		}
		if !isIPv6Target(inner) {
			return "", "", fmt.Errorf("brackets enclose only an IPv6 address or CIDR")
		}
		if tail == "" {
			return inner, "", nil
		}
		p, ok := strings.CutPrefix(tail, ":")
		if !ok {
			return "", "", fmt.Errorf("unexpected %q after \"]\"", tail)
		}
		if err := checkUpstreamPort(p); err != nil {
			return "", "", err
		}
		return inner, p, nil
	}
	// A bare IPv6 address or CIDR contains colons itself.
	if _, _, err := ParseRuleTarget(t); err == nil {
		return t, "", nil
	}
	if strings.Count(t, ":") != 1 {
		return t, "", nil // let ParseRuleTarget report the target
	}
	host, port, _ = strings.Cut(t, ":")
	if err := checkUpstreamPort(port); err != nil {
		return "", "", err
	}
	return host, port, nil
}

// isIPv6Target reports whether s is an IPv6 address or CIDR prefix.
func isIPv6Target(s string) bool {
	if pfx, err := netip.ParsePrefix(s); err == nil {
		return pfx.Addr().Is6()
	}
	a, err := netip.ParseAddr(s)
	return err == nil && a.Is6()
}

func checkUpstreamPort(p string) error {
	if p == "" || strings.Trim(p, "0123456789") != "" {
		return fmt.Errorf("invalid port %q: want a port number 1-65535", p)
	}
	if _, err := parsePortNumber(p); err != nil {
		return fmt.Errorf("invalid port %q: want a port number 1-65535", p)
	}
	return nil
}

// ValidateUpstreamProxy checks an upstream_proxy table. A nil table is valid.
// It never reads the CA or credential files, only checks their paths.
func ValidateUpstreamProxy(p *UpstreamProxy) error {
	if p == nil {
		return nil
	}
	if p.URL != "" {
		if err := validateUpstreamURL(p.URL); err != nil {
			return err
		}
	}
	if p.Command != "" && strings.TrimSpace(p.Command) == "" {
		return fmt.Errorf("\"command\" must not be blank")
	}
	if p.Auth != nil {
		if err := validateUpstreamAuth(p); err != nil {
			return fmt.Errorf("auth: %w", err)
		}
	}
	if p.IsEnabled() && (p.URL == "") == (p.Command == "") {
		return fmt.Errorf("exactly one of \"url\" or \"command\" must be set (or enabled = false)")
	}
	if p.CA != "" && !filepath.IsAbs(p.CA) {
		return fmt.Errorf("\"ca\" path %q must be absolute", p.CA)
	}
	for _, d := range p.Direct {
		if _, err := ParseUpstreamDirect(d); err != nil {
			return fmt.Errorf("direct: %w", err)
		}
	}
	return nil
}

func validateUpstreamAuth(p *UpstreamProxy) error {
	if p.Command != "" || (p.URL == "" && p.IsEnabled()) {
		return fmt.Errorf("\"auth\" is only valid with a static \"url\"")
	}
	if strings.TrimSpace(p.Auth.Username) == "" {
		return fmt.Errorf("\"username\" must not be empty")
	}
	return validateSecret("upstream-proxy-auth", p.Auth.secret(), destFile, "upstream_proxy")
}

// validateUpstreamURL checks a static proxy URL: http://host[:port] or
// unix:///absolute/path, without userinfo, query or fragment.
func validateUpstreamURL(raw string) error {
	bad := func(format string, args ...any) error {
		return fmt.Errorf("invalid url: %s", fmt.Sprintf(format, args...))
	}
	if strings.ContainsFunc(raw, func(r rune) bool { return r <= ' ' || r == 0x7f }) {
		return bad("whitespace and control characters are not allowed")
	}
	if strings.Contains(raw, "#") {
		return bad("a fragment is not allowed")
	}
	if strings.Contains(raw, "?") {
		return bad("a query is not allowed")
	}
	u, err := url.Parse(raw)
	if err != nil {
		return bad("malformed url")
	}
	if u.User != nil {
		return bad("userinfo is not allowed; use auth")
	}
	switch u.Scheme {
	case "http":
		return validateUpstreamHTTPURL(u, bad)
	case "unix":
		return validateUpstreamUnixURL(raw, u, bad)
	default:
		return bad("scheme must be \"http\" or \"unix\"")
	}
}

func validateUpstreamHTTPURL(u *url.URL, bad func(string, ...any) error) error {
	if u.Opaque != "" || (u.Path != "" && u.Path != "/") {
		return bad("path is not allowed")
	}
	host := u.Hostname()
	if host == "" {
		return bad("host is required")
	}
	if addr, err := netip.ParseAddr(host); err == nil {
		if addr.Zone() != "" {
			return bad("host must not have an IPv6 zone")
		}
	} else if err := validateRuleHostname(strings.ToLower(host)); err != nil {
		return bad("invalid host: %v", err)
	}
	if port := u.Port(); port != "" {
		if _, err := parsePortNumber(port); err != nil {
			return bad("port must be 1-65535")
		}
	}
	return nil
}

func validateUpstreamUnixURL(raw string, u *url.URL, bad func(string, ...any) error) error {
	if !strings.HasPrefix(raw, "unix:///") {
		return bad("unix proxy must be \"unix:///absolute/path\" without a host")
	}
	if u.Host != "" {
		return bad("host is not allowed")
	}
	if u.Path == "" || u.Path == "/" {
		return bad("socket path is required")
	}
	if strings.ContainsRune(u.Path, 0) {
		return bad("socket path must not contain NUL")
	}
	if len(u.Path) > maxUpstreamUnixPath {
		return bad("socket path is %d bytes, at most %d fit a unix socket address", len(u.Path), maxUpstreamUnixPath)
	}
	return nil
}

// validateUpstreamProxyScope validates the upstream_proxy of the scope named
// by context, such as "defaults" or `workspace "api"`.
func validateUpstreamProxyScope(p *UpstreamProxy, context string) error {
	if err := ValidateUpstreamProxy(p); err != nil {
		return fmt.Errorf("%s: network.upstream_proxy: %w", context, err)
	}
	return nil
}

// expandUpstreamProxyPaths expands a leading "~" in the host paths of p: the
// CA and the credential file. It does not read them.
func expandUpstreamProxyPaths(p *UpstreamProxy) error {
	if p == nil {
		return nil
	}
	if p.CA != "" {
		expanded, err := ExpandPath(p.CA)
		if err != nil {
			return fmt.Errorf("expand upstream_proxy ca %q: %w", p.CA, err)
		}
		p.CA = expanded
	}
	if p.Auth != nil && p.Auth.FromFile != "" {
		expanded, err := ExpandPath(p.Auth.FromFile)
		if err != nil {
			return fmt.Errorf("expand upstream_proxy auth file %q: %w", p.Auth.FromFile, err)
		}
		p.Auth.FromFile = expanded
	}
	return nil
}
