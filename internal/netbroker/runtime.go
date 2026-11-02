package netbroker

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/netip"
	"path/filepath"
	"sort"
	"sync"
	"sync/atomic"
)

// Runtime is the part of the broker configuration that can change while the
// guest runs: published guest ports and gateway services. It is read from a
// JSON file at start and on SIGHUP.
type Runtime struct {
	Forwards []RuntimeForward `json:"forwards"`
	Services []RuntimeService `json:"services"`
}

// RuntimeForward publishes GuestPort on the host loopback address Host.
type RuntimeForward struct {
	Host      string `json:"host"`
	GuestPort uint16 `json:"guest_port"`
}

// RuntimeService relays the gateway's GuestPort to the host unix socket Path
// or, when TCP is set, to the host loopback TCP address TCP.
type RuntimeService struct {
	GuestPort uint16 `json:"guest_port"`
	Path      string `json:"path,omitempty"`
	TCP       string `json:"tcp,omitempty"`
}

// serviceTarget is the host endpoint a gateway service dials.
type serviceTarget struct {
	network string
	addr    string
}

func (s RuntimeService) target() serviceTarget {
	if s.TCP != "" {
		return serviceTarget{network: "tcp", addr: s.TCP}
	}
	return serviceTarget{network: "unix", addr: s.Path}
}

// validateServiceTarget accepts exactly one of an absolute unix socket path
// and a host loopback TCP address.
func validateServiceTarget(path, tcp string) error {
	switch {
	case path != "" && tcp != "":
		return fmt.Errorf("want a socket path or a TCP address, not both")
	case tcp != "":
		ap, err := netip.ParseAddrPort(tcp)
		if err != nil || !ap.Addr().IsLoopback() || ap.Port() == 0 {
			return fmt.Errorf("TCP target %q: want a loopback address and a non-zero port", tcp)
		}
	case !filepath.IsAbs(path):
		return fmt.Errorf("socket path %q: want an absolute path", path)
	}
	return nil
}

// ParseRuntime validates a runtime file. Unknown fields are rejected.
func ParseRuntime(data []byte, source string) (*Runtime, error) {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	var rt Runtime
	if err := dec.Decode(&rt); err != nil {
		return nil, fmt.Errorf("parse runtime %s: %w", source, err)
	}
	hosts := map[netip.AddrPort]bool{}
	for _, f := range rt.Forwards {
		hp, err := netip.ParseAddrPort(f.Host)
		if err != nil || !hp.Addr().IsLoopback() || hp.Port() == 0 || f.GuestPort == 0 {
			return nil, fmt.Errorf("runtime %s: forward %s -> %d: want a loopback host address and non-zero ports", source, f.Host, f.GuestPort)
		}
		if hosts[hp] {
			return nil, fmt.Errorf("runtime %s: duplicate forward host %s", source, f.Host)
		}
		hosts[hp] = true
	}
	ports := map[uint16]bool{}
	for _, s := range rt.Services {
		if s.GuestPort == 0 || s.GuestPort == 53 {
			return nil, fmt.Errorf("runtime %s: service %d: want a port other than 0 and 53", source, s.GuestPort)
		}
		if err := validateServiceTarget(s.Path, s.TCP); err != nil {
			return nil, fmt.Errorf("runtime %s: service %d: %w", source, s.GuestPort, err)
		}
		if ports[s.GuestPort] {
			return nil, fmt.Errorf("runtime %s: duplicate service port %d", source, s.GuestPort)
		}
		ports[s.GuestPort] = true
	}
	return &rt, nil
}

type runtimeForward struct {
	stop func()
}

type runtimeService struct {
	stop        func()
	ctx         context.Context
	route       atomic.Pointer[serviceRoute]
	cancelRoute context.CancelFunc
}

// retarget sends new connections to t and closes the open ones to the
// previous target, so a reload also revokes access to a replaced target.
func (r *runtimeService) retarget(t serviceTarget) {
	rctx, cancel := context.WithCancel(r.ctx)
	r.route.Store(&serviceRoute{target: t, ctx: rctx})
	if r.cancelRoute != nil {
		r.cancelRoute()
	}
	r.cancelRoute = cancel
}

// runtimeState tracks the listeners started from a Runtime.
type runtimeState struct {
	mu       sync.Mutex
	forwards map[RuntimeForward]*runtimeForward
	services map[uint16]*runtimeService
}

// ApplyRuntime reconciles the running forwards and services with rt:
// removed ones stop, new ones start, and a service whose target changed closes
// its open connections and dials the new target from the next connection. It
// returns one error per entry that failed to start.
func (b *Broker) ApplyRuntime(ctx context.Context, rt *Runtime) []error {
	s := &b.runtime
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.forwards == nil {
		s.forwards = map[RuntimeForward]*runtimeForward{}
		s.services = map[uint16]*runtimeService{}
	}
	wantF := map[RuntimeForward]bool{}
	for _, f := range rt.Forwards {
		wantF[f] = true
	}
	wantS := map[uint16]serviceTarget{}
	for _, sv := range rt.Services {
		wantS[sv.GuestPort] = sv.target()
	}
	// Stop removed entries first so a host port can move between guest ports.
	for f, r := range s.forwards {
		if !wantF[f] {
			r.stop()
			delete(s.forwards, f)
		}
	}
	for port, r := range s.services {
		if _, ok := wantS[port]; !ok {
			r.stop()
			delete(s.services, port)
		}
	}
	var errs []error
	for _, f := range sortedForwards(wantF) {
		if _, ok := s.forwards[f]; ok {
			continue
		}
		fctx, cancel := context.WithCancel(ctx)
		hp := netip.MustParseAddrPort(f.Host)
		closeL, err := b.startForward(fctx, Forward{Host: hp, GuestPort: f.GuestPort})
		if err != nil {
			cancel()
			errs = append(errs, fmt.Errorf("forward %s -> %d: %w", f.Host, f.GuestPort, err))
			continue
		}
		s.forwards[f] = &runtimeForward{stop: func() { closeL(); cancel() }}
	}
	for port, target := range wantS {
		if r, ok := s.services[port]; ok {
			if r.route.Load().target != target {
				r.retarget(target)
			}
			continue
		}
		sctx, cancel := context.WithCancel(ctx)
		r := &runtimeService{ctx: sctx}
		r.retarget(target)
		closeL, err := b.startServiceTarget(sctx, port, func() serviceRoute { return *r.route.Load() })
		if err != nil {
			cancel()
			errs = append(errs, fmt.Errorf("service %d: %w", port, err))
			continue
		}
		r.stop = func() { closeL(); cancel() }
		s.services[port] = r
	}
	return errs
}

func sortedForwards(m map[RuntimeForward]bool) []RuntimeForward {
	out := make([]RuntimeForward, 0, len(m))
	for f := range m {
		out = append(out, f)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Host != out[j].Host {
			return out[i].Host < out[j].Host
		}
		return out[i].GuestPort < out[j].GuestPort
	})
	return out
}
