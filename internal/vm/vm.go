// Package vm runs jailoc's go-microvm/libkrun guests. Every VM's only
// network path is a jailoc network broker. Two kinds exist:
//
//   - builder: a pinned docker:dind guest whose dockerd, reached through a
//     broker-forwarded unix socket, builds workspace images; Docker state
//     lives on a virtio-blk data disk;
//   - workspace: the built workspace image booted as the root file system,
//     with jailoc-guest as init and an SSH server for agent sessions.
package vm

import (
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/seznam/jailoc/internal/brokerproc"
	"github.com/seznam/jailoc/internal/guest"
	microvm "github.com/stacklok/go-microvm"
	"github.com/stacklok/go-microvm/hypervisor/libkrun"
	"github.com/stacklok/go-microvm/image"
	propnet "github.com/stacklok/go-microvm/net"
)

// Guest network served by the broker. Every workspace has its own broker and
// userspace stack, so all of them use the same addresses.
const (
	Subnet     = "192.168.127.0/24"
	Gateway    = "192.168.127.1"
	GuestIP    = "192.168.127.2"
	DockerPort = 2375
	agentGID   = 1000

	// DefaultImage is the guest image: Alpine with dockerd (docker:29-dind,
	// pinned by its multi-platform index digest).
	DefaultImage = "docker@sha256:9e79b879b177fbd72c34376a3dd3d91747da5db49e1f560f76aea59561471977"
	// DefaultDiskSize is the sparse data disk holding /var/lib/docker.
	DefaultDiskSize = 20 << 30

	// maxSocketPath is the shortest sun_path limit of the supported hosts
	// (macOS: 104 bytes including the terminating NUL).
	maxSocketPath = 103
)

//go:embed init.sh
var initScript string

// VM kinds.
const (
	KindBuilder   = "builder"
	KindWorkspace = "workspace"
)

// Share is a host directory mounted in the guest at GuestPath, or at the
// host path when GuestPath is empty.
type Share struct {
	HostPath  string `json:"host_path"`
	GuestPath string `json:"guest_path,omitempty"`
	ReadOnly  bool   `json:"read_only,omitempty"`
}

func (s Share) guestPath() string {
	if s.GuestPath != "" {
		return s.GuestPath
	}
	return s.HostPath
}

// Forward publishes a guest TCP port on a host loopback port.
type Forward struct {
	HostPort  int `json:"host_port"`
	GuestPort int `json:"guest_port"`
}

// Spec describes one workspace VM. jailoc writes it to the state directory
// and the supervisor process reads it back.
type Spec struct {
	Kind       string          `json:"kind"`
	Workspace  string          `json:"workspace"`
	StateDir   string          `json:"state_dir"`
	Image      string          `json:"image"`
	CPUs       uint32          `json:"cpus"`
	MemoryMiB  uint32          `json:"memory_mib"`
	DiskSize   int64           `json:"disk_size"`
	Shares     []Share         `json:"shares"`
	Forwards   []Forward       `json:"forwards,omitempty"`
	Policy     json.RawMessage `json:"policy"`
	RunnerPath string          `json:"runner_path,omitempty"`
	LibDir     string          `json:"lib_dir,omitempty"`
	ImageCache string          `json:"image_cache,omitempty"`
	// Broker is the command that runs the network broker, e.g. the jailoc
	// executable followed by "netbroker".
	Broker []string `json:"broker"`
	// GuestBinary is the host path of jailoc-guest (workspace VMs).
	GuestBinary string `json:"guest_binary,omitempty"`
	// Guest is the guest configuration (workspace VMs); the supervisor
	// fills in the authorized key.
	Guest *guest.Config `json:"guest,omitempty"`
	// Docker attaches a persistent data disk for the Docker daemon of a
	// workspace VM (the builder always has one).
	Docker bool `json:"docker,omitempty"`
	// SSHAgent is the host SSH agent socket relayed into a workspace VM.
	SSHAgent string `json:"ssh_agent,omitempty"`
	// CABundle is a host PEM file the builder's Docker daemon trusts in
	// addition to the image's CA certificates.
	CABundle string `json:"ca_bundle,omitempty"`
	// Log receives lifecycle messages and the broker's output.
	Log io.Writer `json:"-"`
}

// SpecFile is where jailoc stores the spec of the workspace's VM.
func SpecFile(stateDir string) string { return filepath.Join(stateDir, "spec.json") }

