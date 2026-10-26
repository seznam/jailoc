package vmprobe

import (
	"context"
	"net"
	"net/netip"
	"strconv"
	"strings"
	"time"
)

// RunSelfTest runs on the host after sentinels start. It proves every host
// sentinel address answers the probes the guest later expects to be denied;
// without it a passing deny check could be caused by a host firewall or a dead
// sentinel instead of the candidate's policy (spec S-SELFTEST).
func RunSelfTest(ctx context.Context, p *Plan, timeout time.Duration) *Report {
	rep := NewReport(p, "selftest", "host")
	i := 0
	probe := func(id string, a netip.Addr, proto string, port int, soft string) {
		key := id + "#" + strconv.Itoa(i)
		i++
		target := hostPort(a, port)
		var obs, detail string
		if proto == kindTCP {
			obs, detail = ProbeTCP(ctx, target, Payload(p.Nonce, key, "host"), timeout)
		} else {
			obs, detail = ProbeUDP(ctx, target, Payload(p.Nonce, key, "host"), timeout)
		}
		st := Evaluate(ExpectAllow, obs)
		if st == StatusFail && detail == "address not available" {
			st = StatusWarn
			detail += "; interface address is not dialable from the host"
		} else if st == StatusFail && soft != "" {
			st = StatusWarn
			detail += "; " + soft
		}
		rep.Add(Result{ID: id, Key: key, Target: target, Proto: proto, Expect: ExpectAllow, Observation: obs, Status: st, Detail: detail})
	}
	for _, h := range p.HostAddrs {
		a, err := netip.ParseAddr(h.Addr)
		if err != nil {
			continue
		}
		var soft string
		if a.Is6() && a.IsLinkLocalUnicast() {
			a = a.WithZone(h.Iface)
			soft = "link-local sentinel unreachable from the host, guest link-local deny checks are not conclusive"
		}
		if ifc, err := net.InterfaceByName(h.Iface); err == nil && ifc.Flags&net.FlagUp == 0 {
			soft = "host interface " + h.Iface + " is down, deny checks for this address are not conclusive"
		}
		for _, port := range []int{p.PrimaryPort, p.AltPort} {
			probe("S-SELFTEST-HOST-TCP", a, kindTCP, port, soft)
		}
		probe("S-SELFTEST-HOST-UDP", a, kindUDP, p.PrimaryPort, soft)
	}
	for _, s := range p.LANSentinels {
		if a, err := netip.ParseAddr(s); err == nil {
			for _, proto := range []string{kindTCP, kindUDP} {
				for _, port := range []int{p.PrimaryPort, p.AltPort} {
					probe("S-SELFTEST-LAN-"+strings.ToUpper(proto), a, proto, port, "")
				}
			}
		}
	}
	rep.Finished = time.Now().UTC()
	return rep
}
