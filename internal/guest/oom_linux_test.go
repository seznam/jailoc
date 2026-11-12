package guest

import (
	"io"
	"log/slog"
	"maps"
	"os"
	"testing"
)

func TestOOMGuardForkResetsInheritedExemption(t *testing.T) {
	t.Parallel()
	initProc := procInfo{ppid: 1, exe: BinaryPath, argv: []string{BinaryPath}, adj: oomAdjGuest}
	session := func(ppid int, sub string) procInfo {
		return procInfo{ppid: ppid, exe: BinaryPath, argv: []string{BinaryPath, "session", sub}, adj: oomAdjGuest}
	}
	// init (2) forked 10, which forked 11, which forked 12, all before the
	// guard handled any event: each inherited init's -1000. The session
	// processes hold -1000 after their exec: 70, forked by 71, and 80, forked
	// by init, regained it in a rescan while their fork events were queued.
	procs := map[int]procInfo{
		2:  initProc,
		10: {ppid: 2, adj: oomAdjGuest},
		11: {ppid: 10, adj: oomAdjGuest},
		12: {ppid: 11, adj: oomAdjGuest},
		20: {ppid: 2}, 21: {ppid: 20},
		30: {ppid: 70, adj: oomAdjAgent}, 31: {ppid: 30, adj: oomAdjAgent},
		50: session(2, "open"),
		70: session(71, "serve"), 71: session(2, "open"),
		80: session(2, "open"),
	}
	writes := map[int]int{}
	g := &oomGuard{
		logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
		init:   2,
		agents: map[int]bool{30: true},
		readProc: func(pid int) (procInfo, error) {
			p, ok := procs[pid]
			if !ok {
				return procInfo{}, os.ErrNotExist
			}
			return p, nil
		},
		writeAdj: func(pid, adj int) error {
			p := procs[pid]
			p.adj, writes[pid] = adj, adj
			procs[pid] = p
			return nil
		},
	}
	// The kernel reports a new thread with its process's parent as the
	// forking process.
	for _, ev := range []procEvent{
		{what: procEventFork, pid: 2, tgid: 2, childPID: 10, childTGID: 10},
		{what: procEventFork, pid: 10, tgid: 10, childPID: 11, childTGID: 11},
		{what: procEventFork, pid: 11, tgid: 11, childPID: 12, childTGID: 12},
		{what: procEventFork, pid: 1, tgid: 1, childPID: 3, childTGID: 2},     // a thread of init
		{what: procEventFork, pid: 2, tgid: 2, childPID: 51, childTGID: 50},   // a thread of 50
		{what: procEventFork, pid: 20, tgid: 20, childPID: 21, childTGID: 21}, // kernel default already
		{what: procEventFork, pid: 30, tgid: 30, childPID: 31, childTGID: 31}, // agent child
		{what: procEventFork, pid: 40, tgid: 40, childPID: 41, childTGID: 41}, // exited
		{what: procEventFork, pid: 71, tgid: 71, childPID: 70, childTGID: 70}, // holder restored by rescan
		{what: procEventFork, pid: 2, tgid: 2, childPID: 80, childTGID: 80},   // session restored by rescan
	} {
		g.handle(ev)
	}
	want := map[int]int{10: oomAdjChild, 11: oomAdjChild, 12: oomAdjChild, 31: oomAdjChild}
	if !maps.Equal(writes, want) {
		t.Errorf("writes = %v, want %v", writes, want)
	}
}
