package vmprobe

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net"
	"net/netip"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"
)

// SpecVersion identifies the acceptance specification revision the probes implement.
const SpecVersion = "vm-acceptance/1"

// IPv6 modes a candidate may declare.
const (
	IPv6Enforced = "enforced"
	IPv6Disabled = "disabled"
)

// Export modes.
const (
	ModeRW = "rw"
	ModeRO = "ro"
)

// Canary is a fixture file with known content.
type Canary struct {
	Name   string `json:"name"`
	SHA256 string `json:"sha256"`
}

// Export is a host directory shared into the guest.
type Export struct {
	HostPath  string   `json:"host_path"`
	GuestPath string   `json:"guest_path"`
	Mode      string   `json:"mode"`
	Fixture   bool     `json:"fixture"`
	Canaries  []Canary `json:"canaries,omitempty"`
	// Listing is the sorted host directory listing at plan time (fixtures only).
	Listing []string `json:"listing,omitempty"`
}

// HostAddr is a host interface address with its on-link prefix.
type HostAddr struct {
	Iface  string `json:"iface"`
	Addr   string `json:"addr"`
	Prefix string `json:"prefix"`
}

// Plan is the shared input of every probe run for one candidate.
type Plan struct {
	SpecVersion   string      `json:"spec_version"`
	Nonce         string      `json:"nonce"`
	CreatedAt     time.Time   `json:"created_at"`
	Candidate     string      `json:"candidate"`
	PrimaryPort   int         `json:"primary_port"`
	AltPort       int         `json:"alt_port"`
	HostAddrs     []HostAddr  `json:"host_addrs"`
	LANSentinels  []string    `json:"lan_sentinels,omitempty"`
	PeerAddrs     []string    `json:"peer_addrs,omitempty"`
	HostResolvers []string    `json:"host_resolvers,omitempty"`
	Exceptions    []Exception `json:"exceptions,omitempty"`
	IPv6Mode      string      `json:"ipv6_mode"`
	Exports       []Export    `json:"exports"`
	// OutsideHostPath is a host directory that must never be visible to the guest.
	OutsideHostPath string   `json:"outside_host_path"`
	OutsideCanary   Canary   `json:"outside_canary"`
	AllowedSockets  []string `json:"allowed_sockets,omitempty"`
	VsockPorts      string   `json:"vsock_ports"`
}

// PlanOptions configures plan generation.
type PlanOptions struct {
	Candidate      string
	Dir            string
	GuestPrefix    string // replaces Dir in guest paths; empty keeps host paths
	PrimaryPort    int
	AltPort        int
	LANSentinels   []string
	PeerAddrs      []string
	HostResolvers  []string
	Exceptions     []Exception
	IPv6Mode       string
	ExtraExports   []Export
	AllowedSockets []string
	VsockPorts     string
}

func randomHex(n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("read random: %w", err)
	}
	return hex.EncodeToString(b), nil
}

func writeCanary(dir, prefix string) (Canary, error) {
	tok, err := randomHex(8)
	if err != nil {
		return Canary{}, err
	}
	content, err := randomHex(32)
	if err != nil {
		return Canary{}, err
	}
	name := prefix + "-" + tok
	if err := os.WriteFile(filepath.Join(dir, name), []byte(content+"\n"), 0o644); err != nil { //nolint:gosec // G306: fixture must be world-readable for the guest
		return Canary{}, fmt.Errorf("write canary: %w", err)
	}
	sum := sha256.Sum256([]byte(content + "\n"))
	return Canary{Name: name, SHA256: hex.EncodeToString(sum[:])}, nil
}

// ListDir returns the sorted names of entries in dir.
func ListDir(dir string) ([]string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("list %s: %w", dir, err)
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		names = append(names, e.Name())
	}
	slices.Sort(names)
	return names, nil
}

// HostAddresses enumerates non-loopback interface addresses of this host.
func HostAddresses() ([]HostAddr, error) {
	ifaces, err := net.Interfaces()
	if err != nil {
		return nil, fmt.Errorf("list interfaces: %w", err)
	}
	var out []HostAddr
	for _, ifc := range ifaces {
		if ifc.Flags&net.FlagUp == 0 || ifc.Flags&net.FlagLoopback != 0 {
			continue
		}
		addrs, err := ifc.Addrs()
		if err != nil {
			return nil, fmt.Errorf("addresses of %s: %w", ifc.Name, err)
		}
		for _, a := range addrs {
			ipn, ok := a.(*net.IPNet)
			if !ok {
				continue
			}
			ip, ok := netip.AddrFromSlice(ipn.IP)
			if !ok {
				continue
			}
			ip = ip.Unmap()
			ones, _ := ipn.Mask.Size()
			out = append(out, HostAddr{
				Iface:  ifc.Name,
				Addr:   ip.String(),
				Prefix: netip.PrefixFrom(ip, ones).Masked().String(),
			})
		}
	}
	return out, nil
}

