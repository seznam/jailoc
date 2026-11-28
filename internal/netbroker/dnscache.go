package netbroker

import (
	"net/netip"
	"slices"
	"strings"
	"sync"
	"time"

	"golang.org/x/net/dns/dnsmessage"
)

const (
	maxDNSCacheTTL   = 24 * time.Hour
	maxDNSCacheIPs   = 4096
	maxDNSCachePerIP = 64
	dnsTypeSVCB      = dnsmessage.Type(64)
	dnsTypeHTTPS     = dnsmessage.Type(65)
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
// them, bounded by TTL. Each name keeps the DNS chain it came from (the
// question, CNAME names and record owner) so a private address can be
// revalidated against the current internal zones on every lookup.
type dnsCache struct {
	mu      sync.Mutex
	entries map[netip.Addr]map[string]cacheEntry
	now     func() time.Time
}

type cacheEntry struct {
	exp   time.Time
	chain []string
}

func newDNSCache() *dnsCache {
	return &dnsCache{
		entries: make(map[netip.Addr]map[string]cacheEntry),
		now:     time.Now,
	}
}

// record stores addr -> name with ttl.
func (c *dnsCache) record(addr netip.Addr, name string, ttl time.Duration) {
	c.recordChain(addr, name, ttl, nil)
}

// recordChain stores addr -> name with ttl and the DNS chain names of the
// answer. A later record of the same name replaces both the expiry and the
// chain, so a name never stays trusted longer than the answer that trusted it.
func (c *dnsCache) recordChain(addr netip.Addr, name string, ttl time.Duration, chain []string) {
	if c == nil || !addr.IsValid() || ttl <= 0 {
		return
	}
	name = normalizeName(name)
	if name == "" {
		return
	}
	addr = addr.WithZone("").Unmap()
	if ttl > maxDNSCacheTTL {
		ttl = maxDNSCacheTTL
	}
	// Strip the monotonic reading so TTL expiry follows wall clock time across
	// host sleep.
	now := c.now().Round(0)
	exp := now.Add(ttl)

	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.entries) >= maxDNSCacheIPs {
		c.evictExpiredLocked(now)
	}
	names := c.entries[addr]
	if names != nil {
		for k, e := range names {
			if now.After(e.exp) {
				delete(names, k)
			}
		}
		if len(names) == 0 {
			delete(c.entries, addr)
			names = nil
		}
	}
	if names == nil {
		if len(c.entries) >= maxDNSCacheIPs {
			return
		}
		names = make(map[string]cacheEntry, 1)
		c.entries[addr] = names
	}
	if _, exists := names[name]; !exists && len(names) >= maxDNSCachePerIP {
		return
	}
	names[name] = cacheEntry{exp: exp, chain: chain}
}

// lookup returns the non-expired DNS names recorded for addr.
func (c *dnsCache) lookup(addr netip.Addr) []string {
	names, _ := c.lookupTrusted(addr, nil)
	return names
}

// lookupTrusted returns the non-expired DNS names recorded for addr and the
// subset of them whose DNS chain trusted rejects; a nil trusted accepts
// every name.
func (c *dnsCache) lookupTrusted(addr netip.Addr, trusted func(chain []string) bool) (names []string, untrusted map[string]bool) {
	if c == nil || !addr.IsValid() {
		return nil, nil
	}
	addr = addr.WithZone("").Unmap()
	now := c.now().Round(0)

	c.mu.Lock()
	defer c.mu.Unlock()
	entries := c.entries[addr]
	for name, e := range entries {
		if now.After(e.exp) {
			delete(entries, name)
			continue
		}
		names = append(names, name)
		if trusted != nil && !trusted(e.chain) {
			if untrusted == nil {
				untrusted = map[string]bool{}
			}
			untrusted[name] = true
		}
	}
	if entries != nil && len(entries) == 0 {
		delete(c.entries, addr)
	}
	slices.Sort(names)
	return names, untrusted
}

func (c *dnsCache) evictExpiredLocked(now time.Time) {
	for addr, names := range c.entries {
		for name, e := range names {
			if now.After(e.exp) {
				delete(names, name)
			}
		}
		if len(names) == 0 {
			delete(c.entries, addr)
		}
	}
}
