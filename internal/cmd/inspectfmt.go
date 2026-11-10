package cmd

import (
	"encoding/json"
	"fmt"
	"net"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/fatih/color"
	"github.com/seznam/jailoc/internal/guest"
)

// trafficEvent is one line of the network broker's event log. Unknown
// fields are ignored, so events added later (for example decrypted HTTP
// requests) still parse.
type trafficEvent struct {
	Time          time.Time `json:"time"`
	Event         string    `json:"event"`
	Conn          uint64    `json:"conn"`
	Proto         string    `json:"proto"`
	Src           string    `json:"src"`
	Dst           string    `json:"dst"`
	Allow         bool      `json:"allow"`
	Reason        string    `json:"reason"`
	Rule          string    `json:"rule"`
	Name          string    `json:"name"`
	Type          string    `json:"type"`
	Answers       []string  `json:"answers"`
	Blocked       bool      `json:"blocked"`
	Error         string    `json:"error"`
	Host          string    `json:"host"`
	GuestPort     int       `json:"guest_port"`
	Network       string    `json:"network"`
	Target        string    `json:"target"`
	SNI           string    `json:"sni"`
	ALPN          []string  `json:"alpn"`
	Method        string    `json:"method"`
	Path          string    `json:"path"`
	Status        int       `json:"status"`
	PolicyError   string    `json:"policy_error"`
	RuntimeErrors []string  `json:"runtime_errors"`
	DenyAll       bool      `json:"deny_all"`
}

// trafficFormatter renders broker events as aligned, human-readable lines.
// It remembers DNS answers so connections show the name the guest looked
// up instead of only the address.
type trafficFormatter struct {
	names map[string]string // IP → last DNS name that resolved to it
	all   bool              // also show DHCP, NIC and service events
}

func newTrafficFormatter(all bool) *trafficFormatter {
	return &trafficFormatter{names: map[string]string{}, all: all}
}

var (
	colAllow = color.New(color.FgGreen, color.Bold)
	colDeny  = color.New(color.FgRed, color.Bold)
	colInfo  = color.New(color.FgCyan)
	colDim   = color.New(color.Faint)
)

// line parses one log line, updates the DNS name map and returns the
// rendered text, or "" when the event is hidden.
func (f *trafficFormatter) line(raw []byte) string {
	var e trafficEvent
	if err := json.Unmarshal(raw, &e); err != nil || e.Event == "" {
		return ""
	}
	if e.Event == "dns" {
		for _, a := range e.Answers {
			f.names[a] = strings.TrimSuffix(e.Name, ".")
		}
	}
	return f.render(e)
}

