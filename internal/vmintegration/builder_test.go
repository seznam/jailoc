//go:build vmintegration

package vmintegration

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"net/netip"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	dockerclient "github.com/moby/moby/client"

	"github.com/seznam/jailoc/internal/netbroker"
	"github.com/seznam/jailoc/internal/vm"
)

const (
	builderDiskInitial = 2 << 30
	builderDiskGrown   = 3 << 30

	// builderGrownMinBlocks is 2.5 GiB in 1 KiB df blocks: more than any
	// file system on the old 2 GiB disk, less than a 3 GiB one after ext4
	// metadata.
	builderGrownMinBlocks = (5 << 29) / 1024

	// The ready timeouts are outer safety nets, not boot allowances. vm.Run
	// bounds the guest itself: once the VM is started it waits at most 30 s
	// for the NIC and 3 min for dockerd, whatever these say. What the outer
	// timeouts add is the time before that: the first boot pulls the image
	// into the cache and builds the VM rootfs, a restart does only the latter.
	builderFirstBootTimeout = 10 * time.Minute
	builderRebootTimeout    = 5 * time.Minute
	builderStopTimeout      = 2 * time.Minute
)

// builderPolicy is the policy of the shared image builder VM (the default of
// vmBuilderPolicy for a workspace without network rules): public destinations
// are allowed, private and reserved ones denied.
const builderPolicy = `{}`

// builderDenyAllPolicy denies every destination, public ones by raw IP and by
// name included: a wildcard deny rule matches any public address and any name
// resolved to it, wins over allow rules, blocks name resolution, and its mere
// presence puts the policy in rules mode, where what no rule allows is denied.
// (An empty policy would allow public destinations.) A restarted builder must
// run offline from what the first boot left on its data disk.
const builderDenyAllPolicy = `{"deny":[{"name":"deny-all","names":["*"],"proto":"any"}]}`

// builderRun is a vm.Run of a builder VM on its own goroutine.
type builderRun struct {
	cancel  context.CancelFunc
	done    chan error
	stopped bool
}

// startBuilder runs spec on a goroutine until the returned run is stopped.
func startBuilder(ctx context.Context, spec vm.Spec) *builderRun {
	rctx, cancel := context.WithCancel(ctx)
	b := &builderRun{cancel: cancel, done: make(chan error, 1)}
	go func() { b.done <- vm.Run(rctx, spec) }()
	return b
}

// stop cancels the run and joins it within builderStopTimeout. vm.Run returns
// nil after a clean shutdown, once the guest init has stopped dockerd and
// unmounted the data disk. Stopping a stopped run is a no-op.
func (b *builderRun) stop() error {
	if b.stopped {
		return nil
	}
	b.stopped = true
	b.cancel()
	select {
	case err := <-b.done:
		if err != nil {
			return fmt.Errorf("builder vm.Run after stop: %w", err)
		}
		return nil
	case <-time.After(builderStopTimeout):
		return fmt.Errorf("builder vm.Run did not return within %s of the stop", builderStopTimeout)
	}
}

// waitBuilderReady waits until the supervisor of this process reports a ready
// builder, which means the guest init got through resize and dockerd answers
// on the Docker socket. It fails early when vm.Run returns.
func waitBuilderReady(ctx context.Context, b *builderRun, dir string, timeout time.Duration) (*vm.State, error) {
	deadline := time.NewTimer(timeout)
	defer deadline.Stop()
	tick := time.NewTicker(250 * time.Millisecond)
	defer tick.Stop()
	for {
		select {
		case err := <-b.done:
			b.stopped = true
			return nil, fmt.Errorf("vm.Run returned before the builder was ready: %v", err)
		case <-ctx.Done():
			return nil, fmt.Errorf("wait for builder: %w", ctx.Err())
		case <-deadline.C:
			return nil, fmt.Errorf("builder not ready within %s", timeout)
		case <-tick.C:
			st, err := vm.ReadState(dir)
			if err == nil && st.PID == os.Getpid() && st.Ready {
				return st, nil
			}
		}
	}
}

