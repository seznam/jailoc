package config

import (
	"fmt"
	"path/filepath"
	"strconv"
	"strings"
)

// HostPort is a host_ports entry: "PORT" relays guest port PORT to host
// 127.0.0.1:PORT, "GUEST:HOST" relays it to host 127.0.0.1:HOST, and
// "GUEST:/path.sock" relays it to a host unix socket. TOML accepts integers
// and strings.
type HostPort string

// UnmarshalTOML accepts an integer or a string.
func (p *HostPort) UnmarshalTOML(v any) error {
	switch v := v.(type) {
	case int64:
		*p = HostPort(strconv.FormatInt(v, 10))
	case string:
		*p = HostPort(v)
	default:
		return fmt.Errorf("host port must be a number or \"GUEST:HOST\", got %T", v)
	}
	return nil
}

// MarshalText writes the entry as a string.
func (p HostPort) MarshalText() ([]byte, error) { return []byte(p), nil }

// HostPortTarget is a parsed host_ports entry. Exactly one of HostPort and
// Socket is set.
type HostPortTarget struct {
	Guest    int
	HostPort int
	Socket   string
}

// reservedGuestPorts are gateway ports jailoc itself serves to the guest.
var reservedGuestPorts = map[int]string{
	22:   "guest SSH",
	53:   "DNS",
	2375: "Docker",
	7022: "SSH agent",
	7023: "GPG agent",
}

// ParseHostPort parses "PORT", "GUEST:HOST" or "GUEST:/path.sock".
func ParseHostPort(p HostPort) (HostPortTarget, error) {
	guest, target, mapped := strings.Cut(string(p), ":")
	g, err := parsePortNumber(guest)
	if err != nil {
		return HostPortTarget{}, fmt.Errorf("invalid host port %q: want PORT, GUEST:HOST or GUEST:/path.sock with ports 1-65535", string(p))
	}
	if what, ok := reservedGuestPorts[g]; ok {
		return HostPortTarget{}, fmt.Errorf("invalid host port %q: guest port %d is reserved for %s", string(p), g, what)
	}
	if !mapped {
		return HostPortTarget{Guest: g, HostPort: g}, nil
	}
	target = strings.TrimSpace(target)
	if strings.HasPrefix(target, "/") || strings.HasPrefix(target, "~") {
		path, err := ExpandPath(target)
		if err != nil {
			return HostPortTarget{}, fmt.Errorf("invalid host port %q: %w", string(p), err)
		}
		if !filepath.IsAbs(path) {
			return HostPortTarget{}, fmt.Errorf("invalid host port %q: socket path must be absolute", string(p))
		}
		return HostPortTarget{Guest: g, Socket: filepath.Clean(path)}, nil
	}
	h, err := parsePortNumber(target)
	if err != nil {
		return HostPortTarget{}, fmt.Errorf("invalid host port %q: want PORT, GUEST:HOST or GUEST:/path.sock with ports 1-65535", string(p))
	}
	return HostPortTarget{Guest: g, HostPort: h}, nil
}

// Dangerous reports why relaying this target gives the guest control over
// the host, or "" when nothing is known about it.
func (t HostPortTarget) Dangerous() string {
	if t.HostPort == 9222 {
		return "Chrome DevTools Protocol gives full control of the host browser"
	}
	switch filepath.Base(t.Socket) {
	case "docker.sock", "containerd.sock", "podman.sock", "buildkitd.sock":
		return "a container runtime socket gives root-equivalent access to the host"
	}
	return ""
}

func validateHostPorts(ports []HostPort) error {
	seen := make(map[int]bool, len(ports))
	for _, p := range ports {
		t, err := ParseHostPort(p)
		if err != nil {
			return err
		}
		if seen[t.Guest] {
			return fmt.Errorf("host_ports: guest port %d is listed twice", t.Guest)
		}
		seen[t.Guest] = true
	}
	return nil
}

// MergeHostPorts merges defaults and workspace host_ports entries; a
// workspace entry replaces a defaults entry with the same guest port.
func MergeHostPorts(defaults, workspace []HostPort) []HostPortTarget {
	var out []HostPortTarget
	index := map[int]int{}
	for _, list := range [][]HostPort{defaults, workspace} {
		for _, p := range list {
			t, err := ParseHostPort(p)
			if err != nil {
				continue
			}
			if i, ok := index[t.Guest]; ok {
				out[i] = t
				continue
			}
			index[t.Guest] = len(out)
			out = append(out, t)
		}
	}
	return out
}
