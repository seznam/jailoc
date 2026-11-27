# Architecture

Each workspace runs in its own libkrun micro-VM. This page describes the processes on the host, what happens inside the guest, how files and the network reach the VM, and how images are built.

![jailoc architecture: host processes (jailoc CLI, vm-supervise, jailoc-netbroker, jailoc-vm-runner with libkrun) with the image cache, and the workspace VM (libkrunfw kernel, jailoc-guest as PID 1, the coding agent and its processes), separated by the VM boundary that only virtio devices cross](img/architecture.svg)

## Host processes

Every VM is owned by a detached supervisor process (`jailoc vm-supervise`). It starts the workspace's network broker, boots the VM through `jailoc-vm-runner`, waits until the guest is ready, and from then on keeps the two together: when either the VM or the broker exits, it stops the other. A workspace never runs without its broker.

### The VM runtime

The VM runtime (runner, libkrun and the libkrunfw guest kernel) is embedded in release builds of jailoc and unpacked into the cache on first use. Builds without it need `runner` and `lib_dir` in the `[vm]` section. jailoc itself is built without cgo.

### jailoc-guest versions

`jailoc-guest` is pure Go and is embedded in jailoc, so a newly started VM always runs the guest of the jailoc version that started it.

- A running VM keeps its guest across jailoc upgrades. jailoc attaches to it as long as the guest speaks the same protocol, and asks for `jailoc restart` only after an upgrade that changed the protocol.
- Guest fixes of an upgrade take effect at the workspace's next restart.

## Images

Workspace images are ordinary Docker images: the embedded Dockerfile, a `[base] dockerfile` preset and an optional per-workspace overlay (`dockerfile`, `build_context`). See [Image Resolution](../reference/image-resolution.md). The image needs no entrypoint, command, exposed port or firewall setup, because `jailoc-guest` is the init.

`[vm] builder` decides where the build runs: the host Docker daemon, or a builder VM. The builder VM runs a pinned `docker:dind` image with a persistent data disk, reached through a Unix socket that its broker forwards into the VM. The finished image is exported and its layers are converted into read-only EROFS images in a local image cache, and a builder VM shuts down afterwards.

Building on the host daemon is fastest, but Dockerfile `RUN` steps then run with container isolation and unrestricted host networking. A builder VM keeps build steps behind a VM boundary. All workspaces share one builder VM, with one build cache, and it reaches public addresses only. With `isolated_build`, a workspace gets a builder of its own, with a separate data disk and the workspace's `allowed_hosts` and `allowed_networks`, so one workspace's build steps can't read another workspace's build cache or private network access. Builds on the same builder are serialized.