// WriteSpec stores spec in its state directory.
func WriteSpec(spec Spec) error {
	if err := os.MkdirAll(spec.StateDir, 0o700); err != nil {
		return fmt.Errorf("create vm state dir: %w", err)
	}
	data, err := json.MarshalIndent(spec, "", "  ")
	if err != nil {
		return fmt.Errorf("encode vm spec: %w", err)
	}
	if err := os.WriteFile(SpecFile(spec.StateDir), data, 0o600); err != nil {
		return fmt.Errorf("write vm spec: %w", err)
	}
	return nil
}

// ReadSpec loads the spec stored in stateDir.
func ReadSpec(stateDir string) (*Spec, error) {
	data, err := os.ReadFile(SpecFile(stateDir)) //nolint:gosec // G304: jailoc-owned state path
	if err != nil {
		return nil, err
	}
	var spec Spec
	if err := json.Unmarshal(data, &spec); err != nil {
		return nil, fmt.Errorf("parse vm spec: %w", err)
	}
	return &spec, nil
}

// Paths of the files a VM keeps in its state directory.
func StateFile(stateDir string) string    { return filepath.Join(stateDir, "state.json") }
func DockerSocket(stateDir string) string { return filepath.Join(stateDir, "docker.sock") }
func SSHSocket(stateDir string) string    { return filepath.Join(stateDir, "ssh.sock") }
func nicSocket(stateDir string) string    { return filepath.Join(stateDir, "nic.sock") }
func diskPath(stateDir string) string     { return filepath.Join(stateDir, "docker.img") }

// State is written by the supervisor and read by jailoc commands.
type State struct {
	PID          int       `json:"pid"`
	BrokerPID    int       `json:"broker_pid,omitempty"`
	DockerSocket string    `json:"docker_socket,omitempty"`
	SSHSocket    string    `json:"ssh_socket,omitempty"`
	Ready        bool      `json:"ready"`
	Error        string    `json:"error,omitempty"`
	Updated      time.Time `json:"updated"`
}

// ReadState returns the supervisor state, or os.ErrNotExist.
func ReadState(stateDir string) (*State, error) {
	data, err := os.ReadFile(StateFile(stateDir)) //nolint:gosec // G304: jailoc-owned state path
	if err != nil {
		return nil, err
	}
	var st State
	if err := json.Unmarshal(data, &st); err != nil {
		return nil, fmt.Errorf("parse vm state: %w", err)
	}
	return &st, nil
}

func writeState(stateDir string, st State) error {
	st.Updated = time.Now().UTC()
	data, err := json.Marshal(st)
	if err != nil {
		return fmt.Errorf("encode vm state: %w", err)
	}
	tmp := StateFile(stateDir) + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return fmt.Errorf("write vm state: %w", err)
	}
	if err := os.Rename(tmp, StateFile(stateDir)); err != nil {
		return fmt.Errorf("write vm state: %w", err)
	}
	return nil
}

