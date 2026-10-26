package netbroker

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/netip"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
)

type multi []string

func (m *multi) String() string     { return strings.Join(*m, ",") }
func (m *multi) Set(s string) error { *m = append(*m, s); return nil }

// Main runs the broker command line: it parses args (without the program
// name), serves the guest NIC on -socket until SIGINT or SIGTERM, and reloads
// the policy on SIGHUP. The jailoc binary exposes it as the hidden
// "netbroker" command.
func Main(args []string) error {
	fs := flag.NewFlagSet("netbroker", flag.ContinueOnError)
	socket := fs.String("socket", "", "unix socket the VMM connects its virtio-net device to (QEMU stream framing)")
	policyPath := fs.String("policy", "", "JSON policy file; reloaded on SIGHUP, an invalid reload denies all new flows")
	subnet := fs.String("subnet", "192.168.127.0/24", "virtual guest network")
	gateway := fs.String("gateway", "192.168.127.1", "gateway, DNS and DHCP address served by the broker")
	guest := fs.String("guest", "192.168.127.2", "address leased to the guest")
	logPath := fs.String("log", "-", "JSONL decision log, - for stderr")
	statusFD := fs.Int("status-fd", 0, "inherited file descriptor that receives lifecycle lines (ready, nic connected, nic disconnected) for a supervisor; 0 disables")
	var forwards, unixForwards, dnsFallback multi
	fs.Var(&forwards, "forward", "publish a guest TCP port: 127.0.0.1:HOSTPORT=GUESTPORT (repeatable, loopback only)")
	fs.Var(&unixForwards, "forward-unix", "publish a guest TCP port on a host unix socket: PATH=GUESTPORT (repeatable, mode 0600)")
	fs.Var(&dnsFallback, "dns-upstream", "DNS upstream when the policy names none (repeatable, default: host /etc/resolv.conf)")
	if err := fs.Parse(args); err != nil {
		return err
	}

	if *socket == "" || *policyPath == "" {
		return errors.New("-socket and -policy are required")
	}
	pol, err := loadPolicy(*policyPath)
	if err != nil {
		return err
	}
	cfg := Config{}
	if cfg.Subnet, err = netip.ParsePrefix(*subnet); err != nil {
		return fmt.Errorf("subnet: %w", err)
	}
	if cfg.Gateway, err = netip.ParseAddr(*gateway); err != nil {
		return fmt.Errorf("gateway: %w", err)
	}
	if cfg.GuestIP, err = netip.ParseAddr(*guest); err != nil {
		return fmt.Errorf("guest: %w", err)
	}
	for _, f := range forwards {
		fw, err := parseForward(f)
		if err != nil {
			return err
		}
		cfg.Forwards = append(cfg.Forwards, fw)
	}
	for _, f := range unixForwards {
		fw, err := parseUnixForward(f)
		if err != nil {
			return err
		}
		cfg.Forwards = append(cfg.Forwards, fw)
	}
	for _, s := range dnsFallback {
		ap, err := parseUpstream(s)
		if err != nil {
			return err
		}
		cfg.DNSFallback = append(cfg.DNSFallback, ap)
	}
	if len(cfg.DNSFallback) == 0 {
		// Re-read on every query: the host resolvers change with the network.
		cfg.HostDNS = func() []netip.AddrPort {
			var out []netip.AddrPort
			for _, s := range resolvConf("/etc/resolv.conf") {
				if ap, err := parseUpstream(s); err == nil {
					out = append(out, ap)
				}
			}
			return out
		}
	}
	var logw io.Writer = os.Stderr
	if *logPath != "-" {
		f, err := os.OpenFile(*logPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
		if err != nil {
			return fmt.Errorf("open log: %w", err)
		}
		defer func() { _ = f.Close() }()
		logw = f
	}
	cfg.Log = logw
	if *statusFD > 2 {
		cfg.Status = os.NewFile(uintptr(*statusFD), "status")
	}

	b, err := New(cfg, pol)
	if err != nil {
		return fmt.Errorf("create broker: %w", err)
	}
	if err := os.Remove(*socket); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("remove stale socket: %w", err)
	}
	l, err := net.Listen("unix", *socket)
	if err != nil {
		return fmt.Errorf("listen: %w", err)
	}
	defer func() { _ = os.Remove(*socket) }()
	if err := os.Chmod(*socket, 0o600); err != nil {
		return fmt.Errorf("chmod socket: %w", err)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	hup := make(chan os.Signal, 1)
	signal.Notify(hup, syscall.SIGHUP)
	go func() {
		for range hup {
			p, err := loadPolicy(*policyPath)
			if err != nil {
				fmt.Fprintln(os.Stderr, "netbroker: reload failed, denying all new flows:", err)
				p = DenyAll("invalid reload: " + err.Error())
			}
			b.SetPolicy(p)
		}
	}()
	return b.Run(ctx, l)
}

func loadPolicy(path string) (*Policy, error) {
	data, err := os.ReadFile(path) //nolint:gosec // G304: operator-supplied policy path
	if err != nil {
		return nil, fmt.Errorf("read policy: %w", err)
	}
	return ParsePolicy(data, path)
}

func parseForward(s string) (Forward, error) {
	host, guest, ok := strings.Cut(s, "=")
	if !ok {
		return Forward{}, fmt.Errorf("forward %q: want 127.0.0.1:HOSTPORT=GUESTPORT", s)
	}
	hp, err := netip.ParseAddrPort(host)
	if err != nil {
		return Forward{}, fmt.Errorf("forward %q: %w", s, err)
	}
	if !hp.Addr().IsLoopback() {
		return Forward{}, fmt.Errorf("forward %q: host address must be loopback", s)
	}
	gp, err := strconv.ParseUint(guest, 10, 16)
	if err != nil || gp == 0 {
		return Forward{}, fmt.Errorf("forward %q: invalid guest port", s)
	}
	return Forward{Host: hp, GuestPort: uint16(gp)}, nil
}

func parseUnixForward(s string) (Forward, error) {
	path, guest, ok := strings.Cut(s, "=")
	if !ok || path == "" {
		return Forward{}, fmt.Errorf("forward-unix %q: want PATH=GUESTPORT", s)
	}
	gp, err := strconv.ParseUint(guest, 10, 16)
	if err != nil || gp == 0 {
		return Forward{}, fmt.Errorf("forward-unix %q: invalid guest port", s)
	}
	return Forward{UnixPath: path, GuestPort: uint16(gp)}, nil
}

func parseUpstream(s string) (netip.AddrPort, error) {
	if ap, err := netip.ParseAddrPort(s); err == nil {
		return ap, nil
	}
	a, err := netip.ParseAddr(s)
	if err != nil {
		return netip.AddrPort{}, fmt.Errorf("dns upstream %q: %w", s, err)
	}
	return netip.AddrPortFrom(a, 53), nil
}

func resolvConf(path string) []string {
	data, err := os.ReadFile(path) //nolint:gosec // G304: fixed system path
	if err != nil {
		return nil
	}
	var out []string
	for _, line := range strings.Split(string(data), "\n") {
		f := strings.Fields(line)
		if len(f) >= 2 && f[0] == "nameserver" {
			out = append(out, f[1])
		}
	}
	return out
}
