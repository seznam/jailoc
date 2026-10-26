package vmprobe

import (
	"context"
	"fmt"
	"net"
	"net/netip"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Probe kinds.
const (
	kindTCP    = "tcp"
	kindUDP    = "udp"
	kindTLS    = "tls"
	kindQUIC   = "quic"
	kindDNSUDP = "dns-udp"
	kindDNSTCP = "dns-tcp"
)

// Public reference targets used for positive controls.
const (
	PublicV4      = "1.1.1.1"
	PublicV6      = "2606:4700:4700::1111"
	PublicSNI     = "one.one.one.one"
	PublicName    = "example.com"
	PublicDNSName = "example.com"
)

// HostAliasNames are names runtimes commonly map to the host or gateway.
var HostAliasNames = []string{
	"host.docker.internal", "gateway.docker.internal", "host.containers.internal",
	"host.lima.internal", "host.orb.internal", "host.internal", "gateway.internal",
	"host.microsandbox.internal", "host.gondolin.internal", "localhost.localdomain.internal",
}

// MetadataTargets are cloud metadata endpoints.
var MetadataTargets = []string{"169.254.169.254:80", "[fd00:ec2::254]:80", "169.254.170.2:80"}

// DNSInterceptTarget is a TEST-NET-1 address that never runs a resolver. A
// DNS answer from it proves the runtime intercepts DNS sent to any address.
const DNSInterceptTarget = "192.0.2.53"

type probe struct {
	id         string
	kind       string
	target     string // host:port
	expect     string
	serverName string
	dnsName    string
	failFast   time.Duration // WARN when a denial takes longer
}

// GuestOptions configures a guest probe run.
type GuestOptions struct {
	Context     string
	Timeout     time.Duration
	Parallelism int
	Checks      []string // subset of: net, dns, iface, vsock, fs

	dnsIntercepted bool
}

// RunGuest executes the selected guest checks.
func RunGuest(ctx context.Context, p *Plan, opts GuestOptions) *Report {
	rep := NewReport(p, "guest", opts.Context)
	rep.LocalAddrs = localAddrs()
	want := func(c string) bool { return len(opts.Checks) == 0 || slices.Contains(opts.Checks, c) }
	if want("net") || want("dns") {
		res, intercepted := dnsInterception(ctx, opts.Timeout)
		rep.Add(res...)
		opts.dnsIntercepted = intercepted
	}
	var probes []probe
	if want("net") {
		probes = append(probes, netProbes(p)...)
	}
	if want("dns") {
		pr, res := dnsProbes(ctx, p, opts.Timeout)
		probes = append(probes, pr...)
		rep.Add(res...)
	}
	rep.Add(runProbes(ctx, p, opts, probes)...)
	if want("iface") {
		rep.Add(ifaceChecks(p)...)
	}
	if want("vsock") {
		rep.Add(vsockChecks(ctx, p)...)
	}
	if want("fs") {
		rep.Add(fsChecks(p, opts.Context)...)
	}
	rep.Finished = time.Now().UTC()
	return rep
}

func hostPort(a netip.Addr, port int) string {
	return net.JoinHostPort(a.String(), strconv.Itoa(port))
}

// localAddrs returns the addresses assigned in the current network namespace.
func localAddrs() []string {
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		return nil
	}
	var out []string
	for _, a := range addrs {
		if pfx, err := netip.ParsePrefix(a.String()); err == nil {
			out = append(out, pfx.Addr().String())
		}
	}
	return out
}

// dnsInterception queries a TEST-NET address that never runs a resolver.
// Runtimes that answer DNS sent to any destination make a DNS response from a
// denied address meaningless, so such responses become REVIEW instead of FAIL.
func dnsInterception(ctx context.Context, timeout time.Duration) ([]Result, bool) {
	target := net.JoinHostPort(DNSInterceptTarget, "53")
	var out []Result
	intercepted := false
	for _, proto := range []string{"udp", "tcp"} {
		obs, detail, _ := ProbeDNS(ctx, target, proto, PublicDNSName, timeout)
		r := Result{ID: "D-DNS-INTERCEPT-" + strings.ToUpper(proto), Target: target, Proto: "dns-" + proto, Expect: ExpectInfo, Observation: obs, Status: StatusPass, Detail: detail}
		if obs == ObsReached {
			intercepted = true
			r.Status = StatusReview
			r.Detail = "runtime answers DNS sent to a non-routable address; DNS answers from denied destinations are not evidence of reachability"
		}
		out = append(out, r)
	}
	return out, intercepted
}

