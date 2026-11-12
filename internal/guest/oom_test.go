package guest

import (
	"encoding/binary"
	"maps"
	"testing"
)

// procMessage builds a netlink datagram with one proc connector event
// carrying the given union fields.
func procMessage(what uint32, fields ...uint32) []byte {
	ne := binary.NativeEndian
	total := nlmsgHdrLen + cnMsgLen + 16 + 4*len(fields)
	b := make([]byte, (total+nlmsgAlignTo-1)&^(nlmsgAlignTo-1))
	ne.PutUint32(b[0:], uint32(total)) //nolint:gosec // G115: small test size
	ev := b[nlmsgHdrLen+cnMsgLen:]
	ne.PutUint32(ev[0:], what)
	for i, f := range fields {
		ne.PutUint32(ev[16+4*i:], f)
	}
	return b
}

func TestParseProcEvents(t *testing.T) {
	t.Parallel()
	var b []byte
	b = append(b, procMessage(procEventFork, 10, 10, 11, 11)...)
	b = append(b, procMessage(procEventExec, 11, 11)...)
	b = append(b, procMessage(0x00000004, 11, 11, 0, 0)...) // uid change: skipped
	b = append(b, procMessage(procEventExit, 12, 11, 2<<8, 17)...)
	b = append(b, 1, 2, 3) // truncated trailer
	got := parseProcEvents(b)
	want := []procEvent{
		{what: procEventFork, pid: 10, tgid: 10, childPID: 11, childTGID: 11},
		{what: procEventExec, pid: 11, tgid: 11},
		{what: procEventExit, pid: 12, tgid: 11, exitCode: 2 << 8},
	}
	if len(got) != len(want) {
		t.Fatalf("events = %+v, want %+v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("event %d = %+v, want %+v", i, got[i], want[i])
		}
	}
}

func TestClassify(t *testing.T) {
	t.Parallel()
	holder := procInfo{exe: BinaryPath, argv: []string{BinaryPath, "session", "serve", "abc"}}
	tests := []struct {
		name         string
		proc, parent procInfo
		want         oomRole
	}{
		{"holder", holder, procInfo{exe: BinaryPath, argv: []string{"jailoc-guest", "session", "open"}}, roleGuest},
		{"attach client", procInfo{exe: BinaryPath, argv: []string{"jailoc-guest", "session", "open", "--", "opencode"}}, procInfo{}, roleGuest},
		{"session launcher", procInfo{exe: BinaryPath, argv: []string{BinaryPath, "run", "--", "opencode"}}, holder, roleAgent},
		{"agent after exec", procInfo{exe: "/usr/bin/node", argv: []string{"opencode"}}, holder, roleAgent},
		{"exec outside a session", procInfo{exe: BinaryPath, argv: []string{BinaryPath, "run", "--", "sh"}}, procInfo{exe: "/bin/sh"}, roleOther},
		{"other binary named like a holder", procInfo{exe: "/bin/sh"}, procInfo{exe: "/tmp/fake", argv: []string{BinaryPath, "session", "serve", "x"}}, roleOther},
		{"agent child", procInfo{exe: "/usr/bin/make"}, procInfo{exe: "/usr/bin/node"}, roleOther},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := classify(tt.proc, tt.parent); got != tt.want {
				t.Errorf("classify = %d, want %d", got, tt.want)
			}
		})
	}
}

