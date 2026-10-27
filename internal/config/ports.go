package config

import (
	"fmt"
	"strconv"
	"strings"
)

// Port is a ports entry: "PORT" publishes a guest port on the same host
// port, "HOST:GUEST" maps them. TOML accepts integers and strings.
type Port string

// UnmarshalTOML accepts an integer or a string.
func (p *Port) UnmarshalTOML(v any) error {
	switch v := v.(type) {
	case int64:
		*p = Port(strconv.FormatInt(v, 10))
	case string:
		*p = Port(v)
	default:
		return fmt.Errorf("port must be a number or \"HOST:GUEST\", got %T", v)
	}
	return nil
}

// MarshalText writes the entry as a string.
func (p Port) MarshalText() ([]byte, error) { return []byte(p), nil }

// PortForward is a parsed ports entry.
type PortForward struct {
	Host  int
	Guest int
}

// ParsePort parses "PORT" or "HOST:GUEST" with ports in 1-65535.
func ParsePort(p Port) (PortForward, error) {
	host, guest, mapped := strings.Cut(string(p), ":")
	if !mapped {
		guest = host
	}
	h, errH := parsePortNumber(host)
	g, errG := parsePortNumber(guest)
	if errH != nil || errG != nil {
		return PortForward{}, fmt.Errorf("invalid port %q: want PORT or HOST:GUEST with ports 1-65535", string(p))
	}
	return PortForward{Host: h, Guest: g}, nil
}

func parsePortNumber(s string) (int, error) {
	n, err := strconv.Atoi(strings.TrimSpace(s))
	if err != nil || n < 1 || n > 65535 {
		return 0, fmt.Errorf("port %q out of range", s)
	}
	return n, nil
}

func validatePorts(ports []Port) error {
	seen := make(map[int]bool, len(ports))
	for _, p := range ports {
		f, err := ParsePort(p)
		if err != nil {
			return err
		}
		if seen[f.Host] {
			return fmt.Errorf("ports: host port %d is listed twice", f.Host)
		}
		seen[f.Host] = true
	}
	return nil
}

// MergePorts merges defaults and workspace ports entries; a workspace entry
// replaces a defaults entry with the same host port.
func MergePorts(defaults, workspace []Port) []PortForward {
	var out []PortForward
	index := map[int]int{}
	for _, list := range [][]Port{defaults, workspace} {
		for _, p := range list {
			f, err := ParsePort(p)
			if err != nil {
				continue
			}
			if i, ok := index[f.Host]; ok {
				out[i] = f
				continue
			}
			index[f.Host] = len(out)
			out = append(out, f)
		}
	}
	return out
}
