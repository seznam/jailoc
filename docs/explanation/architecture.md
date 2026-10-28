# Architecture

Each workspace runs in its own libkrun micro-VM. This page describes the processes on the host, what happens inside the guest, how files and the network reach the VM, and how images are built.

```mermaid
flowchart LR
  subgraph host["Host"]
    jailoc["jailoc CLI"]
    sup["jailoc vm-supervise"]
    broker["jailoc netbroker<br/>userspace TCP/IP"]
    runner["go-microvm-runner<br/>(libkrun)"]
    cache["image cache"]
    dockerd["Docker daemon<br/>(host or builder VM)"]
  end
  subgraph vm["Workspace VM"]
    guest["jailoc-guest<br/>init + SSH server"]
    agent["opencode (UID 1000)"]
  end
  jailoc -- "1. build: Docker API" --> dockerd
  dockerd -- "2. export image" --> cache
  cache -- "3. root file system" --> vm
  jailoc -- "4. SSH over Unix socket" --> broker
  sup --> broker
  sup --> runner
  runner -- "virtio-net frames" --> broker
  broker -- "policy-checked egress" --> internet["Internet"]
  guest --> agent
```

## Host processes

Every VM is owned by a detached supervisor process (`jailoc vm-supervise`). It starts the workspace's network broker, boots the VM through the go-microvm runner, waits until the guest is ready, and from then on keeps the two together: when either the VM or the broker exits, it stops the other. A workspace never runs without its broker.

The VM runtime (runner, libkrun and the libkrunfw guest kernel) is a separate download. jailoc pins its URL and SHA-256 per platform and fetches it on first use, so the jailoc binary itself stays free of C dependencies. `jailoc-guest` is pure Go and is embedded in jailoc, so it always matches the jailoc version. Upgrading jailoc takes effect in a workspace after `jailoc restart`.

## Images

Workspace images are ordinary Docker images: the embedded Dockerfile, a `[base] dockerfile` preset and an optional per-workspace overlay (`dockerfile`, `build_context`). See [Image Resolution](../reference/image-resolution.md). The image needs no entrypoint, command, exposed port or firewall setup, because `jailoc-guest` is the init.

`[vm] builder` decides where the build runs: the host Docker daemon, or a builder VM. The builder VM runs a pinned `docker:dind` image with a persistent data disk, reached through a Unix socket that its broker forwards into the VM. The finished image is exported and unpacked into a local image cache, and a builder VM shuts down afterwards.

Building on the host daemon is fastest, but Dockerfile `RUN` steps then run with container isolation and unrestricted host networking. A builder VM keeps build steps behind a VM boundary. All workspaces share one builder VM, with one build cache, and it reaches public addresses only. With `isolated_build`, a workspace gets a builder of its own, with a separate data disk and the workspace's `allowed_hosts` and `allowed_networks`, so one workspace's build steps can't read another workspace's build cache or private network access. Builds on the same builder are serialized.

## Inside the guest

The root file system is a copy-on-write clone of the unpacked image and is discarded on every start. `jailoc-guest` prepares the system and then serves SSH:

1. configures the network addresses, the broker as gateway and DNS resolver, and the hostname,
2. on Linux hosts, switches to idmapped mounts of the root filesystem and the shares, because libkrun's virtio-fs shows the real host owner of every file there: your user's files appear as root's in the root filesystem and as the agent's in the home and shared directories, and files the agent creates belong to your user on the host,
3. creates the `agent` user (UID 1000) when the image has none, and its home directory,
4. mounts the shares, binds the snapshots (including the Git configuration) and adds a forwarded CA bundle to the system trust store,
5. adds the hostname to `/etc/hosts`,
6. with `enable_docker`, formats the Docker data disk (20 GiB) on first use, mounts it at `/var/lib/docker` and starts `dockerd` with the socket owned by the `agent` group,
7. starts an SSH server that accepts only the workspace's key and runs every session as `agent`.

`jailoc-guest` leaves privileges alone by default, so the agent can use `sudo` from the image. Root in the VM is still inside the VM boundary.

### protect_git

With `protect_git`, `jailoc-guest` bind-mounts `.git/hooks` and `.git/config` of every repository in the shares read-only over themselves, turns the `.git` directories, the repositories and their parents into mount points that can't be renamed, and sets `no_new_privs` on all its threads. No session can then become root and undo the binds. Guest root could otherwise unmount them, which is why `protect_git` excludes `sudo` and `enable_docker` (Docker access is root access in the VM).