// parseDataDiskBlocks returns the size in 1 KiB blocks of /dev/vda in the
// df -k output that the guest init logs after the "data disk usage (phase):"
// line. The df -i output that follows has the same device row, so a row counts
// only under a "1K-blocks" header. The last such report in console wins.
func parseDataDiskBlocks(console, phase string) (int64, error) {
	const prefix = "jailoc-init:"
	marker := prefix + " data disk usage (" + phase + "):"
	const (
		seekMarker = iota
		seekHeader
		seekRow
	)
	state := seekMarker
	var blocks int64
	found := false
	for line := range strings.SplitSeq(console, "\n") {
		if strings.Contains(line, marker) {
			state = seekHeader
			continue
		}
		_, rest, ok := strings.Cut(line, prefix)
		if !ok || state == seekMarker {
			continue
		}
		fields := strings.Fields(rest)
		if len(fields) == 0 {
			continue
		}
		switch {
		case fields[0] == "Filesystem" && len(fields) > 1 && fields[1] == "1K-blocks":
			state = seekRow
		case fields[0] == "Filesystem":
			state = seekMarker // df -i header: no block row follows
		case state == seekRow && fields[0] == "/dev/vda" && len(fields) > 1:
			n, err := strconv.ParseInt(fields[1], 10, 64)
			if err != nil {
				return 0, fmt.Errorf("parse %q block count %q: %w", phase, fields[1], err)
			}
			blocks, found, state = n, true, seekMarker
		}
	}
	if !found {
		return 0, fmt.Errorf("builder console has no /dev/vda df -k row after %q", marker)
	}
	return blocks, nil
}

// dumpBuilderLogs logs the supervisor, runner, guest console and broker logs
// of the builder in dir.
func dumpBuilderLogs(t *testing.T, dir string) {
	t.Helper()
	for _, name := range []string{"supervisor.log", "microvm/vm.log", "microvm/console.log", "broker.jsonl"} {
		data, err := os.ReadFile(filepath.Join(dir, name)) //nolint:gosec // G304: test state file
		if err != nil {
			continue
		}
		t.Logf("==== builder %s ====\n%s", name, tail(data, 8000))
	}
}

func builderDiskSize(t *testing.T, dir string) int64 {
	t.Helper()
	st, err := os.Stat(filepath.Join(dir, "docker.img"))
	if err != nil {
		t.Fatalf("stat builder data disk: %v", err)
	}
	return st.Size()
}

func TestBuilderPolicies(t *testing.T) {
	t.Parallel()
	parse := func(t *testing.T, policy string) *netbroker.Policy {
		t.Helper()
		pol, err := netbroker.ParsePolicy([]byte(policy), "builder test policy")
		if err != nil {
			t.Fatalf("ParsePolicy(%s): %v", policy, err)
		}
		return pol
	}
	open, denyAll := parse(t, builderPolicy), parse(t, builderDenyAllPolicy)
	env := netbroker.Env{Gateway: netip.MustParseAddr(vm.Gateway), Subnet: netip.MustParsePrefix(vm.Subnet)}
	named := env
	named.Names = []string{"dl-cdn.alpinelinux.org"}
	tests := []struct {
		name  string
		env   netbroker.Env
		proto string
		dst   string
	}{
		{"raw public IP over https", env, netbroker.ProtoTCP, "151.101.2.132:443"},
		{"raw public IP over http", env, netbroker.ProtoTCP, "151.101.2.132:80"},
		{"resolved public name over https", named, netbroker.ProtoTCP, "151.101.2.132:443"},
		{"public DNS resolver", env, netbroker.ProtoUDP, "8.8.8.8:53"},
		{"DNS over TLS port", env, netbroker.ProtoTCP, "1.1.1.1:853"},
		{"QUIC port", env, netbroker.ProtoUDP, "151.101.2.132:443"},
		{"private address", env, netbroker.ProtoTCP, "10.0.0.5:443"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			dst := netip.MustParseAddrPort(tt.dst)
			if got := netbroker.Decide(denyAll, tt.env, tt.proto, dst); got.Allow {
				t.Errorf("deny-all policy allows %s %s: %+v", tt.proto, tt.dst, got)
			}
		})
	}
	// The first boot needs the network; the empty policy must still open it,
	// or the deny-all policy would prove nothing.
	if got := netbroker.Decide(open, env, netbroker.ProtoTCP, netip.MustParseAddrPort("151.101.2.132:443")); !got.Allow {
		t.Errorf("builder policy %s denies public https: %+v", builderPolicy, got)
	}
}

