package config

import (
	"fmt"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
)

// ProxyRelayPort is the guest relay port that fronts every proxy route. It mirrors
// the guest's immutable relay allowlist; config does not import the guest packages.
const ProxyRelayPort = 6081

// proxyLabelRe is a canonical DNS label: lowercase, 1-63 characters, no hyphen
// at either end.
var proxyLabelRe = regexp.MustCompile(`^[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?$`)

// ValidProxyLabel reports whether s is a canonical lowercase single DNS label.
func ValidProxyLabel(s string) bool { return proxyLabelRe.MatchString(s) }

// Default proxy listener ports. The HTTP port redirects to the HTTPS port; the
// standalone pair is used when the privileged listener is not installed.
const (
	DefaultProxyHTTPPort            = 80
	DefaultProxyHTTPSPort           = 443
	DefaultProxyStandaloneHTTPPort  = 7080
	DefaultProxyStandaloneHTTPSPort = 7043
)

// ProxyConfig is the top-level [proxy] table: the host ports the proxy
// listens on. An unset port uses its default (see Ports).
type ProxyConfig struct {
	HTTPPort            *int `toml:"http_port,omitempty"`
	HTTPSPort           *int `toml:"https_port,omitempty"`
	StandaloneHTTPPort  *int `toml:"standalone_http_port,omitempty"`
	StandaloneHTTPSPort *int `toml:"standalone_https_port,omitempty"`
}

// ProxyPorts are the effective host ports of the proxy listeners.
type ProxyPorts struct {
	HTTP, HTTPS                     int
	StandaloneHTTP, StandaloneHTTPS int
}

// Ports returns the configured ports with defaults for unset ones.
func (c ProxyConfig) Ports() ProxyPorts {
	return ProxyPorts{
		HTTP:            portOr(c.HTTPPort, DefaultProxyHTTPPort),
		HTTPS:           portOr(c.HTTPSPort, DefaultProxyHTTPSPort),
		StandaloneHTTP:  portOr(c.StandaloneHTTPPort, DefaultProxyStandaloneHTTPPort),
		StandaloneHTTPS: portOr(c.StandaloneHTTPSPort, DefaultProxyStandaloneHTTPSPort),
	}
}

func portOr(p *int, def int) int {
	if p == nil {
		return def
	}
	return *p
}

// validate checks every configured port is 1-65535 and that the HTTP and
// HTTPS ports differ within the normal pair and within the standalone pair.
// The pairs are alternative modes, so they may share ports with each other.
func (c ProxyConfig) validate() error {
	for _, f := range []struct {
		key string
		p   *int
	}{
		{"http_port", c.HTTPPort},
		{"https_port", c.HTTPSPort},
		{"standalone_http_port", c.StandaloneHTTPPort},
		{"standalone_https_port", c.StandaloneHTTPSPort},
	} {
		if f.p != nil && (*f.p < 1 || *f.p > 65535) {
			return fmt.Errorf("proxy: %s %d out of range 1-65535", f.key, *f.p)
		}
	}
	p := c.Ports()
	if p.HTTP == p.HTTPS {
		return fmt.Errorf("proxy: http_port and https_port must differ, both are %d", p.HTTP)
	}
	if p.StandaloneHTTP == p.StandaloneHTTPS {
		return fmt.Errorf("proxy: standalone_http_port and standalone_https_port must differ, both are %d", p.StandaloneHTTP)
	}
	return nil
}

// ProxyDefaults is [defaults.proxy]: default proxy settings for all workspaces.
// Redirect, when unset, means true.
type ProxyDefaults struct {
	Auth     *bool `toml:"auth,omitempty"`
	Redirect *bool `toml:"redirect,omitempty"`
}

// Get returns d, or a zero ProxyDefaults when d is nil.
func (d *ProxyDefaults) Get() ProxyDefaults {
	if d == nil {
		return ProxyDefaults{}
	}
	return *d
}

