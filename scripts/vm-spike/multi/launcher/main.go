// Command launcher boots one go-microvm workspace VM behind a supervised
// jailoc-netbroker (multi-workspace spike). The broker must report ready
// before the VM boots, the VM's NIC must connect within 30 s, and when either
// side ends the other is stopped.
//
// Docker state lives on a virtio-blk data disk (go-microvm WithDisks), the
// guest init script replaces the image entrypoint, and the workspace shares
// (fx/rw, fx/ro, jlp) come from -shares.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/seznam/jailoc/internal/brokerproc"
	microvm "github.com/stacklok/go-microvm"
	"github.com/stacklok/go-microvm/hypervisor/libkrun"
	"github.com/stacklok/go-microvm/image"
	propnet "github.com/stacklok/go-microvm/net"
)

// brokerProvider hands libkrun the socket of the supervised broker.
type brokerProvider struct{ path string }

func (p brokerProvider) Start(context.Context, propnet.Config) error {
	if _, err := os.Stat(p.path); err != nil {
		return fmt.Errorf("broker socket: %w", err)
	}
	return nil
}
func (p brokerProvider) SocketPath() string { return p.path }
func (brokerProvider) Stop()                {}

// vmHandle adapts a go-microvm VM to brokerproc.VM.
type vmHandle struct{ vm *microvm.VM }

func (h vmHandle) Alive() bool {
	info, err := h.vm.Status(context.Background())
	return err == nil && info.Active
}
func (h vmHandle) Stop(ctx context.Context) error { return h.vm.Stop(ctx) }

type multi []string

func (m *multi) String() string     { return fmt.Sprint(*m) }
func (m *multi) Set(s string) error { *m = append(*m, s); return nil }

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "launcher:", err)
		os.Exit(1)
	}
}

