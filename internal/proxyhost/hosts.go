package proxyhost

import (
	"bytes"
	"fmt"
	"net/netip"
	"sort"
	"strings"
)

const (
	beginMarker = "# BEGIN jailoc proxy hosts"
	endMarker   = "# END jailoc proxy hosts"
	beginWords  = "BEGIN jailoc proxy hosts"
	endWords    = "END jailoc proxy hosts"
	domain      = "jailoc.local"
	ipv4        = "127.0.0.1"
	ipv6        = "::1"
	maxLabel    = 63
)

// Name is one proxy host name request. Workspace yields ws.jailoc.local;
// a non-empty Route yields route.ws.jailoc.local.
type Name struct {
	Workspace string
	Route     string
}

// Ledger records the exact bytes a previous plan inserted into a hosts file,
// including any line separator that was needed to terminate the original
// last line.
type Ledger struct {
	Owned []byte
}

// Plan is the planned hosts file content and the ledger needed to reverse it.
type Plan struct {
	Content []byte
	Ledger  Ledger
}

// PlanHosts computes the hosts file content that holds exactly the records for
// desired, inside one marker block owned by ledger. An empty desired removes
// the block. Bytes outside the owned block are preserved. On error the
// returned Plan is zero.
func PlanHosts(current []byte, ledger Ledger, desired []Name) (Plan, error) {
	hosts, err := hostNames(desired)
	if err != nil {
		return Plan{}, err
	}
	markers, err := scanMarkers(string(current))
	if err != nil {
		return Plan{}, err
	}

	if len(ledger.Owned) == 0 {
		return planFresh(current, markers, hosts)
	}
	return planOwned(current, ledger, markers, hosts)
}

func planFresh(current []byte, markers markerCount, hosts []string) (Plan, error) {
	if markers.begins != 0 || markers.ends != 0 {
		return Plan{}, fmt.Errorf("proxyhost: hosts file has an existing marker block but the ledger is empty; refusing to adopt it")
	}
	if len(hosts) == 0 {
		return Plan{Content: bytes.Clone(current)}, nil
	}
	if err := checkConflicts(string(current), hosts); err != nil {
		return Plan{}, err
	}

	eol := detectEOL(current)
	sep := ""
	if len(current) > 0 && current[len(current)-1] != '\n' {
		sep = eol
	}
	owned := []byte(sep + renderBlock(hosts, eol))
	content := make([]byte, 0, len(current)+len(owned))
	content = append(content, current...)
	content = append(content, owned...)
	return Plan{Content: content, Ledger: Ledger{Owned: owned}}, nil
}

func planOwned(current []byte, ledger Ledger, markers markerCount, hosts []string) (Plan, error) {
	sep, eol, err := parseOwned(string(ledger.Owned))
	if err != nil {
		return Plan{}, err
	}
	if markers.begins != 1 || markers.ends != 1 {
		return Plan{}, fmt.Errorf("proxyhost: hosts file has %d begin and %d end markers, ledger expects exactly one block", markers.begins, markers.ends)
	}
	idx := bytes.Index(current, ledger.Owned)
	if idx < 0 {
		return Plan{}, fmt.Errorf("proxyhost: hosts file marker block differs from the ledger bytes")
	}
	if sep == "" && idx > 0 && current[idx-1] != '\n' {
		return Plan{}, fmt.Errorf("proxyhost: ledger block does not start at a line start")
	}
	prefix := current[:idx]
	suffix := current[idx+len(ledger.Owned):]
	if len(hosts) > 0 {
		if err := checkConflicts(string(prefix), hosts); err != nil {
			return Plan{}, err
		}
		if err := checkConflicts(string(suffix), hosts); err != nil {
			return Plan{}, err
		}
	}

	var owned []byte
	if len(hosts) > 0 {
		owned = []byte(sep + renderBlock(hosts, eol))
	} else if len(suffix) > 0 {
		// Bytes were added after our block; keep the separator so the
		// original last line stays terminated.
		prefix = append(bytes.Clone(prefix), sep...)
	}
	content := make([]byte, 0, len(prefix)+len(owned)+len(suffix))
	content = append(content, prefix...)
	content = append(content, owned...)
	content = append(content, suffix...)
	return Plan{Content: content, Ledger: Ledger{Owned: owned}}, nil
}

func hostNames(desired []Name) ([]string, error) {
	set := make(map[string]struct{}, len(desired))
	for _, n := range desired {
		if !validLabel(n.Workspace) {
			return nil, fmt.Errorf("proxyhost: invalid workspace %q: want a lowercase DNS label of 1-%d characters", n.Workspace, maxLabel)
		}
		host := n.Workspace + "." + domain
		if n.Route != "" {
			if !validLabel(n.Route) {
				return nil, fmt.Errorf("proxyhost: invalid route %q: want a lowercase DNS label of 1-%d characters", n.Route, maxLabel)
			}
			host = n.Route + "." + host
		}
		set[host] = struct{}{}
	}
	hosts := make([]string, 0, len(set))
	for h := range set {
		hosts = append(hosts, h)
	}
	sort.Strings(hosts)
	return hosts, nil
}

func validLabel(s string) bool {
	if len(s) == 0 || len(s) > maxLabel {
		return false
	}
	if s[0] == '-' || s[len(s)-1] == '-' {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if (c < 'a' || c > 'z') && (c < '0' || c > '9') && c != '-' {
			return false
		}
	}
	return true
}

func renderBlock(hosts []string, eol string) string {
	var b strings.Builder
	b.WriteString(beginMarker + eol)
	for _, h := range hosts {
		b.WriteString(ipv4 + " " + h + eol)
		b.WriteString(ipv6 + " " + h + eol)
	}
	b.WriteString(endMarker + eol)
	return b.String()
}