// expectFor returns the expectation for a non-hard-deny destination.
func expectFor(p *Plan, a netip.Addr, proto string, port int) string {
	if Classify(a).Public {
		return ExpectAllow
	}
	if p.ExceptionFor(a, proto, port) {
		return ExpectAllow
	}
	return ExpectDeny
}

// zoned expands IPv6 link-local addresses to one candidate per local interface.
func zoned(a netip.Addr) []netip.Addr {
	if !a.Is6() || !a.IsLinkLocalUnicast() {
		return []netip.Addr{a}
	}
	ifaces, err := net.Interfaces()
	if err != nil {
		return []netip.Addr{a}
	}
	var out []netip.Addr
	for _, ifc := range ifaces {
		if ifc.Flags&net.FlagLoopback == 0 && ifc.Flags&net.FlagUp != 0 {
			out = append(out, a.WithZone(ifc.Name))
		}
	}
	return out
}

func sentinelProbes(p *Plan, id string, a netip.Addr, hard bool, ports []int, protos []string) []probe {
	var out []probe
	for _, za := range zoned(a) {
		for _, proto := range protos {
			for _, port := range ports {
				exp := ExpectDeny
				if !hard {
					exp = expectFor(p, za, proto, port)
				}
				out = append(out, probe{id: id + "-" + strings.ToUpper(proto), kind: proto, target: hostPort(za, port), expect: exp})
			}
		}
	}
	return out
}

func netProbes(p *Plan) []probe {
	pub4 := netip.MustParseAddr(PublicV4)
	probes := []probe{
		{id: "N-PUB-TLS-IP", kind: kindTLS, target: hostPort(pub4, 443), serverName: PublicSNI, expect: ExpectAllow},
		{id: "N-PUB-TLS-NAME", kind: kindTLS, target: net.JoinHostPort(PublicName, "443"), serverName: PublicName, expect: ExpectAllow},
		{id: "N-PUB-DNS-UDP", kind: kindDNSUDP, target: hostPort(pub4, 53), dnsName: PublicDNSName, expect: ExpectAllow},
		{id: "N-PUB-DNS-TCP", kind: kindDNSTCP, target: hostPort(pub4, 53), dnsName: PublicDNSName, expect: ExpectAllow},
		{id: "N-PUB-QUIC", kind: kindQUIC, target: hostPort(pub4, 443), expect: ExpectAllow},
	}
	pub6 := netip.MustParseAddr(PublicV6)
	if p.IPv6Mode == IPv6Enforced {
		probes = append(probes,
			probe{id: "N-PUB6-TLS", kind: kindTLS, target: hostPort(pub6, 443), serverName: PublicSNI, expect: ExpectAllow},
			probe{id: "N-PUB6-QUIC", kind: kindQUIC, target: hostPort(pub6, 443), expect: ExpectAllow},
		)
	} else {
		probes = append(probes,
			probe{id: "N-V6-DISABLED-TCP", kind: kindTCP, target: hostPort(pub6, 443), expect: ExpectDeny, failFast: time.Second},
			probe{id: "N-V6-DISABLED-UDP", kind: kindQUIC, target: hostPort(pub6, 443), expect: ExpectDeny},
		)
	}

	both := []int{p.PrimaryPort, p.AltPort}
	primary := []int{p.PrimaryPort}
	for _, h := range p.HostAddrs {
		a, err := netip.ParseAddr(h.Addr)
		if err != nil {
			continue
		}
		probes = append(probes, sentinelProbes(p, "N-HOST", a, true, both, []string{kindTCP})...)
		probes = append(probes, sentinelProbes(p, "N-HOST", a, true, primary, []string{kindUDP})...)
	}
	for _, gw := range defaultGateways() {
		probes = append(probes, sentinelProbes(p, "N-GW", gw, true, primary, []string{kindTCP, kindUDP})...)
	}
	var aliasAddrs []netip.Addr
	for _, name := range HostAliasNames {
		addrs, err := net.DefaultResolver.LookupNetIP(context.Background(), "ip", name)
		if err != nil {
			continue
		}
		for _, a := range addrs {
			if a = a.Unmap(); !slices.Contains(aliasAddrs, a) {
				aliasAddrs = append(aliasAddrs, a)
				probes = append(probes, sentinelProbes(p, "N-ALIAS", a, true, primary, []string{kindTCP, kindUDP})...)
			}
		}
	}
	for _, s := range p.LANSentinels {
		if a, err := netip.ParseAddr(s); err == nil {
			probes = append(probes, sentinelProbes(p, "N-LAN", a, false, both, []string{kindTCP, kindUDP})...)
		}
	}
	for _, s := range p.PeerAddrs {
		if a, err := netip.ParseAddr(s); err == nil {
			probes = append(probes, sentinelProbes(p, "N-PEER", a, true, primary, []string{kindTCP, kindUDP})...)
		}
	}
	for _, t := range MetadataTargets {
		probes = append(probes, probe{id: "N-META-TCP", kind: kindTCP, target: t, expect: ExpectDeny})
	}
	for _, r := range lanRouters(p) {
		for _, port := range []int{22, 80, 443} {
			probes = append(probes, probe{id: "N-LANGW-TCP", kind: kindTCP, target: hostPort(r, port), expect: expectFor(p, r, kindTCP, port)})
		}
		probes = append(probes, probe{id: "N-LANGW-DNS", kind: kindDNSUDP, target: hostPort(r, 53), dnsName: PublicDNSName, expect: expectFor(p, r, kindUDP, 53)})
	}
	return probes
}