func TestParseDataDiskBlocks(t *testing.T) {
	t.Parallel()
	report := func(phase, blocks string) string {
		return "jailoc-init: data disk usage (" + phase + "):\n" +
			"jailoc-init:   Filesystem           1K-blocks      Used Available Use% Mounted on\n" +
			"jailoc-init:   /dev/vda               " + blocks + "      1024   2999999   0% /var/lib/docker\n" +
			"jailoc-init:   Filesystem              Inodes      Used Available Use% Mounted on\n" +
			"jailoc-init:   /dev/vda                 99999        11     99988   0% /var/lib/docker\n"
	}
	tests := []struct {
		name    string
		console string
		phase   string
		want    int64
		wantErr bool
	}{
		{name: "block row, not the inode row", console: report("after growth", "3030800"), phase: "after growth", want: 3030800},
		{name: "selects the phase", console: report("after mount", "1998000") + report("after growth", "3030800"), phase: "after mount", want: 1998000},
		{name: "last report wins", console: report("after growth", "1") + report("after growth", "2"), phase: "after growth", want: 2},
		{name: "console prefix and CRLF", console: "[    1.0] " + strings.ReplaceAll(report("after growth", "3030800"), "\n", "\r\n"), phase: "after growth", want: 3030800},
		{name: "phase missing", console: report("after mount", "1998000"), phase: "after growth", wantErr: true},
		{name: "inode report only", console: "jailoc-init: data disk usage (after growth):\njailoc-init:   Filesystem Inodes Used Available Use% Mounted on\njailoc-init:   /dev/vda 99999 11 99988 0% /var/lib/docker\n", phase: "after growth", wantErr: true},
		{name: "other device", console: "jailoc-init: data disk usage (after growth):\njailoc-init:   Filesystem 1K-blocks Used Available Use% Mounted on\njailoc-init:   tmpfs 3030800 0 3030800 0% /run\n", phase: "after growth", wantErr: true},
		{name: "block count not a number", console: report("after growth", "lots"), phase: "after growth", wantErr: true},
		{name: "empty console", phase: "after growth", wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, err := parseDataDiskBlocks(tt.console, tt.phase)
			if (err != nil) != tt.wantErr {
				t.Fatalf("parseDataDiskBlocks() error = %v, wantErr %t", err, tt.wantErr)
			}
			if got != tt.want {
				t.Errorf("parseDataDiskBlocks() = %d, want %d", got, tt.want)
			}
		})
	}
}