### Sessions

`jailoc` opens a session by connecting to the guest's SSH server through a Unix socket that the broker forwards into the VM. It authenticates with a per-workspace key and pins the guest's host key, which is generated for every start. The agent runs in a session held by `jailoc-guest`: a small process inside the VM owns the agent's terminal, so the agent keeps running when the SSH connection ends. When a terminal re-attaches, the holder replays the terminal modes the agent had switched on (alternate screen, mouse reporting, bracketed paste, keyboard protocols) and makes the agent redraw. Sessions live in VM memory and end when the VM stops. See [Manage sessions](../how-to/sessions.md).

`jailoc down` asks the guest to stop over a control share, so open files are flushed before the VM powers off.

## Files

### Shares

Workspace `paths` and the directories in `mounts` reach the VM through virtio-fs, one share each. jailoc's own data uses a single internal share, the VM's `guest` directory on the host: copies of the workspace configuration and cache directories, the staged snapshots, the persistent home directories and the control files the host and guest exchange. The guest mounts it under a root-only directory and binds its parts into place (configuration copies read-only). Because the guest can write the whole internal share, the host keeps nothing in it that it trusts, such as keys or the VM spec, and accesses it only in ways that cannot follow a link out of it. One share keeps the number of virtio devices low; libkrun on x86_64 hosts can attach only about six shares besides it. Read-only shares are enforced by the VMM: guest root can't remount them writable. libkrun's virtio-fs server itself does not keep a guest inside a share, so on Linux the VM process runs under Landlock with access only to the shares, the VM's root filesystem, data directory and disks, and the runtime (see [Threat model](threat-model.md#the-boundary-is-the-vm)). virtio-fs shares directories only, so a `mounts` entry that points at a single file isn't supported; use `snapshots` for files.

On macOS, shares use simplified ownership: every guest process sees shared files as its own, and files it creates are owned by the host user. Permission bits stay meaningful on the host, and ownership changes inside the guest have no effect on host files. On Linux, the guest sees the host owner and group of shared files. libkrun creates files as the host user that runs jailoc and allows it only for guest `root` or for a guest user whose uid and gid match that host user, so the `agent` user (uid and gid 1000) can create files in shares only when jailoc runs as uid and gid 1000.

### Snapshots

Files and directories in `snapshots`, and the Git configuration, aren't shared. jailoc copies them into the internal share when the VM starts and on every `jailoc reload`, and the guest binds each item read-only at its destination. Each reload copies into a new directory and the guest replaces its binds; processes that opened a file earlier keep reading the old copy. Nothing the workspace does reaches the host original.

### Persistent home

Only `~/.local/share/opencode`, `~/.local/state/opencode` and `~/.cache` of the agent survive a restart. They live in `~/.cache/jailoc-vm/workspaces/<workspace>/guest/home` on the host, next to the Docker data disk of `enable_docker`. `jailoc down --purge` deletes them.

### Secrets

Secrets never touch the root file system or a share. jailoc reads them on the host, sends them over SSH after the VM starts, and `jailoc-guest` writes file secrets to `/run/secrets`, an in-memory file system readable only by `agent`, and env secrets to the in-memory session environment. See [Secrets](secrets.md).

## Network

The VM has exactly one network interface, and its only peer is the workspace's broker on the host. The broker runs a userspace TCP/IP stack (gVisor), serves DHCP and DNS on the gateway, and checks every outgoing TCP connection and UDP flow against the workspace policy before it opens a host socket. See [Network Isolation](network-isolation.md).

The broker also carries a few host services: the SSH and Docker sockets for jailoc, the host SSH agent when `ssh_auth_sock` is on (exposed in the guest as `/run/ssh-agent.sock`, keys never enter the VM), and `ports`, which publish guest ports on host `127.0.0.1`.

## Trade-offs

- **Start time:** the first start builds and exports the image. Later starts reuse the cache; a builder VM has to boot before it can check the cache.
- **Resources:** `cpu` and `memory` size the whole VM, including its Docker daemon. Nothing limits processes inside it. A build in the builder VM adds the builder's own CPU and memory.
- **Docker inside the VM:** with `enable_docker`, containers share the VM's CPU, memory and network rules.
- **Platform:** darwin/arm64 and linux/amd64 (with access to `/dev/kvm` and Landlock, Linux 5.19 or later). Other platforms need a self-built go-microvm runtime set through `[vm] runner` and `[vm] lib_dir`.