// lanRouters guesses the first address of every private on-link host prefix.
func lanRouters(p *Plan) []netip.Addr {
	var out []netip.Addr
	for _, h := range p.HostAddrs {
		pfx, err := netip.ParsePrefix(h.Prefix)
		if err != nil || !pfx.Addr().Is4() || Classify(pfx.Addr()).Public || pfx.Bits() >= 31 {
			continue
		}
		r := pfx.Masked().Addr().Next()
		if !slices.Contains(out, r) {
			out = append(out, r)
		}
	}
	return out
}

func dnsProbes(ctx context.Context, p *Plan, timeout time.Duration) ([]probe, []Result) {
	var probes []probe
	var results []Result
	resolvers := ResolvConfServers("/etc/resolv.conf")
	if len(resolvers) == 0 {
		results = append(results, Result{ID: "D-RESOLVER", Status: StatusFail, Detail: "no nameserver in /etc/resolv.conf"})
	}
	var guestResolvers []netip.Addr
	for i, s := range resolvers {
		a, err := netip.ParseAddr(s)
		if err != nil {
			continue
		}
		guestResolvers = append(guestResolvers, a.WithZone(""))
		probes = append(probes, probe{id: "D-RESOLVER-UDP", kind: kindDNSUDP, target: hostPort(a, 53), dnsName: PublicDNSName, expect: ExpectAllow})
		if i == 0 {
			probes = append(probes, probe{id: "D-RESOLVER-TCP", kind: kindDNSTCP, target: hostPort(a, 53), dnsName: PublicDNSName, expect: ExpectAllow})
		}
		results = append(results, Result{ID: "D-RESOLVER-CLASS", Target: s, Status: StatusReview, Detail: "guest resolver class " + Classify(a).String() + "; must be the policy broker"})
	}
	isGuestResolver := func(a netip.Addr) bool { return slices.Contains(guestResolvers, a.WithZone("")) }
	for _, s := range p.HostResolvers {
		a, err := netip.ParseAddr(s)
		if err != nil || isGuestResolver(a) || Classify(a).Public {
			continue
		}
		probes = append(probes,
			probe{id: "D-PRIV-RESOLVER-UDP", kind: kindDNSUDP, target: hostPort(a, 53), dnsName: PublicDNSName, expect: expectFor(p, a, kindUDP, 53)},
			probe{id: "D-PRIV-RESOLVER-TCP", kind: kindDNSTCP, target: hostPort(a, 53), dnsName: PublicDNSName, expect: expectFor(p, a, kindTCP, 53)},
		)
	}
	for _, h := range p.HostAddrs {
		a, err := netip.ParseAddr(h.Addr)
		if err != nil || isGuestResolver(a) {
			continue
		}
		for _, za := range zoned(a) {
			probes = append(probes, probe{id: "D-HOST-DNS", kind: kindDNSUDP, target: hostPort(za, 53), dnsName: PublicDNSName, expect: ExpectDeny})
		}
	}

	// DNS rebinding: a public name resolving to a denied address must not open it.
	var rebindTargets []netip.Addr
	for _, h := range p.HostAddrs {
		if a, err := netip.ParseAddr(h.Addr); err == nil && a.Is4() && !Classify(a).Public {
			rebindTargets = append(rebindTargets, a)
			break
		}
	}
	for _, s := range p.LANSentinels {
		if a, err := netip.ParseAddr(s); err == nil && a.Is4() {
			rebindTargets = append(rebindTargets, a)
		}
	}
	for _, t := range rebindTargets {
		name := strings.ReplaceAll(t.String(), ".", "-") + ".sslip.io"
		cctx, cancel := context.WithTimeout(ctx, timeout)
		answers, err := net.DefaultResolver.LookupNetIP(cctx, "ip4", name)
		cancel()
		if err != nil {
			results = append(results, Result{ID: "D-REBIND-RESOLVE", Target: name, Status: StatusPass, Detail: "rebinding name not resolved: " + errDetail(err)})
			continue
		}
		results = append(results, Result{ID: "D-REBIND-RESOLVE", Target: name, Status: StatusReview, Detail: fmt.Sprintf("resolved to %v; connect must still be denied", answers)})
		for _, a := range answers {
			a = a.Unmap()
			probes = append(probes, probe{
				id: "D-REBIND-TCP", kind: kindTCP, target: net.JoinHostPort(name, strconv.Itoa(p.PrimaryPort)),
				expect: expectFor(p, a, kindTCP, p.PrimaryPort),
			})
		}
	}
	return probes, results
}