// TestBuilderVM boots the image builder VM like `jailoc up` does for the
// host-less image build, creates a Docker volume in its dockerd, restarts it
// with a larger data disk and expects the volume to survive and the disk file
// to have grown. A guest init that cannot get through its disk resize, cache
// or dockerd configuration never reports ready, so booting twice covers the
// first (mkfs) and the repeated (cached, grown) init paths. The restart denies
// all egress: the resize tool, its dependencies and nftables must come from the
// package cache the first boot left on the data disk.
func TestBuilderVM(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
	defer cancel()

	// The state directory holds unix sockets (103 bytes at most): keep it short.
	dir := filepath.Join(home, "bld")
	// home is this run's own fresh directory, so the state dir cannot exist.
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	for _, policy := range []string{builderPolicy, builderDenyAllPolicy} {
		if _, err := netbroker.ParsePolicy([]byte(policy), "builder test policy"); err != nil {
			t.Fatalf("builder policy %s: %v", policy, err)
		}
	}
	logFile, err := os.OpenFile(filepath.Join(dir, "supervisor.log"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600) //nolint:gosec // G304: test state file
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = logFile.Close() })

	spec := vm.Spec{
		Kind:       vm.KindBuilder,
		Workspace:  "builder",
		StateDir:   dir,
		Image:      vm.DefaultImage,
		CPUs:       2,
		MemoryMiB:  2048,
		DiskSize:   builderDiskInitial,
		Policy:     []byte(builderPolicy),
		RunnerPath: os.Getenv("JAILOC_VM_RUNNER"),
		LibDir:     os.Getenv("JAILOC_VM_LIBDIR"),
		ImageCache: filepath.Join(home, ".cache", "jailoc-vm", "images"),
		Broker:     []string{binaryPath, "netbroker"},
		Log:        logFile,
	}

	var run *builderRun
	// Cleanups run last-in first-out: stop the VM, then dump the logs.
	t.Cleanup(func() {
		if t.Failed() {
			dumpBuilderLogs(t, dir)
		}
	})
	t.Cleanup(func() {
		if run == nil {
			return
		}
		if err := run.stop(); err != nil {
			t.Errorf("cleanup: %v", err)
		}
	})

	boot := func(timeout time.Duration) *vm.State {
		t.Helper()
		// A state file left by the previous boot must not look like this one.
		if err := os.Remove(vm.StateFile(dir)); err != nil && !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("remove stale builder state: %v", err)
		}
		if err := vm.WriteSpec(spec); err != nil {
			t.Fatalf("write builder spec: %v", err)
		}
		start := time.Now()
		run = startBuilder(ctx, spec)
		st, err := waitBuilderReady(ctx, run, dir, timeout)
		if err != nil {
			t.Fatal(err)
		}
		t.Logf("builder ready after %s (disk %d MiB)", time.Since(start).Round(time.Second), spec.DiskSize>>20)
		return st
	}
	// dockerClient talks to the builder's dockerd over the broker's unix
	// socket forward, leaving DOCKER_HOST and HOME alone.
	dockerClient := func(st *vm.State) *dockerclient.Client {
		t.Helper()
		cli, err := dockerclient.New(dockerclient.WithHost("unix://" + st.DockerSocket))
		if err != nil {
			t.Fatalf("docker client: %v", err)
		}
		t.Cleanup(func() { _ = cli.Close() })
		return cli
	}

	var suffix [4]byte
	if _, err := rand.Read(suffix[:]); err != nil {
		t.Fatal(err)
	}
	volumeName := "jailoc-vmintegration-" + hex.EncodeToString(suffix[:])

	// First boot: mkfs on the empty data disk, then a volume in dockerd.
	st := boot(builderFirstBootTimeout)
	if got := builderDiskSize(t, dir); got != builderDiskInitial {
		t.Fatalf("data disk = %d bytes after the first boot, want %d", got, int64(builderDiskInitial))
	}
	cli := dockerClient(st)
	created, err := cli.VolumeCreate(ctx, dockerclient.VolumeCreateOptions{Name: volumeName})
	if err != nil {
		t.Fatalf("create volume %q: %v", volumeName, err)
	}
	if created.Volume.Name != volumeName {
		t.Fatalf("created volume = %q, want %q", created.Volume.Name, volumeName)
	}
	if _, err := cli.VolumeInspect(ctx, volumeName, dockerclient.VolumeInspectOptions{}); err != nil {
		t.Fatalf("inspect new volume %q: %v", volumeName, err)
	}
	if err := run.stop(); err != nil {
		t.Fatal(err)
	}

	// Second boot with a larger data disk and no network: the guest init must
	// grow the file system from its package cache and keep what dockerd
	// stored. Any download would fail, and a guest init that gives up never
	// reports ready.
	spec.DiskSize = builderDiskGrown
	spec.Policy = []byte(builderDenyAllPolicy)
	st = boot(builderRebootTimeout)
	if got := builderDiskSize(t, dir); got != builderDiskGrown {
		t.Fatalf("data disk = %d bytes after the restart, want %d", got, int64(builderDiskGrown))
	}
	cli = dockerClient(st)
	got, err := cli.VolumeInspect(ctx, volumeName, dockerclient.VolumeInspectOptions{})
	if err != nil {
		t.Fatalf("volume %q lost across the restart with a grown disk: %v", volumeName, err)
	}
	if got.Volume.Name != volumeName {
		t.Fatalf("volume after restart = %q, want %q", got.Volume.Name, volumeName)
	}
	// The guest console is complete once the VM has stopped.
	if err := run.stop(); err != nil {
		t.Fatal(err)
	}
	console, err := os.ReadFile(filepath.Join(dir, "microvm", "console.log")) //nolint:gosec // G304: test state file
	if err != nil {
		t.Fatalf("read builder console: %v", err)
	}
	// The host disk file only proves ensureDisk ran. The guest init logs df
	// after mounting the data disk and again after growing the file system:
	// the second boot must start at the old size and end at the new one.
	mounted, err := parseDataDiskBlocks(string(console), "after mount")
	if err != nil {
		t.Fatal(err)
	}
	grown, err := parseDataDiskBlocks(string(console), "after growth")
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("data disk file system: %d KiB after mount, %d KiB after growth", mounted, grown)
	if mounted > builderDiskInitial/1024 {
		t.Errorf("file system had %d KiB blocks after mount, want at most the old disk size (%d KiB)", mounted, int64(builderDiskInitial/1024))
	}
	if grown < builderGrownMinBlocks || grown > builderDiskGrown/1024 {
		t.Errorf("file system has %d KiB blocks after growth, want %d to %d: resize2fs did not grow it to the 3 GiB disk", grown, int64(builderGrownMinBlocks), int64(builderDiskGrown/1024))
	}
}
