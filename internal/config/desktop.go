package config

import (
	"fmt"
	"regexp"
	"slices"
	"sort"
)

// WebRelayPort is the guest relay port that fronts every web route. It mirrors
// the guest's immutable relay allowlist; config does not import the guest packages.
const WebRelayPort = 6081

// webLabelRe is a canonical DNS label: lowercase, 1-63 characters, no hyphen
// at either end.
var webLabelRe = regexp.MustCompile(`^[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?$`)

// ValidWebLabel reports whether s is a canonical lowercase single DNS label.
func ValidWebLabel(s string) bool { return webLabelRe.MatchString(s) }

// validateDesktopWeb checks the web settings of workspace name against its
// effective defaults, host_ports and ports.
func validateDesktopWeb(cfg *Config, name string, ws Workspace) error {
	if len(ws.Web) == 0 {
		return nil
	}
	routes := make([]string, 0, len(ws.Web))
	for route := range ws.Web {
		routes = append(routes, route)
	}
	sort.Strings(routes)
	for _, route := range routes {
		port := ws.Web[route]
		switch {
		case !ValidWebLabel(route):
			return fmt.Errorf("workspace %q: invalid web route name %q: must be a lowercase DNS label (1-63 characters of a-z, 0-9 and -, not starting or ending with -)", name, route)
		case port < 1 || port > 65535:
			return fmt.Errorf("workspace %q: web route %q: port %d out of range 1-65535", name, route, port)
		case port == WebRelayPort:
			return fmt.Errorf("workspace %q: web route %q: port %d is reserved for the web relay", name, route, port)
		}
	}
	if !ValidWebLabel(name) {
		return fmt.Errorf("workspace %q: name must be a DNS label (1-63 characters of a-z, 0-9 and -, not starting or ending with -) when web is used", name)
	}

	hostGuest := map[int]bool{}
	for _, t := range MergeHostPorts(cfg.Defaults.HostPorts, ws.HostPorts) {
		hostGuest[t.Guest] = true
	}
	for _, route := range routes {
		if port := ws.Web[route]; hostGuest[port] {
			return fmt.Errorf("workspace %q: web route %q: port %d is also a host_ports guest port", name, route, port)
		}
	}
	published := MergePorts(cfg.Defaults.Ports, ws.Ports)
	for _, route := range routes {
		port := ws.Web[route]
		if slices.ContainsFunc(published, func(f PortForward) bool { return f.Guest == port }) {
			return fmt.Errorf("workspace %q: web route %q: port %d is also a ports guest port", name, route, port)
		}
	}
	if hostGuest[WebRelayPort] {
		return fmt.Errorf("workspace %q: host_ports guest port %d is reserved by web routes", name, WebRelayPort)
	}
	if slices.ContainsFunc(published, func(f PortForward) bool { return f.Guest == WebRelayPort }) {
		return fmt.Errorf("workspace %q: ports must not publish guest port %d, reserved by web routes", name, WebRelayPort)
	}
	return nil
}
