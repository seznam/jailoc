package proxyhost

import (
	"bytes"
	"reflect"
	"strings"
	"testing"
)

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
				"127.0.0.1 ws.jailoc.local\n::1 ws.jailoc.local\n" +
				end + "\n",
			wantOwned: begin + "\n" +
				"127.0.0.1 ws.jailoc.local\n::1 ws.jailoc.local\n" +
				end + "\n",
		},
		{
			name:    "trailing newline",
			current: "127.0.0.1 localhost\n",
			desired: []Name{{Workspace: "ws"}},
			wantContent: "127.0.0.1 localhost\n" + begin + "\n" +
				"127.0.0.1 ws.jailoc.local\n::1 ws.jailoc.local\n" +
				end + "\n",
			wantOwned: begin + "\n" +
				"127.0.0.1 ws.jailoc.local\n::1 ws.jailoc.local\n" +
				end + "\n",
		},
		{
			name:    "missing trailing newline owns separator",
			current: "127.0.0.1 localhost",
			desired: []Name{{Workspace: "ws"}},
			wantContent: "127.0.0.1 localhost\n" + begin + "\n" +
				"127.0.0.1 ws.jailoc.local\n::1 ws.jailoc.local\n" +
				end + "\n",
			wantOwned: "\n" + begin + "\n" +
				"127.0.0.1 ws.jailoc.local\n::1 ws.jailoc.local\n" +
				end + "\n",
		},
		{
			name:    "crlf file uses crlf block",
			current: "127.0.0.1 localhost\r\n::1 localhost\r\n",
			desired: []Name{{Workspace: "ws"}},
			wantContent: "127.0.0.1 localhost\r\n::1 localhost\r\n" + begin + "\r\n" +
				"127.0.0.1 ws.jailoc.local\r\n::1 ws.jailoc.local\r\n" +
				end + "\r\n",
			wantOwned: begin + "\r\n" +
				"127.0.0.1 ws.jailoc.local\r\n::1 ws.jailoc.local\r\n" +
				end + "\r\n",
		},
		{
			name:    "crlf file missing trailing newline owns crlf separator",
			current: "127.0.0.1 localhost\r\n::1 localhost",
			desired: []Name{{Workspace: "ws"}},
			wantContent: "127.0.0.1 localhost\r\n::1 localhost\r\n" + begin + "\r\n" +
				"127.0.0.1 ws.jailoc.local\r\n::1 ws.jailoc.local\r\n" +
				end + "\r\n",
			wantOwned: "\r\n" + begin + "\r\n" +
				"127.0.0.1 ws.jailoc.local\r\n::1 ws.jailoc.local\r\n" +
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
				"127.0.0.1 a.jailoc.local\n::1 a.jailoc.local\n" +
				"127.0.0.1 api.a.jailoc.local\n::1 api.a.jailoc.local\n" +
				"127.0.0.1 b.jailoc.local\n::1 b.jailoc.local\n" +
				end + "\n",
			wantOwned: begin + "\n" +
				"127.0.0.1 a.jailoc.local\n::1 a.jailoc.local\n" +
				"127.0.0.1 api.a.jailoc.local\n::1 api.a.jailoc.local\n" +
				"127.0.0.1 b.jailoc.local\n::1 b.jailoc.local\n" +
				end + "\n",
		},
		{
			name:    "route-only names are exact",
			current: "",
			desired: []Name{{Workspace: "ws", Route: "r1"}},
			wantContent: begin + "\n" +
				"127.0.0.1 r1.ws.jailoc.local\n::1 r1.ws.jailoc.local\n" +
				end + "\n",
			wantOwned: begin + "\n" +
				"127.0.0.1 r1.ws.jailoc.local\n::1 r1.ws.jailoc.local\n" +
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
			if !strings.Contains(string(changed.Content), "other.jailoc.local") ||
				strings.Contains(string(changed.Content), "ws.jailoc.local") {
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
				!strings.Contains(string(updated.Content), "two.jailoc.local") {
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
		{"full-line comment", "# 127.0.0.1 ws.jailoc.local\n"},
		{"inline comment", "127.0.0.1 other # ws.jailoc.local\n"},
		{"inline comment without space", "127.0.0.1 other#ws.jailoc.local\n"},
		{"suffix lookalike", "127.0.0.1 ws.jailoc.local.example\n"},
		{"prefix lookalike", "127.0.0.1 xws.jailoc.local\n"},
		{"other workspace", "127.0.0.1 other.jailoc.local\n"},
		{"indented comment", "  \t# ::1 ws.jailoc.local\n"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			plan := mustPlan(t, []byte(tt.current), Ledger{}, []Name{{Workspace: "ws"}})
			if !strings.HasPrefix(string(plan.Content), tt.current) {
				t.Errorf("outside bytes not preserved: %q", plan.Content)
			}
			if !strings.Contains(string(plan.Content), "127.0.0.1 ws.jailoc.local\n") {
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

	block := begin + "\n127.0.0.1 ws.jailoc.local\n::1 ws.jailoc.local\n" + end + "\n"
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
		{name: "begin without end", current: begin + "\n127.0.0.1 ws.jailoc.local\n", ledger: block, desired: valid},
		{name: "end without begin", current: "127.0.0.1 ws.jailoc.local\n" + end + "\n", ledger: block, desired: valid},
		{name: "end before begin", current: end + "\n" + begin + "\n", ledger: block, desired: valid},
		{name: "malformed marker variant", current: "# BEGIN jailoc proxy hosts extra\n", desired: valid},
		{name: "lowercase marker variant", current: "#begin jailoc proxy hosts\n" + end + "\n", desired: valid},
		{
			name:    "marker differs from ledger",
			current: begin + "\n127.0.0.1 ws.jailoc.local\n::1 ws.jailoc.local\n127.0.0.1 extra.jailoc.local\n" + end + "\n",
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
			current: begin + "\r\n127.0.0.1 ws.jailoc.local\n" + end + "\r\n",
			ledger:  begin + "\r\n127.0.0.1 ws.jailoc.local\n" + end + "\r\n",
			desired: valid,
		},
		{
			name:    "ledger holds foreign record",
			current: begin + "\n10.0.0.1 victim\n" + end + "\n",
			ledger:  begin + "\n10.0.0.1 victim\n" + end + "\n",
			desired: valid,
		},

		{name: "outside alias same ip", current: "127.0.0.1 ws.jailoc.local\n", desired: valid},
		{name: "outside alias other ip", current: "10.0.0.1 ws.jailoc.local\n", desired: valid},
		{name: "outside alias ipv6", current: "::1 localhost ws.jailoc.local\n", desired: valid},
		{name: "outside alias tab separated", current: "10.0.0.1\tfoo\tws.jailoc.local\n", desired: valid},
		{name: "outside alias uppercase", current: "10.0.0.1 WS.JAILOC.LOCAL\n", desired: valid},
		{name: "outside alias trailing dot", current: "10.0.0.1 ws.jailoc.local.\n", desired: valid},
		{name: "outside alias crlf", current: "10.0.0.1 ws.jailoc.local\r\n", desired: valid},
		{name: "outside alias no newline", current: "10.0.0.1 ws.jailoc.local", desired: valid},
		{name: "outside alias before inline comment", current: "10.0.0.1 ws.jailoc.local # note\n", desired: valid},
		{name: "outside alias vertical whitespace", current: "10.0.0.1\v\fws.jailoc.local\n", desired: valid},
		{name: "outside route alias", current: "10.0.0.1 api.ws.jailoc.local\n", desired: []Name{{Workspace: "ws", Route: "api"}}},
		{
			name:    "outside alias after owned block",
			current: block + "10.0.0.1 ws.jailoc.local\n",
			ledger:  block,
			desired: valid,
		},
		{
			name:    "outside alias before owned block",
			current: "10.0.0.1 ws.jailoc.local\n" + block,
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

	current := []byte(begin + "\n127.0.0.1 ws.jailoc.local\n::1 ws.jailoc.local\n" + end + "\n")
	if _, err := PlanHosts(current, Ledger{}, nil); err == nil {
		t.Fatal("PlanHosts() error = nil, want refusal to adopt existing marker block")
	}
}

func TestCheckHostsContent(t *testing.T) {
	t.Parallel()

	desired := []Name{{Workspace: "b"}, {Workspace: "a", Route: "api"}, {Workspace: "a"}}
	all := []string{"a.jailoc.local", "api.a.jailoc.local", "b.jailoc.local"}

	tests := []struct {
		name    string
		current string
		desired []Name
		want    []string
	}{
		{name: "empty file", desired: desired, want: all},
		{name: "no desired", current: "127.0.0.1 localhost\n", want: []string{}},
		{
			name:    "all present",
			current: "127.0.0.1 a.jailoc.local api.a.jailoc.local b.jailoc.local\n::1 a.jailoc.local api.a.jailoc.local b.jailoc.local\n",
			desired: desired,
			want:    []string{},
		},
		{
			name:    "managed block counts",
			current: begin + "\n127.0.0.1 a.jailoc.local\n::1 a.jailoc.local\n" + end + "\n",
			desired: []Name{{Workspace: "a"}},
			want:    []string{},
		},
		{
			name:    "ipv4 only",
			current: "127.0.0.1 a.jailoc.local\n",
			desired: []Name{{Workspace: "a"}},
			want:    []string{"a.jailoc.local"},
		},
		{
			name:    "ipv6 only",
			current: "::1 a.jailoc.local\n",
			desired: []Name{{Workspace: "a"}},
			want:    []string{"a.jailoc.local"},
		},
		{
			name:    "records split across lines and ips",
			current: "127.0.0.1 a.jailoc.local\n::1 a.jailoc.local b.jailoc.local\n127.0.0.1 b.jailoc.local\n",
			desired: []Name{{Workspace: "a"}, {Workspace: "b"}, {Workspace: "c"}},
			want:    []string{"c.jailoc.local"},
		},
		{
			name:    "commented records ignored",
			current: "# 127.0.0.1 a.jailoc.local\n127.0.0.1 a.jailoc.local # ::1 a.jailoc.local\n::1 a.jailoc.local\n",
			desired: []Name{{Workspace: "a"}},
			want:    []string{},
		},
		{
			name:    "commented ipv6 record ignored",
			current: "127.0.0.1 a.jailoc.local\n# ::1 a.jailoc.local\n",
			desired: []Name{{Workspace: "a"}},
			want:    []string{"a.jailoc.local"},
		},
		{
			name:    "case trailing dot and crlf tolerated",
			current: "127.0.0.1\tA.JAILOC.LOCAL.\r\n::1 a.jailoc.local\r\n",
			desired: []Name{{Workspace: "a"}},
			want:    []string{},
		},
		{
			name:    "expanded ipv6 literal",
			current: "127.0.0.1 a.jailoc.local\n0:0:0:0:0:0:0:1 a.jailoc.local\n",
			desired: []Name{{Workspace: "a"}},
			want:    []string{},
		},
		{
			name:    "other ip does not count",
			current: "10.0.0.1 a.jailoc.local\n::1 a.jailoc.local\n",
			desired: []Name{{Workspace: "a"}},
			want:    []string{"a.jailoc.local"},
		},
		{
			name:    "host name as first field is not a record",
			current: "a.jailoc.local 127.0.0.1\n",
			desired: []Name{{Workspace: "a"}},
			want:    []string{"a.jailoc.local"},
		},
		{
			name:    "lookalike names do not count",
			current: "127.0.0.1 xa.jailoc.local a.jailoc.local.example\n::1 xa.jailoc.local a.jailoc.local.example\n",
			desired: []Name{{Workspace: "a"}},
			want:    []string{"a.jailoc.local"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, err := CheckHostsContent([]byte(tt.current), tt.desired)
			if err != nil {
				t.Fatalf("CheckHostsContent() error = %v", err)
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("CheckHostsContent() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestCheckHostsContentInvalidName(t *testing.T) {
	t.Parallel()

	got, err := CheckHostsContent(nil, []Name{{Workspace: "ok"}, {Workspace: "BAD"}})
	if err == nil {
		t.Fatalf("CheckHostsContent() = %q, want error", got)
	}
	if got != nil {
		t.Errorf("CheckHostsContent() = %q on error, want nil", got)
	}
}

func TestCheckHostsContentAgreesWithPlanHosts(t *testing.T) {
	t.Parallel()

	desired := []Name{{Workspace: "ws"}, {Workspace: "ws", Route: "api"}}
	plan := mustPlan(t, []byte("127.0.0.1 localhost\n"), Ledger{}, desired)
	got, err := CheckHostsContent(plan.Content, desired)
	if err != nil {
		t.Fatalf("CheckHostsContent() error = %v", err)
	}
	if len(got) != 0 {
		t.Errorf("CheckHostsContent(planned content) = %q, want none missing", got)
	}
}
