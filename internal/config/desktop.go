package config

import (
	"fmt"
	"regexp"
	"slices"
	"sort"
)

// Guest ports of the desktop and web routes. They mirror the guest's
// immutable relay allowlist; config does not import the guest packages.
const (
	// DesktopRouteName is the web route name reserved for the desktop.
	DesktopRouteName = "desktop"
	// DesktopWebPort is the noVNC port the desktop route targets.
	DesktopWebPort = 6080
	// DesktopVNCPort is the VNC server port of the desktop.
	DesktopVNCPort = 5901
	// WebRelayPort is the guest relay port that fronts every web route.
	WebRelayPort = 6081
)

// webLabelRe is a canonical DNS label: lowercase, 1-63 characters, no hyphen
// at either end.
var webLabelRe = regexp.MustCompile(`^[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?$`)

// ValidWebLabel reports whether s is a canonical lowercase single DNS label.
func ValidWebLabel(s string) bool { return webLabelRe.MatchString(s) }

// validateDesktopWeb checks the desktop and web settings of workspace name
// against its effective defaults, host_ports and ports.
func validateDesktopWeb(cfg *Config, name string, ws Workspace) error {
	desktop := explicitTrue(cfg.Defaults.EnableDesktop, ws.EnableDesktop)
	web := desktop || len(ws.Web) > 0

	routes := make([]string, 0, len(ws.Web))
	for route := range ws.Web {
		routes = append(routes, route)
	}
	sort.Strings(routes)
	for _, route := range routes {
		port := ws.Web[route]
		switch {
		case route == DesktopRouteName:
			return fmt.Errorf("workspace %q: web route %q is reserved for enable_desktop", name, route)
		case !ValidWebLabel(route):
			return fmt.Errorf("workspace %q: invalid web route name %q: must be a lowercase DNS label (1-63 characters of a-z, 0-9 and -, not starting or ending with -)", name, route)
		case port < 1 || port > 65535:
			return fmt.Errorf("workspace %q: web route %q: port %d out of range 1-65535", name, route, port)
		case port == WebRelayPort:
			return fmt.Errorf("workspace %q: web route %q: port %d is reserved for the web relay", name, route, port)
		}
	}
	if !web {
		return nil
	}
	if !ValidWebLabel(name) {
		return fmt.Errorf("workspace %q: name must be a DNS label (1-63 characters of a-z, 0-9 and -, not starting or ending with -) when enable_desktop or web is used", name)
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
	builtins := []int{WebRelayPort}
	if desktop {
		builtins = append(builtins, DesktopWebPort, DesktopVNCPort)
	}
	published := MergePorts(cfg.Defaults.Ports, ws.Ports)
	for _, port := range builtins {
		if hostGuest[port] {
			return fmt.Errorf("workspace %q: host_ports guest port %d is reserved by the desktop and web routes", name, port)
		}
		if slices.ContainsFunc(published, func(f PortForward) bool { return f.Guest == port }) {
			return fmt.Errorf("workspace %q: ports must not publish guest port %d, reserved by the desktop and web routes", name, port)
		}
	}
	return nil
}