// ProxyRoute is one entry in [workspaces.<name>.proxy]: an integer guest port
// (`app = 3000`) or a table (`app = { port = 3000, auth = true, redirect = false }`).
// Redirect, when unset, inherits; the final default is true.
type ProxyRoute struct {
	Port     int   `toml:"port"`
	Auth     *bool `toml:"auth,omitempty"`
	Redirect *bool `toml:"redirect,omitempty"`
}

// ProxyTable is [workspaces.<name>.proxy]: optional workspace-wide auth and
// redirect defaults (`auth = true/false`, `redirect = true/false`) and the
// named routes. A route named "redirect" keeps working: only a boolean value
// under that key is the redirect default.
type ProxyTable struct {
	Auth     *bool                 `toml:"auth,omitempty"`
	Redirect *bool                 `toml:"redirect,omitempty"`
	Routes   map[string]ProxyRoute `toml:"routes,omitempty"`
}

// Get returns p, or a zero ProxyTable when p is nil.
func (p *ProxyTable) Get() ProxyTable {
	if p == nil {
		return ProxyTable{}
	}
	return *p
}

// ProxyMap constructs a ProxyTable from a name-to-port map with default auth.
func ProxyMap(m map[string]int) *ProxyTable {
	if len(m) == 0 {
		return nil
	}
	routes := make(map[string]ProxyRoute, len(m))
	for k, port := range m {
		routes[k] = ProxyRoute{Port: port}
	}
	return &ProxyTable{Routes: routes}
}

// marshalProxyRoute encodes a route as a bare port, or as an inline table when
// it overrides auth or redirect.
func marshalProxyRoute(r ProxyRoute) string {
	if r.Auth == nil && r.Redirect == nil {
		return strconv.Itoa(r.Port)
	}
	fields := []string{fmt.Sprintf("port = %d", r.Port)}
	if r.Auth != nil {
		fields = append(fields, fmt.Sprintf("auth = %t", *r.Auth))
	}
	if r.Redirect != nil {
		fields = append(fields, fmt.Sprintf("redirect = %t", *r.Redirect))
	}
	return "{ " + strings.Join(fields, ", ") + " }"
}

// MarshalTOML encodes the proxy table with route names directly under proxy so
// Encode, WriteRaw and ValidateCopy round-trip cleanly.
func (p ProxyTable) MarshalTOML() ([]byte, error) {
	var parts []string
	if p.Auth != nil {
		parts = append(parts, fmt.Sprintf("auth = %t", *p.Auth))
	}
	if p.Redirect != nil {
		parts = append(parts, fmt.Sprintf("redirect = %t", *p.Redirect))
	}
	names := make([]string, 0, len(p.Routes))
	for name := range p.Routes {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		r := p.Routes[name]
		key := strconv.Quote(name)
		if ValidProxyLabel(name) {
			key = name
		}
		parts = append(parts, key+" = "+marshalProxyRoute(r))
	}
	return []byte("{ " + strings.Join(parts, ", ") + " }"), nil
}

