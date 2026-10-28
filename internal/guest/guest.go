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
	// PrivateDir is a root-only tmpfs holding HostDir, so the agent user
	// cannot reach jailoc's internal share.
	PrivateDir = Dir + "/private"
	// HostDir is the mount point of the VM's single internal share: the
	// host's per-VM guest data directory with the control files, staged
	// snapshots, jailoc configuration copies and persistent home
	// directories. The guest binds parts of it into place.
	HostDir = PrivateDir + "/host"
	// ControlDir is the control directory inside HostDir; a file named
	// "shutdown" in it powers the VM off.
	ControlDir = HostDir + "/control"
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

// IDMap holds the host user's IDs as virtio-fs reports them in the guest.
type IDMap struct {
	UID uint32 `json:"uid"`
	GID uint32 `json:"gid"`
}

// Share is a virtio-fs share mounted at GuestPath.
type Share struct {
	Tag       string `json:"tag"`
	GuestPath string `json:"guest_path"`
	ReadOnly  bool   `json:"read_only,omitempty"`
}

// SnapshotsDir is the guest path of the staged snapshots inside HostDir.
const SnapshotsDir = HostDir + "/snapshots"

// Bind binds Source, a directory inside HostDir, at Target.
type Bind struct {
	Source   string `json:"source"`
	Target   string `json:"target"`
	ReadOnly bool   `json:"read_only,omitempty"`
}

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
	// Binds place directories of the internal share (HostDir): jailoc
	// configuration read-only, persistent home directories read-write.
	Binds []Bind `json:"binds,omitempty"`
	// Snapshots are read-only host files and directories bound from the
	// staged snapshots to their destinations.
	Snapshots []Snapshot `json:"snapshots,omitempty"`
	// CABundle is a guest path appended to the system CA bundle.
	CABundle string `json:"ca_bundle,omitempty"`
	// IDMap is set when virtio-fs shows the real host owner of shared files
	// and of the root filesystem (Linux hosts). The guest then mounts them
	// idmapped so the host user appears as root in the root filesystem and
	// as the agent in its home and the shares. Without it, or when the guest
	// kernel cannot idmap, the agent takes the host user's IDs.
	IDMap *IDMap `json:"idmap,omitempty"`
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