When `ca_bundle` selects an external bundle, builds always use a builder VM, so that the builder's daemon and the build steps can trust the bundle without any change to the host Docker daemon's trust. See [Image builds with a CA bundle](../reference/configuration.md#image-builds-with-a-ca-bundle).

## Inside the guest

The VM boots from a small root file system, the boot root, that holds only `jailoc-guest` and its configuration. The image's layers, one read-only EROFS file each, are shared into the VM as one read-only share. `jailoc-guest` mounts each layer file as EROFS, stacks the layers as overlay lower layers under the boot root with a tmpfs upper layer, and switches to the overlay.

Writes are held in VM memory (swappable to compressed RAM), so files written outside the shares and the persistent home count against `memory` and are discarded when the VM stops.

`jailoc-guest` then prepares the system and serves SSH:

1. Configures the network addresses, the broker as gateway and DNS resolver, and the hostname.
2. Sets the clock to host time and keeps it there. Every 30 seconds it asks the broker's time service for the host clock and steps the guest clock when it is off by more than half a second, because the guest clock stops while the host sleeps.
3. Sets the time zone to the host's, or to `timezone` when set. It points `/etc/localtime` at the zone in the image's `/usr/share/zoneinfo`, or copies the host's zone data into it when the image lacks that zone, and writes the zone name to `/etc/timezone`.
4. Sets up file ownership (see [Ownership of the boot root](#ownership-of-the-boot-root)).
5. Creates the `agent` user (UID 1000) when the image has none, and its home directory.
6. Mounts the shares, binds the snapshots (including the Git and agent configuration) and adds a forwarded CA bundle to the system trust store.
7. Adds the hostname to `/etc/hosts`.
8. With `enable_docker`, formats the Docker data disk (`docker_disk`, 20 GiB by default) on first use, mounts it at `/var/lib/docker`, grows its file system when `docker_disk` was raised and starts `dockerd` with the socket owned by the `agent` group.
9. Starts an SSH server that accepts only the workspace's key and runs every session as `agent`.

`jailoc-guest` leaves privileges alone by default, so the agent can use `sudo` from the image. Root in the VM is still inside the VM boundary.

### Ownership of the boot root

libkrun's virtio-fs shows the real host owner of every file. How `jailoc-guest` copes depends on the host:

- **Linux hosts.** It switches to idmapped mounts of the boot root and the shares.
    - Your user's files appear as root's in the boot root and as the agent's in the home and shared directories.
    - Files the agent creates belong to your user on the host.
    - The image layers keep the image's own owners and modes and are not idmapped.
- **macOS hosts, or when the guest kernel rejects idmapped mounts.** The boot root is not idmapped. A root-owned copy of jailoc's files in VM memory takes its place in the overlay, so that directories such as `/etc` keep the image's owners.

### protect_git

With `protect_git`, `jailoc-guest` makes the Git configuration of the repositories in the read-write `paths` and `mounts` immutable for the agent:

- bind-mounts `.git/hooks`, `.git/config` and the Git files they point to (`core.hooksPath`, `include.path`, `config.worktree`) read-only over themselves,
- turns the `.git` directories, the repositories and their parents into mount points that can't be renamed,
- sets `no_new_privs` on all its threads.

No session can then become root and undo the binds. Guest root could otherwise unmount them, which is why `protect_git` excludes `sudo` and `enable_docker` (Docker access is root access in the VM).

### Sessions

`jailoc` opens a session by connecting to the guest's SSH server through a Unix socket that the broker forwards into the VM. It authenticates with a per-workspace key and pins the guest's host key, which is generated for every start.

The agent runs in a session held by `jailoc-guest`: a small process inside the VM owns the agent's terminal, so the agent keeps running when the SSH connection ends. When a terminal re-attaches, the holder replays the terminal modes the agent had switched on (alternate screen, mouse reporting, bracketed paste, keyboard protocols) and makes the agent redraw. Sessions live in VM memory and end when the VM stops. See [Manage sessions](../how-to/sessions.md).

`jailoc down` asks the guest to stop over a control share, so open files are flushed before the VM powers off.

### Memory pressure

When the VM runs out of memory, the kernel's OOM killer ends the process with the highest score, which is mostly its memory use. The agent is usually the largest process, but the memory pressure mostly comes from what it starts: test runs, builds, containers. Losing the agent ends the session and its in-flight work, so `jailoc-guest`, which runs as root, sets `oom_score_adj` for session processes:

| Process | `oom_score_adj` |
|---------|-----------------|
| `jailoc-guest` init, session holders and attached `jailoc-guest` clients | -1000: never killed, so the VM keeps running and a session can be reattached |
| The session program (the agent) | -500: killed only when it uses half of the VM memory more than any other process |
| Processes the agent or `jailoc-guest` init starts outside a session | 0: the kernel default |

#### Why init is exempt

`jailoc-guest` init runs as the child of libkrun's `init.krun`, which powers the VM off when its child exits. Without the exemption, an OOM kill of init would stop the whole VM.

#### How the scores are applied

A forked process inherits its parent's `oom_score_adj`, and only root may lower it, while sessions run as `agent`. So `jailoc-guest` follows process starts through the kernel's proc connector and resets every process the agent or init forks right after the fork, as well as any other forked process that inherited init's exemption. The agent needs no changes.

- A child keeps its parent's value for the short time before `jailoc-guest` handles the event.
- Any program started in a session counts as an agent, including one the agent starts with `jailoc-guest session open`.

#### OOM kills in the console log

The kernel log lives in guest memory and is lost with the VM, so `jailoc-guest` copies every OOM kill to the console log as an `out of memory: kernel killed a process` line with the process, its PID, its anonymous memory and its `oom_score_adj`.

## Files

![Workspace paths, mounts, the internal guest/ and guest-ro/ directories and the image directory each reach the VM as one virtio-fs share: paths keep their host path, mounts appear at their guest path, the internal shares are mounted under a root-only directory and bound into place, and the image's EROFS layer files are the read-only lower layers of the overlay root](img/files.svg)

### Shares

Workspace `paths` and the directories in `mounts` reach the VM through virtio-fs, one share each. virtio-fs shares directories only, so a `mounts` entry that points at a single file isn't supported; use `snapshots` for files.

#### Internal shares

jailoc's own data uses two internal shares:

| Share | Access | Holds |
|-------|--------|-------|
| `guest` | Read-write | The control files the host and guest exchange |
| `guest-ro` | Read-only | Copies of the workspace configuration and cache directories and the staged snapshots, so guest root can't change them either |

The guest mounts both under a root-only directory and binds their parts into place. The persistent home directories and `~/.cache` are not in a share: they live on the `state.img` and `cache.img` virtio-blk disks (see [Persistent home](#persistent-home)). Because the guest can write the `guest` share, the host keeps nothing in it that it trusts, such as keys or the VM spec, and accesses it only in ways that cannot follow a link out of it. Few internal shares keep the number of virtio devices low; libkrun on x86_64 hosts can attach only about five shares besides them.

#### Confinement

- Read-only shares are enforced by the VMM: guest root can't remount them writable.
- libkrun's virtio-fs server itself does not keep a guest inside a share. The VM process therefore runs under Landlock on Linux and under a Seatbelt profile on macOS, with access only to the shares, the VM's root filesystem, data directory and disks, the image's EROFS layer files (read-only), and the runtime (see [Threat model](threat-model.md#the-boundary-is-the-vm)).

#### File ownership in shares

| Host | What the guest sees |
|------|---------------------|
| macOS | Simplified ownership: every guest process sees shared files as its own, and files it creates are owned by the host user. Permission bits stay meaningful on the host, and ownership changes inside the guest have no effect on host files. |
| Linux | The host owner and group of shared files. libkrun creates files as the host user that runs jailoc and allows it only for guest `root` or for a guest user whose uid and gid match that host user, so the `agent` user (uid and gid 1000) can create files in shares only when jailoc runs as uid and gid 1000. |

### Snapshots

Files and directories in `snapshots`, and the Git and agent configuration, aren't shared. jailoc copies them into the read-only internal share when the VM starts and on every `jailoc reload`, and the guest binds each item read-only at its destination.

- Each reload copies into a new directory and the guest replaces its binds. Processes that opened a file earlier keep reading the old copy.
- Nothing the workspace does reaches the host original.

### Persistent home

Only these survive a restart:

- the state directories of the workspace's coding agent (for OpenCode `~/.local/share/opencode` and `~/.local/state/opencode`, for Claude Code `~/.claude`),
- `~/.cache`.

They live on two sparse ext4 disks in `~/.cache/jailoc-vm/workspaces/<workspace>/` on the host, next to the Docker data disk of `enable_docker`:

- `state.img` (`state_disk`, 10 GiB by default) holds the agent's state directories.
- `cache.img` (`cache_disk`, 20 GiB by default) holds `~/.cache`.

`jailoc down --purge` deletes both. Sessions point the download caches of Go modules, npm, Bun and NuGet into `~/.cache` (see [Tool caches](../reference/configuration.md#tool-caches)), so they survive a restart and stay out of VM memory.

### Secrets

Secrets never touch the root file system or a share. jailoc reads them on the host, sends them over SSH after the VM starts, and `jailoc-guest` writes file secrets to `/run/secrets`, an in-memory file system readable only by `agent`, and env secrets to the in-memory session environment. See [Secrets](secrets.md).

## Network

The VM has exactly one network interface, and its only peer is the workspace's broker on the host. The broker runs a userspace TCP/IP stack (gVisor), serves DHCP, DNS and the host clock (SNTP) on the gateway, and checks every outgoing TCP connection and UDP flow against the workspace policy before it opens a host socket. See [Network Isolation](network-isolation.md).

The broker also carries a few host services:

| Service | What the broker does |
|---------|----------------------|
| SSH and Docker sockets | Carries them for jailoc |
| Host SSH agent (`ssh_auth_sock` on) | Exposes it in the guest as `/run/ssh-agent.sock`; keys never enter the VM |
| Host gpg-agent extra socket (`gpg_agent` on) | Exposes it as `/run/user/<uid>/gnupg/S.gpg-agent` |
| `ports` | Publishes guest ports on host `127.0.0.1` |
| `host_ports` | Relays guest connections on the gateway to host loopback ports or Unix sockets. `jailoc-guest` mirrors each entry on guest loopback and maps `host.jailoc.internal` and `host.docker.internal` to the gateway in `/etc/hosts`. |
| `proxy` routes | The background HTTPS proxy connects to a private Unix socket (`proxy.sock`); the broker relays it to guest NIC port 6081, and `jailoc-guest` forwards it to the route's loopback port. The relay runs in every workspace VM; its allowlist of route ports is replaced on `jailoc reload`. |

## Trade-offs

- **Start time:** the first start builds and exports the image. Later starts reuse the cache; a builder VM has to boot before it can check the cache.
- **Resources:** `cpu` and `memory` size the whole VM, including its Docker daemon. Nothing limits processes inside it. A build in the builder VM adds the builder's own CPU and memory. See [Resource use](resource-use.md).
- **Docker inside the VM:** with `enable_docker`, containers share the VM's CPU, memory and network rules.
- **Platform:** darwin/arm64, linux/amd64 and experimental linux/arm64 (on Linux with access to `/dev/kvm` and Landlock, Linux 5.19 or later). Release builds embed the go-microvm runtime for these platforms; other platforms and builds from source need a self-built runtime set through `[vm] runner` and `[vm] lib_dir`.
