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
	// init (1) forked 10, which forked 11, which forked 12, all before the
	// guard handled any event: each inherited init's -1000.
	adjs := map[int]int{1: oomAdjGuest, 10: oomAdjGuest, 11: oomAdjGuest, 12: oomAdjGuest, 20: 0, 21: 0, 30: oomAdjAgent, 31: oomAdjAgent}
	writes := map[int]int{}
	g := &oomGuard{
		logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
		init:   1,
		agents: map[int]bool{30: true},
		readAdj: func(pid int) (int, error) {
			adj, ok := adjs[pid]
			if !ok {
				return 0, os.ErrNotExist
			}
			return adj, nil
		},
		writeAdj: func(pid, adj int) error {
			adjs[pid], writes[pid] = adj, adj
			return nil
		},
	}
	for _, ev := range []procEvent{
		{what: procEventFork, pid: 1, tgid: 1, childPID: 10, childTGID: 10},
		{what: procEventFork, pid: 10, tgid: 10, childPID: 11, childTGID: 11},
		{what: procEventFork, pid: 11, tgid: 11, childPID: 12, childTGID: 12},
		{what: procEventFork, pid: 10, tgid: 10, childPID: 13, childTGID: 10}, // a thread
		{what: procEventFork, pid: 20, tgid: 20, childPID: 21, childTGID: 21}, // kernel default already
		{what: procEventFork, pid: 30, tgid: 30, childPID: 31, childTGID: 31}, // agent child
		{what: procEventFork, pid: 40, tgid: 40, childPID: 41, childTGID: 41}, // exited
	} {
		g.handle(ev)
	}
	want := map[int]int{10: oomAdjChild, 11: oomAdjChild, 12: oomAdjChild, 31: oomAdjChild}
	if !maps.Equal(writes, want) {
		t.Errorf("writes = %v, want %v", writes, want)
	}
}
