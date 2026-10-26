package vmprobe

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/netip"
	"strings"
	"time"
)

// TimelineEntry is one watch tick.
type TimelineEntry struct {
	Time    time.Time `json:"time"`
	DenyTCP string    `json:"deny_tcp"`
	DenyUDP string    `json:"deny_udp"`
	Public  string    `json:"public"`
}

func watchDenyTarget(p *Plan) (netip.Addr, bool) {
	for _, h := range p.HostAddrs {
		a, err := netip.ParseAddr(h.Addr)
		if err == nil && !a.IsLinkLocalUnicast() {
			return a, true
		}
	}
	return netip.Addr{}, false
}

// RunWatch probes a hard-deny host address and a public target on every tick
// so broker kills, restarts and cached boots can be observed (spec L-FAILCLOSED).
func RunWatch(ctx context.Context, p *Plan, ctxName string, interval, duration, timeout time.Duration, timeline io.Writer) *Report {
	rep := NewReport(p, "watch", ctxName)
	deny, ok := watchDenyTarget(p)
	if !ok {
		rep.Add(Result{ID: "L-FAILCLOSED-DENY", Status: StatusFail, Detail: "plan has no usable host address"})
		rep.Finished = time.Now().UTC()
		return rep
	}
	enc := json.NewEncoder(timeline)
	end := time.Now().Add(duration)
	var leaks []string
	var ticks, publicDown int
	var transitions []string
	lastPublic := ""
	pub := hostPort(netip.MustParseAddr(PublicV4), 443)
	for tick := 0; time.Now().Before(end) && ctx.Err() == nil; tick++ {
		start := time.Now()
		key := fmt.Sprintf("L-FAILCLOSED#%d", tick)
		e := TimelineEntry{Time: start.UTC()}
		e.DenyTCP, _ = ProbeTCP(ctx, hostPort(deny, p.PrimaryPort), Payload(p.Nonce, key, ctxName), timeout)
		e.DenyUDP, _ = ProbeUDP(ctx, hostPort(deny, p.PrimaryPort), Payload(p.Nonce, key, ctxName), timeout)
		e.Public, _ = ProbeTLS(ctx, pub, PublicSNI, timeout)
		_ = enc.Encode(e)
		ticks++
		if Evaluate(ExpectDeny, e.DenyTCP) != StatusPass || Evaluate(ExpectDeny, e.DenyUDP) != StatusPass {
			leaks = append(leaks, fmt.Sprintf("%s tcp=%s udp=%s", e.Time.Format(time.RFC3339), e.DenyTCP, e.DenyUDP))
		}
		if e.Public != ObsReached {
			publicDown++
		}
		if e.Public != lastPublic {
			transitions = append(transitions, e.Time.Format("15:04:05")+"="+e.Public)
			lastPublic = e.Public
		}
		select {
		case <-ctx.Done():
		case <-time.After(time.Until(start.Add(interval))):
		}
	}
	deny4 := Result{ID: "L-FAILCLOSED-DENY", Key: "L-FAILCLOSED", Target: hostPort(deny, p.PrimaryPort), Expect: ExpectDeny, Status: StatusPass, Detail: fmt.Sprintf("%d ticks, no leak", ticks)}
	if len(leaks) > 0 {
		deny4.Status = StatusFail
		deny4.Detail = fmt.Sprintf("%d/%d ticks leaked: %s", len(leaks), ticks, strings.Join(leaks, ", "))
	}
	rep.Add(deny4, Result{
		ID: "L-FAILCLOSED-PUBLIC", Target: pub, Status: StatusReview,
		Detail: fmt.Sprintf("public unreachable in %d/%d ticks; transitions %s", publicDown, ticks, strings.Join(transitions, " ")),
	})
	rep.Finished = time.Now().UTC()
	return rep
}

// RunIngress checks from the host that a published workspace port answers on
// loopback only (spec L-INGRESS).
func RunIngress(ctx context.Context, p *Plan, port int, timeout time.Duration) *Report {
	rep := NewReport(p, "ingress", "host")
	dial := func(addr string) error {
		d := net.Dialer{Timeout: timeout}
		c, err := d.DialContext(ctx, "tcp", addr)
		if err != nil {
			return fmt.Errorf("dial %s: %w", addr, err)
		}
		return c.Close()
	}
	lo := hostPort(netip.MustParseAddr("127.0.0.1"), port)
	r := Result{ID: "L-INGRESS-LOOPBACK", Target: lo, Expect: ExpectAllow, Status: StatusPass, Observation: "connected"}
	if err := dial(lo); err != nil {
		r.Status, r.Observation, r.Detail = StatusFail, ObsBlocked, errDetail(err)
	}
	rep.Add(r)
	for _, h := range p.HostAddrs {
		a, err := netip.ParseAddr(h.Addr)
		if err != nil {
			continue
		}
		if a.IsLinkLocalUnicast() && a.Is6() {
			a = a.WithZone(h.Iface)
		}
		t := hostPort(a, port)
		r := Result{ID: "L-INGRESS-EXTERNAL", Target: t, Expect: ExpectDeny, Status: StatusPass, Observation: ObsBlocked}
		if err := dial(t); err == nil {
			r.Status, r.Observation, r.Detail = StatusFail, "connected", "published port reachable on a non-loopback host address"
		} else {
			r.Detail = errDetail(err)
		}
		rep.Add(r)
	}
	rep.Finished = time.Now().UTC()
	return rep
}