func (f *trafficFormatter) render(e trafficEvent) string {
	ts := colDim.Sprint(e.Time.Local().Format("15:04:05"))
	switch e.Event {
	case "flow":
		verdict := colAllow.Sprint("ALLOW")
		label := e.Reason
		if e.Rule != "" {
			label = "rule=" + e.Rule
		}
		detail := ""
		if !e.Allow {
			verdict = colDeny.Sprint("DENY ")
			detail = colDeny.Sprint(escapeVisible(label))
		} else if label != "public" {
			detail = colDim.Sprint(escapeVisible(label))
		}
		return fmt.Sprintf("%s  %s  %-4s %-48s %s", ts, verdict, escapeVisible(e.Proto), f.destWith(e.Dst, e.Name), detail)
	case "l7":
		var parts []string
		switch e.Proto {
		case "tls":
			if e.SNI != "" {
				parts = append(parts, escapeVisible(e.SNI))
			}
			if len(e.ALPN) > 0 {
				parts = append(parts, escapeVisible(strings.Join(e.ALPN, ",")))
			}
		default:
			if e.Method != "" {
				parts = append(parts, escapeVisible(e.Method)+" "+escapeVisible(e.Host)+escapeVisible(e.Path))
			}
			if e.Status != 0 {
				parts = append(parts, strconv.Itoa(e.Status))
			}
		}
		return fmt.Sprintf("%s  %s %-4s %s", ts, colDim.Sprint("      └"), escapeVisible(e.Proto), colInfo.Sprint(strings.Join(parts, "  ")))
	case "dns":
		name := escapeVisible(strings.TrimSuffix(e.Name, "."))
		switch {
		case e.Blocked:
			why := "filtered_dns"
			if e.Rule != "" {
				why = "rule=" + escapeVisible(e.Rule)
			}
			return fmt.Sprintf("%s  %s  dns  %s %s", ts, colDeny.Sprint("BLOCK"), name, colDeny.Sprint("→ NXDOMAIN ("+why+")"))
		case e.Error != "":
			return fmt.Sprintf("%s  %s  dns  %s %s %s", ts, colInfo.Sprint("DNS  "), name, escapeVisible(e.Type), colDeny.Sprint("→ "+escapeVisible(e.Error)))
		case len(e.Answers) == 0:
			return fmt.Sprintf("%s  %s  dns  %s %s %s", ts, colInfo.Sprint("DNS  "), name, escapeVisible(e.Type), colDim.Sprint("→ no records"))
		default:
			answers := make([]string, len(e.Answers))
			for i, answer := range e.Answers {
				answers[i] = escapeVisible(answer)
			}
			return fmt.Sprintf("%s  %s  dns  %s %s → %s", ts, colInfo.Sprint("DNS  "), name, escapeVisible(e.Type), strings.Join(answers, ", "))
		}
	case "dial-error":
		return fmt.Sprintf("%s  %s  %-4s %-48s %s", ts, colDeny.Sprint("FAIL "), escapeVisible(e.Proto), f.dest(e.Dst), colDeny.Sprint(escapeVisible(e.Error)))
	case "flow-limit":
		msg := colDeny.Sprint("too many open connections")
		if e.Dst == "" {
			return fmt.Sprintf("%s  %s  %-4s gateway port %d %s", ts, colDeny.Sprint("LIMIT"), escapeVisible(e.Proto), e.GuestPort, msg)
		}
		return fmt.Sprintf("%s  %s  %-4s %-48s %s", ts, colDeny.Sprint("LIMIT"), escapeVisible(e.Proto), f.dest(e.Dst), msg)
	case "ingress":
		if e.GuestPort == guest.SSHPort && !f.all {
			return "" // jailoc's own SSH sessions
		}
		return fmt.Sprintf("%s  %s  tcp  %s → guest port %d", ts, colInfo.Sprint("IN   "), escapeVisible(e.Host), e.GuestPort)
	case "service":
		if (e.GuestPort == guest.SSHAgentPort || e.GuestPort == guest.GPGAgentPort) && !f.all {
			return "" // SSH and GPG agent forwarding
		}
		return fmt.Sprintf("%s  %s  %-4s gateway port %d%s", ts, colInfo.Sprint("HOST "), escapeVisible(e.Network), e.GuestPort, serviceTarget(e))
	case "service-error":
		return fmt.Sprintf("%s  %s  gateway port %d%s: %s", ts, colDeny.Sprint("FAIL "), e.GuestPort, serviceTarget(e), escapeVisible(e.Error))
	case "reload":
		msg := "configuration reloaded"
		if e.PolicyError != "" {
			msg += colDeny.Sprint("; invalid policy, all new connections denied: " + escapeVisible(e.PolicyError))
		}
		for _, r := range e.RuntimeErrors {
			msg += colDeny.Sprint("; " + escapeVisible(r))
		}
		return fmt.Sprintf("%s  %s  %s", ts, colInfo.Sprint("---  "), msg)
	case "policy":
		if e.DenyAll {
			return fmt.Sprintf("%s  %s  %s", ts, colDeny.Sprint("---  "), "all new connections denied")
		}
		return ""
	}
	if f.all {
		data, _ := json.Marshal(e)
		return fmt.Sprintf("%s  %s  %s %s", ts, colDim.Sprint("     "), escapeVisible(e.Event), colDim.Sprint(escapeVisible(string(data))))
	}
	return ""
}

// serviceTarget renders the host target of a gateway service event, or ""
// for events from brokers that did not record it.
func serviceTarget(e trafficEvent) string {
	if e.Target == "" {
		return ""
	}
	return " → " + escapeVisible(e.Target)
}

// dest renders an address with the DNS name it was looked up by.
func (f *trafficFormatter) dest(addr string) string {
	return f.destWith(addr, "")
}

func (f *trafficFormatter) destWith(addr, eventName string) string {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return escapeVisible(addr)
	}
	name := strings.TrimSuffix(eventName, ".")
	if name == "" {
		name = f.names[host]
	}
	if name != "" {
		return escapeVisible(net.JoinHostPort(name, port)) + " " + colDim.Sprint("["+escapeVisible(host)+"]")
	}
	return escapeVisible(addr)
}