func TestRescanPlan(t *testing.T) {
	t.Parallel()
	// PID 1 is libkrun's init.krun, which runs jailoc-guest init (2) and
	// adopts orphans, including session holders.
	procs := map[int]procInfo{
		1:  {exe: "/init.krun"},
		2:  {ppid: 1, exe: BinaryPath},
		20: {ppid: 1, exe: BinaryPath, argv: []string{BinaryPath, "session", "serve", "a"}, adj: oomAdjGuest},
		21: {ppid: 20, exe: "/usr/bin/node", argv: []string{"opencode"}, adj: oomAdjAgent},
		22: {ppid: 21, exe: "/usr/bin/make", adj: oomAdjAgent},                              // missed fork
		23: {ppid: 22, exe: "/usr/bin/cc", adj: oomAdjAgent},                                // its child
		24: {ppid: 21, exe: "/usr/bin/go", adj: oomAdjChild},                                // already reset
		30: {ppid: 1, exe: BinaryPath, argv: []string{BinaryPath, "session", "serve", "b"}}, // missed exec
		31: {ppid: 30, exe: "/usr/bin/node", argv: []string{"claude"}},
		40: {ppid: 1, exe: "/usr/bin/gopls", adj: oomAdjAgent},      // outlived its agent
		50: {ppid: 2, exe: "/bin/sh", adj: oomAdjGuest},             // init's missed fork
		51: {ppid: 50, exe: "/usr/bin/git", adj: oomAdjGuest},       // its child
		52: {ppid: 2, exe: "/usr/bin/dockerd", adj: oomAdjChild},    // already reset
		53: {ppid: 1, exe: "/usr/bin/containerd", adj: oomAdjGuest}, // outlived init's child
		54: {ppid: 53, exe: "/usr/bin/runc", adj: oomAdjGuest},      // its child
		60: {ppid: 21, exe: "/usr/bin/sudo", adj: oomAdjGuest},      // lowered in a session
		70: {ppid: 1, exe: BinaryPath, argv: []string{BinaryPath, "session", "open"}, adj: oomAdjGuest},
	}
	agents, writes := rescanPlan(procs, 2)
	if want := map[int]bool{21: true, 31: true}; !maps.Equal(agents, want) {
		t.Errorf("agents = %v, want %v", agents, want)
	}
	want := map[int]int{
		2: oomAdjGuest, 22: oomAdjChild, 23: oomAdjChild, 30: oomAdjGuest, 31: oomAdjAgent,
		40: oomAdjChild, 50: oomAdjChild, 51: oomAdjChild, 53: oomAdjChild, 54: oomAdjChild,
	}
	if !maps.Equal(writes, want) {
		t.Errorf("writes = %v, want %v", writes, want)
	}
}

func TestParseOOMKill(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		record string
		want   oomKill
		ok     bool
	}{
		{
			"kill",
			"3,1234,5678901,-;Out of memory: Killed process 271 (gopls) total-vm:2846932kB, anon-rss:1572864kB, file-rss:0kB, shmem-rss:0kB, UID:1000 pgtables:3420kB oom_score_adj:0\n",
			oomKill{pid: 271, comm: "gopls", anonRSSKiB: 1572864, adj: 0},
			true,
		},
		{
			"memory cgroup kill with continuation",
			"3,9,10,-;Memory cgroup out of memory: Killed process 42 (node (v8)) total-vm:10kB, anon-rss:2048kB, file-rss:0kB, shmem-rss:0kB, UID:1000 pgtables:8kB oom_score_adj:-500\n SUBSYSTEM=memory\n",
			oomKill{pid: 42, comm: "node (v8)", anonRSSKiB: 2048, adj: -500},
			true,
		},
		{"invocation", "4,1,2,-;gopls invoked oom-killer: gfp_mask=0x140cca(GFP_HIGHUSER_MOVABLE|__GFP_COMP), order=0, oom_score_adj=0\n", oomKill{}, false},
		{"other message", "6,2,3,-;virtio_net virtio0 eth0: link up\n", oomKill{}, false},
		{"no header", "Out of memory: Killed process 1 (x) total-vm:1kB, anon-rss:1kB oom_score_adj:0", oomKill{}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, ok := parseOOMKill(tt.record)
			if ok != tt.ok || got != tt.want {
				t.Errorf("parseOOMKill = %+v, %v, want %+v, %v", got, ok, tt.want, tt.ok)
			}
		})
	}
}
