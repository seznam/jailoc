// Command spike boots one go-microvm workspace VM whose only NIC is served by
// an external jailoc-netbroker (P2 network proof).
//
// usage: spike IMAGE DATA_DIR SHARES_DIR INIT_SCRIPT BROKER_SOCKET
//
// SHARES_DIR must contain fx/rw, fx/ro, jlp and dstate. INIT_SCRIPT is copied
// into the rootfs as /spike-init.sh and replaces the image entrypoint.
package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	microvm "github.com/stacklok/go-microvm"
	"github.com/stacklok/go-microvm/hypervisor/libkrun"
	"github.com/stacklok/go-microvm/image"
	propnet "github.com/stacklok/go-microvm/net"
)

// brokerProvider hands libkrun the socket of an already running broker. The
// broker owns DHCP, DNS, policy and the published ports.
type brokerProvider struct{ path string }

func (p brokerProvider) Start(context.Context, propnet.Config) error {
	if _, err := os.Stat(p.path); err != nil {
		return fmt.Errorf("broker socket: %w", err)
	}
	return nil
}
func (p brokerProvider) SocketPath() string { return p.path }
func (brokerProvider) Stop()                {}

func main() {
	if len(os.Args) != 6 {
		fmt.Fprintln(os.Stderr, "usage: spike IMAGE DATA_DIR SHARES_DIR INIT_SCRIPT BROKER_SOCKET")
		os.Exit(2)
	}
	img, dataDir, shares, initPath, sock := os.Args[1], os.Args[2], os.Args[3], os.Args[4], os.Args[5]
	initScript, err := os.ReadFile(initPath) //nolint:gosec // G304: spike input
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	start := time.Now()
	vm, err := microvm.Run(ctx, img,
		microvm.WithName("jailoc-p2-broker-spike"),
		microvm.WithCPUs(4),
		microvm.WithMemory(3072),
		microvm.WithTmpSize(1024),
		microvm.WithDataDir(dataDir),
		microvm.WithCleanDataDir(),
		microvm.WithBackend(libkrun.NewBackend(
			libkrun.WithRunnerPath(filepath.Join(filepath.Dir(dataDir), "go-microvm-runner")),
			libkrun.WithLibDir("/opt/homebrew/lib"),
		)),
		microvm.WithNetProvider(brokerProvider{path: sock}),
		microvm.WithVirtioFS(
			microvm.VirtioFSMount{Tag: "fx-rw", HostPath: filepath.Join(shares, "fx", "rw")},
			microvm.VirtioFSMount{Tag: "fx-ro", HostPath: filepath.Join(shares, "fx", "ro"), ReadOnly: true},
			microvm.VirtioFSMount{Tag: "jlp", HostPath: filepath.Join(shares, "jlp")},
			microvm.VirtioFSMount{Tag: "dstate", HostPath: filepath.Join(shares, "dstate")},
		),
		microvm.WithInitOverride("/bin/sh", "/spike-init.sh"),
		microvm.WithRootFSHook(func(rootfs string, _ *image.OCIConfig) error {
			return os.WriteFile(filepath.Join(rootfs, "spike-init.sh"), initScript, 0o755) //nolint:gosec // G306: guest init must be executable
		}),
	)
	fmt.Printf("run_elapsed=%s error=%v\n", time.Since(start), err)
	if err != nil {
		os.Exit(1)
	}
	fmt.Printf("runner_pid=%s\n", vm.ID())
	<-ctx.Done()
	stopStart := time.Now()
	if err := vm.Stop(context.Background()); err != nil {
		fmt.Fprintln(os.Stderr, err)
	}
	fmt.Printf("stop_elapsed=%s\n", time.Since(stopStart))
}
