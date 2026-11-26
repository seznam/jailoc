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

// ProxyDefaults is [defaults.proxy]: default proxy settings for all workspaces.
type ProxyDefaults struct {
	Auth *bool `toml:"auth,omitempty"`
}

// Get returns d, or a zero ProxyDefaults when d is nil.
func (d *ProxyDefaults) Get() ProxyDefaults {
	if d == nil {
		return ProxyDefaults{}
	}
	return *d
}

// ProxyRoute is one entry in [workspaces.<name>.proxy]: an integer guest port
// (`app = 3000`) or a table (`app = { port = 3000, auth = true }`).
type ProxyRoute struct {
	Port int   `toml:"port"`
	Auth *bool `toml:"auth,omitempty"`
}

// ProxyTable is [workspaces.<name>.proxy]: an optional workspace-wide auth
// default (`auth = true/false`) and the named routes.
type ProxyTable struct {
	Auth   *bool                 `toml:"auth,omitempty"`
	Routes map[string]ProxyRoute `toml:"routes,omitempty"`
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

// MarshalTOML encodes the proxy table with route names directly under proxy so
// Encode, WriteRaw and ValidateCopy round-trip cleanly.
func (p ProxyTable) MarshalTOML() ([]byte, error) {
	var parts []string
	if p.Auth != nil {
		parts = append(parts, fmt.Sprintf("auth = %t", *p.Auth))
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
		if r.Auth == nil {
			parts = append(parts, fmt.Sprintf("%s = %d", key, r.Port))
		} else {
			parts = append(parts, fmt.Sprintf("%s = { port = %d, auth = %t }", key, r.Port, *r.Auth))
		}
	}
	return []byte("{ " + strings.Join(parts, ", ") + " }"), nil
}

// UnmarshalTOML decodes a [workspaces.<name>.proxy] table containing optional
// `auth = bool` and route entries (`<name> = 3000` or `<name> = { port = 3000, auth = true }`).
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
				default:
					return fmt.Errorf("proxy.%s: unknown field %q (allowed: port, auth)", k, field)
				}
			}
			if !hasPort {
				return fmt.Errorf("proxy.%s: missing required field \"port\"", k)
			}
			p.Routes[k] = route
		default:
			return fmt.Errorf("proxy.%s must be a port number or { port = ..., auth = ... }, got %T", k, raw)
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