// Run starts the broker and the VM, waits until the guest is ready (the
// builder's dockerd answers on the Docker socket, the workspace's SSH server
// completes a handshake), and then supervises both until ctx is cancelled or
// either side exits. The state file tracks progress for other processes.
func Run(ctx context.Context, spec Spec) (err error) {
	logf := func(format string, args ...any) {
		if spec.Log != nil {
			_, _ = fmt.Fprintf(spec.Log, "%s "+format+"\n", append([]any{time.Now().UTC().Format(time.RFC3339)}, args...)...)
		}
	}
	workspace := spec.Kind == KindWorkspace
	st := State{PID: os.Getpid()}
	sockets := []string{nicSocket(spec.StateDir)}
	if workspace {
		st.SSHSocket = SSHSocket(spec.StateDir)
		sockets = append(sockets, st.SSHSocket)
	} else {
		st.DockerSocket = DockerSocket(spec.StateDir)
		sockets = append(sockets, st.DockerSocket)
	}
	defer func() {
		st.Ready = false
		if err != nil {
			st.Error = err.Error()
			logf("vm stopped: %v", err)
		}
		_ = writeState(spec.StateDir, st)
	}()

	if err := os.MkdirAll(spec.StateDir, 0o700); err != nil {
		return fmt.Errorf("create vm state dir: %w", err)
	}
	if err := writeState(spec.StateDir, st); err != nil {
		return err
	}
	for _, p := range sockets {
		if len(p) > maxSocketPath {
			return fmt.Errorf("socket path %q is longer than %d bytes", p, maxSocketPath)
		}
	}
	if !workspace || spec.Docker {
		diskSize := spec.DiskSize
		if diskSize == 0 {
			diskSize = DefaultDiskSize
		}
		if err := ensureDisk(diskPath(spec.StateDir), diskSize); err != nil {
			return err
		}
	}
	var guestFiles map[string]fileContent
	if workspace {
		if guestFiles, err = prepareGuest(spec); err != nil {
			return err
		}
	}
	ctl := controlDir(spec.StateDir)
	if err := os.RemoveAll(ctl); err != nil {
		return fmt.Errorf("reset vm control dir: %w", err)
	}
	if err := os.MkdirAll(ctl, 0o700); err != nil {
		return fmt.Errorf("create vm control dir: %w", err)
	}
	policyPath := PolicyFile(spec.StateDir)
	if err := WritePolicy(spec); err != nil {
		return fmt.Errorf("write broker policy: %w", err)
	}

	if err := WriteBrokerRuntime(spec); err != nil {
		return err
	}
	bp, err := startBroker(ctx, spec, policyPath)
	if err != nil {
		return err
	}
	st.BrokerPID = bp.PID()
	logf("broker ready pid=%d", bp.PID())

	opts := builderOptions(spec)
	if workspace {
		opts = workspaceOptions(spec, guestFiles)
	}
	vm, err := microvm.Run(ctx, imageRef(spec), opts...)
	if err != nil {
		_ = bp.Stop(5 * time.Second)
		return fmt.Errorf("start vm: %w", err)
	}
	logf("vm started")
	stopBoth := func() {
		sctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = vm.Stop(sctx)
		_ = bp.Stop(5 * time.Second)
	}
	if err := bp.WaitNIC(ctx, 30*time.Second); err != nil {
		stopBoth()
		return err
	}
	if workspace {
		err = waitSSH(ctx, spec.StateDir, 2*time.Minute)
	} else {
		err = waitDocker(ctx, st.DockerSocket, 3*time.Minute)
	}
	if err != nil {
		stopBoth()
		return err
	}
	st.Ready = true
	if err := writeState(spec.StateDir, st); err != nil {
		stopBoth()
		return err
	}
	logf("%s ready", spec.Kind)

	out := brokerproc.Supervise(ctx, bp, vmHandle{vm: vm, control: ctl}, 500*time.Millisecond, stopGrace)
	logf("supervise %s", out.Reason)
	if out.Reason == "shutdown" {
		return nil
	}
	return fmt.Errorf("%s vm stopped: %s", spec.Kind, out.Reason)
}

func imageRef(spec Spec) string {
	if spec.Image != "" {
		return spec.Image
	}
	return DefaultImage
}

func startBroker(ctx context.Context, spec Spec, policyPath string) (*brokerproc.Process, error) {
	if len(spec.Broker) == 0 {
		return nil, errors.New("no broker command")
	}
	args := append(append([]string{}, spec.Broker[1:]...),
		"-socket", nicSocket(spec.StateDir),
		"-policy", policyPath,
		"-runtime", BrokerRuntimeFile(spec.StateDir),
		"-subnet", Subnet, "-gateway", Gateway, "-guest", GuestIP,
		"-log", filepath.Join(spec.StateDir, "broker.jsonl"),
	)
	if spec.Kind == KindWorkspace {
		args = append(args, "-forward-unix", SSHSocket(spec.StateDir)+"="+strconv.Itoa(guest.SSHPort))
	} else {
		args = append(args, "-forward-unix", DockerSocket(spec.StateDir)+"="+strconv.Itoa(DockerPort))
	}
	bp, err := brokerproc.Start(ctx, brokerproc.Options{
		Binary: spec.Broker[0], Args: args, ReadyTimeout: 10 * time.Second,
		Stdout: spec.Log, Stderr: spec.Log,
	})
	if err != nil {
		return nil, fmt.Errorf("start network broker: %w", err)
	}
	return bp, nil
}

