// Command jailoc-vmprobe runs the executable acceptance probes for VM
// isolation backends. See docs/research/vm-acceptance-spec.md.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/seznam/jailoc/internal/vmprobe"
)

type multi []string

func (m *multi) String() string     { return strings.Join(*m, ",") }
func (m *multi) Set(v string) error { *m = append(*m, v); return nil }

const usage = `usage: jailoc-vmprobe <command> [flags]

commands (spec ` + vmprobe.SpecVersion + `):
  plan      host: create fixtures and write plan.json
  sentinel  host or LAN machine: log TCP/UDP hits on the plan ports
  selftest  host: prove every sentinel answers before guest deny checks count
  guest     guest/container: run network, DNS, interface, vsock and filesystem checks
  watch     guest: probe deny and public targets in a loop (fail-closed evidence)
  ingress   host: check a published port answers on loopback only
  verdict   host: merge reports, sentinel logs and fixture checks; exit 1 on FAIL
`

func main() {
	os.Exit(run())
}

func run() int {
	if len(os.Args) < 2 {
		fmt.Fprint(os.Stderr, usage)
		return 2
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	var err error
	switch os.Args[1] {
	case "plan":
		err = cmdPlan(os.Args[2:])
	case "sentinel":
		err = cmdSentinel(ctx, os.Args[2:])
	case "selftest":
		err = cmdSelfTest(ctx, os.Args[2:])
	case "guest":
		err = cmdGuest(ctx, os.Args[2:])
	case "watch":
		err = cmdWatch(ctx, os.Args[2:])
	case "ingress":
		err = cmdIngress(ctx, os.Args[2:])
	case "verdict":
		err = cmdVerdict(os.Args[2:])
	case "-h", "--help", "help":
		fmt.Print(usage)
		return 0
	default:
		fmt.Fprint(os.Stderr, usage)
		return 2
	}
	var failed errFailed
	switch {
	case errors.As(err, &failed):
		return 1
	case err != nil:
		fmt.Fprintln(os.Stderr, "jailoc-vmprobe:", err)
		return 2
	}
	return 0
}

type errFailed struct{}

func (errFailed) Error() string { return "acceptance failed" }

func parseException(s string) (vmprobe.Exception, error) {
	parts := strings.Split(s, ",")
	if len(parts) < 2 {
		return vmprobe.Exception{}, fmt.Errorf("exception %q: want cidr,proto[,port...]", s)
	}
	e := vmprobe.Exception{CIDR: parts[0], Proto: parts[1]}
	for _, ps := range parts[2:] {
		port, err := strconv.Atoi(ps)
		if err != nil {
			return vmprobe.Exception{}, fmt.Errorf("exception %q port: %w", s, err)
		}
		e.Ports = append(e.Ports, port)
	}
	return e, e.Validate()
}

func parseExport(s string) (vmprobe.Export, error) {
	parts := strings.Split(s, ":")
	if len(parts) != 3 || (parts[2] != vmprobe.ModeRW && parts[2] != vmprobe.ModeRO) {
		return vmprobe.Export{}, fmt.Errorf("export %q: want host:guest:rw|ro", s)
	}
	return vmprobe.Export{HostPath: parts[0], GuestPath: parts[1], Mode: parts[2]}, nil
}

func cmdPlan(args []string) error {
	fs := flag.NewFlagSet("plan", flag.ExitOnError)
	opts := vmprobe.PlanOptions{}
	var lan, peers, resolvers, excs, exports, sockets multi
	out := fs.String("out", "plan.json", "plan output path")
	fs.StringVar(&opts.Candidate, "candidate", "", "candidate name (required)")
	fs.StringVar(&opts.Dir, "dir", "", "fixture directory to create (required)")
	fs.StringVar(&opts.GuestPrefix, "guest-prefix", "", "guest path replacing -dir when the candidate cannot mount at the host path")
	fs.IntVar(&opts.PrimaryPort, "port", 47801, "primary sentinel port")
	fs.IntVar(&opts.AltPort, "alt-port", 47802, "alternate sentinel port")
	fs.StringVar(&opts.IPv6Mode, "ipv6", "", "candidate IPv6 mode: enforced or disabled (required)")
	fs.StringVar(&opts.VsockPorts, "vsock-ports", "1-8192", "host vsock ports to scan from the guest")
	fs.Var(&lan, "lan-sentinel", "address of a sentinel on another LAN machine (repeatable)")
	fs.Var(&peers, "peer", "address of a peer workspace running a sentinel (repeatable)")
	fs.Var(&resolvers, "host-resolver", "host resolver address (repeatable, default: /etc/resolv.conf)")
	fs.Var(&excs, "exception", "policy exception cidr,proto[,port...] configured on the candidate (repeatable)")
	fs.Var(&exports, "export", "additional declared share host:guest:rw|ro (repeatable)")
	fs.Var(&sockets, "allowed-socket", "guest path of an intentionally shared socket (repeatable)")
	_ = fs.Parse(args)
	if opts.Candidate == "" || opts.Dir == "" {
		return errors.New("plan: -candidate and -dir are required")
	}
	opts.LANSentinels, opts.PeerAddrs, opts.HostResolvers, opts.AllowedSockets = lan, peers, resolvers, sockets
	for _, s := range excs {
		e, err := parseException(s)
		if err != nil {
			return err
		}
		opts.Exceptions = append(opts.Exceptions, e)
	}
	for _, s := range exports {
		e, err := parseExport(s)
		if err != nil {
			return err
		}
		opts.ExtraExports = append(opts.ExtraExports, e)
	}
	p, err := vmprobe.NewPlan(opts)
	if err != nil {
		return fmt.Errorf("plan: %w", err)
	}
	return vmprobe.WriteJSON(*out, p)
}

func cmdSentinel(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("sentinel", flag.ExitOnError)
	planPath := fs.String("plan", "", "plan file (provides ports)")
	name := fs.String("name", "", "sentinel name reported in banners and logs")
	logPath := fs.String("log", "-", "JSONL hit log (appended), - for stdout")
	var ports multi
	fs.Var(&ports, "port", "port to listen on (repeatable; default: plan ports)")
	_ = fs.Parse(args)
	s := &vmprobe.Sentinel{Name: *name}
	if s.Name == "" {
		s.Name, _ = os.Hostname()
	}
	for _, ps := range ports {
		port, err := strconv.Atoi(ps)
		if err != nil {
			return fmt.Errorf("sentinel port %q: %w", ps, err)
		}
		s.Ports = append(s.Ports, port)
	}
	if len(s.Ports) == 0 {
		if *planPath == "" {
			return errors.New("sentinel: -plan or -port is required")
		}
		p, err := vmprobe.LoadPlan(*planPath)
		if err != nil {
			return err
		}
		s.Ports = []int{p.PrimaryPort, p.AltPort}
	}
	var w io.Writer = os.Stdout
	if *logPath != "-" {
		f, err := os.OpenFile(*logPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644) //nolint:gosec // G302,G304: operator-chosen log path
		if err != nil {
			return fmt.Errorf("open log: %w", err)
		}
		defer func() { _ = f.Close() }()
		w = f
	}
	s.Log = w
	fmt.Fprintf(os.Stderr, "sentinel %s listening on tcp+udp ports %v\n", s.Name, s.Ports)
	return s.Run(ctx)
}

func cmdSelfTest(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("selftest", flag.ExitOnError)
	planPath := fs.String("plan", "plan.json", "plan file")
	timeout := fs.Duration("timeout", 2*time.Second, "per-probe timeout")
	out := fs.String("out", "-", "report output path")
	_ = fs.Parse(args)
	p, err := vmprobe.LoadPlan(*planPath)
	if err != nil {
		return err
	}
	return vmprobe.WriteJSON(*out, vmprobe.RunSelfTest(ctx, p, *timeout))
}

func cmdGuest(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("guest", flag.ExitOnError)
	planPath := fs.String("plan", "plan.json", "plan file")
	ctxName := fs.String("context", "", "execution context: guest-root, guest-user, container-bridge, container-hostnet-privileged (required)")
	checks := fs.String("checks", "", "comma-separated subset of net,dns,iface,vsock,fs (default: all for guest-root, net,dns otherwise)")
	timeout := fs.Duration("timeout", vmprobe.DefaultTimeout, "per-probe timeout")
	par := fs.Int("parallel", 16, "concurrent probes")
	out := fs.String("out", "-", "report output path")
	_ = fs.Parse(args)
	if *ctxName == "" {
		return errors.New("guest: -context is required")
	}
	p, err := vmprobe.LoadPlan(*planPath)
	if err != nil {
		return err
	}
	opts := vmprobe.GuestOptions{Context: *ctxName, Timeout: *timeout, Parallelism: *par}
	switch {
	case *checks != "":
		opts.Checks = strings.Split(*checks, ",")
	case *ctxName != vmprobe.CtxGuestRoot:
		opts.Checks = []string{"net", "dns"}
	}
	return vmprobe.WriteJSON(*out, vmprobe.RunGuest(ctx, p, opts))
}

func cmdWatch(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("watch", flag.ExitOnError)
	planPath := fs.String("plan", "plan.json", "plan file")
	ctxName := fs.String("context", vmprobe.CtxGuestRoot, "execution context label")
	interval := fs.Duration("interval", time.Second, "tick interval")
	duration := fs.Duration("duration", time.Minute, "total duration")
	timeout := fs.Duration("timeout", 2*time.Second, "per-probe timeout")
	out := fs.String("out", "-", "report output path")
	timeline := fs.String("timeline", "", "JSONL timeline path (default: stderr)")
	_ = fs.Parse(args)
	p, err := vmprobe.LoadPlan(*planPath)
	if err != nil {
		return err
	}
	var w io.Writer = os.Stderr
	if *timeline != "" {
		f, err := os.Create(*timeline) //nolint:gosec // G304: operator-chosen path
		if err != nil {
			return fmt.Errorf("create timeline: %w", err)
		}
		defer func() { _ = f.Close() }()
		w = f
	}
	return vmprobe.WriteJSON(*out, vmprobe.RunWatch(ctx, p, *ctxName, *interval, *duration, *timeout, w))
}

func cmdIngress(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("ingress", flag.ExitOnError)
	planPath := fs.String("plan", "plan.json", "plan file")
	port := fs.Int("port", 0, "published workspace port (required)")
	timeout := fs.Duration("timeout", 2*time.Second, "dial timeout")
	out := fs.String("out", "-", "report output path")
	_ = fs.Parse(args)
	if *port == 0 {
		return errors.New("ingress: -port is required")
	}
	p, err := vmprobe.LoadPlan(*planPath)
	if err != nil {
		return err
	}
	return vmprobe.WriteJSON(*out, vmprobe.RunIngress(ctx, p, *port, *timeout))
}

func cmdVerdict(args []string) error {
	fs := flag.NewFlagSet("verdict", flag.ExitOnError)
	planPath := fs.String("plan", "plan.json", "plan file")
	out := fs.String("out", "-", "markdown output path")
	var reports, hitLogs multi
	fs.Var(&reports, "report", "probe report JSON (repeatable)")
	fs.Var(&hitLogs, "hits", "sentinel JSONL log (repeatable)")
	_ = fs.Parse(args)
	reports = append(reports, fs.Args()...)
	p, err := vmprobe.LoadPlan(*planPath)
	if err != nil {
		return err
	}
	var reps []*vmprobe.Report
	for _, path := range reports {
		var r vmprobe.Report
		if err := vmprobe.ReadJSON(path, &r); err != nil {
			return err
		}
		reps = append(reps, &r)
	}
	var hits []vmprobe.Hit
	for _, path := range hitLogs {
		f, err := os.Open(path) //nolint:gosec // G304: operator-supplied log path
		if err != nil {
			return fmt.Errorf("open hits: %w", err)
		}
		h, err := vmprobe.ReadHits(f)
		_ = f.Close()
		if err != nil {
			return fmt.Errorf("%s: %w", path, err)
		}
		hits = append(hits, h...)
	}
	v, err := vmprobe.BuildVerdict(p, reps, hits)
	if err != nil {
		return err
	}
	md := v.Markdown()
	if *out == "-" {
		fmt.Print(md)
	} else if err := os.WriteFile(*out, []byte(md), 0o644); err != nil { //nolint:gosec // G306: report is not secret
		return fmt.Errorf("write verdict: %w", err)
	}
	if v.Failed() {
		return errFailed{}
	}
	return nil
}
