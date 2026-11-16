package netbroker

import (
	"fmt"
	"net/url"
	"strings"
)

// HTTPMatch narrows a rule to HTTP requests: the rule then applies per
// request, to the listed methods and path patterns, instead of to the whole
// connection. Methods and Paths left empty match any method or path.
type HTTPMatch struct {
	Methods []string `json:"methods,omitempty"`
	Paths   []string `json:"paths,omitempty"`

	anyMethod bool
	methods   map[string]struct{}
	paths     []pathPattern
}

func (m *HTTPMatch) compile(kind, rule string) error {
	m.anyMethod = len(m.Methods) == 0
	m.methods = make(map[string]struct{}, len(m.Methods))
	for _, s := range m.Methods {
		if s == "*" {
			m.anyMethod = true
			continue
		}
		if !isHTTPToken(s) {
			return fmt.Errorf("%s rule %q: invalid http method %q", kind, rule, s)
		}
		m.methods[strings.ToUpper(s)] = struct{}{}
	}
	m.paths = m.paths[:0]
	for _, s := range m.Paths {
		pp, err := compilePathPattern(s)
		if err != nil {
			return fmt.Errorf("%s rule %q: http path %q: %w", kind, rule, s, err)
		}
		m.paths = append(m.paths, pp)
	}
	return nil
}

// matches reports whether a request with method and decoded path segments
// is covered. Allow rules compare the method as sent; deny rules fold its case
// so a lowercase method cannot slip past a deny rule to a lenient upstream.
func (m *HTTPMatch) matches(method string, segs []string, foldMethod bool) bool {
	if !m.anyMethod {
		if foldMethod {
			method = strings.ToUpper(method)
		}
		if _, ok := m.methods[method]; !ok {
			return false
		}
	}
	if len(m.paths) == 0 {
		return true
	}
	for _, pp := range m.paths {
		if pp.match(segs) {
			return true
		}
	}
	return false
}

// pathSeg is one segment of a path pattern: a literal, or "*" for exactly one
// non-empty segment.
type pathSeg struct {
	lit  string
	star bool
}

// pathPattern is a compiled path pattern. rest is set when it ends in "**",
// which matches zero or more remaining segments.
type pathPattern struct {
	segs []pathSeg
	rest bool
}

// compilePathPattern parses a path pattern: it starts with "/", "*" is one
// whole non-empty segment, "**" is zero or more remaining segments and only
// valid last. A trailing "/" is a trailing empty segment matched literally.
func compilePathPattern(s string) (pathPattern, error) {
	var pp pathPattern
	rest, ok := strings.CutPrefix(s, "/")
	if !ok {
		return pp, fmt.Errorf("must start with /")
	}
	for i := 0; i < len(s); i++ {
		switch c := s[i]; {
		case c < 0x20 || c == 0x7f:
			return pp, fmt.Errorf("control character")
		case c == '?' || c == '#' || c == '%' || c == '\\':
			return pp, fmt.Errorf("character %q is not allowed", c)
		}
	}
	parts := strings.Split(rest, "/")
	for i, seg := range parts {
		last := i == len(parts)-1
		switch {
		case seg == "**":
			if !last {
				return pp, fmt.Errorf("** is only allowed as the last segment")
			}
			pp.rest = true
		case seg == "*":
			pp.segs = append(pp.segs, pathSeg{star: true})
		case strings.Contains(seg, "*"):
			return pp, fmt.Errorf("partial wildcard in segment %q", seg)
		case seg == "." || seg == "..":
			return pp, fmt.Errorf("%q segment", seg)
		case seg == "" && !last:
			return pp, fmt.Errorf("empty segment")
		default:
			pp.segs = append(pp.segs, pathSeg{lit: seg})
		}
	}
	return pp, nil
}

// match reports whether the decoded request path segments fit the pattern.
func (pp pathPattern) match(segs []string) bool {
	if len(segs) < len(pp.segs) || (!pp.rest && len(segs) != len(pp.segs)) {
		return false
	}
	for i, ps := range pp.segs {
		switch {
		case ps.star:
			if segs[i] == "" {
				return false
			}
		case ps.lit != segs[i]:
			return false
		}
	}
	return true
}

// canonicalPath splits an escaped request path into decoded segments. It
// reports false for a path the proxy refuses to match, because two parsers
// could read it differently: no leading "/", "//", a backslash, an encoded
// slash, backslash or NUL, a segment that does not decode, a "." or ".."
// segment, or a control character in a segment.
func canonicalPath(escaped string) ([]string, bool) {
	rest, ok := strings.CutPrefix(escaped, "/")
	if !ok || strings.Contains(escaped, "//") || strings.Contains(escaped, "\\") {
		return nil, false
	}
	lower := strings.ToLower(escaped)
	if strings.Contains(lower, "%2f") || strings.Contains(lower, "%5c") || strings.Contains(lower, "%00") {
		return nil, false
	}
	segs := strings.Split(rest, "/")
	for i, raw := range segs {
		seg, err := url.PathUnescape(raw)
		if err != nil || seg == "." || seg == ".." {
			return nil, false
		}
		for j := 0; j < len(seg); j++ {
			if seg[j] < 0x20 || seg[j] == 0x7f {
				return nil, false
			}
		}
		segs[i] = seg
	}
	return segs, true
}