func run() error {
	name := flag.String("name", "", "workspace name")
	img := flag.String("image", "docker:29-dind", "guest image")
	mem := flag.Uint("mem", 2048, "guest memory MiB")
	cpus := flag.Uint("cpus", 2, "guest vCPUs")
	state := flag.String("state", "", "workspace state dir (VM data dir, broker socket and log, data disk)")
	cache := flag.String("cache", "", "shared OCI image cache dir")
	shares := flag.String("shares", "", "dir containing fx/rw, fx/ro and jlp")
	initPath := flag.String("init", "", "guest init script, run as /bin/sh /spike-init.sh")
	initCmd := flag.String("init-cmd", "", "guest init command instead of -init (space-separated, e.g. /lib/systemd/systemd)")
	rootfs := flag.String("rootfs", "", "boot this prepared rootfs dir instead of pulling -image (changes persist in it)")
	runner := flag.String("runner", "", "go-microvm-runner binary")
	libDir := flag.String("libdir", "/opt/homebrew/lib", "libkrun library dir")
	broker := flag.String("broker", "", "jailoc-netbroker binary")
	policy := flag.String("policy", "", "broker policy file")
	subnet := flag.String("subnet", "192.168.127.0/24", "guest network; gateway .1, guest .2")
	diskSize := flag.Int64("disk-size", 10<<30, "data disk size in bytes (sparse)")
	var forwards multi
	flag.Var(&forwards, "forward", "broker ingress forward 127.0.0.1:HOSTPORT=GUESTPORT (repeatable)")
	flag.Parse()
	if *name == "" || *state == "" || *shares == "" || (*initPath == "" && *initCmd == "") || *runner == "" || *broker == "" || *policy == "" {
		return fmt.Errorf("-name, -state, -shares, -init or -init-cmd, -runner, -broker and -policy are required")
	}
	pfx, gw, guest, err := addrs(*subnet)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(*state, 0o750); err != nil {
		return fmt.Errorf("state dir: %w", err)
	}
	disk := filepath.Join(*state, "docker.img")
	if err := ensureDisk(disk, *diskSize); err != nil {
		return err
	}
	var initScript []byte
	if *initPath != "" {
		if initScript, err = os.ReadFile(*initPath); err != nil {
			return fmt.Errorf("read init: %w", err)
		}
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// Unix socket paths are limited to ~104 bytes on macOS, so the socket
	// lives in the system temp dir.
	sock := filepath.Join(os.TempDir(), "jlp-nb-"+*name+".sock")
	args := []string{"-socket", sock, "-policy", *policy, "-subnet", pfx, "-gateway", gw, "-guest", guest,
		"-log", filepath.Join(*state, "broker.jsonl")}
	for _, f := range forwards {
		args = append(args, "-forward", f)
	}
	t0 := time.Now()
	bp, err := brokerproc.Start(ctx, brokerproc.Options{Binary: *broker, Args: args, ReadyTimeout: 5 * time.Second, Stderr: os.Stderr})
	if err != nil {
		event("broker-failed", err.Error())
		return err
	}
	event("broker-ready", fmt.Sprintf("pid=%d after=%s", bp.PID(), time.Since(t0).Round(time.Millisecond)))

	opts := []microvm.Option{
		microvm.WithName("jailoc-" + *name),
		microvm.WithCPUs(uint32(*cpus)),  //nolint:gosec // G115: small flag value
		microvm.WithMemory(uint32(*mem)), //nolint:gosec // G115: small flag value
		microvm.WithTmpSize(512),
		microvm.WithDataDir(filepath.Join(*state, "vm")),
		microvm.WithCleanDataDir(),
		microvm.WithBackend(libkrun.NewBackend(libkrun.WithRunnerPath(*runner), libkrun.WithLibDir(*libDir))),
		microvm.WithNetProvider(brokerProvider{path: sock}),
		microvm.WithDisks(microvm.Disk{ID: "docker", HostPath: disk}),
		microvm.WithVirtioFS(
			microvm.VirtioFSMount{Tag: "fx-rw", HostPath: filepath.Join(*shares, "fx", "rw")},
			microvm.VirtioFSMount{Tag: "fx-ro", HostPath: filepath.Join(*shares, "fx", "ro"), ReadOnly: true},
			microvm.VirtioFSMount{Tag: "jlp", HostPath: filepath.Join(*shares, "jlp")},
		),
	}
	if *initCmd != "" {
		opts = append(opts, microvm.WithInitOverride(strings.Fields(*initCmd)...))
	} else {
		opts = append(opts, microvm.WithInitOverride("/bin/sh", "/spike-init.sh"))
	}
	if initScript != nil {
		opts = append(opts, microvm.WithRootFSHook(func(rootfs string, _ *image.OCIConfig) error {
			return os.WriteFile(filepath.Join(rootfs, "spike-init.sh"), initScript, 0o755) //nolint:gosec // G306: guest init must be executable
		}))
	}
	if *rootfs != "" {
		opts = append(opts, microvm.WithRootFSPath(*rootfs))
	}
	if *cache != "" {
		opts = append(opts, microvm.WithImageCache(image.NewCache(*cache)))
	}
	vm, err := microvm.Run(ctx, *img, opts...)
	if err != nil {
		event("vm-failed", err.Error())
		_ = bp.Stop(5 * time.Second)
		return fmt.Errorf("run vm: %w", err)
	}
	event("vm-started", fmt.Sprintf("id=%s after=%s", vm.ID(), time.Since(t0).Round(time.Millisecond)))
	if err := bp.WaitNIC(ctx, 30*time.Second); err != nil {
		event("nic-failed", err.Error())
		sctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = vm.Stop(sctx)
		_ = bp.Stop(5 * time.Second)
		return err
	}
	event("nic-connected", "after="+time.Since(t0).Round(time.Millisecond).String())

	out := brokerproc.Supervise(ctx, bp, vmHandle{vm}, 500*time.Millisecond, 10*time.Second)
	detail := ""
	if out.StopErr != nil {
		detail = out.StopErr.Error()
	}
	event("supervise-"+out.Reason, detail)
	return nil
}

func event(kind, detail string) {
	fmt.Printf("%s %s %s\n", time.Now().UTC().Format(time.RFC3339Nano), kind, detail)
}

func addrs(subnet string) (prefix, gateway, guest string, err error) {
	var a, b, c, d, bits int
	if _, err := fmt.Sscanf(subnet, "%d.%d.%d.%d/%d", &a, &b, &c, &d, &bits); err != nil || bits != 24 || d != 0 {
		return "", "", "", fmt.Errorf("subnet %q: want an IPv4 /24 like 192.168.127.0/24", subnet)
	}
	base := fmt.Sprintf("%d.%d.%d.", a, b, c)
	return subnet, base + "1", base + "2", nil
}

// ensureDisk creates a sparse raw image; the guest formats it on first use.
// An existing image smaller than size is grown back: libkrun's block layer
// (imago < 0.2.5) truncates the file when the guest discards or zeroes a range
// that reaches the end of the device, which mkfs does. The lost tail reads as
// zeroes either way, so growing it restores the device size the filesystem
// was created with.
func ensureDisk(path string, size int64) error {
	if st, err := os.Stat(path); err == nil {
		if st.Size() >= size {
			return nil
		}
		if err := os.Truncate(path, size); err != nil {
			return fmt.Errorf("regrow disk: %w", err)
		}
		fmt.Printf("%s disk-regrown from=%d to=%d\n", time.Now().UTC().Format(time.RFC3339Nano), st.Size(), size)
		return nil
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600) //nolint:gosec // G304: spike state path
	if err != nil {
		return fmt.Errorf("create disk: %w", err)
	}
	defer func() { _ = f.Close() }()
	if err := f.Truncate(size); err != nil {
		return fmt.Errorf("size disk: %w", err)
	}
	return nil
}