// ResolvConfServers parses nameserver lines of a resolv.conf file.
func ResolvConfServers(path string) []string {
	data, err := os.ReadFile(path) //nolint:gosec // G304: fixed system path or operator-supplied path
	if err != nil {
		return nil
	}
	var out []string
	for line := range strings.SplitSeq(string(data), "\n") {
		f := strings.Fields(line)
		if len(f) >= 2 && f[0] == "nameserver" {
			out = append(out, f[1])
		}
	}
	return out
}

// NewPlan creates fixture directories under opts.Dir and returns the plan.
func NewPlan(opts PlanOptions) (*Plan, error) {
	if opts.IPv6Mode != IPv6Enforced && opts.IPv6Mode != IPv6Disabled {
		return nil, fmt.Errorf("ipv6 mode %q: want %s or %s", opts.IPv6Mode, IPv6Enforced, IPv6Disabled)
	}
	for _, e := range opts.Exceptions {
		if err := e.Validate(); err != nil {
			return nil, err
		}
	}
	dir, err := filepath.Abs(opts.Dir)
	if err != nil {
		return nil, fmt.Errorf("resolve fixture dir: %w", err)
	}
	nonce, err := randomHex(8)
	if err != nil {
		return nil, err
	}
	hostAddrs, err := HostAddresses()
	if err != nil {
		return nil, err
	}
	guestPath := func(hostPath string) string {
		if opts.GuestPrefix == "" {
			return hostPath
		}
		return opts.GuestPrefix + strings.TrimPrefix(hostPath, dir)
	}

	p := &Plan{
		SpecVersion:    SpecVersion,
		Nonce:          nonce,
		CreatedAt:      time.Now().UTC(),
		Candidate:      opts.Candidate,
		PrimaryPort:    opts.PrimaryPort,
		AltPort:        opts.AltPort,
		HostAddrs:      hostAddrs,
		LANSentinels:   opts.LANSentinels,
		PeerAddrs:      opts.PeerAddrs,
		HostResolvers:  opts.HostResolvers,
		Exceptions:     opts.Exceptions,
		IPv6Mode:       opts.IPv6Mode,
		AllowedSockets: opts.AllowedSockets,
		VsockPorts:     opts.VsockPorts,
	}
	if len(p.HostResolvers) == 0 {
		p.HostResolvers = ResolvConfServers("/etc/resolv.conf")
	}

	for _, sub := range []struct{ name, mode string }{{"rw", ModeRW}, {"ro", ModeRO}} {
		hp := filepath.Join(dir, sub.name)
		if err := os.MkdirAll(hp, 0o755); err != nil { //nolint:gosec // G301: fixture must be traversable by the guest
			return nil, fmt.Errorf("create fixture dir: %w", err)
		}
		c, err := writeCanary(hp, "canary-"+sub.name)
		if err != nil {
			return nil, err
		}
		listing, err := ListDir(hp)
		if err != nil {
			return nil, err
		}
		p.Exports = append(p.Exports, Export{
			HostPath: hp, GuestPath: guestPath(hp), Mode: sub.mode,
			Fixture: true, Canaries: []Canary{c}, Listing: listing,
		})
	}
	outside := filepath.Join(dir, "outside")
	if err := os.MkdirAll(outside, 0o755); err != nil { //nolint:gosec // G301: same permissions as the shared fixtures
		return nil, fmt.Errorf("create outside dir: %w", err)
	}
	oc, err := writeCanary(outside, "canary-outside")
	if err != nil {
		return nil, err
	}
	p.OutsideHostPath = outside
	p.OutsideCanary = oc
	p.Exports = append(p.Exports, opts.ExtraExports...)
	return p, nil
}

// WriteJSON writes v as indented JSON to path, or stdout when path is "-".
func WriteJSON(path string, v any) error {
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal: %w", err)
	}
	data = append(data, '\n')
	if path == "-" || path == "" {
		_, err = os.Stdout.Write(data)
		if err != nil {
			return fmt.Errorf("write stdout: %w", err)
		}
		return nil
	}
	if err := os.WriteFile(path, data, 0o644); err != nil { //nolint:gosec // G306: probe artifacts are not secret
		return fmt.Errorf("write %s: %w", path, err)
	}
	return nil
}

// ReadJSON decodes the JSON file at path into v.
func ReadJSON(path string, v any) error {
	data, err := os.ReadFile(path) //nolint:gosec // G304: operator-supplied artifact path
	if err != nil {
		return fmt.Errorf("read %s: %w", path, err)
	}
	if err := json.Unmarshal(data, v); err != nil {
		return fmt.Errorf("decode %s: %w", path, err)
	}
	return nil
}

// LoadPlan reads and checks a plan file.
func LoadPlan(path string) (*Plan, error) {
	var p Plan
	if err := ReadJSON(path, &p); err != nil {
		return nil, err
	}
	if p.SpecVersion != SpecVersion {
		return nil, fmt.Errorf("plan spec version %q, probe implements %q", p.SpecVersion, SpecVersion)
	}
	return &p, nil
}

// ExceptionFor returns whether any plan exception opens addr:port over proto.
func (p *Plan) ExceptionFor(addr netip.Addr, proto string, port int) bool {
	for _, e := range p.Exceptions {
		if e.Matches(addr, proto, port) {
			return true
		}
	}
	return false
}
