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

// RuntimeService relays the gateway's GuestPort to the host unix socket Path.
type RuntimeService struct {
	GuestPort uint16 `json:"guest_port"`
	Path      string `json:"path"`
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
		if s.GuestPort == 0 || s.GuestPort == 53 || !filepath.IsAbs(s.Path) {
			return nil, fmt.Errorf("runtime %s: service %d -> %q: want a port other than 0 and 53 and an absolute socket path", source, s.GuestPort, s.Path)
		}
		if ports[s.GuestPort] {
			return nil, fmt.Errorf("runtime %s: duplicate service port %d", source, s.GuestPort)
		}
		ports[s.GuestPort] = true
	}
	return &rt, nil
}

type runtimeForward struct {
	cancel context.CancelFunc
}

type runtimeService struct {
	cancel context.CancelFunc
	path   atomic.Pointer[string]
}

// runtimeState tracks the listeners started from a Runtime.
type runtimeState struct {
	mu       sync.Mutex
	forwards map[RuntimeForward]*runtimeForward
	services map[uint16]*runtimeService
}

// ApplyRuntime reconciles the running forwards and services with rt:
// removed ones stop, new ones start, and a changed service path applies to
// the next connection. It returns one error per entry that failed to start.
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
	wantS := map[uint16]string{}
	for _, sv := range rt.Services {
		wantS[sv.GuestPort] = sv.Path
	}
	// Stop removed entries first so a host port can move between guest ports.
	for f, r := range s.forwards {
		if !wantF[f] {
			r.cancel()
			delete(s.forwards, f)
		}
	}
	for port, r := range s.services {
		if _, ok := wantS[port]; !ok {
			r.cancel()
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
		if err := b.startForward(fctx, Forward{Host: hp, GuestPort: f.GuestPort}); err != nil {
			cancel()
			errs = append(errs, fmt.Errorf("forward %s -> %d: %w", f.Host, f.GuestPort, err))
			continue
		}
		s.forwards[f] = &runtimeForward{cancel: cancel}
	}
	for port, path := range wantS {
		if r, ok := s.services[port]; ok {
			r.path.Store(&path)
			continue
		}
		r := &runtimeService{}
		r.path.Store(&path)
		sctx, cancel := context.WithCancel(ctx)
		if err := b.startServicePath(sctx, port, func() string { return *r.path.Load() }); err != nil {
			cancel()
			errs = append(errs, fmt.Errorf("service %d: %w", port, err))
			continue
		}
		r.cancel = cancel
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
