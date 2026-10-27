// Package guest is the jailoc agent inside a backend "vm" workspace VM:
// it prepares the guest (mounts, network, user, CA bundle), serves SSH
// sessions for the agent user and powers the VM off on request.
//
// The config types are shared with the host, which writes Config into the
// guest root file system before boot. The boot code is Linux only.
package guest

// Paths inside the guest root file system.
const (
	// Dir holds the jailoc guest binary and its configuration.
	Dir = "/.jailoc"
	// BinaryPath is the jailoc-guest binary injected by the host.
	BinaryPath = Dir + "/jailoc-guest"
	// ConfigPath is the guest configuration written by the host.
	ConfigPath = Dir + "/guest.json"
	// HostKeyPath is the SSH host key written by the host; the guest
	// deletes it after loading.
	HostKeyPath = Dir + "/ssh_host_key"
	// ControlDir is shared read-write with the host; a file named
	// "shutdown" in it powers the VM off.
	ControlDir = Dir + "/control"
	// SecretsDir holds file secrets on a tmpfs owned by the agent user.
	SecretsDir = "/run/secrets"
	// EnvFile holds KEY=VALUE lines added to every agent session.
	EnvFile = "/run/jailoc/env"
	// MTU of the guest network interface. It matches the broker's MTU;
	// large frames cut per-packet work in the broker roughly in half.
	MTU = 65520
	// SSHPort is the guest port of the SSH server.
	SSHPort = 22
	// SSHAgentPort is the gateway port the broker relays to the host SSH
	// agent when the workspace forwards it.
	SSHAgentPort = 7022
	// SSHAgentSocket is the agent socket sessions see as SSH_AUTH_SOCK.
	SSHAgentSocket = "/run/ssh-agent.sock"
)

// Share is a virtio-fs share mounted at GuestPath.
type Share struct {
	Tag       string `json:"tag"`
	GuestPath string `json:"guest_path"`
	ReadOnly  bool   `json:"read_only,omitempty"`
}

// SnapshotsDir is the guest path of the read-only share holding the staged
// snapshots.
const SnapshotsDir = Dir + "/snapshots"

// Snapshot binds Source, a file or directory inside SnapshotsDir, read-only at
// Target.
type Snapshot struct {
	Source string `json:"source"`
	Target string `json:"target"`
}

// Config is the guest configuration the host writes to ConfigPath.
type Config struct {
	Hostname      string   `json:"hostname"`
	User          string   `json:"user"`
	UID           uint32   `json:"uid"`
	GID           uint32   `json:"gid"`
	Home          string   `json:"home"`
	WorkDir       string   `json:"work_dir"`
	Shares        []Share  `json:"shares"`
	AuthorizedKey string   `json:"authorized_key"`
	Env           []string `json:"env,omitempty"`
	// Snapshots are read-only host files and directories bound from the
	// staged snapshots share to their destinations.
	Snapshots []Snapshot `json:"snapshots,omitempty"`
	// CABundle is a guest path appended to the system CA bundle.
	CABundle string `json:"ca_bundle,omitempty"`
	// SSHAgent exposes the host SSH agent at SSHAgentSocket, relayed
	// through the broker gateway.
	SSHAgent bool `json:"ssh_agent,omitempty"`
	// Pins are paths bound onto themselves so that they cannot be renamed
	// or removed in the guest; they keep their share's writability.
	Pins []string `json:"pins,omitempty"`
	// ReadOnly are paths bound read-only onto themselves.
	ReadOnly []string `json:"read_only,omitempty"`
	// NoPrivilegeEscalation removes privilege escalation, including sudo,
	// from sessions, so the agent cannot undo Pins and ReadOnly.
	NoPrivilegeEscalation bool `json:"no_privilege_escalation,omitempty"`
	// Docker, when set, makes the guest run dockerd with its data on a
	// block device.
	Docker *Docker `json:"docker,omitempty"`
}

// DockerDisk is the block device of the workspace VM's Docker data disk.
const DockerDisk = "/dev/vda"

// Docker configures the Docker daemon of a workspace VM.
type Docker struct {
	// Disk is the block device mounted at /var/lib/docker; it is
	// formatted as ext4 when it has no file system yet.
	Disk string `json:"disk"`
}

// Secrets is the payload of "jailoc-guest secrets": environment entries
// added to every session and file secrets written to SecretsDir.
type Secrets struct {
	Env   []string          `json:"env,omitempty"`
	Files map[string][]byte `json:"files,omitempty"`
}
