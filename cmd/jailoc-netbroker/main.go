// Command jailoc-netbroker serves the single network interface of a VM
// workspace from a userspace TCP/IP stack and enforces the workspace network
// policy on the real destination of every guest flow. See
// docs/research/p2-broker.md.
package main

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

	"github.com/seznam/jailoc/internal/netbroker"
)

type multi []string

func (m *multi) String() string     { return strings.Join(*m, ",") }
func (m *multi) Set(s string) error { *m = append(*m, s); return nil }

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "jailoc-netbroker:", err)
		os.Exit(1)
	}
}

func run() error {
	socket := flag.String("socket", "", "unix socket the VMM connects its virtio-net device to (QEMU stream framing)")
	policyPath := flag.String("policy", "", "JSON policy file; reloaded on SIGHUP, an invalid reload denies all new flows")
	subnet := flag.String("subnet", "192.168.127.0/24", "virtual guest network")
	gateway := flag.String("gateway", "192.168.127.1", "gateway, DNS and DHCP address served by the broker")
	guest := flag.String("guest", "192.168.127.2", "address leased to the guest")
	logPath := flag.String("log", "-", "JSONL decision log, - for stderr")
	var forwards, dnsFallback multi
	flag.Var(&forwards, "forward", "publish a guest TCP port: 127.0.0.1:HOSTPORT=GUESTPORT (repeatable, loopback only)")
	flag.Var(&dnsFallback, "dns-upstream", "DNS upstream when the policy names none (repeatable, default: host /etc/resolv.conf)")
	flag.Parse()

	if *socket == "" || *policyPath == "" {
		return errors.New("-socket and -policy are required")
	}
	pol, err := loadPolicy(*policyPath)
	if err != nil {
		return err
	}
	cfg := netbroker.Config{}
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
	if len(dnsFallback) == 0 {
		dnsFallback = resolvConf("/etc/resolv.conf")
	}
	for _, s := range dnsFallback {
		ap, err := netip.ParseAddrPort(s)
		if err != nil {
			a, aerr := netip.ParseAddr(s)
			if aerr != nil {
				return fmt.Errorf("dns upstream %q: %w", s, aerr)
			}
			ap = netip.AddrPortFrom(a, 53)
		}
		cfg.DNSFallback = append(cfg.DNSFallback, ap)
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

	b, err := netbroker.New(cfg, pol)
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
				fmt.Fprintln(os.Stderr, "jailoc-netbroker: reload failed, denying all new flows:", err)
				p = netbroker.DenyAll("invalid reload: " + err.Error())
			}
			b.SetPolicy(p)
		}
	}()
	return b.Run(ctx, l)
}

func loadPolicy(path string) (*netbroker.Policy, error) {
	data, err := os.ReadFile(path) //nolint:gosec // G304: operator-supplied policy path
	if err != nil {
		return nil, fmt.Errorf("read policy: %w", err)
	}
	return netbroker.ParsePolicy(data, path)
}

func parseForward(s string) (netbroker.Forward, error) {
	host, guest, ok := strings.Cut(s, "=")
	if !ok {
		return netbroker.Forward{}, fmt.Errorf("forward %q: want 127.0.0.1:HOSTPORT=GUESTPORT", s)
	}
	hp, err := netip.ParseAddrPort(host)
	if err != nil {
		return netbroker.Forward{}, fmt.Errorf("forward %q: %w", s, err)
	}
	if !hp.Addr().IsLoopback() {
		return netbroker.Forward{}, fmt.Errorf("forward %q: host address must be loopback", s)
	}
	gp, err := strconv.ParseUint(guest, 10, 16)
	if err != nil || gp == 0 {
		return netbroker.Forward{}, fmt.Errorf("forward %q: invalid guest port", s)
	}
	return netbroker.Forward{Host: hp, GuestPort: uint16(gp)}, nil
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