// execFormatter renders exec log records. It remembers the command of each
// running process to label its exit.
type execFormatter struct {
	all   bool // also show jailoc's own guest processes and exits with status 0
	procs map[int]execProc
}

type execProc struct {
	cmd    string
	hidden bool
}

func newExecFormatter(all bool) *execFormatter {
	return &execFormatter{all: all, procs: map[int]execProc{}}
}

func (f *execFormatter) line(raw []byte) string {
	var e guest.ExecRecord
	if err := json.Unmarshal(raw, &e); err != nil || e.PID == 0 {
		return ""
	}
	switch e.Event {
	case "":
		return f.start(e)
	case "exit":
		return f.exit(e)
	}
	return ""
}

func (f *execFormatter) start(e guest.ExecRecord) string {
	cmd := escapeVisible(shellQuote(e.Argv))
	name := cmd
	if cmd == "" {
		name = escapeVisible(e.Comm)
		cmd = colDim.Sprint("[" + name + ", exited]")
	}
	hidden := !f.all && isGuestProcess(e)
	f.procs[e.PID] = execProc{cmd: name, hidden: hidden}
	if hidden {
		return ""
	}
	user := strconv.Itoa(e.UID)
	switch e.UID {
	case 0:
		user = colDeny.Sprint("root")
	case 1000:
		user = "agent"
	case -1:
		user = "?"
	}
	cwd := e.Cwd
	if rest, ok := strings.CutPrefix(cwd, "/home/agent"); ok {
		cwd = "~" + rest
	}
	return fmt.Sprintf("%s  %7d %s  %-5s %s  %s",
		colDim.Sprint(e.Time.Local().Format("15:04:05")), e.PID, colDim.Sprintf("←%-7d", e.PPID), user, colInfo.Sprint(escapeVisible(cwd)), cmd)
}

// exit renders a process exit in the parent PID column: failures always,
// status 0 only with --all.
func (f *execFormatter) exit(e guest.ExecRecord) string {
	p := f.procs[e.PID]
	delete(f.procs, e.PID)
	status, col := "exit "+strconv.Itoa(e.Code), colDeny
	switch {
	case e.Signal != "":
		status = escapeVisible(e.Signal)
	case e.Code == 0:
		if !f.all {
			return ""
		}
		col = colDim
	}
	if p.hidden {
		return ""
	}
	return fmt.Sprintf("%s  %7d %s  %s",
		colDim.Sprint(e.Time.Local().Format("15:04:05")), e.PID, col.Sprintf("%-8s", status), colDim.Sprint(p.cmd))
}

func escapeVisible(s string) string {
	var out strings.Builder
	for len(s) > 0 {
		r, size := utf8.DecodeRuneInString(s)
		switch {
		case r == utf8.RuneError && size == 1:
			fmt.Fprintf(&out, `\x%02x`, s[0])
		case !unicode.IsPrint(r):
			quoted := strconv.QuoteRune(r)
			out.WriteString(quoted[1 : len(quoted)-1])
		default:
			out.WriteString(s[:size])
		}
		s = s[size:]
	}
	return out.String()
}

// escapeLines is escapeVisible for multi-line text: it keeps the line
// breaks and escapes every other control character.
func escapeLines(s string) string {
	lines := strings.Split(s, "\n")
	for i, l := range lines {
		lines[i] = escapeVisible(l)
	}
	return strings.Join(lines, "\n")
}

// isGuestProcess reports jailoc's own guest processes: jailoc-guest and
// the shell the SSH server starts it with.
func isGuestProcess(e guest.ExecRecord) bool {
	if e.Exe == guest.BinaryPath {
		return true
	}
	return len(e.Argv) == 3 && e.Argv[1] == "-c" && strings.HasPrefix(e.Argv[2], "'"+guest.BinaryPath+"'")
}

// shellQuote joins argv, quoting only the arguments that need it.
func shellQuote(argv []string) string {
	out := make([]string, len(argv))
	for i, a := range argv {
		if a != "" && !strings.ContainsAny(a, " \t\n'\"\\$`;&|<>()*?[]{}!#~") {
			out[i] = a
			continue
		}
		out[i] = "'" + strings.ReplaceAll(a, "'", `'\''`) + "'"
	}
	return strings.Join(out, " ")
}