// commonOptions are shared by both VM kinds.
func commonOptions(spec Spec) []microvm.Option {
	backend := []libkrun.Option{}
	if spec.RunnerPath != "" {
		backend = append(backend, libkrun.WithRunnerPath(spec.RunnerPath))
	}
	if spec.LibDir != "" {
		backend = append(backend, libkrun.WithLibDir(spec.LibDir))
	}
	opts := []microvm.Option{
		microvm.WithName("jailoc-" + spec.Workspace),
		microvm.WithCPUs(spec.CPUs),
		microvm.WithMemory(spec.MemoryMiB),
		microvm.WithDataDir(filepath.Join(spec.StateDir, "microvm")),
		microvm.WithCleanDataDir(),
		microvm.WithBackend(libkrun.NewBackend(backend...)),
		microvm.WithNetProvider(brokerProvider{path: nicSocket(spec.StateDir)}),
	}
	if spec.ImageCache != "" {
		opts = append(opts, microvm.WithImageCache(image.NewCache(spec.ImageCache)))
	}
	return opts
}

func builderOptions(spec Spec) []microvm.Option {
	script := strings.NewReplacer(
		"@GATEWAY@", Gateway, "@GUEST@", GuestIP,
		"@DOCKER_PORT@", strconv.Itoa(DockerPort), "@AGENT_GID@", strconv.Itoa(agentGID),
		"@CONTROL@", controlDir(spec.StateDir),
	).Replace(initScript)
	mounts := make([]microvm.VirtioFSMount, 0, len(spec.Shares))
	var table strings.Builder
	for i, s := range spec.Shares {
		tag := fmt.Sprintf("jailoc%d", i)
		// Simplified ownership: the container agent (uid 1000) and root both see
		// shared files as their own, whatever the host owner is.
		mounts = append(mounts, microvm.VirtioFSMount{Tag: tag, HostPath: s.HostPath, ReadOnly: s.ReadOnly, SimplifiedOwnership: true})
		mode := "rw"
		if s.ReadOnly {
			mode = "ro"
		}
		fmt.Fprintf(&table, "%s\t%s\t%s\n", tag, mode, s.guestPath())
	}
	// The control share carries the shutdown request from the supervisor to
	// the guest init; it is the only state-dir content the guest sees.
	mounts = append(mounts, microvm.VirtioFSMount{Tag: "jailocctl", HostPath: controlDir(spec.StateDir), SimplifiedOwnership: true})
	fmt.Fprintf(&table, "%s\t%s\t%s\n", "jailocctl", "rw", controlDir(spec.StateDir))
	return append(commonOptions(spec),
		microvm.WithDisks(microvm.Disk{ID: "docker", HostPath: diskPath(spec.StateDir)}),
		microvm.WithVirtioFS(mounts...),
		microvm.WithInitOverride("/bin/sh", "/jailoc-init.sh"),
		microvm.WithRootFSHook(func(rootfs string, _ *image.OCIConfig) error {
			if err := os.WriteFile(filepath.Join(rootfs, "jailoc-shares"), []byte(table.String()), 0o644); err != nil { //nolint:gosec // G306: read by the guest init
				return fmt.Errorf("write share table: %w", err)
			}
			if err := os.WriteFile(filepath.Join(rootfs, "jailoc-init.sh"), []byte(script), 0o755); err != nil { //nolint:gosec // G306: guest init must be executable
				return fmt.Errorf("write guest init: %w", err)
			}
			if spec.CABundle != "" {
				pem, err := os.ReadFile(spec.CABundle)
				if err != nil {
					return fmt.Errorf("read CA bundle: %w", err)
				}
				if err := os.WriteFile(filepath.Join(rootfs, "jailoc-ca.pem"), pem, 0o644); err != nil { //nolint:gosec // G306: public certificates
					return fmt.Errorf("write CA bundle: %w", err)
				}
			}
			return nil
		}),
	)
}

// ensureDisk creates the sparse data disk, or grows it back to size. libkrun
// releases built with imago < 0.2.5 truncate the image when a discard or
// write-zeroes request reaches its end (libkrun/libkrun#949), which mkfs does.
func ensureDisk(path string, size int64) error {
	st, err := os.Stat(path)
	if err == nil {
		if st.Size() < size {
			if err := os.Truncate(path, size); err != nil {
				return fmt.Errorf("grow data disk: %w", err)
			}
		}
		return nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("stat data disk: %w", err)
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600) //nolint:gosec // G304: jailoc-owned state path
	if err != nil {
		return fmt.Errorf("create data disk: %w", err)
	}
	if err := f.Truncate(size); err != nil {
		_ = f.Close()
		return fmt.Errorf("size data disk: %w", err)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("close data disk: %w", err)
	}
	return nil
}