// UnmarshalTOML decodes a [workspaces.<name>.proxy] table containing optional
// `auth = bool`, `redirect = bool` and route entries (`<name> = 3000` or
// `<name> = { port = 3000, auth = true, redirect = false }`).
func (p *ProxyTable) UnmarshalTOML(v any) error {
	m, ok := v.(map[string]any)
	if !ok {
		return fmt.Errorf("proxy must be a table, got %T", v)
	}
	p.Routes = make(map[string]ProxyRoute, len(m))
	for k, raw := range m {
		if k == "auth" {
			b, ok := raw.(bool)
			if !ok {
				return fmt.Errorf("proxy.auth must be a boolean, got %T", raw)
			}
			p.Auth = &b
			continue
		}
		// A non-boolean "redirect" is a route of that name, valid before the
		// redirect default existed.
		if b, ok := raw.(bool); ok && k == "redirect" {
			p.Redirect = &b
			continue
		}
		switch val := raw.(type) {
		case int64:
			p.Routes[k] = ProxyRoute{Port: int(val)}
		case map[string]any:
			var route ProxyRoute
			var hasPort bool
			for field, fv := range val {
				switch field {
				case "port":
					port, ok := fv.(int64)
					if !ok {
						return fmt.Errorf("proxy.%s.port must be an integer, got %T", k, fv)
					}
					route.Port = int(port)
					hasPort = true
				case "auth":
					b, ok := fv.(bool)
					if !ok {
						return fmt.Errorf("proxy.%s.auth must be a boolean, got %T", k, fv)
					}
					route.Auth = &b
				case "redirect":
					b, ok := fv.(bool)
					if !ok {
						return fmt.Errorf("proxy.%s.redirect must be a boolean, got %T", k, fv)
					}
					route.Redirect = &b
				default:
					return fmt.Errorf("proxy.%s: unknown field %q (allowed: port, auth, redirect)", k, field)
				}
			}
			if !hasPort {
				return fmt.Errorf("proxy.%s: missing required field \"port\"", k)
			}
			p.Routes[k] = route
		default:
			return fmt.Errorf("proxy.%s must be a port number or { port = ..., auth = ..., redirect = ... }, got %T", k, raw)
		}
	}
	return nil
}

// validateProxy checks the proxy settings of workspace name against its
// effective defaults, host_ports and ports. The proxy relay listens on guest
// port ProxyRelayPort in every workspace VM, whether or not it has routes.
func validateProxy(cfg *Config, name string, ws Workspace) error {
	hostGuest := map[int]bool{}
	for _, t := range MergeHostPorts(cfg.Defaults.HostPorts, ws.HostPorts) {
		hostGuest[t.Guest] = true
	}
	published := MergePorts(cfg.Defaults.Ports, ws.Ports)
	if hostGuest[ProxyRelayPort] {
		return fmt.Errorf("workspace %q: host_ports guest port %d is reserved for the proxy relay", name, ProxyRelayPort)
	}
	if slices.ContainsFunc(published, func(f PortForward) bool { return f.Guest == ProxyRelayPort }) {
		return fmt.Errorf("workspace %q: ports must not publish guest port %d, reserved for the proxy relay", name, ProxyRelayPort)
	}
	proxy := ws.Proxy.Get()
	if len(proxy.Routes) == 0 {
		return nil
	}
	routes := make([]string, 0, len(proxy.Routes))
	for route := range proxy.Routes {
		routes = append(routes, route)
	}
	sort.Strings(routes)
	for _, route := range routes {
		port := proxy.Routes[route].Port
		switch {
		case !ValidProxyLabel(route):
			return fmt.Errorf("workspace %q: invalid proxy route name %q: must be a lowercase DNS label (1-63 characters of a-z, 0-9 and -, not starting or ending with -)", name, route)
		case route == "redirect" && proxy.Redirect != nil:
			return fmt.Errorf("workspace %q: proxy route name \"redirect\" conflicts with the proxy redirect default", name)
		case port < 1 || port > 65535:
			return fmt.Errorf("workspace %q: proxy route %q: port %d out of range 1-65535", name, route, port)
		case port == ProxyRelayPort:
			return fmt.Errorf("workspace %q: proxy route %q: port %d is reserved for the proxy relay", name, route, port)
		}
	}
	if !ValidProxyLabel(name) {
		return fmt.Errorf("workspace %q: name must be a DNS label (1-63 characters of a-z, 0-9 and -, not starting or ending with -) when proxy is used", name)
	}

	for _, route := range routes {
		if port := proxy.Routes[route].Port; hostGuest[port] {
			return fmt.Errorf("workspace %q: proxy route %q: port %d is also a host_ports guest port", name, route, port)
		}
	}
	for _, route := range routes {
		port := proxy.Routes[route].Port
		if slices.ContainsFunc(published, func(f PortForward) bool { return f.Guest == port }) {
			return fmt.Errorf("workspace %q: proxy route %q: port %d is also a ports guest port", name, route, port)
		}
	}
	return nil
}
