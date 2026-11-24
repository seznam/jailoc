package proxyhost

import (
	"bytes"
	"fmt"
	"sort"
	"strings"
	"testing"
)

const (
	beginMarker = "# BEGIN jailoc proxy hosts"
	endMarker   = "# END jailoc proxy hosts"
	beginWords  = "BEGIN jailoc proxy hosts"
	endWords    = "END jailoc proxy hosts"
	domain      = "jl.local"
	ipv4        = "127.0.0.1"
	ipv6        = "::1"
	maxLabel    = 63
)

// Name is one proxy host name request. Workspace yields ws.jl.local;
// a non-empty Route yields route.ws.jl.local.
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

const (
	begin = "# BEGIN jailoc proxy hosts"
	end   = "# END jailoc proxy hosts"
)

func mustPlan(t *testing.T, current []byte, ledger Ledger, desired []Name) Plan {
	t.Helper()
	plan, err := PlanHosts(current, ledger, desired)
	if err != nil {
		t.Fatalf("PlanHosts() error = %v", err)
	}
	return plan
}

func TestPlanHostsInsert(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		current     string
		desired     []Name
		wantContent string
		wantOwned   string
	}{
		{
			name:    "empty file",
			desired: []Name{{Workspace: "ws"}},
			wantContent: begin + "\n" +
				"127.0.0.1 ws.jl.local\n::1 ws.jl.local\n" +
				end + "\n",
			wantOwned: begin + "\n" +
				"127.0.0.1 ws.jl.local\n::1 ws.jl.local\n" +
				end + "\n",
		},
		{
			name:    "trailing newline",
			current: "127.0.0.1 localhost\n",
			desired: []Name{{Workspace: "ws"}},
			wantContent: "127.0.0.1 localhost\n" + begin + "\n" +
				"127.0.0.1 ws.jl.local\n::1 ws.jl.local\n" +
				end + "\n",
			wantOwned: begin + "\n" +
				"127.0.0.1 ws.jl.local\n::1 ws.jl.local\n" +
				end + "\n",
		},
		{
			name:    "missing trailing newline owns separator",
			current: "127.0.0.1 localhost",
			desired: []Name{{Workspace: "ws"}},
			wantContent: "127.0.0.1 localhost\n" + begin + "\n" +
				"127.0.0.1 ws.jl.local\n::1 ws.jl.local\n" +
				end + "\n",
			wantOwned: "\n" + begin + "\n" +
				"127.0.0.1 ws.jl.local\n::1 ws.jl.local\n" +
				end + "\n",
		},
		{
			name:    "crlf file uses crlf block",
			current: "127.0.0.1 localhost\r\n::1 localhost\r\n",
			desired: []Name{{Workspace: "ws"}},
			wantContent: "127.0.0.1 localhost\r\n::1 localhost\r\n" + begin + "\r\n" +
				"127.0.0.1 ws.jl.local\r\n::1 ws.jl.local\r\n" +
				end + "\r\n",
			wantOwned: begin + "\r\n" +
				"127.0.0.1 ws.jl.local\r\n::1 ws.jl.local\r\n" +
				end + "\r\n",
		},
		{
			name:    "crlf file missing trailing newline owns crlf separator",
			current: "127.0.0.1 localhost\r\n::1 localhost",
			desired: []Name{{Workspace: "ws"}},
			wantContent: "127.0.0.1 localhost\r\n::1 localhost\r\n" + begin + "\r\n" +
				"127.0.0.1 ws.jl.local\r\n::1 ws.jl.local\r\n" +
				end + "\r\n",
			wantOwned: "\r\n" + begin + "\r\n" +
				"127.0.0.1 ws.jl.local\r\n::1 ws.jl.local\r\n" +
				end + "\r\n",
		},
		{
			name:    "sorted deduplicated names with routes",
			current: "",
			desired: []Name{
				{Workspace: "b"},
				{Workspace: "a", Route: "api"},
				{Workspace: "a"},
				{Workspace: "b"},
				{Workspace: "a", Route: "api"},
			},
			wantContent: begin + "\n" +
				"127.0.0.1 a.jl.local\n::1 a.jl.local\n" +
				"127.0.0.1 api.a.jl.local\n::1 api.a.jl.local\n" +
				"127.0.0.1 b.jl.local\n::1 b.jl.local\n" +
				end + "\n",
			wantOwned: begin + "\n" +
				"127.0.0.1 a.jl.local\n::1 a.jl.local\n" +
				"127.0.0.1 api.a.jl.local\n::1 api.a.jl.local\n" +
				"127.0.0.1 b.jl.local\n::1 b.jl.local\n" +
				end + "\n",
		},
		{
			name:    "route-only names are exact",
			current: "",
			desired: []Name{{Workspace: "ws", Route: "r1"}},
			wantContent: begin + "\n" +
				"127.0.0.1 r1.ws.jl.local\n::1 r1.ws.jl.local\n" +
				end + "\n",
			wantOwned: begin + "\n" +
				"127.0.0.1 r1.ws.jl.local\n::1 r1.ws.jl.local\n" +
				end + "\n",
		},
		{
			name:        "empty desired and no ledger leaves bytes",
			current:     "127.0.0.1 localhost",
			desired:     nil,
			wantContent: "127.0.0.1 localhost",
			wantOwned:   "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			plan := mustPlan(t, []byte(tt.current), Ledger{}, tt.desired)
			if got := string(plan.Content); got != tt.wantContent {
				t.Errorf("Content = %q, want %q", got, tt.wantContent)
			}
			if got := string(plan.Ledger.Owned); got != tt.wantOwned {
				t.Errorf("Ledger.Owned = %q, want %q", got, tt.wantOwned)
			}
		})
	}
}