// waitDocker polls the Docker API behind the broker's unix socket forward.
func waitDocker(ctx context.Context, socket string, timeout time.Duration) error {
	client := &http.Client{
		Timeout: 2 * time.Second,
		Transport: &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			var d net.Dialer
			return d.DialContext(ctx, "unix", socket)
		}},
	}
	deadline := time.Now().Add(timeout)
	for {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://docker/_ping", nil)
		if err != nil {
			return fmt.Errorf("build docker ping: %w", err)
		}
		resp, err := client.Do(req)
		if err == nil {
			_ = resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return nil
			}
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("guest docker did not answer within %s", timeout)
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("wait for guest docker: %w", ctx.Err())
		case <-time.After(time.Second):
		}
	}
}

type brokerProvider struct{ path string }

func (p brokerProvider) Start(context.Context, propnet.Config) error {
	if _, err := os.Stat(p.path); err != nil {
		return fmt.Errorf("broker socket: %w", err)
	}
	return nil
}
func (p brokerProvider) SocketPath() string { return p.path }
func (brokerProvider) Stop()                {}

// stopGrace bounds how long the supervisor waits for the guest to shut down
// cleanly and for the broker to exit.
const stopGrace = 30 * time.Second

// guestShutdownWait is the part of stopGrace given to the guest init to stop
// dockerd, sync and unmount the data disk before the VM is stopped hard.
const guestShutdownWait = 20 * time.Second

func controlDir(stateDir string) string { return filepath.Join(stateDir, "control") }

// ControlDir is the directory shared read-write with the workspace guest
// for shutdown and reload requests.
func ControlDir(stateDir string) string { return controlDir(stateDir) }

// PolicyFile is the network policy the broker reloads on SIGHUP.
func PolicyFile(stateDir string) string { return filepath.Join(stateDir, "policy.json") }

// BrokerRuntimeFile lists the published ports and the SSH agent service the
// broker reapplies on SIGHUP.
func BrokerRuntimeFile(stateDir string) string { return filepath.Join(stateDir, "runtime.json") }

// WriteBrokerRuntime writes the broker runtime file of spec.
func WriteBrokerRuntime(spec Spec) error {
	type forward struct {
		Host      string `json:"host"`
		GuestPort int    `json:"guest_port"`
	}
	type service struct {
		GuestPort uint16 `json:"guest_port"`
		Path      string `json:"path"`
	}
	rt := struct {
		Forwards []forward `json:"forwards"`
		Services []service `json:"services"`
	}{Forwards: []forward{}, Services: []service{}}
	for _, f := range spec.Forwards {
		rt.Forwards = append(rt.Forwards, forward{Host: fmt.Sprintf("127.0.0.1:%d", f.HostPort), GuestPort: f.GuestPort})
	}
	if spec.Kind == KindWorkspace && spec.SSHAgent != "" {
		rt.Services = append(rt.Services, service{GuestPort: guest.SSHAgentPort, Path: spec.SSHAgent})
	}
	data, err := json.Marshal(rt)
	if err != nil {
		return fmt.Errorf("encode broker runtime: %w", err)
	}
	return writeFileAtomic(BrokerRuntimeFile(spec.StateDir), data)
}

// WritePolicy writes the network policy of spec.
func WritePolicy(spec Spec) error {
	return writeFileAtomic(PolicyFile(spec.StateDir), spec.Policy)
}

func writeFileAtomic(path string, data []byte) error {
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return fmt.Errorf("write %s: %w", filepath.Base(path), err)
	}
	if err := os.Rename(tmp, path); err != nil {
		return fmt.Errorf("install %s: %w", filepath.Base(path), err)
	}
	return nil
}

type vmHandle struct {
	vm      *microvm.VM
	control string
}

func (h vmHandle) Alive() bool {
	info, err := h.vm.Status(context.Background())
	return err == nil && info.Active
}

// Stop asks the guest init to shut down cleanly through the control share
// (the builder's dockerd flushes its state to the data disk, the workspace
// guest ends its sessions and syncs), then stops the VM.
func (h vmHandle) Stop(ctx context.Context) error {
	if h.Alive() {
		if err := os.WriteFile(filepath.Join(h.control, "shutdown"), nil, 0o600); err == nil {
			wait, cancel := context.WithTimeout(ctx, guestShutdownWait)
			for h.Alive() && wait.Err() == nil {
				select {
				case <-wait.Done():
				case <-time.After(200 * time.Millisecond):
				}
			}
			cancel()
		}
	}
	return h.vm.Stop(ctx)
}
