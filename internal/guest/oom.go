package guest

import (
	"encoding/binary"
	"regexp"
	"strconv"
	"strings"
)

// oom_score_adj values the guest gives processes. The kernel's OOM killer
// adds adj/1000 of the guest memory to a process's usage when it picks a
// victim, so the agent is chosen only when it uses half of the memory more
// than the next candidate, and the processes it starts are chosen first.
const (
	// oomAdjGuest exempts jailoc-guest init and session processes: holders
	// and attached clients, so a session can be reattached after an OOM kill.
	// libkrun's init.krun, which runs jailoc-guest init as its child, powers
	// the VM off when init dies.
	oomAdjGuest = -1000
	// oomAdjAgent is the session program, the coding agent.
	oomAdjAgent = -500
	// oomAdjChild resets processes init and the agent start, which inherit
	// their parent's value on fork.
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
// parent's; for an exec or exit, the process's. exitCode is the wait status
// of an exit.
type procEvent struct {
	what      uint32
	pid       int
	tgid      int
	childPID  int
	childTGID int
	exitCode  uint32
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
		// exec {pid, tgid}, exit {pid, tgid, exit code, exit signal, ...}.
		if len(msg) >= cnMsgLen+24 {
			ev := msg[cnMsgLen:]
			e := procEvent{what: ne.Uint32(ev[0:]), pid: int(ne.Uint32(ev[16:])), tgid: int(ne.Uint32(ev[20:]))}
			switch {
			case e.what == procEventFork && len(ev) >= 32:
				e.childPID, e.childTGID = int(ne.Uint32(ev[24:])), int(ne.Uint32(ev[28:]))
				out = append(out, e)
			case e.what == procEventExit:
				if len(ev) >= 28 {
					e.exitCode = ne.Uint32(ev[24:])
				}
				out = append(out, e)
			case e.what == procEventExec:
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
// snapshot of all processes, for when the guard missed events. init is the
// PID of jailoc-guest init. Processes that still hold a value inherited
// from an agent or init are reset (see inheritedAdj).
func rescanPlan(procs map[int]procInfo, init int) (agents map[int]bool, writes map[int]int) {
	agents, writes = map[int]bool{}, map[int]int{}
	if procs[init].adj != oomAdjGuest {
		writes[init] = oomAdjGuest
	}
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
		if agents[pid] || pid == init || (p.adj != oomAdjAgent && p.adj != oomAdjGuest) {
			continue
		}
		if p.adj == oomAdjGuest && isGuestSession(p) {
			continue
		}
		if inheritedAdj(procs, agents, init, p) {
			writes[pid] = oomAdjChild
		}
	}
	return agents, writes
}

// inheritedAdj reports whether p holds an agent's or init's value it
// inherited on fork. The nearest agent, session process or init among its
// ancestors decides; reaching PID 1 without one means p outlived the
// process it inherited the value from and was reparented. The depth limit
// guards against a ppid cycle in an inconsistent snapshot.
func inheritedAdj(procs map[int]procInfo, agents map[int]bool, init int, p procInfo) bool {
	for anc, depth := p.ppid, 0; anc > 0 && depth < 64; anc, depth = procs[anc].ppid, depth+1 {
		switch {
		case agents[anc]:
			return p.adj == oomAdjAgent
		case anc == init:
			return p.adj == oomAdjGuest
		case isGuestSession(procs[anc]):
			return false
		case anc == 1:
			return true
		}
	}
	return false
}

// oomKill is a kernel OOM kill report.
type oomKill struct {
	pid        int
	comm       string
	anonRSSKiB int
	adj        int
}

var oomKillRe = regexp.MustCompile(`Killed process (\d+) \((.*)\) total-vm:.*anon-rss:(\d+)kB.*oom_score_adj:(-?\d+)`)

// parseOOMKill returns the OOM kill reported by a /dev/kmsg record:
// "prio,seq,usec,flags;message", optionally followed by continuation lines.
func parseOOMKill(record string) (oomKill, bool) {
	_, msg, ok := strings.Cut(record, ";")
	if !ok {
		return oomKill{}, false
	}
	msg, _, _ = strings.Cut(msg, "\n")
	m := oomKillRe.FindStringSubmatch(msg)
	if m == nil {
		return oomKill{}, false
	}
	// The pattern only matches digits.
	pid, _ := strconv.Atoi(m[1])
	rss, _ := strconv.Atoi(m[3])
	adj, _ := strconv.Atoi(m[4])
	return oomKill{pid: pid, comm: m[2], anonRSSKiB: rss, adj: adj}, true
}