func TestPlanHostsRoundTrip(t *testing.T) {
	t.Parallel()

	originals := []struct {
		name string
		data string
	}{
		{"empty", ""},
		{"only newline", "\n"},
		{"trailing newline", "127.0.0.1 localhost\n"},
		{"no trailing newline", "127.0.0.1 localhost"},
		{"crlf", "127.0.0.1 localhost\r\n"},
		{"crlf no trailing newline", "127.0.0.1 localhost\r\n::1 localhost"},
		{"lone cr tail", "127.0.0.1 localhost\r"},
		{"comment without newline", "# comment"},
	}

	for _, tt := range originals {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			original := []byte(tt.data)
			desired := []Name{{Workspace: "ws"}, {Workspace: "ws", Route: "api"}}

			added := mustPlan(t, original, Ledger{}, desired)
			if bytes.Equal(added.Content, original) {
				t.Fatalf("add left content unchanged: %q", added.Content)
			}

			again := mustPlan(t, added.Content, added.Ledger, desired)
			if !bytes.Equal(again.Content, added.Content) {
				t.Errorf("idempotent Content = %q, want %q", again.Content, added.Content)
			}
			if !bytes.Equal(again.Ledger.Owned, added.Ledger.Owned) {
				t.Errorf("idempotent Ledger = %q, want %q", again.Ledger.Owned, added.Ledger.Owned)
			}

			changed := mustPlan(t, added.Content, added.Ledger, []Name{{Workspace: "other"}})
			if !strings.Contains(string(changed.Content), "other.jl.local") ||
				strings.Contains(string(changed.Content), "ws.jl.local") {
				t.Errorf("update Content = %q", changed.Content)
			}

			removed := mustPlan(t, changed.Content, changed.Ledger, nil)
			if !bytes.Equal(removed.Content, original) {
				t.Errorf("removed Content = %q, want %q", removed.Content, original)
			}
			if len(removed.Ledger.Owned) != 0 {
				t.Errorf("removed Ledger = %q, want empty", removed.Ledger.Owned)
			}
		})
	}
}

func TestPlanHostsPreservesUnrelatedEdits(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		original string
		prepend  string
		append   string
		want     string
	}{
		{
			name:     "host appended after block",
			original: "127.0.0.1 localhost\n",
			append:   "10.0.0.1 other\n",
			want:     "127.0.0.1 localhost\n10.0.0.1 other\n",
		},
		{
			name:     "host prepended before block",
			original: "127.0.0.1 localhost\n",
			prepend:  "10.0.0.2 first\n",
			want:     "10.0.0.2 first\n127.0.0.1 localhost\n",
		},
		{
			name:     "append after block when separator was owned keeps lines apart",
			original: "127.0.0.1 localhost",
			append:   "10.0.0.1 other\n",
			want:     "127.0.0.1 localhost\n10.0.0.1 other\n",
		},
		{
			name:     "crlf appended",
			original: "127.0.0.1 localhost\r\n",
			append:   "10.0.0.1 other\r\n",
			want:     "127.0.0.1 localhost\r\n10.0.0.1 other\r\n",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			added := mustPlan(t, []byte(tt.original), Ledger{}, []Name{{Workspace: "ws"}})
			edited := []byte(tt.prepend + string(added.Content) + tt.append)

			updated := mustPlan(t, edited, added.Ledger, []Name{{Workspace: "ws"}, {Workspace: "two"}})
			if !strings.HasPrefix(string(updated.Content), tt.prepend) ||
				!strings.HasSuffix(string(updated.Content), tt.append) ||
				!strings.Contains(string(updated.Content), "two.jl.local") {
				t.Fatalf("update Content = %q", updated.Content)
			}

			removed := mustPlan(t, updated.Content, updated.Ledger, nil)
			if string(removed.Content) != tt.want {
				t.Errorf("removed Content = %q, want %q", removed.Content, tt.want)
			}
			if len(removed.Ledger.Owned) != 0 {
				t.Errorf("removed Ledger = %q, want empty", removed.Ledger.Owned)
			}
		})
	}
}

