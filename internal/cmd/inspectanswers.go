package cmd

import (
	"net/netip"
	"slices"
	"strconv"
	"strings"
)

// answerItem is one rendered entry of a collapsed DNS answer list, sorted by
// the first address it covers.
type answerItem struct {
	first netip.Addr
	text  string
}

// collapseAnswers shortens the DNS answers of a traffic line without losing
// any address. It sorts and deduplicates the addresses, prints a run of
// consecutive addresses that forms exactly one aligned block as a CIDR prefix
// and folds IPv4 addresses that differ in a single octet into a pattern such
// as 104.16.{0-11}.34. Answers that are not IP addresses follow the
// addresses unchanged, escaped.
func collapseAnswers(answers []string) []string {
	var addrs []netip.Addr
	var other []string
	for _, a := range answers {
		ip, err := netip.ParseAddr(a)
		if err != nil || ip.Zone() != "" {
			other = append(other, escapeVisible(a))
			continue
		}
		addrs = append(addrs, ip)
	}
	slices.SortFunc(addrs, netip.Addr.Compare)
	addrs = slices.Compact(addrs)

	var items []answerItem
	var singles []netip.Addr
	for i := 0; i < len(addrs); {
		run := consecutiveRun(addrs[i:])
		if prefix, ok := alignedBlock(addrs[i], run); ok {
			items = append(items, answerItem{addrs[i], prefix.String()})
		} else {
			singles = append(singles, addrs[i:i+run]...)
		}
		i += run
	}
	items = append(items, octetPatterns(singles)...)
	slices.SortFunc(items, func(a, b answerItem) int { return a.first.Compare(b.first) })

	out := make([]string, 0, len(items)+len(other))
	for _, it := range items {
		out = append(out, it.text)
	}
	return append(out, other...)
}

// consecutiveRun returns how many addresses at the start of the sorted,
// deduplicated addrs follow each other without a gap.
func consecutiveRun(addrs []netip.Addr) int {
	n := 1
	for n < len(addrs) && addrs[n] == addrs[n-1].Next() {
		n++
	}
	return n
}

// alignedBlock reports the prefix covering exactly the n addresses starting
// at first, if n is a power of two of at least 2 and first is aligned to it.
func alignedBlock(first netip.Addr, n int) (netip.Prefix, bool) {
	if n < 2 || n&(n-1) != 0 {
		return netip.Prefix{}, false
	}
	hostBits := 0
	for 1<<hostBits < n {
		hostBits++
	}
	prefix, err := first.Prefix(first.BitLen() - hostBits)
	if err != nil || prefix.Addr() != first {
		return netip.Prefix{}, false
	}
	return prefix, true
}

// octetPatterns folds IPv4 addresses that are equal except for one octet
// into a pattern such as 104.16.{0-11}.34, trying the last octet first.
// IPv6 and remaining IPv4 addresses are returned as they are.
func octetPatterns(addrs []netip.Addr) []answerItem {
	var items []answerItem
	var v4 []netip.Addr
	for _, a := range addrs {
		if a.Is4() {
			v4 = append(v4, a)
		} else {
			items = append(items, answerItem{a, a.String()})
		}
	}
	for pos := 3; pos >= 0; pos-- {
		groups := map[[4]byte][]netip.Addr{}
		for _, a := range v4 {
			groups[octetKey(a, pos)] = append(groups[octetKey(a, pos)], a)
		}
		var rest []netip.Addr
		for _, a := range v4 {
			group := groups[octetKey(a, pos)]
			switch {
			case len(group) < 2:
				rest = append(rest, a)
			case group[0] == a:
				items = append(items, answerItem{a, octetPattern(group, pos)})
			}
		}
		v4 = rest
	}
	for _, a := range v4 {
		items = append(items, answerItem{a, a.String()})
	}
	return items
}

// octetKey returns a with the octet at pos cleared.
func octetKey(a netip.Addr, pos int) [4]byte {
	key := a.As4()
	key[pos] = 0
	return key
}

// octetPattern renders sorted addresses that differ only in the octet at pos,
// with runs of three or more values as a range: 10.0.{1,3,5-9}.1.
func octetPattern(group []netip.Addr, pos int) string {
	first := group[0].As4()
	parts := make([]string, 4)
	for i, b := range first {
		parts[i] = strconv.Itoa(int(b))
	}
	var values []string
	for i := 0; i < len(group); {
		lo := int(group[i].As4()[pos])
		j := i + 1
		for j < len(group) && int(group[j].As4()[pos]) == lo+(j-i) {
			j++
		}
		hi := lo + (j - i) - 1
		switch hi - lo {
		case 0:
			values = append(values, strconv.Itoa(lo))
		case 1:
			values = append(values, strconv.Itoa(lo), strconv.Itoa(hi))
		default:
			values = append(values, strconv.Itoa(lo)+"-"+strconv.Itoa(hi))
		}
		i = j
	}
	parts[pos] = "{" + strings.Join(values, ",") + "}"
	return strings.Join(parts, ".")
}