func detectEOL(b []byte) string {
	crlf := bytes.Count(b, []byte("\r\n"))
	lf := bytes.Count(b, []byte("\n")) - crlf
	if crlf > lf {
		return "\r\n"
	}
	return "\n"
}

type markerCount struct {
	begins int
	ends   int
}

// scanMarkers counts marker lines and rejects malformed or misordered ones.
func scanMarkers(s string) (markerCount, error) {
	var c markerCount
	for _, line := range strings.Split(s, "\n") {
		t := strings.TrimSpace(line)
		if !strings.HasPrefix(t, "#") {
			continue
		}
		body := strings.TrimSpace(t[1:])
		switch {
		case hasFoldPrefix(body, beginWords):
			if t != beginMarker {
				return markerCount{}, fmt.Errorf("proxyhost: malformed begin marker line %q", t)
			}
			c.begins++
		case hasFoldPrefix(body, endWords):
			if t != endMarker {
				return markerCount{}, fmt.Errorf("proxyhost: malformed end marker line %q", t)
			}
			if c.begins == 0 {
				return markerCount{}, fmt.Errorf("proxyhost: end marker without preceding begin marker")
			}
			c.ends++
		}
		if c.begins > 1 || c.ends > 1 {
			return markerCount{}, fmt.Errorf("proxyhost: duplicate marker blocks in hosts file")
		}
	}
	if c.begins != c.ends {
		return markerCount{}, fmt.Errorf("proxyhost: unbalanced markers (%d begin, %d end)", c.begins, c.ends)
	}
	return c, nil
}

func hasFoldPrefix(s, prefix string) bool {
	return len(s) >= len(prefix) && strings.EqualFold(s[:len(prefix)], prefix)
}

// parseOwned validates the ledger bytes and returns their leading separator
// and line ending.
func parseOwned(s string) (sep, eol string, err error) {
	switch {
	case strings.HasPrefix(s, "\r\n"):
		sep = "\r\n"
	case strings.HasPrefix(s, "\n"):
		sep = "\n"
	}
	rest := s[len(sep):]
	switch {
	case strings.HasPrefix(rest, beginMarker+"\r\n"):
		eol = "\r\n"
	case strings.HasPrefix(rest, beginMarker+"\n"):
		eol = "\n"
	default:
		return "", "", fmt.Errorf("proxyhost: ledger does not start with the begin marker")
	}
	if sep != "" && sep != eol {
		return "", "", fmt.Errorf("proxyhost: ledger separator and block line endings differ")
	}
	if !strings.HasSuffix(rest, endMarker+eol) || len(rest) < len(beginMarker)+len(endMarker)+2*len(eol) {
		return "", "", fmt.Errorf("proxyhost: ledger does not end with the end marker")
	}
	body := rest[len(beginMarker)+len(eol) : len(rest)-len(endMarker)-len(eol)]
	if body == "" {
		return sep, eol, nil
	}
	if !strings.HasSuffix(body, eol) {
		return "", "", fmt.Errorf("proxyhost: ledger record lines do not use a consistent line ending")
	}
	for _, line := range strings.Split(strings.TrimSuffix(body, eol), eol) {
		if err := checkOwnedRecord(line); err != nil {
			return "", "", err
		}
	}
	return sep, eol, nil
}

func checkOwnedRecord(line string) error {
	fields := strings.Fields(line)
	ok := len(fields) == 2 &&
		(fields[0] == ipv4 || fields[0] == ipv6) &&
		strings.HasSuffix(fields[1], "."+domain) &&
		line == fields[0]+" "+fields[1]
	if !ok {
		return fmt.Errorf("proxyhost: ledger holds unexpected record %q", line)
	}
	return nil
}

// checkConflicts rejects uncommented records outside the owned block that
// already use any desired host name as an IP or alias token.
func checkConflicts(outside string, hosts []string) error {
	want := make(map[string]struct{}, len(hosts))
	for _, h := range hosts {
		want[h] = struct{}{}
	}
	for _, line := range strings.Split(outside, "\n") {
		if i := strings.IndexByte(line, '#'); i >= 0 {
			line = line[:i]
		}
		for _, f := range strings.Fields(line) {
			name := strings.ToLower(strings.TrimSuffix(f, "."))
			if _, ok := want[name]; ok {
				return fmt.Errorf("proxyhost: %q is already defined outside the managed block", name)
			}
		}
	}
	return nil
}

// CheckHostsContent returns the sorted desired host names that lack an
// uncommented 127.0.0.1 record or an uncommented ::1 record in current. It
// reads only the given bytes, so it needs no privileges.
func CheckHostsContent(current []byte, desired []Name) ([]string, error) {
	hosts, err := hostNames(desired)
	if err != nil {
		return nil, err
	}
	have4, have6 := map[string]struct{}{}, map[string]struct{}{}
	for _, line := range strings.Split(string(current), "\n") {
		if i := strings.IndexByte(line, '#'); i >= 0 {
			line = line[:i]
		}
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		ip, err := netip.ParseAddr(fields[0])
		if err != nil {
			continue
		}
		var set map[string]struct{}
		switch ip {
		case netip.MustParseAddr(ipv4):
			set = have4
		case netip.MustParseAddr(ipv6):
			set = have6
		default:
			continue
		}
		for _, f := range fields[1:] {
			set[strings.ToLower(strings.TrimSuffix(f, "."))] = struct{}{}
		}
	}
	missing := []string{}
	for _, h := range hosts {
		_, ok4 := have4[h]
		_, ok6 := have6[h]
		if !ok4 || !ok6 {
			missing = append(missing, h)
		}
	}
	return missing, nil
}