func TestPlanHostsOutsideCommentsAndLookalikesAreIgnored(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		current string
	}{
		{"full-line comment", "# 127.0.0.1 ws.jl.local\n"},
		{"inline comment", "127.0.0.1 other # ws.jl.local\n"},
		{"inline comment without space", "127.0.0.1 other#ws.jl.local\n"},
		{"suffix lookalike", "127.0.0.1 ws.jl.local.example\n"},
		{"prefix lookalike", "127.0.0.1 xws.jl.local\n"},
		{"other workspace", "127.0.0.1 other.jl.local\n"},
		{"indented comment", "  \t# ::1 ws.jl.local\n"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			plan := mustPlan(t, []byte(tt.current), Ledger{}, []Name{{Workspace: "ws"}})
			if !strings.HasPrefix(string(plan.Content), tt.current) {
				t.Errorf("outside bytes not preserved: %q", plan.Content)
			}
			if !strings.Contains(string(plan.Content), "127.0.0.1 ws.jl.local\n") {
				t.Errorf("record missing: %q", plan.Content)
			}
		})
	}
}

func TestPlanHostsDoesNotAliasInputs(t *testing.T) {
	t.Parallel()

	current := []byte("127.0.0.1 localhost\n")
	snapshot := append([]byte(nil), current...)
	noop := mustPlan(t, current, Ledger{}, nil)
	added := mustPlan(t, current, Ledger{}, []Name{{Workspace: "ws"}})

	noop.Content[0] = 'X'
	added.Content[0] = 'Y'
	if !bytes.Equal(current, snapshot) {
		t.Errorf("input mutated: %q", current)
	}

	ownedSnapshot := append([]byte(nil), added.Ledger.Owned...)
	removed := mustPlan(t, added.Content, added.Ledger, nil)
	removed.Content[0] = 'Z'
	if !bytes.Equal(added.Ledger.Owned, ownedSnapshot) {
		t.Errorf("ledger mutated: %q", added.Ledger.Owned)
	}
}

