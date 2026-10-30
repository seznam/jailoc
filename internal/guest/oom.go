package guest

import "encoding/binary"

// oom_score_adj values the guest gives processes. The kernel's OOM killer
// adds adj/1000 of the guest memory to a process's usage when it picks a
// victim, so the agent is chosen only when it uses half of the memory more
// than the next candidate, and the processes it starts are chosen first.
const (
	// oomAdjGuest exempts jailoc-guest session processes: holders and
	// attached clients, so a session can be reattached after an OOM kill.
	oomAdjGuest = -1000
	// oomAdjAgent is the session program, the coding agent.
	oomAdjAgent = -500
	// oomAdjChild resets processes the agent starts, which inherit the
	// agent's value on fork.
	oomAdjChild = 0
)

// Proc connector event types (linux/cn_proc.h) and message sizes.
const (
	procEventFork = 0x00000001
	procEventExec = 0x00000002
	procEventExit = 0x80000000
	nlmsgHdrLen   = 16
	nlmsgAlignTo  = 4
	cnMsgLen      = 20 // struct cn_msg without data
)

// procEvent is a proc connector event. For a fork, pid and tgid are the
// parent's; for an exec or exit, the process's.
type procEvent struct {
	what      uint32
	pid       int
	tgid      int
	childPID  int
	childTGID int
}

// parseProcEvents returns the proc connector events in a netlink datagram.
func parseProcEvents(b []byte) []procEvent {
	var out []procEvent
	ne := binary.NativeEndian
	for len(b) >= nlmsgHdrLen {
		l := int(ne.Uint32(b[0:]))
		if l < nlmsgHdrLen || l > len(b) {
			break
		}
		msg := b[nlmsgHdrLen:l]
		// proc_event: what u32, cpu u32, timestamp u64, then the union:
		// fork {parent pid, parent tgid, child pid, child tgid},
		// exec and exit {pid, tgid, ...}.
		if len(msg) >= cnMsgLen+24 {
			ev := msg[cnMsgLen:]
			e := procEvent{what: ne.Uint32(ev[0:]), pid: int(ne.Uint32(ev[16:])), tgid: int(ne.Uint32(ev[20:]))}
			switch {
			case e.what == procEventFork && len(ev) >= 32:
				e.childPID, e.childTGID = int(ne.Uint32(ev[24:])), int(ne.Uint32(ev[28:]))
				out = append(out, e)
			case e.what == procEventExec, e.what == procEventExit:
				out = append(out, e)
			}
		}
		b = b[(l+nlmsgAlignTo-1)&^(nlmsgAlignTo-1):]
	}
	return out
}

// procInfo is what the OOM guard reads about a process from /proc.
type procInfo struct {
	ppid int
	exe  string
	argv []string
	adj  int
}

type oomRole int

const (
	roleOther oomRole = iota
	roleGuest
	roleAgent
)

func isGuestSession(p procInfo) bool {
	return p.exe == BinaryPath && len(p.argv) > 1 && p.argv[1] == "session"
}

func isHolder(p procInfo) bool {
	return isGuestSession(p) && len(p.argv) > 2 && p.argv[2] == "serve"
}

// classify returns the role of process p with parent parent: jailoc-guest
// session processes are guest processes, and the program a session holder
// starts is the agent.
func classify(p, parent procInfo) oomRole {
	switch {
	case isGuestSession(p):
		return roleGuest
	case isHolder(parent):
		return roleAgent
	}
	return roleOther
}

// rescanPlan recomputes the agents and the oom_score_adj writes from a
// snapshot of all processes, for when the guard missed events. Descendants
// of agents that still hold the agent's value are reset.
func rescanPlan(procs map[int]procInfo) (agents map[int]bool, writes map[int]int) {
	agents, writes = map[int]bool{}, map[int]int{}
	for pid, p := range procs {
		switch classify(p, procs[p.ppid]) {
		case roleGuest:
			if p.adj != oomAdjGuest {
				writes[pid] = oomAdjGuest
			}
		case roleAgent:
			agents[pid] = true
			if p.adj != oomAdjAgent {
				writes[pid] = oomAdjAgent
			}
		}
	}
	for pid, p := range procs {
		if agents[pid] || p.adj != oomAdjAgent {
			continue
		}
		// The depth limit guards against a ppid cycle in an inconsistent
		// snapshot.
		for anc, depth := p.ppid, 0; anc > 1 && depth < 64; anc, depth = procs[anc].ppid, depth+1 {
			if agents[anc] {
				writes[pid] = oomAdjChild
				break
			}
		}
	}
	return agents, writes
}