func runProbes(ctx context.Context, p *Plan, opts GuestOptions, probes []probe) []Result {
	par := max(opts.Parallelism, 1)
	results := make([]Result, len(probes))
	sem := make(chan struct{}, par)
	var wg sync.WaitGroup
	for i, pr := range probes {
		key := fmt.Sprintf("%s#%d", pr.id, i)
		wg.Add(1)
		sem <- struct{}{}
		go func() {
			defer wg.Done()
			defer func() { <-sem }()
			results[i] = execProbe(ctx, p, opts, pr, key)
		}()
	}
	wg.Wait()
	return results
}

func execProbe(ctx context.Context, p *Plan, opts GuestOptions, pr probe, key string) Result {
	start := time.Now()
	var obs, detail string
	payload := Payload(p.Nonce, key, opts.Context)
	switch pr.kind {
	case kindTCP:
		obs, detail = ProbeTCP(ctx, pr.target, payload, opts.Timeout)
	case kindUDP:
		obs, detail = ProbeUDP(ctx, pr.target, payload, opts.Timeout)
	case kindTLS:
		obs, detail = ProbeTLS(ctx, pr.target, pr.serverName, opts.Timeout)
	case kindQUIC:
		obs, detail = ProbeQUIC(ctx, pr.target, opts.Timeout)
	case kindDNSUDP:
		obs, detail, _ = ProbeDNS(ctx, pr.target, "udp", pr.dnsName, opts.Timeout)
	case kindDNSTCP:
		obs, detail, _ = ProbeDNS(ctx, pr.target, "tcp", pr.dnsName, opts.Timeout)
	}
	elapsed := time.Since(start)
	st := Evaluate(pr.expect, obs)
	if st == StatusFail && opts.dnsIntercepted && obs == ObsReached && (pr.kind == kindDNSUDP || pr.kind == kindDNSTCP) {
		st = StatusReview
		detail += "; answered while the runtime intercepts DNS (D-DNS-INTERCEPT), the destination itself may be unreachable"
	}
	if st == StatusPass && pr.failFast > 0 && elapsed > pr.failFast {
		st = StatusWarn
		detail += fmt.Sprintf("; denial took %s (want < %s)", elapsed.Round(time.Millisecond), pr.failFast)
	}
	return Result{
		ID: pr.id, Key: key, Target: pr.target, Proto: pr.kind, Expect: pr.expect,
		Observation: obs, Status: st, Detail: detail, Millis: elapsed.Milliseconds(),
	}
}