func TestPlanHostsErrors(t *testing.T) {
	t.Parallel()

	block := begin + "\n127.0.0.1 ws.jl.local\n::1 ws.jl.local\n" + end + "\n"
	valid := []Name{{Workspace: "ws"}}

	tests := []struct {
		name    string
		current string
		ledger  string
		desired []Name
	}{
		{name: "empty workspace", desired: []Name{{}}},
		{name: "uppercase workspace", desired: []Name{{Workspace: "Ws"}}},
		{name: "dot in workspace", desired: []Name{{Workspace: "a.b"}}},
		{name: "underscore workspace", desired: []Name{{Workspace: "a_b"}}},
		{name: "leading hyphen workspace", desired: []Name{{Workspace: "-ws"}}},
		{name: "trailing hyphen workspace", desired: []Name{{Workspace: "ws-"}}},
		{name: "64 char workspace", desired: []Name{{Workspace: strings.Repeat("a", 64)}}},
		{name: "newline workspace", desired: []Name{{Workspace: "ws\n127.0.0.1 evil"}}},
		{name: "tab workspace", desired: []Name{{Workspace: "ws\tevil"}}},
		{name: "space workspace", desired: []Name{{Workspace: "ws evil"}}},
		{name: "non-ascii workspace", desired: []Name{{Workspace: "wš"}}},
		{name: "uppercase route", desired: []Name{{Workspace: "ws", Route: "Api"}}},
		{name: "dotted route", desired: []Name{{Workspace: "ws", Route: "a.b"}}},
		{name: "hyphen edge route", desired: []Name{{Workspace: "ws", Route: "api-"}}},
		{name: "64 char route", desired: []Name{{Workspace: "ws", Route: strings.Repeat("a", 64)}}},
		{name: "newline route", desired: []Name{{Workspace: "ws", Route: "a\nb"}}},
		{name: "one invalid among valid", desired: []Name{{Workspace: "ok"}, {Workspace: "BAD"}}},

		{name: "marker with empty ledger", current: block, desired: valid},
		{name: "marker with empty ledger and empty desired", current: block},
		{name: "empty marker block with empty ledger", current: begin + "\n" + end + "\n", desired: valid},
		{name: "duplicate blocks", current: block + block, ledger: block, desired: valid},
		{name: "duplicate begin", current: begin + "\n" + block, ledger: block, desired: valid},
		{name: "begin without end", current: begin + "\n127.0.0.1 ws.jl.local\n", ledger: block, desired: valid},
		{name: "end without begin", current: "127.0.0.1 ws.jl.local\n" + end + "\n", ledger: block, desired: valid},
		{name: "end before begin", current: end + "\n" + begin + "\n", ledger: block, desired: valid},
		{name: "malformed marker variant", current: "# BEGIN jailoc proxy hosts extra\n", desired: valid},
		{name: "lowercase marker variant", current: "#begin jailoc proxy hosts\n" + end + "\n", desired: valid},
		{
			name:    "marker differs from ledger",
			current: begin + "\n127.0.0.1 ws.jl.local\n::1 ws.jl.local\n127.0.0.1 extra.jl.local\n" + end + "\n",
			ledger:  block,
			desired: valid,
		},
		{
			name:    "ledger bytes absent from file",
			current: "127.0.0.1 localhost\n",
			ledger:  block,
			desired: valid,
		},
		{
			name:    "ledger bytes absent with empty desired",
			current: "127.0.0.1 localhost\n",
			ledger:  block,
		},
		{
			name:    "separator in ledger but not in file",
			current: block,
			ledger:  "\n" + block,
			desired: valid,
		},
		{
			name:    "ledger not at line start",
			current: "x" + block,
			ledger:  block,
			desired: valid,
		},
		{name: "malformed ledger", current: block, ledger: "junk\n", desired: valid},
		{name: "ledger missing end", current: begin + "\n", ledger: begin + "\n", desired: valid},
		{
			name:    "ledger mixed line endings",
			current: begin + "\r\n127.0.0.1 ws.jl.local\n" + end + "\r\n",
			ledger:  begin + "\r\n127.0.0.1 ws.jl.local\n" + end + "\r\n",
			desired: valid,
		},
		{
			name:    "ledger holds foreign record",
			current: begin + "\n10.0.0.1 victim\n" + end + "\n",
			ledger:  begin + "\n10.0.0.1 victim\n" + end + "\n",
			desired: valid,
		},

		{name: "outside alias same ip", current: "127.0.0.1 ws.jl.local\n", desired: valid},
		{name: "outside alias other ip", current: "10.0.0.1 ws.jl.local\n", desired: valid},
		{name: "outside alias ipv6", current: "::1 localhost ws.jl.local\n", desired: valid},
		{name: "outside alias tab separated", current: "10.0.0.1\tfoo\tws.jl.local\n", desired: valid},
		{name: "outside alias uppercase", current: "10.0.0.1 WS.JL.LOCAL\n", desired: valid},
		{name: "outside alias trailing dot", current: "10.0.0.1 ws.jl.local.\n", desired: valid},
		{name: "outside alias crlf", current: "10.0.0.1 ws.jl.local\r\n", desired: valid},
		{name: "outside alias no newline", current: "10.0.0.1 ws.jl.local", desired: valid},
		{name: "outside alias before inline comment", current: "10.0.0.1 ws.jl.local # note\n", desired: valid},
		{name: "outside alias vertical whitespace", current: "10.0.0.1\v\fws.jl.local\n", desired: valid},
		{name: "outside route alias", current: "10.0.0.1 api.ws.jl.local\n", desired: []Name{{Workspace: "ws", Route: "api"}}},
		{
			name:    "outside alias after owned block",
			current: block + "10.0.0.1 ws.jl.local\n",
			ledger:  block,
			desired: valid,
		},
		{
			name:    "outside alias before owned block",
			current: "10.0.0.1 ws.jl.local\n" + block,
			ledger:  block,
			desired: valid,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			plan, err := PlanHosts([]byte(tt.current), Ledger{Owned: []byte(tt.ledger)}, tt.desired)
			if err == nil {
				t.Fatalf("PlanHosts() = %q, want error", plan.Content)
			}
			if !strings.HasPrefix(err.Error(), "proxyhost: ") {
				t.Errorf("error %q lacks context prefix", err)
			}
			if plan.Content != nil || plan.Ledger.Owned != nil {
				t.Errorf("plan on error = %+v, want zero", plan)
			}
		})
	}
}

func TestPlanHostsEmptyDesiredWithMarkersFailsBeforeAdopt(t *testing.T) {
	t.Parallel()

	current := []byte(begin + "\n127.0.0.1 ws.jl.local\n::1 ws.jl.local\n" + end + "\n")
	if _, err := PlanHosts(current, Ledger{}, nil); err == nil {
		t.Fatal("PlanHosts() error = nil, want refusal to adopt existing marker block")
	}
}
