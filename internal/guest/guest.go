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
	// SSHPort is the guest port of the SSH server.
	SSHPort = 22
)

// Share is a virtio-fs share mounted at GuestPath.
type Share struct {
	Tag       string `json:"tag"`
	GuestPath string `json:"guest_path"`
	ReadOnly  bool   `json:"read_only,omitempty"`
}

// CopiesDir is the guest path of the read-only share holding the staged
// copies.
const CopiesDir = Dir + "/copies"

// Copy binds Source, a file or directory inside CopiesDir, read-only at
// Target.
type Copy struct {
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
	// Copies are read-only snapshots bound from the staged copies share
	// to their destinations.
	Copies []Copy `json:"copies,omitempty"`
	// CABundle is a guest path appended to the system CA bundle.
	CABundle string `json:"ca_bundle,omitempty"`
}

// Secrets is the payload of "jailoc-guest secrets": environment entries
// added to every session and file secrets written to SecretsDir.
type Secrets struct {
	Env   []string          `json:"env,omitempty"`
	Files map[string][]byte `json:"files,omitempty"`
}
