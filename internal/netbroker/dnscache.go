package netbroker

import (
	"net/netip"
	"strings"
	"sync"
	"time"

	"golang.org/x/net/dns/dnsmessage"
)

const (
	minDNSCacheTTL = 60 * time.Second
	maxDNSCacheTTL = 24 * time.Hour
	maxDNSCacheIPs = 4096
	dnsTypeSVCB    = dnsmessage.Type(64)
	dnsTypeHTTPS   = dnsmessage.Type(65)
)

// nameSet matches domain names against exact and "*.suffix" patterns in O(labels).
type nameSet struct {
	exact    map[string]struct{}
	wildcard map[string]struct{}
}

func newNameSet(patterns []string) nameSet {
	s := nameSet{
		exact:    make(map[string]struct{}),
		wildcard: make(map[string]struct{}),
	}
	for _, p := range patterns {
		p = normalizeName(p)
		if p == "" {
			continue
		}
		if rest, ok := strings.CutPrefix(p, "*."); ok {
			if rest != "" {
				s.wildcard[rest] = struct{}{}
			}
			continue
		}
		s.exact[p] = struct{}{}
	}
	return s
}

func (s nameSet) empty() bool {
	return len(s.exact) == 0 && len(s.wildcard) == 0
}

// matches reports whether name matches an exact entry or a "*.suffix" entry.
// "*.example.com" matches "a.example.com" and "a.b.example.com", not "example.com".
func (s nameSet) matches(name string) bool {
	name = normalizeName(name)
	if name == "" {
		return false
	}
	if _, ok := s.exact[name]; ok {
		return true
	}
	if len(s.wildcard) == 0 {
		return false
	}
	for rest := name; ; {
		_, after, ok := strings.Cut(rest, ".")
		if !ok {
			return false
		}
		if _, match := s.wildcard[after]; match {
			return true
		}
		rest = after
	}
}

// dnsCache maps resolved IP addresses back to the DNS names that returned
// them, bounded by TTL.
type dnsCache struct {
	mu      sync.Mutex
	entries map[netip.Addr]map[string]time.Time
	now     func() time.Time
}

func newDNSCache() *dnsCache {
	return &dnsCache{
		entries: make(map[netip.Addr]map[string]time.Time),
		now:     time.Now,
	}
}

// record stores addr -> name with ttl.
func (c *dnsCache) record(addr netip.Addr, name string, ttl time.Duration) {
	if c == nil || !addr.IsValid() {
		return
	}
	name = normalizeName(name)
	if name == "" {
		return
	}
	addr = addr.WithZone("").Unmap()
	if ttl < minDNSCacheTTL {
		ttl = minDNSCacheTTL
	} else if ttl > maxDNSCacheTTL {
		ttl = maxDNSCacheTTL
	}
	now := c.now()
	exp := now.Add(ttl)

	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.entries) >= maxDNSCacheIPs {
		c.evictExpiredLocked(now)
	}
	names := c.entries[addr]
	if names == nil {
		if len(c.entries) >= maxDNSCacheIPs {
			return
		}
		names = make(map[string]time.Time, 1)
		c.entries[addr] = names
	}
	if prev, ok := names[name]; !ok || exp.After(prev) {
		names[name] = exp
	}
}

// lookup returns the non-expired DNS names recorded for addr.
func (c *dnsCache) lookup(addr netip.Addr) []string {
	if c == nil || !addr.IsValid() {
		return nil
	}
	addr = addr.WithZone("").Unmap()
	now := c.now()

	c.mu.Lock()
	defer c.mu.Unlock()
	names := c.entries[addr]
	if len(names) == 0 {
		return nil
	}
	var out []string
	for name, exp := range names {
		if now.After(exp) {
			delete(names, name)
			continue
		}
		out = append(out, name)
	}
	if len(names) == 0 {
		delete(c.entries, addr)
	}
	return out
}

func (c *dnsCache) evictExpiredLocked(now time.Time) {
	for addr, names := range c.entries {
		for name, exp := range names {
			if now.After(exp) {
				delete(names, name)
			}
		}
		if len(names) == 0 {
			delete(c.entries, addr)
		}
	}
}
