// Command spike boots one go-microvm workspace VM for the P1 acceptance spike.
//
// usage: spike IMAGE DATA_DIR SHARES_DIR INIT_SCRIPT
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
)

func main() {
	if len(os.Args) != 5 {
		fmt.Fprintln(os.Stderr, "usage: spike IMAGE DATA_DIR SHARES_DIR INIT_SCRIPT")
		os.Exit(2)
	}
	img, dataDir, shares, initPath := os.Args[1], os.Args[2], os.Args[3], os.Args[4]
	initScript, err := os.ReadFile(initPath) //nolint:gosec // G304: spike input
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	start := time.Now()
	vm, err := microvm.Run(ctx, img,
		microvm.WithName("jailoc-go-microvm-spike"),
		microvm.WithCPUs(4),
		microvm.WithMemory(4096),
		microvm.WithTmpSize(1024),
		microvm.WithDataDir(dataDir),
		microvm.WithCleanDataDir(),
		microvm.WithBackend(libkrun.NewBackend(
			libkrun.WithRunnerPath(filepath.Join(filepath.Dir(dataDir), "go-microvm-runner")),
			libkrun.WithLibDir("/opt/homebrew/lib"),
		)),
		microvm.WithPorts(microvm.PortForward{Host: 47823, Guest: 4096}),
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
	fmt.Printf("runner_pid=%s rootfs=%s\n", vm.ID(), vm.RootFSPath())
	<-ctx.Done()
	stopStart := time.Now()
	if err := vm.Stop(context.Background()); err != nil {
		fmt.Fprintln(os.Stderr, err)
	}
	fmt.Printf("stop_elapsed=%s\n", time.Since(stopStart))
}
